# Botster Broker Sync Design

**Version:** 1.0  
**Date:** 2026-03-03  
**Author:** Síofra (subagent, broker-sync-design session)  
**Codebase:** `/home/siofra_actuator/dev/botster-broker-go/`

---

## 1. Architecture Overview

### Problem Statement

Two independent Botster Go broker instances need to share state selectively and securely:

- **Prod → Staging:** Copy secrets/config from prod so staging can run realistic tests
- **Prod ↔ Prod:** Keep two prod brokers' secret stores in sync for HA or geographic redundancy
- **Future:** Generic enough to sync agents, actuators, and other resource types

**Core constraints:**
- Sync is **pull-based and explicit** — the receiving broker initiates and controls what it takes
- Secrets **remain encrypted** — they're never decrypted on the wire
- No broker serves double duty (a prod broker is never also staging)
- Authentication is **mutual** and uses a dedicated sync credential, not a regular agent token

### Topology

```
┌─────────────────────────────────────────────────────────┐
│                   Sync Architecture                      │
│                                                         │
│  ┌─────────────┐                  ┌─────────────┐       │
│  │  Broker A   │  <── pull ───    │  Broker B   │       │
│  │  (source)   │                  │  (receiver) │       │
│  │             │                  │             │       │
│  │  /sync/...  │◄─── HTTPS TLS ──►│  sync CLI / │       │
│  │  endpoints  │                  │  API caller │       │
│  └─────────────┘                  └─────────────┘       │
│                                                         │
│  Either broker can be source or receiver.               │
│  The receiver always drives the pull.                   │
└─────────────────────────────────────────────────────────┘
```

### Design Principles

1. **Pull-only model.** The receiver calls the source. The source never pushes. The source exposes endpoints; it never needs to know about the receiver's address.

2. **Encrypted-at-rest passthrough.** Secrets stored in the DB are AES-256-GCM encrypted with the source broker's `MASTER_KEY`. During sync, the source re-encrypts each secret's plaintext with a **transit key** shared between the two brokers out-of-band. The receiver decrypts with the transit key, then re-encrypts with its own `MASTER_KEY`. The plaintext touches RAM only during this handoff — it is never logged or written to disk unencrypted.

3. **Scope-limited sync tokens.** A dedicated `seks_sync_*` token class (analogous to existing `seks_scoped_*` tokens) is introduced. These are long-lived pre-shared keys stored in peer config, used only for sync operations, and distinct from all agent tokens.

4. **Idempotent upserts.** All write operations use `INSERT OR REPLACE` / `ON CONFLICT` semantics. Running the same sync twice is safe.

5. **Account mapping.** A source account ID is not guaranteed to match a destination account ID. The sync payload always carries the source's `account_id`. The receiving broker uses a configurable **peer map** to translate source account IDs to local ones.

6. **Resource namespacing.** All sync endpoints live under `/sync/v1/` to isolate them from the existing `/v1/` and `/api/` route trees.

---

## 2. API Endpoints

All endpoints (except `/sync/v1/import`) live on the **source broker**. The receiver calls them via HTTPS.

### Authentication Header

All sync calls use:
```
Authorization: Bearer seks_sync_<peer-id>_<32-hex-chars>
```

The source broker looks up the peer by token hash and validates permissions.

---

### GET /sync/v1/health
**Auth:** sync token (any peer)

Returns source broker version, schema version, and supported resource types. Used by the receiver to confirm the source is reachable and compatible before pulling.

**Response:**
```json
{
  "ok": true,
  "broker_version": "go-1.4.0",
  "schema_version": 4,
  "supported_resources": ["secrets", "agents", "actuators"]
}
```

---

### GET /sync/v1/manifest
**Auth:** sync token

Returns a manifest of all syncable resources (metadata only, no values). The receiver uses this to decide what to pull and to skip items already in sync.

**Query params:**

