package db

import (
	"database/sql"
	"time"
)

// Provider represents a configured external service provider.
type Provider struct {
	ID          string `json:"id"`
	AccountID   string `json:"account_id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	BaseURL     string `json:"base_url"`
	AuthType    string `json:"auth_type"`   // "bearer", "basic", "header"
	AuthHeader  string `json:"auth_header"` // header name (default "Authorization")
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// CreateProvider creates a new provider for an account.
func (db *DB) CreateProvider(accountID, name, displayName, baseURL, authType, authHeader string) (*Provider, error) {
	id := generateID()
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	_, err := db.Exec(`
		INSERT INTO providers (id, account_id, name, display_name, base_url, auth_type, auth_header, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, accountID, name, displayName, baseURL, authType, authHeader, now, now)
	if err != nil {
		return nil, err
	}
	return &Provider{
		ID: id, AccountID: accountID, Name: name, DisplayName: displayName,
		BaseURL: baseURL, AuthType: authType, AuthHeader: authHeader,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

// ListProviders returns all providers for an account.
func (db *DB) ListProviders(accountID string) ([]Provider, error) {
	rows, err := db.Query(`
		SELECT id, account_id, name, display_name, base_url, auth_type, auth_header, created_at, updated_at
		FROM providers WHERE account_id = ? ORDER BY name
	`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var providers []Provider
	for rows.Next() {
		var p Provider
		if err := rows.Scan(&p.ID, &p.AccountID, &p.Name, &p.DisplayName, &p.BaseURL,
			&p.AuthType, &p.AuthHeader, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		providers = append(providers, p)
	}
	return providers, nil
}

// GetProviderByName returns a provider by account and name.
func (db *DB) GetProviderByName(accountID, name string) (*Provider, error) {
	var p Provider
	err := db.QueryRow(`
		SELECT id, account_id, name, display_name, base_url, auth_type, auth_header, created_at, updated_at
		FROM providers WHERE account_id = ? AND name = ?
	`, accountID, name).Scan(&p.ID, &p.AccountID, &p.Name, &p.DisplayName, &p.BaseURL,
		&p.AuthType, &p.AuthHeader, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetProviderByID returns a provider by ID.
func (db *DB) GetProviderByID(id string) (*Provider, error) {
	var p Provider
	err := db.QueryRow(`
		SELECT id, account_id, name, display_name, base_url, auth_type, auth_header, created_at, updated_at
		FROM providers WHERE id = ?
	`, id).Scan(&p.ID, &p.AccountID, &p.Name, &p.DisplayName, &p.BaseURL,
		&p.AuthType, &p.AuthHeader, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// UpdateProvider updates a provider's mutable fields.
func (db *DB) UpdateProvider(id, displayName, baseURL, authType, authHeader string) error {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	_, err := db.Exec(`
		UPDATE providers SET display_name = ?, base_url = ?, auth_type = ?, auth_header = ?, updated_at = ?
		WHERE id = ?
	`, displayName, baseURL, authType, authHeader, now, id)
	return err
}

// DeleteProvider removes a provider.
func (db *DB) DeleteProvider(id string) error {
	_, err := db.Exec(`DELETE FROM providers WHERE id = ?`, id)
	return err
}
