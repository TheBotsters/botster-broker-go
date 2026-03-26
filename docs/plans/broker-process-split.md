# Broker Process Split: WebSocket Proxy + Business Logic

**Author:** FootGun 🔫  
**Date:** 2026-03-25  
**Status:** Plan / Study  
**Repo:** TheBotsters/botster-broker-go

---

## Problem

Every broker update requires a full process restart. This drops all WebSocket connections — brains (agents) and actuators must reconnect. During reconnection:

- Agents lose in-flight commands (result channels close)
- Wake messages may be lost
- There's a visible gap in responsiveness (~2-10 seconds per client)
- Token rotation messages can fail mid-delivery
- With 4+ sisters and their actuators, that's 8+ disconnects per deploy

## Goal

Enable broker software updates without disconnecting WebSocket clients.

## Design Principle

> "The simplest thing that could possibly work."

No service mesh. No shared memory. No complex IPC protocol. Two processes, one Unix domain socket between them, and clear separation of concerns.

---

## Architecture

```
                        ┌─────────────────────────────────┐
 Brains (agents) ──ws──▶│                                 │
                        │     ws-proxy (long-lived)       │
 Actuators ────────ws──▶│                                 │
                        │  - Accepts/maintains WebSocket  │
                        │    connections                   │
                        │  - Authenticates hello messages  │
                        │  - Routes messages bidirectionally│
                        │  - Handles ping/pong keepalive  │
                        │  - Buffers messages during       │
                        │    backend restarts (~5s window) │
                        │                                 │
                        └────────────┬────────────────────┘
                                     │
                              Unix domain socket
                           (JSON-newline protocol)
                                     │
                        ┌────────────▼────────────────────┐
                        │                                 │
                        │     broker (restartable)        │
                        │                                 │
                        │  - REST API (HTTP)              │
                        │  - Command routing & auth       │
                        │  - Capability checks            │
                        │  - Safe mode enforcement        │
                        │  - Workspace file ops           │
                        │  - Inference proxy              │
                        │  - Database (SQLite)            │
                        │  - Token rotation               │
                        │  - Sync, dashboard, tap         │
                        │  - All business logic           │
                        │                                 │
                        └─────────────────────────────────┘
```

### Two Processes

**1. `ws-proxy` (long-lived, rarely updated)**

Responsibilities:
- Accept WebSocket connections on the public port
- Read the initial hello message (actuator_hello / brain_hello)
- Validate tokens against the broker backend (one-time auth call per connection)
- Maintain the connection map (agentID → ws, actuatorID → ws)
- Forward all non-hello messages to/from the broker backend via Unix socket
- Handle ping/pong keepalive autonomously (no backend involvement)
- Buffer outbound messages if the backend is temporarily unavailable (restart window)
- Replay buffered messages when the backend reconnects

**2. `broker` (restartable, frequently updated)**

Responsibilities:
- Everything the current broker does EXCEPT raw WebSocket management
- REST API (same HTTP port as today, or a different one — see Options below)
- Command routing, capability checks, safe mode, workspace ops
- Inference proxy, sync, dashboard, secrets
- Database ownership (SQLite)
- Connects to ws-proxy via Unix domain socket on startup

### The Link: Unix Domain Socket

Protocol: **newline-delimited JSON** (JSON-lines). Each message is a single JSON object followed by `\n`.

Message types flowing proxy → broker:
```json
{"type": "connect", "conn_id": "c1", "role": "brain", "agent_id": "...", "account_id": "..."}
{"type": "disconnect", "conn_id": "c1"}
{"type": "message", "conn_id": "c1", "payload": { ... original WSMessage ... }}
```

Message types flowing broker → proxy:
```json
{"type": "send", "conn_id": "c1", "payload": { ... WSMessage to send ... }}
{"type": "send_agent", "agent_id": "...", "payload": { ... }}
{"type": "send_actuator", "actuator_id": "...", "payload": { ... }}
{"type": "close", "conn_id": "c1", "code": 4001, "reason": "..."}
{"type": "auth_result", "request_id": "...", "ok": true, "conn_id": "c1", "role": "brain", "agent_id": "...", "account_id": "..."}
```

Why Unix socket + JSON-lines:
- No external dependencies (no Redis, no NATS, no shared memory)
- Debuggable: `socat` or `nc` can tap the stream
- Go's `net.Dial("unix", ...)` is trivial
- JSON-lines is self-framing and trivially parseable
- Performance: Unix sockets are ~10μs per message, orders of magnitude faster than we need

### Auth Flow (Hello Handling)

When a WebSocket client sends a hello:

1. ws-proxy receives the hello message
2. ws-proxy sends an `auth_request` to the broker over the Unix socket:
   ```json
   {"type": "auth_request", "request_id": "r1", "hello": { ... raw hello message ... }}
   ```
