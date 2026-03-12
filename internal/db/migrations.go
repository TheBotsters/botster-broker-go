package db

import (
	"fmt"
	"log"
)

// migration represents a numbered, idempotent schema change.
type migration struct {
	version     int
	description string
	sql         string
}

// migrations is the ordered list of all schema migrations.
// Each migration must be idempotent (safe to re-run).
// NEVER modify an existing migration. Only append new ones.
var migrations = []migration{
	{
		version:     1,
		description: "Initial schema — accounts, agents, actuators, secrets, audit",
		sql: `
			CREATE TABLE IF NOT EXISTS schema_version (
				version INTEGER NOT NULL,
				applied_at TEXT NOT NULL DEFAULT (datetime('now'))
			);

			CREATE TABLE IF NOT EXISTS accounts (
				id TEXT PRIMARY KEY,
				email TEXT NOT NULL UNIQUE,
				password_hash TEXT NOT NULL,
				created_at TEXT NOT NULL DEFAULT (datetime('now'))
			);

			CREATE TABLE IF NOT EXISTS agents (
				id TEXT PRIMARY KEY,
				account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
				name TEXT NOT NULL,
				token_hash TEXT NOT NULL UNIQUE,
				encrypted_token TEXT,
				safe INTEGER NOT NULL DEFAULT 0,
				selected_actuator_id TEXT,
				created_at TEXT NOT NULL DEFAULT (datetime('now')),
				UNIQUE(account_id, name)
			);

			CREATE TABLE IF NOT EXISTS actuators (
				id TEXT PRIMARY KEY,
				account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
				name TEXT NOT NULL,
				type TEXT NOT NULL DEFAULT 'vps',
				status TEXT NOT NULL DEFAULT 'offline',
				token_hash TEXT UNIQUE,
				encrypted_token TEXT,
				enabled INTEGER NOT NULL DEFAULT 1,
				last_seen_at TEXT,
				created_at TEXT NOT NULL DEFAULT (datetime('now'))
			);

			CREATE TABLE IF NOT EXISTS agent_actuator_assignments (
				id TEXT PRIMARY KEY,
				agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
				actuator_id TEXT NOT NULL REFERENCES actuators(id) ON DELETE CASCADE,
				enabled INTEGER NOT NULL DEFAULT 1,
				created_at TEXT NOT NULL DEFAULT (datetime('now')),
				UNIQUE(agent_id, actuator_id)
			);

			CREATE TABLE IF NOT EXISTS secrets (
				id TEXT PRIMARY KEY,
				account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
				name TEXT NOT NULL,
				provider TEXT NOT NULL,
				encrypted_value TEXT NOT NULL,
				metadata TEXT,
				created_at TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at TEXT NOT NULL DEFAULT (datetime('now')),
				UNIQUE(account_id, name)
			);

			CREATE TABLE IF NOT EXISTS secret_access (
				id TEXT PRIMARY KEY,
				secret_id TEXT NOT NULL REFERENCES secrets(id) ON DELETE CASCADE,
				agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
				UNIQUE(secret_id, agent_id)
			);

			CREATE TABLE IF NOT EXISTS audit_log (
				id TEXT PRIMARY KEY,
				account_id TEXT,
				agent_id TEXT,
				actuator_id TEXT,
				action TEXT NOT NULL,
				detail TEXT,
				created_at TEXT NOT NULL DEFAULT (datetime('now'))
			);

			CREATE TABLE IF NOT EXISTS broker_settings (
				key TEXT PRIMARY KEY,
				value TEXT NOT NULL,
				updated_at TEXT NOT NULL DEFAULT (datetime('now'))
			);

			CREATE TABLE IF NOT EXISTS commands (
				id TEXT PRIMARY KEY,
				agent_id TEXT NOT NULL,
				actuator_id TEXT,
				capability TEXT NOT NULL,
				payload TEXT NOT NULL,
				status TEXT NOT NULL DEFAULT 'pending',
				result TEXT,
				created_at TEXT NOT NULL DEFAULT (datetime('now')),
				completed_at TEXT
			);
		`,
	},
	{
		version:     2,
		description: "Add role column to agents table",
		sql: `
			ALTER TABLE agents ADD COLUMN role TEXT NOT NULL DEFAULT 'agent';
		`,
	},
	{
		version:     3,
		description: "Add retention_months setting to broker_settings",
		sql: `
			INSERT OR IGNORE INTO broker_settings (key, value, updated_at)
			VALUES ('retention_months', '6', datetime('now'));
		`,
	},
	{
		version:     4,
		description: "Add groups table and group_id to agents",
		sql: `
			CREATE TABLE IF NOT EXISTS groups (
				id TEXT PRIMARY KEY,
				account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
				name TEXT NOT NULL,
				admin_key_hash TEXT NOT NULL,
				created_at TEXT NOT NULL DEFAULT (datetime('now')),
				UNIQUE(account_id, name)
			);

			ALTER TABLE agents ADD COLUMN group_id TEXT REFERENCES groups(id);
		`,
	},
	{
		version:     5,
		description: "Add sync_peers table for broker-to-broker sync",
		sql: `
			CREATE TABLE IF NOT EXISTS sync_peers (
				id TEXT PRIMARY KEY,                          -- peer_id, e.g. "staging"
				label TEXT NOT NULL,                          -- human name
				token_hash TEXT NOT NULL UNIQUE,              -- SHA-256 of plaintext token
				transit_key_hex TEXT NOT NULL,               -- AES-256 transit key (64-char hex)
				transit_key_id TEXT NOT NULL,                -- label for key rotation
				allowed_resources TEXT NOT NULL DEFAULT 'secrets',  -- comma-separated
				allowed_accounts TEXT,                        -- NULL = all; else comma-separated IDs
				last_synced_at TEXT,
				created_at TEXT NOT NULL DEFAULT (datetime('now'))
			);
		`,
	},
	{
		version:     6,
		description: "Add token rotation grace-period columns to agents and actuators",
		sql: `
			ALTER TABLE agents ADD COLUMN prev_token_hash TEXT;
			ALTER TABLE agents ADD COLUMN token_rotation_expires_at TEXT;
			ALTER TABLE agents ADD COLUMN pending_encrypted_token TEXT;

			ALTER TABLE actuators ADD COLUMN prev_token_hash TEXT;
			ALTER TABLE actuators ADD COLUMN token_rotation_expires_at TEXT;
			ALTER TABLE actuators ADD COLUMN pending_encrypted_token TEXT;
		`,
	},
	{
		version:     7,
		description: "Add hardened token-rotation recovery state columns",
		sql: `
			ALTER TABLE agents ADD COLUMN pending_rotation_id TEXT;
			ALTER TABLE agents ADD COLUMN pending_recovery_expires_at TEXT;
			ALTER TABLE agents ADD COLUMN recovery_issued_at TEXT;

			ALTER TABLE actuators ADD COLUMN pending_rotation_id TEXT;
			ALTER TABLE actuators ADD COLUMN pending_recovery_expires_at TEXT;
			ALTER TABLE actuators ADD COLUMN recovery_issued_at TEXT;
		`,
	},
	{
		version:     8,
		description: "Add providers table for capability-based proxy",
		sql: `
			CREATE TABLE IF NOT EXISTS providers (
				id TEXT PRIMARY KEY,
				account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
				name TEXT NOT NULL,
				display_name TEXT NOT NULL,
				base_url TEXT NOT NULL,
				auth_type TEXT NOT NULL DEFAULT 'bearer',
				auth_header TEXT NOT NULL DEFAULT 'Authorization',
				secret_name TEXT NOT NULL,
				created_at TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at TEXT NOT NULL DEFAULT (datetime('now')),
				UNIQUE(account_id, name)
			);
		`,
	},
}

// migrate runs all pending migrations in order.
func (db *DB) migrate() error {
	current, err := db.SchemaVersion()
	if err != nil {
		// schema_version table might not exist yet — that's fine, version is 0
		current = 0
	}

	applied := 0
	for _, m := range migrations {
		if m.version <= current {
			continue
		}

		log.Printf("Applying migration %d: %s", m.version, m.description)

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", m.version, err)
		}

		if _, err := tx.Exec(m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("exec migration %d: %w", m.version, err)
		}

		if _, err := tx.Exec(`INSERT INTO schema_version (version) VALUES (?)`, m.version); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", m.version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", m.version, err)
		}

		applied++
	}

	if applied > 0 {
		log.Printf("Applied %d migration(s), now at version %d", applied, migrations[len(migrations)-1].version)
	} else {
		log.Printf("Schema up to date at version %d", current)
	}

	return nil
}
