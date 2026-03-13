package db

import (
	"fmt"
	"time"
)

// RecordCommandInsert inserts a command lifecycle row.
func (db *DB) RecordCommandInsert(id, agentID, actuatorID, capability, payload string) error {
	_, err := db.Exec(`
		INSERT OR REPLACE INTO commands (id, agent_id, actuator_id, capability, payload, status, created_at)
		VALUES (?, ?, ?, ?, ?, 'pending', ?)
	`, id, agentID, actuatorID, capability, payload, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("insert command: %w", err)
	}
	return nil
}

// RecordCommandResult updates command lifecycle status/result.
func (db *DB) RecordCommandResult(id, status, result string) error {
	_, err := db.Exec(`
		UPDATE commands
		SET status = ?, result = ?, completed_at = ?
		WHERE id = ?
	`, status, result, time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return fmt.Errorf("update command result: %w", err)
	}
	return nil
}
