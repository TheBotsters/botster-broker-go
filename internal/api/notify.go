// Package api — notify endpoint.
package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/TheBotsters/botster-broker-go/internal/hub"
	"github.com/go-chi/chi/v5"
)

// POST /v1/notify/{agentName} — root only
// Sends a wake message to the named agent's brain connection.
// If not connected, buffers it for delivery on next connect.
func (s *Server) handleNotify(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(r) {
		jsonError(w, 401, "[BSA:SPINE/NOTIFY] Unauthorized: invalid or missing X-Admin-Key")
		return
	}

	agentName := chi.URLParam(r, "agentName")
	agent, err := s.DB.GetAgentByName(agentName)
	if err != nil {
		jsonError(w, 500, "[BSA:SPINE/NOTIFY] Internal error")
		return
	}
	if agent == nil {
		jsonError(w, 404, "[BSA:SPINE/NOTIFY] agent_not_found")
		return
	}

	var body struct {
		Text   string `json:"text"`
		Source string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Text == "" {
		jsonError(w, 400, "[BSA:SPINE/NOTIFY] text required")
		return
	}
	if body.Source == "" {
		body.Source = "unknown"
	}

	ts := time.Now().UTC().Format(time.RFC3339)

	wakeMsg := hub.WSMessage{
		Type: hub.TypeWake,
		Text: body.Text,
	}

	// Try to deliver to connected brain directly.
	// SendToAgent works in both link mode and direct WS mode.
	if s.Hub.SendToAgent(agent.ID, wakeMsg) {
		accID := agent.AccountID
		s.DB.LogAudit(&accID, &agent.ID, nil, "notify.sent", body.Source)
		jsonResponse(w, 200, map[string]interface{}{"ok": true, "delivered": true})
		return
	}

	// Buffer for later delivery
	s.Hub.BufferWake(agent.ID, body.Text, body.Source, ts)
	accID := agent.AccountID
	s.DB.LogAudit(&accID, &agent.ID, nil, "notify.buffered", body.Source)
	jsonResponse(w, 200, map[string]interface{}{"ok": true, "delivered": false, "buffered": true})
}
