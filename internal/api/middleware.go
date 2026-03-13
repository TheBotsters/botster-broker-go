// Package api — RBAC middleware helpers.
package api

import (
	"net/http"
	"strings"

	"github.com/TheBotsters/botster-broker-go/internal/db"
)

// requireRoot checks that the request carries the master key in X-Admin-Key.
func (s *Server) requireRoot(r *http.Request) bool {
	key := r.Header.Get("X-Admin-Key")
	return key != "" && key == s.MasterKey
}

// requireAdmin extracts the agent token, verifies the agent exists and has role=admin.
// Writes a 401/403 response and returns nil on failure.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) *db.Agent {
	token := extractToken(r)
	if token == "" {
		jsonError(w, 401, "[BSA:SPINE/AUTH] Missing auth token")
		return nil
	}
	agent, err := s.DB.GetAgentByToken(token)
	if err != nil {
		jsonError(w, 500, "[BSA:SPINE/AUTH] Internal error")
		return nil
	}
	if agent == nil {
		jsonError(w, 401, "[BSA:SPINE/AUTH] Invalid token")
		return nil
	}
	if agent.Role != "admin" {
		jsonError(w, 403, "[BSA:SPINE/AUTH] Forbidden: admin role required")
		return nil
	}
	return agent
}

// requireRootOrAdmin checks for root key first, then admin agent token.
// Returns (isRoot=true, nil, true) for root.
// Returns (isRoot=false, agent, true) for admin.
// Returns (false, nil, false) and writes error response on failure.
func (s *Server) requireRootOrAdmin(w http.ResponseWriter, r *http.Request) (isRoot bool, agent *db.Agent, ok bool) {
	// Check root first
	if s.requireRoot(r) {
		return true, nil, true
	}

	// Try admin agent token (Authorization: Bearer, X-API-Key, X-Agent-Token, ?agent_token)
	token := extractToken(r)
	if token == "" {
		token = r.Header.Get("X-Agent-Token")
	}
	if token == "" {
		token = r.URL.Query().Get("agent_token")
	}
	if token == "" {
		jsonError(w, 401, "[BSA:SPINE/AUTH] Missing auth token or admin key")
		return false, nil, false
	}

	a, err := s.DB.GetAgentByToken(token)
	if err != nil {
		jsonError(w, 500, "[BSA:SPINE/AUTH] Internal error")
		return false, nil, false
	}
	if a == nil {
		jsonError(w, 401, "[BSA:SPINE/AUTH] Invalid token")
		return false, nil, false
	}
	if a.Role != "admin" {
		jsonError(w, 403, "[BSA:SPINE/AUTH] Forbidden: admin role or master key required")
		return false, nil, false
	}
	return false, a, true
}

// requireAccountScope validates that the target account matches the admin's account.
// Returns false (caller should 403) if scope check fails.
func requireAccountScope(agentAccountID, targetAccountID string) bool {
	return strings.EqualFold(agentAccountID, targetAccountID)
}

// requireGroupAdmin checks the X-Group-Admin-Key header, hashes it, and looks up the group.
// Returns (group, true) if authorized, writes a response and returns (nil, false) on failure.
// Note: this does NOT write an error response on missing key — it just returns (nil, false),
// so callers can fall through to other auth methods. Only writes error on DB failure.
func (s *Server) requireGroupAdmin(w http.ResponseWriter, r *http.Request) (*db.Group, bool) {
	key := r.Header.Get("X-Group-Admin-Key")
	if key == "" {
		return nil, false
	}

	group, err := s.DB.GetGroupByAdminKey(key)
	if err != nil {
		jsonError(w, 500, "[BSA:SPINE/AUTH] Internal error")
		return nil, false
	}
	if group == nil {
		return nil, false
	}
	return group, true
}

// requireRootOrGroupAdmin checks root first, then group admin key.
// Returns (isRoot=true, nil, true) for root.
// Returns (isRoot=false, group, true) for group admin.
// Returns (false, nil, false) and writes error on complete failure.
func (s *Server) requireRootOrGroupAdmin(w http.ResponseWriter, r *http.Request) (isRoot bool, group *db.Group, ok bool) {
	if s.requireRoot(r) {
		return true, nil, true
	}

	g, ok := s.requireGroupAdmin(w, r)
	if ok {
		return false, g, true
	}

	// Neither root nor group admin — write error
	jsonError(w, 401, "[BSA:SPINE/AUTH] Missing or invalid X-Admin-Key or X-Group-Admin-Key")
	return false, nil, false
}

// isAgentInGroup checks whether an agent belongs to the specified group.
func (s *Server) isAgentInGroup(agentID, groupID string) bool {
	agent, err := s.DB.GetAgentByID(agentID)
	if err != nil || agent == nil {
		return false
	}
	return agent.GroupID.Valid && agent.GroupID.String == groupID
}

// requireOperator checks that the agent token belongs to an agent with role=operator.
// Writes a 401/403 and returns nil on failure.
func (s *Server) requireOperator(w http.ResponseWriter, r *http.Request) *db.Agent {
	token := extractToken(r)
	if token == "" {
		token = r.Header.Get("X-Agent-Token")
	}
	if token == "" {
		jsonError(w, 401, "[BSA:SPINE/AUTH] Missing auth token")
		return nil
	}
	agent, err := s.DB.GetAgentByToken(token)
	if err != nil {
		jsonError(w, 500, "[BSA:SPINE/AUTH] Internal error")
		return nil
	}
	if agent == nil {
		jsonError(w, 401, "[BSA:SPINE/AUTH] Invalid token")
		return nil
	}
	if agent.Role != "operator" {
		jsonError(w, 403, "[BSA:SPINE/AUTH] Forbidden: operator role required")
		return nil
	}
	return agent
}
