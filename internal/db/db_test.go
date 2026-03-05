package db

import (
	"os"
	"testing"
	"time"
)

const testMasterKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testDB(t *testing.T) *DB {
	t.Helper()
	path := t.TempDir() + "/test.db"
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
		os.Remove(path)
	})
	return db
}

func TestMigrations(t *testing.T) {
	db := testDB(t)
	v, err := db.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v != 6 {
		t.Errorf("expected schema version 6, got %d", v)
	}
}

func TestAccountCRUD(t *testing.T) {
	db := testDB(t)

	acc, err := db.CreateAccount("test@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if acc.Email != "test@example.com" {
		t.Errorf("expected email test@example.com, got %s", acc.Email)
	}

	found, err := db.GetAccountByEmail("test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if found == nil {
		t.Fatal("expected to find account")
	}
	if !found.VerifyPassword("password123") {
		t.Error("password verification failed")
	}
	if found.VerifyPassword("wrong") {
		t.Error("wrong password should not verify")
	}
}

func TestAgentCRUD(t *testing.T) {
	db := testDB(t)

	acc, _ := db.CreateAccount("test@example.com", "pass")
	agent, token, err := db.CreateAgent(acc.ID, "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Name != "test-agent" {
		t.Errorf("expected name test-agent, got %s", agent.Name)
	}

	// Lookup by token
	found, err := db.GetAgentByToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if found == nil {
		t.Fatal("expected to find agent by token")
	}
	if found.ID != agent.ID {
		t.Error("agent ID mismatch")
	}

	// Safe mode
	if err := db.SetAgentSafe(agent.ID, true); err != nil {
		t.Fatal(err)
	}
	updated, _ := db.GetAgentByID(agent.ID)
	if !updated.Safe {
		t.Error("expected agent to be in safe mode")
	}
}

