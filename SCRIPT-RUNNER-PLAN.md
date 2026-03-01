# Script-Runner Actuator Capability — Plan

## Concept

A new actuator capability (`script-runner`) that executes only pre-approved,
named shell scripts. No arguments from the brain — zero injection surface.

## Interface

Brain sends:
```json
{
  "capability": "script-runner",
  "payload": { "script": "deploy-broker" }
}
```

Actuator looks up name in whitelist → runs script → returns stdout/stderr.
Unknown names: hard rejection, no execution.

## Config (TOML)

```toml
[scripts]
deploy-broker         = "/home/siofra_actuator/scripts/deploy-broker.sh"
rotate-key            = "/home/siofra_actuator/scripts/rotate-master-key.sh"
db-backup             = "/home/siofra_actuator/scripts/backup-db.sh"
```

## No Args — By Design

Scripts are fully self-contained. All cleverness (which tag/release/commit to
deploy, which channel, etc.) lives inside the script in bash — not in runtime
input. The brain only picks a name. Example deploy script:

```bash
#!/bin/bash
set -euo pipefail
cd /home/siofra_actuator/dev/botster-broker-go
git fetch origin
LATEST=$(git tag --sort=-version:refname | head -1)
echo "Deploying $LATEST..."
git checkout "$LATEST"
/usr/local/go/bin/go build -o /usr/local/bin/botster-broker ./cmd/broker
systemctl restart botster-broker
echo "Done."
```

## Security Properties

- Brain controls only the script *name*, never path, args, or execution context
- Whitelist is config on the machine, written/reviewed by humans
- All invocations audited via broker with agent identity
- Runs as a dedicated user with narrow sudo rules (not the general actuator user)

## Implementation Location

**Standalone lightweight actuator** — decided by Peter 2026-03-01. Not part of
`botster-actuator-2`. Cleaner privilege separation: its own user, its own sudo
rules, its own broker registration.

## Broker vs Actuator Split

- **Broker changes: minimal to none.** The broker routes capability payloads
  generically. `script-runner` looks like any other capability message — no
  broker surgery needed. Optional: schema validation for known capability names,
  but not required.
- **Actuator changes: all of it.** The work is entirely in the new standalone actuator:
  1. Add `script-runner` capability handler
  2. Parse TOML whitelist config
  3. Hard-reject unknown script names
  4. Execute script, return stdout/stderr
  5. Explicit local audit logging (broker message trail provides implicit audit,
     but local logs add value)

## Status

Assigned to FootGun — 2026-03-01. Originally deferred 2026-02-27.
