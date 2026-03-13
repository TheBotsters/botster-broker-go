// Package api — account management, agent/actuator CRUD, secret admin, audit log.
package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/TheBotsters/botster-broker-go/internal/db"
	"github.com/go-chi/chi/v5"
)

// ─── Account CRUD (root only) ──────────────────────────────────────────────────

// GET /api/accounts
func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(r) {
		jsonError(w, 401, "[BSA:SPINE/ADMIN] Unauthorized: invalid or missing X-Admin-Key")
		return
	}
	accounts, err := s.DB.ListAccounts()
	if err != nil {
		jsonError(w, 500, "[BSA:SPINE/ADMIN] Failed to list accounts")
		return
	}
	result := make([]map[string]interface{}, len(accounts))
	for i, a := range accounts {
		result[i] = map[string]interface{}{
			"id":         a.ID,
			"email":      a.Email,
			"created_at": a.CreatedAt,
		}
	}
	jsonResponse(w, 200, result)
}

// GET /api/accounts/{id}
func (s *Server) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(r) {
		jsonError(w, 401, "[BSA:SPINE/ADMIN] Unauthorized: invalid or missing X-Admin-Key")
		return
	}
	id := chi.URLParam(r, "id")
	acc, err := s.DB.GetAccountByID(id)
	if err != nil || acc == nil {
		jsonError(w, 404, "[BSA:SPINE/ADMIN] Account not found")
		return
	}
	jsonResponse(w, 200, map[string]interface{}{
		"id":         acc.ID,
		"email":      acc.Email,
		"created_at": acc.CreatedAt,
	})
}

// PATCH /api/accounts/{id}
func (s *Server) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(r) {
		jsonError(w, 401, "[BSA:SPINE/ADMIN] Unauthorized: invalid or missing X-Admin-Key")
		return
	}
	id := chi.URLParam(r, "id")
	acc, err := s.DB.GetAccountByID(id)
	if err != nil || acc == nil {
		jsonError(w, 404, "[BSA:SPINE/ADMIN] Account not found")
		return
	}

	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, 400, "[BSA:SPINE/ADMIN] Invalid request body")
		return
	}

	// Only allow safe fields
	updates := map[string]interface{}{}
	for _, field := range []string{"email", "name", "plan", "status"} {
		if v, ok := body[field]; ok {
			updates[field] = v
		}
	}

	if err := s.DB.UpdateAccount(id, updates); err != nil {
		jsonError(w, 500, "[BSA:SPINE/ADMIN] Failed to update account")
		return
	}
	s.DB.LogAudit(&id, nil, nil, "account.update", "")

	updated, _ := s.DB.GetAccountByID(id)
	jsonResponse(w, 200, map[string]interface{}{
		"id":         updated.ID,
		"email":      updated.Email,
		"created_at": updated.CreatedAt,
	})
}

// DELETE /api/accounts/{id}
func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(r) {
		jsonError(w, 401, "[BSA:SPINE/ADMIN] Unauthorized: invalid or missing X-Admin-Key")
		return
	}
	id := chi.URLParam(r, "id")
	acc, err := s.DB.GetAccountByID(id)
	if err != nil || acc == nil {
		jsonError(w, 404, "[BSA:SPINE/ADMIN] Account not found")
		return
	}
	if err := s.DB.DeleteAccount(id); err != nil {
		jsonError(w, 500, "[BSA:SPINE/ADMIN] Failed to delete account")
		return
	}
	s.DB.LogAudit(&id, nil, nil, "account.delete", acc.Email)
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

// ─── Agent Management under Account ───────────────────────────────────────────

// formatAgent formats a single agent for API responses.
func formatAgent(a *db.Agent) map[string]interface{} {
	groupID := interface{}(nil)
	if a.GroupID.Valid {
		groupID = a.GroupID.String
	}
	return map[string]interface{}{
		"id":         a.ID,
		"name":       a.Name,
		"role":       a.Role,
		"safe":       a.Safe,
		"group_id":   groupID,
		"created_at": a.CreatedAt,
	}
}

// formatAgents formats a slice of agents for API responses.
func formatAgents(agents []*db.Agent) []map[string]interface{} {
	result := make([]map[string]interface{}, len(agents))
	for i, a := range agents {
		result[i] = formatAgent(a)
	}
	return result
}

