# Integration Checklist
**For: Go Broker (botster-broker-go)**
**Date:** 2026-03-03

## Purpose
Prevent development blocks by catching common failure modes before they break agent functionality.

## Automated Test Script
Run after deployments or when troubleshooting:
```bash
# From workspace (has agent token configured)
~/workspace/test-broker-practical.sh

# Or with custom token
MY_TOKEN=your_agent_token ~/workspace/test-broker-practical.sh
```

## What It Tests (Automated)
1. **Agent functionality** — can access secrets, exec commands
2. **Critical secrets** — ANTHROPIC_TOKEN, BRAVE_BASE_API_TOKEN, BOTSTERSORG_GITHUB_PERSONAL_ACCESS_TOKEN exist
3. **Agent isolation** — cannot read other agents' exclusive secrets (security)
4. **Broker reachability** — health endpoint responds

## Manual Steps (When Automated Tests Pass)
Run these after automated tests pass, or when setting up new environment:

### 1. Admin Access Verification
```bash
# Master key in /etc/botster-broker/env
MASTER_KEY=$(sudo cat /etc/botster-broker/env | grep MASTER_KEY | cut -d= -f2)

# Test admin endpoint
curl -H "X-Admin-Key: $MASTER_KEY" https://broker-internal.seksbot.com/api/accounts
```
**Expected:** JSON list of accounts (at least one)

### 2. Web UI Pages
```bash
# Login page (should return HTML)
curl -I https://broker-internal.seksbot.com/login | grep 200

# Secrets page (should return HTML or redirect to login)
curl -I https://broker-internal.seksbot.com/secrets
```
**Note:** `/secrets` may 404 if not yet deployed.

### 3. GitHub PAT Validation (Optional)
```bash
# Get BotstersOrg PAT via agent token
PAT=$(curl -s -H "Authorization: Bearer $AGENT_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"BOTSTERSORG_GITHUB_PERSONAL_ACCESS_TOKEN"}' \
  https://broker-internal.seksbot.com/v1/secrets/get | jq -r '.value')

# Test GitHub API access
curl -H "Authorization: token $PAT" https://api.github.com/rate_limit
```
**Expected:** GitHub rate limit information

### 4. Actuator Account Verification
```bash
# Exec whoami and verify expected username
curl -s -H "Authorization: Bearer $AGENT_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"capability":"exec","payload":{"command":"whoami","args":[]},"timeoutSeconds":5}' \
  https://broker-internal.seksbot.com/v1/command | jq -r '.result.stdout'
```
**Expected:** `siofra_actuator` (or appropriate actuator username)

## Known Issues (To Fix)
1. **Agent isolation broken** — agents can read each other's secrets (security issue)
2. **`/secrets` web UI page missing** — returns 404 (human access issue)
3. **No self-test endpoints** — manual testing required

## Prevention Checklist
**Before deploying broker changes:**
- [ ] Run automated test script
- [ ] Verify admin access works
- [ ] Check actuator connectivity

**After deployment:**
- [ ] Re-run automated tests
- [ ] Test web UI pages
- [ ] Verify agent isolation still works

## Failure Modes We've Experienced
1. **DB wipe** (2026-02-25) — lost all secrets
2. **Missing web UI** (2026-03-03) — human admin access broken
3. **Agent isolation failure** (ongoing) — security compromise
4. **Actuator connectivity loss** (previous incidents) — exec broken

## Adding New Tests
When a new failure mode is discovered:
1. Add test to `test-broker-practical.sh`
2. Update this checklist
3. Ensure test catches the failure before it blocks development

## References
- Automated test script: `~/workspace/test-broker-practical.sh`
- Manual test instructions: `~/workspace/broker-admin-test-instructions.md`
- Connection checklist: `~/workspace/broker-connection-checklist.md`
