package db

import "fmt"

// ListActuatorCapabilities returns explicit capability allowlist rows for an actuator.
func (db *DB) ListActuatorCapabilities(actuatorID string) ([]string, error) {
	rows, err := db.Query(`SELECT capability FROM actuator_capabilities WHERE actuator_id = ? ORDER BY capability`, actuatorID)
	if err != nil {
		return nil, fmt.Errorf("query actuator capabilities: %w", err)
	}
	defer rows.Close()

	caps := []string{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, fmt.Errorf("scan actuator capability: %w", err)
		}
		caps = append(caps, c)
	}
	return caps, rows.Err()
}

// ReplaceActuatorCapabilities replaces an actuator's explicit capability allowlist.
func (db *DB) ReplaceActuatorCapabilities(actuatorID string, caps []string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM actuator_capabilities WHERE actuator_id = ?`, actuatorID); err != nil {
		return err
	}
	for _, c := range caps {
		if c == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO actuator_capabilities (id, actuator_id, capability) VALUES (?, ?, ?)`, generateID(), actuatorID, c); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ActuatorCapabilityAllowed checks whether actuator is allowed to run the capability.
// Backward compatibility: if no explicit rows exist for actuator, allow all capabilities.
func (db *DB) ActuatorCapabilityAllowed(actuatorID, capability string) (bool, error) {
	var total int
	if err := db.QueryRow(`SELECT count(*) FROM actuator_capabilities WHERE actuator_id = ?`, actuatorID).Scan(&total); err != nil {
		return false, err
	}
	if total == 0 {
		return true, nil
	}
	var allowed int
	if err := db.QueryRow(`SELECT count(*) FROM actuator_capabilities WHERE actuator_id = ? AND capability = ?`, actuatorID, capability).Scan(&allowed); err != nil {
		return false, err
	}
	return allowed > 0, nil
}