// formatAudit formats audit entries for API responses.
func formatAudit(entries []*db.AuditEntry) []map[string]interface{} {
	result := make([]map[string]interface{}, len(entries))
	for i, e := range entries {
		result[i] = map[string]interface{}{
			"id":          e.ID,
			"account_id":  e.AccountID,
			"agent_id":    e.AgentID,
			"actuator_id": e.ActuatorID,
			"action":      e.Action,
			"detail":      e.Detail,
			"created_at":  e.CreatedAt,
		}
	}
	return result
}

// POST /api/accounts/{id}/agents — root only
func (s *Server) handleCreateAgentForAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(r) {
		jsonError(w, 401, "[BSA:SPINE/ADMIN] Unauthorized: invalid or missing X-Admin-Key")
		return
	}
	accountID := chi.URLParam(r, "id")
	acc, err := s.DB.GetAccountByID(accountID)
	if err != nil || acc == nil {
		jsonError(w, 404, "[BSA:SPINE/ADMIN] Account not found")
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		jsonError(w, 400, "[BSA:SPINE/ADMIN] name required")
		return
	}

	newAgent, token, err := s.DB.CreateAgent(accountID, body.Name)
	if err != nil {
		jsonError(w, 409, "[BSA:SPINE/ADMIN] Agent creation failed (name may exist)")
		return
	}
	s.DB.LogAudit(&accountID, &newAgent.ID, nil, "agent.create", newAgent.Name)

	jsonResponse(w, 201, map[string]interface{}{
		"id":         newAgent.ID,
		"name":       newAgent.Name,
		"role":       newAgent.Role,
		"token":      token,
		"created_at": newAgent.CreatedAt,
	})
}

// GET /api/accounts/{id}/agents — root, admin (scoped), or group admin (group-scoped)
func (s *Server) handleListAgentsForAccount(w http.ResponseWriter, r *http.Request) {
	accountID := chi.URLParam(r, "id")

	// Root sees all agents in the account
	if s.requireRoot(r) {
		acc, err := s.DB.GetAccountByID(accountID)
		if err != nil || acc == nil {
			jsonError(w, 404, "[BSA:SPINE/ADMIN] Account not found")
			return
		}
		agents, err := s.DB.ListAgentsByAccount(accountID)
		if err != nil {
			jsonError(w, 500, "[BSA:SPINE/ADMIN] Failed to list agents")
			return
		}
		jsonResponse(w, 200, formatAgents(agents))
		return
	}

	// Group admin — sees only their group's agents (filtered by account for safety)
	if group, ok := s.requireGroupAdmin(w, r); ok {
		if group.AccountID != accountID {
			jsonError(w, 403, "[BSA:SPINE/ADMIN] Forbidden: group not in this account")
			return
		}
		agents, err := s.DB.GetAgentsByGroup(group.ID)
		if err != nil {
			jsonError(w, 500, "[BSA:SPINE/ADMIN] Failed to list agents")
			return
		}
		jsonResponse(w, 200, formatAgents(agents))
		return
	}

	// Account admin — scoped to own account
	isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
	if !ok {
		return
	}
	if !isRoot && !requireAccountScope(adminAgent.AccountID, accountID) {
		jsonError(w, 403, "[BSA:SPINE/ADMIN] Forbidden: account scope violation")
		return
	}

	acc, err := s.DB.GetAccountByID(accountID)
	if err != nil || acc == nil {
		jsonError(w, 404, "[BSA:SPINE/ADMIN] Account not found")
		return
	}

	agents, err := s.DB.ListAgentsByAccount(accountID)
	if err != nil {
		jsonError(w, 500, "[BSA:SPINE/ADMIN] Failed to list agents")
		return
	}
	jsonResponse(w, 200, formatAgents(agents))
}

