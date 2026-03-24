package db

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/TheBotsters/botster-broker-go/internal/auth"
)

// Agent represents a brain agent in the broker.
type Agent struct {
	ID                       string
	AccountID                string
	Name                     string
	TokenHash                string
	EncryptedToken           sql.NullString
	Safe                     bool
	SelectedActuatorID       sql.NullString
	Role                     string
	GroupID                  sql.NullString
	CreatedAt                string
	PrevTokenHash            sql.NullString
	TokenRotationExpiresAt   sql.NullString
	PendingEncryptedToken    sql.NullString
	PendingRotationID        sql.NullString
	PendingRecoveryExpiresAt sql.NullString
	RecoveryIssuedAt         sql.NullString
	WorkspaceRoot            sql.NullString
	AuthMode                 string
}

// agentScanFields returns the standard column list for agent SELECT queries.
const agentColumns = `id, account_id, name, token_hash, encrypted_token, safe, selected_actuator_id, role, group_id, created_at, prev_token_hash, token_rotation_expires_at, pending_encrypted_token, pending_rotation_id, pending_recovery_expires_at, recovery_issued_at, workspace_root`

// scanAgent scans a row into an Agent struct.
func scanAgent(scanner interface {
	Scan(dest ...interface{}) error
}, a *Agent) error {
	return scanner.Scan(
		&a.ID, &a.AccountID, &a.Name, &a.TokenHash, &a.EncryptedToken,
		&a.Safe, &a.SelectedActuatorID, &a.Role, &a.GroupID, &a.CreatedAt,
		&a.PrevTokenHash, &a.TokenRotationExpiresAt, &a.PendingEncryptedToken,
		&a.PendingRotationID, &a.PendingRecoveryExpiresAt, &a.RecoveryIssuedAt,
		&a.WorkspaceRoot,
	)
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
		INSERT INTO agents (id, account_id, name, token_hash, role, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, id, accountID, name, tokenHash, "agent", time.Now().UTC().Format(time.RFC3339))
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
	err := scanAgent(db.QueryRow(`SELECT `+agentColumns+` FROM agents WHERE id = ?`, id), a)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query agent: %w", err)
	}
	return a, nil
}

