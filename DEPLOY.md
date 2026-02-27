# Deployment

## Build

```bash
/usr/local/go/bin/go build -o botster-broker ./cmd/broker
```

## Systemd

```bash
# Copy binary
sudo cp botster-broker /usr/local/bin/botster-broker

# Copy service file
sudo cp botster-broker.service /etc/systemd/system/

# Set secrets (do not use the defaults in the service file)
sudo mkdir -p /etc/botster-broker
sudo tee /etc/botster-broker/env << 'ENVEOF'
BROKER_MASTER_KEY=<generate with: openssl rand -hex 32>
BROKER_PORT=8080
BROKER_DB_PATH=/var/lib/botster-broker/broker.db
ENVEOF
sudo chmod 600 /etc/botster-broker/env

# Edit service file to uncomment EnvironmentFile line
sudo systemctl daemon-reload
sudo systemctl enable --now botster-broker
sudo systemctl status botster-broker
```

## Caddy (TLS)

Add to `/etc/caddy/Caddyfile`:

```
broker.yourdomain.com {
    reverse_proxy localhost:8080
}
```

## Notes

- Two TODOs resolved (2026-02-27):
  - `handleActuatorsList` now scoped to authenticated account
  - Hub command routing now tracks origin agent — results go to the correct brain only, no broadcast
- `botster-broker.service` has `EnvironmentFile` commented out — uncomment and point to `/etc/botster-broker/env` before deploying
