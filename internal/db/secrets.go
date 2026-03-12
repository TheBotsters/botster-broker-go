package db

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log"
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
	if len(key) != 32 {
		return nil, fmt.Errorf("key must be 32 bytes (got %d)", len(key))
	}

	combined, err := decodeCiphertext(encodedCiphertext)
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

	// Format (Node + Go): nonce/iv (12) + ciphertext + authTag (16), then encoded.
	nonceSize := gcm.NonceSize()
	tagSize := gcm.Overhead()
	if len(combined) < nonceSize+tagSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, sealed := combined[:nonceSize], combined[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plaintext, nil
}

// DecryptToken decrypts an AES-256-GCM encrypted token using the given hex master key.
func DecryptToken(encryptedToken, masterKey string) (string, error) {
	plaintext, err := decrypt(encryptedToken, masterKey)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
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
//
// This is account-scoped only and does not apply per-agent ACL checks.
// Prefer GetSecretForAgent for agent-authenticated API paths.
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
		// Sanitize error to avoid leaking decryption details
		return "", fmt.Errorf("secret %q: access denied", name)
	}
	return string(plaintext), nil
}

// GetSecretForAgent retrieves and decrypts a secret with agent-level authorization.
//
// Policy:
//   - Secret must belong to the provided accountID.
//   - If no ACL entries exist for the secret, allow any agent in the account (backward compatible default).
//   - If one or more ACL entries exist, allow only agents explicitly granted in secret_access.
func (db *DB) GetSecretForAgent(accountID, agentID, name, masterKey string) (string, error) {
	var agentCount int
	if err := db.QueryRow(`SELECT count(*) FROM agents WHERE id = ? AND account_id = ?`, agentID, accountID).Scan(&agentCount); err != nil {
		return "", fmt.Errorf("query agent scope: %w", err)
	}
	if agentCount == 0 {
		log.Printf("[secret_acl] deny agent scope: account_id=%s agent_id=%s secret_name=%s", accountID, agentID, name)
		return "", fmt.Errorf("secret %q: access denied", name)
	}

	var (
		secretID  string
		encrypted string
	)
	err := db.QueryRow(`
		SELECT id, encrypted_value FROM secrets WHERE account_id = ? AND name = ?
	`, accountID, name).Scan(&secretID, &encrypted)
	if err == sql.ErrNoRows {
		log.Printf("[secret_acl] secret not found: account_id=%s agent_id=%s secret_name=%s", accountID, agentID, name)
		return "", fmt.Errorf("secret %q not found", name)
	}
	if err != nil {
		return "", fmt.Errorf("query secret: %w", err)
	}

	var aclCount int
	if err := db.QueryRow(`SELECT count(*) FROM secret_access WHERE secret_id = ?`, secretID).Scan(&aclCount); err != nil {
		return "", fmt.Errorf("query secret acl: %w", err)
	}

	if aclCount > 0 {
		var grantedCount int
		if err := db.QueryRow(`SELECT count(*) FROM secret_access WHERE secret_id = ? AND agent_id = ?`, secretID, agentID).Scan(&grantedCount); err != nil {
			return "", fmt.Errorf("query secret acl grant: %w", err)
		}
		if grantedCount == 0 {
			log.Printf("[secret_acl] deny acl: account_id=%s secret_id=%s secret_name=%s agent_id=%s acl_count=%d granted_count=%d", accountID, secretID, name, agentID, aclCount, grantedCount)
			return "", fmt.Errorf("secret %q: access denied", name)
		}
		log.Printf("[secret_acl] allow acl grant: account_id=%s secret_id=%s secret_name=%s agent_id=%s acl_count=%d", accountID, secretID, name, agentID, aclCount)
	} else {
		log.Printf("[secret_acl] allow default account scope (no acl rows): account_id=%s secret_id=%s secret_name=%s agent_id=%s", accountID, secretID, name, agentID)
	}

	plaintext, err := decrypt(encrypted, masterKey)
	if err != nil {
		log.Printf("[secret_acl] decrypt failed after ACL allow: account_id=%s secret_id=%s secret_name=%s agent_id=%s err=%v", accountID, secretID, name, agentID, err)
		// Sanitize error to avoid leaking decryption details
		return "", fmt.Errorf("secret %q: access denied", name)
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
			// Sanitize error to avoid leaking decryption details
			return nil, fmt.Errorf("decryption failed for one or more secrets")
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
func (db *DB) AgentHasSecretAccess(agentID, secretName string) (bool, error) {
	var count int
	err := db.QueryRow(`
		SELECT count(*) FROM secret_access sa
		JOIN secrets s ON sa.secret_id = s.id
		WHERE sa.agent_id = ? AND s.name = ?
	`, agentID, secretName).Scan(&count)
	return count > 0, err
}

// ListSecretGrantAgentNames returns sorted agent names granted to a secret.
func (db *DB) ListSecretGrantAgentNames(secretID string) ([]string, error) {
	rows, err := db.Query(`
		SELECT a.name
		FROM secret_access sa
		JOIN agents a ON a.id = sa.agent_id
		WHERE sa.secret_id = ?
		ORDER BY a.name
	`, secretID)
	if err != nil {
		return nil, fmt.Errorf("query secret grants: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan secret grant: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
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

// GetSecretByID retrieves a secret by its ID.
func (db *DB) GetSecretByID(id string) (*Secret, error) {
	s := &Secret{}
	err := db.QueryRow(`
		SELECT id, account_id, name, provider, encrypted_value, metadata, created_at, updated_at
		FROM secrets WHERE id = ?
	`, id).Scan(&s.ID, &s.AccountID, &s.Name, &s.Provider, &s.EncryptedValue, &s.Metadata, &s.CreatedAt, &s.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query secret by id: %w", err)
	}
	return s, nil
}

// UpdateSecretByID updates a secret's encrypted value by ID.
func (db *DB) UpdateSecretByID(id, newValue, masterKey string) error {
	encrypted, err := encrypt([]byte(newValue), masterKey)
	if err != nil {
		return fmt.Errorf("encrypt secret: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := db.Exec(`UPDATE secrets SET encrypted_value = ?, updated_at = ? WHERE id = ?`, encrypted, now, id)
	if err != nil {
		return fmt.Errorf("update secret by id: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("secret %q not found", id)
	}
	return nil
}

// ExportSecret decrypts a stored secret and re-encrypts with transitKey.
// SECURITY: plaintext exists only in RAM during this call; do not log.
func (db *DB) ExportSecret(s *Secret, masterKey, transitKey string) (string, error) {
	plaintext, err := decrypt(s.EncryptedValue, masterKey)
	if err != nil {
		// Sanitize error to avoid leaking decryption details
		return "", fmt.Errorf("export failed")
	}
	return encrypt(plaintext, transitKey)
}

// ImportSecret decrypts a transit-encrypted value and re-encrypts with masterKey.
// SECURITY: plaintext exists only in RAM during this call; do not log.
func (db *DB) ImportSecret(transitEncrypted, transitKey, masterKey string) (string, error) {
	plaintext, err := decrypt(transitEncrypted, transitKey)
	if err != nil {
		// Sanitize error to avoid leaking decryption details
		return "", fmt.Errorf("import failed")
	}
	return encrypt(plaintext, masterKey)
}

// ChecksumSecret computes a checksum for a secret.
// This is used in sync manifests to detect changes.
// SECURITY: Does NOT decrypt the secret; uses encrypted value + metadata.
func (db *DB) ChecksumSecret(secret *Secret, masterKey string) (string, error) {
	// Compute checksum on encrypted value + metadata + timestamps
	// This ensures changes are detected without exposing plaintext
	data := fmt.Sprintf("%s|%s|%s|%s|%s",
		secret.EncryptedValue,
		secret.Metadata.String,
		secret.Name,
		secret.Provider,
		secret.UpdatedAt)

	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:]), nil
}

// CreateOrUpdateSecret creates a new secret or updates an existing one by name.
// Returns the secret and a boolean indicating if it was created (true) or updated (false).
func (db *DB) CreateOrUpdateSecret(accountID, name, provider, value, masterKey string) (*Secret, bool, error) {
	// Check if secret already exists
	secrets, err := db.ListSecrets(accountID)
	if err != nil {
		return nil, false, fmt.Errorf("list secrets: %w", err)
	}

	var existingSecret *Secret
	for _, s := range secrets {
		if s.Name == name && s.Provider == provider {
			existingSecret = s
			break
		}
	}

	if existingSecret != nil {
		// Update existing secret
		err = db.UpdateSecretByID(existingSecret.ID, value, masterKey)
		if err != nil {
			return nil, false, fmt.Errorf("update secret: %w", err)
		}
		// Return the updated secret (need to fetch it again)
		secret, err := db.GetSecretByID(existingSecret.ID)
		return secret, false, err
	} else {
		// Create new secret
		secret, err := db.CreateSecret(accountID, name, provider, value, masterKey)
		return secret, true, err
	}
}
