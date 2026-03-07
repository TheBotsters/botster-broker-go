package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/siofra-seksbot/botster-broker-go/internal/api"
	"github.com/siofra-seksbot/botster-broker-go/internal/db"
	"github.com/siofra-seksbot/botster-broker-go/internal/hub"
)

func TestSecretsRoutesAuthDomains(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "broker.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	acc, err := database.CreateAccount("staging@test.local", "password")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	_, agentToken, err := database.CreateAgent(acc.ID, "mocksiofra")
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}

	masterKey := "test-master-key-0123456789abcdef0123456789abcdef"
	wsHub := hub.New(database, masterKey)
	srv := &api.Server{
		Sessions:  api.NewSessionStore(24 * time.Hour),
		Gateways:  map[string]api.GatewayConfig{},
		Hub:       wsHub,
		DB:        database,
		MasterKey: masterKey,
	}

	router := srv.NewRouter()
	brokerSecrets := hub.NewSecretsStore(map[string]string{"probe:key": "probe-value"})
	router.With(wsHub.RequireBrokerToken("test-broker-token")).Post("/v1/broker/secrets/get", wsHub.HandleSecretGet(brokerSecrets))

	ts := httptest.NewServer(router)
	defer ts.Close()

	t.Run("agent token works on /v1/actuators", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/actuators", nil)
		req.Header.Set("Authorization", "Bearer "+agentToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
	})

	t.Run("agent token on /v1/secrets/get hits API auth domain (not broker-token middleware)", func(t *testing.T) {
		body, _ := json.Marshal(map[string]string{"name": "does-not-exist"})
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/secrets/get", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+agentToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()

		// If route collision reappears, this would be 401 from broker-token middleware.
		// Correct API path with valid agent token should proceed to lookup and return 404.
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected 404 from API secret lookup, got %d", resp.StatusCode)
		}
	})

	t.Run("/v1/broker/secrets/get requires broker token", func(t *testing.T) {
		body, _ := json.Marshal(map[string]string{"key": "probe:key"})

		// Agent token must be rejected on broker secrets endpoint.
		req1, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/broker/secrets/get", bytes.NewReader(body))
		req1.Header.Set("Authorization", "Bearer "+agentToken)
		req1.Header.Set("Content-Type", "application/json")
		resp1, err := http.DefaultClient.Do(req1)
		if err != nil {
			t.Fatalf("request1: %v", err)
		}
		defer resp1.Body.Close()
		if resp1.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 with agent token, got %d", resp1.StatusCode)
		}

		// Broker token should succeed.
		req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/broker/secrets/get", bytes.NewReader(body))
		req2.Header.Set("Authorization", "Bearer test-broker-token")
		req2.Header.Set("Content-Type", "application/json")
		resp2, err := http.DefaultClient.Do(req2)
		if err != nil {
			t.Fatalf("request2: %v", err)
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 with broker token, got %d", resp2.StatusCode)
		}
	})
}