// DELETE /api/accounts/{id}/agents/{agentId} — root only
func (s *Server) handleDeleteAgentFromAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(r) {
		jsonError(w, 401, "[BSA:SPINE/ADMIN] Unauthorized: invalid or missing X-Admin-Key")
		return
	}
	accountID := chi.URLParam(r, "id")
	agentID := chi.URLParam(r, "agentId")

	agent, err := s.DB.GetAgentByID(agentID)
	if err != nil || agent == nil || agent.AccountID != accountID {
		jsonError(w, 404, "[BSA:SPINE/ADMIN] Agent not found")
		return
	}
	if err := s.DB.DeleteAgent(agentID); err != nil {
		jsonError(w, 500, "[BSA:SPINE/ADMIN] Failed to delete agent")
		return
	}
	s.DB.LogAudit(&accountID, &agentID, nil, "agent.delete", agent.Name)
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

// PATCH /api/accounts/{id}/agents/{agentId}
// Auth: root (full access), group admin (safe mode only, scoped to group), account admin (name + safe).
func (s *Server) handleUpdateAgentInAccount(w http.ResponseWriter, r *http.Request) {
	accountID := chi.URLParam(r, "id")
	agentID := chi.URLParam(r, "agentId")

	agent, err := s.DB.GetAgentByID(agentID)
	if err != nil || agent == nil || agent.AccountID != accountID {
		jsonError(w, 404, "[BSA:SPINE/ADMIN] Agent not found")
		return
	}

	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, 400, "[BSA:SPINE/ADMIN] Invalid request body")
		return
	}

	// Root — can update anything including role
	if s.requireRoot(r) {
		updates := buildAgentUpdatesRoot(body)
		if err := s.DB.UpdateAgent(agentID, updates); err != nil {
			jsonError(w, 500, "[BSA:SPINE/ADMIN] Failed to update agent")
			return
		}
		s.DB.LogAudit(&accountID, &agentID, nil, "agent.update", agent.Name)
		updated, _ := s.DB.GetAgentByID(agentID)
		jsonResponse(w, 200, formatAgent(updated))
		return
	}

	// Group admin — safe mode only, scoped to group
	if group, ok := s.requireGroupAdmin(w, r); ok {
		if !s.isAgentInGroup(agentID, group.ID) {
			jsonError(w, 403, "[BSA:SPINE/ADMIN] Agent not in your group")
			return
		}
		updates := buildSafeUpdate(body)
		if len(updates) == 0 {
			jsonError(w, 400, "[BSA:SPINE/ADMIN] Group admin can only update 'safe' field")
			return
		}
		if err := s.DB.UpdateAgent(agentID, updates); err != nil {
			jsonError(w, 500, "[BSA:SPINE/ADMIN] Failed to update agent")
			return
		}
		s.DB.LogAudit(&accountID, &agentID, nil, "agent.update", agent.Name)
		updated, _ := s.DB.GetAgentByID(agentID)
		jsonResponse(w, 200, formatAgent(updated))
		return
	}

	// Account admin — name + safe, no role changes
	isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
	if !ok {
		return
	}
	if !isRoot && !requireAccountScope(adminAgent.AccountID, accountID) {
		jsonError(w, 403, "[BSA:SPINE/ADMIN] Forbidden: account scope violation")
		return
	}
	updates := buildAgentUpdatesAdmin(body)
	if err := s.DB.UpdateAgent(agentID, updates); err != nil {
		jsonError(w, 500, "[BSA:SPINE/ADMIN] Failed to update agent")
		return
	}
	s.DB.LogAudit(&accountID, &agentID, nil, "agent.update", agent.Name)
	updated, _ := s.DB.GetAgentByID(agentID)
	jsonResponse(w, 200, formatAgent(updated))
}

// buildAgentUpdatesRoot builds update map for root — allows role, name, safe.
func buildAgentUpdatesRoot(body map[string]interface{}) map[string]interface{} {
	updates := map[string]interface{}{}
	if v, ok := body["role"]; ok {
		role, _ := v.(string)
		if role == "agent" || role == "admin" || role == "operator" {
			updates["role"] = role
		}
	}
	if v, ok := body["name"]; ok {
		updates["name"] = v
	}
	if upd := buildSafeUpdate(body); len(upd) > 0 {
		for k, v := range upd {
			updates[k] = v
		}
	}
	return updates
}

