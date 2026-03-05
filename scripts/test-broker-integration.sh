#!/bin/bash
# Broker Integration Test Script
# Run manually before/after deployments to catch regressions

set -e

BROKER_URL="${1:-http://localhost:8080}"
MASTER_KEY="${MASTER_KEY:-a3b17f17299bb3c29f445fdf7b10d2d97ee0b9a73649b6ae3c477fa8c27ff0be}"
AGENT_TOKEN="${AGENT_TOKEN:-}"  # Set via env var

echo "=== Broker Integration Test ==="
echo "Broker: $BROKER_URL"
echo "Date: $(date)"
echo ""

# Helper function
test_endpoint() {
    local method=$1
    local path=$2
    local expected_status=$3
    local auth_header=$4
    local data=$5
    
    echo -n "  $method $path... "
    
    local curl_cmd="curl -s -X '$method' '$BROKER_URL$path'"
    
    if [ -n "$auth_header" ]; then
        curl_cmd="$curl_cmd -H '$auth_header'"
    fi
    
    if [ -n "$data" ]; then
        curl_cmd="$curl_cmd -H 'Content-Type: application/json' -d '$data'"
    fi
    
    curl_cmd="$curl_cmd -w '%{http_code}' -o /tmp/curl_output.$$"
    
    eval "$curl_cmd" > /tmp/curl_status.$$
    local status=$(cat /tmp/curl_status.$$)
    local output=$(cat /tmp/curl_output.$$)
    
    rm -f /tmp/curl_output.$$ /tmp/curl_status.$$
    
    if [ "$status" -eq "$expected_status" ]; then
        echo "✓ $status"
        return 0
    else
        echo "✗ $status (expected $expected_status)"
        echo "    Response: $output"
        return 1
    fi
}

# Track failures
failures=0

echo "1. Core Health"
test_endpoint GET /health 200 "" || failures=$((failures + 1))

echo ""
echo "2. Admin Endpoints (MASTER_KEY)"
test_endpoint GET "/api/accounts" 200 "X-Admin-Key: $MASTER_KEY" || failures=$((failures + 1))

echo ""
echo "3. Agent API (Bearer token)"
test_endpoint POST /v1/secrets/list 200 "Authorization: Bearer $AGENT_TOKEN" "{}" || failures=$((failures + 1))
test_endpoint GET /v1/inference/providers 200 "Authorization: Bearer $AGENT_TOKEN" || failures=$((failures + 1))

echo ""
echo "4. Web UI Pages"
test_endpoint GET /login 200 "" || failures=$((failures + 1))
test_endpoint GET /secrets 200 "" || failures=$((failures + 1))  # Should redirect to login if not auth'd

echo ""
echo "5. Inference Proxy (requires actual token)"
# Skip if no Anthropic token available
if [ -n "$ANTHROPIC_TOKEN" ]; then
    test_endpoint POST /v1/inference 200 "Authorization: Bearer $AGENT_TOKEN" '{"provider":"anthropic","model":"claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"Hello"}]}' || failures=$((failures + 1))
else
    echo "  ⚠️  Inference test skipped (no ANTHROPIC_TOKEN)"
fi

echo ""
echo "6. Command Routing (requires actuator)"
# Basic test - just check endpoint exists
test_endpoint POST /v1/command 400 "Authorization: Bearer $AGENT_TOKEN" '{"capability":"exec"}' || failures=$((failures + 1))

echo ""
echo "=== Summary ==="
if [ $failures -eq 0 ]; then
    echo "✅ All tests passed"
    exit 0
else
    echo "❌ $failures test(s) failed"
    echo ""
    echo "Next steps:"
    echo "1. Check broker logs: sudo journalctl -u botster-broker --since '5 minutes ago'"
    echo "2. Verify Caddy is running: sudo systemctl status caddy"
    echo "3. Check database: sudo sqlite3 /var/lib/botster-broker/broker.db '.tables'"
    exit 1
fi