| Param | Type | Description |
|---|---|---|
| `resource` | string | Filter: `secrets`, `agents`, `actuators` (default: `secrets`) |
| `account_id` | string | Source account to export from |
| `updated_since` | RFC3339 | Only return records updated after this time |

**Response:**
```json
{
  "resource": "secrets",
  "source_account_id": "abc-123",
  "generated_at": "2026-03-03T12:00:00Z",
  "items": [
    {
      "id": "secret-uuid",
      "name": "ANTHROPIC_API_KEY",
      "provider": "anthropic",
      "updated_at": "2026-03-01T09:00:00Z",
      "checksum": "sha256:<hex of name|provider|updated_at>"
    }
  ]
}
```

The `checksum` lets the receiver skip items already in sync without fetching the full payload.

---

### POST /sync/v1/export
**Auth:** sync token

Fetches actual secret values, re-encrypted with the transit key for this peer. This is the only endpoint that touches plaintext — and only in RAM, never logged.

**Request:**
```json
{
  "resource": "secrets",
  "source_account_id": "abc-123",
  "item_ids": ["secret-uuid-1", "secret-uuid-2"],
  "transit_key_id": "tk_abc123"
}
```

- `item_ids`: List of specific IDs to export (from manifest). Max 100 per call.
- `transit_key_id`: Identifies which transit key the source should encrypt with. Must match a key configured for this peer on the source.

**Response:**
```json
{
  "resource": "secrets",
  "source_account_id": "abc-123",
  "source_broker_id": "prod-east",
  "transit_key_id": "tk_abc123",
  "exported_at": "2026-03-03T12:00:05Z",
  "schema_version": 4,
  "items": [
    {
      "id": "secret-uuid-1",
      "name": "ANTHROPIC_API_KEY",
      "provider": "anthropic",
      "transit_encrypted_value": "<hex: AES-256-GCM with transit key>",
      "metadata": null,
      "updated_at": "2026-03-01T09:00:00Z"
    }
  ]
}
```

`transit_encrypted_value` = `encrypt(decrypt(db_value, SOURCE_MASTER_KEY), TRANSIT_KEY)` — in RAM only.

---

### POST /sync/v1/import  *(receiver-side)*
**Auth:** root (`X-Admin-Key`) or operator role

This endpoint lives on the **receiving broker** and triggers the pull sequence. The receiver calls this; the receiver in turn calls the source's `/manifest` and `/export` internally.

**Request:**
```json
{
  "peer_id": "prod-east",
  "resource": "secrets",
  "source_account_id": "abc-123",
  "target_account_id": "xyz-789",
  "item_ids": ["secret-uuid-1"],
  "dry_run": false
}
```

- `peer_id`: Identifies the source broker config entry on the receiver
- `dry_run`: If true, shows what would be imported without writing anything

**Response:**
```json
{
  "ok": true,
  "dry_run": false,
  "imported": 2,
  "skipped": 0,
  "errors": [],
  "items": [
    { "id": "secret-uuid-1", "name": "ANTHROPIC_API_KEY", "action": "created" },
    { "id": "secret-uuid-2", "name": "OPENAI_API_KEY",    "action": "updated" }
  ]
}
```

---

### GET /sync/v1/peers  *(source-side, root-only)*
**Auth:** root (`X-Admin-Key`)

Lists configured sync peers and their access permissions. Used for auditing who has sync access to this broker.

**Response:**
```json
{
  "peers": [
    {
      "peer_id": "staging",
      "label": "Staging Broker",
      "allowed_resources": ["secrets"],
      "allowed_accounts": ["abc-123"],
      "last_synced_at": "2026-03-03T10:00:00Z",
      "created_at": "2026-01-15T00:00:00Z"
    }
  ]
}
```

---

### Admin Endpoints for Peer Management (root-only)

```
POST   /api/sync/peers              — create peer, returns plaintext token (once only)
DELETE /api/sync/peers/{id}         — delete peer (immediately invalidates their token)
POST   /api/sync/peers/{id}/rotate  — rotate token, returns new plaintext token
```

