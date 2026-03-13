# Botster Spine — Data Model

*How entities relate, and why they're separated the way they are.*

## Entities

There are five core entities in the Spine. Understanding why each exists
as a separate thing — and not collapsed into one of the others — is
critical to understanding the security model.

### 1. Account

An ownership and isolation boundary. All other entities belong to an account.

```
accounts
├── id          (PK)
├── email       (unique login)
├── password    (bcrypt)
└── created_at
```

Multi-tenancy is enforced at every API layer. Account A cannot see or
touch anything belonging to Account B. In a hosted deployment, each
customer is an account.

### 2. Provider

**What an external service looks like.** Its API shape — where it lives,
how it accepts authentication. A provider is public knowledge. Knowing
that GitHub's API is at `https://api.github.com` and uses bearer tokens
is not a secret.

```
providers
├── id          (PK)
├── account_id  (FK → accounts)
├── name        ("github", "hetzner", "cloudflare")
├── display_name ("GitHub", "Hetzner Cloud")
├── base_url    ("https://api.github.com")
├── auth_type   ("bearer" | "basic" | "header")
├── auth_header ("Authorization" — or custom header name for auth_type=header)
├── created_at
└── updated_at
```

A provider does **not** reference any secret. It doesn't know which
credentials exist for it. It only knows the shape of the API.

**Why separate?** Because multiple secrets can exist for the same provider.
Peter might have three GitHub PATs — personal, org, and CI. All three
talk to `https://api.github.com` with bearer auth. The provider is the
same; the credentials are different.

### 3. Secret

