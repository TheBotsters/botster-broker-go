// Package proxy implements the long-lived WebSocket proxy process for the
// broker process split. It accepts WebSocket connections from brains and
// actuators, delegates authentication to the broker over a Unix socket link,
// maintains a connection map, and forwards messages bidirectionally.
//
// The proxy handles ping/pong keepalive autonomously — the broker is not
// involved in keepalive. This is what allows broker restarts without
// dropping WebSocket connections.
package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TheBotsters/botster-broker-go/internal/link"
	"github.com/coder/websocket"
)

// Config holds proxy configuration.
type Config struct {
	// SocketPath is the Unix domain socket path for the broker link.
	SocketPath string

	// WSListenAddr is the address to listen for WebSocket connections.
	WSListenAddr string

	// AuthTimeout is how long to wait for an auth_result from the broker.
	AuthTimeout time.Duration

	// BufferSize is the max number of messages to buffer during broker restart.
	BufferSize int

	// BufferMaxBytes is the max total bytes to buffer (0 = no byte limit).
	BufferMaxBytes int64
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		SocketPath:     "/run/botster-broker/hub.sock",
		WSListenAddr:   ":9084",
		AuthTimeout:    5 * time.Second,
		BufferSize:     1000,
		BufferMaxBytes: 10 << 20, // 10MB
	}
}

// Proxy is the WebSocket proxy process.
type Proxy struct {
	cfg Config

	// Connection map: WebSocket clients.
	mu    sync.RWMutex
	conns map[string]*wsConn // connID → wsConn

	// Reverse maps for targeted sends.
	agents    map[string]string // agentID → connID
	actuators map[string]string // actuatorID → connID

	// Broker link.
	linkMu  sync.Mutex
	linkEnc *link.Encoder
	linkUp  atomic.Bool

	// Pending auth requests.
	authMu      sync.Mutex
	authPending map[string]chan link.LinkMessage // requestID → result channel

	// Message buffer for broker restarts.
	bufMu     sync.Mutex
	buffer    []link.LinkMessage
	bufBytes  int64
	buffering bool

	// Connection ID counter.
	nextID atomic.Uint64

	// Stats.
	totalConns   atomic.Int64
	totalMsgsIn  atomic.Int64
	totalMsgsOut atomic.Int64
}

// New creates a new Proxy.
func New(cfg Config) *Proxy {
	return &Proxy{
		cfg:         cfg,
		conns:       make(map[string]*wsConn),
		agents:      make(map[string]string),
		actuators:   make(map[string]string),
		authPending: make(map[string]chan link.LinkMessage),
	}
}

// wsConn represents a single WebSocket client connection.
type wsConn struct {
	id         string
	role       string // "brain" or "actuator"
	agentID    string
	actuatorID string
	accountID  string
	ws         *websocket.Conn
	sendCh     chan []byte
	cancel     context.CancelFunc
}

// nextConnID returns a monotonically increasing connection ID.
func (p *Proxy) nextConnID() string {
	return fmt.Sprintf("c%d", p.nextID.Add(1))
}