func TestActuatorAndAssignment(t *testing.T) {
	db := testDB(t)

	acc, _ := db.CreateAccount("test@example.com", "pass")
	agent, _, _ := db.CreateAgent(acc.ID, "test-agent")
	actuator, _, err := db.CreateActuator(acc.ID, "test-actuator", "vps")
	if err != nil {
		t.Fatal(err)
	}

	// Assign
	if err := db.AssignActuatorToAgent(agent.ID, actuator.ID); err != nil {
		t.Fatal(err)
	}
	assigned, err := db.IsActuatorAssignedToAgent(agent.ID, actuator.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !assigned {
		t.Error("expected actuator to be assigned")
	}

	// Resolve
	resolved, err := db.ResolveActuatorForAgent(agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved == nil {
		t.Fatal("expected to resolve actuator")
	}
	if resolved.ID != actuator.ID {
		t.Error("resolved wrong actuator")
	}
}

func TestSecretEncryption(t *testing.T) {
	db := testDB(t)

	acc, _ := db.CreateAccount("test@example.com", "pass")

	// Create secret
	_, err := db.CreateSecret(acc.ID, "ANTHROPIC_TOKEN", "anthropic", "sk-ant-secret-value", testMasterKey)
	if err != nil {
		t.Fatal(err)
	}

	// Retrieve and decrypt
	value, err := db.GetSecret(acc.ID, "ANTHROPIC_TOKEN", testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	if value != "sk-ant-secret-value" {
		t.Errorf("expected sk-ant-secret-value, got %s", value)
	}

	// Prefix lookup (for round-robin)
	db.CreateSecret(acc.ID, "ANTHROPIC_TOKEN_2", "anthropic", "sk-ant-second", testMasterKey)
	db.CreateSecret(acc.ID, "ANTHROPIC_TOKEN_3", "anthropic", "sk-ant-third", testMasterKey)

	values, err := db.GetSecretsByPrefix(acc.ID, "ANTHROPIC_TOKEN", testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 3 {
		t.Errorf("expected 3 secrets, got %d", len(values))
	}
}

func TestGlobalSafeMode(t *testing.T) {
	db := testDB(t)

	safe, _ := db.GetGlobalSafe()
	if safe {
		t.Error("expected global safe mode off by default")
	}

	db.SetGlobalSafe(true)
	safe, _ = db.GetGlobalSafe()
	if !safe {
		t.Error("expected global safe mode on")
	}

	db.SetGlobalSafe(false)
	safe, _ = db.GetGlobalSafe()
	if safe {
		t.Error("expected global safe mode off")
	}
}

func TestAuditLog(t *testing.T) {
	db := testDB(t)

	acc, _ := db.CreateAccount("test@example.com", "pass")
	err := db.LogAudit(&acc.ID, nil, nil, "test_action", "test detail")
	if err != nil {
		t.Fatal(err)
	}

	// Verify it's in the DB
	var count int
	db.QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'test_action'`).Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 audit entry, got %d", count)
	}
}

func TestGetSecretForAgentACLPolicy(t *testing.T) {
	db := testDB(t)

	acc, _ := db.CreateAccount("acl@example.com", "pass")
	a1, _, _ := db.CreateAgent(acc.ID, "agent-1")
	a2, _, _ := db.CreateAgent(acc.ID, "agent-2")
	otherAcc, _ := db.CreateAccount("other@example.com", "pass")
	otherAgent, _, _ := db.CreateAgent(otherAcc.ID, "other-agent")

	secret, err := db.CreateSecret(acc.ID, "BOTSTERSORG_GITHUB_PERSONAL_ACCESS_TOKEN", "github", "ghp-test", testMasterKey)
	if err != nil {
		t.Fatal(err)
	}

	// Default (no ACL rows): any agent in same account can read.
	if _, err := db.GetSecretForAgent(acc.ID, a1.ID, secret.Name, testMasterKey); err != nil {
		t.Fatalf("agent-1 should have default account-level access: %v", err)
	}
	if _, err := db.GetSecretForAgent(acc.ID, a2.ID, secret.Name, testMasterKey); err != nil {
		t.Fatalf("agent-2 should have default account-level access: %v", err)
	}

	// Agent from another account must never pass account scope check.
	if _, err := db.GetSecretForAgent(acc.ID, otherAgent.ID, secret.Name, testMasterKey); err == nil {
		t.Fatalf("expected cross-account access to be denied")
	}

	// Once ACL rows exist, access becomes explicit allow-list.
	if err := db.GrantSecretAccess(secret.ID, a1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetSecretForAgent(acc.ID, a1.ID, secret.Name, testMasterKey); err != nil {
		t.Fatalf("agent-1 should be allowed via explicit grant: %v", err)
	}
	if _, err := db.GetSecretForAgent(acc.ID, a2.ID, secret.Name, testMasterKey); err == nil {
		t.Fatalf("agent-2 should be denied once ACL is enabled and not granted")
	}

	if err := db.GrantSecretAccess(secret.ID, a2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetSecretForAgent(acc.ID, a2.ID, secret.Name, testMasterKey); err != nil {
		t.Fatalf("agent-2 should be allowed after grant: %v", err)
	}
}

func TestGetSecretForAgentWrongMasterKeyLooksLikeAccessDenied(t *testing.T) {
	db := testDB(t)

	acc, _ := db.CreateAccount("key-mismatch@example.com", "pass")
	agent, _, _ := db.CreateAgent(acc.ID, "agent")
	secret, err := db.CreateSecret(acc.ID, "OPENAI_API_KEY", "openai", "sk-test", testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.GrantSecretAccess(secret.ID, agent.ID); err != nil {
		t.Fatal(err)
	}

	wrongKey := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	if _, err := db.GetSecretForAgent(acc.ID, agent.ID, secret.Name, wrongKey); err == nil {
		t.Fatalf("expected failure with wrong master key")
	}
}

func TestAgentTokenRotationGracePeriod(t *testing.T) {
	db := testDB(t)

	acc, _ := db.CreateAccount("rotation@example.com", "pass")
	agent, oldToken, err := db.CreateAgent(acc.ID, "rotate-agent")
	if err != nil {
		t.Fatal(err)
	}

	// Rotate with a generous grace period.
	newToken, err := db.RotateAgentToken(agent.ID, 5*time.Minute, testMasterKey)
	if err != nil {
		t.Fatalf("rotate token: %v", err)
	}
	if newToken == oldToken {
		t.Fatal("new token should differ from old token")
	}

	// New token works immediately.
	found, err := db.GetAgentByToken(newToken)
	if err != nil {
		t.Fatalf("new token lookup: %v", err)
	}
	if found == nil || found.ID != agent.ID {
		t.Fatal("expected to find agent by new token")
	}

	// Old token still accepted during grace period.
	found, err = db.GetAgentByToken(oldToken)
	if err != nil {
		t.Fatalf("old token lookup: %v", err)
	}
	if found == nil || found.ID != agent.ID {
		t.Fatal("expected to find agent by old token during grace period")
	}
}

func TestAgentTokenRotationExpiredGrace(t *testing.T) {
	db := testDB(t)

	acc, _ := db.CreateAccount("expired@example.com", "pass")
	agent, oldToken, err := db.CreateAgent(acc.ID, "expire-agent")
	if err != nil {
		t.Fatal(err)
	}

	// Rotate with a normal grace period first.
	newToken, err := db.RotateAgentToken(agent.ID, 5*time.Minute, testMasterKey)
	if err != nil {
		t.Fatalf("rotate token: %v", err)
	}

	// Force the expiry into the past to simulate grace period elapsed.
	_, err = db.Exec(`UPDATE agents SET token_rotation_expires_at = datetime('now', '-1 minute') WHERE id = ?`, agent.ID)
	if err != nil {
		t.Fatalf("force expiry: %v", err)
	}

	// New token still works.
	found, err := db.GetAgentByToken(newToken)
	if err != nil {
		t.Fatalf("new token lookup: %v", err)
	}
	if found == nil || found.ID != agent.ID {
		t.Fatal("expected to find agent by new token")
	}

	// Old token rejected after grace expired.
	found, err = db.GetAgentByToken(oldToken)
	if err != nil {
		t.Fatalf("old token lookup error: %v", err)
	}
	if found != nil {
		t.Fatal("expected old token to be rejected after grace period")
	}
}

func TestActuatorTokenRotationGracePeriod(t *testing.T) {
	db := testDB(t)

	acc, _ := db.CreateAccount("act-rotation@example.com", "pass")
	actuator, oldToken, err := db.CreateActuator(acc.ID, "rotate-actuator", "vps")
	if err != nil {
		t.Fatal(err)
	}

	newToken, err := db.RotateActuatorToken(actuator.ID, 5*time.Minute, testMasterKey)
	if err != nil {
		t.Fatalf("rotate token: %v", err)
	}

	// New token works.
	found, err := db.GetActuatorByToken(newToken)
	if err != nil {
		t.Fatalf("new token lookup: %v", err)
	}
	if found == nil || found.ID != actuator.ID {
		t.Fatal("expected to find actuator by new token")
	}

	// Old token accepted during grace.
	found, err = db.GetActuatorByToken(oldToken)
	if err != nil {
		t.Fatalf("old token lookup: %v", err)
	}
	if found == nil || found.ID != actuator.ID {
		t.Fatal("expected to find actuator by old token during grace period")
	}
}
