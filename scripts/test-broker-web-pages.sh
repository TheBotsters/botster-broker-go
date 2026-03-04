#!/bin/bash
# Test Broker Web Pages
# Verify all expected web UI pages exist

set -e

BROKER_URL="${1:-https://broker-internal.seksbot.com}"

echo "=== Broker Web Pages Test ==="
echo ""

pages=(
    "/"
    "/login"
    "/secrets"
    "/chat"
    "/dashboard"
)

for page in "${pages[@]}"; do
    echo -n "GET $page... "
    status=$(curl -s -I -w "%{http_code}" -o /dev/null "$BROKER_URL$page")
    
    case $status in
        200)
            echo "✓ 200 OK"
            ;;
        30[0-9])
            location=$(curl -s -I "$BROKER_URL$page" | grep -i "location:" | cut -d' ' -f2- | tr -d '\r')
            echo "↪ $status → $location"
            ;;
        404)
            echo "✗ 404 Not Found"
            ;;
        50*)
            echo "✗ $status Server Error"
            ;;
        *)
            echo "? $status"
            ;;
    esac
done

echo ""
echo "Note: Some pages may redirect to /login (expected for authenticated pages)"
echo "Critical pages: /login (must work), /secrets (should work after deployment)"
