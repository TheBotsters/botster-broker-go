#!/bin/bash
# Comprehensive broker health check

set -e

ENV="${1:-prod}"
ALERT_ON_FAILURE="${2:-false}"

echo "=== Broker Health Check: $ENV ==="
echo "Time: $(date)"

case "$ENV" in
    prod)
        BROKER_URL="https://broker-internal.seksbot.com"
        PORT=8080
        ;;
    staging)
        BROKER_URL="http://localhost:8081"
        PORT=8081
        ;;
    prod2)
        BROKER_URL="http://localhost:8082"
        PORT=8082
        ;;
    *)
        echo "Unknown environment: $ENV"
        exit 1
        ;;
esac

FAILURES=0

# 1. Basic connectivity
echo -n "1. TCP port $PORT reachable... "
if nc -z localhost $PORT 2>/dev/null; then
    echo "✅"
else
    echo "❌"
    FAILURES=$((FAILURES + 1))
fi

# 2. HTTP health endpoint
echo -n "2. Health endpoint... "
HEALTH=$(curl -s "$BROKER_URL/health" 2>/dev/null || echo "{}")
STATUS=$(echo "$HEALTH" | jq -r '.status' 2>/dev/null || echo "error")
if [ "$STATUS" = "ok" ]; then
    echo "✅ ($STATUS)"
else
    echo "❌ ($STATUS)"
    FAILURES=$((FAILURES + 1))
fi

# 3. Web UI pages
echo -n "3. Login page... "
LOGIN_STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$BROKER_URL/login" 2>/dev/null || echo "000")
if [ "$LOGIN_STATUS" = "200" ] || [ "$LOGIN_STATUS" = "303" ]; then
    echo "✅ ($LOGIN_STATUS)"
else
    echo "❌ ($LOGIN_STATUS)"
    FAILURES=$((FAILURES + 1))
fi

echo -n "4. Secrets page... "
SECRETS_STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$BROKER_URL/secrets" 2>/dev/null || echo "000")
if [ "$SECRETS_STATUS" = "200" ] || [ "$SECRETS_STATUS" = "303" ] || [ "$SECRETS_STATUS" = "302" ]; then
    echo "✅ ($SECRETS_STATUS)"
else
    echo "⚠️ ($SECRETS_STATUS - may need deployment)"
    # Not a critical failure
fi

# 4. Process status
echo -n "5. Broker process... "
if pgrep -f "botster-broker" > /dev/null; then
    echo "✅"
else
    echo "❌"
    FAILURES=$((FAILURES + 1))
fi

# Summary
echo ""
echo "=== Summary ==="
if [ $FAILURES -eq 0 ]; then
    echo "✅ All health checks passed"
    exit 0
else
    echo "❌ $FAILURES critical check(s) failed"
    echo ""
    echo "Recommended actions:"
    echo "1. Check logs: sudo journalctl -u botster-broker --since '5 minutes ago'"
    echo "2. Restart: ./restart-broker.sh $ENV"
    echo "3. Deploy updates: ./deploy-broker.sh $ENV"
    
    if [ "$ALERT_ON_FAILURE" = "true" ]; then
        echo ""
        echo "⚠️  ALERT: Broker health check failed"
        # TODO: Send alert (email, notification, etc.)
    fi
    exit 1
fi
