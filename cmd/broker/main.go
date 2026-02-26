// Botster Broker — Go clean-room reimplementation
//
// Built by Síofra on the VPS actuator.
// The broker that builds itself.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/siofra-seksbot/botster-broker-go/internal/config"
	"github.com/siofra-seksbot/botster-broker-go/internal/db"
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

	// HTTP server — serve static files from web/ for now
	mux := http.NewServeMux()

	// Static files (the dashboard)
	webDir := "web"
	if _, err := os.Stat(webDir); err == nil {
		mux.Handle("/", http.FileServer(http.Dir(webDir)))
	}

	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","schema_version":%d}`, version)
	})

	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("Listening on %s", addr)

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
