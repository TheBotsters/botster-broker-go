package api

import (
	"github.com/go-chi/chi/v5"
	"fmt"
	"net/http"
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
