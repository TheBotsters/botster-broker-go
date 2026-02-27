// Package api implements the REST API handlers.
package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/siofra-seksbot/botster-broker-go/internal/db"
	"github.com/siofra-seksbot/botster-broker-go/internal/hub"
)

// Server holds dependencies for API handlers.
type Server struct {
	Hub *hub.Hub
	DB        *db.DB
	MasterKey string
}

// NewRouter creates the chi router with all API routes.
func (s *Server) NewRouter() chi.Router {
	r := chi.NewRouter()

	// Health
	r.Get("/health", s.handleHealth)

	// V1 API
	r.Route("/v1", func(r chi.Router) {
		r.Post("/secrets/get", s.handleSecretsGet)
		r.Post("/secrets/list", s.handleSecretsList)
		r.Get("/actuators", s.handleActuatorsList)
		r.Post("/actuator/select", s.handleActuatorSelect)
		r.Get("/actuator/selected", s.handleActuatorSelected)
		r.Post("/command", s.handleCommand)
		r.Post("/agents/{id}/safe", s.handleAgentSafeToggle)
		r.Post("/dashboard/safe", s.handleGlobalSafeToggle)
	})

	// Account/management API
	r.Route("/api", func(r chi.Router) {
		r.Post("/accounts", s.handleCreateAccount)
		r.Get("/agents", s.handleListAgents)
		r.Post("/agents", s.handleCreateAgent)
	})

	return r
}

// --- Helpers ---

func jsonResponse(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	jsonResponse(w, status, map[string]string{"error": msg})
}

// extractToken gets the bearer token from Authorization header or X-API-Key.
func extractToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	if key := r.Header.Get("X-API-Key"); key != "" {
		return key
	}
	return ""
}

// authenticateAgent extracts token and looks up the agent.
func (s *Server) authenticateAgent(w http.ResponseWriter, r *http.Request) *db.Agent {
	token := extractToken(r)
	if token == "" {
		jsonError(w, 401, "Missing auth token")
		return nil
	}
	agent, err := s.DB.GetAgentByToken(token)
	if err != nil {
		log.Printf("Auth error: %v", err)
		jsonError(w, 500, "Internal error")
		return nil
	}
	if agent == nil {
		jsonError(w, 401, "Invalid token")
		return nil
	}
	return agent
}

// --- Handlers ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	v, _ := s.DB.SchemaVersion()
	jsonResponse(w, 200, map[string]interface{}{
		"status":         "ok",
		"schema_version": v,
	})
}

func (s *Server) handleSecretsGet(w http.ResponseWriter, r *http.Request) {
	agent := s.authenticateAgent(w, r)
	if agent == nil {
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		jsonError(w, 400, "name required")
		return
	}

	value, err := s.DB.GetSecret(agent.AccountID, body.Name, s.MasterKey)
	if err != nil {
		jsonError(w, 404, "Secret not found or decrypt failed")
		return
	}

	s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "secret_access", body.Name)
	jsonResponse(w, 200, map[string]string{"name": body.Name, "value": value})
}

func (s *Server) handleSecretsList(w http.ResponseWriter, r *http.Request) {
	agent := s.authenticateAgent(w, r)
	if agent == nil {
		return
	}

	secrets, err := s.DB.ListSecrets(agent.AccountID)
	if err != nil {
		jsonError(w, 500, "Failed to list secrets")
		return
	}

	// Return names only, not values
	names := make([]map[string]string, len(secrets))
	for i, sec := range secrets {
		names[i] = map[string]string{
			"id":       sec.ID,
			"name":     sec.Name,
			"provider": sec.Provider,
		}
	}
	jsonResponse(w, 200, names)
}

func (s *Server) handleActuatorsList(w http.ResponseWriter, r *http.Request) {
	agent := s.authenticateAgent(w, r)
	if agent == nil {
		return
	}

	var actuators []map[string]interface{}
	rows, err := s.DB.Query(`
		SELECT id, name, type, status, enabled, last_seen_at FROM actuators
		WHERE account_id = ? ORDER BY created_at
	`, agent.AccountID)
	if err != nil {
		jsonError(w, 500, "Failed to list actuators")
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id, name, aType, status string
		var enabled int
		var lastSeen *string
		rows.Scan(&id, &name, &aType, &status, &enabled, &lastSeen)
		actuators = append(actuators, map[string]interface{}{
			"id":           id,
			"name":         name,
			"type":         aType,
			"status":       status,
			"enabled":      enabled == 1,
			"last_seen_at": lastSeen,
		})
	}
	if actuators == nil {
		actuators = []map[string]interface{}{}
	}
	jsonResponse(w, 200, actuators)
}