// HandleWebSocket handles a new WebSocket upgrade.
func (p *Proxy) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Printf("[ws-proxy] accept error: %v", err)
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	connID := p.nextConnID()

	conn := &wsConn{
		id:     connID,
		ws:     ws,
		sendCh: make(chan []byte, 64),
		cancel: cancel,
	}

	// Read the hello message with a timeout.
	readCtx, readCancel := context.WithTimeout(ctx, 10*time.Second)
	_, helloData, err := ws.Read(readCtx)
	readCancel()
	if err != nil {
		ws.Close(websocket.StatusProtocolError, "Expected hello message")
		cancel()
		return
	}

	// Check for legacy query-param auth.
	q := r.URL.Query()
	if q.Get("role") == "actuator" {
		// Legacy protocol — build a synthetic hello and auth it.
		helloData, _ = json.Marshal(map[string]string{
			"type":        "actuator_hello",
			"token":       q.Get("token"),
			"actuator_id": q.Get("actuator_id"),
		})
	}

	// Delegate auth to the broker.
	result, err := p.authViaLink(connID, helloData)
	if err != nil {
		log.Printf("[ws-proxy] auth error for %s: %v", connID, err)
		ws.Close(websocket.StatusInternalError, "Auth unavailable")
		cancel()
		return
	}
	if !result.OK {
		log.Printf("[ws-proxy] auth rejected for %s: %s", connID, result.Error)
		ws.Close(websocket.StatusPolicyViolation, result.Error)
		cancel()
		return
	}

	// Populate connection metadata from auth result.
	conn.role = result.Role
	conn.agentID = result.AgentID
	conn.actuatorID = result.ActuatorID
	conn.accountID = result.AccountID

	// Register in connection map.
	p.register(conn)
	p.totalConns.Add(1)

	// Notify broker of new connection.
	p.sendToLink(link.LinkMessage{
		Type:       link.TypeConnect,
		ConnID:     connID,
		Role:       conn.role,
		AgentID:    conn.agentID,
		AccountID:  conn.accountID,
		ActuatorID: conn.actuatorID,
	})

	log.Printf("[ws-proxy] registered %s role=%s agent=%s actuator=%s", connID, conn.role, conn.agentID, conn.actuatorID)

	// Start writer goroutine.
	go p.writePump(ctx, conn)

	// Start ping goroutine (autonomous, no broker involvement).
	go p.pingPump(ctx, conn)

	// Reader loop (blocks).
	p.readPump(ctx, conn)

	// Cleanup.
	p.unregister(conn)
	cancel()
	close(conn.sendCh)

	p.sendToLink(link.LinkMessage{
		Type:   link.TypeDisconnect,
		ConnID: connID,
		Reason: "client_closed",
	})

	log.Printf("[ws-proxy] disconnected %s", connID)
}

// register adds a connection to the maps.
func (p *Proxy) register(c *wsConn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.conns[c.id] = c
	if c.role == "brain" && c.agentID != "" {
		p.agents[c.agentID] = c.id
	}
	if c.role == "actuator" && c.actuatorID != "" {
		p.actuators[c.actuatorID] = c.id
	}
}

// unregister removes a connection from the maps.
func (p *Proxy) unregister(c *wsConn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.conns, c.id)
	if c.role == "brain" && c.agentID != "" {
		if p.agents[c.agentID] == c.id {
			delete(p.agents, c.agentID)
		}
	}
	if c.role == "actuator" && c.actuatorID != "" {
		if p.actuators[c.actuatorID] == c.id {
			delete(p.actuators, c.actuatorID)
		}
	}
}

// readPump reads messages from a WebSocket client and forwards to the broker.
func (p *Proxy) readPump(ctx context.Context, c *wsConn) {
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			return
		}
		p.totalMsgsIn.Add(1)
		p.sendToLink(link.LinkMessage{
			Type:    link.TypeMessage,
			ConnID:  c.id,
			Payload: json.RawMessage(data),
		})
	}
}

// writePump writes messages from sendCh to the WebSocket client.
func (p *Proxy) writePump(ctx context.Context, c *wsConn) {
	for {
		select {
		case data, ok := <-c.sendCh:
			if !ok {
				return
			}
			if err := c.ws.Write(ctx, websocket.MessageText, data); err != nil {
				return
			}
			p.totalMsgsOut.Add(1)
		case <-ctx.Done():
			return
		}
	}
}

// pingPump sends WebSocket pings at a fixed interval.
func (p *Proxy) pingPump(ctx context.Context, c *wsConn) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := c.ws.Ping(ctx); err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// authViaLink sends an auth_request to the broker and waits for auth_result.
func (p *Proxy) authViaLink(connID string, helloData []byte) (link.LinkMessage, error) {
	requestID := fmt.Sprintf("auth_%s", connID)

	resultCh := make(chan link.LinkMessage, 1)
	p.authMu.Lock()
	p.authPending[requestID] = resultCh
	p.authMu.Unlock()

	defer func() {
		p.authMu.Lock()
		delete(p.authPending, requestID)
		p.authMu.Unlock()
	}()

	p.sendToLink(link.LinkMessage{
		Type:      link.TypeAuthRequest,
		RequestID: requestID,
		ConnID:    connID,
		Hello:     json.RawMessage(helloData),
	})

	select {
	case result := <-resultCh:
		return result, nil
	case <-time.After(p.cfg.AuthTimeout):
		return link.LinkMessage{}, fmt.Errorf("auth timeout after %s", p.cfg.AuthTimeout)
	}
}

