package db

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/siofra-seksbot/botster-broker-go/internal/auth"
)

// Actuator represents an execution endpoint.
type Actuator struct {
	ID                      string
	AccountID               string
	Name                    string
	Type                    string
	Status                  string
	TokenHash               sql.NullString
	EncryptedToken          sql.NullString
	Enabled                 bool
	LastSeenAt              sql.NullString
	CreatedAt               string
	PrevTokenHash           sql.NullString
	TokenRotationExpiresAt  sql.NullString
	PendingEncryptedToken   sql.NullString
}

const actuatorColumns = `id, account_id, name, type, status, token_hash, encrypted_token, enabled, last_seen_at, created_at, prev_token_hash, token_rotation_expires_at, pending_encrypted_token`

func scanActuator(scanner interface {
	Scan(dest ...interface{}) error
}, a *Actuator) error {
	return scanner.Scan(
		&a.ID, &a.AccountID, &a.Name, &a.Type, &a.Status, &a.TokenHash, &a.EncryptedToken,
		&a.Enabled, &a.LastSeenAt, &a.CreatedAt,
		&a.PrevTokenHash, &a.TokenRotationExpiresAt, &a.PendingEncryptedToken,
	)
}

// CreateActuator creates a new actuator and returns it along with the plaintext token.
func (db *DB) CreateActuator(accountID, name, actuatorType string) (*Actuator, string, error) {
	id := generateID()
	token, err := auth.GenerateToken("seks_actuator")
	if err != nil {
		return nil, "", fmt.Errorf("generate token: %w", err)
	}
	tokenHash := auth.HashToken(token)

	_, err = db.Exec(`
		INSERT INTO actuators (id, account_id, name, type, token_hash, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, id, accountID, name, actuatorType, tokenHash, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return nil, "", fmt.Errorf("insert actuator: %w", err)
	}

	act, err := db.GetActuatorByID(id)
	if err != nil {
		return nil, "", err
	}
	return act, token, nil
}

// GetActuatorByID returns an actuator by ID.
func (db *DB) GetActuatorByID(id string) (*Actuator, error) {
	a := &Actuator{}
	err := scanActuator(db.QueryRow(`SELECT `+actuatorColumns+` FROM actuators WHERE id = ?`, id), a)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query actuator: %w", err)
	}
	return a, nil
}

// GetActuatorByToken looks up an actuator by plaintext token.
// It checks the current token_hash first, then falls back to prev_token_hash
// if a rotation grace period is active. Expired grace periods are lazily cleaned up.
func (db *DB) GetActuatorByToken(token string) (*Actuator, error) {
	hash := auth.HashToken(token)

	// Try current token first.
	a := &Actuator{}
	err := scanActuator(db.QueryRow(`SELECT `+actuatorColumns+` FROM actuators WHERE token_hash = ?`, hash), a)
	if err == nil {
		return a, nil
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("query actuator by token: %w", err)
	}

	// Try previous token during grace period.
	a = &Actuator{}
	err = scanActuator(db.QueryRow(
		`SELECT `+actuatorColumns+` FROM actuators WHERE prev_token_hash = ? AND token_rotation_expires_at > datetime('now')`,
		hash,
	), a)
	if err == sql.ErrNoRows {
		db.Exec(`UPDATE actuators SET prev_token_hash = NULL, token_rotation_expires_at = NULL, pending_encrypted_token = NULL WHERE prev_token_hash = ? AND token_rotation_expires_at <= datetime('now')`, hash)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query actuator by prev token: %w", err)
	}

	log.Printf("[botster-broker] Actuator %s authenticated with previous token (grace period expires %s)", a.ID, a.TokenRotationExpiresAt.String)
	return a, nil
}

// ListActuatorsByAccount returns all actuators for an account.
func (db *DB) ListActuatorsByAccount(accountID string) ([]*Actuator, error) {
	rows, err := db.Query(`SELECT `+actuatorColumns+` FROM actuators WHERE account_id = ? ORDER BY created_at`, accountID)
	if err != nil {
		return nil, fmt.Errorf("query actuators: %w", err)
	}
	defer rows.Close()

	var actuators []*Actuator
	for rows.Next() {
		a := &Actuator{}
		if err := scanActuator(rows, a); err != nil {
			return nil, fmt.Errorf("scan actuator: %w", err)
		}
		actuators = append(actuators, a)
	}
	return actuators, rows.Err()
}

// AssignActuatorToAgent creates an assignment between an agent and actuator.
func (db *DB) AssignActuatorToAgent(agentID, actuatorID string) error {
	id := generateID()
	_, err := db.Exec(`
		INSERT OR IGNORE INTO agent_actuator_assignments (id, agent_id, actuator_id, created_at)
		VALUES (?, ?, ?, ?)
	`, id, agentID, actuatorID, time.Now().UTC().Format(time.RFC3339))
	return err
}

// UnassignActuatorFromAgent removes an assignment.
func (db *DB) UnassignActuatorFromAgent(agentID, actuatorID string) error {
	_, err := db.Exec(`DELETE FROM agent_actuator_assignments WHERE agent_id = ? AND actuator_id = ?`, agentID, actuatorID)
	return err
}

// IsActuatorAssignedToAgent checks if an actuator is assigned to an agent.
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

// ResolveActuatorForAgent finds the actuator for an agent using the selection chain:
// 1. Persisted selection (selected_actuator_id) — use if valid, null if not. No fallthrough.
// 2. Implicit auto-selection — ONLY when selected_actuator_id is NULL:
//    count non-brain actuators assigned; if exactly 1, auto-persist and return it.
// 3. Null — agent must explicitly select via POST /v1/actuator/select.
//
// Brain-type actuators are NEVER candidates for auto-selection.
// This is the single function for actuator selection (DRY policy).
func (db *DB) ResolveActuatorForAgent(agentID string) (*Actuator, error) {
	// Step 1: Check persisted selection
	var selectedID sql.NullString
	err := db.QueryRow(`SELECT selected_actuator_id FROM agents WHERE id = ?`, agentID).Scan(&selectedID)
	if err != nil {
		return nil, err
	}
	if selectedID.Valid {
		// Validate it's still assigned and enabled
		assigned, err := db.IsActuatorAssignedToAgent(agentID, selectedID.String)
		if err != nil {
			return nil, err
		}
		if assigned {
			return db.GetActuatorByID(selectedID.String)
		}
		// Invalid selection — return null, no fallthrough
		return nil, nil
	}

	// Step 2: Implicit auto-selection (only when selected_actuator_id is NULL)
	// Count non-brain actuators assigned to this agent
	rows, err := db.Query(`
		SELECT act.id, act.account_id, act.name, act.type, act.status, act.token_hash,
		       act.encrypted_token, act.enabled, act.last_seen_at, act.created_at,
		       act.prev_token_hash, act.token_rotation_expires_at, act.pending_encrypted_token
		FROM actuators act
		JOIN agent_actuator_assignments aaa ON act.id = aaa.actuator_id
		WHERE aaa.agent_id = ? AND aaa.enabled = 1 AND act.enabled = 1 AND act.type != 'brain'
		ORDER BY act.created_at
	`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []*Actuator
	for rows.Next() {
		a := &Actuator{}
		if err := scanActuator(rows, a); err != nil {
			return nil, err
		}
		candidates = append(candidates, a)
	}

	if len(candidates) == 1 {
		// Exactly one non-brain actuator — auto-persist and return
		db.SelectActuator(agentID, &candidates[0].ID)
		return candidates[0], nil
	}

	// Step 3: Null — agent must explicitly select
	return nil, nil
}

// UpdateActuatorStatus sets the status and last_seen_at for an actuator.
func (db *DB) UpdateActuatorStatus(id, status string) error {
	_, err := db.Exec(`
		UPDATE actuators SET status = ?, last_seen_at = ? WHERE id = ?
	`, status, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// DeleteActuator removes an actuator by ID.
func (db *DB) DeleteActuator(id string) error {
	_, err := db.Exec(`DELETE FROM actuators WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete actuator: %w", err)
	}
	return nil
}