---

## 3. Authentication and Authorization

### 3.1 Sync Peer Tokens

Sync peers use a new token class: `seks_sync_<peer-id>_<32-hex-chars>`.

Generated with the existing `auth.GenerateToken("seks_sync_" + peerID)` pattern. Stored as SHA-256 hashes in the `sync_peers` table. Plaintext is returned once at creation time, like agent tokens.

Unlike scoped tokens (which are HMAC-signed and stateless), sync tokens are stateful: they live in the DB and can be revoked instantly by deleting the peer or rotating the token.

**Token lifecycle:**
1. Admin calls `POST /api/sync/peers` on the source broker
2. Source generates token, stores hash + peer config
3. Admin copies plaintext token to receiver's environment config
4. Receiver stores: peer ID, source URL, sync token, transit key (all in env/secrets, not DB)
5. Rotation: `POST /api/sync/peers/{id}/rotate` on source, update receiver env, redeploy

### 3.2 New DB Table: `sync_peers`

Add as migration 5:

```sql
CREATE TABLE IF NOT EXISTS sync_peers (
    id TEXT PRIMARY KEY,                          -- peer_id, e.g. "staging"
    label TEXT NOT NULL,                          -- human name
    token_hash TEXT NOT NULL UNIQUE,              -- SHA-256 of plaintext token
    transit_key_hex TEXT NOT NULL,               -- AES-256 transit key (64-char hex)
    transit_key_id TEXT NOT NULL,                -- label for key rotation
    allowed_resources TEXT NOT NULL DEFAULT 'secrets',  -- comma-separated
    allowed_accounts TEXT,                        -- NULL = all; else comma-separated IDs
    last_synced_at TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
```

Note: `transit_key_hex` is stored on the **source** broker (it needs the key to encrypt for export). The receiver holds the same key in environment config. Both sides must keep this key secure.

### 3.3 Authorization Model on Source Broker

When a sync request arrives:

1. Extract token from `Authorization: Bearer seks_sync_*`
2. Hash it (`auth.HashToken`) and look up in `sync_peers`
3. Verify `allowed_resources` contains requested resource
4. Verify `allowed_accounts` permits requested `source_account_id` (NULL = all allowed)
5. For export: use `transit_key_hex` from peer config for re-encryption

The source never needs to know the receiver's address or identity beyond what's in `sync_peers`.

### 3.4 Transit Key Crypto Flow

```
Source DB:   AES-256-GCM(plaintext, SOURCE_MASTER_KEY)  [hex-encoded]
     ↓  decrypt(db_value, SOURCE_MASTER_KEY)
RAM:         plaintext bytes
     ↓  encrypt(plaintext, TRANSIT_KEY)
Wire:        AES-256-GCM(plaintext, TRANSIT_KEY)  [hex-encoded in JSON]
     ↓  decrypt(transit_value, TRANSIT_KEY)
RAM:         plaintext bytes
     ↓  encrypt(plaintext, DEST_MASTER_KEY)
Dest DB:     AES-256-GCM(plaintext, DEST_MASTER_KEY)  [hex-encoded]
```

Transit keys are 32-byte random keys (64-char hex), matching the existing `MASTER_KEY` format and reusing the existing `encrypt`/`decrypt` functions in `internal/db/secrets.go`. No new crypto primitives needed.

### 3.5 mTLS (Optional, Recommended for Prod↔Prod)

For prod-to-prod sync, add TLS client certificate pinning. The receiver presents a client cert when calling the source. Caddy can enforce this with `tls { client_auth { ... } }`. This provides an additional layer: even if a sync token leaks, it's useless without the cert.

---

## 4. Data Format for Sync Payloads

### 4.1 Manifest Item (all resources)

```json
{
  "id": "<source-uuid>",
  "name": "<human name>",
  "resource_type": "secret",
  "updated_at": "<RFC3339>",
  "checksum": "<sha256-hex of name|provider|updated_at>"
}
```

