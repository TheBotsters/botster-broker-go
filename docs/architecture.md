# Botster Spine — Architecture

*The Spine is the security boundary between AI agents and the outside world.*

## Overview

The Botster Spine (formerly "SEKS Broker") is a Go service that sits between AI agents and external services. It holds credentials, enforces capabilities, proxies requests, and logs everything. Agents never touch secrets directly.

```
┌─────────────────────────────────────────────────────────┐
│                      Human Owner                         │
│                    (Dashboard UI)                         │
└────────────┬────────────────────────────────┬────────────┘
             │ manage providers,              │ view audit
             │ secrets, grants                │ log
             ▼                                ▼
┌─────────────────────────────────────────────────────────┐
│                    Botster Spine                         │
│                                                          │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌─────────┐ │
│  │ Provider │  │ Secret   │  │Capability│  │  Audit  │ │
│  │ Registry │  │  Vault   │  │  Grants  │  │   Log   │ │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘  └────┬────┘ │
│       │              │             │              │      │
│  ┌────┴──────────────┴─────────────┴──────────────┘     │
│  │              Proxy Engine                             │
│  │   (credential injection, URL validation, logging)     │
│  └──────────────────┬───────────────────────────────┘   │
│                     │                                    │
│  ┌──────────────────┴───────────────────────────────┐   │
│  │              Actuator Hub (WebSocket)              │   │
│  └──────────────────────────────────────────────────┘   │
└──────┬───────────────┬──────────────────┬───────────────┘
       │               │                  │
       ▼               ▼                  ▼
  ┌─────────┐   ┌───────────┐     ┌───────────────┐
  │  Agent  │   │ Actuator  │     │  External API  │
  │ (Brain) │   │ (VPS/Mac) │     │ (GitHub, etc.) │
  └─────────┘   └───────────┘     └───────────────┘
```

## Core Principle: Agents Never See Secrets

This is the non-negotiable design constraint. Everything else follows from it.

An agent holds a **token** that identifies it to the Spine. When the agent needs to call an external API (GitHub, Hetzner, Cloudflare, etc.), it tells the Spine *what it wants to do* — the Spine resolves *how to authenticate*, injects the credentials into the outgoing request, and returns only the response.

The agent never holds, sees, or transmits an API key, PAT, or password. The Spine is the only process that touches credentials.

**Why this matters:**

- **Compromised agent can't exfiltrate keys.** An attacker who gains control of an agent's session gets capabilities (which can be revoked instantly), not credentials (which require rotation across every service).
- **Key rotation is invisible.** Change a GitHub PAT in the Spine — every agent using GitHub continues working. No config push, no restarts.
- **Revocation is surgical.** Revoke one agent's access to GitHub without affecting its access to Hetzner, and without touching any other agent.
- **Audit is meaningful.** Every external API call flows through the Spine and is logged with agent identity, provider, URL, method, and timestamp.

### The Analogy: Prepared Statements

The insight that led to this design came from database security. SQL injection exists because user input is mixed with code. Prepared statements fix it by keeping data out of the command channel entirely.

Secret exfiltration in AI agents is the same class of problem. If an agent holds an API key in its environment or memory, a prompt injection or tool misuse can extract it — through string transforms, file writes, encoding tricks, or side channels. The fix is the same: **keep secrets out of the agent's address space entirely**. The Spine is the prepared statement. The agent provides parameters (provider, URL, method). The Spine injects the credential at execution time, server-side, where the agent can't touch it.

## Entity Model

### Accounts

An account is a billing and ownership boundary. Typically one per customer (person or organization). All resources — agents, actuators, secrets, providers — belong to an account. Multi-tenant isolation is enforced at every API layer.

### Agents

An agent is an AI brain — an OpenClaw instance (or similar runtime) that thinks and makes decisions. Each agent has:

- A **name** (human-readable, e.g., "nira")
- A **token** (bearer credential for authenticating to the Spine)
- A **safe mode** flag (when enabled, the Spine refuses all proxy and command requests for this agent)

Agents authenticate to the Spine with their token via `Authorization: Bearer <token>`.

### Actuators

An actuator is a machine that executes commands on behalf of an agent. An agent can have multiple actuators — for example, one on a cloud VPS and another on a local MacBook. Actuators connect to the Spine via persistent WebSocket and receive commands routed from their agent.

Key property: **actuators are interchangeable.** The agent says "run this command"; the Spine routes it to whichever actuator is currently selected and connected. Switching machines is a one-API-call operation — the agent doesn't need to know or care which physical machine is doing the work.

### Providers

A provider represents an external service that agents interact with through the Spine. Each provider is configured with:

