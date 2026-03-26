// Package link defines the wire protocol between the ws-proxy (long-lived
// WebSocket frontend) and the broker (restartable business-logic backend).
//
// The two processes communicate over a Unix domain socket using newline-
// delimited JSON (JSON-lines). Each message is a single LinkMessage encoded
// as one JSON object followed by '\n'.
package link

import "encoding/json"

// Message type constants — proxy → broker.
const (
	TypeAuthRequest = "auth_request" // Proxy asks broker to validate a hello.
	TypeConnect     = "connect"      // Proxy notifies broker of a new authenticated connection.
	TypeDisconnect  = "disconnect"   // Proxy notifies broker a connection closed.
	TypeMessage     = "message"      // Proxy forwards a WS message from a client.
)

// Message type constants — broker → proxy.
const (
	TypeAuthResult   = "auth_result"   // Broker returns auth decision for a hello.
	TypeSend         = "send"          // Broker sends a message to a specific connection.
	TypeSendAgent    = "send_agent"    // Broker sends a message to a brain by agent ID.
	TypeSendActuator = "send_actuator" // Broker sends a message to an actuator by ID.
	TypeClose        = "close"         // Broker asks proxy to close a connection.
)

// LinkMessage is the envelope for all messages on the Unix socket link.
// It follows the flat-struct pattern used by hub.WSMessage — a single
// type with optional fields for each variant rather than nested oneofs.
type LinkMessage struct {
	Type string `json:"type"`

	// Identifiers
	ConnID     string `json:"conn_id,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	Role       string `json:"role,omitempty"` // "brain" or "actuator"
	AgentID    string `json:"agent_id,omitempty"`
	AccountID  string `json:"account_id,omitempty"`
	ActuatorID string `json:"actuator_id,omitempty"`

	// Auth
	OK           bool   `json:"ok,omitempty"`
	RecoveryOnly bool   `json:"recovery_only,omitempty"`
	Error        string `json:"error,omitempty"`

	// Close
	Code   int    `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`

	// Payloads — raw JSON, not decoded by the link layer.
	// Hello carries the original WS hello message (for auth_request).
	// Payload carries an original WSMessage (for message/send/send_agent/send_actuator).
	Hello   json.RawMessage `json:"hello,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}
