// Package hub manages WebSocket connections for brains and actuators.
//
// Architecture: Erlang-inspired supervisor pattern in Go.
// Hub is the supervisor goroutine. Each connection gets its own goroutine.
// Communication via channels. Dead connections = goroutine exits, hub cleans up.
package hub

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/siofra-seksbot/botster-broker-go/internal/db"
)

// Message types for the WebSocket protocol.
const (
	TypeActuatorHello  = "actuator_hello"
	TypeBrainHello     = "brain_hello"
	TypeCommandRequest = "command_delivery"
	TypeCommandResult  = "command_result"
	TypeWake           = "wake"
	TypeSafeModeError  = "safe_mode"
	TypePing           = "ping"
	TypePong           = "pong"
)

// WSMessage is a generic envelope for WebSocket messages.
type WSMessage struct {
	Type       string          `json:"type"`
	ID         string          `json:"id,omitempty"`
	Token      string          `json:"token,omitempty"`
	Capability string          `json:"capability,omitempty"`
	ActuatorID string          `json:"actuator_id,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	Status     string          `json:"status,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	Text       string          `json:"text,omitempty"`
	TTL        int             `json:"ttl_seconds,omitempty"`
}

// Connection represents a connected brain or actuator.
type Connection struct {
	ID         string
	Role       string // "brain" or "actuator"
	AgentID    string // for brains: agent ID; for actuators: ""
	ActuatorID string // for actuators: actuator ID; for brains: ""
	AccountID  string
	ws         *websocket.Conn
	sendCh     chan []byte
	hub        *Hub
}

// Hub is the supervisor that manages all WebSocket connections.
type Hub struct {
	db        *db.DB
	masterKey string

	mu          sync.RWMutex
	brains      map[string]*Connection // agentID → connection
	actuators   map[string]*Connection // actuatorID → connection
	wakeBuffers map[string][]WSMessage // agentID → buffered wake messages

	// Channels for goroutine communication
	registerCh   chan *Connection
	unregisterCh chan *Connection
	commandCh    chan commandRequest

	// Result waiting (for sync REST commands)
	pendingMu sync.Mutex
	pending   map[string]chan WSMessage // commandID → result channel

	// Command origin tracking (for async WS result routing)
	originMu sync.Mutex
	origins  map[string]string // commandID → agentID
}

type commandRequest struct {
	agentID    string
	accountID  string
	msg        WSMessage
	resultCh   chan WSMessage // optional, for sync mode
}

// New creates a new Hub.
func New(database *db.DB, masterKey string) *Hub {
	return &Hub{
		db:           database,
		masterKey:    masterKey,
		brains:       make(map[string]*Connection),
		actuators:    make(map[string]*Connection),
		wakeBuffers:  make(map[string][]WSMessage),
		registerCh:   make(chan *Connection, 16),
		unregisterCh: make(chan *Connection, 16),
		commandCh:    make(chan commandRequest, 64),
		pending:      make(map[string]chan WSMessage),
		origins:      make(map[string]string),
	}
}

// Run starts the hub supervisor goroutine. Call in a goroutine.
func (h *Hub) Run() {
	log.Println("[hub] Supervisor started")
	for {
		select {
		case conn := <-h.registerCh:
			h.mu.Lock()
			if conn.Role == "brain" {
				h.brains[conn.AgentID] = conn
				log.Printf("[hub] Brain registered: agent=%s", conn.AgentID)
			} else {
				h.actuators[conn.ActuatorID] = conn
				h.db.UpdateActuatorStatus(conn.ActuatorID, "online")
				log.Printf("[hub] Actuator registered: id=%s", conn.ActuatorID)

				// Deliver buffered wake messages
				if msgs, ok := h.wakeBuffers[conn.ActuatorID]; ok {
					for _, msg := range msgs {
						data, _ := json.Marshal(msg)
						conn.sendCh <- data
					}
					delete(h.wakeBuffers, conn.ActuatorID)
					log.Printf("[hub] Delivered %d buffered wake messages to %s", len(msgs), conn.ActuatorID)
				}
			}
			h.mu.Unlock()

		case conn := <-h.unregisterCh:
			h.mu.Lock()
			if conn.Role == "brain" {
				delete(h.brains, conn.AgentID)
				log.Printf("[hub] Brain disconnected: agent=%s", conn.AgentID)
			} else {
				delete(h.actuators, conn.ActuatorID)
				h.db.UpdateActuatorStatus(conn.ActuatorID, "offline")
				log.Printf("[hub] Actuator disconnected: id=%s", conn.ActuatorID)
			}
			h.mu.Unlock()

		case cmd := <-h.commandCh:
			h.handleCommand(cmd)
		}
	}
}