| Field | Purpose | Example |
|-------|---------|---------|
| `name` | Identifier used in API calls | `github` |
| `display_name` | Human-readable label | `GitHub` |
| `base_url` | URL prefix for request validation | `https://api.github.com` |
| `auth_type` | How credentials are injected | `bearer`, `basic`, or `header` |
| `auth_header` | Header name for credential injection | `Authorization` (default) |
| `secret_name` | Which secret to resolve for this provider | `GITHUB_PAT` |

Providers are **configurable via the management API** — not hardcoded. Adding support for a new external service means creating a provider record and storing the corresponding secret. No code changes required.

### Secrets

A secret is an encrypted credential stored in the Spine's SQLite database. Secrets are:

- **Encrypted at rest** with AES-256-GCM using the broker's master key
- **Scoped to an account** (multi-tenant isolation)
- **Linked to a provider** via the `provider` field and the provider's `secret_name`
- **Grantable per-agent** via the `secret_access` table

The management API can retrieve decrypted secret values (for the dashboard UI, authenticated with session credentials). The agent-facing API **cannot** — there is no endpoint in `/v1/` that returns a secret value to an agent.

### Capabilities

A capability is what an agent *can do*. Capabilities are derived from which provider secrets the agent has been granted access to:

```
Agent "nira" has been granted access to secret "GITHUB_PAT"
  → Secret "GITHUB_PAT" is linked to provider "github"
    → Agent "nira" has capability "github"
      → Agent can call POST /v1/proxy/request with provider "github"
```

The `POST /v1/capabilities` endpoint returns this derived list. The agent asks "what can I do?" and gets back provider names — never secret values, never secret names.

Grant and revocation happen at the secret-access level:
- **Grant:** `POST /api/secrets/{id}/grant` with `{"agent_id": "..."}` — agent gains capability
- **Revoke:** `DELETE /api/secrets/{id}/grant/{agentId}` — agent loses capability immediately

## Request Flows

### Proxy Request (the core flow)

When an agent needs to call an external API:

```
1. Agent → POST /v1/proxy/request
   {"provider": "github", "method": "GET", "url": "https://api.github.com/user/repos"}

2. Spine authenticates agent token
3. Spine looks up provider "github" in providers table
   → base_url: https://api.github.com
   → auth_type: bearer
   → secret_name: GITHUB_PAT

4. Spine validates request URL starts with provider's base_url
   (prevents agent from using a GitHub capability to hit an arbitrary URL)

5. Spine checks agent has access to secret "GITHUB_PAT" via secret_access table

6. Spine decrypts secret value in-memory (never written to disk unencrypted)

7. Spine builds outgoing HTTP request, injects credential:
   Authorization: Bearer ghp_xxxxxxxxxxxx

8. Spine makes HTTP request to https://api.github.com/user/repos

9. Spine logs to audit_log:
   agent=nira, action=proxy.request, detail="github: GET /user/repos → 200 OK"

10. Spine returns response to agent (headers + body, no credentials)
```

The credential existed only in the Spine's memory for the duration of the request.

### Actuator Command

```
1. Agent → POST /v1/command
   {"capability": "exec", "payload": {"command": ["ls", "-la"]}}

2. Spine authenticates agent, checks safe mode is off
3. Spine routes command to agent's selected actuator via WebSocket
4. Actuator executes command on its machine, returns result via WebSocket
5. Spine logs to audit_log, returns result to agent
```

### Inference Proxy

For AI model inference (Anthropic, OpenAI, xAI), the Spine provides specialized streaming proxy endpoints:

```
POST /v1/proxy/anthropic/v1/messages   → proxied to api.anthropic.com
POST /v1/proxy/openai/v1/chat/completions → proxied to api.openai.com
POST /v1/inference                      → generic, auto-detects provider from model name
```

These handle SSE streaming, OAuth token refresh, round-robin key rotation, and provider-specific quirks (Anthropic API key vs OAuth, OpenAI Codex bundles). They resolve secrets via the providers table first, falling back to built-in defaults.

The **inference tap** (`GET /api/inference/stream`) provides a real-time SSE stream of all inference traffic — the dashboard uses this to show live AI calls as they happen.

## API Reference

### Agent-Facing API (`/v1/...`)