// buildAgentUpdatesAdmin builds update map for account admin — name + safe only.
func buildAgentUpdatesAdmin(body map[string]interface{}) map[string]interface{} {
	updates := map[string]interface{}{}
	if v, ok := body["name"]; ok {
		updates["name"] = v
	}
	if upd := buildSafeUpdate(body); len(upd) > 0 {
		for k, v := range upd {
			updates[k] = v
		}
	}
	return updates
}

// buildSafeUpdate builds update map for safe field only.
func buildSafeUpdate(body map[string]interface{}) map[string]interface{} {
	updates := map[string]interface{}{}
	if v, ok := body["safe"]; ok {
		if safeBool, ok := v.(bool); ok {
			safeInt := 0
			if safeBool {
				safeInt = 1
			}
			updates["safe"] = safeInt
		}
	}
	return updates
}

// POST /api/accounts/{id}/agents/{agentId}/rotate-token
// Auth: root (any agent), group admin (scoped to group), account admin (own token only).
func (s *Server) handleRotateAgentToken(w http.ResponseWriter, r *http.Request) {
	accountID := chi.URLParam(r, "id")
	agentID := chi.URLParam(r, "agentId")

	target, err := s.DB.GetAgentByID(agentID)
	if err != nil || target == nil || target.AccountID != accountID {
		jsonError(w, 404, "[BSA:SPINE/ADMIN] Agent not found")
		return
	}

	// ROOT ONLY — no admin agent tokens allowed
	// This prevents accidental rotation via API calls
	// UI can still rotate using root (master key) authentication
	if !s.requireRoot(r) {
		jsonError(w, 403, "[BSA:SPINE/ADMIN] Forbidden: token rotation requires root access")
		return
	}

	newToken, err := s.DB.RotateAgentToken(agentID, 15*time.Minute, s.MasterKey)
	if err != nil {
		jsonError(w, 500, "[BSA:SPINE/ADMIN] Failed to rotate token")
		return
	}

	s.Hub.NotifyAgentTokenRotated(agentID, newToken)
	s.DB.LogAudit(&accountID, &agentID, nil, "agent.rotate-token", target.Name)
	jsonResponse(w, 200, map[string]interface{}{
		"ok":    true,
		"token": newToken,
		"note":  "Token rotated. Old token valid for 15-minute grace period.",
	})
}

// ─── Actuator Management ───────────────────────────────────────────────────────

// POST /api/agents/{agentId}/actuators — root or admin (scoped), or group admin (group-scoped)
func (s *Server) handleCreateActuatorForAgent(w http.ResponseWriter, r *http.Request) {
	agentID := chi.URLParam(r, "agentId")

	agentTarget, err := s.DB.GetAgentByID(agentID)
	if err != nil || agentTarget == nil {
		jsonError(w, 404, "[BSA:SPINE/ADMIN] Agent not found")
		return
	}

	// Check auth: root > group admin > account admin
	authorized := false
	if s.requireRoot(r) {
		authorized = true
	} else if group, ok := s.requireGroupAdmin(w, r); ok {
		if s.isAgentInGroup(agentID, group.ID) {
			authorized = true
		} else {
			jsonError(w, 403, "[BSA:SPINE/ADMIN] Agent not in your group")
			return
		}
	} else {
		isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
		if !ok {
			return
		}
		if isRoot || requireAccountScope(adminAgent.AccountID, agentTarget.AccountID) {
			authorized = true
		}
	}
	if !authorized {
		jsonError(w, 403, "[BSA:SPINE/ADMIN] Forbidden")
		return
	}

	var body struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		jsonError(w, 400, "[BSA:SPINE/ADMIN] name required")
		return
	}
	if body.Type == "" {
		body.Type = "vps"
	}

	actuator, token, err := s.DB.CreateActuator(agentTarget.AccountID, body.Name, body.Type)
	if err != nil {
		jsonError(w, 409, "[BSA:SPINE/ADMIN] Actuator creation failed")
		return
	}

	// Auto-assign to the agent
	if err := s.DB.AssignActuatorToAgent(agentID, actuator.ID); err != nil {
		jsonError(w, 500, "[BSA:SPINE/ADMIN] Actuator created but assignment failed")
		return
	}
	s.DB.LogAudit(&agentTarget.AccountID, &agentID, &actuator.ID, "actuator.create", actuator.Name)

	jsonResponse(w, 201, map[string]interface{}{
		"id":         actuator.ID,
		"name":       actuator.Name,
		"type":       actuator.Type,
		"token":      token,
		"created_at": actuator.CreatedAt,
	})
}

