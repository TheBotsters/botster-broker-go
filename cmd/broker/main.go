// Botster Broker — Go clean-room reimplementation
//
// Built by Síofra on the VPS actuator.
// The broker that builds itself.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/TheBotsters/botster-broker-go/internal/api"
	"github.com/TheBotsters/botster-broker-go/internal/config"
	"github.com/TheBotsters/botster-broker-go/internal/db"
	"github.com/TheBotsters/botster-broker-go/internal/hub"
	"github.com/TheBotsters/botster-broker-go/internal/tap"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	log.Println("Botster Broker (Go) starting...")

	// Load config
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	// Open database
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("Database error: %v", err)
	}
	defer database.Close()

	version, _ := database.SchemaVersion()
	log.Printf("Schema version: %d", version)

	// Prune old audit log entries based on retention_months setting
	retentionStr, _ := database.GetSetting("retention_months")
	retentionMonths := 6
	if retentionStr != "" {
		if n, err := strconv.Atoi(retentionStr); err == nil && n > 0 {
			retentionMonths = n
		}
	}
	if pruned, err := database.PruneAuditLog(retentionMonths); err != nil {
		log.Printf("Audit log prune error: %v", err)
	} else if pruned > 0 {
		log.Printf("Pruned %d audit log entries older than %d months", pruned, retentionMonths)
	}

	// Create WebSocket hub
	wsHub := hub.New(database, cfg.MasterKey)
	go wsHub.Run()
	log.Println("WebSocket hub started")

	// If PROXY_SOCKET is set, connect to ws-proxy via Unix socket link
	// instead of handling WebSocket connections directly (two-process mode).
	proxySocket := os.Getenv("PROXY_SOCKET")
	if proxySocket != "" {
		linkClient := hub.NewLinkClient(wsHub)
		go linkClient.ConnectWithRetry(proxySocket)
		log.Printf("Link mode: connecting to ws-proxy at %s", proxySocket)
	}

	// Create inference tap (pub/sub for dashboard SSE)
	inferenceTap := tap.New()
	log.Println("Inference tap initialized")

	// Initialize session store (24h TTL)
	sessions := api.NewSessionStore(24 * time.Hour)

	// Load gateway configs from BROKER_GATEWAYS env or file
	gateways := loadGateways()

	// Create API server
	srv := &api.Server{
		DB:        database,
		MasterKey: cfg.MasterKey,
		Hub:       wsHub,
		Tap:       inferenceTap,
		Sessions:  sessions,
		Gateways:  gateways,
		Config:    cfg,
	}

	// Build router
	router := srv.NewRouter()

	// Broker-owned in-memory secret store (temporary bootstrap values)
	brokerSecrets := hub.NewSecretsStore(map[string]string{
		"openai:embedding":  "sk-embedding-test-key",
		"openai:chat":       "sk-chat-test-key",
		"anthropic:default": "sk-ant-test-key",
	})
	brokerToken := cfg.AdminKey
	if brokerToken == "" {
		// Fallback for local/dev where only MASTER_KEY is set
		brokerToken = cfg.MasterKey
	}
	router.With(wsHub.RequireBrokerToken(brokerToken)).Post("/v1/broker/secrets/get", wsHub.HandleSecretGet(brokerSecrets))

	// WebSocket endpoint
	router.HandleFunc("/ws", wsHub.HandleWebSocket)

	// Serve static files from web/ if it exists
	webDir := "web"
	if _, err := os.Stat(webDir); err == nil {
		router.Handle("/*", http.FileServer(http.Dir(webDir)))
	}

	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("Listening on %s", addr)

	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// loadGateways reads gateway configs from BROKER_GATEWAYS env var (JSON)
// or /etc/botster-broker/gateways.json file.
// Format: {"nira": {"url": "ws://127.0.0.1:18786", "token": "..."}, ...}
func loadGateways() map[string]api.GatewayConfig {
	gateways := make(map[string]api.GatewayConfig)

	// Try env var first
	if raw := os.Getenv("BROKER_GATEWAYS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &gateways); err != nil {
			log.Printf("Warning: BROKER_GATEWAYS parse error: %v", err)
		} else {
			log.Printf("Loaded %d gateway configs from env", len(gateways))
			return gateways
		}
	}

	// Try config file
	for _, path := range []string{"/etc/botster-broker/gateways.json", "gateways.json"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if err := json.Unmarshal(data, &gateways); err != nil {
			log.Printf("Warning: %s parse error: %v", path, err)
			continue
		}
		log.Printf("Loaded %d gateway configs from %s", len(gateways), path)
		return gateways
	}

	log.Println("No gateway configs found (chat proxy disabled)")
	return gateways
}
