// Import tool — reads the current TypeScript broker's SQLite DB
// and writes compatible records into the Go broker's DB.
//
// Same MASTER_KEY = encrypted secrets transfer without re-encryption.
//
// Usage:
//   go run ./cmd/import --from /opt/broker/data/broker.db --to data/broker.db
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"

	"github.com/siofra-seksbot/botster-broker-go/internal/db"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	fromPath := flag.String("from", "/opt/broker/data/broker.db", "Source broker DB path")
	toPath := flag.String("to", "data/broker.db", "Destination Go broker DB path")
	masterKey := flag.String("master-key", "", "Master key (reads MASTER_KEY env if empty)")
	flag.Parse()

	if *masterKey == "" {
		*masterKey = os.Getenv("MASTER_KEY")
	}
	if *masterKey == "" {
		log.Fatal("MASTER_KEY required (--master-key or env)")
	}

	// Open source (read-only)
	src, err := sql.Open("sqlite3", *fromPath)
	if err != nil {
		log.Fatalf("Open source: %v", err)
	}
	defer src.Close()

	// Verify source is readable
	var srcCount int
	if err := src.QueryRow("SELECT count(*) FROM accounts").Scan(&srcCount); err != nil {
		log.Fatalf("Source DB not readable: %v", err)
	}
	log.Printf("Source DB: %s (%d accounts)", *fromPath, srcCount)

	// Open destination (Go broker DB — will run migrations)
	os.Setenv("MASTER_KEY", *masterKey)
	dir := filepath.Dir(*toPath)
	os.MkdirAll(dir, 0755)

	dest, err := db.Open(*toPath)
	if err != nil {
		log.Fatalf("Open destination: %v", err)
	}
	defer dest.Close()

	// Import in order: accounts → agents → actuators → assignments → secrets → secret_access → settings
	importAccounts(src, dest)
	importAgents(src, dest)
	importActuators(src, dest)
	importAssignments(src, dest)
	importSecrets(src, dest)
	importSecretAccess(src, dest)
	importSettings(src, dest)

	verifyImport(dest, *masterKey)
	log.Println("Import complete!")
}

