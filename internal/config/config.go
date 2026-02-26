// Package config handles broker configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds all broker configuration.
type Config struct {
	Port      int
	DBPath    string
	MasterKey string // 64-char hex string for AES-256-GCM
	AdminKey  string // Optional admin API key
}

// Load reads configuration from environment variables with sensible defaults.
func Load() (*Config, error) {
	c := &Config{
		Port:      8787,
		DBPath:    "data/broker.db",
		MasterKey: os.Getenv("MASTER_KEY"),
		AdminKey:  os.Getenv("ADMIN_KEY"),
	}

	if p := os.Getenv("PORT"); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid PORT %q: %w", p, err)
		}
		c.Port = port
	}

	if d := os.Getenv("DB_PATH"); d != "" {
		c.DBPath = d
	}

	if c.MasterKey == "" {
		return nil, fmt.Errorf("MASTER_KEY environment variable is required (64-char hex string)")
	}

	if len(c.MasterKey) != 64 {
		return nil, fmt.Errorf("MASTER_KEY must be exactly 64 hex characters (got %d)", len(c.MasterKey))
	}

	return c, nil
}
