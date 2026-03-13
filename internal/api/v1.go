// Package api implements the REST API handlers.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/TheBotsters/botster-broker-go/internal/auth"
	"github.com/TheBotsters/botster-broker-go/internal/config"
	"github.com/TheBotsters/botster-broker-go/internal/db"
	"github.com/TheBotsters/botster-broker-go/internal/hub"
	"github.com/TheBotsters/botster-broker-go/internal/tap"
	"github.com/go-chi/chi/v5"
)

// Server holds dependencies for API handlers.
type Server struct {
	Sessions  *SessionStore
	Gateways  map[string]GatewayConfig
	Hub       *hub.Hub
	DB        *db.DB
	MasterKey string
	Tap       *tap.InferenceTap
	Config    *config.Config
}

// scopedCapsKey is the context key for scoped token capabilities.
type contextKey int

const scopedCapsKey contextKey = iota

// NewRouter creates the chi router with all API routes.
func (s *Server) NewRouter() chi.Router {
	r := chi.NewRouter()

	// Health
	r.Get("/health", s.handleHealth)

	// V1 API
	r.Route("/v1", func(r chi.Router) {
		r.Post("/tokens/scoped", s.handleCreateScopedToken)
		r.Get("/actuators", s.handleActuatorsList)
		r.Post("/actuator/select", s.handleActuatorSelect)
		r.Get("/actuator/selected", s.handleActuatorSelected)
		r.Post("/command", s.handleCommand)
		r.Post("/agents/{id}/safe", s.handleAgentSafeToggle)
		r.Post("/dashboard/safe", s.handleGlobalSafeToggle)
		// Inference proxy routes
		r.Post("/inference", s.handleInference)
		r.Get("/inference/providers", s.handleInferenceProviders)
		r.Post("/proxy/anthropic/*", s.handleProxyAnthropic)
		r.Post("/proxy/openai/*", s.handleProxyOpenAI)
		r.Post("/web/search", s.handleWebSearch)
		r.Post("/capabilities", s.handleCapabilities)
		r.Post("/proxy/request", s.handleProxyRequest)
		// Notify (root only)
		r.Post("/notify/{agentName}", s.handleNotify)
	})

	// Account/management API
	r.Route("/api", func(r chi.Router) {
		// Inference tap SSE stream (dashboard)
		r.Get("/inference/stream", s.handleInferenceStream)
		// Existing (root only — create account)
		r.Post("/accounts", s.handleCreateAccount)
		r.Get("/agents", s.handleListAgents)
		r.Post("/agents", s.handleCreateAgent)

		// Account CRUD (root only)
		r.Get("/accounts", s.handleListAccounts)
		r.Get("/accounts/{id}", s.handleGetAccount)
		r.Patch("/accounts/{id}", s.handleUpdateAccount)
		r.Delete("/accounts/{id}", s.handleDeleteAccount)

		// Agent management under account
		r.Post("/accounts/{id}/agents", s.handleCreateAgentForAccount)
		r.Get("/accounts/{id}/agents", s.handleListAgentsForAccount)
		r.Delete("/accounts/{id}/agents/{agentId}", s.handleDeleteAgentFromAccount)
		r.Patch("/accounts/{id}/agents/{agentId}", s.handleUpdateAgentInAccount)
		r.Post("/accounts/{id}/agents/{agentId}/rotate-token", s.handleRotateAgentToken)

		// Actuator management (root or admin scoped)
		r.Post("/agents/{agentId}/actuators", s.handleCreateActuatorForAgent)
		r.Delete("/actuators/{id}", s.handleDeleteActuator)

		// Agent group assignment (root only)
		r.Post("/agents/{id}/assign-group", s.handleAssignAgentToGroup)

		// Interchange export/import (root only)
		r.Get("/export", s.handleExportInterchange)
		r.Post("/import", s.handleImportInterchange)

		// Secret retrieval (dashboard only — not agent-facing)
		r.Post("/secrets/get", s.handleSecretsGet)

		// Secret management (root or admin scoped)
		r.Post("/secrets", s.handleCreateSecret)
		r.Put("/secrets/{id}", s.handleUpdateSecret)
		r.Post("/secrets/{id}/grant", s.handleGrantSecretAdmin)

		// Capability management (root or admin scoped)
		r.Post("/capabilities", s.handleCreateCapability)
		r.Get("/capabilities", s.handleListCapabilities)
		r.Put("/capabilities/{id}", s.handleUpdateCapability)
		r.Delete("/capabilities/{id}", s.handleDeleteCapability)
		r.Post("/capabilities/{id}/grant", s.handleGrantCapability)
		r.Delete("/capabilities/{id}/grant/{agentId}", s.handleRevokeCapability)

		// Provider management (root or admin scoped)
		r.Post("/providers", s.handleCreateProvider)
		r.Get("/providers", s.handleListProviders)
		r.Put("/providers/{id}", s.handleUpdateProvider)
		r.Delete("/providers/{id}", s.handleDeleteProvider)

		// Audit log (root or admin scoped)
		r.Get("/audit", s.handleListAudit)

		// Group management (root only, with group-admin reads)
		r.Post("/groups", s.handleCreateGroup)
		r.Get("/groups", s.handleListGroups)
		r.Patch("/groups/{id}", s.handleUpdateGroup)
		r.Delete("/groups/{id}", s.handleDeleteGroup)
		r.Post("/groups/{id}/rotate-key", s.handleRotateGroupKey)
		r.Get("/groups/{id}/agents", s.handleListGroupAgents)
	})

	// Root redirects to dashboard
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	})

	// Login page redirect
	r.Get("/login", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "web/login.html")
	})

	// Auth routes (no auth required)
	r.Post("/auth/login", s.handleLogin)
	r.Post("/auth/logout", s.handleLogout)
	r.Get("/auth/status", s.handleAuthStatus)

	// Chat routes (session auth required)
	r.Route("/chat", func(r chi.Router) {
		r.Use(s.requireAuth)
		r.Get("/", s.handleChatIndex)
		r.Get("/api/agents", s.handleChatAgentList)
		r.Get("/{agent}/ws", s.handleChatProxy)
		r.Get("/{agent}/", s.handleChatPage)
	})

	// Secrets page (session auth required)
	r.Route("/secrets", func(r chi.Router) {
		r.Use(s.requireAuth)
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, "web/secrets.html")
		})
		r.Get("/api/list", s.handleWebSecretsList)
		r.Get("/api/agents", s.handleWebSecretsAgents)
		r.Post("/api/create", s.handleWebSecretsCreate)
		r.Put("/api/{id}", s.handleWebSecretsUpdate)
		r.Post("/api/{id}/grant", s.handleWebSecretsGrant)
	})

	// Dashboard (session auth required)
	r.Route("/dashboard", func(r chi.Router) {
		r.Use(s.requireAuth)
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, "web/index.html")
		})
		r.Get("/api/data", s.handleDashboardData)
		r.Post("/api/safe", s.handleDashboardSafeToggle)
		r.Post("/api/agents/{id}/safe", s.handleDashboardAgentSafeToggle)
		r.Get("/api/actuators/{id}/capabilities", s.handleDashboardActuatorCapabilitiesGet)
		r.Post("/api/actuators/{id}/capabilities", s.handleDashboardActuatorCapabilitiesSet)
		r.Get("/api/actuators/{id}/logs", s.handleDashboardActuatorLogs)
		r.Get("/api/inference/tail", s.handleDashboardInferenceTail)
		r.Get("/api/inference/stream", s.handleInferenceStream)
	})

	// Sync routes
	s.RegisterSyncRoutes(r)

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
	a := r.Header.Get("Authorization")
	if strings.HasPrefix(a, "Bearer ") {
		return strings.TrimPrefix(a, "Bearer ")
	}
	if key := r.Header.Get("X-API-Key"); key != "" {
		return key
	}
	return ""
}

