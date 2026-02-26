package db

import (
	"database/sql"
	"time"
)

// GetSetting retrieves a broker setting by key.
func (db *DB) GetSetting(key string) (string, error) {
	var value string
	err := db.QueryRow(`SELECT value FROM broker_settings WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// SetSetting upserts a broker setting.
func (db *DB) SetSetting(key, value string) error {
	_, err := db.Exec(`
		INSERT INTO broker_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = ?, updated_at = ?
	`, key, value, time.Now().UTC().Format(time.RFC3339), value, time.Now().UTC().Format(time.RFC3339))
	return err
}

// GetGlobalSafe returns whether global safe mode is active.
func (db *DB) GetGlobalSafe() (bool, error) {
	val, err := db.GetSetting("global_safe")
	if err != nil {
		return false, err
	}
	return val == "1", nil
}

// SetGlobalSafe toggles global safe mode.
func (db *DB) SetGlobalSafe(safe bool) error {
	val := "0"
	if safe {
		val = "1"
	}
	return db.SetSetting("global_safe", val)
}
