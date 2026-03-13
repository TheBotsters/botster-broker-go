package main

import (
	"fmt"
	"log"

	"github.com/TheBotsters/botster-broker-go/internal/db"
)

func verifyImport(dest *db.DB, masterKey string) {
	// Count entities
	tables := map[string]string{
		"accounts":    "SELECT count(*) FROM accounts",
		"agents":      "SELECT count(*) FROM agents",
		"actuators":   "SELECT count(*) FROM actuators",
		"secrets":     "SELECT count(*) FROM secrets",
		"assignments": "SELECT count(*) FROM agent_actuator_assignments",
	}

	fmt.Println("\n=== Import Verification ===")
	for name, query := range tables {
		var count int
		dest.QueryRow(query).Scan(&count)
		fmt.Printf("  %-15s %d\n", name, count)
	}

	// Test secret decryption
	var accountID string
	dest.QueryRow("SELECT id FROM accounts LIMIT 1").Scan(&accountID)
	if accountID == "" {
		log.Println("No accounts found — skipping decryption test")
		return
	}

	secrets, err := dest.ListSecrets(accountID)
	if err != nil {
		log.Printf("List secrets failed: %v", err)
		return
	}

	decrypted := 0
	failed := 0
	for _, s := range secrets {
		_, err := dest.GetSecret(accountID, s.Name, masterKey)
		if err != nil {
			log.Printf("  ✗ %s: %v", s.Name, err)
			failed++
		} else {
			decrypted++
		}
	}
	fmt.Printf("\n  Secrets: %d decrypted OK, %d failed\n", decrypted, failed)

	// Test round-robin prefix
	anthropic, err := dest.GetSecretsByPrefix(accountID, "ANTHROPIC_TOKEN", masterKey)
	if err == nil {
		fmt.Printf("  ANTHROPIC_TOKEN round-robin pool: %d tokens\n", len(anthropic))
	}

	fmt.Println("=== Verification Complete ===")
}