// withScopedCaps stores scoped token capabilities in the request context.
func withScopedCaps(r *http.Request, caps []string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), scopedCapsKey, caps))
}

// getScopedCaps retrieves scoped capabilities from context.
// Returns nil if the request was not made with a scoped token (full access).
func getScopedCaps(r *http.Request) []string {
	v := r.Context().Value(scopedCapsKey)
	if v == nil {
		return nil
	}
	caps, _ := v.([]string)
	return caps
}

// checkScopedCapability returns true if the request is allowed to use requiredCap.
// If the request carries no scoped caps (regular agent token), always returns true.
func checkScopedCapability(r *http.Request, requiredCap string) bool {
	caps := getScopedCaps(r)
	if caps == nil {
		return true // not a scoped token — full access
	}
	for _, c := range caps {
		if c == requiredCap || c == "*" {
			return true
		}
	}
	return false
}

func secretNameToCapability(name string) string {
	upper := strings.ToUpper(name)
	switch {
	case strings.Contains(upper, "ANTHROPIC") || strings.Contains(upper, "CLAUDE"):
		return "anthropic"
	case strings.Contains(upper, "OPENAI"):
		return "openai"
	case strings.Contains(upper, "BRAVE"):
		return "brave"
	default:
		return ""
	}
}

// authenticateAgent extracts token and looks up the agent.
// For scoped tokens, it writes caps into the request context pointer so callers
// can use checkScopedCapability(r, ...) after this call.
// The caller must pass &r (pointer to the local *http.Request variable) so the
// updated context is visible in the same handler scope.
func (s *Server) authenticateAgent(w http.ResponseWriter, r *http.Request) *db.Agent {
	token := extractToken(r)
	if token == "" {
		jsonError(w, 401, "[BSA:SPINE/API] Missing auth token")
		return nil
	}

	// Handle scoped tokens — verify HMAC, inject caps into ctx
	if auth.IsScopedToken(token) {
		payload, err := auth.VerifyScopedToken(token, s.MasterKey)
		if err != nil {
			jsonError(w, 401, "[BSA:SPINE/API] Invalid or expired scoped token: "+err.Error())
			return nil
		}
		agent, err := s.DB.GetAgentByID(payload.AgentID)
		if err != nil || agent == nil {
			jsonError(w, 401, "[BSA:SPINE/API] Scoped token references unknown agent")
			return nil
		}
		// Inject scoped caps into the request context.
		// We set a value on the *existing* context; callers reading r.Context() will see it.
		enriched := withScopedCaps(r, payload.Caps)
		*r = *enriched
		return agent
	}

	agent, err := s.DB.GetAgentByToken(token)
	if err != nil {
		log.Printf("Auth error: %v", err)
		jsonError(w, 500, "[BSA:SPINE/API] Internal error")
		return nil
	}
	if agent == nil {
		jsonError(w, 401, "[BSA:SPINE/API] Invalid token")
		return nil
	}
	return agent
}

