package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/siofra-seksbot/botster-broker-go/internal/auth"
)

// SyncPeer represents a configured sync peer in the database.
type SyncPeer struct {
	ID               string
	Label            string
	TokenHash        string
	TransitKeyHex    string
	TransitKeyID     string
	AllowedResources string
	AllowedAccounts  sql.NullString
	LastSyncedAt     sql.NullString
	CreatedAt        string
}

// CreateSyncPeer creates a new sync peer record and returns the plaintext token.
func (db *DB) CreateSyncPeer(id, label, transitKeyHex, transitKeyID, allowedResources, allowedAccounts string) (string, error) {
	// Generate sync token
	token, err := auth.GenerateToken("seks_sync_" + id)
	if err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	tokenHash := auth.HashToken(token)

	now := time.Now().UTC().Format(time.RFC3339)
	
	var allowedAccountsPtr interface{}
	if allowedAccounts == "" {
		allowedAccountsPtr = nil
	} else {
		allowedAccountsPtr = allowedAccounts
	}

	_, err = db.Exec(`
		INSERT INTO sync_peers (id, label, token_hash, transit_key_hex, transit_key_id, allowed_resources, allowed_accounts, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, id, label, tokenHash, transitKeyHex, transitKeyID, allowedResources, allowedAccountsPtr, now)
	if err != nil {
		return "", fmt.Errorf("insert sync peer: %w", err)
	}

	return token, nil
}

// GetSyncPeerByToken looks up a sync peer by token hash.
func (db *DB) GetSyncPeerByToken(token string) (*SyncPeer, error) {
	tokenHash := auth.HashToken(token)
	
	peer := &SyncPeer{}
	err := db.QueryRow(`
		SELECT id, label, token_hash, transit_key_hex, transit_key_id, allowed_resources, allowed_accounts, last_synced_at, created_at
		FROM sync_peers WHERE token_hash = ?
	`, tokenHash).Scan(
		&peer.ID, &peer.Label, &peer.TokenHash, &peer.TransitKeyHex, &peer.TransitKeyID,
		&peer.AllowedResources, &peer.AllowedAccounts, &peer.LastSyncedAt, &peer.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query sync peer: %w", err)
	}
	return peer, nil
}

// GetSyncPeerByID looks up a sync peer by ID.
func (db *DB) GetSyncPeerByID(id string) (*SyncPeer, error) {
	peer := &SyncPeer{}
	err := db.QueryRow(`
		SELECT id, label, token_hash, transit_key_hex, transit_key_id, allowed_resources, allowed_accounts, last_synced_at, created_at
		FROM sync_peers WHERE id = ?
	`, id).Scan(
		&peer.ID, &peer.Label, &peer.TokenHash, &peer.TransitKeyHex, &peer.TransitKeyID,
		&peer.AllowedResources, &peer.AllowedAccounts, &peer.LastSyncedAt, &peer.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query sync peer by id: %w", err)
	}
	return peer, nil
}

// ListSyncPeers returns all configured sync peers.
func (db *DB) ListSyncPeers() ([]*SyncPeer, error) {
	rows, err := db.Query(`
		SELECT id, label, token_hash, transit_key_hex, transit_key_id, allowed_resources, allowed_accounts, last_synced_at, created_at
		FROM sync_peers ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("query sync peers: %w", err)
	}
	defer rows.Close()

	var peers []*SyncPeer
	for rows.Next() {
		peer := &SyncPeer{}
		if err := rows.Scan(
			&peer.ID, &peer.Label, &peer.TokenHash, &peer.TransitKeyHex, &peer.TransitKeyID,
			&peer.AllowedResources, &peer.AllowedAccounts, &peer.LastSyncedAt, &peer.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan sync peer: %w", err)
		}
		peers = append(peers, peer)
	}
	return peers, rows.Err()
}

// DeleteSyncPeer removes a sync peer by ID.
func (db *DB) DeleteSyncPeer(id string) error {
	_, err := db.Exec(`DELETE FROM sync_peers WHERE id = ?`, id)
	return err
}

// RotateSyncPeerToken generates a new token for an existing sync peer.
func (db *DB) RotateSyncPeerToken(id string) (string, error) {
	// Generate new token
	token, err := auth.GenerateToken("seks_sync_" + id)
	if err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	tokenHash := auth.HashToken(token)

	_, err = db.Exec(`UPDATE sync_peers SET token_hash = ? WHERE id = ?`, tokenHash, id)
	if err != nil {
		return "", fmt.Errorf("update sync peer token: %w", err)
	}

	return token, nil
}

// UpdateSyncPeerLastSynced updates the last_synced_at timestamp for a peer.
func (db *DB) UpdateSyncPeerLastSynced(id string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`UPDATE sync_peers SET last_synced_at = ? WHERE id = ?`, now, id)
	return err
}

// IsAccountAllowed checks if a peer is allowed to access a specific account.
func (p *SyncPeer) IsAccountAllowed(accountID string) bool {
	if !p.AllowedAccounts.Valid || p.AllowedAccounts.String == "" {
		return true // NULL or empty means all accounts allowed
	}
	
	allowed := strings.Split(p.AllowedAccounts.String, ",")
	for _, allowedID := range allowed {
		if strings.TrimSpace(allowedID) == accountID {
			return true
		}
	}
	return false
}

// IsResourceAllowed checks if a peer is allowed to access a specific resource type.
func (p *SyncPeer) IsResourceAllowed(resource string) bool {
	allowed := strings.Split(p.AllowedResources, ",")
	for _, allowedResource := range allowed {
		if strings.TrimSpace(allowedResource) == resource {
			return true
		}
	}
	return false
}