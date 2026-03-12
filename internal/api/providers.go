package api

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/TheBotsters/botster-broker-go/internal/db"
)

// ─── Management API: Provider CRUD ─────────────────────────────────────────────

// POST /api/providers
func (s *Server) handleCreateProvider(w http.ResponseWriter, r *http.Request) {
	isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
	if !ok {
		return
	}

	var body struct {
		AccountID   string `json:"account_id"`
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		BaseURL     string `json:"base_url"`
		AuthType    string `json:"auth_type"`
		AuthHeader  string `json:"auth_header"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, 400, "Invalid request body")
		return
	}
	if body.AccountID == "" || body.Name == "" || body.BaseURL == "" {
		jsonError(w, 400, "account_id, name, and base_url required")
		return
	}
	if body.DisplayName == "" {
		body.DisplayName = body.Name
	}
	if body.AuthType == "" {
		body.AuthType = "bearer"
	}
	if body.AuthHeader == "" {
		body.AuthHeader = "Authorization"
	}

	if !isRoot && !requireAccountScope(adminAgent.AccountID, body.AccountID) {
		jsonError(w, 403, "Forbidden: account scope violation")
		return
	}

	provider, err := s.DB.CreateProvider(body.AccountID, body.Name, body.DisplayName, body.BaseURL, body.AuthType, body.AuthHeader)
	if err != nil {
		jsonError(w, 409, "Provider creation failed (name may exist)")
		return
	}

	s.DB.LogAudit(&body.AccountID, nil, nil, "provider.create", body.Name)
	jsonResponse(w, 201, provider)
}

// GET /api/providers
func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
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

	providers, err := s.DB.ListProviders(accountID)
	if err != nil {
		jsonError(w, 500, "Failed to list providers")
		return
	}
	if providers == nil {
		providers = []db.Provider{}
	}
	jsonResponse(w, 200, providers)
}

// PUT /api/providers/{id}
func (s *Server) handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
	if !ok {
		return
	}
	providerID := chi.URLParam(r, "id")

	provider, err := s.DB.GetProviderByID(providerID)
	if err != nil || provider == nil {
		jsonError(w, 404, "Provider not found")
		return
	}
	if !isRoot && !requireAccountScope(adminAgent.AccountID, provider.AccountID) {
		jsonError(w, 403, "Forbidden: account scope violation")
		return
	}

	var body struct {
		DisplayName string `json:"display_name"`
		BaseURL     string `json:"base_url"`
		AuthType    string `json:"auth_type"`
		AuthHeader  string `json:"auth_header"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, 400, "Invalid request body")
		return
	}
	// Use existing values as defaults
	if body.DisplayName == "" {
		body.DisplayName = provider.DisplayName
	}
	if body.BaseURL == "" {
		body.BaseURL = provider.BaseURL
	}
	if body.AuthType == "" {
		body.AuthType = provider.AuthType
	}
	if body.AuthHeader == "" {
		body.AuthHeader = provider.AuthHeader
	}
	

	if err := s.DB.UpdateProvider(providerID, body.DisplayName, body.BaseURL, body.AuthType, body.AuthHeader); err != nil {
		jsonError(w, 500, "Failed to update provider")
		return
	}
	accID := provider.AccountID
	s.DB.LogAudit(&accID, nil, nil, "provider.update", provider.Name)
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

// DELETE /api/providers/{id}
func (s *Server) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	isRoot, adminAgent, ok := s.requireRootOrAdmin(w, r)
	if !ok {
		return
	}
	providerID := chi.URLParam(r, "id")

	provider, err := s.DB.GetProviderByID(providerID)
	if err != nil || provider == nil {
		jsonError(w, 404, "Provider not found")
		return
	}
	if !isRoot && !requireAccountScope(adminAgent.AccountID, provider.AccountID) {
		jsonError(w, 403, "Forbidden: account scope violation")
		return
	}

	if err := s.DB.DeleteProvider(providerID); err != nil {
		jsonError(w, 500, "Failed to delete provider")
		return
	}
	accID := provider.AccountID
	s.DB.LogAudit(&accID, nil, nil, "provider.delete", provider.Name)
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