// GetAgentByToken looks up an agent by plaintext token (hashes it first).
// Auth modes:
// - current: token_hash match
// - grace: prev_token_hash match within grace period
// - recovery: single-use fallback after grace, only while pending rotation is valid
func (db *DB) GetAgentByToken(token string) (*Agent, error) {
	hash := auth.HashToken(token)

	// Try current token first.
	a := &Agent{}
	err := scanAgent(db.QueryRow(`SELECT `+agentColumns+` FROM agents WHERE token_hash = ?`, hash), a)
	if err == nil {
		a.AuthMode = "current"
		return a, nil
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("query agent by token: %w", err)
	}

	// Try previous token during grace period.
	a = &Agent{}
	err = scanAgent(db.QueryRow(
		`SELECT `+agentColumns+` FROM agents WHERE prev_token_hash = ? AND token_rotation_expires_at > datetime('now')`,
		hash,
	), a)
	if err == nil {
		a.AuthMode = "grace"
		log.Printf("[botster-broker] Agent %s authenticated with previous token (grace period expires %s)", a.ID, a.TokenRotationExpiresAt.String)
		return a, nil
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("query agent by prev token: %w", err)
	}

	// Recovery path: allow one-time auth with previous token after grace, if pending
	// rotation is still valid and has not already been issued.
	res, err := db.Exec(`
		UPDATE agents
		SET recovery_issued_at = datetime('now')
		WHERE prev_token_hash = ?
		  AND pending_encrypted_token IS NOT NULL
		  AND pending_rotation_id IS NOT NULL
		  AND pending_recovery_expires_at > datetime('now')
		  AND (recovery_issued_at IS NULL)
	`, hash)
	if err != nil {
		return nil, fmt.Errorf("claim agent recovery: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows > 0 {
		a = &Agent{}
		err = scanAgent(db.QueryRow(`SELECT `+agentColumns+` FROM agents WHERE prev_token_hash = ?`, hash), a)
		if err != nil {
			return nil, fmt.Errorf("query agent after recovery claim: %w", err)
		}
		a.AuthMode = "recovery"
		log.Printf("[botster-broker] Agent %s authenticated via single-use recovery path", a.ID)
		return a, nil
	}

	// Lazy cleanup: clear fully expired pending rotation state.
	db.Exec(`
		UPDATE agents
		SET prev_token_hash = NULL,
		    token_rotation_expires_at = NULL,
		    pending_encrypted_token = NULL,
		    pending_rotation_id = NULL,
		    pending_recovery_expires_at = NULL,
		    recovery_issued_at = NULL
		WHERE prev_token_hash = ?
		  AND pending_recovery_expires_at <= datetime('now')
	`, hash)

	return nil, nil
}

// ListAgentsByAccount returns all agents belonging to an account.
func (db *DB) ListAgentsByAccount(accountID string) ([]*Agent, error) {
	rows, err := db.Query(`SELECT `+agentColumns+` FROM agents WHERE account_id = ? ORDER BY created_at`, accountID)
	if err != nil {
		return nil, fmt.Errorf("query agents: %w", err)
	}
	defer rows.Close()

	var agents []*Agent
	for rows.Next() {
		a := &Agent{}
		if err := scanAgent(rows, a); err != nil {
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

// GetAgentByName looks up an agent by name (globally, not account-scoped).
// For account-scoped lookup, use GetAgentByNameAndAccount.
func (db *DB) GetAgentByName(name string) (*Agent, error) {
	a := &Agent{}
	err := scanAgent(db.QueryRow(`SELECT `+agentColumns+` FROM agents WHERE name = ? LIMIT 1`, name), a)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query agent by name: %w", err)
	}
	return a, nil
}

// UpdateAgent updates mutable agent fields (name, role, safe).
// Only non-nil fields are updated.
func (db *DB) UpdateAgent(id string, updates map[string]interface{}) error {
	if len(updates) == 0 {
		return nil
	}
	// Build SET clause dynamically
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
	_, err := db.Exec("UPDATE agents SET "+setClauses+" WHERE id = ?", args...)
	if err != nil {
		return fmt.Errorf("update agent: %w", err)
	}
	return nil
}

// RotateAgentToken generates a new token with a two-phase grace period.
// The old token remains valid until gracePeriod elapses.
// The new plaintext token is encrypted with masterKey for re-delivery on reconnect.
func (db *DB) RotateAgentToken(agentID string, gracePeriod time.Duration, masterKey string) (string, error) {
	agent, err := db.GetAgentByID(agentID)
	if err != nil {
		return "", fmt.Errorf("get agent: %w", err)
	}
	if agent == nil {
		return "", fmt.Errorf("agent not found: %s", agentID)
	}

	token, err := auth.GenerateToken("seks_agent")
	if err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	newHash := auth.HashToken(token)

	encryptedNew, err := encrypt([]byte(token), masterKey)
	if err != nil {
		return "", fmt.Errorf("encrypt new token: %w", err)
	}

	now := time.Now().UTC()
	expiresAt := now.Add(gracePeriod).Format(time.RFC3339)
	hardExpiresAt := now.Add(24 * time.Hour).Format(time.RFC3339)
	rotationID := generateID()

	_, err = db.Exec(`
		UPDATE agents
		SET token_hash = ?, prev_token_hash = ?, token_rotation_expires_at = ?, pending_encrypted_token = ?, pending_rotation_id = ?, pending_recovery_expires_at = ?, recovery_issued_at = NULL
		WHERE id = ?
	`, newHash, agent.TokenHash, expiresAt, encryptedNew, rotationID, hardExpiresAt, agentID)
	if err != nil {
		return "", fmt.Errorf("rotate token: %w", err)
	}
	return token, nil
}

// AcknowledgeAgentTokenRotation clears pending token-rotation residue after client confirmation.
// Returns true when a matching pending rotation was acknowledged and cleared.
func (db *DB) AcknowledgeAgentTokenRotation(agentID, rotationID string) (bool, error) {
	res, err := db.Exec(`
		UPDATE agents
		SET prev_token_hash = NULL,
		    token_rotation_expires_at = NULL,
		    pending_encrypted_token = NULL,
		    pending_rotation_id = NULL,
		    pending_recovery_expires_at = NULL,
		    recovery_issued_at = NULL
		WHERE id = ? AND pending_rotation_id = ?
	`, agentID, rotationID)
	if err != nil {
		return false, fmt.Errorf("ack agent token rotation: %w", err)
	}
	rows, _ := res.RowsAffected()
	return rows > 0, nil
}

// ListAllAgents returns all agents across all accounts.
func (db *DB) ListAllAgents() ([]*Agent, error) {
	rows, err := db.Query(`SELECT ` + agentColumns + ` FROM agents ORDER BY account_id, created_at`)
	if err != nil {
		return nil, fmt.Errorf("query all agents: %w", err)
	}
	defer rows.Close()

	var agents []*Agent
	for rows.Next() {
		a := &Agent{}
		if err := scanAgent(rows, a); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}
