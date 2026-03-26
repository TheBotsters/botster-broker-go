package hub

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/TheBotsters/botster-broker-go/internal/link"
)

// LinkClient connects the Hub to the ws-proxy process over a Unix domain socket.
// It replaces direct WebSocket management — the Hub sends/receives through the
// link instead of managing gorilla/coder websocket connections directly.
type LinkClient struct {
	mu      sync.Mutex
	enc     *link.Encoder
	hub     *Hub
	conn    net.Conn
	running bool
}

// NewLinkClient creates a LinkClient wired to the given Hub.
func NewLinkClient(h *Hub) *LinkClient {
	return &LinkClient{hub: h}
}

// Connect dials the ws-proxy Unix socket and starts the read loop.
// Blocks until the connection is lost or the context expires.
func (lc *LinkClient) Connect(socketPath string) error {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return fmt.Errorf("link connect: %w", err)
	}

	enc := link.NewEncoder(conn)
	dec := link.NewDecoder(conn)

	lc.mu.Lock()
	lc.enc = enc
	lc.conn = conn
	lc.running = true
	lc.mu.Unlock()

	log.Printf("[hub/link] connected to ws-proxy at %s", socketPath)

	// Read loop: process messages from the proxy.
	for {
		msg, err := dec.Decode()
		if err != nil {
			log.Printf("[hub/link] read error: %v", err)
			break
		}
		lc.handleProxyMessage(msg)
	}

	lc.mu.Lock()
	lc.enc = nil
	lc.conn = nil
	lc.running = false
	lc.mu.Unlock()

	log.Printf("[hub/link] disconnected from ws-proxy")
	return nil
}