### 4.2 Secret Export Item

```json
{
  "id": "<source-uuid>",
  "name": "ANTHROPIC_API_KEY",
  "provider": "anthropic",
  "metadata": null,
  "transit_encrypted_value": "<hex>",
  "updated_at": "<RFC3339>"
}
```

`account_id` is **not** in the item — it's only in the outer envelope. The receiver maps source account → target account via peer config; source internal IDs are never re-used in the receiver's DB.

### 4.3 Agent Export Item (future resource type)

```json
{
  "id": "<source-uuid>",
  "name": "siofra-main",
  "role": "agent",
  "group_name": "primary",
  "safe": false,
  "created_at": "<RFC3339>"
}
```

Agent **tokens are not exported**. Syncing agents is metadata only (name, role, group membership). The receiver issues fresh tokens for synced agents. This preserves the receiver's autonomy over its own access control.

### 4.4 Actuator Export Item (future resource type)

```json
{
  "id": "<source-uuid>",
  "name": "our-house-actuator",
  "type": "vps",
  "enabled": true,
  "created_at": "<RFC3339>"
}
```

Same as agents: tokens not exported. Operator must re-register the actuator on the receiver with fresh tokens.

### 4.5 Export Response Envelope

```json
{
  "resource": "secrets",
  "source_account_id": "<uuid>",
  "source_broker_id": "prod-east",
  "transit_key_id": "tk_abc123",
  "exported_at": "<RFC3339>",
  "schema_version": 4,
  "items": [ ... ]
}
```

`schema_version` lets the receiver reject exports from a source with an incompatible schema (see §5.3).

---

## 5. Error Handling and Idempotency

### 5.1 Idempotency

**Secrets** — Upsert by `(account_id, name)`:

```sql
INSERT INTO secrets (id, account_id, name, provider, encrypted_value, metadata, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(account_id, name) DO UPDATE SET
    provider = excluded.provider,
    encrypted_value = excluded.encrypted_value,
    metadata = excluded.metadata,
    updated_at = excluded.updated_at;
```

The receiver generates its own `id` (new UUID) rather than re-using the source's ID. This prevents ID collisions between independent brokers.

**Agents** — Upsert by `(account_id, name)`. Token is generated fresh if creating; left unchanged if updating name/role.

**Actuators** — Upsert by `(account_id, name)`. Token generated fresh if creating.

### 5.2 Partial Failure Handling

`POST /sync/v1/import` processes items one by one. Failures on individual items are collected and returned in `errors` — they don't abort the entire batch:

```json
{
  "ok": false,
  "imported": 3,
  "skipped": 1,
  "errors": [
    {
      "item_id": "secret-uuid-4",
      "name": "PROBLEM_SECRET",
      "error": "transit decrypt failed: gcm: message authentication failed"
    }
  ]
}
```

`ok: false` when any errors occurred. Each successfully imported item is committed individually. If full atomicity is needed: use `dry_run: true` first to validate all items, then run for real.

### 5.3 Schema Version Gating

The receiver rejects exports where `schema_version` in the envelope exceeds its own schema version:

```go
if envelope.SchemaVersion > receiverSchemaVersion {
    return 409 Conflict: "source schema %d > receiver %d; upgrade receiver first"
}
```

This prevents importing data that references DB columns the receiver doesn't have yet.

### 5.4 Retry Safety

All export operations are read-only on the source — safe to retry freely. Import upserts are idempotent — a failed-and-retried import always produces the correct final state.

### 5.5 HTTP Status Conventions

| Status | Meaning |
|---|---|
| 200 | Success |
| 400 | Bad request (missing fields, invalid JSON) |
| 401 | Missing or invalid sync token |
| 403 | Valid token, not authorized for this resource/account |
| 404 | Resource not found |
| 409 | Schema version conflict |
| 422 | Transit decrypt failed (wrong transit key) |
| 500 | Source broker internal error |

---

## 6. Implementation Steps

### Phase 1: Source Broker (2–3 days)

