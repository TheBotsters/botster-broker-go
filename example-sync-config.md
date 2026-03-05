# Broker Sync Configuration Example

## SYNC_PEERS Environment Variable Example

The `SYNC_PEERS` environment variable should contain a JSON array of sync peer configurations. Here's an example for syncing from a production broker to a staging broker:

```json
[
  {
    "peer_id": "prod-east",
    "label": "Production East Broker",
    "source_url": "https://broker.botsters.dev",
    "sync_token": "seks_sync_prod-east_abc123def4567890abcdef1234567890",
    "transit_key_id": "tk_2026q1",
    "transit_key": "deadbeef1234567890abcdef1234567890deadbeef1234567890abcdef1234567890",
    "account_map": {
      "prod-account-uuid-123": "staging-account-uuid-456"
    }
  }
]
```

## Generating Transit Keys

Generate a 256-bit (64 hex character) transit key:

```bash
openssl rand -hex 32
# Example output: deadbeef1234567890abcdef1234567890deadbeef1234567890abcdef1234567890
```

## Setting Up Sync Peers on Source Broker

On the source broker (the one being synced FROM), you need to create a sync peer record:

1. Generate a transit key (see above)
2. Create the sync peer via API:

```bash
curl -X POST https://source-broker.example.com/api/sync/peers \
  -H "X-Admin-Key: YOUR_ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "staging",
    "label": "Staging Broker",
    "transit_key_hex": "deadbeef1234567890abcdef1234567890deadbeef1234567890abcdef1234567890",
    "transit_key_id": "tk_2026q1",
    "allowed_resources": "secrets",
    "allowed_accounts": "prod-account-uuid-123"
  }'
```

Response will include the sync token:
```json
{
  "id": "staging",
  "token": "seks_sync_staging_abc123def4567890abcdef1234567890"
}
```

3. Save this token and add it to the receiver's `SYNC_PEERS` config.

## Testing Sync Between Two Broker Instances

### Prerequisites

1. Two running broker instances:
   - Source broker (port 8787)
   - Receiver broker (port 8788)

2. On source broker:
   - Create an account: `POST /api/accounts`
   - Create some secrets: `POST /api/secrets`
   - Create a sync peer for the receiver (as above)

3. On receiver broker:
   - Create a corresponding account (different ID)
   - Set `SYNC_PEERS` environment variable with the configuration

### Test 1: Dry Run Sync

```bash
curl -X POST http://localhost:8788/sync/v1/import \
  -H "X-Admin-Key: RECEIVER_ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "peer_id": "prod-east",
    "resource": "secrets",
    "source_account_id": "prod-account-uuid-123",
    "target_account_id": "staging-account-uuid-456",
    "item_ids": [],
    "dry_run": true
  }'
```

### Test 2: Actual Sync

```bash
curl -X POST http://localhost:8788/sync/v1/import \
  -H "X-Admin-Key: RECEIVER_ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "peer_id": "prod-east",
    "resource": "secrets",
    "source_account_id": "prod-account-uuid-123",
    "target_account_id": "staging-account-uuid-456",
    "item_ids": []
  }'
```

### Test 3: Sync Specific Items

```bash
curl -X POST http://localhost:8788/sync/v1/import \
  -H "X-Admin-Key: RECEIVER_ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "peer_id": "prod-east",
    "resource": "secrets",
    "source_account_id": "prod-account-uuid-123",
    "target_account_id": "staging-account-uuid-456",
    "item_ids": ["secret-uuid-1", "secret-uuid-2"]
  }'
```

## Expected Responses

### Successful Sync Response

```json
{
  "ok": true,
  "dry_run": false,
  "imported": 3,
  "skipped": 0,
  "errors": [],
  "items": [
    {"id": "secret-uuid-1", "name": "ANTHROPIC_API_KEY", "action": "created"},
    {"id": "secret-uuid-2", "name": "OPENAI_API_KEY", "action": "created"},
    {"id": "secret-uuid-3", "name": "BRAVE_API_KEY", "action": "created"}
  ]
}
```

### Partial Failure Response

```json
{
  "ok": false,
  "dry_run": false,
  "imported": 2,
  "skipped": 0,
  "errors": [
    "item secret-uuid-3 (BRAVE_API_KEY): transit decrypt failed: gcm: message authentication failed"
  ],
  "items": [
    {"id": "secret-uuid-1", "name": "ANTHROPIC_API_KEY", "action": "created"},
    {"id": "secret-uuid-2", "name": "OPENAI_API_KEY", "action": "created"},
    {"id": "secret-uuid-3", "name": "BRAVE_API_KEY", "action": "error", "error": "transit decrypt failed: gcm: message authentication failed"}
  ]
}
```

## Audit Logging

Sync operations are logged in the audit log with action `sync.import` (receiver side) and `sync.export` (source side when endpoints are implemented).

To view audit logs:
```bash
curl -H "X-Admin-Key: ADMIN_KEY" http://localhost:8788/api/audit
```

## Troubleshooting

1. **"peer not found in SYNC_PEERS config"**: Check that `peer_id` in request matches exactly the `peer_id` in `SYNC_PEERS` config.

2. **"transit decrypt failed"**: Ensure transit keys match exactly between source and receiver.

3. **HTTP 401/403**: Check sync token is valid and has required permissions on source broker.

4. **HTTP 404**: Source broker might not have sync endpoints implemented yet (Phase 2).

5. **Account mapping issues**: Verify `account_map` in `SYNC_PEERS` includes the source account ID.

## Security Notes

1. Transit keys should be stored securely (Doppler, etc.)
2. Sync tokens should be rotated periodically
3. Use HTTPS for production sync
4. Consider mTLS for prod↔prod sync
5. Monitor audit logs for sync activity