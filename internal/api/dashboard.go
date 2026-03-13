package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// handleDashboardData returns all dashboard data for the authenticated user's account.
// GET /dashboard/api/data (session auth via requireAuth middleware)
func (s *Server) handleDashboardData(w http.ResponseWriter, r *http.Request) {
	accountID := r.Header.Get("X-Account-ID")
	if accountID == "" {
		jsonError(w, 401, "Not authenticated")
		return
	}

	// Agents
	agents, err := s.DB.ListAgentsByAccount(accountID)
	if err != nil {
		jsonError(w, 500, "Failed to list agents")
		return
	}
	agentList := make([]map[string]interface{}, len(agents))
	for i, a := range agents {
		agentList[i] = map[string]interface{}{
			"id":   a.ID,
			"name": a.Name,
			"safe": a.Safe,
		}
	}

	// Actuators
	var actuatorList []map[string]interface{}
	rows, err := s.DB.Query(`
		SELECT id, name, type, status, enabled, last_seen_at FROM actuators
		WHERE account_id = ? ORDER BY created_at
	`, accountID)
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

		// Check if this actuator is connected via WebSocket
		wsStatus := "offline"
		if s.Hub.IsActuatorConnected(id) {
			wsStatus = "online"
		}

		actuatorList = append(actuatorList, map[string]interface{}{
			"id":           id,
			"name":         name,
			"type":         aType,
			"status":       wsStatus,
			"enabled":      enabled == 1,
			"last_seen_at": lastSeen,
		})
	}
	if actuatorList == nil {
		actuatorList = []map[string]interface{}{}
	}

	// Secret count
	secrets, err := s.DB.ListSecrets(accountID)
	secretCount := 0
	if err == nil {
		secretCount = len(secrets)
	}

	// Global safe mode
	globalSafe, _ := s.DB.GetGlobalSafe()

	jsonResponse(w, 200, map[string]interface{}{
		"agents":       agentList,
		"actuators":    actuatorList,
		"secret_count": secretCount,
		"global_safe":  globalSafe,
	})
}

// handleDashboardSafeToggle toggles global safe mode.
// POST /dashboard/api/safe (session auth)
func (s *Server) handleDashboardSafeToggle(w http.ResponseWriter, r *http.Request) {
	accountID := r.Header.Get("X-Account-ID")
	if accountID == "" {
		jsonError(w, 401, "Not authenticated")
		return
	}

	current, _ := s.DB.GetGlobalSafe()
	newSafe := !current

	if err := s.DB.SetGlobalSafe(newSafe); err != nil {
		jsonError(w, 500, "Failed to toggle global safe mode")
		return
	}

	s.DB.LogAudit(nil, nil, nil, "global_safe_toggle", fmt.Sprintf("%v", newSafe))
	jsonResponse(w, 200, map[string]interface{}{"global_safe": newSafe})
}

