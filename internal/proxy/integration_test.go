package proxy_test

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/TheBotsters/botster-broker-go/internal/link"
	"github.com/TheBotsters/botster-broker-go/internal/proxy"
)

// TestProxyBrokerLinkRoundTrip verifies that the proxy and a mock broker
// can communicate over a Unix socket: connect → message → send.
func TestProxyBrokerLinkRoundTrip(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "hub.sock")

	// Start Unix socket listener.
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	// Accept one connection in background.
	acceptCh := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		acceptCh <- conn
	}()

	// Dial from the other side (simulating the broker connecting).
	clientConn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer clientConn.Close()

	var serverConn net.Conn
	select {
	case serverConn = <-acceptCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for accept")
	}
	defer serverConn.Close()

	// Test link codec over the real Unix socket.
	clientEnc := link.NewEncoder(clientConn)
	serverDec := link.NewDecoder(serverConn)
	serverEnc := link.NewEncoder(serverConn)
	clientDec := link.NewDecoder(clientConn)

	// Client → Server: connect message.
	connectMsg := link.LinkMessage{
		Type:      link.TypeConnect,
		ConnID:    "c1",
		Role:      "brain",
		AgentID:   "ag_test",
		AccountID: "acc_test",
	}
	if err := clientEnc.Encode(connectMsg); err != nil {
		t.Fatalf("encode connect: %v", err)
	}

	got, err := serverDec.Decode()
	if err != nil {
		t.Fatalf("decode connect: %v", err)
	}
	if got.Type != link.TypeConnect {
		t.Fatalf("type: got %q, want %q", got.Type, link.TypeConnect)
	}
	if got.AgentID != "ag_test" {
		t.Fatalf("agent_id: got %q, want %q", got.AgentID, "ag_test")
	}

	// Server → Client: send_agent message.
	payload := json.RawMessage(`{"type":"wake","text":"hello from broker"}`)
	sendMsg := link.LinkMessage{
		Type:    link.TypeSendAgent,
		AgentID: "ag_test",
		Payload: payload,
	}
	if err := serverEnc.Encode(sendMsg); err != nil {
		t.Fatalf("encode send_agent: %v", err)
	}

	gotSend, err := clientDec.Decode()
	if err != nil {
		t.Fatalf("decode send_agent: %v", err)
	}
	if gotSend.Type != link.TypeSendAgent {
		t.Fatalf("type: got %q, want %q", gotSend.Type, link.TypeSendAgent)
	}
	if string(gotSend.Payload) != string(payload) {
		t.Fatalf("payload: got %s, want %s", gotSend.Payload, payload)
	}

	// Client → Server: auth_request + Server → Client: auth_result.
	authReq := link.LinkMessage{
		Type:      link.TypeAuthRequest,
		RequestID: "auth_c2",
		ConnID:    "c2",
		Hello:     json.RawMessage(`{"type":"brain_hello","token":"seks_agent_test123"}`),
	}
	if err := clientEnc.Encode(authReq); err != nil {
		t.Fatalf("encode auth_request: %v", err)
	}

	gotAuth, err := serverDec.Decode()
	if err != nil {
		t.Fatalf("decode auth_request: %v", err)
	}
	if gotAuth.Type != link.TypeAuthRequest {
		t.Fatalf("type: got %q, want %q", gotAuth.Type, link.TypeAuthRequest)
	}
	if gotAuth.RequestID != "auth_c2" {
		t.Fatalf("request_id: got %q, want %q", gotAuth.RequestID, "auth_c2")
	}

	authResult := link.LinkMessage{
		Type:      link.TypeAuthResult,
		RequestID: "auth_c2",
		OK:        true,
		ConnID:    "c2",
		Role:      "brain",
		AgentID:   "ag_new",
		AccountID: "acc_new",
	}
	if err := serverEnc.Encode(authResult); err != nil {
		t.Fatalf("encode auth_result: %v", err)
	}

	gotResult, err := clientDec.Decode()
	if err != nil {
		t.Fatalf("decode auth_result: %v", err)
	}
	if !gotResult.OK {
		t.Fatal("expected OK=true")
	}
	if gotResult.AgentID != "ag_new" {
		t.Fatalf("agent_id: got %q, want %q", gotResult.AgentID, "ag_new")
	}

	// Client → Server: disconnect.
	disconnectMsg := link.LinkMessage{
		Type:   link.TypeDisconnect,
		ConnID: "c1",
		Reason: "test_done",
	}
	if err := clientEnc.Encode(disconnectMsg); err != nil {
		t.Fatalf("encode disconnect: %v", err)
	}

	gotDC, err := serverDec.Decode()
	if err != nil {
		t.Fatalf("decode disconnect: %v", err)
	}
	if gotDC.Type != link.TypeDisconnect {
		t.Fatalf("type: got %q, want %q", gotDC.Type, link.TypeDisconnect)
	}
	if gotDC.Reason != "test_done" {
		t.Fatalf("reason: got %q, want %q", gotDC.Reason, "test_done")
	}
}

// TestProxyInitialState verifies fresh proxy state.
func TestProxyInitialState(t *testing.T) {
	p := proxy.New(proxy.DefaultConfig())
	if p.LinkUp() {
		t.Fatal("expected link down on fresh proxy")
	}
	if p.ConnCount() != 0 {
		t.Fatalf("expected 0 connections, got %d", p.ConnCount())
	}
}

// TestProxyHealthEndpoint verifies the health handler returns valid JSON.
func TestProxyHealthEndpoint(t *testing.T) {
	p := proxy.New(proxy.DefaultConfig())
	handler := p.HealthHandler()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != 200 {
		t.Fatalf("health status: %d", rec.Code)
	}

	var health map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatalf("health JSON: %v\nbody: %s", err, rec.Body.String())
	}

	if health["status"] != "ok" {
		t.Fatalf("status: got %v, want ok", health["status"])
	}
	if health["link_connected"] != false {
		t.Fatalf("link_connected: got %v, want false", health["link_connected"])
	}
	if health["connections"].(float64) != 0 {
		t.Fatalf("connections: got %v, want 0", health["connections"])
	}
}

// TestUnixSocketStaleCleanup verifies that stale socket files can be replaced.
func TestUnixSocketStaleCleanup(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "stale.sock")

	// Create a stale file.
	if err := os.WriteFile(socketPath, []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}

	// Remove + re-listen (same pattern as cmd/ws-proxy).
	_ = os.Remove(socketPath)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen after cleanup: %v", err)
	}
	ln.Close()
}
