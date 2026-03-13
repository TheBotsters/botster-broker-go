package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TheBotsters/botster-broker-go/internal/config"
	"github.com/TheBotsters/botster-broker-go/internal/db"
)

const testMK = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// setupCapabilityServer creates a test server with an account, agent (with token),
// provider, secret, and capability. Returns what you need for tests.
type capTestEnv struct {
	server      *httptest.Server
	accountID   string
	agentID     string
	agentToken  string
	providerID  string
	secretID    string
	capID       string
	capName     string
}

func setupCapabilityServer(t *testing.T) *capTestEnv {
	t.Helper()
	d := testDB(t)

	acc, _ := d.CreateAccount("cap@example.com", "pass")
	agent, token, _ := d.CreateAgent(acc.ID, "test-agent")
	prov, _ := d.CreateProvider(acc.ID, "github", "GitHub", "https://api.github.com", "bearer", "Authorization")
	secret, _ := d.CreateSecret(acc.ID, "GH_PAT", "github", "ghp-test-value", testMK)
	cap, _ := d.CreateCapability(acc.ID, "github-botsters", "GitHub (Botsters)", prov.ID, secret.ID)
	d.GrantCapability(cap.ID, agent.ID)

	s := &Server{
		DB:        d,
		MasterKey: testMK,
		Config:    &config.Config{},
	}
	ts := httptest.NewServer(s.NewRouter())
	t.Cleanup(ts.Close)

	return &capTestEnv{
		server:     ts,
		accountID:  acc.ID,
		agentID:    agent.ID,
		agentToken: token,
		providerID: prov.ID,
		secretID:   secret.ID,
		capID:      cap.ID,
		capName:    "github-botsters",
	}
}

// ─── Management API Tests ──────────────────────────────────────────────────────