// handleDashboardAgentSafeToggle toggles safe mode for a specific agent.
// POST /dashboard/api/agents/{id}/safe (session auth)
func (s *Server) handleDashboardAgentSafeToggle(w http.ResponseWriter, r *http.Request) {
	accountID := r.Header.Get("X-Account-ID")
	if accountID == "" {
		jsonError(w, 401, "Not authenticated")
		return
	}

	agentID := chi.URLParam(r, "id")
	agent, err := s.DB.GetAgentByID(agentID)
	if err != nil || agent == nil {
		jsonError(w, 404, "Agent not found")
		return
	}

	// Verify agent belongs to this account
	if agent.AccountID != accountID {
		jsonError(w, 403, "Agent does not belong to this account")
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

// GET /dashboard/api/actuators/{id}/capabilities
func (s *Server) handleDashboardActuatorCapabilitiesGet(w http.ResponseWriter, r *http.Request) {
	accountID := r.Header.Get("X-Account-ID")
	if accountID == "" {
		jsonError(w, 401, "Not authenticated")
		return
	}
	actuatorID := chi.URLParam(r, "id")
	actuator, err := s.DB.GetActuatorByID(actuatorID)
	if err != nil || actuator == nil {
		jsonError(w, 404, "Actuator not found")
		return
	}
	if actuator.AccountID != accountID {
		jsonError(w, 403, "Actuator does not belong to this account")
		return
	}
	caps, err := s.DB.ListActuatorCapabilities(actuatorID)
	if err != nil {
		jsonError(w, 500, "Failed to list actuator capabilities")
		return
	}
	jsonResponse(w, 200, map[string]interface{}{"actuator_id": actuatorID, "capabilities": caps})
}

// POST /dashboard/api/actuators/{id}/capabilities
func (s *Server) handleDashboardActuatorCapabilitiesSet(w http.ResponseWriter, r *http.Request) {
	accountID := r.Header.Get("X-Account-ID")
	if accountID == "" {
		jsonError(w, 401, "Not authenticated")
		return
	}
	actuatorID := chi.URLParam(r, "id")
	actuator, err := s.DB.GetActuatorByID(actuatorID)
	if err != nil || actuator == nil {
		jsonError(w, 404, "Actuator not found")
		return
	}
	if actuator.AccountID != accountID {
		jsonError(w, 403, "Actuator does not belong to this account")
		return
	}
	var body struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, 400, "Invalid request body")
		return
	}
	norm := make([]string, 0, len(body.Capabilities))
	seen := map[string]bool{}
	for _, c := range body.Capabilities {
		c = strings.TrimSpace(strings.ToLower(c))
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		norm = append(norm, c)
	}
	if err := s.DB.ReplaceActuatorCapabilities(actuatorID, norm); err != nil {
		jsonError(w, 500, "Failed to update actuator capabilities")
		return
	}
	s.DB.LogAudit(&accountID, nil, &actuatorID, "actuator.capabilities.update", strings.Join(norm, ","))
	jsonResponse(w, 200, map[string]interface{}{"ok": true, "actuator_id": actuatorID, "capabilities": norm})
}

// GET /dashboard/api/actuators/{id}/logs?limit=100
func (s *Server) handleDashboardActuatorLogs(w http.ResponseWriter, r *http.Request) {
	accountID := r.Header.Get("X-Account-ID")
	if accountID == "" {
		jsonError(w, 401, "Not authenticated")
		return
	}
	actuatorID := chi.URLParam(r, "id")
	actuator, err := s.DB.GetActuatorByID(actuatorID)
	if err != nil || actuator == nil {
		jsonError(w, 404, "Actuator not found")
		return
	}
	if actuator.AccountID != accountID {
		jsonError(w, 403, "Actuator does not belong to this account")
		return
	}
	limit := 100
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	entries, err := s.DB.ListAuditLog(&accountID, limit)
	if err != nil {
		jsonError(w, 500, "Failed to query logs")
		return
	}
	out := []map[string]interface{}{}
	for _, e := range entries {
		if e.ActuatorID != nil && *e.ActuatorID == actuatorID {
			out = append(out, map[string]interface{}{
				"id": e.ID, "action": e.Action, "detail": e.Detail, "created_at": e.CreatedAt,
			})
		}
	}
	jsonResponse(w, 200, map[string]interface{}{"actuator_id": actuatorID, "entries": out})
}

// GET /dashboard/api/inference/tail?limit=100
func (s *Server) handleDashboardInferenceTail(w http.ResponseWriter, r *http.Request) {
	accountID := r.Header.Get("X-Account-ID")
	if accountID == "" {
		jsonError(w, 401, "Not authenticated")
		return
	}
	limit := 100
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	entries, err := s.DB.ListAuditLog(&accountID, limit)
	if err != nil {
		jsonError(w, 500, "Failed to query logs")
		return
	}
	out := []map[string]interface{}{}
	for _, e := range entries {
		if strings.HasPrefix(e.Action, "inference.") {
			out = append(out, map[string]interface{}{
				"id": e.ID, "agent_id": e.AgentID, "action": e.Action, "detail": e.Detail, "created_at": e.CreatedAt,
			})
		}
	}
	jsonResponse(w, 200, map[string]interface{}{"entries": out, "server_time": time.Now().UTC().Format(time.RFC3339)})
}