3. Broker validates the token against the DB, returns:
   ```json
   {"type": "auth_result", "request_id": "r1", "ok": true, "conn_id": "c1", "role": "brain", "agent_id": "ag_123", "account_id": "acc_1", "recovery_only": false}
   ```
4. ws-proxy registers the connection in its map
5. ws-proxy sends a `connect` notification to the broker

If auth fails, broker returns `"ok": false` and ws-proxy closes the WebSocket.

Why not have the proxy do auth directly? Because token validation requires the database, and the database belongs to the broker. Keeping auth in the broker means the proxy has zero knowledge of DB schema, tokens, or crypto. It's just a dumb pipe with a connection map.

### Restart Buffering

When the broker process restarts:

1. ws-proxy detects the Unix socket disconnect
2. ws-proxy enters **buffering mode**: incoming WS messages are queued in memory (bounded: 1000 messages or 10MB, whichever first)
3. ws-proxy continues responding to pings (keepalive continues)
4. New broker process starts, connects to the Unix socket
5. ws-proxy replays its connection map: sends a `connect` message for every active connection
6. ws-proxy drains the message buffer to the new broker
7. Normal operation resumes

From the clients' perspective: nothing happened. Their WebSocket stayed open. Commands submitted during the restart window (~2-5 seconds) may experience slightly higher latency but won't fail.

**Buffer overflow policy:** If the buffer fills before the broker reconnects, the proxy starts dropping oldest messages and logs a warning. This should only happen if the broker is down for an extended period (minutes), which indicates a deployment problem, not a normal restart.

---

## What Moves Where

### ws-proxy gets (extracted from current hub.go):

| Current Code | What Moves |
|---|---|
| `HandleWebSocket()` | WebSocket accept + upgrade |
| `readPump()` / `writePump()` | Per-connection goroutines |
| Ping/pong handling | Keepalive (autonomous) |
| Connection map (`brains`, `actuators`) | Connection tracking |
| `registerCh` / `unregisterCh` | Connection lifecycle |

**Lines of code:** ~150-200 new Go code for the proxy (it's simpler than the current hub because it doesn't do routing)

### broker keeps (everything else):

| Current Code | Stays |
|---|---|
| `handleCommand()` | Command routing + capability checks |
| `tryHandleWorkspaceOp()` | Broker-local file ops |
| `SendCommand()` | Sync command dispatch |
| Token rotation logic | Auth + crypto |
| Wake buffer logic | Wake delivery |
| All of `internal/api/` | REST API |
| All of `internal/db/` | Database |
| All of `internal/sync/` | Peer sync |
| All of `internal/tap/` | Dashboard SSE |

The broker's Hub struct changes from managing WebSocket connections directly to communicating via the Unix socket. The Hub becomes a "virtual hub" — same interface, different transport.

---

## Options to Decide

### A. Port Allocation

**Option A1: Single port (proxy fronts everything)**
- ws-proxy listens on port 9084 (current broker port)
- ws-proxy reverse-proxies HTTP requests to the broker on a second internal port or Unix socket
- Pro: No port changes for clients
- Con: Proxy becomes an HTTP reverse proxy too (more code, more failure modes)

**Option A2: Two ports**
- ws-proxy listens on port 9084 (WebSocket only, `/ws` path)
- Broker listens on port 9085 (REST API only)
- Pro: Clean separation, proxy stays simple
- Con: REST API clients need port update (but these are all internal — just update manifests)

**Recommendation: A2 (two ports).** Simplest thing. The proxy should do ONE thing: WebSocket connections. Adding HTTP reverse proxying violates the principle. All REST API consumers are internal (deploy scripts, golem runner, dashboard) and can be updated to use the new port.

### B. Deployment Model

**Option B1: Two separate binaries**
- `cmd/ws-proxy/main.go` and `cmd/broker/main.go`
- Two systemd services
- Pro: Independent restarts, clear process isolation
- Con: Two things to manage

