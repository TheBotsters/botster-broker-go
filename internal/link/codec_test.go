package link

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestRoundTripAuthRequest(t *testing.T) {
	hello := json.RawMessage(`{"type":"brain_hello","token":"seks_agent_abc"}`)
	orig := LinkMessage{
		Type:      TypeAuthRequest,
		RequestID: "r1",
		Hello:     hello,
	}
	got := roundTrip(t, orig)
	if got.Type != TypeAuthRequest {
		t.Fatalf("type: got %q, want %q", got.Type, TypeAuthRequest)
	}
	if got.RequestID != "r1" {
		t.Fatalf("request_id: got %q, want %q", got.RequestID, "r1")
	}
	if string(got.Hello) != string(hello) {
		t.Fatalf("hello: got %s, want %s", got.Hello, hello)
	}
}

func TestRoundTripAuthResult(t *testing.T) {
	orig := LinkMessage{
		Type:         TypeAuthResult,
		RequestID:    "r1",
		OK:           true,
		ConnID:       "c42",
		Role:         "brain",
		AgentID:      "ag_123",
		AccountID:    "acc_1",
		RecoveryOnly: false,
	}
	got := roundTrip(t, orig)
	if !got.OK {
		t.Fatal("ok: got false, want true")
	}
	if got.ConnID != "c42" {
		t.Fatalf("conn_id: got %q, want %q", got.ConnID, "c42")
	}
	if got.Role != "brain" {
		t.Fatalf("role: got %q, want %q", got.Role, "brain")
	}
	if got.AgentID != "ag_123" {
		t.Fatalf("agent_id: got %q, want %q", got.AgentID, "ag_123")
	}
}

func TestRoundTripAuthResultFail(t *testing.T) {
	orig := LinkMessage{
		Type:      TypeAuthResult,
		RequestID: "r2",
		OK:        false,
		Error:     "Invalid agent token",
	}
	got := roundTrip(t, orig)
	if got.OK {
		t.Fatal("ok: got true, want false")
	}
	if got.Error != "Invalid agent token" {
		t.Fatalf("error: got %q, want %q", got.Error, "Invalid agent token")
	}
}

func TestRoundTripConnect(t *testing.T) {
	orig := LinkMessage{
		Type:      TypeConnect,
		ConnID:    "c1",
		Role:      "actuator",
		ActuatorID: "act_99",
		AccountID: "acc_2",
	}
	got := roundTrip(t, orig)
	if got.Type != TypeConnect {
		t.Fatalf("type: got %q, want %q", got.Type, TypeConnect)
	}
	if got.ActuatorID != "act_99" {
		t.Fatalf("actuator_id: got %q, want %q", got.ActuatorID, "act_99")
	}
}

func TestRoundTripDisconnect(t *testing.T) {
	orig := LinkMessage{
		Type:   TypeDisconnect,
		ConnID: "c1",
		Reason: "client_closed",
	}
	got := roundTrip(t, orig)
	if got.Reason != "client_closed" {
		t.Fatalf("reason: got %q, want %q", got.Reason, "client_closed")
	}
}

func TestRoundTripMessage(t *testing.T) {
	payload := json.RawMessage(`{"type":"command_result","id":"cmd_1","status":"ok"}`)
	orig := LinkMessage{
		Type:    TypeMessage,
		ConnID:  "c5",
		Payload: payload,
	}
	got := roundTrip(t, orig)
	if string(got.Payload) != string(payload) {
		t.Fatalf("payload: got %s, want %s", got.Payload, payload)
	}
}

func TestRoundTripSend(t *testing.T) {
	payload := json.RawMessage(`{"type":"wake","text":"hello"}`)
	orig := LinkMessage{
		Type:    TypeSend,
		ConnID:  "c3",
		Payload: payload,
	}
	got := roundTrip(t, orig)
	if got.ConnID != "c3" {
		t.Fatalf("conn_id: got %q, want %q", got.ConnID, "c3")
	}
	if string(got.Payload) != string(payload) {
		t.Fatalf("payload: got %s, want %s", got.Payload, payload)
	}
}

func TestRoundTripSendAgent(t *testing.T) {
	payload := json.RawMessage(`{"type":"wake","text":"time to work"}`)
	orig := LinkMessage{
		Type:    TypeSendAgent,
		AgentID: "ag_5",
		Payload: payload,
	}
	got := roundTrip(t, orig)
	if got.AgentID != "ag_5" {
		t.Fatalf("agent_id: got %q, want %q", got.AgentID, "ag_5")
	}
}

