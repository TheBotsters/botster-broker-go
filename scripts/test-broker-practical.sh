#!/bin/bash
# Practical Broker Test Script
# Tests that matter for development continuity

set -e

BROKER_URL="${1:-http://localhost:8080}"
MY_TOKEN="${MY_TOKEN:-}"  # Set via env var

echo "=== Practical Broker Test ==="
echo "Testing connections that break our development"
echo ""

# 1. Can I work? (Agent functionality)
echo "1. Agent Can Work"
echo -n "  List secrets... "
if curl -s -H "Authorization: Bearer $MY_TOKEN" -H "Content-Type: application/json" -d '{}' "$BROKER_URL/v1/secrets/list" | jq -e '. | length > 0' > /dev/null 2>&1; then
    echo "✓"
else
    echo "✗ (Agent cannot access secrets)"
    exit 1
fi

# 2. Can I exec? (Actuator connectivity)
echo -n "  Exec whoami... "
if curl -s -H "Authorization: Bearer $MY_TOKEN" -H "Content-Type: application/json" -d '{"capability":"exec","payload":{"command":"whoami","args":[]},"timeoutSeconds":5}' "$BROKER_URL/v1/command" | jq -e '.result.stdout' > /dev/null 2>&1; then
    echo "✓"
else
    echo "✗ (Cannot exec through actuator)"
    exit 1
fi

# 3. Critical secrets exist (check by list visibility)
echo ""
echo "2. Critical Secrets Exist"
secrets_list=$(curl -s -H "Authorization: Bearer $MY_TOKEN" -H "Content-Type: application/json" -d '{}' "$BROKER_URL/v1/secrets/list")
for secret in ANTHROPIC_TOKEN BRAVE_BASE_API_TOKEN BOTSTERSORG_GITHUB_PERSONAL_ACCESS_TOKEN; do
    echo -n "  $secret... "
    if echo "$secrets_list" | jq -e --arg secret "$secret" 'type == "array" and any(.[]; .name == $secret)' > /dev/null 2>&1; then
        echo "✓"
    else
        echo "✗ (Missing from broker secret index)"
    fi
done

# 4. Agent isolation (security check)
echo ""
echo "3. Security: Agent Isolation"
echo -n "  Cannot read Annie's GitHub PAT value... "
response=$(curl -s -H "Authorization: Bearer $MY_TOKEN" -H "Content-Type: application/json" -d '{"name":"ANNIE_GITHUB_PERSONAL_ACCESS_TOKEN"}' "$BROKER_URL/v1/secrets/get")
if echo "$response" | jq -e '.value? and (.value | tostring | length > 0)' > /dev/null 2>&1; then
    echo "✗ (ISOLATION BROKEN - value returned)"
    echo "    Security issue: agents should not read other agents' secret values"
elif echo "$response" | jq -e '.error == "unauthorized" or .error == "forbidden"' > /dev/null 2>&1; then
    echo "✓ (Value access blocked)"
else
    echo "? (Unexpected response: $(echo "$response" | jq -c . 2>/dev/null || echo "$response"))"
fi

# 5. Broker reachable
echo ""
echo "4. Broker Reachable"
echo -n "  Health check... "
if curl -s -f "$BROKER_URL/health" > /dev/null 2>&1; then
    echo "✓"
else
    echo "✗ (Broker down)"
    exit 1
fi

echo ""
echo "=== Test Complete ==="
echo "Run this after deployments or when things feel broken."
echo "Focus: Can agents work? Are critical connections intact?"

# 6. Web UI Pages
echo ""
echo "5. Web UI Pages"
echo -n "  Login page... "
if curl -s -f "$BROKER_URL/login" > /dev/null 2>&1; then
    echo "✓"
else
    echo "✗ (Login page missing)"
fi

echo -n "  Secrets page... "
status=$(curl -s -o /dev/null -w "%{http_code}" "$BROKER_URL/secrets")
if [ "$status" -eq 200 ] || [ "$status" -eq 302 ] || [ "$status" -eq 303 ]; then
    echo "✓ ($status)"
else
    echo "✗ ($status - may need deployment)"
fi

# 6. GitHub Org Administration Test (if AEONBYTE_TOKEN provided)
if [ -n "$AEONBYTE_TOKEN" ]; then
    echo ""
    echo "6. GitHub Org Administration (AeonByte)"
    echo "   Note: This test requires AEONBYTE_TOKEN environment variable"
    
    # Quick check if token is agent token
    if [[ "$AEONBYTE_TOKEN" == seks_agent_* ]]; then
        echo -n "   Token type... ✓ (agent token)"
        # Try to get GitHub token
        GITHUB_TOKEN_RESPONSE=$(curl -s -H "Authorization: Bearer $AEONBYTE_TOKEN" \
          -H "Content-Type: application/json" \
          -d '{"name":"BOTSTERSORG_GITHUB_PERSONAL_ACCESS_TOKEN"}' \
          "$BROKER_URL/v1/secrets/get" 2>/dev/null || echo '{"error":"request failed"}')
        
        if echo "$GITHUB_TOKEN_RESPONSE" | jq -e '.value' > /dev/null 2>&1; then
            echo " | GitHub token accessible... ✓"
        elif echo "$GITHUB_TOKEN_RESPONSE" | jq -e '.error' > /dev/null 2>&1; then
            ERROR=$(echo "$GITHUB_TOKEN_RESPONSE" | jq -r '.error')
            echo " | GitHub token access... ✗ ($ERROR)"
        else
            echo " | GitHub token check... ? (unexpected response)"
        fi
    else
        echo "   Token type... ✗ (not an agent token: $(echo "$AEONBYTE_TOKEN" | cut -c1-20)...)"
    fi
    echo "   Run full test: ./test-broker-github-access.sh"
fi
