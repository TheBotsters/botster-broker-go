// Package config handles broker configuration from environment variables.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// SyncPeerConfig holds configuration for a sync peer (receiver side).
type SyncPeerConfig struct {
	PeerID       string            `json:"peer_id"`
	Label        string            `json:"label"`
	SourceURL    string            `json:"source_url"`
	SyncToken    string            `json:"sync_token"`
	TransitKeyID string            `json:"transit_key_id"`
	TransitKey   string            `json:"transit_key"`   // 64-char hex
	AccountMap   map[string]string `json:"account_map"`   // source_id → local_id
}

// Config holds all broker configuration.
type Config struct {
	Port      int
	DBPath    string
	MasterKey string // 64-char hex string for AES-256-GCM
	AdminKey  string // Optional admin API key
	SyncPeers []SyncPeerConfig
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

	// Load sync peers from SYNC_PEERS environment variable
	if raw := os.Getenv("SYNC_PEERS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &c.SyncPeers); err != nil {
			return nil, fmt.Errorf("parse SYNC_PEERS: %w", err)
		}
		// Validate each sync peer config
		for i, peer := range c.SyncPeers {
			if peer.PeerID == "" {
				return nil, fmt.Errorf("SYNC_PEERS[%d]: peer_id is required", i)
			}
			if peer.SourceURL == "" {
				return nil, fmt.Errorf("SYNC_PEERS[%d]: source_url is required", i)
			}
			if peer.SyncToken == "" {
				return nil, fmt.Errorf("SYNC_PEERS[%d]: sync_token is required", i)
			}
			if peer.TransitKey == "" {
				return nil, fmt.Errorf("SYNC_PEERS[%d]: transit_key is required", i)
			}
			if len(peer.TransitKey) != 64 {
				return nil, fmt.Errorf("SYNC_PEERS[%d]: transit_key must be 64 hex characters (got %d)", i, len(peer.TransitKey))
			}
			if peer.TransitKeyID == "" {
				return nil, fmt.Errorf("SYNC_PEERS[%d]: transit_key_id is required", i)
			}
		}
	}

	return c, nil
}

// GetSyncPeer returns the sync peer configuration for the given peer ID.
func (c *Config) GetSyncPeer(peerID string) *SyncPeerConfig {
	for _, peer := range c.SyncPeers {
		if peer.PeerID == peerID {
			return &peer
		}
	}
	return nil
}
