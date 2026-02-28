// Package api — account management, agent/actuator CRUD, secret admin, audit log.
package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// ─── Account CRUD (root only) ──────────────────────────────────────────────────

// GET /api/accounts
func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(r) {
		jsonError(w, 401, "Unauthorized: invalid or missing X-Admin-Key")
		return
	}
	accounts, err := s.DB.ListAccounts()
	if err != nil {
		jsonError(w, 500, "Failed to list accounts")
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
		jsonError(w, 401, "Unauthorized: invalid or missing X-Admin-Key")
		return
	}
	id := chi.URLParam(r, "id")
	acc, err := s.DB.GetAccountByID(id)
	if err != nil || acc == nil {
		jsonError(w, 404, "Account not found")
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
		jsonError(w, 401, "Unauthorized: invalid or missing X-Admin-Key")
		return
	}
	id := chi.URLParam(r, "id")
	acc, err := s.DB.GetAccountByID(id)
	if err != nil || acc == nil {
		jsonError(w, 404, "Account not found")
		return
	}

	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, 400, "Invalid request body")
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
		jsonError(w, 500, "Failed to update account")
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
		jsonError(w, 401, "Unauthorized: invalid or missing X-Admin-Key")
		return
	}
	id := chi.URLParam(r, "id")
	acc, err := s.DB.GetAccountByID(id)
	if err != nil || acc == nil {
		jsonError(w, 404, "Account not found")
		return
	}
	if err := s.DB.DeleteAccount(id); err != nil {
		jsonError(w, 500, "Failed to delete account")
		return
	}
	s.DB.LogAudit(&id, nil, nil, "account.delete", acc.Email)
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

// ─── Agent Management under Account ───────────────────────────────────────────

// POST /api/accounts/{id}/agents — root only
func (s *Server) handleCreateAgentForAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(r) {
		jsonError(w, 401, "Unauthorized: invalid or missing X-Admin-Key")
		return
	}
	accountID := chi.URLParam(r, "id")
	acc, err := s.DB.GetAccountByID(accountID)
	if err != nil || acc == nil {
		jsonError(w, 404, "Account not found")
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		jsonError(w, 400, "name required")
		return
	}

	newAgent, token, err := s.DB.CreateAgent(accountID, body.Name)
	if err != nil {
		jsonError(w, 409, "Agent creation failed (name may exist)")
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

// GET /api/accounts/{id}/agents — root or admin (scoped to own account)
func (s *Server) handleListAgentsForAccount(w http.ResponseWriter, r *http.Request) {
	isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
	if !ok {
		return
	}
	accountID := chi.URLParam(r, "id")

	// Admin can only see their own account
	if !isRoot && !requireAccountScope(adminAgent.AccountID, accountID) {
		jsonError(w, 403, "Forbidden: account scope violation")
		return
	}

	acc, err := s.DB.GetAccountByID(accountID)
	if err != nil || acc == nil {
		jsonError(w, 404, "Account not found")
		return
	}

	agents, err := s.DB.ListAgentsByAccount(accountID)
	if err != nil {
		jsonError(w, 500, "Failed to list agents")
		return
	}

	result := make([]map[string]interface{}, len(agents))
	for i, a := range agents {
		result[i] = map[string]interface{}{
			"id":         a.ID,
			"name":       a.Name,
			"role":       a.Role,
			"safe":       a.Safe,
			"created_at": a.CreatedAt,
		}
	}
	jsonResponse(w, 200, result)
}

// DELETE /api/accounts/{id}/agents/{agentId} — root only
func (s *Server) handleDeleteAgentFromAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(r) {
		jsonError(w, 401, "Unauthorized: invalid or missing X-Admin-Key")
		return
	}
	accountID := chi.URLParam(r, "id")
	agentID := chi.URLParam(r, "agentId")

	agent, err := s.DB.GetAgentByID(agentID)
	if err != nil || agent == nil || agent.AccountID != accountID {
		jsonError(w, 404, "Agent not found")
		return
	}
	if err := s.DB.DeleteAgent(agentID); err != nil {
		jsonError(w, 500, "Failed to delete agent")
		return
	}
	s.DB.LogAudit(&accountID, &agentID, nil, "agent.delete", agent.Name)
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

// PATCH /api/accounts/{id}/agents/{agentId} — root only (for role assignment, etc.)
func (s *Server) handleUpdateAgentInAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(r) {
		jsonError(w, 401, "Unauthorized: invalid or missing X-Admin-Key")
		return
	}
	accountID := chi.URLParam(r, "id")
	agentID := chi.URLParam(r, "agentId")

	agent, err := s.DB.GetAgentByID(agentID)
	if err != nil || agent == nil || agent.AccountID != accountID {
		jsonError(w, 404, "Agent not found")
		return
	}

	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, 400, "Invalid request body")
		return
	}

	// Allow name, role, safe
	updates := map[string]interface{}{}
	if v, ok := body["role"]; ok {
		role, _ := v.(string)
		if role != "agent" && role != "admin" {
			jsonError(w, 400, "role must be 'agent' or 'admin'")
			return
		}
		updates["role"] = role
	}
	if v, ok := body["name"]; ok {
		updates["name"] = v
	}
	if v, ok := body["safe"]; ok {
		if safeBool, ok := v.(bool); ok {
			safeInt := 0
			if safeBool {
				safeInt = 1
			}
			updates["safe"] = safeInt
		}
	}

	if err := s.DB.UpdateAgent(agentID, updates); err != nil {
		jsonError(w, 500, "Failed to update agent")
		return
	}
	s.DB.LogAudit(&accountID, &agentID, nil, "agent.update", agent.Name)

	updated, _ := s.DB.GetAgentByID(agentID)
	jsonResponse(w, 200, map[string]interface{}{
		"id":   updated.ID,
		"name": updated.Name,
		"role": updated.Role,
		"safe": updated.Safe,
	})
}