// sendToLink sends a message to the broker. If the link is down, the message
// is buffered (if buffering is active) or dropped with a warning.
func (p *Proxy) sendToLink(msg link.LinkMessage) {
	p.linkMu.Lock()
	enc := p.linkEnc
	p.linkMu.Unlock()

	if enc != nil {
		if err := enc.Encode(msg); err != nil {
			log.Printf("[ws-proxy] link write error: %v", err)
			// Link may have broken — will be detected by the read loop.
		}
		return
	}

	// Link is down — buffer if appropriate.
	if msg.Type == link.TypeMessage || msg.Type == link.TypeDisconnect {
		p.bufferMessage(msg)
	}
}

// bufferMessage stores a message for replay when the broker reconnects.
func (p *Proxy) bufferMessage(msg link.LinkMessage) {
	p.bufMu.Lock()
	defer p.bufMu.Unlock()
	p.buffering = true

	if len(p.buffer) >= p.cfg.BufferSize {
		// Drop oldest.
		p.buffer = p.buffer[1:]
		log.Printf("[ws-proxy] buffer overflow — dropped oldest message")
	}

	msgBytes, _ := json.Marshal(msg)
	msgSize := int64(len(msgBytes))
	if p.cfg.BufferMaxBytes > 0 && p.bufBytes+msgSize > p.cfg.BufferMaxBytes {
		// Drop oldest until we have room.
		for len(p.buffer) > 0 && p.bufBytes+msgSize > p.cfg.BufferMaxBytes {
			oldBytes, _ := json.Marshal(p.buffer[0])
			p.bufBytes -= int64(len(oldBytes))
			p.buffer = p.buffer[1:]
		}
		log.Printf("[ws-proxy] buffer byte limit — evicted messages")
	}

	p.buffer = append(p.buffer, msg)
	p.bufBytes += msgSize
}

// HandleLinkConnection handles a new broker connection on the Unix socket.
// Called when the broker connects (or reconnects after a restart).
func (p *Proxy) HandleLinkConnection(conn net.Conn) {
	log.Printf("[ws-proxy] broker connected via link")

	enc := link.NewEncoder(conn)
	dec := link.NewDecoder(conn)

	// Set the encoder so sends go to this broker.
	p.linkMu.Lock()
	p.linkEnc = enc
	p.linkMu.Unlock()
	p.linkUp.Store(true)

	// Replay existing connections so the broker knows who's connected.
	p.replayConnections(enc)

	// Drain any buffered messages.
	p.drainBuffer(enc)

	// Read loop — process messages from broker.
	for {
		msg, err := dec.Decode()
		if err != nil {
			log.Printf("[ws-proxy] broker link read error: %v", err)
			break
		}
		p.handleBrokerMessage(msg)
	}

	// Broker disconnected — clear encoder, enter buffering mode.
	p.linkMu.Lock()
	p.linkEnc = nil
	p.linkMu.Unlock()
	p.linkUp.Store(false)

	log.Printf("[ws-proxy] broker disconnected — entering buffer mode")
}

// replayConnections sends a connect message for every active WS connection.
func (p *Proxy) replayConnections(enc *link.Encoder) {
	p.mu.RLock()
	conns := make([]*wsConn, 0, len(p.conns))
	for _, c := range p.conns {
		conns = append(conns, c)
	}
	p.mu.RUnlock()

	for _, c := range conns {
		_ = enc.Encode(link.LinkMessage{
			Type:       link.TypeConnect,
			ConnID:     c.id,
			Role:       c.role,
			AgentID:    c.agentID,
			AccountID:  c.accountID,
			ActuatorID: c.actuatorID,
		})
	}
	log.Printf("[ws-proxy] replayed %d connections to broker", len(conns))
}