// DELETE /api/actuators/{id} — root or admin (scoped), or group admin (group-scoped)
func (s *Server) handleDeleteActuator(w http.ResponseWriter, r *http.Request) {
	actuatorID := chi.URLParam(r, "id")

	actuator, err := s.DB.GetActuatorByID(actuatorID)
	if err != nil || actuator == nil {
		jsonError(w, 404, "[BSA:SPINE/ADMIN] Actuator not found")
		return
	}

	// Check auth: root > group admin > account admin
	if s.requireRoot(r) {
		// fall through
	} else if group, ok := s.requireGroupAdmin(w, r); ok {
		// Group admin can delete actuators only if assigned to an agent in their group
		groupAgents, err := s.DB.GetAgentsByGroup(group.ID)
		if err != nil {
			jsonError(w, 500, "[BSA:SPINE/ADMIN] Internal error")
			return
		}
		allowed := false
		for _, ga := range groupAgents {
			assigned, _ := s.DB.IsActuatorAssignedToAgent(ga.ID, actuatorID)
			if assigned {
				allowed = true
				break
			}
		}
		if !allowed {
			jsonError(w, 403, "[BSA:SPINE/ADMIN] Actuator not assigned to an agent in your group")
			return
		}
	} else {
		isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
		if !ok {
			return
		}
		if !isRoot && !requireAccountScope(adminAgent.AccountID, actuator.AccountID) {
			jsonError(w, 403, "[BSA:SPINE/ADMIN] Forbidden: account scope violation")
			return
		}
	}

	if err := s.DB.DeleteActuator(actuatorID); err != nil {
		jsonError(w, 500, "[BSA:SPINE/ADMIN] Failed to delete actuator")
		return
	}
	accID := actuator.AccountID
	s.DB.LogAudit(&accID, nil, &actuatorID, "actuator.delete", actuator.Name)
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

// ─── Secret Management (root or admin, scoped) ─────────────────────────────────

// POST /api/secrets
func (s *Server) handleCreateSecret(w http.ResponseWriter, r *http.Request) {
	isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
	if !ok {
		return
	}

	var body struct {
		AccountID string `json:"account_id"`
		Name      string `json:"name"`
		Provider  string `json:"provider"`
		Value     string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, 400, "[BSA:SPINE/ADMIN] Invalid request body")
		return
	}
	if body.AccountID == "" || body.Name == "" || body.Provider == "" || body.Value == "" {
		jsonError(w, 400, "[BSA:SPINE/ADMIN] account_id, name, provider, and value required")
		return
	}

	// Admin scope check
	if !isRoot && !requireAccountScope(adminAgent.AccountID, body.AccountID) {
		jsonError(w, 403, "[BSA:SPINE/ADMIN] Forbidden: account scope violation")
		return
	}

	secret, err := s.DB.CreateSecret(body.AccountID, body.Name, body.Provider, body.Value, s.MasterKey)
	if err != nil {
		jsonError(w, 409, "[BSA:SPINE/ADMIN] Secret creation failed (name may exist)")
		return
	}
	accID := body.AccountID
	s.DB.LogAudit(&accID, nil, nil, "secret.create", body.Name)

	jsonResponse(w, 201, map[string]interface{}{
		"id":         secret.ID,
		"name":       secret.Name,
		"provider":   secret.Provider,
		"created_at": secret.CreatedAt,
	})
}

