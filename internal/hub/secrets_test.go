package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestSecretsGetEndpointReturnsKnownKey(t *testing.T) {
	h := &Hub{}
	store := NewSecretsStore(map[string]string{
		"openai:embedding": "sk-embedding-test-key",
	})

	r := chi.NewRouter()
	r.With(h.RequireBrokerToken("test-broker-token")).Post("/v1/broker/secrets/get", h.HandleSecretGet(store))

	body := map[string]string{"key": "openai:embedding"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/broker/secrets/get", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-broker-token")

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var out struct {
		Value  string `json:"value"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if out.Value != "sk-embedding-test-key" {
		t.Fatalf("expected embedding key, got %q", out.Value)
	}
	if out.Source != "broker" {
		t.Fatalf("expected source=broker, got %q", out.Source)
	}
}
