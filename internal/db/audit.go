package db

import "time"

// LogAudit records an action in the audit log.
func (db *DB) LogAudit(accountID, agentID, actuatorID, action, detail string) error {
	id := generateID()
	_, err := db.Exec(`
		INSERT INTO audit_log (id, account_id, agent_id, actuator_id, action, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, id, nilIfEmpty(accountID), nilIfEmpty(agentID), nilIfEmpty(actuatorID), action, detail, time.Now().UTC().Format(time.RFC3339))
	return err
}

func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
