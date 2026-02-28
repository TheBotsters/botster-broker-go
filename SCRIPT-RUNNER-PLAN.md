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
- Suggest running as a separate user with narrow sudo rules (not general actuator user)

## Implementation Location

Likely a new capability in `botster-actuator-2`, or a standalone lightweight
actuator specifically for privileged ops (cleaner separation).

## Status

Deferred — 2026-02-27. Resume after current priorities.
