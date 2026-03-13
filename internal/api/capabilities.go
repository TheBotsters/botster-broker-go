package api

import (
	"encoding/json"

	"github.com/TheBotsters/botster-broker-go/internal/db"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// ─── Management API: Capability CRUD + Grants ──────────────────────────────────

// POST /api/capabilities
func (s *Server) handleCreateCapability(w http.ResponseWriter, r *http.Request) {
	isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
	if !ok {
		return
	}

	var body struct {
		AccountID   string `json:"account_id"`
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		ProviderID  string `json:"provider_id"`
		SecretID    string `json:"secret_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, 400, "Invalid request body")
		return
	}
	if body.AccountID == "" || body.Name == "" || body.ProviderID == "" || body.SecretID == "" {
		jsonError(w, 400, "account_id, name, provider_id, and secret_id required")
		return
	}
	if body.DisplayName == "" {
		body.DisplayName = body.Name
	}

	if !isRoot && !requireAccountScope(adminAgent.AccountID, body.AccountID) {
		jsonError(w, 403, "Forbidden: account scope violation")
		return
	}

	// Validate provider and secret belong to same account
	provider, err := s.DB.GetProviderByID(body.ProviderID)
	if err != nil || provider == nil || provider.AccountID != body.AccountID {
		jsonError(w, 404, "Provider not found in this account")
		return
	}
	secret, err := s.DB.GetSecretByID(body.SecretID)
	if err != nil || secret == nil || secret.AccountID != body.AccountID {
		jsonError(w, 404, "Secret not found in this account")
		return
	}

	cap, err := s.DB.CreateCapability(body.AccountID, body.Name, body.DisplayName, body.ProviderID, body.SecretID)
	if err != nil {
		jsonError(w, 409, "Capability creation failed (name may exist)")
		return
	}

	s.DB.LogAudit(&body.AccountID, nil, nil, "capability.create", body.Name)
	jsonResponse(w, 201, cap)
}

// GET /api/capabilities
func (s *Server) handleListCapabilities(w http.ResponseWriter, r *http.Request) {
	isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
	if !ok {
		return
	}

	accountID := r.URL.Query().Get("account_id")
	if accountID == "" && !isRoot {
		accountID = adminAgent.AccountID
	}
	if accountID == "" {
		jsonError(w, 400, "account_id required")
		return
	}
	if !isRoot && !requireAccountScope(adminAgent.AccountID, accountID) {
		jsonError(w, 403, "Forbidden: account scope violation")
		return
	}

	caps, err := s.DB.ListCapabilities(accountID)
	if err != nil {
		jsonError(w, 500, "Failed to list capabilities")
		return
	}
	if caps == nil {
		caps = []db.Capability{}
	}
	jsonResponse(w, 200, caps)
}

// PUT /api/capabilities/{id}
func (s *Server) handleUpdateCapability(w http.ResponseWriter, r *http.Request) {
	isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
	if !ok {
		return
	}
	capID := chi.URLParam(r, "id")

	cap, err := s.DB.GetCapabilityByID(capID)
	if err != nil || cap == nil {
		jsonError(w, 404, "Capability not found")
		return
	}
	if !isRoot && !requireAccountScope(adminAgent.AccountID, cap.AccountID) {
		jsonError(w, 403, "Forbidden: account scope violation")
		return
	}

	var body struct {
		DisplayName string `json:"display_name"`
		ProviderID  string `json:"provider_id"`
		SecretID    string `json:"secret_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, 400, "Invalid request body")
		return
	}
	if body.DisplayName == "" {
		body.DisplayName = cap.DisplayName
	}
	if body.ProviderID == "" {
		body.ProviderID = cap.ProviderID
	}
	if body.SecretID == "" {
		body.SecretID = cap.SecretID
	}

	if err := s.DB.UpdateCapability(capID, body.DisplayName, body.ProviderID, body.SecretID); err != nil {
		jsonError(w, 500, "Failed to update capability")
		return
	}
	accID := cap.AccountID
	s.DB.LogAudit(&accID, nil, nil, "capability.update", cap.Name)
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

// DELETE /api/capabilities/{id}
func (s *Server) handleDeleteCapability(w http.ResponseWriter, r *http.Request) {
	isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
	if !ok {
		return
	}
	capID := chi.URLParam(r, "id")

	cap, err := s.DB.GetCapabilityByID(capID)
	if err != nil || cap == nil {
		jsonError(w, 404, "Capability not found")
		return
	}
	if !isRoot && !requireAccountScope(adminAgent.AccountID, cap.AccountID) {
		jsonError(w, 403, "Forbidden: account scope violation")
		return
	}

	if err := s.DB.DeleteCapability(capID); err != nil {
		jsonError(w, 500, "Failed to delete capability")
		return
	}
	accID := cap.AccountID
	s.DB.LogAudit(&accID, nil, nil, "capability.delete", cap.Name)
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

// POST /api/capabilities/{id}/grant
func (s *Server) handleGrantCapability(w http.ResponseWriter, r *http.Request) {
	isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
	if !ok {
		return
	}
	capID := chi.URLParam(r, "id")

	cap, err := s.DB.GetCapabilityByID(capID)
	if err != nil || cap == nil {
		jsonError(w, 404, "Capability not found")
		return
	}
	if !isRoot && !requireAccountScope(adminAgent.AccountID, cap.AccountID) {
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

	// Validate agent belongs to same account
	targetAgent, err := s.DB.GetAgentByID(body.AgentID)
	if err != nil || targetAgent == nil || targetAgent.AccountID != cap.AccountID {
		jsonError(w, 404, "Agent not found in this account")
		return
	}

	if err := s.DB.GrantCapability(capID, body.AgentID); err != nil {
		jsonError(w, 500, "Failed to grant capability")
		return
	}
	accID := cap.AccountID
	s.DB.LogAudit(&accID, &body.AgentID, nil, "capability.grant", cap.Name)
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

// DELETE /api/capabilities/{id}/grant/{agentId}
func (s *Server) handleRevokeCapability(w http.ResponseWriter, r *http.Request) {
	isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
	if !ok {
		return
	}
	capID := chi.URLParam(r, "id")
	agentID := chi.URLParam(r, "agentId")

	cap, err := s.DB.GetCapabilityByID(capID)
	if err != nil || cap == nil {
		jsonError(w, 404, "Capability not found")
		return
	}
	if !isRoot && !requireAccountScope(adminAgent.AccountID, cap.AccountID) {
		jsonError(w, 403, "Forbidden: account scope violation")
		return
	}

	if err := s.DB.RevokeCapability(capID, agentID); err != nil {
		jsonError(w, 500, "Failed to revoke capability")
		return
	}
	accID := cap.AccountID
	s.DB.LogAudit(&accID, &agentID, nil, "capability.revoke", cap.Name)
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