func TestCapabilitiesCRUD_API(t *testing.T) {
	d := testDB(t)
	acc, _ := d.CreateAccount("crud@example.com", "pass")
	prov, _ := d.CreateProvider(acc.ID, "github", "GitHub", "https://api.github.com", "bearer", "Authorization")
	secret, _ := d.CreateSecret(acc.ID, "GH_PAT", "github", "ghp-val", testMK)

	s := &Server{DB: d, MasterKey: testMK, Config: &config.Config{}}
	ts := httptest.NewServer(s.NewRouter())
	defer ts.Close()

	// CREATE
	body, _ := json.Marshal(map[string]string{
		"account_id":   acc.ID,
		"name":         "github-test",
		"display_name": "GitHub Test",
		"provider_id":  prov.ID,
		"secret_id":    secret.ID,
	})
	req, _ := http.NewRequest("POST", ts.URL+"/api/capabilities", bytes.NewReader(body))
	req.Header.Set("X-Admin-Key", testMK)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, string(b))
	}
	var created db.Capability
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.Name != "github-test" {
		t.Errorf("expected name github-test, got %s", created.Name)
	}

	// LIST
	req, _ = http.NewRequest("GET", ts.URL+"/api/capabilities?account_id="+acc.ID, nil)
	req.Header.Set("X-Admin-Key", testMK)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("list: expected 200, got %d", resp.StatusCode)
	}
	var caps []db.Capability
	json.NewDecoder(resp.Body).Decode(&caps)
	resp.Body.Close()
	if len(caps) != 1 {
		t.Fatalf("expected 1 capability, got %d", len(caps))
	}

	// UPDATE
	body, _ = json.Marshal(map[string]string{"display_name": "Updated"})
	req, _ = http.NewRequest("PUT", ts.URL+"/api/capabilities/"+created.ID, bytes.NewReader(body))
	req.Header.Set("X-Admin-Key", testMK)
	req.Header.Set("Content-Type", "application/json")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("update: expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// DELETE
	req, _ = http.NewRequest("DELETE", ts.URL+"/api/capabilities/"+created.ID, nil)
	req.Header.Set("X-Admin-Key", testMK)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("delete: expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Verify deleted
	req, _ = http.NewRequest("GET", ts.URL+"/api/capabilities?account_id="+acc.ID, nil)
	req.Header.Set("X-Admin-Key", testMK)
	resp, _ = http.DefaultClient.Do(req)
	json.NewDecoder(resp.Body).Decode(&caps)
	resp.Body.Close()
	if len(caps) != 0 {
		t.Fatalf("expected 0 after delete, got %d", len(caps))
	}
}

func TestCapabilityGrantRevoke_API(t *testing.T) {
	d := testDB(t)
	acc, _ := d.CreateAccount("grant@example.com", "pass")
	agent, _, _ := d.CreateAgent(acc.ID, "test-agent")
	prov, _ := d.CreateProvider(acc.ID, "github", "GitHub", "https://api.github.com", "bearer", "Authorization")
	secret, _ := d.CreateSecret(acc.ID, "GH_PAT", "github", "ghp-val", testMK)
	cap, _ := d.CreateCapability(acc.ID, "github-test", "GitHub", prov.ID, secret.ID)

	s := &Server{DB: d, MasterKey: testMK, Config: &config.Config{}}
	ts := httptest.NewServer(s.NewRouter())
	defer ts.Close()

	// GRANT
	body, _ := json.Marshal(map[string]string{"agent_id": agent.ID})
	req, _ := http.NewRequest("POST", ts.URL+"/api/capabilities/"+cap.ID+"/grant", bytes.NewReader(body))
	req.Header.Set("X-Admin-Key", testMK)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("grant: expected 200, got %d: %s", resp.StatusCode, string(b))
	}
	resp.Body.Close()

	// Verify grant via DB
	has, _ := d.AgentHasCapability(cap.ID, agent.ID)
	if !has {
		t.Fatal("expected grant to exist")
	}

	// REVOKE
	req, _ = http.NewRequest("DELETE", fmt.Sprintf("%s/api/capabilities/%s/grant/%s", ts.URL, cap.ID, agent.ID), nil)
	req.Header.Set("X-Admin-Key", testMK)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("revoke: expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	has, _ = d.AgentHasCapability(cap.ID, agent.ID)
	if has {
		t.Fatal("expected grant to be revoked")
	}
}

func TestCapabilitiesAPI_Unauthorized(t *testing.T) {
	d := testDB(t)
	s := &Server{DB: d, MasterKey: testMK, Config: &config.Config{}}
	ts := httptest.NewServer(s.NewRouter())
	defer ts.Close()

	// No auth header
	req, _ := http.NewRequest("GET", ts.URL+"/api/capabilities?account_id=foo", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 401 && resp.StatusCode != 403 {
		t.Fatalf("expected 401 or 403, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestCapabilitiesAPI_NotFound(t *testing.T) {
	d := testDB(t)
	s := &Server{DB: d, MasterKey: testMK, Config: &config.Config{}}
	ts := httptest.NewServer(s.NewRouter())
	defer ts.Close()

	// PUT on nonexistent
	body, _ := json.Marshal(map[string]string{"display_name": "X"})
	req, _ := http.NewRequest("PUT", ts.URL+"/api/capabilities/nonexistent", bytes.NewReader(body))
	req.Header.Set("X-Admin-Key", testMK)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// DELETE on nonexistent
	req, _ = http.NewRequest("DELETE", ts.URL+"/api/capabilities/nonexistent", nil)
	req.Header.Set("X-Admin-Key", testMK)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// ─── Agent-facing: /v1/capabilities ────────────────────────────────────────────

func TestV1Capabilities_ReturnsGranted(t *testing.T) {
	env := setupCapabilityServer(t)

	// Create a second capability that is NOT granted
	d := env.server.Client()
	_ = d // we already have one granted via setupCapabilityServer

	req, _ := http.NewRequest("POST", env.server.URL+"/v1/capabilities", nil)
	req.Header.Set("Authorization", "Bearer "+env.agentToken)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(b))
	}

	var result struct {
		Agent        string `json:"agent"`
		Capabilities []struct {
			Name        string `json:"name"`
			DisplayName string `json:"display_name"`
			Provider    string `json:"provider"`
		} `json:"capabilities"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()

	if result.Agent != "test-agent" {
		t.Errorf("expected agent test-agent, got %s", result.Agent)
	}
	if len(result.Capabilities) != 1 {
		t.Fatalf("expected 1 capability, got %d", len(result.Capabilities))
	}
	if result.Capabilities[0].Name != "github-botsters" {
		t.Errorf("expected name github-botsters, got %s", result.Capabilities[0].Name)
	}
	if result.Capabilities[0].Provider != "github" {
		t.Errorf("expected provider github, got %s", result.Capabilities[0].Provider)
	}
	if result.Capabilities[0].DisplayName != "GitHub (Botsters)" {
		t.Errorf("expected display_name GitHub (Botsters), got %s", result.Capabilities[0].DisplayName)
	}
}

func TestV1Capabilities_ReturnsNone(t *testing.T) {
	d := testDB(t)
	acc, _ := d.CreateAccount("none@example.com", "pass")
	_, token, _ := d.CreateAgent(acc.ID, "lonely-agent")

	s := &Server{DB: d, MasterKey: testMK, Config: &config.Config{}}
	ts := httptest.NewServer(s.NewRouter())
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/v1/capabilities", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result struct {
		Capabilities []interface{} `json:"capabilities"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()

	if len(result.Capabilities) != 0 {
		t.Fatalf("expected 0 capabilities, got %d", len(result.Capabilities))
	}
}

// ─── Agent-facing: /v1/proxy/request ───────────────────────────────────────────

func TestProxyRequest_WithCapability(t *testing.T) {
	// Start an upstream server that echoes back what it receives
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]string{
			"method": r.Method,
			"path":   r.URL.Path,
			"auth":   auth,
		})
	}))
	defer upstream.Close()

	// Set up broker with provider pointing at our upstream
	d := testDB(t)
	acc, _ := d.CreateAccount("proxy@example.com", "pass")
	agent, token, _ := d.CreateAgent(acc.ID, "proxy-agent")
	prov, _ := d.CreateProvider(acc.ID, "testapi", "Test API", upstream.URL, "bearer", "Authorization")
	secret, _ := d.CreateSecret(acc.ID, "TEST_TOKEN", "testapi", "secret-value-123", testMK)
	cap, _ := d.CreateCapability(acc.ID, "test-api", "Test API", prov.ID, secret.ID)
	d.GrantCapability(cap.ID, agent.ID)

	s := &Server{DB: d, MasterKey: testMK, Config: &config.Config{}}
	ts := httptest.NewServer(s.NewRouter())
	defer ts.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"capability": "test-api",
		"method":     "GET",
		"url":        upstream.URL + "/repos",
		"headers":    map[string]string{"Accept": "application/json"},
	})
	req, _ := http.NewRequest("POST", ts.URL+"/v1/proxy/request", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(b))
	}

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()

	if result["auth"] != "Bearer secret-value-123" {
		t.Errorf("expected broker to inject Bearer secret-value-123, got %q", result["auth"])
	}
	if result["method"] != "GET" {
		t.Errorf("expected GET, got %s", result["method"])
	}
}