**Option B2: Single binary, subcommand**
- `botster-broker serve` (business logic) and `botster-broker ws-proxy` (WebSocket proxy)
- Pro: One binary to build and distribute
- Con: Still two systemd services; shared binary means proxy binary updates too (but it doesn't need to restart)

**Recommendation: B1 (two binaries).** The whole point is that the proxy rarely changes. If it's the same binary, every code change rebuilds both, and operators might accidentally restart the proxy when they only meant to update the broker. Separate binaries make the operational boundary crystal clear.

### C. Unix Socket Location

`/run/botster-broker/hub.sock` — created by the proxy, connected to by the broker. Standard location, cleaned up by systemd on stop.

---

## Estimate

### Implementation Work

| Task | Effort | Notes |
|---|---|---|
| 1. `cmd/ws-proxy/main.go` — skeleton + Unix socket listener | 2h | Accept broker connections, manage lifecycle |
| 2. ws-proxy WebSocket handling (extract from hub.go) | 3h | Hello interception, connection map, read/write pumps |
| 3. ws-proxy ↔ broker JSON-lines protocol (types + encode/decode) | 2h | Shared package `internal/link/` |
| 4. ws-proxy buffering logic (restart tolerance) | 2h | Bounded queue, replay on reconnect |
| 5. Broker-side link client (replaces direct WS in Hub) | 3h | Hub sends/receives via Unix socket instead of WS directly |
| 6. Auth delegation (proxy → broker → proxy) | 2h | Request/response over the link |
| 7. Broker startup: connect to proxy, receive connection replay | 1h | Replay existing connections from proxy's map |
| 8. Systemd service files (two units, ordering) | 1h | `ws-proxy.service`, `broker.service` with `After=` |
| 9. Update deploy-env.sh for two-process model | 1h | Manifest changes, deploy both |
| 10. Integration testing (golem scenarios) | 3h | Verify restart-without-disconnect |
| 11. Migration path (backward compat / cutover) | 2h | Old single-process still works during transition |

**Total estimate: ~22 hours of implementation work**

Realistically with testing, debugging, and edge cases: **3-4 working days** for an experienced Go developer (Síofra).

### What Does NOT Change

- WebSocket wire protocol (brains/actuators see no difference)
- REST API endpoints (just a port number change)
- Database schema
- Auth model
- All business logic

---

## Risk Analysis

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| **Message loss during broker restart** | Medium | High | Bounded buffer in proxy with replay. Test extensively with golem. |
| **Unix socket connection failure** | Low | High | Proxy logs clearly. Broker retries connect with backoff. Health endpoint reports link status. |
| **Buffer overflow during extended outage** | Low | Medium | Configurable limits. Log warnings. Oldest-first drop policy. Operational alert if buffer >50% full. |
| **Auth request timeout (broker slow to respond)** | Low | Medium | 5-second timeout on auth requests. Proxy closes WS if auth times out. |
| **Proxy itself crashes** | Low | Critical | Same as today (all connections drop). But proxy is ~200 lines of simple code — much less likely to crash than the full broker. |
| **Race conditions during broker restart** | Medium | Medium | Proxy sequences replay carefully: first sends all `connect` messages, waits for ack, then drains buffer. |
| **State divergence (proxy thinks connection alive, broker disagrees)** | Low | Medium | Periodic heartbeat from broker to proxy: "give me your connection list." Broker reconciles. |
| **Complexity increase** | — | Ongoing | Two processes is inherently more complex than one. Mitigated by keeping the proxy dead simple and the protocol trivial. |
| **Operational confusion** | Low | Low | Clear naming, clear docs, deploy scripts handle both. `ws-proxy` service should be started once and basically never touched. |

### Risk Summary

The biggest real risk is **message ordering and loss during the restart window**. This needs thorough integration testing. The golem test framework is perfect for this — add a scenario that:
1. Starts a command
2. Kills the broker mid-flight
3. Restarts the broker
4. Verifies the command completes (or fails gracefully)

---

## What We Gain

1. **Zero-downtime broker updates** — the core goal
2. **Faster deployment confidence** — deploy broker changes without worrying about sister disconnects
3. **Operational clarity** — WebSocket health separate from broker health
4. **Future optionality** — the proxy could eventually support horizontal broker scaling (multiple brokers behind one proxy), though that's NOT in scope here

## What We Don't Gain (Non-Goals)

- This does NOT solve proxy updates (those still drop connections — but they should be rare)
- This does NOT add horizontal scaling
- This does NOT change the WebSocket protocol
- This does NOT require client changes (agents/actuators reconnect logic stays as-is, just used less often)

---

## Migration Path

1. **Phase 1:** Build and test ws-proxy alongside existing single-process broker. Both run on different ports. Golem tests validate the two-process model.
2. **Phase 2:** Cut over prod environments to two-process model. Update manifests and deploy scripts. Old single-process broker binary remains available as rollback.
3. **Phase 3:** Remove single-process WebSocket code from broker (cleanup). Proxy becomes the only WebSocket entry point.

Rollback at any phase: just restart the old single-process broker on the original port.

---

## Open Questions

1. **Should the proxy handle TLS termination?** Currently the broker doesn't do TLS (Tailscale handles encryption). If that changes, the proxy would be the natural place for TLS. Not relevant now.

2. **Should REST API go through the proxy too?** No. See Option A2 above. Keep it simple.

3. **Should the proxy have its own health endpoint?** Yes — a minimal `/health` on the WebSocket port that reports connection count and buffer status. Useful for monitoring.

4. **Who owns the Unix socket file?** The proxy creates it. The broker connects to it. If the proxy isn't running, the broker logs an error and retries.

---

*"The simplest thing that could possibly work" — and not one thing simpler.*
