package db

import (
	"database/sql"
	"time"
)

// Capability binds an agent-visible name to a provider and a secret.
type Capability struct {
	ID          string `json:"id"`
	AccountID   string `json:"account_id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	ProviderID  string `json:"provider_id"`
	SecretID    string `json:"secret_id"`
	CreatedAt   string `json:"created_at"`
}

// CapabilityWithProvider is a Capability joined with its provider info.
// This is what the proxy handler and agent-facing API need.
type CapabilityWithProvider struct {
	Capability
	ProviderName string `json:"provider_name"`
	BaseURL      string `json:"base_url"`
	AuthType     string `json:"auth_type"`
	AuthHeader   string `json:"auth_header"`
	SecretName   string `json:"-"` // internal — never returned to agents
}

// CreateCapability creates a capability binding.
func (db *DB) CreateCapability(accountID, name, displayName, providerID, secretID string) (*Capability, error) {
	id := generateID()
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	_, err := db.Exec(`
		INSERT INTO capabilities (id, account_id, name, display_name, provider_id, secret_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, id, accountID, name, displayName, providerID, secretID, now)
	if err != nil {
		return nil, err
	}
	return &Capability{
		ID: id, AccountID: accountID, Name: name, DisplayName: displayName,
		ProviderID: providerID, SecretID: secretID, CreatedAt: now,
	}, nil
}

// ListCapabilities returns all capabilities for an account.
func (db *DB) ListCapabilities(accountID string) ([]Capability, error) {
	rows, err := db.Query(`
		SELECT id, account_id, name, display_name, provider_id, secret_id, created_at
		FROM capabilities WHERE account_id = ? ORDER BY name
	`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var caps []Capability
	for rows.Next() {
		var c Capability
		if err := rows.Scan(&c.ID, &c.AccountID, &c.Name, &c.DisplayName,
			&c.ProviderID, &c.SecretID, &c.CreatedAt); err != nil {
			return nil, err
		}
		caps = append(caps, c)
	}
	return caps, nil
}

// GetCapabilityByName returns a capability by account and name.
func (db *DB) GetCapabilityByName(accountID, name string) (*CapabilityWithProvider, error) {
	var c CapabilityWithProvider
	err := db.QueryRow(`
		SELECT c.id, c.account_id, c.name, c.display_name, c.provider_id, c.secret_id, c.created_at,
		       p.name, p.base_url, p.auth_type, p.auth_header,
		       s.name
		FROM capabilities c
		JOIN providers p ON c.provider_id = p.id
		JOIN secrets s ON c.secret_id = s.id
		WHERE c.account_id = ? AND c.name = ?
	`, accountID, name).Scan(
		&c.ID, &c.AccountID, &c.Name, &c.DisplayName, &c.ProviderID, &c.SecretID, &c.CreatedAt,
		&c.ProviderName, &c.BaseURL, &c.AuthType, &c.AuthHeader,
		&c.SecretName,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// GetCapabilityByID returns a capability by ID.
func (db *DB) GetCapabilityByID(id string) (*Capability, error) {
	var c Capability
	err := db.QueryRow(`
		SELECT id, account_id, name, display_name, provider_id, secret_id, created_at
		FROM capabilities WHERE id = ?
	`, id).Scan(&c.ID, &c.AccountID, &c.Name, &c.DisplayName,
		&c.ProviderID, &c.SecretID, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// UpdateCapability updates a capability's mutable fields.
func (db *DB) UpdateCapability(id, displayName, providerID, secretID string) error {
	_, err := db.Exec(`
		UPDATE capabilities SET display_name = ?, provider_id = ?, secret_id = ?
		WHERE id = ?
	`, displayName, providerID, secretID, id)
	return err
}

// DeleteCapability removes a capability and its grants (CASCADE).
func (db *DB) DeleteCapability(id string) error {
	_, err := db.Exec(`DELETE FROM capabilities WHERE id = ?`, id)
	return err
}

// GrantCapability grants a capability to an agent.
func (db *DB) GrantCapability(capabilityID, agentID string) error {
	id := generateID()
	_, err := db.Exec(`
		INSERT OR IGNORE INTO capability_grants (id, capability_id, agent_id) VALUES (?, ?, ?)
	`, id, capabilityID, agentID)
	return err
}

// RevokeCapability revokes a capability from an agent.
func (db *DB) RevokeCapability(capabilityID, agentID string) error {
	_, err := db.Exec(`
		DELETE FROM capability_grants WHERE capability_id = ? AND agent_id = ?
	`, capabilityID, agentID)
	return err
}

// AgentHasCapability checks if an agent has been granted a specific capability.
func (db *DB) AgentHasCapability(capabilityID, agentID string) (bool, error) {
	var count int
	err := db.QueryRow(`
		SELECT COUNT(*) FROM capability_grants WHERE capability_id = ? AND agent_id = ?
	`, capabilityID, agentID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// ListAgentCapabilities returns all capabilities granted to an agent, with provider info.
func (db *DB) ListAgentCapabilities(accountID, agentID string) ([]CapabilityWithProvider, error) {
	rows, err := db.Query(`
		SELECT c.id, c.account_id, c.name, c.display_name, c.provider_id, c.secret_id, c.created_at,
		       p.name, p.base_url, p.auth_type, p.auth_header,
		       s.name
		FROM capabilities c
		JOIN capability_grants cg ON c.id = cg.capability_id
		JOIN providers p ON c.provider_id = p.id
		JOIN secrets s ON c.secret_id = s.id
		WHERE c.account_id = ? AND cg.agent_id = ?
		ORDER BY c.name
	`, accountID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var caps []CapabilityWithProvider
	for rows.Next() {
		var c CapabilityWithProvider
		if err := rows.Scan(
			&c.ID, &c.AccountID, &c.Name, &c.DisplayName, &c.ProviderID, &c.SecretID, &c.CreatedAt,
			&c.ProviderName, &c.BaseURL, &c.AuthType, &c.AuthHeader,
			&c.SecretName,
		); err != nil {
			return nil, err
		}
		caps = append(caps, c)
	}
	return caps, nil
}
