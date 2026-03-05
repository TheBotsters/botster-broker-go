# Test Instructions for Full Sync Flow

## Prerequisites

1. Two broker instances running (source and receiver)
2. Master keys configured for both brokers
3. Sync peer configured on source broker
4. Sync token and transit key shared between brokers

## Step 1: Configure Source Broker

### Create a sync peer on the source broker:

```bash
# Using the admin API
curl -X POST http://source-broker:8787/api/sync/peers \
  -H "Authorization: Bearer $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "receiver-1",
    "label": "Receiver Broker",
    "transit_key_hex": "64-char-hex-transit-key",
    "transit_key_id": "transit-key-1",
    "allowed_resources": "secrets",
    "allowed_accounts": "source-account-id"
  }'
```

Response will include a sync token. Save this token.

### Verify sync peer was created:

```bash
curl -X GET http://source-broker:8787/api/sync/peers \
  -H "Authorization: Bearer $ADMIN_KEY"
```

## Step 2: Configure Receiver Broker

Update receiver broker's configuration to include sync peer:

```env
SYNC_PEERS='[
  {
    "peer_id": "receiver-1",
    "label": "Receiver Broker",
    "source_url": "https://source-broker.example.com",
    "sync_token": "token-from-step-1",
    "transit_key_id": "transit-key-1",
    "transit_key": "64-char-hex-transit-key",
    "account_map": {
      "source-account-id": "local-account-id"
    }
  }
]'
```

## Step 3: Test Manifest Endpoint

### Request manifest from source:

```bash
curl -X GET "https://source-broker.example.com/sync/v1/manifest?resource=secrets&account_id=source-account-id" \
  -H "Authorization: Bearer $SYNC_TOKEN"
```

Expected response:
```json
{
  "resource": "secrets",
  "source_account_id": "source-account-id",
  "generated_at": "2024-01-01T00:00:00Z",
  "items": [
    {
      "id": "secret-id-1",
      "name": "secret-name-1",
      "provider": "provider-1",
      "updated_at": "2024-01-01T00:00:00Z",
      "checksum": "sha256-hash-of-encrypted-data"
    }
  ]
}
```

**Security check**: Verify checksums are computed WITHOUT decrypting secrets.

## Step 4: Test Export Endpoint

### Request export of specific secrets:

```bash
curl -X POST https://source-broker.example.com/sync/v1/export \
  -H "Authorization: Bearer $SYNC_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "resource": "secrets",
    "source_account_id": "source-account-id",
    "item_ids": ["secret-id-1", "secret-id-2"],
    "transit_key_id": "transit-key-1"
  }'
```

Expected response:
```json
{
  "resource": "secrets",
  "source_account_id": "source-account-id",
  "source_broker_id": "",
  "transit_key_id": "transit-key-1",
  "exported_at": "2024-01-01T00:00:00Z",
  "schema_version": 1,
  "items": [
    {
      "id": "secret-id-1",
      "name": "secret-name-1",
      "provider": "provider-1",
      "transit_encrypted_value": "encrypted-with-transit-key",
      "metadata": "optional-metadata",
      "updated_at": "2024-01-01T00:00:00Z"
    }
  ]
}
```

**Security check**: Verify secrets are encrypted with transit key, not master key.

## Step 5: Test Import Endpoint

### Trigger sync on receiver:

```bash
curl -X POST http://receiver-broker:8787/sync/v1/import \
  -H "Authorization: Bearer $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "peer_id": "receiver-1",
    "resource": "secrets",
    "source_account_id": "source-account-id",
    "target_account_id": "local-account-id",
    "item_ids": ["secret-id-1", "secret-id-2"],
    "dry_run": false
  }'
```

Expected response:
```json
{
  "ok": true,
  "dry_run": false,
  "imported": 2,
  "skipped": 0,
  "errors": [],
  "items": [
    {
      "id": "secret-id-1",
      "name": "secret-name-1",
      "action": "created"
    },
    {
      "id": "secret-id-2",
      "name": "secret-name-2",
      "action": "created"
    }
  ]
}
```

## Step 6: Verify Sync Results

### Check secrets on receiver:

```bash
curl -X POST http://receiver-broker:8787/v1/secrets/list \
  -H "X-Account-ID: local-account-id" \
  -H "Content-Type: application/json" \
  -d '{}'
```

Should show the synced secrets.

## Step 7: Test Security Features

### 1. Rate Limiting Test

```bash
# Make rapid requests to trigger rate limit
for i in {1..11}; do
  curl -X GET "https://source-broker.example.com/sync/v1/manifest?resource=secrets&account_id=source-account-id" \
    -H "Authorization: Bearer $SYNC_TOKEN" \
    -w "Request $i: %{http_code}\n" \
    -o /dev/null \
    -s
  sleep 1
done
```

Expected: After 10 requests in a minute, should get 429 Too Many Requests.

### 2. Authentication Test

```bash
# Try without auth token
curl -X GET "https://source-broker.example.com/sync/v1/manifest?resource=secrets&account_id=source-account-id" \
  -w "Status: %{http_code}\n" \
  -o /dev/null \
  -s

# Try with invalid token
curl -X GET "https://source-broker.example.com/sync/v1/manifest?resource=secrets&account_id=source-account-id" \
  -H "Authorization: Bearer invalid-token" \
  -w "Status: %{http_code}\n" \
  -o /dev/null \
  -s
```

Expected: Both should return 401 Unauthorized.

### 3. Authorization Test

```bash
# Try to access unauthorized account
curl -X GET "https://source-broker.example.com/sync/v1/manifest?resource=secrets&account_id=unauthorized-account" \
  -H "Authorization: Bearer $SYNC_TOKEN" \
  -w "Status: %{http_code}\n" \
  -o /dev/null \
  -s
```

Expected: 403 Forbidden if peer not allowed to access that account.

### 4. Error Message Sanitization Test

```bash
# Try to export with wrong transit key ID
curl -X POST https://source-broker.example.com/sync/v1/export \
  -H "Authorization: Bearer $SYNC_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "resource": "secrets",
    "source_account_id": "source-account-id",
    "item_ids": ["secret-id-1"],
    "transit_key_id": "wrong-key-id"
  }' \
  -w "Status: %{http_code}\n" \
  -o /dev/null \
  -s
```

Expected: 400 Bad Request with generic error message, not decryption details.

## Step 8: Check Audit Logs

### On source broker:

```sql
SELECT * FROM audit_log WHERE event LIKE 'sync.%' ORDER BY created_at DESC LIMIT 10;
```

Should show:
- `sync.manifest` events for manifest requests
- `sync.export` events for export requests
- `sync.manifest.error` / `sync.export.error` for any failures

### On receiver broker:

```sql
SELECT * FROM audit_log WHERE event LIKE 'sync.%' ORDER BY created_at DESC LIMIT 10;
```

Should show `sync.import` events.

## Step 9: Build Verification

```bash
# Verify code builds successfully
cd /path/to/botster-broker-go
go build ./...

# Run tests
go test ./internal/db -v
go test ./internal/api -v
```

## Summary

The full sync flow should work end-to-end with all security features:

1. **Authentication**: Sync tokens required for source endpoints
2. **Authorization**: Peer restrictions enforced (resources, accounts)
3. **Encryption**: Secrets never transmitted in plaintext
4. **Rate limiting**: Prevents abuse of sync endpoints
5. **Error sanitization**: No leakage of decryption details
6. **Audit logging**: All operations logged for security monitoring
7. **Checksum security**: Manifest checksums computed without decrypting