// --- Handlers ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	v, _ := s.DB.SchemaVersion()
	jsonResponse(w, 200, map[string]interface{}{
		"status":         hub.StatusOK,
		"schema_version": v,
	})
}

// handleSecretsGet is dashboard/management only — agents cannot call this.
func (s *Server) handleSecretsGet(w http.ResponseWriter, r *http.Request) {
	isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
	if !ok {
		return
	}

	var body struct {
		AccountID string `json:"account_id"`
		Name      string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		jsonError(w, 400, "[BSA:SPINE/API] name required")
		return
	}

	accountID := body.AccountID
	if accountID == "" && !isRoot {
		accountID = adminAgent.AccountID
	}
	if accountID == "" {
		jsonError(w, 400, "[BSA:SPINE/API] account_id required")
		return
	}
	if !isRoot && !requireAccountScope(adminAgent.AccountID, accountID) {
		jsonError(w, 403, "[BSA:SPINE/API] Forbidden: account scope violation")
		return
	}

	value, err := s.DB.GetSecret(accountID, body.Name, s.MasterKey)
	if err != nil {
		jsonError(w, 404, "[BSA:SPINE/API] Secret not found or decrypt failed")
		return
	}

	s.DB.LogAudit(&accountID, nil, nil, "secret_access.dashboard", body.Name)
	jsonResponse(w, 200, map[string]string{"name": body.Name, "value": value})
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
		jsonError(w, 500, "[BSA:SPINE/API] Failed to list actuators")
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
		jsonError(w, 400, "[BSA:SPINE/API] Invalid request body")
		return
	}

	if body.ActuatorID != nil {
		assigned, err := s.DB.IsActuatorAssignedToAgent(agent.ID, *body.ActuatorID)
		if err != nil || !assigned {
			jsonError(w, 404, "[BSA:SPINE/API] Actuator not found or not assigned to this agent")
			return
		}
	}

	if err := s.DB.SelectActuator(agent.ID, body.ActuatorID); err != nil {
		jsonError(w, 500, "[BSA:SPINE/API] Failed to select actuator")
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
		jsonError(w, 500, "[BSA:SPINE/API] Failed to resolve actuator")
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
		jsonError(w, 404, "[BSA:SPINE/API] Agent not found")
		return
	}

	newSafe := !agent.Safe
	if err := s.DB.SetAgentSafe(agentID, newSafe); err != nil {
		jsonError(w, 500, "[BSA:SPINE/API] Failed to toggle safe mode")
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
		jsonError(w, 500, "[BSA:SPINE/API] Failed to toggle global safe mode")
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
		jsonError(w, 400, "[BSA:SPINE/API] email and password required")
		return
	}

	acc, err := s.DB.CreateAccount(body.Email, body.Password)
	if err != nil {
		jsonError(w, 409, "[BSA:SPINE/API] Account creation failed (email may exist)")
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
		jsonError(w, 500, "[BSA:SPINE/API] Failed to list agents")
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
		jsonError(w, 400, "[BSA:SPINE/API] name required")
		return
	}

	newAgent, token, err := s.DB.CreateAgent(agent.AccountID, body.Name)
	if err != nil {
		jsonError(w, 409, "[BSA:SPINE/API] Agent creation failed (name may exist)")
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
		jsonError(w, 400, "[BSA:SPINE/API] Invalid request body")
		return
	}
	if body.Capability == "" || body.Payload == nil {
		jsonError(w, 400, "[BSA:SPINE/API] capability and payload required")
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
			jsonError(w, 500, "[BSA:SPINE/API] Command dispatch failed")
			return
		}
		if result == nil {
			jsonResponse(w, 202, map[string]interface{}{
				"status":     hub.StatusTimeout,
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
		"status":     hub.StatusSent,
		"command_id": commandID,
		"message":    "Command routed. Results delivered via WS to brain.",
	})
}

// handleWebSecretsList returns secrets for the authenticated web user.
// GET /secrets/api/list
func (s *Server) handleWebSecretsList(w http.ResponseWriter, r *http.Request) {
	accountID := r.Header.Get("X-Account-ID")
	if accountID == "" {
		jsonError(w, 401, "[BSA:SPINE/API] Not authenticated")
		return
	}

	secrets, err := s.DB.ListSecrets(accountID)
	if err != nil {
		jsonError(w, 500, "[BSA:SPINE/API] Failed to list secrets")
		return
	}

	// Return secret metadata (no encrypted values)
	result := make([]map[string]interface{}, 0, len(secrets))
	for _, sec := range secrets {
		grants, _ := s.DB.ListSecretGrantAgentNames(sec.ID)
		result = append(result, map[string]interface{}{
			"id":         sec.ID,
			"name":       sec.Name,
			"provider":   sec.Provider,
			"created_at": sec.CreatedAt,
			"updated_at": sec.UpdatedAt,
			"grants":     grants,
		})
	}
	jsonResponse(w, 200, result)
}