// handleCommand routes a command to the appropriate actuator.
func (h *Hub) handleCommand(cmd commandRequest) {
	// Check safe mode
	globalSafe, _ := h.db.GetGlobalSafe()
	if globalSafe {
		errMsg := WSMessage{Type: TypeSafeModeError, ID: cmd.msg.ID, Error: "Global safe mode is active"}
		if cmd.resultCh != nil {
			cmd.resultCh <- errMsg
		}
		log.Printf("[hub] Command %s blocked: global safe mode", cmd.msg.ID)
		return
	}

	agent, _ := h.db.GetAgentByID(cmd.agentID)
	if agent != nil && agent.Safe {
		errMsg := WSMessage{Type: TypeSafeModeError, ID: cmd.msg.ID, Error: "Agent safe mode is active"}
		if cmd.resultCh != nil {
			cmd.resultCh <- errMsg
		}
		log.Printf("[hub] Command %s blocked: agent %s safe mode", cmd.msg.ID, cmd.agentID)
		return
	}

	// Resolve actuator
	actuator, _ := h.db.ResolveActuatorForAgent(cmd.agentID)
	if actuator == nil {
		errMsg := WSMessage{Type: TypeCommandResult, ID: cmd.msg.ID, Status: "error", Error: "No actuator available"}
		if cmd.resultCh != nil {
			cmd.resultCh <- errMsg
		}
		return
	}

	h.mu.RLock()
	conn, ok := h.actuators[actuator.ID]
	h.mu.RUnlock()

	if !ok {
		errMsg := WSMessage{Type: TypeCommandResult, ID: cmd.msg.ID, Status: "error", Error: "Actuator not connected"}
		if cmd.resultCh != nil {
			cmd.resultCh <- errMsg
		}
		return
	}

	// Register pending result if sync
	if cmd.resultCh != nil {
		h.pendingMu.Lock()
		h.pending[cmd.msg.ID] = cmd.resultCh
		h.pendingMu.Unlock()
	}

	// Track origin for async result routing
	h.originMu.Lock()
	h.origins[cmd.msg.ID] = cmd.agentID
	h.originMu.Unlock()

	// Send to actuator
	data, _ := json.Marshal(cmd.msg)
	conn.sendCh <- data
	log.Printf("[hub] Command %s routed to actuator %s (origin agent: %s)", cmd.msg.ID, actuator.Name, cmd.agentID)
}

// HandleWebSocket handles a new WebSocket connection (called from HTTP upgrade).
func (h *Hub) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // Accept from any origin for now
	})
	if err != nil {
		log.Printf("[hub] WebSocket accept error: %v", err)
		return
	}

	ctx := r.Context()

	conn := &Connection{
		ws:     ws,
		sendCh: make(chan []byte, 64),
		hub:    h,
	}

	// Legacy actuator protocol: auth via query params (?token=...&role=actuator&actuator_id=...)
	q := r.URL.Query()
	if q.Get("role") == "actuator" {
		token := q.Get("token")
		actuator, err := h.db.GetActuatorByToken(token)
		if err != nil || actuator == nil {
			log.Printf("[hub] Actuator query-param auth failed for id=%s", q.Get("actuator_id"))
			ws.Close(websocket.StatusPolicyViolation, "Invalid actuator token")
			return
		}
		conn.ID = actuator.ID
		conn.Role = "actuator"
		conn.ActuatorID = actuator.ID
		conn.AccountID = actuator.AccountID
		h.registerCh <- conn
		log.Printf("[hub] Actuator registered (legacy proto): id=%s name=%s", actuator.ID, actuator.Name)
		h.db.UpdateActuatorStatus(actuator.ID, "online")
		go conn.writePump(ctx)
		conn.readPump(ctx)
		h.unregisterCh <- conn
		close(conn.sendCh)
		return
	}

	// Modern protocol: read hello message
	_, data, err := ws.Read(ctx)
	if err != nil {
		ws.Close(websocket.StatusProtocolError, "Expected hello message")
		return
	}

	var hello WSMessage
	if err := json.Unmarshal(data, &hello); err != nil {
		ws.Close(websocket.StatusProtocolError, "Invalid hello message")
		return
	}

	switch hello.Type {
	case TypeActuatorHello:
		actuator, err := h.db.GetActuatorByToken(hello.Token)
		if err != nil || actuator == nil {
			ws.Close(websocket.StatusPolicyViolation, "Invalid actuator token")
			return
		}
		conn.ID = actuator.ID
		conn.Role = "actuator"
		conn.ActuatorID = actuator.ID
		conn.AccountID = actuator.AccountID

	case TypeBrainHello:
		agent, err := h.db.GetAgentByToken(hello.Token)
		if err != nil || agent == nil {
			ws.Close(websocket.StatusPolicyViolation, "Invalid agent token")
			return
		}
		conn.ID = agent.ID
		conn.Role = "brain"
		conn.AgentID = agent.ID
		conn.AccountID = agent.AccountID

	default:
		ws.Close(websocket.StatusProtocolError, "Expected actuator_hello or brain_hello")
		return
	}

	h.registerCh <- conn

	// Deliver buffered wake messages for brain connections
	if conn.Role == "brain" {
		// Small yield to let registerCh be processed before we write to sendCh
		go func() {
			h.DeliverBufferedWakes(conn.AgentID, conn)
		}()
	}

	// Start writer goroutine
	go conn.writePump(ctx)

	// Reader loop (runs in this goroutine)
	conn.readPump(ctx)

	// When readPump exits, clean up
	h.unregisterCh <- conn
	close(conn.sendCh)
}