// POST /api/accounts/{id}/agents/{agentId}/rotate-token — root or admin (admin: own token only)
func (s *Server) handleRotateAgentToken(w http.ResponseWriter, r *http.Request) {
	isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
	if !ok {
		return
	}
	accountID := chi.URLParam(r, "id")
	agentID := chi.URLParam(r, "agentId")

	target, err := s.DB.GetAgentByID(agentID)
	if err != nil || target == nil || target.AccountID != accountID {
		jsonError(w, 404, "Agent not found")
		return
	}

	// Admin can only rotate their own token
	if !isRoot {
		if !requireAccountScope(adminAgent.AccountID, accountID) || adminAgent.ID != agentID {
			jsonError(w, 403, "Forbidden: admin can only rotate own token")
			return
		}
	}

	newToken, err := s.DB.RotateAgentToken(agentID)
	if err != nil {
		jsonError(w, 500, "Failed to rotate token")
		return
	}
	s.DB.LogAudit(&accountID, &agentID, nil, "agent.rotate-token", target.Name)

	jsonResponse(w, 200, map[string]interface{}{
		"ok":    true,
		"token": newToken,
	})
}

// ─── Actuator Management ───────────────────────────────────────────────────────

// POST /api/agents/{agentId}/actuators — root or admin (scoped)
func (s *Server) handleCreateActuatorForAgent(w http.ResponseWriter, r *http.Request) {
	isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
	if !ok {
		return
	}
	agentID := chi.URLParam(r, "agentId")

	agent, err := s.DB.GetAgentByID(agentID)
	if err != nil || agent == nil {
		jsonError(w, 404, "Agent not found")
		return
	}

	// Admin scope check
	if !isRoot && !requireAccountScope(adminAgent.AccountID, agent.AccountID) {
		jsonError(w, 403, "Forbidden: account scope violation")
		return
	}

	var body struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		jsonError(w, 400, "name required")
		return
	}
	if body.Type == "" {
		body.Type = "vps"
	}

	actuator, token, err := s.DB.CreateActuator(agent.AccountID, body.Name, body.Type)
	if err != nil {
		jsonError(w, 409, "Actuator creation failed")
		return
	}

	// Auto-assign to the agent
	if err := s.DB.AssignActuatorToAgent(agentID, actuator.ID); err != nil {
		jsonError(w, 500, "Actuator created but assignment failed")
		return
	}
	s.DB.LogAudit(&agent.AccountID, &agentID, &actuator.ID, "actuator.create", actuator.Name)

	jsonResponse(w, 201, map[string]interface{}{
		"id":         actuator.ID,
		"name":       actuator.Name,
		"type":       actuator.Type,
		"token":      token,
		"created_at": actuator.CreatedAt,
	})
}

