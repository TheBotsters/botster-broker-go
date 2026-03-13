// Package api — Inference Tap SSE stream + scoped token creation endpoint.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/TheBotsters/botster-broker-go/internal/auth"
)

// handleInferenceStream handles GET /api/inference/stream
//
// Streams inference events as Server-Sent Events (SSE).
// Auth: session cookie OR X-Admin-Key (master key) for dashboard access,
//
//	OR a regular agent token.
//
// Query: ?agent_id=<optional> — filter to a specific agent's events.
func (s *Server) handleInferenceStream(w http.ResponseWriter, r *http.Request) {
	// Auth: accept session-auth account header, root key, or valid agent token
	authed := false
	if r.Header.Get("X-Account-ID") != "" {
		authed = true
	}
	if !authed && s.requireRoot(r) {
		authed = true
	}
	if !authed {
		token := extractToken(r)
		if token == "" {
			token = r.Header.Get("X-Agent-Token")
		}
		if token == "" {
			token = r.URL.Query().Get("agent_token")
		}
		if token != "" {
			agent, err := s.DB.GetAgentByToken(token)
			if err == nil && agent != nil {
				authed = true
			}
		}
	}
	if !authed {
		jsonError(w, 401, "[BSA:SPINE/TAPSTREAM] Unauthorized")
		return
	}

	if s.Tap == nil {
		jsonError(w, 503, "[BSA:SPINE/TAPSTREAM] Inference tap not initialized")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, 500, "[BSA:SPINE/TAPSTREAM] Streaming not supported")
		return
	}

	filterAgentID := r.URL.Query().Get("agent_id")

	ch, unsub := s.Tap.Subscribe(filterAgentID)
	defer unsub()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx: disable buffering
	w.WriteHeader(http.StatusOK)

	// Send initial keepalive to flush headers
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case event, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// handleCreateScopedToken handles POST /v1/tokens/scoped
//
// Creates a stateless HMAC-signed scoped token for a sub-skill/tool.
// Auth: agent token (the parent agent issuing the scoped token).
func (s *Server) handleCreateScopedToken(w http.ResponseWriter, r *http.Request) {
	agent := s.authenticateAgent(w, r)
	if agent == nil {
		return
	}

	// Scoped tokens cannot themselves create scoped tokens
	if getScopedCaps(r) != nil {
		jsonError(w, 403, "[BSA:SPINE/TAPSTREAM] Scoped tokens cannot create further scoped tokens")
		return
	}

	var body struct {
		SkillName    string   `json:"skillName"`
		Capabilities []string `json:"capabilities"`
		TTLSeconds   int      `json:"ttlSeconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, 400, "[BSA:SPINE/TAPSTREAM] Invalid JSON body")
		return
	}
	if len(body.Capabilities) == 0 {
		jsonError(w, 400, "[BSA:SPINE/TAPSTREAM] capabilities required (non-empty array)")
		return
	}

	// Sanitize capabilities — reject any that look dangerous
	for _, cap := range body.Capabilities {
		if strings.ContainsAny(cap, "\n\r\t") {
			jsonError(w, 400, "[BSA:SPINE/TAPSTREAM] Invalid capability string")
			return
		}
	}

	token, expiresAt, err := auth.CreateScopedToken(
		s.MasterKey,
		agent.ID,
		agent.AccountID,
		body.SkillName,
		body.Capabilities,
		body.TTLSeconds,
	)
	if err != nil {
		jsonError(w, 500, "[BSA:SPINE/TAPSTREAM] Failed to create scoped token: "+err.Error())
		return
	}

	s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "scoped_token_create",
		fmt.Sprintf("skill=%s caps=%v ttl=%d", body.SkillName, body.Capabilities, body.TTLSeconds))

	jsonResponse(w, 201, map[string]interface{}{
		"ok":        true,
		"token":     token,
		"expiresAt": expiresAt.UTC().Format(time.RFC3339),
	})
}
