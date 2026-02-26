package db

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
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

// CreateSecret encrypts and stores a new secret.
func (db *DB) CreateSecret(accountID, name, provider, plaintext, masterKey string) (*Secret, error) {
	encrypted, err := encrypt(plaintext, masterKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt secret: %w", err)
	}

	id := generateID()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err = db.Exec(`
		INSERT INTO secrets (id, account_id, name, provider, encrypted_value, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, id, accountID, name, provider, encrypted, now, now)
	if err != nil {
		return nil, fmt.Errorf("insert secret: %w", err)
	}

	return &Secret{
		ID:             id,
		AccountID:      accountID,
		Name:           name,
		Provider:        provider,
		EncryptedValue: encrypted,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// GetSecret retrieves and decrypts a secret by name for an account.
func (db *DB) GetSecret(accountID, name, masterKey string) (string, error) {
	var encrypted string
	err := db.QueryRow(`
		SELECT encrypted_value FROM secrets
		WHERE account_id = ? AND name = ?
	`, accountID, name).Scan(&encrypted)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("secret %q not found", name)
	}
	if err != nil {
		return "", fmt.Errorf("query secret: %w", err)
	}

	plaintext, err := decrypt(encrypted, masterKey)
	if err != nil {
		return "", fmt.Errorf("decrypt secret: %w", err)
	}
	return plaintext, nil
}

// GetSecretsByPrefix retrieves all secrets matching a name prefix (for round-robin).
// Returns decrypted values.
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
			return nil, err
		}
		plain, err := decrypt(encrypted, masterKey)
		if err != nil {
			return nil, fmt.Errorf("decrypt secret: %w", err)
		}
		values = append(values, plain)
	}
	return values, rows.Err()
}

// ListSecrets returns all secret metadata (without decrypting) for an account.
func (db *DB) ListSecrets(accountID string) ([]Secret, error) {
	rows, err := db.Query(`
		SELECT id, account_id, name, provider, encrypted_value, metadata, created_at, updated_at
		FROM secrets WHERE account_id = ? ORDER BY name
	`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list secrets: %w", err)
	}
	defer rows.Close()

	var secrets []Secret
	for rows.Next() {
		var s Secret
		if err := rows.Scan(&s.ID, &s.AccountID, &s.Name, &s.Provider, &s.EncryptedValue, &s.Metadata, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		secrets = append(secrets, s)
	}
	return secrets, rows.Err()
}

// DeleteSecret removes a secret by name.
func (db *DB) DeleteSecret(accountID, name string) error {
	result, err := db.Exec(`DELETE FROM secrets WHERE account_id = ? AND name = ?`, accountID, name)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("secret %q not found", name)
	}
	return nil
}

// GrantSecretAccess allows an agent to access a specific secret.
func (db *DB) GrantSecretAccess(secretID, agentID string) error {
	id := generateID()
	_, err := db.Exec(`
		INSERT OR IGNORE INTO secret_access (id, secret_id, agent_id)
		VALUES (?, ?, ?)
	`, id, secretID, agentID)
	return err
}

// AES-256-GCM encryption/decryption using the master key.

func encrypt(plaintext, masterKeyHex string) (string, error) {
	key, err := hex.DecodeString(masterKeyHex)
	if err != nil {
		return "", fmt.Errorf("decode master key: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return hex.EncodeToString(ciphertext), nil
}

func decrypt(ciphertextHex, masterKeyHex string) (string, error) {
	key, err := hex.DecodeString(masterKeyHex)
	if err != nil {
		return "", fmt.Errorf("decode master key: %w", err)
	}

	ciphertext, err := hex.DecodeString(ciphertextHex)
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create gcm: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}

	return string(plaintext), nil
}
