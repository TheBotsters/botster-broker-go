package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/siofra-seksbot/botster-broker-go/internal/auth"
)

// Agent represents a registered agent (brain).
type Agent struct {
	ID                  string
	AccountID           string
	Name                string
	TokenHash           string
	EncryptedToken      sql.NullString
	Safe                bool
	SelectedActuatorID  sql.NullString
	CreatedAt           string
}

// CreateAgent registers a new agent and returns the agent with its plaintext token.
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

	agent := &Agent{
		ID:        id,
		AccountID: accountID,
		Name:      name,
		TokenHash: tokenHash,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return agent, token, nil
}

// GetAgentByToken looks up an agent by its plaintext token.
func (db *DB) GetAgentByToken(token string) (*Agent, error) {
	hash := auth.HashToken(token)
	return db.getAgentByHash(hash)
}

func (db *DB) getAgentByHash(hash string) (*Agent, error) {
	a := &Agent{}
	var safe int
	err := db.QueryRow(`
		SELECT id, account_id, name, token_hash, encrypted_token, safe, selected_actuator_id, created_at
		FROM agents WHERE token_hash = ?
	`, hash).Scan(&a.ID, &a.AccountID, &a.Name, &a.TokenHash, &a.EncryptedToken, &safe, &a.SelectedActuatorID, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query agent by token: %w", err)
	}
	a.Safe = safe != 0
	return a, nil
}

// GetAgentByID retrieves an agent by its ID.
func (db *DB) GetAgentByID(id string) (*Agent, error) {
	a := &Agent{}
	var safe int
	err := db.QueryRow(`
		SELECT id, account_id, name, token_hash, encrypted_token, safe, selected_actuator_id, created_at
		FROM agents WHERE id = ?
	`, id).Scan(&a.ID, &a.AccountID, &a.Name, &a.TokenHash, &a.EncryptedToken, &safe, &a.SelectedActuatorID, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query agent by id: %w", err)
	}
	a.Safe = safe != 0
	return a, nil
}

// ListAgents returns all agents for an account.
func (db *DB) ListAgents(accountID string) ([]Agent, error) {
	rows, err := db.Query(`
		SELECT id, account_id, name, token_hash, encrypted_token, safe, selected_actuator_id, created_at
		FROM agents WHERE account_id = ? ORDER BY name
	`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()

	var agents []Agent
	for rows.Next() {
		var a Agent
		var safe int
		if err := rows.Scan(&a.ID, &a.AccountID, &a.Name, &a.TokenHash, &a.EncryptedToken, &safe, &a.SelectedActuatorID, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		a.Safe = safe != 0
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

// SetAgentSafe toggles the safe mode flag for an agent.
func (db *DB) SetAgentSafe(agentID string, safe bool) error {
	val := 0
	if safe {
		val = 1
	}
	_, err := db.Exec(`UPDATE agents SET safe = ? WHERE id = ?`, val, agentID)
	return err
}

// SelectActuator sets the selected actuator for an agent.
func (db *DB) SelectActuator(agentID string, actuatorID *string) error {
	_, err := db.Exec(`UPDATE agents SET selected_actuator_id = ? WHERE id = ?`, actuatorID, agentID)
	return err
}
