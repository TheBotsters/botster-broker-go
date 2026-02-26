package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFullLifecycle(t *testing.T) {
	// Use temp directory
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	// Check schema version
	ver, err := database.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if ver != 1 {
		t.Fatalf("expected schema version 1, got %d", ver)
	}

	// Create account
	accountID := generateID()
	_, err = database.Exec(`INSERT INTO accounts (id, email, password_hash) VALUES (?, ?, ?)`,
		accountID, "test@example.com", "hash123")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	// Create agent
	agent, token, err := database.CreateAgent(accountID, "test-agent")
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if agent.Name != "test-agent" {
		t.Fatalf("expected name 'test-agent', got %q", agent.Name)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	// Look up by token
	found, err := database.GetAgentByToken(token)
	if err != nil {
		t.Fatalf("GetAgentByToken: %v", err)
	}
	if found == nil {
		t.Fatal("expected to find agent by token")
	}
	if found.ID != agent.ID {
		t.Fatalf("agent ID mismatch: %s vs %s", found.ID, agent.ID)
	}

	// Create actuator
	act, actToken, err := database.CreateActuator(accountID, "test-actuator", "vps")
	if err != nil {
		t.Fatalf("CreateActuator: %v", err)
	}
	if actToken == "" {
		t.Fatal("expected non-empty actuator token")
	}

	// Assign actuator to agent
	if err := database.AssignActuatorToAgent(agent.ID, act.ID); err != nil {
		t.Fatalf("AssignActuatorToAgent: %v", err)
	}

	// Check assignment
	assigned, err := database.IsActuatorAssignedToAgent(agent.ID, act.ID)
	if err != nil {
		t.Fatalf("IsActuatorAssignedToAgent: %v", err)
	}
	if !assigned {
		t.Fatal("expected actuator to be assigned")
	}

	// Resolve actuator for agent
	resolved, err := database.ResolveActuatorForAgent(agent.ID)
	if err != nil {
		t.Fatalf("ResolveActuatorForAgent: %v", err)
	}
	if resolved == nil {
		t.Fatal("expected to resolve an actuator")
	}
	if resolved.ID != act.ID {
		t.Fatalf("resolved wrong actuator: %s vs %s", resolved.ID, act.ID)
	}

	// Safe mode
	if err := database.SetAgentSafe(agent.ID, true); err != nil {
		t.Fatalf("SetAgentSafe: %v", err)
	}
	safeAgent, _ := database.GetAgentByID(agent.ID)
	if !safeAgent.Safe {
		t.Fatal("expected agent to be in safe mode")
	}

	// Global safe mode
	if err := database.SetGlobalSafe(true); err != nil {
		t.Fatalf("SetGlobalSafe: %v", err)
	}
	globalSafe, _ := database.GetGlobalSafe()
	if !globalSafe {
		t.Fatal("expected global safe mode to be on")
	}

	// Secrets — AES-256-GCM round-trip
	masterKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	secret, err := database.CreateSecret(accountID, "TEST_KEY", "test", "super-secret-value", masterKey)
	if err != nil {
		t.Fatalf("CreateSecret: %v", err)
	}
	if secret.Name != "TEST_KEY" {
		t.Fatalf("expected secret name TEST_KEY, got %q", secret.Name)
	}

	// Retrieve and decrypt
	decrypted, err := database.GetSecret(accountID, "TEST_KEY", masterKey)
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if decrypted != "super-secret-value" {
		t.Fatalf("decrypted mismatch: got %q", decrypted)
	}

	// Prefix lookup (round-robin pattern)
	database.CreateSecret(accountID, "ANTHROPIC_TOKEN", "anthropic", "token1", masterKey)
	database.CreateSecret(accountID, "ANTHROPIC_TOKEN_2", "anthropic", "token2", masterKey)
	database.CreateSecret(accountID, "ANTHROPIC_TOKEN_3", "anthropic", "token3", masterKey)

	tokens, err := database.GetSecretsByPrefix(accountID, "ANTHROPIC_TOKEN", masterKey)
	if err != nil {
		t.Fatalf("GetSecretsByPrefix: %v", err)
	}
	if len(tokens) != 3 {
		t.Fatalf("expected 3 tokens, got %d", len(tokens))
	}

	// Audit log
	if err := database.LogAudit(accountID, agent.ID, "", "test_action", "test detail"); err != nil {
		t.Fatalf("LogAudit: %v", err)
	}

	// Verify audit entry exists
	var auditCount int
	database.QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'test_action'`).Scan(&auditCount)
	if auditCount != 1 {
		t.Fatalf("expected 1 audit entry, got %d", auditCount)
	}

	// Clean up
	os.RemoveAll(dir)
}
