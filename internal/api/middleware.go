// Package api — RBAC middleware helpers.
package api

import (
	"net/http"
	"strings"

	"github.com/siofra-seksbot/botster-broker-go/internal/db"
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
		jsonError(w, 401, "Missing auth token")
		return nil
	}
	agent, err := s.DB.GetAgentByToken(token)
	if err != nil {
		jsonError(w, 500, "Internal error")
		return nil
	}
	if agent == nil {
		jsonError(w, 401, "Invalid token")
		return nil
	}
	if agent.Role != "admin" {
		jsonError(w, 403, "Forbidden: admin role required")
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
		jsonError(w, 401, "Missing auth token or admin key")
		return false, nil, false
	}

	a, err := s.DB.GetAgentByToken(token)
	if err != nil {
		jsonError(w, 500, "Internal error")
		return false, nil, false
	}
	if a == nil {
		jsonError(w, 401, "Invalid token")
		return false, nil, false
	}
	if a.Role != "admin" {
		jsonError(w, 403, "Forbidden: admin role or master key required")
		return false, nil, false
	}
	return false, a, true
}

// requireAccountScope validates that the target account matches the admin's account.
// Returns false (caller should 403) if scope check fails.
func requireAccountScope(agentAccountID, targetAccountID string) bool {
	return strings.EqualFold(agentAccountID, targetAccountID)
}