func TestRoundTripSendActuator(t *testing.T) {
	payload := json.RawMessage(`{"type":"command_delivery","id":"cmd_7","capability":"exec"}`)
	orig := LinkMessage{
		Type:       TypeSendActuator,
		ActuatorID: "act_3",
		Payload:    payload,
	}
	got := roundTrip(t, orig)
	if got.ActuatorID != "act_3" {
		t.Fatalf("actuator_id: got %q, want %q", got.ActuatorID, "act_3")
	}
}

func TestRoundTripClose(t *testing.T) {
	orig := LinkMessage{
		Type:   TypeClose,
		ConnID: "c9",
		Code:   4001,
		Reason: "auth_failed",
	}
	got := roundTrip(t, orig)
	if got.Code != 4001 {
		t.Fatalf("code: got %d, want %d", got.Code, 4001)
	}
	if got.Reason != "auth_failed" {
		t.Fatalf("reason: got %q, want %q", got.Reason, "auth_failed")
	}
}

func TestMultipleMessagesStream(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf)

	msgs := []LinkMessage{
		{Type: TypeConnect, ConnID: "c1", Role: "brain", AgentID: "ag_1"},
		{Type: TypeMessage, ConnID: "c1", Payload: json.RawMessage(`{"type":"ping"}`)},
		{Type: TypeDisconnect, ConnID: "c1", Reason: "normal"},
	}
	for _, m := range msgs {
		if err := enc.Encode(m); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}

	dec := NewDecoder(&buf)
	for i, want := range msgs {
		got, err := dec.Decode()
		if err != nil {
			t.Fatalf("decode[%d]: %v", i, err)
		}
		if got.Type != want.Type {
			t.Fatalf("decode[%d]: type: got %q, want %q", i, got.Type, want.Type)
		}
		if got.ConnID != want.ConnID {
			t.Fatalf("decode[%d]: conn_id: got %q, want %q", i, got.ConnID, want.ConnID)
		}
	}

	// Next decode should return EOF.
	_, err := dec.Decode()
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

func TestDecodeMalformedJSON(t *testing.T) {
	r := strings.NewReader("this is not json\n")
	dec := NewDecoder(r)
	_, err := dec.Decode()
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "unmarshal") {
		t.Fatalf("expected unmarshal error, got: %v", err)
	}
}

func TestDecodeOversizedLine(t *testing.T) {
	// Create a line that exceeds the max size.
	maxSize := 256
	big := `{"type":"` + strings.Repeat("x", maxSize) + `"}` + "\n"
	r := strings.NewReader(big)
	dec := NewDecoderSize(r, maxSize)
	_, err := dec.Decode()
	if err == nil {
		t.Fatal("expected error for oversized line")
	}
	if !strings.Contains(err.Error(), "scan") {
		t.Fatalf("expected scan error, got: %v", err)
	}
}

func TestDecodeEmptyPayload(t *testing.T) {
	// A message with no payload field — Payload should be nil.
	line := `{"type":"disconnect","conn_id":"c1","reason":"gone"}` + "\n"
	dec := NewDecoder(strings.NewReader(line))
	msg, err := dec.Decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.Payload != nil {
		t.Fatalf("expected nil payload, got %s", msg.Payload)
	}
}

func TestDecodeEmptyObjectPayload(t *testing.T) {
	line := `{"type":"message","conn_id":"c1","payload":{}}` + "\n"
	dec := NewDecoder(strings.NewReader(line))
	msg, err := dec.Decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(msg.Payload) != "{}" {
		t.Fatalf("expected {}, got %s", msg.Payload)
	}
}

func TestConcurrentEncode(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf)

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			_ = enc.Encode(LinkMessage{
				Type:   TypeConnect,
				ConnID: strings.Repeat("x", i%10+1),
			})
		}(i)
	}
	wg.Wait()

	// Verify we can decode all n messages without corruption.
	dec := NewDecoder(&buf)
	count := 0
	for {
		_, err := dec.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("corrupt message at index %d: %v", count, err)
		}
		count++
	}
	if count != n {
		t.Fatalf("decoded %d messages, want %d", count, n)
	}
}

func TestEOFOnEmptyReader(t *testing.T) {
	dec := NewDecoder(strings.NewReader(""))
	_, err := dec.Decode()
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

// roundTrip encodes a message and decodes it back, returning the result.
func roundTrip(t *testing.T, msg LinkMessage) LinkMessage {
	t.Helper()
	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	if err := enc.Encode(msg); err != nil {
		t.Fatalf("encode: %v", err)
	}
	dec := NewDecoder(&buf)
	got, err := dec.Decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}
