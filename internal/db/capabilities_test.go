package db

import (
	"testing"
)

// helper: create an account + provider + secret for capability tests.
func setupCapabilityDeps(t *testing.T, db *DB) (accountID, providerID, secretID string) {
	t.Helper()
	acc, err := db.CreateAccount("cap-test@example.com", "pass")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	prov, err := db.CreateProvider(acc.ID, "github", "GitHub", "https://api.github.com", "bearer", "Authorization")
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	secret, err := db.CreateSecret(acc.ID, "GH_PAT", "github", "ghp-test-value", testMasterKey)
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}
	return acc.ID, prov.ID, secret.ID
}

func TestCreateCapability(t *testing.T) {
	db := testDB(t)
	accID, provID, secID := setupCapabilityDeps(t, db)

	cap, err := db.CreateCapability(accID, "github-botsters", "GitHub (Botsters)", provID, secID)
	if err != nil {
		t.Fatalf("create capability: %v", err)
	}
	if cap.ID == "" {
		t.Error("expected non-empty ID")
	}
	if cap.Name != "github-botsters" {
		t.Errorf("expected name github-botsters, got %s", cap.Name)
	}
	if cap.DisplayName != "GitHub (Botsters)" {
		t.Errorf("expected display_name GitHub (Botsters), got %s", cap.DisplayName)
	}
	if cap.AccountID != accID {
		t.Errorf("account_id mismatch")
	}
	if cap.ProviderID != provID {
		t.Errorf("provider_id mismatch")
	}
	if cap.SecretID != secID {
		t.Errorf("secret_id mismatch")
	}
	if cap.CreatedAt == "" {
		t.Error("expected non-empty created_at")
	}
}

func TestCreateCapabilityDuplicateName(t *testing.T) {
	db := testDB(t)
	accID, provID, secID := setupCapabilityDeps(t, db)

	_, err := db.CreateCapability(accID, "github-botsters", "GitHub (Botsters)", provID, secID)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err = db.CreateCapability(accID, "github-botsters", "GitHub (Botsters) Dupe", provID, secID)
	if err == nil {
		t.Fatal("expected unique constraint error on duplicate name+account")
	}
}

func TestCreateCapabilityCrossAccount(t *testing.T) {
	db := testDB(t)
	accID, provID, secID := setupCapabilityDeps(t, db)

	acc2, _ := db.CreateAccount("cap-test2@example.com", "pass")
	prov2, _ := db.CreateProvider(acc2.ID, "github", "GitHub", "https://api.github.com", "bearer", "Authorization")
	sec2, _ := db.CreateSecret(acc2.ID, "GH_PAT2", "github", "ghp-other", testMasterKey)

	_, err1 := db.CreateCapability(accID, "github-botsters", "GitHub (Botsters)", provID, secID)
	_, err2 := db.CreateCapability(acc2.ID, "github-botsters", "GitHub (Botsters)", prov2.ID, sec2.ID)
	if err1 != nil || err2 != nil {
		t.Fatalf("same name on different accounts should both succeed: err1=%v err2=%v", err1, err2)
	}
}

