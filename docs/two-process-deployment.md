# Two-Process Deployment Guide

**Author:** FootGun 🔫  
**Date:** 2026-03-26

## Overview

The broker process split separates the broker into two processes:

1. **ws-proxy** — Long-lived WebSocket frontend. Holds all client connections (brains + actuators). Handles ping/pong keepalive autonomously. Survives broker restarts.

2. **botster-broker** — Restartable business logic backend. Auth, command routing, token rotation, API endpoints. Connects to ws-proxy over a Unix domain socket.

## Quick Start

```bash
# Build both binaries
make build-all

# Start ws-proxy (holds connections, rarely restarted)
./ws-proxy -socket /tmp/hub.sock -listen :9084

# Start broker in link mode (can be restarted freely)
PROXY_SOCKET=/tmp/hub.sock ./botster-broker
```

## Systemd Deployment

```bash
# Install binaries
sudo make install-split

# Install service files
sudo cp ws-proxy.service /etc/systemd/system/
sudo cp botster-broker-split.service /etc/systemd/system/

# Edit secrets
sudo systemctl edit botster-broker-split
# Add: Environment=BROKER_MASTER_KEY=<your-key>

# Enable and start
sudo systemctl daemon-reload
sudo systemctl enable --now ws-proxy
sudo systemctl enable --now botster-broker-split
```

## Zero-Downtime Broker Restart

```bash
# This is the whole point. Restart the broker without dropping connections:
sudo systemctl restart botster-broker-split

# Verify connections survived:
curl http://localhost:9084/health
```

The `/health` endpoint shows:
- `link_connected`: whether the broker is connected
- `connections`: active WebSocket clients
- `buffering`: whether messages are being buffered (broker is down)
- `buffer_messages`: number of buffered messages

## How It Works

```
                 Internet
                    │
            ┌───────┴────────┐
            │   ws-proxy     │  ← Long-lived, holds WS connections
            │  :9084 /ws     │
            └───────┬────────┘
                    │ Unix socket (/run/botster-broker/hub.sock)
                    │ JSON-lines protocol
            ┌───────┴────────┐
            │ botster-broker │  ← Restartable, business logic
            │  :8080 /v1/*   │
            └────────────────┘
```

### Protocol

Communication over the Unix socket uses newline-delimited JSON (JSON-lines):

**Proxy → Broker:**
- `auth_request` — "this client sent this hello, should I let them in?"
- `connect` — "new authenticated client"
- `disconnect` — "client disconnected"
- `message` — "client sent this WebSocket message"

**Broker → Proxy:**
- `auth_result` — "yes/no + connection metadata"
- `send` — "send this to connection X"
- `send_agent` — "send this to agent Y's brain"
- `send_actuator` — "send this to actuator Z"
- `close` — "close connection X with this code"

### Broker Restart Sequence

1. Broker process exits (or is killed)
2. Proxy detects link drop, enters **buffer mode**
3. WebSocket clients are unaffected (proxy handles keepalive)
4. New broker process starts, connects to Unix socket
5. Proxy **replays** all active connections (connect messages)
6. Proxy **drains** buffered messages
7. Normal operation resumes

Typical restart gap: **< 1 second**.

## Backward Compatibility

The split is **opt-in**. Without `PROXY_SOCKET` set, the broker runs in single-process mode exactly as before — handling WebSocket connections directly. No changes required for existing deployments.

## Configuration

### ws-proxy flags

| Flag | Default | Description |
|------|---------|-------------|
| `-socket` | `/run/botster-broker/hub.sock` | Unix socket path |
| `-listen` | `:9084` | WebSocket listen address |
| `-auth-timeout` | `5s` | Auth delegation timeout |
| `-buffer-size` | `1000` | Max buffered messages during restart |

### Broker environment

| Var | Description |
|-----|-------------|
| `PROXY_SOCKET` | Unix socket path. Set to enable two-process mode. |
| All existing vars | Work exactly as before. |

## Monitoring

```bash
# Proxy health
curl -s http://localhost:9084/health | jq .

# Broker health (existing endpoint)
curl -s http://localhost:8080/v1/status -H "X-Admin-Key: ..." | jq .

# Logs
journalctl -u ws-proxy -f
journalctl -u botster-broker-split -f
```