// ListActuatorsByAgent returns all actuators assigned to a specific agent.
func (db *DB) ListActuatorsByAgent(agentID string) ([]*Actuator, error) {
	rows, err := db.Query(`
		SELECT act.id, act.account_id, act.name, act.type, act.status, act.token_hash,
		       act.encrypted_token, act.enabled, act.last_seen_at, act.created_at,
		       act.prev_token_hash, act.token_rotation_expires_at, act.pending_encrypted_token
		FROM actuators act
		JOIN agent_actuator_assignments aaa ON act.id = aaa.actuator_id
		WHERE aaa.agent_id = ?
		ORDER BY act.created_at
	`, agentID)
	if err != nil {
		return nil, fmt.Errorf("query actuators by agent: %w", err)
	}
	defer rows.Close()

	var actuators []*Actuator
	for rows.Next() {
		a := &Actuator{}
		if err := scanActuator(rows, a); err != nil {
			return nil, fmt.Errorf("scan actuator: %w", err)
		}
		actuators = append(actuators, a)
	}
	return actuators, rows.Err()
}

// RotateActuatorToken generates a new token with a two-phase grace period.
// The old token remains valid until gracePeriod elapses.
func (db *DB) RotateActuatorToken(actuatorID string, gracePeriod time.Duration, masterKey string) (string, error) {
	actuator, err := db.GetActuatorByID(actuatorID)
	if err != nil {
		return "", fmt.Errorf("get actuator: %w", err)
	}
	if actuator == nil {
		return "", fmt.Errorf("actuator not found: %s", actuatorID)
	}

	token, err := auth.GenerateToken("seks_actuator")
	if err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	newHash := auth.HashToken(token)

	encryptedNew, err := encrypt([]byte(token), masterKey)
	if err != nil {
		return "", fmt.Errorf("encrypt new token: %w", err)
	}

	expiresAt := time.Now().UTC().Add(gracePeriod).Format(time.RFC3339)

	_, err = db.Exec(`
		UPDATE actuators
		SET token_hash = ?, prev_token_hash = ?, token_rotation_expires_at = ?, pending_encrypted_token = ?
		WHERE id = ?
	`, newHash, actuator.TokenHash.String, expiresAt, encryptedNew, actuatorID)
	if err != nil {
		return "", fmt.Errorf("rotate actuator token: %w", err)
	}
	return token, nil
}