Authenticated with agent bearer token. These are the endpoints agents and agent tools call.

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/v1/capabilities` | List capabilities (providers agent can access) |
| `POST` | `/v1/proxy/request` | Proxy HTTP request with credential injection |
| `POST` | `/v1/secrets/list` | List secret names visible to agent (no values) |
| `POST` | `/v1/tokens/scoped` | Mint short-lived capability-scoped token |
| `GET` | `/v1/actuators` | List actuators for agent's account |
| `POST` | `/v1/actuator/select` | Select which actuator receives commands |
| `GET` | `/v1/actuator/selected` | Check which actuator is selected |
| `POST` | `/v1/command` | Send command to selected actuator |
| `POST` | `/v1/inference` | Inference proxy (auto-detect provider) |
| `POST` | `/v1/proxy/anthropic/*` | Inference proxy (Anthropic-specific) |
| `POST` | `/v1/proxy/openai/*` | Inference proxy (OpenAI-specific) |
| `POST` | `/v1/web/search` | Web search proxy |

**Deliberately absent from agent API:** `secrets/get`. Agents cannot retrieve secret values.

### Management API (`/api/...`)

Authenticated with master key (`X-API-Key` header) or admin agent token. Used by operators and the dashboard backend.

| Method | Path | Purpose |
|--------|------|---------|
| `POST` | `/api/providers` | Create provider |
| `GET` | `/api/providers` | List providers |
| `PUT` | `/api/providers/{id}` | Update provider |
| `DELETE` | `/api/providers/{id}` | Delete provider |
| `POST` | `/api/accounts` | Create account |
| `GET` | `/api/accounts` | List accounts |
| `GET` | `/api/accounts/{id}` | Get account |
| `PATCH` | `/api/accounts/{id}` | Update account |
| `DELETE` | `/api/accounts/{id}` | Delete account |
| `POST` | `/api/accounts/{id}/agents` | Create agent under account |
| `GET` | `/api/accounts/{id}/agents` | List agents for account |
| `PATCH` | `/api/accounts/{id}/agents/{agentId}` | Update agent |
| `DELETE` | `/api/accounts/{id}/agents/{agentId}` | Delete agent |
| `POST` | `/api/accounts/{id}/agents/{agentId}/rotate-token` | Rotate agent token |
| `POST` | `/api/agents/{agentId}/actuators` | Create actuator for agent |
| `DELETE` | `/api/actuators/{id}` | Delete actuator |
| `POST` | `/api/secrets` | Create secret |
| `PUT` | `/api/secrets/{id}` | Update secret value |
| `POST` | `/api/secrets/{id}/grant` | Grant agent access to secret |
| `DELETE` | `/api/secrets/{id}/grant/{agentId}` | Revoke agent access |
| `POST` | `/api/secrets/get` | Retrieve decrypted secret value (dashboard only) |
| `GET` | `/api/audit` | Query audit log |
| `GET` | `/api/inference/stream` | Inference tap SSE stream |

### Dashboard API (`/dashboard/...`)

Authenticated with session cookie (login via `/auth/login`).

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/dashboard/api/data` | Agents, actuators, safe mode status |
| `POST` | `/dashboard/api/safe` | Toggle global safe mode |
| `POST` | `/dashboard/api/agents/{id}/safe` | Toggle per-agent safe mode |

## Safe Mode

Safe mode is the emergency stop. When activated:

- **Global safe mode:** All proxy requests denied, all commands refused, all agents halted.
- **Per-agent safe mode:** That specific agent's requests are refused; all other agents continue normally.

Togglable from the dashboard UI. It's the "big red button" — if an agent is misbehaving, a human can stop it instantly without taking down the Spine or affecting other agents.

## Security Properties

| Property | Mechanism |
|----------|-----------|
| Secrets encrypted at rest | AES-256-GCM with broker master key |
| Agents can't see secrets | No agent-facing endpoint returns secret values |
| URL validation | Proxy rejects URLs not matching provider's `base_url` prefix |
| Per-agent access control | `secret_access` table: explicit grants per secret per agent |
| Comprehensive audit trail | Every proxy request and command logged with full context |
| Token rotation | Grace period + single-use recovery path for zero-downtime rotation |
| Emergency stop | Global and per-agent safe mode, instant effect |
| Multi-tenant isolation | All resources scoped to accounts; cross-account access impossible |
| Scoped tokens | Short-lived tokens with restricted capability set |

## Companion: Agent Tools

The Spine has companion CLI tools (in [TheBotsters/botster-tools](https://github.com/TheBotsters/botster-tools)) that agents use on their actuators:

- **`seklist`** — "What can I do?" Lists capabilities via `/v1/capabilities`.
- **`sekdo`** — "Do this thing." Capability-first CLI with provider schemas. Translates high-level commands (e.g., `sekdo github list-repos`) into `/v1/proxy/request` calls.
- **`sekgit`** — Git operations (clone, push, pull) proxied through the Spine.

These run on the actuator. The agent invokes them via the command channel. The tool calls the Spine. The Spine proxies the request. The tool returns the result. **At no point does the agent or the actuator hold a credential.**

## Deployment

Single Go binary + SQLite database. No external dependencies.

```bash
go build -o botster-broker ./cmd/broker

BROKER_MASTER_KEY=$(openssl rand -hex 32) \
BROKER_PORT=9080 \
BROKER_DB_PATH=./broker.db \
./botster-broker
```

See [DEPLOY.md](../DEPLOY.md) for systemd service setup, Caddy TLS, and production configuration.