**A credential.** An encrypted value stored in the Spine's database. A secret
has a name (admin-chosen, for the admin's own reference) and a provider tag
(for grouping in the admin UI). The encrypted value is decrypted only
in-memory, only when needed, only by the Spine process.

```
secrets
├── id              (PK)
├── account_id      (FK → accounts)
├── name            ("ROTCS_GITHUB_PAT", "BOTSTERS_GITHUB_PAT")
├── provider        ("github" — informational, for admin UI grouping)
├── encrypted_value (AES-256-GCM with broker master key)
├── created_at
└── updated_at
```

Secrets are **never exposed to agents.** No agent-facing API endpoint
returns a secret name or value. The admin UI (management API) can
retrieve values for display — authenticated with session credentials,
not agent tokens.

**Why separate from providers?** Because a credential is not an API shape.
"GitHub uses bearer tokens" is one fact. "This particular PAT with value
ghp_xxx belongs to the Botsters org" is a completely different fact. Merging
them means one provider = one credential, which falls apart the moment you
need two PATs for the same service.

**Why separate from capabilities?** Because a credential is not a permission.
The same credential might back multiple capabilities (unlikely but possible),
or a capability might be re-pointed to a different credential during key
rotation without the agent knowing anything changed.

### 4. Capability

**What an agent is allowed to do.** A capability is the indirection layer
between what agents see and what credentials exist. It binds:

- An **agent-visible name** (chosen by the admin, meaningful to the agent)
- A **provider** (API shape)
- A **secret** (credential)

```
capabilities
├── id            (PK)
├── account_id    (FK → accounts)
├── name          ("github-personal", "github-botsters", "hetzner")
├── display_name  ("GitHub (Personal)", "GitHub (Botsters Org)")
├── provider_id   (FK → providers)
├── secret_id     (FK → secrets)
└── created_at
```

The capability name is what agents use in API calls:
`sekdo github-botsters list-repos`. The agent knows `github-botsters`
exists. The agent does **not** know that it maps to secret
`BOTSTERS_GITHUB_PAT` with value `ghp_yyy`.

**Why separate from providers?** Because the agent needs to distinguish
between "GitHub for the Botsters org" and "GitHub for my personal account."
Both use the same provider (same API, same auth pattern). They differ in
which credential is used. The capability is the binding.

**Why separate from secrets?** Because the agent-visible name is admin-chosen
and meaningful ("github-botsters"), while the secret name is internal
bookkeeping ("ROTCS_GITHUB_PAT"). Exposing secret names to agents leaks
information they don't need. The capability name is the safe, intentional
interface.

**Key rotation:** When a PAT expires and is replaced, the admin updates the
secret's encrypted value. The capability still points to the same secret ID.
Every agent using that capability continues working. No capability changes,
no agent notification, no config push.

**Revocation:** Remove the capability grant. The agent immediately loses access.
The secret and provider are untouched — other agents with their own grants
to the same capability still work.

### 5. Capability Grant

**Which agent has which capability.** A join table.

```
capability_grants
├── id              (PK)
├── capability_id   (FK → capabilities)
├── agent_id        (FK → agents)
└── created_at

UNIQUE(capability_id, agent_id)
```

Granting is explicit. An agent has zero capabilities until an admin grants
them. There is no implicit "agent in this account can access all secrets"
fallback.

## Entity Relationships

```
Account
├── Providers (API shapes)
│     github, hetzner, cloudflare
│
├── Secrets (credentials, encrypted)
│     ROTCS_GITHUB_PAT, BOTSTERS_GITHUB_PAT, HETZNER_API_TOKEN
│
├── Capabilities (bindings: name → provider + secret)
│     github-personal  → github provider + ROTCS_GITHUB_PAT
│     github-botsters  → github provider + BOTSTERS_GITHUB_PAT
│     hetzner          → hetzner provider + HETZNER_API_TOKEN
│
├── Agents
│     nira, footgun
│
└── Capability Grants (agent → capability)
      nira     → github-personal, github-botsters, hetzner
      footgun  → github-botsters
```

## What Agents See vs. What Exists

| Layer | Agent sees | Admin sees |
|-------|-----------|-----------|
| Provider | (nothing — implicit in capability) | github, hetzner, cloudflare |
| Secret | (nothing) | ROTCS_GITHUB_PAT = ghp_xxx... |
| Capability | "github-botsters", "hetzner" | github-botsters → github provider + BOTSTERS_GITHUB_PAT |
| Grant | "I have github-botsters" | nira has github-botsters |

## Request Flow

```
Agent calls: sekdo github-botsters list-repos

1. sekdo builds: POST /v1/proxy/request
   {"capability": "github-botsters", "method": "GET", "url": "https://api.github.com/user/repos"}

2. Spine: authenticate agent token → agent "nira"

3. Spine: look up capability "github-botsters" for this account
   → provider_id → provider "github" (base_url, auth_type)
   → secret_id  → secret "BOTSTERS_GITHUB_PAT"

4. Spine: check capability_grants for (capability_id, agent_id)
   → grant exists → proceed

5. Spine: validate URL starts with provider.base_url
   → "https://api.github.com/user/repos" starts with "https://api.github.com" → ok

6. Spine: decrypt secret value in-memory

7. Spine: inject credential into outgoing request
   → auth_type=bearer → Authorization: Bearer ghp_yyy...

8. Spine: make HTTP request to GitHub

9. Spine: audit log (agent=nira, capability=github-botsters, method=GET, url=..., status=200)

10. Spine: return response to agent (headers + body, no credentials)
```

## Anti-Patterns This Model Prevents

| Anti-pattern | How it's prevented |
|-------------|-------------------|
| Agent sees secret value | No agent-facing endpoint returns secret values |
| Agent sees secret name | Capabilities use admin-chosen names; secret names are internal |
| One provider = one credential | Capabilities decouple: N capabilities can share one provider |
| Implicit access | Capability grants are explicit; no "account-wide" fallback |
| Credential in agent config | Agent holds only its Spine token; all credentials are in the Spine |
| Key rotation breaks agents | Update secret value; capability binding unchanged; agents unaffected |
| Revoking one agent breaks others | Remove one grant; other agents' grants are independent |
