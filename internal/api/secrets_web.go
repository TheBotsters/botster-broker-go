package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// POST /secrets/api/create
func (s *Server) handleWebSecretsCreate(w http.ResponseWriter, r *http.Request) {
	accountID := r.Header.Get("X-Account-ID")
	if accountID == "" {
		jsonError(w, 401, "Not authenticated")
		return
	}

	var body struct {
		Name     string `json:"name"`
		Provider string `json:"provider"`
		Value    string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, 400, "Invalid request body")
		return
	}
	if body.Name == "" || body.Provider == "" || body.Value == "" {
		jsonError(w, 400, "name, provider, value required")
		return
	}

	secret, err := s.DB.CreateSecret(accountID, body.Name, body.Provider, body.Value, s.MasterKey)
	if err != nil {
		jsonError(w, 409, "Secret creation failed (name may exist)")
		return
	}
	s.DB.LogAudit(&accountID, nil, nil, "secret.create", body.Name)
	jsonResponse(w, 201, map[string]interface{}{"id": secret.ID, "name": secret.Name, "provider": secret.Provider})
}

// PUT /secrets/api/{id}
func (s *Server) handleWebSecretsUpdate(w http.ResponseWriter, r *http.Request) {
	accountID := r.Header.Get("X-Account-ID")
	if accountID == "" {
		jsonError(w, 401, "Not authenticated")
		return
	}
	secretID := chi.URLParam(r, "id")
	secret, err := s.DB.GetSecretByID(secretID)
	if err != nil || secret == nil {
		jsonError(w, 404, "Secret not found")
		return
	}
	if secret.AccountID != accountID {
		jsonError(w, 403, "Forbidden")
		return
	}

	var body struct{ Value string `json:"value"` }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Value == "" {
		jsonError(w, 400, "value required")
		return
	}
	if err := s.DB.UpdateSecretByID(secretID, body.Value, s.MasterKey); err != nil {
		jsonError(w, 500, "Failed to update secret")
		return
	}
	s.DB.LogAudit(&accountID, nil, nil, "secret.update", secret.Name)
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

// GET /secrets/api/agents
func (s *Server) handleWebSecretsAgents(w http.ResponseWriter, r *http.Request) {
	accountID := r.Header.Get("X-Account-ID")
	if accountID == "" {
		jsonError(w, 401, "Not authenticated")
		return
	}
	agents, err := s.DB.ListAgentsByAccount(accountID)
	if err != nil {
		jsonError(w, 500, "Failed to list agents")
		return
	}
	out := make([]map[string]interface{}, 0, len(agents))
	for _, a := range agents {
		out = append(out, map[string]interface{}{"id": a.ID, "name": a.Name, "safe": a.Safe})
	}
	jsonResponse(w, 200, out)
}

// POST /secrets/api/{id}/grant