func (s *Server) handleActuatorSelect(w http.ResponseWriter, r *http.Request) {
	agent := s.authenticateAgent(w, r)
	if agent == nil {
		return
	}

	var body struct {
		ActuatorID *string `json:"actuator_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, 400, "Invalid request body")
		return
	}

	if body.ActuatorID != nil {
		assigned, err := s.DB.IsActuatorAssignedToAgent(agent.ID, *body.ActuatorID)
		if err != nil || !assigned {
			jsonError(w, 404, "Actuator not found or not assigned to this agent")
			return
		}
	}

	if err := s.DB.SelectActuator(agent.ID, body.ActuatorID); err != nil {
		jsonError(w, 500, "Failed to select actuator")
		return
	}

	jsonResponse(w, 200, map[string]interface{}{
		"ok":                   true,
		"selected_actuator_id": body.ActuatorID,
	})
}

func (s *Server) handleActuatorSelected(w http.ResponseWriter, r *http.Request) {
	agent := s.authenticateAgent(w, r)
	if agent == nil {
		return
	}

	actuator, err := s.DB.ResolveActuatorForAgent(agent.ID)
	if err != nil {
		jsonError(w, 500, "Failed to resolve actuator")
		return
	}
	if actuator == nil {
		jsonResponse(w, 200, map[string]interface{}{"actuator_id": nil, "message": "No actuator selected"})
		return
	}

	jsonResponse(w, 200, map[string]interface{}{
		"actuator_id": actuator.ID,
		"name":        actuator.Name,
		"type":        actuator.Type,
		"status":      actuator.Status,
	})
}

func (s *Server) handleAgentSafeToggle(w http.ResponseWriter, r *http.Request) {
	agentID := chi.URLParam(r, "id")

	agent, err := s.DB.GetAgentByID(agentID)
	if err != nil || agent == nil {
		jsonError(w, 404, "Agent not found")
		return
	}

	newSafe := !agent.Safe
	if err := s.DB.SetAgentSafe(agentID, newSafe); err != nil {
		jsonError(w, 500, "Failed to toggle safe mode")
		return
	}

	action := "agent_safe_on"
	if !newSafe {
		action = "agent_safe_off"
	}
	s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, action, agent.Name)

	jsonResponse(w, 200, map[string]interface{}{
		"agent_id": agentID,
		"name":     agent.Name,
		"safe":     newSafe,
	})
}

func (s *Server) handleGlobalSafeToggle(w http.ResponseWriter, r *http.Request) {
	current, _ := s.DB.GetGlobalSafe()
	newSafe := !current

	if err := s.DB.SetGlobalSafe(newSafe); err != nil {
		jsonError(w, 500, "Failed to toggle global safe mode")
		return
	}

	s.DB.LogAudit(nil, nil, nil, "global_safe_toggle", fmt.Sprintf("%v", newSafe))

	jsonResponse(w, 200, map[string]interface{}{
		"global_safe": newSafe,
	})
}

func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Email == "" || body.Password == "" {
		jsonError(w, 400, "email and password required")
		return
	}

	acc, err := s.DB.CreateAccount(body.Email, body.Password)
	if err != nil {
		jsonError(w, 409, "Account creation failed (email may exist)")
		return
	}

	jsonResponse(w, 201, map[string]string{
		"id":    acc.ID,
		"email": acc.Email,
	})
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	agent := s.authenticateAgent(w, r)
	if agent == nil {
		return
	}

	agents, err := s.DB.ListAgentsByAccount(agent.AccountID)
	if err != nil {
		jsonError(w, 500, "Failed to list agents")
		return
	}

	result := make([]map[string]interface{}, len(agents))
	for i, a := range agents {
		result[i] = map[string]interface{}{
			"id":   a.ID,
			"name": a.Name,
			"safe": a.Safe,
		}
	}
	jsonResponse(w, 200, result)
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	// Requires account auth — for now, use an existing agent token to identify the account
	agent := s.authenticateAgent(w, r)
	if agent == nil {
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		jsonError(w, 400, "name required")
		return
	}

	newAgent, token, err := s.DB.CreateAgent(agent.AccountID, body.Name)
	if err != nil {
		jsonError(w, 409, "Agent creation failed (name may exist)")
		return
	}

	jsonResponse(w, 201, map[string]interface{}{
		"id":    newAgent.ID,
		"name":  newAgent.Name,
		"token": token,
	})
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	agent := s.authenticateAgent(w, r)
	if agent == nil {
		return
	}

	var body struct {
		Capability string          `json:"capability"`
		ActuatorID string          `json:"actuator_id,omitempty"`
		Payload    json.RawMessage `json:"payload"`
		Sync       *bool           `json:"sync,omitempty"`
		TimeoutMs  int             `json:"timeout_ms,omitempty"`
		TTL        int             `json:"ttl_seconds,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, 400, "Invalid request body")
		return
	}
	if body.Capability == "" || body.Payload == nil {
		jsonError(w, 400, "capability and payload required")
		return
	}

	commandID := fmt.Sprintf("rest_%d", time.Now().UnixMilli())

	msg := hub.WSMessage{
		Type:       hub.TypeCommandRequest,
		ID:         commandID,
		Capability: body.Capability,
		ActuatorID: body.ActuatorID,
		Payload:    body.Payload,
		TTL:        body.TTL,
	}

	// Default to sync mode
	sync := true
	if body.Sync != nil {
		sync = *body.Sync
	}

	if sync {
		timeout := 30 * time.Second
		if body.TimeoutMs > 0 && body.TimeoutMs <= 60000 {
			timeout = time.Duration(body.TimeoutMs) * time.Millisecond
		}

		result, err := s.Hub.SendCommand(agent.ID, agent.AccountID, msg, timeout)
		if err != nil {
			jsonError(w, 500, "Command dispatch failed")
			return
		}
		if result == nil {
			jsonResponse(w, 202, map[string]interface{}{
				"status":     "timeout",
				"command_id": commandID,
				"message":    "Command sent but result not received within timeout",
			})
			return
		}
		jsonResponse(w, 200, map[string]interface{}{
			"status":     result.Status,
			"command_id": commandID,
			"result":     result.Result,
		})
		return
	}

	// Async — fire and forget through hub
	s.Hub.SendCommand(agent.ID, agent.AccountID, msg, 0)
	jsonResponse(w, 200, map[string]interface{}{
		"status":     "sent",
		"command_id": commandID,
		"message":    "Command routed. Results delivered via WS to brain.",
	})
}