// ─── Agent-facing: Capabilities ────────────────────────────────────────────────

// POST /v1/capabilities
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	agent := s.authenticateAgent(w, r)
	if agent == nil {
		return
	}

	agentCaps, err := s.DB.ListAgentCapabilities(agent.AccountID, agent.ID)
	if err != nil {
		jsonError(w, 500, "Failed to list capabilities")
		return
	}

	type capResponse struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Provider    string `json:"provider"`
	}

	caps := make([]capResponse, 0, len(agentCaps))
	for _, c := range agentCaps {
		caps = append(caps, capResponse{
			Name:        c.Name,
			DisplayName: c.DisplayName,
			Provider:    c.ProviderName,
		})
	}

	jsonResponse(w, 200, map[string]interface{}{
		"agent":        agent.Name,
		"capabilities": caps,
	})
}

// ─── Agent-facing: Proxy Request ───────────────────────────────────────────────

// POST /v1/proxy/request
func (s *Server) handleProxyRequest(w http.ResponseWriter, r *http.Request) {
	agent := s.authenticateAgent(w, r)
	if agent == nil {
		return
	}

	var body struct {
		Capability string            `json:"capability"`
		Method     string            `json:"method"`
		URL        string            `json:"url"`
		Headers    map[string]string `json:"headers"`
		Body       string            `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, 400, "Invalid request body")
		return
	}
	if body.Capability == "" || body.Method == "" || body.URL == "" {
		jsonError(w, 400, "capability, method, and url required")
		return
	}

	// Look up capability (joins provider + secret)
	cap, err := s.DB.GetCapabilityByName(agent.AccountID, body.Capability)
	if err != nil || cap == nil {
		jsonError(w, 404, "Capability not found: "+body.Capability)
		return
	}

	// Check agent has been granted this capability
	granted, err := s.DB.AgentHasCapability(cap.ID, agent.ID)
	if err != nil || !granted {
		jsonError(w, 403, "Agent does not have capability: "+body.Capability)
		return
	}

	// Validate URL matches provider base_url
	if !strings.HasPrefix(body.URL, cap.BaseURL) {
		jsonError(w, 403, "URL does not match provider base URL")
		return
	}

	// Decrypt the secret value
	secretValue, err := s.DB.GetSecret(agent.AccountID, cap.SecretName, s.MasterKey)
	if err != nil {
		log.Printf("[proxy/request] failed to decrypt secret for capability %s: %v", body.Capability, err)
		jsonError(w, 500, "Failed to resolve credential for capability")
		return
	}

	// Build proxied request
	var reqBody io.Reader
	if body.Body != "" {
		reqBody = strings.NewReader(body.Body)
	}
	proxyReq, err := http.NewRequest(body.Method, body.URL, reqBody)
	if err != nil {
		jsonError(w, 400, "Invalid request: "+err.Error())
		return
	}

	// Copy agent-provided headers
	for k, v := range body.Headers {
		proxyReq.Header.Set(k, v)
	}

	// Inject credentials based on provider auth type
	switch cap.AuthType {
	case "bearer":
		proxyReq.Header.Set("Authorization", "Bearer "+secretValue)
	case "basic":
		proxyReq.Header.Set("Authorization", "Basic "+secretValue)
	case "header":
		proxyReq.Header.Set(cap.AuthHeader, secretValue)
	}

	// Execute request
	client := &http.Client{Timeout: 30 * 1000000000} // 30 seconds
	resp, err := client.Do(proxyReq)
	if err != nil {
		log.Printf("[proxy/request] error for agent %s, provider %s: %v", agent.Name, body.Capability, err)
		jsonError(w, 502, "Upstream request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	// Audit log
	s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "proxy.request",
		body.Capability+": "+body.Method+" "+body.URL+" → "+http.StatusText(resp.StatusCode))

	// Stream response back
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
