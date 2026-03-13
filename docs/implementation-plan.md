# Capability Model v2 — Implementation Plan

*Concrete steps to implement the data model described in data-model.md.*

## What Changes

### Database

**New tables:**
- `capabilities` — binds agent-visible name → provider + secret
- `capability_grants` — which agent has which capability

**Modified tables:**
- `providers` — remove `secret_name` column (providers don't reference secrets)

**Removed tables:**
- `secret_access` — replaced by `capability_grants`

### Broker API

**New endpoints (management):**
- `POST /api/capabilities` — create capability
- `GET /api/capabilities` — list capabilities (with query filter by account)
- `PUT /api/capabilities/{id}` — update capability
- `DELETE /api/capabilities/{id}` — delete capability
- `POST /api/capabilities/{id}/grant` — grant to agent
- `DELETE /api/capabilities/{id}/grant/{agentId}` — revoke from agent

**Modified endpoints (agent-facing):**
- `POST /v1/proxy/request` — takes `capability` field (not `provider`)
- `POST /v1/capabilities` — reads from capabilities + capability_grants tables

**Removed from agent API:**
- `POST /v1/secrets/list` — agents have no business knowing secret names

**Removed from management API:**
- `POST /api/secrets/{id}/grant` — replaced by capability grants
- `DELETE /api/secrets/{id}/grant/{agentId}` — replaced by capability grants

**Kept but re-authed:**
- `POST /api/secrets/get` — stays in management API (dashboard-only)

### Agent Tools (botster-tools repo)

- `seklist` — no code change needed (already calls `/v1/capabilities`)
- `sekdo` — first argument becomes capability name, not provider name
  - Provider schema lookup: extract provider from capability response, match to compiled schema
  - Or: sekdo calls `/v1/capabilities` first to learn which provider backs the capability
- `sekgit` — capability name passed as arg, used in proxy request

## Implementation Steps

### Step 1: Migration 9 — new tables, provider column cleanup

```sql
-- New: capabilities table
CREATE TABLE IF NOT EXISTS capabilities (
    id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    display_name TEXT NOT NULL,
    provider_id TEXT NOT NULL REFERENCES providers(id),
    secret_id TEXT NOT NULL REFERENCES secrets(id),
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(account_id, name)
);

-- New: capability_grants table
CREATE TABLE IF NOT EXISTS capability_grants (
    id TEXT PRIMARY KEY,
    capability_id TEXT NOT NULL REFERENCES capabilities(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(capability_id, agent_id)
);
```

Note: SQLite doesn't support DROP COLUMN before 3.35.0. Rather than
removing `secret_name` from `providers`, we leave it as a deprecated
column and stop reading it. The `secret_access` table is similarly
left in place but unused — data migrates to `capability_grants`.

### Step 2: DB methods for capabilities

New file: `internal/db/capabilities.go`

- `CreateCapability(accountID, name, displayName, providerID, secretID) (*Capability, error)`
- `ListCapabilities(accountID) ([]Capability, error)`
- `GetCapabilityByName(accountID, name) (*Capability, error)`
- `GetCapabilityByID(id) (*Capability, error)`
- `UpdateCapability(id, displayName, providerID, secretID) error`
- `DeleteCapability(id) error`
- `GrantCapability(capabilityID, agentID) error`
- `RevokeCapability(capabilityID, agentID) error`
- `ListCapabilityGrants(capabilityID) ([]Agent, error)`
- `ListAgentCapabilities(accountID, agentID) ([]CapabilityWithProvider, error)`
  - Joins capabilities + providers to return name, display_name, provider base_url, auth info
  - This is what the proxy handler needs

### Step 3: Management API for capabilities

New file: `internal/api/capabilities.go`

CRUD + grant/revoke endpoints. All require root or admin auth.
Follow the pattern of existing handlers (accounts, secrets).

### Step 4: Rewrite `/v1/proxy/request`

Current: takes `provider` field, looks up provider, checks secret_access.
New: takes `capability` field, looks up capability → provider + secret,
checks capability_grants.

### Step 5: Rewrite `/v1/capabilities`

Current: derives from providers table + secret access.
New: queries capability_grants for this agent, returns capability names
and display names.

### Step 6: Remove deprecated agent endpoints

- Remove `POST /v1/secrets/list` from agent-facing router
- Remove `POST /api/secrets/{id}/grant` from management router
- Remove `DELETE /api/secrets/{id}/grant/{agentId}` from management router

### Step 7: Update inference proxy

The inference proxy currently resolves secrets by provider name using
`inferenceProviders` map or the providers table. It needs to also work
with the capability model for agents that have inference capabilities
granted.

For now: inference proxy continues using its own resolution path
(provider name → secret name). This is acceptable because inference
providers are a separate concept (see architecture.md discussion of
inference providers as their own data model — deferred work).

### Step 8: Update sekdo in botster-tools

sekdo's first argument becomes a capability name:
`sekdo github-botsters list-repos`

sekdo needs to know which provider schema to use for a given capability.
Two approaches:

**Option A:** sekdo calls `/v1/capabilities` to get the list with provider
info, maps capability → provider, uses the compiled schema for that
provider. Requires `/v1/capabilities` to return the provider name.

**Option B:** The capability name contains the provider as a prefix
convention (e.g., `github-*` → github provider). sekdo parses it.

Option A is cleaner — no naming convention required, works with any
capability name. The `/v1/capabilities` response should include the
provider name:

```json
{
  "capabilities": [
    {"name": "github-botsters", "display_name": "GitHub (Botsters)", "provider": "github"},
    {"name": "hetzner", "display_name": "Hetzner Cloud", "provider": "hetzner"}
  ]
}
```

Then sekdo can: look up capability "github-botsters" → provider "github" →
use GitHub schema for action resolution → call proxy/request with
capability "github-botsters".

### Step 9: Update sekgit in botster-tools

sekgit takes a capability name:
`sekgit --capability github-botsters clone https://github.com/org/repo.git`

Passes the capability to `/v1/proxy/request` for each proxied git request.

### Step 10: Update dashboard

- Capabilities tab: show capabilities per agent (name, display_name, provider, grants)
- Remove secrets grants UI (replaced by capability grants)
- Keep secrets management for creating/updating encrypted values

### Step 11: Tests

- DB tests for capability CRUD and grants
- API tests for capability endpoints
- Integration test: create provider, secret, capability, grant to agent,
  proxy request succeeds; revoke grant, proxy request denied
- Update existing tests that reference `secret_access`

## Order of Execution

1. Step 1 (migration) — schema first
2. Step 2 (DB methods) — data layer
3. Step 3 (management API) — admin can create capabilities
4. Step 4 + 5 (proxy/request + /v1/capabilities) — agents use new model
5. Step 6 (remove deprecated) — clean up
6. Step 8 + 9 (sekdo, sekgit) — tools use new model
7. Step 10 (dashboard) — admin UI
8. Step 11 (tests) — throughout, not last