**Step 1.1 — DB Migration 5: `sync_peers` table**

Add `sync_peers` DDL to `internal/db/migrations.go`. Add `internal/db/sync_peers.go` with:
- `CreateSyncPeer(id, label, transitKeyHex, allowedResources, allowedAccounts string) (string, error)` — returns plaintext token
- `GetSyncPeerByToken(token string) (*SyncPeer, error)`
- `ListSyncPeers() ([]*SyncPeer, error)`
- `DeleteSyncPeer(id string) error`
- `RotateSyncPeerToken(id string) (string, error)`
- `UpdateSyncPeerLastSynced(id string) error`

**Step 1.2 — Transit key crypto helpers**

Add to `internal/db/secrets.go` (or `internal/sync/crypto.go`):

```go
// ExportSecret decrypts a stored secret and re-encrypts with transitKey.
// SECURITY: plaintext exists only in RAM during this call; do not log.
func (db *DB) ExportSecret(s *Secret, masterKey, transitKey string) (string, error) {
    plaintext, err := decrypt(s.EncryptedValue, masterKey)
    if err != nil {
        return "", fmt.Errorf("decrypt for export: %w", err)
    }
    return encrypt(plaintext, transitKey)
}

// ImportSecret decrypts a transit-encrypted value and re-encrypts with masterKey.
// SECURITY: plaintext exists only in RAM during this call; do not log.
func ImportSecret(transitEncrypted, transitKey, masterKey string) (string, error) {
    plaintext, err := decrypt(transitEncrypted, transitKey)
    if err != nil {
        return "", fmt.Errorf("transit decrypt: %w", err)
    }
    return encrypt(plaintext, masterKey)
}
```

These reuse the existing `encrypt`/`decrypt` functions — no new crypto.

**Step 1.3 — Sync token auth middleware**

Add `authenticateSyncPeer(w http.ResponseWriter, r *http.Request) *db.SyncPeer` to `internal/api/sync.go`. Mirrors `authenticateAgent` but uses `sync_peers` table.

**Step 1.4 — Source broker sync endpoints**

New file `internal/api/sync.go`. Register in `NewRouter()`:

```go
r.Route("/sync/v1", func(r chi.Router) {
    r.Get("/health",   s.handleSyncHealth)
    r.Get("/manifest", s.handleSyncManifest)
    r.Post("/export",  s.handleSyncExport)
    r.Get("/peers",    s.handleSyncListPeers) // root-only
})
```

**Step 1.5 — Peer admin endpoints**

Add to `/api/` section in `NewRouter()` (root-only):

```go
r.Post("/sync/peers",           s.handleCreateSyncPeer)
r.Delete("/sync/peers/{id}",    s.handleDeleteSyncPeer)
r.Post("/sync/peers/{id}/rotate", s.handleRotateSyncPeerToken)
```

**Step 1.6 — Audit logging**

Add `sync.export` action to audit log in `handleSyncExport`. Record: peer ID, resource, item count, account ID.

### Phase 2: Receiver Side (1–2 days)

**Step 2.1 — Peer config in receiver config**

Add to `internal/config/config.go`:

```go
type SyncPeerConfig struct {
    PeerID       string            `json:"peer_id"`
    Label        string            `json:"label"`
    SourceURL    string            `json:"source_url"`
    SyncToken    string            `json:"sync_token"`
    TransitKeyID string            `json:"transit_key_id"`
    TransitKey   string            `json:"transit_key"`   // 64-char hex
    AccountMap   map[string]string `json:"account_map"`   // source_id → local_id
}

type Config struct {
    // ... existing fields ...
    SyncPeers []SyncPeerConfig
}
```

Load from `SYNC_PEERS` env var (JSON array) — matching the existing `BROKER_GATEWAYS` pattern in `main.go`.

**Step 2.2 — Import endpoint on receiver**

