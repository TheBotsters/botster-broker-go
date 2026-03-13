package api

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
)

// GatewayConfig holds connection info for an agent's gateway.
type GatewayConfig struct {
	URL   string `json:"url"`   // e.g. "ws://127.0.0.1:18786"
	Token string `json:"token"` // gateway auth token
}

// handleChatAgentList returns agents the authenticated user can chat with.
func (s *Server) handleChatAgentList(w http.ResponseWriter, r *http.Request) {
	accountID := r.Header.Get("X-Account-ID")
	agents, err := s.DB.ListAgentsByAccount(accountID)
	if err != nil {
		jsonError(w, 500, "[BSA:SPINE/PROXY] Failed to list agents")
		return
	}

	type agentInfo struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		HasGateway bool   `json:"hasGateway"`
	}
	result := make([]agentInfo, 0, len(agents))
	for _, a := range agents {
		_, hasGW := s.Gateways[a.Name]
		result = append(result, agentInfo{
			ID:         a.ID,
			Name:       a.Name,
			HasGateway: hasGW,
		})
	}
	jsonResponse(w, 200, result)
}

// handleChatProxy proxies a WebSocket connection to an agent's gateway.
// The broker handles the gateway's challenge-response auth on behalf of the user.
func (s *Server) handleChatProxy(w http.ResponseWriter, r *http.Request) {
	agentName := chi.URLParam(r, "agent")
	accountID := r.Header.Get("X-Account-ID")

	// Verify agent exists and belongs to this account
	agents, err := s.DB.ListAgentsByAccount(accountID)
	if err != nil {
		log.Printf("[chat-proxy] db error: %v", err)
		http.Error(w, "Internal error", 500)
		return
	}
	found := false
	for _, a := range agents {
		if a.Name == agentName {
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "Agent not found or not owned by you", 403)
		return
	}

	// Look up gateway config
	gw, ok := s.Gateways[agentName]
	if !ok {
		http.Error(w, "No gateway configured for agent", 404)
		return
	}

	// Accept browser WebSocket
	browserConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // Origin check handled by session auth
	})
	if err != nil {
		log.Printf("[chat-proxy] accept error: %v", err)
		return
	}
	defer browserConn.CloseNow()

	log.Printf("[chat-proxy] %s → %s (account %s)", agentName, gw.URL, accountID)

	// Connect to agent gateway
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	gatewayConn, _, err := websocket.Dial(ctx, gw.URL, &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Origin": []string{gw.URL},
		},
	})
	cancel()
	if err != nil {
		log.Printf("[chat-proxy] gateway connect error (%s): %v", gw.URL, err)
		browserConn.Close(websocket.StatusInternalError, "Agent gateway unreachable")
		return
	}
	defer gatewayConn.CloseNow()

	// Step 1: Read the connect.challenge from gateway
	ctx, cancel = context.WithTimeout(r.Context(), 10*time.Second)
	_, challengeData, err := gatewayConn.Read(ctx)
	cancel()
	if err != nil {
		log.Printf("[chat-proxy] gateway challenge read error: %v", err)
		browserConn.Close(websocket.StatusInternalError, "Gateway handshake failed")
		return
	}

	// Parse challenge to extract nonce
	var challenge struct {
		Type    string `json:"type"`
		Event   string `json:"event"`
		Payload struct {
			Nonce string `json:"nonce"`
			Ts    int64  `json:"ts"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(challengeData, &challenge); err != nil || challenge.Event != "connect.challenge" {
		log.Printf("[chat-proxy] unexpected first message (expected challenge): %s", string(challengeData))
		browserConn.Close(websocket.StatusInternalError, "Gateway protocol error")
		return
	}

	// Step 2: Send connect with auth token
	connectMsg := map[string]interface{}{
		"type":   "req",
		"id":     "broker-connect-1",
		"method": "connect",
		"params": map[string]interface{}{
			"minProtocol": 3,
			"maxProtocol": 3,
			"client": map[string]interface{}{
				"id":      "openclaw-control-ui",
				"version": "1.0.0",
				"mode":     "webchat",
				"platform": "web",
			},
			"role":   "operator",
			"scopes": []string{"operator.admin", "operator.read", "operator.write"},
			"auth": map[string]string{
				"token": gw.Token,
			},
		},
	}
	connectJSON, _ := json.Marshal(connectMsg)
	err = gatewayConn.Write(r.Context(), websocket.MessageText, connectJSON)
	if err != nil {
		log.Printf("[chat-proxy] gateway connect write error: %v", err)
		browserConn.Close(websocket.StatusInternalError, "Gateway handshake failed")
		return
	}

	// Step 3: Read connect response
	ctx, cancel = context.WithTimeout(r.Context(), 10*time.Second)
	_, connectResp, err := gatewayConn.Read(ctx)
	cancel()
	if err != nil {
		log.Printf("[chat-proxy] gateway connect response error: %v", err)
		browserConn.Close(websocket.StatusInternalError, "Gateway handshake failed")
		return
	}

	var resp struct {
		Type string `json:"type"`
		OK   bool   `json:"ok"`
	}
	if err := json.Unmarshal(connectResp, &resp); err != nil || !resp.OK {
		log.Printf("[chat-proxy] gateway connect rejected: %s", string(connectResp))
		browserConn.Close(websocket.StatusInternalError, "Gateway auth failed")
		return
	}

	log.Printf("[chat-proxy] %s authenticated, relaying", agentName)

	// Bidirectional relay — browser ↔ gateway
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		relay(r.Context(), browserConn, gatewayConn, "browser→gw")
	}()

	go func() {
		defer wg.Done()
		relay(r.Context(), gatewayConn, browserConn, "gw→browser")
	}()

	wg.Wait()
	log.Printf("[chat-proxy] %s disconnected", agentName)
}

// relay copies WebSocket messages from src to dst.
func relay(ctx context.Context, src, dst *websocket.Conn, label string) {
	for {
		msgType, data, err := src.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != -1 || err == io.EOF {
				dst.Close(websocket.StatusNormalClosure, "")
			} else {
				log.Printf("[chat-proxy] %s read error: %v", label, err)
				dst.Close(websocket.StatusInternalError, "relay error")
			}
			return
		}
		if err := dst.Write(ctx, msgType, data); err != nil {
			log.Printf("[chat-proxy] %s write error: %v", label, err)
			return
		}
	}
}