// DELETE /api/actuators/{id} — root or admin (scoped)
func (s *Server) handleDeleteActuator(w http.ResponseWriter, r *http.Request) {
	isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
	if !ok {
		return
	}
	actuatorID := chi.URLParam(r, "id")

	actuator, err := s.DB.GetActuatorByID(actuatorID)
	if err != nil || actuator == nil {
		jsonError(w, 404, "Actuator not found")
		return
	}

	// Admin scope check
	if !isRoot && !requireAccountScope(adminAgent.AccountID, actuator.AccountID) {
		jsonError(w, 403, "Forbidden: account scope violation")
		return
	}

	if err := s.DB.DeleteActuator(actuatorID); err != nil {
		jsonError(w, 500, "Failed to delete actuator")
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
		jsonError(w, 400, "Invalid request body")
		return
	}
	if body.AccountID == "" || body.Name == "" || body.Provider == "" || body.Value == "" {
		jsonError(w, 400, "account_id, name, provider, and value required")
		return
	}

	// Admin scope check
	if !isRoot && !requireAccountScope(adminAgent.AccountID, body.AccountID) {
		jsonError(w, 403, "Forbidden: account scope violation")
		return
	}

	secret, err := s.DB.CreateSecret(body.AccountID, body.Name, body.Provider, body.Value, s.MasterKey)
	if err != nil {
		jsonError(w, 409, "Secret creation failed (name may exist)")
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
		jsonError(w, 404, "Secret not found")
		return
	}

	// Admin scope check
	if !isRoot && !requireAccountScope(adminAgent.AccountID, secret.AccountID) {
		jsonError(w, 403, "Forbidden: account scope violation")
		return
	}

	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Value == "" {
		jsonError(w, 400, "value required")
		return
	}

	if err := s.DB.UpdateSecretByID(secretID, body.Value, s.MasterKey); err != nil {
		jsonError(w, 500, "Failed to update secret")
		return
	}
	accID := secret.AccountID
	s.DB.LogAudit(&accID, nil, nil, "secret.update", secret.Name)
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

// POST /api/secrets/{id}/grant
func (s *Server) handleGrantSecretAccess(w http.ResponseWriter, r *http.Request) {
	isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
	if !ok {
		return
	}
	secretID := chi.URLParam(r, "id")

	secret, err := s.DB.GetSecretByID(secretID)
	if err != nil || secret == nil {
		jsonError(w, 404, "Secret not found")
		return
	}

	// Admin scope check
	if !isRoot && !requireAccountScope(adminAgent.AccountID, secret.AccountID) {
		jsonError(w, 403, "Forbidden: account scope violation")
		return
	}

	var body struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AgentID == "" {
		jsonError(w, 400, "agent_id required")
		return
	}

	// Validate target agent belongs to the same account
	targetAgent, err := s.DB.GetAgentByID(body.AgentID)
	if err != nil || targetAgent == nil || targetAgent.AccountID != secret.AccountID {
		jsonError(w, 404, "Agent not found in this account")
		return
	}

	if err := s.DB.GrantSecretAccess(secretID, body.AgentID); err != nil {
		jsonError(w, 500, "Failed to grant access")
		return
	}
	accID := secret.AccountID
	s.DB.LogAudit(&accID, &body.AgentID, nil, "secret.grant", secret.Name)
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

// DELETE /api/secrets/{id}/grant/{agentId}
func (s *Server) handleRevokeSecretAccess(w http.ResponseWriter, r *http.Request) {
	isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
	if !ok {
		return
	}
	secretID := chi.URLParam(r, "id")
	agentID := chi.URLParam(r, "agentId")

	secret, err := s.DB.GetSecretByID(secretID)
	if err != nil || secret == nil {
		jsonError(w, 404, "Secret not found")
		return
	}

	// Admin scope check
	if !isRoot && !requireAccountScope(adminAgent.AccountID, secret.AccountID) {
		jsonError(w, 403, "Forbidden: account scope violation")
		return
	}

	if err := s.DB.RevokeSecretAccess(secretID, agentID); err != nil {
		jsonError(w, 500, "Failed to revoke access")
		return
	}
	accID := secret.AccountID
	s.DB.LogAudit(&accID, &agentID, nil, "secret.revoke", secret.Name)
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

// ─── Audit Log ─────────────────────────────────────────────────────────────────

// GET /api/audit — root sees all, admin sees own account
func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
	if !ok {
		return
	}

	limitStr := r.URL.Query().Get("limit")
	limit := 200
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	var accountFilter *string
	if !isRoot {
		accountFilter = &adminAgent.AccountID
	} else if accID := r.URL.Query().Get("account_id"); accID != "" {
		accountFilter = &accID
	}

	entries, err := s.DB.ListAuditLog(accountFilter, limit)
	if err != nil {
		jsonError(w, 500, "Failed to list audit log")
		return
	}

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
	jsonResponse(w, 200, result)
}