Add `handleSyncImport` handler to `internal/api/sync.go` (root-only). Flow:
1. Parse request, find peer config by `peer_id`
2. Map `source_account_id` → `target_account_id` via `AccountMap`
3. HTTP GET source `/sync/v1/manifest?resource=...&account_id=...`
4. Filter to requested `item_ids` (or all if omitted); skip items matching checksum
5. HTTP POST source `/sync/v1/export` with filtered IDs
6. For each item: call `db.ImportSecret(transitEncrypted, transitKey, s.MasterKey)`
7. Upsert into local DB (generate new UUIDs)
8. Update audit log: `sync.import`
9. Return summary

**Step 2.3 — Sync CLI (optional, nice to have)**

A thin `botster-sync` binary or `sync` subcommand:

```bash
# Pull all secrets from prod to staging
botster-sync pull \
  --broker https://staging.botsters.dev \
  --admin-key $ADMIN_KEY \
  --peer prod-east \
  --resource secrets \
  --source-account abc-123 \
  --target-account xyz-789 \
  --dry-run
```

This is a convenience wrapper around `POST /sync/v1/import` — useful for operator scripts.

### Phase 3: Documentation and Ops (0.5 day)

**Step 3.1 — Update DEPLOY.md** with:
- How to create sync peers on source
- How to configure receiver's `SYNC_PEERS` env
- Transit key generation (`openssl rand -hex 32`)
- Token rotation procedure
- Prod↔Prod vs. Prod→Staging setup differences

**Step 3.2 — Transit key rotation runbook:**
1. Generate new transit key: `openssl rand -hex 32`
2. Update source peer config: `POST /api/sync/peers/{id}/rotate-transit-key` (or redeploy with new key)
3. Update receiver env with new transit key; keep old key as `TRANSIT_KEY_PREV` temporarily
4. Test with `dry_run: true`
5. Remove `TRANSIT_KEY_PREV` from receiver config
6. Rotate sync token as a separate independent step

---

## 7. Security Considerations

### 7.1 Plaintext Never Persisted or Logged

The critical invariant: **plaintext secret values never appear in logs, HTTP response bodies, or disk writes.**

Implementation:
- `handleSyncExport` must not log any fields from the export item
- Add `// SECURITY: no plaintext logging below this line` comment at decrypt site
- Use `zerolog` or similar structured logger that never auto-serializes values
- Add a test that calls `handleSyncExport` and asserts no plaintext appears in captured log output

The `transit_encrypted_value` in the export response is a fresh ciphertext each call (random GCM nonce), so an attacker with network capture cannot correlate two exports of the same secret.

### 7.2 Sync Tokens Are Narrowly Scoped

Sync tokens cannot:
- Authenticate as any agent
- Access `/v1/` or `/api/` endpoints
- Trigger commands, actuators, or inference
- Write anything (export is read-only on the source)

They can only call `/sync/v1/health`, `/sync/v1/manifest`, and `/sync/v1/export` — and only for the accounts and resources listed in their peer config.

Enforcement: the `authenticateSyncPeer` middleware returns a `*db.SyncPeer` (not a `*db.Agent`), so it's structurally impossible for a sync token to pass agent-authenticated handlers.

### 7.3 Staging Gets Minimal Access

For prod→staging:
- Grant staging peer only specific named accounts, not `allowed_accounts: null`
- Use a transit key unique to the staging peer (not shared with prod↔prod peers)
- Consider syncing only a subset of secrets — e.g., API keys but not OAuth refresh tokens with real user data
- Rotate staging's transit key and sync token on a shorter schedule than prod↔prod

### 7.4 No Background Sync — Deliberate Design Choice

The absence of automatic background replication is a **security feature**, not an oversight:

- An operator always initiates sync; nothing happens silently
- If a prod secret is revoked/rotated, it doesn't silently propagate to staging
- Staging can run stale-but-safe credentials; prod controls freshness

If periodic sync is eventually desired, implement it as an explicit cron job calling `POST /sync/v1/import` — **not** as broker-internal polling. This keeps sync visible in logs and auditable.

