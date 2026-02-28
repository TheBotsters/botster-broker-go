// Package api — group management handlers.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// ─── Group Management (root only) ─────────────────────────────────────────────

// POST /api/groups — create a group, returns group + plaintext admin key
func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(r) {
		jsonError(w, 401, "Unauthorized: invalid or missing X-Admin-Key")
		return
	}

	var body struct {
		AccountID string `json:"account_id"`
		Name      string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, 400, "Invalid request body")
		return
	}
	if body.AccountID == "" || body.Name == "" {
		jsonError(w, 400, "account_id and name required")
		return
	}

	// Verify account exists
	acc, err := s.DB.GetAccountByID(body.AccountID)
	if err != nil || acc == nil {
		jsonError(w, 404, "Account not found")
		return
	}

	group, plainKey, err := s.DB.CreateGroup(body.AccountID, body.Name)
	if err != nil {
		jsonError(w, 409, "Group creation failed (name may exist in account)")
		return
	}

	accID := body.AccountID
	s.DB.LogAudit(&accID, nil, nil, "group.create", group.Name)

	jsonResponse(w, 201, map[string]interface{}{
		"id":         group.ID,
		"account_id": group.AccountID,
		"name":       group.Name,
		"admin_key":  plainKey,
		"created_at": group.CreatedAt,
	})
}

// GET /api/groups — list all groups (root only)
func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(r) {
		jsonError(w, 401, "Unauthorized: invalid or missing X-Admin-Key")
		return
	}

	// Optional filter by account_id
	accountID := r.URL.Query().Get("account_id")

	groups, err := s.DB.ListGroups(accountID)
	if err != nil {
		jsonError(w, 500, "Failed to list groups")
		return
	}

	result := make([]map[string]interface{}, len(groups))
	for i, g := range groups {
		result[i] = map[string]interface{}{
			"id":         g.ID,
			"account_id": g.AccountID,
			"name":       g.Name,
			"created_at": g.CreatedAt,
		}
	}
	jsonResponse(w, 200, result)
}

// PATCH /api/groups/{id} — update group name (root only)
func (s *Server) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(r) {
		jsonError(w, 401, "Unauthorized: invalid or missing X-Admin-Key")
		return
	}

	groupID := chi.URLParam(r, "id")
	group, err := s.DB.GetGroupByID(groupID)
	if err != nil || group == nil {
		jsonError(w, 404, "Group not found")
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		jsonError(w, 400, "name required")
		return
	}

	if err := s.DB.UpdateGroup(groupID, body.Name); err != nil {
		jsonError(w, 500, "Failed to update group")
		return
	}

	accID := group.AccountID
	s.DB.LogAudit(&accID, nil, nil, "group.update", group.Name+" -> "+body.Name)

	updated, _ := s.DB.GetGroupByID(groupID)
	jsonResponse(w, 200, map[string]interface{}{
		"id":         updated.ID,
		"account_id": updated.AccountID,
		"name":       updated.Name,
		"created_at": updated.CreatedAt,
	})
}

// DELETE /api/groups/{id} — delete group, unsets group_id on agents (root only)
func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(r) {
		jsonError(w, 401, "Unauthorized: invalid or missing X-Admin-Key")
		return
	}

	groupID := chi.URLParam(r, "id")
	group, err := s.DB.GetGroupByID(groupID)
	if err != nil || group == nil {
		jsonError(w, 404, "Group not found")
		return
	}

	if err := s.DB.DeleteGroup(groupID); err != nil {
		jsonError(w, 500, "Failed to delete group")
		return
	}

	accID := group.AccountID
	s.DB.LogAudit(&accID, nil, nil, "group.delete", group.Name)
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

// POST /api/groups/{id}/rotate-key — rotate group admin key (root only)
func (s *Server) handleRotateGroupKey(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(r) {
		jsonError(w, 401, "Unauthorized: invalid or missing X-Admin-Key")
		return
	}

	groupID := chi.URLParam(r, "id")
	group, err := s.DB.GetGroupByID(groupID)
	if err != nil || group == nil {
		jsonError(w, 404, "Group not found")
		return
	}

	newKey, err := s.DB.RotateGroupAdminKey(groupID)
	if err != nil {
		jsonError(w, 500, "Failed to rotate group admin key")
		return
	}

	accID := group.AccountID
	s.DB.LogAudit(&accID, nil, nil, "group.rotate-key", group.Name)

	jsonResponse(w, 200, map[string]interface{}{
		"ok":        true,
		"admin_key": newKey,
	})
}

// POST /api/agents/{id}/assign-group — assign agent to group (root only)
func (s *Server) handleAssignAgentToGroup(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(r) {
		jsonError(w, 401, "Unauthorized: invalid or missing X-Admin-Key")
		return
	}

	agentID := chi.URLParam(r, "id")
	agent, err := s.DB.GetAgentByID(agentID)
	if err != nil || agent == nil {
		jsonError(w, 404, "Agent not found")
		return
	}

	var body struct {
		GroupID string `json:"group_id"` // empty string to unassign
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, 400, "Invalid request body")
		return
	}

	// If a group_id is provided, verify it exists and belongs to same account
	if body.GroupID != "" {
		group, err := s.DB.GetGroupByID(body.GroupID)
		if err != nil || group == nil {
			jsonError(w, 404, "Group not found")
			return
		}
		if group.AccountID != agent.AccountID {
			jsonError(w, 400, "Group and agent must belong to the same account")
			return
		}
	}

	if err := s.DB.AssignAgentToGroup(agentID, body.GroupID); err != nil {
		jsonError(w, 500, "Failed to assign agent to group")
		return
	}

	accID := agent.AccountID
	detail := agent.Name + " -> group:" + body.GroupID
	s.DB.LogAudit(&accID, &agentID, nil, "agent.assign-group", detail)

	updated, _ := s.DB.GetAgentByID(agentID)
	groupIDVal := interface{}(nil)
	if updated.GroupID.Valid {
		groupIDVal = updated.GroupID.String
	}
	jsonResponse(w, 200, map[string]interface{}{
		"ok":       true,
		"agent_id": agentID,
		"group_id": groupIDVal,
	})
}

// GET /api/groups/{id}/agents — list agents in a group (root or group admin)
func (s *Server) handleListGroupAgents(w http.ResponseWriter, r *http.Request) {
	groupID := chi.URLParam(r, "id")

	isRoot, group, ok := s.requireRootOrGroupAdmin(w, r)
	if !ok {
		return
	}

	// Group admin can only see their own group
	if !isRoot && group.ID != groupID {
		jsonError(w, 403, "Forbidden: not your group")
		return
	}

	// Verify group exists
	targetGroup, err := s.DB.GetGroupByID(groupID)
	if err != nil || targetGroup == nil {
		jsonError(w, 404, "Group not found")
		return
	}

	agents, err := s.DB.GetAgentsByGroup(groupID)
	if err != nil {
		jsonError(w, 500, "Failed to list group agents")
		return
	}

	result := make([]map[string]interface{}, len(agents))
	for i, a := range agents {
		result[i] = map[string]interface{}{
			"id":         a.ID,
			"name":       a.Name,
			"role":       a.Role,
			"safe":       a.Safe,
			"group_id":   groupID,
			"created_at": a.CreatedAt,
		}
	}
	jsonResponse(w, 200, result)
}
