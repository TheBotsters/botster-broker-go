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

	"github.com/siofra-seksbot/botster-broker-go/internal/api"
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

	// Create API server
	srv := &api.Server{
		DB:        database,
		MasterKey: cfg.MasterKey,
	}

	// Build router
	router := srv.NewRouter()

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
