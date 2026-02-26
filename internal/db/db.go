// Package db handles SQLite database initialization, migrations, and queries.
package db

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// DB wraps a sql.DB with broker-specific methods.
type DB struct {
	*sql.DB
}

// Open creates or opens the SQLite database at the given path.
// It enables WAL mode and foreign keys, then runs migrations.
func Open(dbPath string) (*DB, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	sqlDB, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// SQLite pragmas
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	}
	for _, p := range pragmas {
		if _, err := sqlDB.Exec(p); err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("exec pragma %q: %w", p, err)
		}
	}

	db := &DB{sqlDB}

	// Run migrations
	if err := db.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	log.Printf("Database opened: %s", dbPath)
	return db, nil
}

// SchemaVersion returns the current schema version, or 0 if no migrations have run.
func (db *DB) SchemaVersion() (int, error) {
	// Check if schema_version table exists
	var count int
	err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_version'`).Scan(&count)
	if err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, nil
	}

	var version int
	err = db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version)
	if err != nil {
		return 0, err
	}
	return version, nil
}
