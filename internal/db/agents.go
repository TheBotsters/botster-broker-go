package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/siofra-seksbot/botster-broker-go/internal/auth"
)

// Agent represents a brain agent in the broker.
type Agent struct {
	ID                 string
	AccountID          string
	Name               string
	TokenHash          string
	EncryptedToken     sql.NullString
	Safe               bool
	SelectedActuatorID sql.NullString
	CreatedAt          string
}

// CreateAgent creates a new agent and returns it along with the plaintext token.
func (db *DB) CreateAgent(accountID, name string) (*Agent, string, error) {
	id := generateID()
	token, err := auth.GenerateToken("seks_agent")
	if err != nil {
		return nil, "", fmt.Errorf("generate token: %w", err)
	}
	tokenHash := auth.HashToken(token)

	_, err = db.Exec(`
		INSERT INTO agents (id, account_id, name, token_hash, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, id, accountID, name, tokenHash, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return nil, "", fmt.Errorf("insert agent: %w", err)
	}

	agent, err := db.GetAgentByID(id)
	if err != nil {
		return nil, "", err
	}
	return agent, token, nil
}

// GetAgentByID returns an agent by its ID.
func (db *DB) GetAgentByID(id string) (*Agent, error) {
	a := &Agent{}
	err := db.QueryRow(`
		SELECT id, account_id, name, token_hash, encrypted_token, safe, selected_actuator_id, created_at
		FROM agents WHERE id = ?
	`, id).Scan(&a.ID, &a.AccountID, &a.Name, &a.TokenHash, &a.EncryptedToken, &a.Safe, &a.SelectedActuatorID, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query agent: %w", err)
	}
	return a, nil
}

// GetAgentByToken looks up an agent by plaintext token (hashes it first).
func (db *DB) GetAgentByToken(token string) (*Agent, error) {
	hash := auth.HashToken(token)
	a := &Agent{}
	err := db.QueryRow(`
		SELECT id, account_id, name, token_hash, encrypted_token, safe, selected_actuator_id, created_at
		FROM agents WHERE token_hash = ?
	`, hash).Scan(&a.ID, &a.AccountID, &a.Name, &a.TokenHash, &a.EncryptedToken, &a.Safe, &a.SelectedActuatorID, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query agent by token: %w", err)
	}
	return a, nil
}

// ListAgentsByAccount returns all agents belonging to an account.
func (db *DB) ListAgentsByAccount(accountID string) ([]*Agent, error) {
	rows, err := db.Query(`
		SELECT id, account_id, name, token_hash, encrypted_token, safe, selected_actuator_id, created_at
		FROM agents WHERE account_id = ? ORDER BY created_at
	`, accountID)
	if err != nil {
		return nil, fmt.Errorf("query agents: %w", err)
	}
	defer rows.Close()

	var agents []*Agent
	for rows.Next() {
		a := &Agent{}
		if err := rows.Scan(&a.ID, &a.AccountID, &a.Name, &a.TokenHash, &a.EncryptedToken, &a.Safe, &a.SelectedActuatorID, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

// SetAgentSafe toggles the per-agent safe mode flag.
func (db *DB) SetAgentSafe(agentID string, safe bool) error {
	val := 0
	if safe {
		val = 1
	}
	_, err := db.Exec(`UPDATE agents SET safe = ? WHERE id = ?`, val, agentID)
	if err != nil {
		return fmt.Errorf("set agent safe: %w", err)
	}
	return nil
}

// SelectActuator sets the selected actuator for an agent.
func (db *DB) SelectActuator(agentID string, actuatorID *string) error {
	_, err := db.Exec(`UPDATE agents SET selected_actuator_id = ? WHERE id = ?`, actuatorID, agentID)
	if err != nil {
		return fmt.Errorf("select actuator: %w", err)
	}
	return nil
}

// DeleteAgent removes an agent by ID.
func (db *DB) DeleteAgent(id string) error {
	_, err := db.Exec(`DELETE FROM agents WHERE id = ?`, id)
	return err
}