func importAccounts(src *sql.DB, dest *db.DB) {
	rows, err := src.Query(`SELECT id, email, password_hash, created_at FROM accounts`)
	if err != nil {
		log.Fatalf("Query accounts: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var id, email, pwHash, createdAt string
		if err := rows.Scan(&id, &email, &pwHash, &createdAt); err != nil {
			log.Fatalf("Scan account: %v", err)
		}
		_, err := dest.Exec(`
			INSERT OR IGNORE INTO accounts (id, email, password_hash, created_at)
			VALUES (?, ?, ?, ?)
		`, id, email, pwHash, createdAt)
		if err != nil {
			log.Printf("Skip account %s: %v", email, err)
			continue
		}
		count++
	}
	log.Printf("Imported %d accounts", count)
}

func importAgents(src *sql.DB, dest *db.DB) {
	rows, err := src.Query(`
		SELECT id, account_id, name, token_hash, encrypted_token, safe, selected_actuator_id, created_at
		FROM agents
	`)
	if err != nil {
		log.Fatalf("Query agents: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var id, accountID, name, tokenHash, createdAt string
		var encToken, selActuator sql.NullString
		var safe int
		if err := rows.Scan(&id, &accountID, &name, &tokenHash, &encToken, &safe, &selActuator, &createdAt); err != nil {
			log.Fatalf("Scan agent: %v", err)
		}
		_, err := dest.Exec(`
			INSERT OR IGNORE INTO agents (id, account_id, name, token_hash, encrypted_token, safe, selected_actuator_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, id, accountID, name, tokenHash, encToken, safe, selActuator, createdAt)
		if err != nil {
			log.Printf("Skip agent %s: %v", name, err)
			continue
		}
		count++
	}
	log.Printf("Imported %d agents", count)
}

func importActuators(src *sql.DB, dest *db.DB) {
	rows, err := src.Query(`
		SELECT id, account_id, name, type, status, token_hash, encrypted_token, enabled, last_seen_at, created_at
		FROM actuators
	`)
	if err != nil {
		log.Fatalf("Query actuators: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var id, name, aType, status, createdAt string
		var accountID, tokenHash, encToken, lastSeen sql.NullString
		var enabled int
		if err := rows.Scan(&id, &accountID, &name, &aType, &status, &tokenHash, &encToken, &enabled, &lastSeen, &createdAt); err != nil {
			log.Fatalf("Scan actuator: %v", err)
		}

		// Source may have NULL account_id (legacy) — skip those or use a sentinel
		acctID := accountID.String
		if !accountID.Valid || acctID == "" {
			log.Printf("Skip actuator %s (no account_id)", name)
			continue
		}

		_, err := dest.Exec(`
			INSERT OR IGNORE INTO actuators (id, account_id, name, type, status, token_hash, encrypted_token, enabled, last_seen_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, id, acctID, name, aType, status, tokenHash, encToken, enabled, lastSeen, createdAt)
		if err != nil {
			log.Printf("Skip actuator %s: %v", name, err)
			continue
		}
		count++
	}
	log.Printf("Imported %d actuators", count)
}

func importAssignments(src *sql.DB, dest *db.DB) {
	rows, err := src.Query(`SELECT agent_id, actuator_id, enabled, assigned_at FROM agent_actuator_assignments`)
	if err != nil {
		log.Fatalf("Query assignments: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var agentID, actuatorID, assignedAt string
		var enabled int
		if err := rows.Scan(&agentID, &actuatorID, &enabled, &assignedAt); err != nil {
			log.Fatalf("Scan assignment: %v", err)
		}

		id := fmt.Sprintf("asgn_%s_%s", agentID[:8], actuatorID[:8])
		_, err := dest.Exec(`
			INSERT OR IGNORE INTO agent_actuator_assignments (id, agent_id, actuator_id, enabled, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, id, agentID, actuatorID, enabled, assignedAt)
		if err != nil {
			log.Printf("Skip assignment %s→%s: %v", agentID, actuatorID, err)
			continue
		}
		count++
	}
	log.Printf("Imported %d assignments", count)
}

func importSecrets(src *sql.DB, dest *db.DB) {
	// Secrets transfer directly — same encryption, same MASTER_KEY
	rows, err := src.Query(`
		SELECT id, account_id, name, provider, encrypted_value, metadata, created_at, updated_at
		FROM secrets
	`)
	if err != nil {
		log.Fatalf("Query secrets: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var id, accountID, name, provider, encValue, createdAt, updatedAt string
		var metadata sql.NullString
		if err := rows.Scan(&id, &accountID, &name, &provider, &encValue, &metadata, &createdAt, &updatedAt); err != nil {
			log.Fatalf("Scan secret: %v", err)
		}
		_, err := dest.Exec(`
			INSERT OR IGNORE INTO secrets (id, account_id, name, provider, encrypted_value, metadata, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, id, accountID, name, provider, encValue, metadata, createdAt, updatedAt)
		if err != nil {
			log.Printf("Skip secret %s: %v", name, err)
			continue
		}
		count++
	}
	log.Printf("Imported %d secrets", count)
}

func importSecretAccess(src *sql.DB, dest *db.DB) {
	// Source has no id column — composite PK (secret_id, agent_id)
	rows, err := src.Query(`SELECT secret_id, agent_id FROM secret_access`)
	if err != nil {
		log.Fatalf("Query secret_access: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var secretID, agentID string
		if err := rows.Scan(&secretID, &agentID); err != nil {
			log.Fatalf("Scan secret_access: %v", err)
		}
		id := fmt.Sprintf("sa_%s_%s", secretID[:8], agentID[:8])
		_, err := dest.Exec(`
			INSERT OR IGNORE INTO secret_access (id, secret_id, agent_id) VALUES (?, ?, ?)
		`, id, secretID, agentID)
		if err != nil {
			log.Printf("Skip secret_access %s→%s: %v", secretID, agentID, err)
			continue
		}
		count++
	}
	log.Printf("Imported %d secret_access entries", count)
}

func importSettings(src *sql.DB, dest *db.DB) {
	rows, err := src.Query(`SELECT key, value FROM broker_settings`)
	if err != nil {
		log.Fatalf("Query settings: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			log.Fatalf("Scan setting: %v", err)
		}
		now := time.Now().UTC().Format(time.RFC3339)
		_, err := dest.Exec(`
			INSERT OR REPLACE INTO broker_settings (key, value, updated_at) VALUES (?, ?, ?)
		`, key, value, now)
		if err != nil {
			log.Printf("Skip setting %s: %v", key, err)
			continue
		}
		count++
	}
	log.Printf("Imported %d settings", count)
}
