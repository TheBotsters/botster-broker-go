package db

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"time"
)

// Secret represents an encrypted secret in the vault.
type Secret struct {
	ID             string
	AccountID      string
	Name           string
	Provider       string
	EncryptedValue string
	Metadata       sql.NullString
	CreatedAt      string
	UpdatedAt      string
}

// decodeCiphertext tries hex first, then base64.
// The TypeScript broker uses base64; Go broker uses hex for new secrets.
func decodeCiphertext(encoded string) ([]byte, error) {
	// Try hex first
	if data, err := hex.DecodeString(encoded); err == nil {
		return data, nil
	}
	// Try base64
	if data, err := base64.StdEncoding.DecodeString(encoded); err == nil {
		return data, nil
	}
	// Try base64 URL-safe variant
	if data, err := base64.URLEncoding.DecodeString(encoded); err == nil {
		return data, nil
	}
	// Try base64 raw (no padding)
	if data, err := base64.RawStdEncoding.DecodeString(encoded); err == nil {
		return data, nil
	}
	return nil, fmt.Errorf("cannot decode ciphertext: not hex or base64")
}

// encrypt encrypts plaintext using AES-256-GCM with the given hex key.
// Output is hex-encoded (Go broker convention).
func encrypt(plaintext []byte, hexKey string) (string, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return "", fmt.Errorf("decode key: %w", err)
	}
	if len(key) != 32 {
		return "", fmt.Errorf("key must be 32 bytes (got %d)", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("new cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("new gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return hex.EncodeToString(ciphertext), nil
}

// decrypt decrypts an AES-256-GCM ciphertext (hex or base64 encoded).
func decrypt(encodedCiphertext, hexKey string) ([]byte, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("decode key: %w", err)
	}

	ciphertext, err := decodeCiphertext(encodedCiphertext)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plaintext, nil
}

// CreateSecret stores an encrypted secret.
func (db *DB) CreateSecret(accountID, name, provider, value, masterKey string) (*Secret, error) {
	id := generateID()
	encrypted, err := encrypt([]byte(value), masterKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt secret: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = db.Exec(`
		INSERT INTO secrets (id, account_id, name, provider, encrypted_value, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, id, accountID, name, provider, encrypted, now, now)
	if err != nil {
		return nil, fmt.Errorf("insert secret: %w", err)
	}

	return &Secret{
		ID: id, AccountID: accountID, Name: name, Provider: provider,
		EncryptedValue: encrypted, CreatedAt: now, UpdatedAt: now,
	}, nil
}

// GetSecret retrieves and decrypts a secret by account and name.
func (db *DB) GetSecret(accountID, name, masterKey string) (string, error) {
	var encrypted string
	err := db.QueryRow(`
		SELECT encrypted_value FROM secrets WHERE account_id = ? AND name = ?
	`, accountID, name).Scan(&encrypted)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("secret %q not found", name)
	}
	if err != nil {
		return "", fmt.Errorf("query secret: %w", err)
	}

	plaintext, err := decrypt(encrypted, masterKey)
	if err != nil {
		return "", fmt.Errorf("decrypt secret %q: %w", name, err)
	}
	return string(plaintext), nil
}

// GetSecretsByPrefix retrieves all secrets matching a name prefix (for round-robin).
func (db *DB) GetSecretsByPrefix(accountID, prefix, masterKey string) ([]string, error) {
	rows, err := db.Query(`
		SELECT encrypted_value FROM secrets
		WHERE account_id = ? AND name LIKE ?
		ORDER BY name
	`, accountID, prefix+"%")
	if err != nil {
		return nil, fmt.Errorf("query secrets by prefix: %w", err)
	}
	defer rows.Close()

	var values []string
	for rows.Next() {
		var encrypted string
		if err := rows.Scan(&encrypted); err != nil {
			return nil, fmt.Errorf("scan secret: %w", err)
		}
		plaintext, err := decrypt(encrypted, masterKey)
		if err != nil {
			return nil, fmt.Errorf("decrypt: %w", err)
		}
		values = append(values, string(plaintext))
	}
	return values, rows.Err()
}

// ListSecrets returns metadata (no values) for all secrets in an account.
func (db *DB) ListSecrets(accountID string) ([]*Secret, error) {
	rows, err := db.Query(`
		SELECT id, account_id, name, provider, encrypted_value, metadata, created_at, updated_at
		FROM secrets WHERE account_id = ? ORDER BY name
	`, accountID)
	if err != nil {
		return nil, fmt.Errorf("query secrets: %w", err)
	}
	defer rows.Close()

	var secrets []*Secret
	for rows.Next() {
		s := &Secret{}
		if err := rows.Scan(&s.ID, &s.AccountID, &s.Name, &s.Provider, &s.EncryptedValue, &s.Metadata, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan secret: %w", err)
		}
		secrets = append(secrets, s)
	}
	return secrets, rows.Err()
}

// DeleteSecret removes a secret by ID.
func (db *DB) DeleteSecret(id string) error {
	_, err := db.Exec(`DELETE FROM secrets WHERE id = ?`, id)
	return err
}

// GrantSecretAccess gives an agent access to a secret.
func (db *DB) GrantSecretAccess(secretID, agentID string) error {
	id := generateID()
	_, err := db.Exec(`
		INSERT OR IGNORE INTO secret_access (id, secret_id, agent_id) VALUES (?, ?, ?)
	`, id, secretID, agentID)
	return err
}

// RevokeSecretAccess removes an agent's access to a secret.
func (db *DB) RevokeSecretAccess(secretID, agentID string) error {
	_, err := db.Exec(`DELETE FROM secret_access WHERE secret_id = ? AND agent_id = ?`, secretID, agentID)
	return err
}

// AgentHasSecretAccess checks if an agent can access a specific secret.
func (db *DB) AgentHasSecretAccess(agentID, secretName string) (bool, error) {
	var count int
	err := db.QueryRow(`
		SELECT count(*) FROM secret_access sa
		JOIN secrets s ON sa.secret_id = s.id
		WHERE sa.agent_id = ? AND s.name = ?
	`, agentID, secretName).Scan(&count)
	return count > 0, err
}

// UpdateSecret updates an existing secret's encrypted value.
// Used to persist refreshed OAuth tokens.
func (db *DB) UpdateSecret(accountID, name, newValue, masterKey string) error {
	encrypted, err := encrypt([]byte(newValue), masterKey)
	if err != nil {
		return fmt.Errorf("encrypt secret: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	result, err := db.Exec(`
		UPDATE secrets SET encrypted_value = ?, updated_at = ?
		WHERE account_id = ? AND name = ?
	`, encrypted, now, accountID, name)
	if err != nil {
		return fmt.Errorf("update secret: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("secret %q not found for account %q", name, accountID)
	}
	return nil
}
