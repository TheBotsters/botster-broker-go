#!/bin/bash
# Deploy broker from git repo to environment

set -e

ENV="${1:-staging}"
TARGET="${2:-main}"
REPO_DIR="/home/siofra_actuator/dev/botster-broker-go"
LOG_FILE="/tmp/broker-deploy-$ENV-$(date +%Y%m%d-%H%M%S).log"

echo "=== Deploying broker to $ENV ===" | tee "$LOG_FILE"
echo "Target: $TARGET" | tee -a "$LOG_FILE"
echo "Date: $(date)" | tee -a "$LOG_FILE"

# Environment configurations
case "$ENV" in
    prod)
        PORT=8080
        DB_PATH="/var/lib/botster-broker/broker.db"
        SERVICE_NAME="botster-broker"
        ;;
    staging)
        PORT=8081
        DB_PATH="/tmp/broker-staging.db"
        SERVICE_NAME=""  # No systemd service for staging
        ;;
    prod2)
        PORT=8082
        DB_PATH="/var/lib/botster-broker/broker2.db"
        SERVICE_NAME="botster-broker2"
        ;;
    *)
        echo "Unknown environment: $ENV" | tee -a "$LOG_FILE"
        echo "Valid: prod, staging, prod2" | tee -a "$LOG_FILE"
        exit 1
        ;;
esac

# 1. Fetch and checkout
echo "1. Checking out $TARGET..." | tee -a "$LOG_FILE"
cd "$REPO_DIR"
git fetch origin 2>&1 | tee -a "$LOG_FILE"
git checkout "$TARGET" 2>&1 | tee -a "$LOG_FILE"
git pull origin "$TARGET" 2>&1 | tee -a "$LOG_FILE"

# 2. Build
echo "2. Building broker..." | tee -a "$LOG_FILE"
go build -o broker ./cmd/broker 2>&1 | tee -a "$LOG_FILE"
if [ $? -ne 0 ]; then
    echo "Build failed" | tee -a "$LOG_FILE"
    exit 1
fi

# 3. Stop existing service
if [ -n "$SERVICE_NAME" ]; then
    echo "3. Stopping service $SERVICE_NAME..." | tee -a "$LOG_FILE"
    sudo systemctl stop "$SERVICE_NAME" 2>&1 | tee -a "$LOG_FILE" || true
else
    echo "3. Killing existing staging broker..." | tee -a "$LOG_FILE"
    pkill -f "broker.*$PORT" 2>&1 | tee -a "$LOG_FILE" || true
    sleep 2
fi

# 4. Deploy binary
if [ "$ENV" = "prod" ]; then
    echo "4. Deploying to /usr/local/bin/botster-broker..." | tee -a "$LOG_FILE"
    sudo cp broker /usr/local/bin/botster-broker 2>&1 | tee -a "$LOG_FILE"
elif [ "$ENV" = "prod2" ]; then
    echo "4. Deploying to /usr/local/bin/botster-broker2..." | tee -a "$LOG_FILE"
    sudo cp broker /usr/local/bin/botster-broker2 2>&1 | tee -a "$LOG_FILE"
fi

# 5. Start broker
echo "5. Starting broker on port $PORT..." | tee -a "$LOG_FILE"
if [ -n "$SERVICE_NAME" ]; then
    sudo systemctl start "$SERVICE_NAME" 2>&1 | tee -a "$LOG_FILE"
else
    # Start staging directly
    MASTER_KEY=$(sudo cat /etc/botster-broker/env 2>/dev/null | grep MASTER_KEY | cut -d= -f2)
    if [ -z "$MASTER_KEY" ]; then
        MASTER_KEY="deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
    fi
    PORT=$PORT MASTER_KEY=$MASTER_KEY DB_PATH=$DB_PATH ./broker 2>&1 | tee -a "$LOG_FILE" &
    BROKER_PID=$!
    echo $BROKER_PID > "/tmp/broker-$ENV.pid"
fi

# 6. Wait for health
echo "6. Waiting for broker health..." | tee -a "$LOG_FILE"
for i in {1..10}; do
    if curl -s "http://localhost:$PORT/health" > /dev/null 2>&1; then
        echo "Broker healthy on port $PORT" | tee -a "$LOG_FILE"
        break
    fi
    sleep 1
    if [ $i -eq 10 ]; then
        echo "Broker failed to start" | tee -a "$LOG_FILE"
        exit 1
    fi
done

echo "=== Deployment complete ===" | tee -a "$LOG_FILE"
echo "Broker running on port $PORT" | tee -a "$LOG_FILE"
echo "Log: $LOG_FILE" | tee -a "$LOG_FILE"