// drainBuffer sends all buffered messages to the broker and clears the buffer.
func (p *Proxy) drainBuffer(enc *link.Encoder) {
	p.bufMu.Lock()
	buf := p.buffer
	p.buffer = nil
	p.bufBytes = 0
	p.buffering = false
	p.bufMu.Unlock()

	if len(buf) == 0 {
		return
	}

	for _, msg := range buf {
		if err := enc.Encode(msg); err != nil {
			log.Printf("[ws-proxy] buffer drain write error: %v", err)
			return
		}
	}
	log.Printf("[ws-proxy] drained %d buffered messages to broker", len(buf))
}

// handleBrokerMessage processes a message from the broker.
func (p *Proxy) handleBrokerMessage(msg link.LinkMessage) {
	switch msg.Type {
	case link.TypeAuthResult:
		p.authMu.Lock()
		ch, ok := p.authPending[msg.RequestID]
		p.authMu.Unlock()
		if ok {
			ch <- msg
		} else {
			log.Printf("[ws-proxy] auth_result for unknown request %s", msg.RequestID)
		}

	case link.TypeSend:
		p.sendToConn(msg.ConnID, msg.Payload)

	case link.TypeSendAgent:
		p.mu.RLock()
		connID, ok := p.agents[msg.AgentID]
		p.mu.RUnlock()
		if ok {
			p.sendToConn(connID, msg.Payload)
		} else {
			log.Printf("[ws-proxy] send_agent: agent %s not connected", msg.AgentID)
		}

	case link.TypeSendActuator:
		p.mu.RLock()
		connID, ok := p.actuators[msg.ActuatorID]
		p.mu.RUnlock()
		if ok {
			p.sendToConn(connID, msg.Payload)
		} else {
			log.Printf("[ws-proxy] send_actuator: actuator %s not connected", msg.ActuatorID)
		}

	case link.TypeClose:
		p.mu.RLock()
		c, ok := p.conns[msg.ConnID]
		p.mu.RUnlock()
		if ok {
			c.ws.Close(websocket.StatusCode(msg.Code), msg.Reason)
			c.cancel()
		}

	default:
		log.Printf("[ws-proxy] unknown broker message type: %s", msg.Type)
	}
}

// sendToConn sends raw payload bytes to a specific connection's send channel.
func (p *Proxy) sendToConn(connID string, payload json.RawMessage) {
	p.mu.RLock()
	c, ok := p.conns[connID]
	p.mu.RUnlock()
	if !ok {
		return
	}
	select {
	case c.sendCh <- []byte(payload):
	default:
		log.Printf("[ws-proxy] send channel full for %s, dropping", connID)
	}
}

// HealthHandler returns an HTTP handler for the /health endpoint.
func (p *Proxy) HealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p.mu.RLock()
		connCount := len(p.conns)
		brainCount := len(p.agents)
		actuatorCount := len(p.actuators)
		p.mu.RUnlock()

		p.bufMu.Lock()
		bufLen := len(p.buffer)
		bufBytes := p.bufBytes
		buffering := p.buffering
		p.bufMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":          "ok",
			"link_connected":  p.linkUp.Load(),
			"connections":     connCount,
			"brains":          brainCount,
			"actuators":       actuatorCount,
			"buffer_messages": bufLen,
			"buffer_bytes":    bufBytes,
			"buffering":       buffering,
			"total_conns":     p.totalConns.Load(),
			"total_msgs_in":   p.totalMsgsIn.Load(),
			"total_msgs_out":  p.totalMsgsOut.Load(),
		})
	}
}

// ConnCount returns the current number of active connections.
func (p *Proxy) ConnCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.conns)
}

// LinkUp returns whether the broker link is currently connected.
func (p *Proxy) LinkUp() bool {
	return p.linkUp.Load()
}