func TestProxyRequest_NoGrant(t *testing.T) {
	d := testDB(t)
	acc, _ := d.CreateAccount("nogrant@example.com", "pass")
	_, token, _ := d.CreateAgent(acc.ID, "no-grant-agent")
	prov, _ := d.CreateProvider(acc.ID, "github", "GitHub", "https://api.github.com", "bearer", "Authorization")
	secret, _ := d.CreateSecret(acc.ID, "GH_PAT", "github", "ghp-val", testMK)
	d.CreateCapability(acc.ID, "github-test", "GitHub", prov.ID, secret.ID)
	// Note: NOT granting to agent

	s := &Server{DB: d, MasterKey: testMK, Config: &config.Config{}}
	ts := httptest.NewServer(s.NewRouter())
	defer ts.Close()

	body, _ := json.Marshal(map[string]string{
		"capability": "github-test",
		"method":     "GET",
		"url":        "https://api.github.com/repos",
	})
	req, _ := http.NewRequest("POST", ts.URL+"/v1/proxy/request", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)

	if resp.StatusCode != 403 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 403, got %d: %s", resp.StatusCode, string(b))
	}
	resp.Body.Close()
}

func TestProxyRequest_UnknownCapability(t *testing.T) {
	d := testDB(t)
	acc, _ := d.CreateAccount("unknown@example.com", "pass")
	_, token, _ := d.CreateAgent(acc.ID, "agent")

	s := &Server{DB: d, MasterKey: testMK, Config: &config.Config{}}
	ts := httptest.NewServer(s.NewRouter())
	defer ts.Close()

	body, _ := json.Marshal(map[string]string{
		"capability": "nonexistent",
		"method":     "GET",
		"url":        "https://example.com",
	})
	req, _ := http.NewRequest("POST", ts.URL+"/v1/proxy/request", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)

	if resp.StatusCode != 404 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 404, got %d: %s", resp.StatusCode, string(b))
	}
	resp.Body.Close()
}

func TestProxyRequest_CapabilityFieldRequired(t *testing.T) {
	d := testDB(t)
	acc, _ := d.CreateAccount("nofld@example.com", "pass")
	_, token, _ := d.CreateAgent(acc.ID, "agent")

	s := &Server{DB: d, MasterKey: testMK, Config: &config.Config{}}
	ts := httptest.NewServer(s.NewRouter())
	defer ts.Close()

	body, _ := json.Marshal(map[string]string{
		"method": "GET",
		"url":    "https://example.com",
	})
	req, _ := http.NewRequest("POST", ts.URL+"/v1/proxy/request", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(req)

	if resp.StatusCode != 400 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400, got %d: %s", resp.StatusCode, string(b))
	}
	resp.Body.Close()
}

// ─── Deprecated endpoint removal ───────────────────────────────────────────────

func TestSecretsListRemoved(t *testing.T) {
	d := testDB(t)
	acc, _ := d.CreateAccount("rm@example.com", "pass")
	_, token, _ := d.CreateAgent(acc.ID, "agent")

	s := &Server{DB: d, MasterKey: testMK, Config: &config.Config{}}
	ts := httptest.NewServer(s.NewRouter())
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/v1/secrets/list", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := http.DefaultClient.Do(req)

	// Should be 404 or 405 (route doesn't exist)
	if resp.StatusCode != 404 && resp.StatusCode != 405 {
		t.Fatalf("expected 404/405 for removed endpoint, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}