// PUT /api/secrets/{id}
func (s *Server) handleUpdateSecret(w http.ResponseWriter, r *http.Request) {
	isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
	if !ok {
		return
	}
	secretID := chi.URLParam(r, "id")

	secret, err := s.DB.GetSecretByID(secretID)
	if err != nil || secret == nil {
		jsonError(w, 404, "[BSA:SPINE/ADMIN] Secret not found")
		return
	}

	// Admin scope check
	if !isRoot && !requireAccountScope(adminAgent.AccountID, secret.AccountID) {
		jsonError(w, 403, "[BSA:SPINE/ADMIN] Forbidden: account scope violation")
		return
	}

	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Value == "" {
		jsonError(w, 400, "[BSA:SPINE/ADMIN] value required")
		return
	}

	if err := s.DB.UpdateSecretByID(secretID, body.Value, s.MasterKey); err != nil {
		jsonError(w, 500, "[BSA:SPINE/ADMIN] Failed to update secret")
		return
	}
	accID := secret.AccountID
	s.DB.LogAudit(&accID, nil, nil, "secret.update", secret.Name)
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

// POST /api/secrets/{id}/grant
func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	limit := 200
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	// Root sees all
	if s.requireRoot(r) {
		var accountFilter *string
		if accID := r.URL.Query().Get("account_id"); accID != "" {
			accountFilter = &accID
		}
		entries, err := s.DB.ListAuditLog(accountFilter, limit)
		if err != nil {
			jsonError(w, 500, "[BSA:SPINE/ADMIN] Failed to list audit log")
			return
		}
		jsonResponse(w, 200, formatAudit(entries))
		return
	}

	// Group admin — scoped to their account
	if group, ok := s.requireGroupAdmin(w, r); ok {
		accountFilter := group.AccountID
		entries, err := s.DB.ListAuditLog(&accountFilter, limit)
		if err != nil {
			jsonError(w, 500, "[BSA:SPINE/ADMIN] Failed to list audit log")
			return
		}
		jsonResponse(w, 200, formatAudit(entries))
		return
	}

	// Account admin or operator — scoped to their account
	token := extractToken(r)
	if token == "" {
		token = r.Header.Get("X-Agent-Token")
	}
	if token == "" {
		jsonError(w, 401, "[BSA:SPINE/ADMIN] Missing auth token or admin key")
		return
	}
	agent, err := s.DB.GetAgentByToken(token)
	if err != nil {
		jsonError(w, 500, "[BSA:SPINE/ADMIN] Internal error")
		return
	}
	if agent == nil {
		jsonError(w, 401, "[BSA:SPINE/ADMIN] Invalid token")
		return
	}
	if agent.Role != "admin" && agent.Role != "operator" {
		jsonError(w, 403, "[BSA:SPINE/ADMIN] Forbidden: admin, operator, or master key required")
		return
	}

	accountFilter := agent.AccountID
	entries, err := s.DB.ListAuditLog(&accountFilter, limit)
	if err != nil {
		jsonError(w, 500, "[BSA:SPINE/ADMIN] Failed to list audit log")
		return
	}
	jsonResponse(w, 200, formatAudit(entries))
}

// POST /api/secrets/{id}/grant — admin grants a secret to an agent.
func (s *Server) handleGrantSecretAdmin(w http.ResponseWriter, r *http.Request) {
	isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
	if !ok {
		return
	}

	secretID := chi.URLParam(r, "id")
	secret, err := s.DB.GetSecretByID(secretID)
	if err != nil || secret == nil {
		jsonError(w, 404, "[BSA:SPINE/ADMIN] Secret not found")
		return
	}

	if !isRoot && !requireAccountScope(adminAgent.AccountID, secret.AccountID) {
		jsonError(w, 403, "[BSA:SPINE/ADMIN] Forbidden: account scope violation")
		return
	}

	var body struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AgentID == "" {
		jsonError(w, 400, "[BSA:SPINE/ADMIN] agent_id required")
		return
	}

	targetAgent, err := s.DB.GetAgentByID(body.AgentID)
	if err != nil || targetAgent == nil {
		jsonError(w, 404, "[BSA:SPINE/ADMIN] Agent not found")
		return
	}
	if targetAgent.AccountID != secret.AccountID {
		jsonError(w, 403, "[BSA:SPINE/ADMIN] Forbidden: account scope violation")
		return
	}

	if err := s.DB.GrantSecretToAgent(secretID, body.AgentID); err != nil {
		jsonError(w, 500, "[BSA:SPINE/ADMIN] Failed to grant secret")
		return
	}
	s.DB.LogAudit(&secret.AccountID, &body.AgentID, nil, "secret.grant", secret.Name)
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