func TestListCapabilities(t *testing.T) {
	db := testDB(t)
	accID, provID, secID := setupCapabilityDeps(t, db)

	db.CreateCapability(accID, "cap-a", "A", provID, secID)
	db.CreateCapability(accID, "cap-b", "B", provID, secID)
	db.CreateCapability(accID, "cap-c", "C", provID, secID)

	caps, err := db.ListCapabilities(accID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(caps) != 3 {
		t.Fatalf("expected 3 capabilities, got %d", len(caps))
	}
	if caps[0].Name != "cap-a" || caps[1].Name != "cap-b" || caps[2].Name != "cap-c" {
		t.Errorf("unexpected order: %s, %s, %s", caps[0].Name, caps[1].Name, caps[2].Name)
	}
}

func TestListCapabilitiesEmpty(t *testing.T) {
	db := testDB(t)
	acc, _ := db.CreateAccount("empty@example.com", "pass")

	caps, err := db.ListCapabilities(acc.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if caps != nil && len(caps) != 0 {
		t.Fatalf("expected empty list, got %d", len(caps))
	}
}

func TestGetCapabilityByName(t *testing.T) {
	db := testDB(t)
	accID, provID, secID := setupCapabilityDeps(t, db)

	created, _ := db.CreateCapability(accID, "github-botsters", "GitHub (Botsters)", provID, secID)

	found, err := db.GetCapabilityByName(accID, "github-botsters")
	if err != nil {
		t.Fatalf("get by name: %v", err)
	}
	if found == nil {
		t.Fatal("expected non-nil result")
	}
	if found.ID != created.ID {
		t.Errorf("ID mismatch: %s != %s", found.ID, created.ID)
	}
	if found.ProviderName != "github" {
		t.Errorf("expected provider name github, got %s", found.ProviderName)
	}
	if found.BaseURL != "https://api.github.com" {
		t.Errorf("expected base_url https://api.github.com, got %s", found.BaseURL)
	}
	if found.AuthType != "bearer" {
		t.Errorf("expected auth_type bearer, got %s", found.AuthType)
	}
	if found.SecretName != "GH_PAT" {
		t.Errorf("expected secret name GH_PAT, got %s", found.SecretName)
	}
}

func TestGetCapabilityByNameNotFound(t *testing.T) {
	db := testDB(t)
	acc, _ := db.CreateAccount("nf@example.com", "pass")

	found, err := db.GetCapabilityByName(acc.ID, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found != nil {
		t.Fatal("expected nil for nonexistent capability")
	}
}

func TestGetCapabilityByID(t *testing.T) {
	db := testDB(t)
	accID, provID, secID := setupCapabilityDeps(t, db)

	created, _ := db.CreateCapability(accID, "github-botsters", "GitHub (Botsters)", provID, secID)

	found, err := db.GetCapabilityByID(created.ID)
	if err != nil {
		t.Fatalf("get by ID: %v", err)
	}
	if found == nil {
		t.Fatal("expected non-nil")
	}
	if found.Name != "github-botsters" {
		t.Errorf("expected github-botsters, got %s", found.Name)
	}
}

func TestUpdateCapability(t *testing.T) {
	db := testDB(t)
	accID, provID, secID := setupCapabilityDeps(t, db)

	prov2, _ := db.CreateProvider(accID, "hetzner", "Hetzner Cloud", "https://api.hetzner.cloud/v1", "bearer", "Authorization")

	cap, _ := db.CreateCapability(accID, "github-botsters", "GitHub (Botsters)", provID, secID)

	err := db.UpdateCapability(cap.ID, "Updated Name", prov2.ID, secID)
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	found, _ := db.GetCapabilityByID(cap.ID)
	if found.DisplayName != "Updated Name" {
		t.Errorf("display_name not updated: got %s", found.DisplayName)
	}
	if found.ProviderID != prov2.ID {
		t.Errorf("provider_id not updated")
	}
}

func TestDeleteCapability(t *testing.T) {
	db := testDB(t)
	accID, provID, secID := setupCapabilityDeps(t, db)

	cap, _ := db.CreateCapability(accID, "github-botsters", "GitHub (Botsters)", provID, secID)

	if err := db.DeleteCapability(cap.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	found, _ := db.GetCapabilityByID(cap.ID)
	if found != nil {
		t.Fatal("expected nil after delete")
	}
}

func TestDeleteCapabilityCascadesGrants(t *testing.T) {
	db := testDB(t)
	accID, provID, secID := setupCapabilityDeps(t, db)
	agent, _, _ := db.CreateAgent(accID, "test-agent")

	cap, _ := db.CreateCapability(accID, "github-botsters", "GitHub (Botsters)", provID, secID)
	db.GrantCapability(cap.ID, agent.ID)

	has, _ := db.AgentHasCapability(cap.ID, agent.ID)
	if !has {
		t.Fatal("expected grant to exist before delete")
	}

	db.DeleteCapability(cap.ID)

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM capability_grants WHERE capability_id = ?`, cap.ID).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 grant rows after cascade delete, got %d", count)
	}
}

func TestGrantCapability(t *testing.T) {
	db := testDB(t)
	accID, provID, secID := setupCapabilityDeps(t, db)
	agent, _, _ := db.CreateAgent(accID, "test-agent")

	cap, _ := db.CreateCapability(accID, "github-botsters", "GitHub (Botsters)", provID, secID)

	if err := db.GrantCapability(cap.ID, agent.ID); err != nil {
		t.Fatalf("grant: %v", err)
	}

	has, err := db.AgentHasCapability(cap.ID, agent.ID)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !has {
		t.Error("expected agent to have capability after grant")
	}
}

func TestGrantCapabilityDuplicate(t *testing.T) {
	db := testDB(t)
	accID, provID, secID := setupCapabilityDeps(t, db)
	agent, _, _ := db.CreateAgent(accID, "test-agent")

	cap, _ := db.CreateCapability(accID, "github-botsters", "GitHub (Botsters)", provID, secID)

	db.GrantCapability(cap.ID, agent.ID)
	err := db.GrantCapability(cap.ID, agent.ID)
	if err != nil {
		t.Fatalf("duplicate grant should be idempotent, got: %v", err)
	}
}

func TestRevokeCapability(t *testing.T) {
	db := testDB(t)
	accID, provID, secID := setupCapabilityDeps(t, db)
	agent, _, _ := db.CreateAgent(accID, "test-agent")

	cap, _ := db.CreateCapability(accID, "github-botsters", "GitHub (Botsters)", provID, secID)
	db.GrantCapability(cap.ID, agent.ID)

	if err := db.RevokeCapability(cap.ID, agent.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	has, _ := db.AgentHasCapability(cap.ID, agent.ID)
	if has {
		t.Error("expected agent to NOT have capability after revoke")
	}
}

func TestListCapabilityGrants(t *testing.T) {
	db := testDB(t)
	accID, provID, secID := setupCapabilityDeps(t, db)
	a1, _, _ := db.CreateAgent(accID, "agent-1")
	a2, _, _ := db.CreateAgent(accID, "agent-2")

	cap, _ := db.CreateCapability(accID, "github-botsters", "GitHub (Botsters)", provID, secID)
	db.GrantCapability(cap.ID, a1.ID)
	db.GrantCapability(cap.ID, a2.ID)

	has1, _ := db.AgentHasCapability(cap.ID, a1.ID)
	has2, _ := db.AgentHasCapability(cap.ID, a2.ID)
	if !has1 || !has2 {
		t.Errorf("expected both agents to have capability: a1=%v a2=%v", has1, has2)
	}
}

func TestListAgentCapabilities(t *testing.T) {
	db := testDB(t)
	accID, provID, secID := setupCapabilityDeps(t, db)

	prov2, _ := db.CreateProvider(accID, "hetzner", "Hetzner Cloud", "https://api.hetzner.cloud/v1", "bearer", "Authorization")
	sec2, _ := db.CreateSecret(accID, "HETZNER_TOKEN", "hetzner", "hz-test", testMasterKey)

	agent, _, _ := db.CreateAgent(accID, "test-agent")

	cap1, _ := db.CreateCapability(accID, "github-botsters", "GitHub (Botsters)", provID, secID)
	cap2, _ := db.CreateCapability(accID, "hetzner-cloud", "Hetzner Cloud", prov2.ID, sec2.ID)

	db.GrantCapability(cap1.ID, agent.ID)
	db.GrantCapability(cap2.ID, agent.ID)

	caps, err := db.ListAgentCapabilities(accID, agent.ID)
	if err != nil {
		t.Fatalf("list agent capabilities: %v", err)
	}
	if len(caps) != 2 {
		t.Fatalf("expected 2 capabilities, got %d", len(caps))
	}

	// Ordered by name
	if caps[0].Name != "github-botsters" {
		t.Errorf("expected first cap github-botsters, got %s", caps[0].Name)
	}
	if caps[0].ProviderName != "github" {
		t.Errorf("expected provider name github, got %s", caps[0].ProviderName)
	}
	if caps[0].BaseURL != "https://api.github.com" {
		t.Errorf("expected base_url https://api.github.com, got %s", caps[0].BaseURL)
	}
	if caps[0].AuthType != "bearer" {
		t.Errorf("expected auth_type bearer, got %s", caps[0].AuthType)
	}

	if caps[1].Name != "hetzner-cloud" {
		t.Errorf("expected second cap hetzner-cloud, got %s", caps[1].Name)
	}
	if caps[1].ProviderName != "hetzner" {
		t.Errorf("expected provider name hetzner, got %s", caps[1].ProviderName)
	}
}

func TestListAgentCapabilitiesUngranted(t *testing.T) {
	db := testDB(t)
	accID, provID, secID := setupCapabilityDeps(t, db)
	agent, _, _ := db.CreateAgent(accID, "test-agent")

	db.CreateCapability(accID, "github-botsters", "GitHub (Botsters)", provID, secID)

	caps, err := db.ListAgentCapabilities(accID, agent.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(caps) != 0 {
		t.Fatalf("expected 0 capabilities for ungranted agent, got %d", len(caps))
	}
}
