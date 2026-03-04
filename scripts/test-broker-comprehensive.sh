#!/bin/bash
# Comprehensive Broker Test Script
# Test-First: Automate checks that catch our failure modes

set -e

BROKER_URL="${1:-https://broker-internal.seksbot.com}"
MASTER_KEY="${MASTER_KEY:-a3b17f17299bb3c29f445fdf7b10d2d97ee0b9a73649b6ae3c477fa8c27ff0be}"
MY_TOKEN="${MY_TOKEN:-seks_agent_ZnmHrsX8DDV7qY8LvHtjZigChTXr5NbK}"  # Síofra's token
OTHER_AGENT_SECRET="${OTHER_AGENT_SECRET:-ANNIE_GITHUB_PERSONAL_ACCESS_TOKEN}"  # Test isolation

echo "=== Comprehensive Broker Test ==="
echo "Broker: $BROKER_URL"
echo "Date: $(date)"
echo ""

failures=0
passed=0

test_case() {
    local name="$1"
    local command="$2"
    local expected_status="$3"
    
    echo -n "  $name... "
    
    eval "$command" > /tmp/test_output.$$ 2>&1
    local exit_code=$?
    
    if [ "$exit_code" -eq "$expected_status" ]; then
        echo "✓"
        passed=$((passed + 1))
        return 0
    else
        echo "✗ (exit $exit_code, expected $expected_status)"
        cat /tmp/test_output.$$ | sed 's/^/    /'
        failures=$((failures + 1))
        return 1
    fi
}

# 1. Core Health
echo "1. Core Health"
test_case "Health endpoint" "curl -s -f '$BROKER_URL/health' | grep -q '\"status\":\"ok\"'" 0

# 2. Broker Pages Exist
echo ""
echo "2. Broker Pages"
test_case "Login page" "curl -s -I '$BROKER_URL/login' | grep -q '200 OK'" 0
test_case "Secrets page" "curl -s -I '$BROKER_URL/secrets' | grep -q '200 OK\|30[0-9]'" 0  # May redirect to login

# 3. Agent Functionality
echo ""
echo "3. Agent Functionality"
test_case "List secrets" "curl -s -H 'Authorization: Bearer $MY_TOKEN' -H 'Content-Type: application/json' -d '{}' '$BROKER_URL/v1/secrets/list' | jq -e '. | length > 0'" 0
test_case "Get own secret" "curl -s -H 'Authorization: Bearer $MY_TOKEN' -H 'Content-Type: application/json' -d '{\"name\":\"SIOFRA_GITHUB_PERSONAL_ACCESS_TOKEN\"}' '$BROKER_URL/v1/secrets/get' | jq -e '.value'" 0

# 4. Agent Isolation (CRITICAL)
echo ""
echo "4. Agent Isolation"
test_case "Cannot access other agent's secret" "curl -s -H 'Authorization: Bearer $MY_TOKEN' -H 'Content-Type: application/json' -d '{\"name\":\"$OTHER_AGENT_SECRET\"}' '$BROKER_URL/v1/secrets/get' | jq -e '.error' 2>/dev/null || exit 1" 0
# Note: Currently FAILS because isolation is broken

# 5. Actuator Connectivity
echo ""
echo "5. Actuator Connectivity"
test_case "Exec whoami" "curl -s -H 'Authorization: Bearer $MY_TOKEN' -H 'Content-Type: application/json' -d '{\"capability\":\"exec\",\"payload\":{\"command\":\"whoami\",\"args\":[]},\"timeoutSeconds\":5}' '$BROKER_URL/v1/command' | jq -e '.result.stdout | contains(\"siofra_actuator\")'" 0

# 6. Critical Secrets Exist
echo ""
echo "6. Critical Secrets"
for secret in ANTHROPIC_TOKEN BRAVE_BASE_API_TOKEN CLOUDFLARE_API_TOKEN BOTSTERSORG_GITHUB_PERSONAL_ACCESS_TOKEN; do
    test_case "$secret exists" "curl -s -H 'Authorization: Bearer $MY_TOKEN' -H 'Content-Type: application/json' -d '{\"name\":\"$secret\"}' '$BROKER_URL/v1/secrets/get' | jq -e '.value'" 0
done

# 7. Admin Endpoints (with master key)
echo ""
echo "7. Admin Endpoints"
test_case "List accounts" "curl -s -H 'X-Admin-Key: $MASTER_KEY' '$BROKER_URL/api/accounts' | jq -e '. | length > 0'" 0

# Cleanup
rm -f /tmp/test_output.$$

echo ""
echo "=== Summary ==="
echo "Passed: $passed"
echo "Failed: $failures"
echo ""

if [ $failures -eq 0 ]; then
    echo "✅ All tests passed"
    exit 0
else
    echo "❌ $failures test(s) failed"
    echo ""
    echo "Known issues:"
    echo "1. Agent isolation likely broken (test 4 expected to fail)"
    echo "2. /secrets page may 404 (not deployed yet)"
    echo ""
    echo "Next: Fix issues, then re-run tests"
    exit 1
fi