// readPump reads messages from the WebSocket.
func (c *Connection) readPump(ctx context.Context) {
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			return // Connection closed
		}

		var msg WSMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case TypeCommandRequest:
			if c.Role == "brain" {
				c.hub.commandCh <- commandRequest{
					agentID:   c.AgentID,
					accountID: c.AccountID,
					msg:       msg,
				}
			}

		case TypeCommandResult:
			// Result from actuator — deliver to pending sync request or brain
			c.hub.pendingMu.Lock()
			ch, ok := c.hub.pending[msg.ID]
			if ok {
				ch <- msg
				delete(c.hub.pending, msg.ID)
			}
			c.hub.pendingMu.Unlock()

			if !ok {
				// Deliver result to the originating brain only
				c.hub.originMu.Lock()
				agentID, hasOrigin := c.hub.origins[msg.ID]
				delete(c.hub.origins, msg.ID)
				c.hub.originMu.Unlock()

				c.hub.mu.RLock()
				if hasOrigin {
					if brain, exists := c.hub.brains[agentID]; exists {
						data, _ := json.Marshal(msg)
						brain.sendCh <- data
					} else {
						log.Printf("[hub] Command %s result: origin brain %s not connected", msg.ID, agentID)
					}
				} else {
					// No origin tracked — broadcast as fallback (shouldn't happen)
					log.Printf("[hub] Command %s result: no origin tracked, broadcasting", msg.ID)
					for _, brain := range c.hub.brains {
						data, _ := json.Marshal(msg)
						brain.sendCh <- data
					}
				}
				c.hub.mu.RUnlock()
			}

		case TypePing:
			pong, _ := json.Marshal(WSMessage{Type: TypePong})
			c.sendCh <- pong
		}
	}
}

// writePump writes messages to the WebSocket.
func (c *Connection) writePump(ctx context.Context) {
	for data := range c.sendCh {
		err := c.ws.Write(ctx, websocket.MessageText, data)
		if err != nil {
			return
		}
	}
}

// SendCommand dispatches a command through the hub (for REST API sync mode).
func (h *Hub) SendCommand(agentID, accountID string, msg WSMessage, timeout time.Duration) (*WSMessage, error) {
	resultCh := make(chan WSMessage, 1)

	h.commandCh <- commandRequest{
		agentID:   agentID,
		accountID: accountID,
		msg:       msg,
		resultCh:  resultCh,
	}

	select {
	case result := <-resultCh:
		return &result, nil
	case <-time.After(timeout):
		// Clean up pending
		h.pendingMu.Lock()
		delete(h.pending, msg.ID)
		h.pendingMu.Unlock()
		return nil, nil // timeout
	}
}

// GetBrainConnection returns the brain (agent) WebSocket connection for the given agentID,
// or nil if the agent is not currently connected.
func (h *Hub) GetBrainConnection(agentID string) *Connection {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.brains[agentID]
}

// BufferWake stores a wake message for delivery when the agent's brain connects.
// The message is indexed by agentID (not actuatorID, as brains connect via agent token).
func (h *Hub) BufferWake(agentID, text, source, ts string) {
	msg := WSMessage{
		Type: TypeWake,
		Text: text,
	}
	h.mu.Lock()
	h.wakeBuffers[agentID] = append(h.wakeBuffers[agentID], msg)
	h.mu.Unlock()
	log.Printf("[hub] Buffered wake for agent %s (source=%s, ts=%s)", agentID, source, ts)
}

// DeliverBufferedWakes sends any buffered wake messages to a newly-connected brain.
// Called internally when a brain_hello is received and registers.
func (h *Hub) DeliverBufferedWakes(agentID string, conn *Connection) {
	h.mu.Lock()
	msgs, ok := h.wakeBuffers[agentID]
	if ok {
		delete(h.wakeBuffers, agentID)
	}
	h.mu.Unlock()

	if !ok {
		return
	}
	for _, msg := range msgs {
		data, _ := json.Marshal(msg)
		conn.sendCh <- data
	}
	log.Printf("[hub] Delivered %d buffered wake(s) to brain agent %s", len(msgs), agentID)
}

// SendCh returns the connection's send channel for direct message delivery.
// Used by external callers (e.g., notify handler) to push messages.
func (c *Connection) SendCh() chan []byte {
	return c.sendCh
}

// RequireBrokerToken validates a broker token via Authorization: Bearer,
// X-Broker-Token, or X-API-Key.
func (h *Hub) RequireBrokerToken(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := ""
			if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
				provided = strings.TrimPrefix(a, "Bearer ")
			}
			if provided == "" {
				provided = r.Header.Get("X-Broker-Token")
			}
			if provided == "" {
				provided = r.Header.Get("X-API-Key")
			}
			if provided == "" || provided != token {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// HandleSecretGet handles POST /v1/secrets/get for broker-owned in-memory secrets.
func (h *Hub) HandleSecretGet(store *SecretsStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Key string `json:"key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Key == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "key required"})
			return
		}

		value, ok := store.Get(req.Key)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "secret not found"})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"value":  value,
			"source": "broker",
		})
	}
}
