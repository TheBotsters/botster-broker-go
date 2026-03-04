#!/bin/bash
# Restart broker with health checks

set -e

ENV="${1:-prod}"
LOG_FILE="/tmp/broker-restart-$ENV-$(date +%Y%m%d-%H%M%S).log"

echo "=== Restarting broker: $ENV ===" | tee "$LOG_FILE"

case "$ENV" in
    prod)
        SERVICE_NAME="botster-broker"
        PORT=8080
        ;;
    staging)
        SERVICE_NAME=""
        PORT=8081
        ;;
    prod2)
        SERVICE_NAME="botster-broker2"
        PORT=8082
        ;;
    *)
        echo "Unknown environment: $ENV" | tee -a "$LOG_FILE"
        exit 1
        ;;
esac

# 1. Stop
echo "1. Stopping broker..." | tee -a "$LOG_FILE"
if [ -n "$SERVICE_NAME" ]; then
    sudo systemctl stop "$SERVICE_NAME" 2>&1 | tee -a "$LOG_FILE" || true
else
    pkill -f "broker.*$PORT" 2>&1 | tee -a "$LOG_FILE" || true
fi
sleep 2

# 2. Verify stopped
if pgrep -f "broker.*$PORT" > /dev/null; then
    echo "Force killing..." | tee -a "$LOG_FILE"
    pkill -9 -f "broker.*$PORT" 2>&1 | tee -a "$LOG_FILE" || true
    sleep 1
fi

# 3. Start
echo "2. Starting broker..." | tee -a "$LOG_FILE"
if [ -n "$SERVICE_NAME" ]; then
    sudo systemctl start "$SERVICE_NAME" 2>&1 | tee -a "$LOG_FILE"
else
    # Start staging
    cd /home/siofra_actuator/dev/botster-broker-go
    MASTER_KEY=$(sudo cat /etc/botster-broker/env 2>/dev/null | grep MASTER_KEY | cut -d= -f2)
    if [ -z "$MASTER_KEY" ]; then
        MASTER_KEY="deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
    fi
    PORT=$PORT MASTER_KEY=$MASTER_KEY DB_PATH="/tmp/broker-$ENV.db" ./broker 2>&1 | tee -a "$LOG_FILE" &
    sleep 2
fi

# 4. Health check
echo "3. Health check..." | tee -a "$LOG_FILE"
for i in {1..10}; do
    if curl -s "http://localhost:$PORT/health" > /dev/null 2>&1; then
        echo "✅ Broker healthy on port $PORT" | tee -a "$LOG_FILE"
        break
    fi
    sleep 1
    if [ $i -eq 10 ]; then
        echo "❌ Broker failed to start" | tee -a "$LOG_FILE"
        echo "Check logs: $LOG_FILE" | tee -a "$LOG_FILE"
        exit 1
    fi
done

echo "=== Restart complete ===" | tee -a "$LOG_FILE"
echo "Log: $LOG_FILE" | tee -a "$LOG_FILE"
