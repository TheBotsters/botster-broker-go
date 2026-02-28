package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Account represents a multi-tenant root entity.
type Account struct {
	ID           string
	Email        string
	PasswordHash string
	CreatedAt    string
}

// CreateAccount creates a new account with a hashed password.
func (db *DB) CreateAccount(email, password string) (*Account, error) {
	id := generateID()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = db.Exec(`
		INSERT INTO accounts (id, email, password_hash, created_at)
		VALUES (?, ?, ?, ?)
	`, id, email, string(hash), now)
	if err != nil {
		return nil, fmt.Errorf("insert account: %w", err)
	}

	return &Account{ID: id, Email: email, PasswordHash: string(hash), CreatedAt: now}, nil
}

// GetAccountByEmail looks up an account by email.
func (db *DB) GetAccountByEmail(email string) (*Account, error) {
	a := &Account{}
	err := db.QueryRow(`
		SELECT id, email, password_hash, created_at FROM accounts WHERE email = ?
	`, email).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query account: %w", err)
	}
	return a, nil
}

// GetAccountByID looks up an account by ID.
func (db *DB) GetAccountByID(id string) (*Account, error) {
	a := &Account{}
	err := db.QueryRow(`
		SELECT id, email, password_hash, created_at FROM accounts WHERE id = ?
	`, id).Scan(&a.ID, &a.Email, &a.PasswordHash, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query account: %w", err)
	}
	return a, nil
}

// VerifyPassword checks a plaintext password against an account's hash.
func (a *Account) VerifyPassword(password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(a.PasswordHash), []byte(password)) == nil
}

// GenerateSessionToken creates a random session token for web UI auth.
func GenerateSessionToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ListAccounts returns all accounts.
func (db *DB) ListAccounts() ([]*Account, error) {
	rows, err := db.Query(`SELECT id, email, password_hash, created_at FROM accounts ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("query accounts: %w", err)
	}
	defer rows.Close()

	var accounts []*Account
	for rows.Next() {
		a := &Account{}
		if err := rows.Scan(&a.ID, &a.Email, &a.PasswordHash, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

// UpdateAccount updates mutable account fields. Only non-nil keys in updates are modified.
func (db *DB) UpdateAccount(id string, updates map[string]interface{}) error {
	if len(updates) == 0 {
		return nil
	}
	setClauses := ""
	args := []interface{}{}
	for k, v := range updates {
		if setClauses != "" {
			setClauses += ", "
		}
		setClauses += k + " = ?"
		args = append(args, v)
	}
	args = append(args, id)
	_, err := db.Exec("UPDATE accounts SET "+setClauses+" WHERE id = ?", args...)
	if err != nil {
		return fmt.Errorf("update account: %w", err)
	}
	return nil
}

// DeleteAccount removes an account and all cascaded data.
func (db *DB) DeleteAccount(id string) error {
	_, err := db.Exec(`DELETE FROM accounts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	return nil
}
