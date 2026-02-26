package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/siofra-seksbot/botster-broker-go/internal/auth"
)

// Actuator represents a registered actuator endpoint.
type Actuator struct {
	ID             string
	AccountID      string
	Name           string
	Type           string
	Status         string
	TokenHash      sql.NullString
	EncryptedToken sql.NullString
	Enabled        bool
	LastSeenAt     sql.NullString
	CreatedAt      string
}

// CreateActuator registers a new actuator and returns it with its plaintext token.
func (db *DB) CreateActuator(accountID, name, actType string) (*Actuator, string, error) {
	id := generateID()
	token, err := auth.GenerateToken("seks_actuator")
	if err != nil {
		return nil, "", fmt.Errorf("generate token: %w", err)
	}
	tokenHash := auth.HashToken(token)

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = db.Exec(`
		INSERT INTO actuators (id, account_id, name, type, token_hash, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, id, accountID, name, actType, tokenHash, now)
	if err != nil {
		return nil, "", fmt.Errorf("insert actuator: %w", err)
	}

	act := &Actuator{
		ID:        id,
		AccountID: accountID,
		Name:      name,
		Type:      actType,
		Status:    "offline",
		TokenHash: sql.NullString{String: tokenHash, Valid: true},
		Enabled:   true,
		CreatedAt: now,
	}
	return act, token, nil
}

// GetActuatorByToken looks up an actuator by its plaintext token.
func (db *DB) GetActuatorByToken(token string) (*Actuator, error) {
	hash := auth.HashToken(token)
	a := &Actuator{}
	var enabled int
	err := db.QueryRow(`
		SELECT id, account_id, name, type, status, token_hash, encrypted_token, enabled, last_seen_at, created_at
		FROM actuators WHERE token_hash = ?
	`, hash).Scan(&a.ID, &a.AccountID, &a.Name, &a.Type, &a.Status, &a.TokenHash, &a.EncryptedToken, &enabled, &a.LastSeenAt, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query actuator by token: %w", err)
	}
	a.Enabled = enabled != 0
	return a, nil
}

// GetActuatorByID retrieves an actuator by its ID.
func (db *DB) GetActuatorByID(id string) (*Actuator, error) {
	a := &Actuator{}
	var enabled int
	err := db.QueryRow(`
		SELECT id, account_id, name, type, status, token_hash, encrypted_token, enabled, last_seen_at, created_at
		FROM actuators WHERE id = ?
	`, id).Scan(&a.ID, &a.AccountID, &a.Name, &a.Type, &a.Status, &a.TokenHash, &a.EncryptedToken, &enabled, &a.LastSeenAt, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query actuator by id: %w", err)
	}
	a.Enabled = enabled != 0
	return a, nil
}

// ListActuators returns all actuators for an account.
func (db *DB) ListActuators(accountID string) ([]Actuator, error) {
	rows, err := db.Query(`
		SELECT id, account_id, name, type, status, token_hash, encrypted_token, enabled, last_seen_at, created_at
		FROM actuators WHERE account_id = ? ORDER BY name
	`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list actuators: %w", err)
	}
	defer rows.Close()

	var actuators []Actuator
	for rows.Next() {
		var a Actuator
		var enabled int
		if err := rows.Scan(&a.ID, &a.AccountID, &a.Name, &a.Type, &a.Status, &a.TokenHash, &a.EncryptedToken, &enabled, &a.LastSeenAt, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan actuator: %w", err)
		}
		a.Enabled = enabled != 0
		actuators = append(actuators, a)
	}
	return actuators, rows.Err()
}

// IsActuatorAssignedToAgent checks if an actuator is assigned and enabled for an agent.
func (db *DB) IsActuatorAssignedToAgent(agentID, actuatorID string) (bool, error) {
	var count int
	err := db.QueryRow(`
		SELECT count(*) FROM agent_actuator_assignments
		WHERE agent_id = ? AND actuator_id = ? AND enabled = 1
	`, agentID, actuatorID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// AssignActuatorToAgent creates or enables an assignment.
func (db *DB) AssignActuatorToAgent(agentID, actuatorID string) error {
	id := generateID()
	_, err := db.Exec(`
		INSERT INTO agent_actuator_assignments (id, agent_id, actuator_id, enabled)
		VALUES (?, ?, ?, 1)
		ON CONFLICT(agent_id, actuator_id) DO UPDATE SET enabled = 1
	`, id, agentID, actuatorID)
	return err
}

// UnassignActuatorFromAgent disables an assignment.
func (db *DB) UnassignActuatorFromAgent(agentID, actuatorID string) error {
	_, err := db.Exec(`
		UPDATE agent_actuator_assignments SET enabled = 0
		WHERE agent_id = ? AND actuator_id = ?
	`, agentID, actuatorID)
	return err
}

// ResolveActuatorForAgent finds the best actuator for an agent:
// 1. Selected actuator if set and assigned
// 2. First enabled assigned actuator
func (db *DB) ResolveActuatorForAgent(agentID string) (*Actuator, error) {
	// Check selected first
	var selectedID sql.NullString
	db.QueryRow(`SELECT selected_actuator_id FROM agents WHERE id = ?`, agentID).Scan(&selectedID)
	if selectedID.Valid {
		assigned, _ := db.IsActuatorAssignedToAgent(agentID, selectedID.String)
		if assigned {
			return db.GetActuatorByID(selectedID.String)
		}
	}

	// Fall back to first enabled assignment
	a := &Actuator{}
	var enabled int
	err := db.QueryRow(`
		SELECT a.id, a.account_id, a.name, a.type, a.status, a.token_hash, a.encrypted_token, a.enabled, a.last_seen_at, a.created_at
		FROM actuators a
		JOIN agent_actuator_assignments aaa ON a.id = aaa.actuator_id
		WHERE aaa.agent_id = ? AND aaa.enabled = 1
		ORDER BY a.name LIMIT 1
	`, agentID).Scan(&a.ID, &a.AccountID, &a.Name, &a.Type, &a.Status, &a.TokenHash, &a.EncryptedToken, &enabled, &a.LastSeenAt, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.Enabled = enabled != 0
	return a, nil
}

// UpdateActuatorStatus sets the status and last_seen_at for an actuator.
func (db *DB) UpdateActuatorStatus(actuatorID, status string) error {
	_, err := db.Exec(`
		UPDATE actuators SET status = ?, last_seen_at = ? WHERE id = ?
	`, status, time.Now().UTC().Format(time.RFC3339), actuatorID)
	return err
}