// ConnectWithRetry connects to the proxy with exponential backoff.
// Never returns unless the process is shutting down.
func (lc *LinkClient) ConnectWithRetry(socketPath string) {
	backoff := time.Second
	maxBackoff := 30 * time.Second
	for {
		err := lc.Connect(socketPath)
		if err != nil {
			log.Printf("[hub/link] connection failed: %v (retry in %s)", err, backoff)
		}
		time.Sleep(backoff)
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// Send encodes a LinkMessage to the proxy.
func (lc *LinkClient) Send(msg link.LinkMessage) error {
	lc.mu.Lock()
	enc := lc.enc
	lc.mu.Unlock()
	if enc == nil {
		return fmt.Errorf("link not connected")
	}
	return enc.Encode(msg)
}

// SendToConn sends a WSMessage payload to a specific proxy connection.
func (lc *LinkClient) SendToConn(connID string, wsMsg WSMessage) error {
	data, err := json.Marshal(wsMsg)
	if err != nil {
		return err
	}
	return lc.Send(link.LinkMessage{
		Type:    link.TypeSend,
		ConnID:  connID,
		Payload: json.RawMessage(data),
	})
}

// SendToAgent sends a WSMessage payload to the brain for the given agentID.
func (lc *LinkClient) SendToAgent(agentID string, wsMsg WSMessage) error {
	data, err := json.Marshal(wsMsg)
	if err != nil {
		return err
	}
	return lc.Send(link.LinkMessage{
		Type:    link.TypeSendAgent,
		AgentID: agentID,
		Payload: json.RawMessage(data),
	})
}

// SendToActuator sends a WSMessage payload to a specific actuator.
func (lc *LinkClient) SendToActuator(actuatorID string, wsMsg WSMessage) error {
	data, err := json.Marshal(wsMsg)
	if err != nil {
		return err
	}
	return lc.Send(link.LinkMessage{
		Type:       link.TypeSendActuator,
		ActuatorID: actuatorID,
		Payload:    json.RawMessage(data),
	})
}

// CloseConn asks the proxy to close a connection.
func (lc *LinkClient) CloseConn(connID string, code int, reason string) error {
	return lc.Send(link.LinkMessage{
		Type:   link.TypeClose,
		ConnID: connID,
		Code:   code,
		Reason: reason,
	})
}

// handleProxyMessage dispatches a message from the proxy to the Hub.
func (lc *LinkClient) handleProxyMessage(msg link.LinkMessage) {
	switch msg.Type {
	case link.TypeAuthRequest:
		lc.handleAuth(msg)

	case link.TypeConnect:
		conn := &Connection{
			ID:         msg.ConnID,
			Role:       msg.Role,
			AgentID:    msg.AgentID,
			ActuatorID: msg.ActuatorID,
			AccountID:  msg.AccountID,
			linkClient: lc,
		}
		lc.hub.registerCh <- conn

		// Deliver buffered wakes for brain connections.
		if conn.Role == "brain" {
			go lc.hub.DeliverBufferedWakesViaLink(conn.AgentID)
		}

	case link.TypeDisconnect:
		lc.hub.mu.RLock()
		// Find the connection by conn_id.
		var found *Connection
		for _, c := range lc.hub.brains {
			if c.ID == msg.ConnID {
				found = c
				break
			}
		}
		if found == nil {
			for _, c := range lc.hub.actuators {
				if c.ID == msg.ConnID {
					found = c
					break
				}
			}
		}
		lc.hub.mu.RUnlock()

		if found != nil {
			lc.hub.unregisterCh <- found
		}

	case link.TypeMessage:
		// Forward a WS message from a client to the Hub's processing logic.
		lc.hub.mu.RLock()
		var conn *Connection
		for _, c := range lc.hub.brains {
			if c.ID == msg.ConnID {
				conn = c
				break
			}
		}
		if conn == nil {
			for _, c := range lc.hub.actuators {
				if c.ID == msg.ConnID {
					conn = c
					break
				}
			}
		}
		lc.hub.mu.RUnlock()

		if conn == nil {
			log.Printf("[hub/link] message from unknown conn %s", msg.ConnID)
			return
		}

		var wsMsg WSMessage
		if err := json.Unmarshal(msg.Payload, &wsMsg); err != nil {
			log.Printf("[hub/link] bad WS message from %s: %v", msg.ConnID, err)
			return
		}

		// Dispatch to the same handlers as the old readPump.
		lc.dispatchWSMessage(conn, wsMsg)

	default:
		log.Printf("[hub/link] unknown proxy message type: %s", msg.Type)
	}
}

// handleAuth processes an auth_request from the proxy.
func (lc *LinkClient) handleAuth(msg link.LinkMessage) {
	var hello WSMessage
	if err := json.Unmarshal(msg.Hello, &hello); err != nil {
		_ = lc.Send(link.LinkMessage{
			Type:      link.TypeAuthResult,
			RequestID: msg.RequestID,
			OK:        false,
			Error:     "Invalid hello message",
		})
		return
	}

	result := link.LinkMessage{
		Type:      link.TypeAuthResult,
		RequestID: msg.RequestID,
		ConnID:    msg.ConnID,
	}

	switch hello.Type {
	case TypeActuatorHello:
		actuator, err := lc.hub.db.GetActuatorByToken(hello.Token)
		if err != nil || actuator == nil {
			result.OK = false
			result.Error = "Invalid actuator token"
		} else {
			result.OK = true
			result.Role = "actuator"
			result.ActuatorID = actuator.ID
			result.AccountID = actuator.AccountID
			result.RecoveryOnly = actuator.AuthMode == "recovery"
		}

	case TypeBrainHello:
		agent, err := lc.hub.db.GetAgentByToken(hello.Token)
		if err != nil || agent == nil {
			result.OK = false
			result.Error = "Invalid agent token"
		} else {
			result.OK = true
			result.Role = "brain"
			result.AgentID = agent.ID
			result.AccountID = agent.AccountID
			result.RecoveryOnly = agent.AuthMode == "recovery"
		}

	default:
		result.OK = false
		result.Error = "Expected actuator_hello or brain_hello"
	}

	_ = lc.Send(result)
}

// dispatchWSMessage routes a decoded WS message from a connection, mirroring
// the logic that was previously in Connection.readPump.
func (lc *LinkClient) dispatchWSMessage(conn *Connection, msg WSMessage) {
	switch msg.Type {
	case TypeCommandRequest:
		if conn.Role == "brain" {
			if conn.RecoveryOnly {
				log.Printf("[hub/link] Dropping command_request from recovery-only brain agent=%s", conn.AgentID)
				return
			}
			lc.hub.commandCh <- commandRequest{
				agentID:   conn.AgentID,
				accountID: conn.AccountID,
				msg:       msg,
			}
		}

	case TypeCommandResult:
		// Result from actuator — persist and deliver.
		resultBytes, _ := json.Marshal(msg.Result)
		resultText := string(resultBytes)
		if msg.Error != "" {
			resultText = msg.Error
		}
		status := msg.Status
		if status == "" {
			status = StatusOK
		}
		_ = lc.hub.db.RecordCommandResult(msg.ID, status, resultText)

		// Deliver to pending sync request.
		lc.hub.pendingMu.Lock()
		ch, ok := lc.hub.pending[msg.ID]
		if ok {
			ch <- msg
			delete(lc.hub.pending, msg.ID)
		}
		lc.hub.pendingMu.Unlock()

		if !ok {
			// Async mode: deliver to originating brain via link.
			lc.hub.originMu.Lock()
			agentID, hasOrigin := lc.hub.origins[msg.ID]
			delete(lc.hub.origins, msg.ID)
			lc.hub.originMu.Unlock()

			if hasOrigin {
				_ = lc.SendToAgent(agentID, msg)
			} else {
				log.Printf("[hub/link] Command %s result: no origin tracked", msg.ID)
			}
		}

	case TypeTokenRotatedAck:
		if msg.RotationID == "" {
			log.Printf("[hub/link] Ignoring token_rotated_ack with empty rotation_id from %s", conn.Role)
			return
		}
		if conn.Role == "brain" {
			ok, err := lc.hub.db.AcknowledgeAgentTokenRotation(conn.AgentID, msg.RotationID)
			if err != nil {
				log.Printf("[hub/link] Failed token rotation ack for brain %s: %v", conn.AgentID, err)
				return
			}
			if ok {
				conn.RecoveryOnly = false
				log.Printf("[hub/link] Cleared pending token rotation for brain %s", conn.AgentID)
			}
		} else if conn.Role == "actuator" {
			ok, err := lc.hub.db.AcknowledgeActuatorTokenRotation(conn.ActuatorID, msg.RotationID)
			if err != nil {
				log.Printf("[hub/link] Failed token rotation ack for actuator %s: %v", conn.ActuatorID, err)
				return
			}
			if ok {
				conn.RecoveryOnly = false
				log.Printf("[hub/link] Cleared pending token rotation for actuator %s", conn.ActuatorID)
			}
		}

	case TypePing:
		// In link mode, pings are handled by the proxy. But if one leaks through:
		_ = lc.SendToConn(conn.ID, WSMessage{Type: TypePong})
	}
}