### 7.5 TLS Enforcement

- All `/sync/v1/` endpoints must be TLS-only. Caddy handles this already.
- Add a guard in `handleSyncExport`: if `X-Forwarded-Proto` != `https`, return 403. This is a safety net against Caddy misconfiguration.
- For prod↔prod, consider mTLS (client cert pinning) as an additional layer.

### 7.6 Rate Limiting

Add rate limiting on `/sync/v1/export`: max 10 requests/minute per peer token. A misbehaving or compromised receiver can't bulk-drain the source's secrets store rapidly. Use a simple sliding window counter in the hub or a middleware (go-chi has no built-in rate limiter; use `golang.org/x/time/rate`).

### 7.7 Audit Trail

Every export call is logged with:
- `sync.export` action
- Peer ID (not the token — the peer's human-readable ID)
- Resource type and count
- Source account ID
- Timestamp

Every import call on the receiver is logged with:
- `sync.import` action
- Peer ID
- Resource type, count, skipped count
- Target account ID
- Any errors

This gives operators a complete audit trail: who pulled what, when, from where.

### 7.8 Transit Key Security

The transit key is the most sensitive part of the system — it's a symmetric key that can decrypt any in-flight export. Treat it like `MASTER_KEY`:

- Store in Doppler or equivalent (not in `.env` files committed to git)
- Use a separate transit key per peer pair (staging vs. prod↔prod)
- Rotate quarterly or on any suspected compromise
- The source stores the transit key in the `sync_peers` DB table — the DB file should be on an encrypted volume (same requirement as for `MASTER_KEY` protection)

---

## Appendix A: Existing Code Reuse Map

| New Component | Reuses |
|---|---|
| Transit encrypt/decrypt | `encrypt()`/`decrypt()` in `internal/db/secrets.go` (unchanged) |
| Sync token generation | `auth.GenerateToken("seks_sync_" + peerID)` |
| Sync token hashing | `auth.HashToken()` (unchanged) |
| UUID generation | `generateID()` in `internal/db/helpers.go` (unchanged) |
| JSON responses | `jsonResponse()`/`jsonError()` in `internal/api/v1.go` (unchanged) |
| Root auth check | `s.requireRoot(r)` in `internal/api/middleware.go` (unchanged) |
| Audit logging | `s.DB.LogAudit()` in `internal/db/audit.go` (unchanged) |
| Config pattern | `SYNC_PEERS` env var mirrors existing `BROKER_GATEWAYS` pattern |

No existing code needs to be modified for Phase 1 (source endpoints). Phase 2 (receiver import) adds one new handler and config fields.

---

## Appendix B: Peer Config Example (Receiver)

```json
[
  {
    "peer_id": "prod-east",
    "label": "Production East",
    "source_url": "https://broker.botsters.dev",
    "sync_token": "seks_sync_prod-east_abc123def456...",
    "transit_key_id": "tk_2026q1",
    "transit_key": "deadbeef...<64 hex chars>",
    "account_map": {
      "prod-account-uuid-123": "staging-account-uuid-456"
    }
  }
]
```

Set as `SYNC_PEERS=<json>` in the receiver's environment (Doppler, systemd EnvironmentFile, etc.).

---

## Appendix C: Prod↔Prod vs. Prod→Staging Differences

| Aspect | Prod↔Prod | Prod→Staging |
|---|---|---|
| Direction | Bidirectional (each side is source for the other) | Unidirectional (prod is source only) |
| `allowed_accounts` | Usually `null` (all accounts) | Specific account IDs only |
| Transit key | Shared unique key between the two prod brokers | Unique key, never shared with prod↔prod peer |
| mTLS | Strongly recommended | Optional |
| Token rotation | Quarterly | Monthly or on every deploy |
| Rate limiting | Loose (high trust) | Tighter (prod data in staging is sensitive) |
| Agent/actuator sync | Full metadata + token re-issue | Secrets only (agents and actuators differ between envs) |

