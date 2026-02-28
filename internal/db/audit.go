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

// ListAuditLog returns audit entries. If accountID is non-nil, filters to that account.
// Entries are returned newest-first, limited to 500.
func (db *DB) ListAuditLog(accountID *string, limit int) ([]*AuditEntry, error) {
	if limit <= 0 {
		limit = 500
	}
	var rows interface{ Next() bool; Scan(...interface{}) error; Close() error; Err() error }
	var err error
	if accountID != nil {
		rows, err = db.Query(`
			SELECT id, account_id, agent_id, actuator_id, action, detail, created_at
			FROM audit_log WHERE account_id = ?
			ORDER BY created_at DESC LIMIT ?
		`, *accountID, limit)
	} else {
		rows, err = db.Query(`
			SELECT id, account_id, agent_id, actuator_id, action, detail, created_at
			FROM audit_log ORDER BY created_at DESC LIMIT ?
		`, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("query audit log: %w", err)
	}
	defer rows.Close()

	var entries []*AuditEntry
	for rows.Next() {
		e := &AuditEntry{}
		if err := rows.Scan(&e.ID, &e.AccountID, &e.AgentID, &e.ActuatorID, &e.Action, &e.Detail, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan audit entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// PruneAuditLog deletes audit entries older than retentionMonths months.
func (db *DB) PruneAuditLog(retentionMonths int) (int64, error) {
	result, err := db.Exec(`
		DELETE FROM audit_log
		WHERE created_at < datetime('now', ? || ' months')
	`, fmt.Sprintf("-%d", retentionMonths))
	if err != nil {
		return 0, fmt.Errorf("prune audit log: %w", err)
	}
	n, _ := result.RowsAffected()
	return n, nil
}
