package proxy

import (
	"encoding/json"
	"testing"

	"github.com/TheBotsters/botster-broker-go/internal/link"
)

func TestNextConnID(t *testing.T) {
	p := New(DefaultConfig())
	id1 := p.nextConnID()
	id2 := p.nextConnID()
	if id1 == id2 {
		t.Fatalf("expected unique IDs, got %q and %q", id1, id2)
	}
	if id1 != "c1" {
		t.Fatalf("first ID: got %q, want %q", id1, "c1")
	}
	if id2 != "c2" {
		t.Fatalf("second ID: got %q, want %q", id2, "c2")
	}
}

func TestRegisterUnregister(t *testing.T) {
	p := New(DefaultConfig())

	brain := &wsConn{id: "c1", role: "brain", agentID: "ag_1"}
	act := &wsConn{id: "c2", role: "actuator", actuatorID: "act_1"}

	p.register(brain)
	p.register(act)

	if p.ConnCount() != 2 {
		t.Fatalf("conn count: got %d, want 2", p.ConnCount())
	}

	p.mu.RLock()
	if p.agents["ag_1"] != "c1" {
		t.Fatal("brain not in agents map")
	}
	if p.actuators["act_1"] != "c2" {
		t.Fatal("actuator not in actuators map")
	}
	p.mu.RUnlock()

	p.unregister(brain)
	if p.ConnCount() != 1 {
		t.Fatalf("conn count after unregister: got %d, want 1", p.ConnCount())
	}

	p.mu.RLock()
	if _, ok := p.agents["ag_1"]; ok {
		t.Fatal("brain still in agents map after unregister")
	}
	p.mu.RUnlock()

	p.unregister(act)
	if p.ConnCount() != 0 {
		t.Fatalf("conn count after both unregister: got %d, want 0", p.ConnCount())
	}
}

func TestUnregisterDoesNotRemoveReplacedConnection(t *testing.T) {
	// If a new brain connects for the same agentID before the old one is
	// unregistered, unregistering the old one should NOT remove the new one
	// from the agents map.
	p := New(DefaultConfig())

	old := &wsConn{id: "c1", role: "brain", agentID: "ag_1"}
	new := &wsConn{id: "c2", role: "brain", agentID: "ag_1"}

	p.register(old)
	p.register(new) // Overwrites agents["ag_1"] → "c2"

	p.unregister(old) // Should NOT delete agents["ag_1"] because it points to "c2", not "c1"

	p.mu.RLock()
	connID, ok := p.agents["ag_1"]
	p.mu.RUnlock()

	if !ok {
		t.Fatal("agents[ag_1] was deleted by unregister of old connection")
	}
	if connID != "c2" {
		t.Fatalf("agents[ag_1]: got %q, want %q", connID, "c2")
	}
}

func TestBufferMessage(t *testing.T) {
	p := New(Config{BufferSize: 3, BufferMaxBytes: 0})

	p.bufferMessage(link.LinkMessage{Type: link.TypeMessage, ConnID: "c1"})
	p.bufferMessage(link.LinkMessage{Type: link.TypeMessage, ConnID: "c2"})
	p.bufferMessage(link.LinkMessage{Type: link.TypeMessage, ConnID: "c3"})

	p.bufMu.Lock()
	if len(p.buffer) != 3 {
		t.Fatalf("buffer len: got %d, want 3", len(p.buffer))
	}
	p.bufMu.Unlock()

	// Fourth message should evict the oldest.
	p.bufferMessage(link.LinkMessage{Type: link.TypeMessage, ConnID: "c4"})

	p.bufMu.Lock()
	if len(p.buffer) != 3 {
		t.Fatalf("buffer len after overflow: got %d, want 3", len(p.buffer))
	}
	if p.buffer[0].ConnID != "c2" {
		t.Fatalf("oldest after eviction: got %q, want %q", p.buffer[0].ConnID, "c2")
	}
	if p.buffer[2].ConnID != "c4" {
		t.Fatalf("newest: got %q, want %q", p.buffer[2].ConnID, "c4")
	}
	p.bufMu.Unlock()
}

func TestSendToConn(t *testing.T) {
	p := New(DefaultConfig())
	c := &wsConn{id: "c1", sendCh: make(chan []byte, 10)}
	p.mu.Lock()
	p.conns["c1"] = c
	p.mu.Unlock()

	payload := json.RawMessage(`{"type":"wake","text":"hi"}`)
	p.sendToConn("c1", payload)

	select {
	case data := <-c.sendCh:
		if string(data) != string(payload) {
			t.Fatalf("got %s, want %s", data, payload)
		}
	default:
		t.Fatal("expected message in sendCh")
	}

	// Send to nonexistent connection should not panic.
	p.sendToConn("c999", payload)
}

func TestSendToConnFullChannel(t *testing.T) {
	p := New(DefaultConfig())
	c := &wsConn{id: "c1", sendCh: make(chan []byte, 1)}
	p.mu.Lock()
	p.conns["c1"] = c
	p.mu.Unlock()

	// Fill the channel.
	c.sendCh <- []byte("first")

	// This should drop (channel full), not block.
	payload := json.RawMessage(`{"type":"wake","text":"dropped"}`)
	p.sendToConn("c1", payload)

	// Should still have only the first message.
	data := <-c.sendCh
	if string(data) != "first" {
		t.Fatalf("got %s, want 'first'", data)
	}
}

func TestHandleBrokerMessageAuthResult(t *testing.T) {
	p := New(DefaultConfig())

	ch := make(chan link.LinkMessage, 1)
	p.authMu.Lock()
	p.authPending["auth_c1"] = ch
	p.authMu.Unlock()

	result := link.LinkMessage{
		Type:      link.TypeAuthResult,
		RequestID: "auth_c1",
		OK:        true,
		ConnID:    "c1",
		Role:      "brain",
		AgentID:   "ag_1",
	}
	p.handleBrokerMessage(result)

	got := <-ch
	if !got.OK {
		t.Fatal("expected OK=true")
	}
	if got.AgentID != "ag_1" {
		t.Fatalf("agent_id: got %q, want %q", got.AgentID, "ag_1")
	}
}

func TestHandleBrokerMessageSendAgent(t *testing.T) {
	p := New(DefaultConfig())

	c := &wsConn{id: "c1", role: "brain", agentID: "ag_1", sendCh: make(chan []byte, 10)}
	p.register(c)

	payload := json.RawMessage(`{"type":"wake","text":"hello agent"}`)
	p.handleBrokerMessage(link.LinkMessage{
		Type:    link.TypeSendAgent,
		AgentID: "ag_1",
		Payload: payload,
	})

	select {
	case data := <-c.sendCh:
		if string(data) != string(payload) {
			t.Fatalf("got %s, want %s", data, payload)
		}
	default:
		t.Fatal("expected message in sendCh")
	}
}

func TestHandleBrokerMessageSendActuator(t *testing.T) {
	p := New(DefaultConfig())

	c := &wsConn{id: "c2", role: "actuator", actuatorID: "act_5", sendCh: make(chan []byte, 10)}
	p.register(c)

	payload := json.RawMessage(`{"type":"command_delivery","id":"cmd_1"}`)
	p.handleBrokerMessage(link.LinkMessage{
		Type:       link.TypeSendActuator,
		ActuatorID: "act_5",
		Payload:    payload,
	})

	select {
	case data := <-c.sendCh:
		if string(data) != string(payload) {
			t.Fatalf("got %s, want %s", data, payload)
		}
	default:
		t.Fatal("expected message in sendCh")
	}
}
