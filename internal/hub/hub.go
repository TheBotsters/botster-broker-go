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
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/siofra-seksbot/botster-broker-go/internal/db"
)

// Message types for the WebSocket protocol.
const (
	TypeActuatorHello  = "actuator_hello"
	TypeBrainHello     = "brain_hello"
	TypeCommandRequest = "command_request"
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

	// Send to actuator
	data, _ := json.Marshal(cmd.msg)
	conn.sendCh <- data
	log.Printf("[hub] Command %s routed to actuator %s", cmd.msg.ID, actuator.Name)
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

	// Read the hello message to identify the connection
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

	conn := &Connection{
		ws:     ws,
		sendCh: make(chan []byte, 64),
		hub:    h,
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
				// Deliver to brain via WS
				c.hub.mu.RLock()
				// Find which brain owns this command
				// For now, broadcast to all brains (TODO: track command→agent mapping)
				for _, brain := range c.hub.brains {
					data, _ := json.Marshal(msg)
					brain.sendCh <- data
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
