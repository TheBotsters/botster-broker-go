package db

import (
	"fmt"
	"time"
)

// AuditEntry represents a logged action.
type AuditEntry struct {
	ID         string
	AccountID  *string
	AgentID    *string
	ActuatorID *string
	Action     string
	Detail     *string
	CreatedAt  string
}

// LogAudit writes an entry to the audit log.
func (db *DB) LogAudit(accountID, agentID, actuatorID *string, action, detail string) error {
	id := generateID()
	var detailPtr *string
	if detail != "" {
		detailPtr = &detail
	}

	_, err := db.Exec(`
		INSERT INTO audit_log (id, account_id, agent_id, actuator_id, action, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, id, accountID, agentID, actuatorID, action, detailPtr, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("insert audit: %w", err)
	}
	return nil
}
