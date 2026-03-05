// Package sync implements broker-to-broker synchronization.
package sync

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/siofra-seksbot/botster-broker-go/internal/config"
	"github.com/siofra-seksbot/botster-broker-go/internal/db"
)

// SyncResult represents the result of a sync operation.
type SyncResult struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Errors   []string `json:"errors"`
	Items    []SyncItemResult `json:"items"`
}

// SyncItemResult represents the result for a single item.
type SyncItemResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Action string `json:"action"` // "created", "updated", "skipped", "error"
	Error  string `json:"error,omitempty"`
}

// ManifestResponse represents the response from /sync/v1/manifest endpoint.
type ManifestResponse struct {
	Resource         string           `json:"resource"`
	SourceAccountID  string           `json:"source_account_id"`
	GeneratedAt      string           `json:"generated_at"`
	Items            []ManifestItem   `json:"items"`
}

// ManifestItem represents a single item in the manifest.
type ManifestItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	UpdatedAt string `json:"updated_at"`
	Checksum  string `json:"checksum"`
}

// ExportRequest represents the request to /sync/v1/export endpoint.
type ExportRequest struct {
	Resource        string   `json:"resource"`
	SourceAccountID string   `json:"source_account_id"`
	ItemIDs         []string `json:"item_ids"`
	TransitKeyID    string   `json:"transit_key_id"`
}

// ExportResponse represents the response from /sync/v1/export endpoint.
type ExportResponse struct {
	Resource        string          `json:"resource"`
	SourceAccountID string          `json:"source_account_id"`
	SourceBrokerID  string          `json:"source_broker_id"`
	TransitKeyID    string          `json:"transit_key_id"`
	ExportedAt      string          `json:"exported_at"`
	SchemaVersion   int             `json:"schema_version"`
	Items           []ExportItem    `json:"items"`
}

// ExportItem represents a single exported item.
type ExportItem struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	Provider              string `json:"provider"`
	TransitEncryptedValue string `json:"transit_encrypted_value"`
	Metadata              string `json:"metadata"`
	UpdatedAt             string `json:"updated_at"`
}

// SyncFromPeer synchronizes resources from a peer broker.
// This is the internal sync function that can be called from command handler.
func SyncFromPeer(database *db.DB, cfg *config.Config, masterKey string, peerID, resource string, itemIDs []string) (*SyncResult, error) {
	// Find peer config
	peerConfig := cfg.GetSyncPeer(peerID)
	if peerConfig == nil {
		return nil, fmt.Errorf("peer %q not found in SYNC_PEERS config", peerID)
	}

	// For now, only support secrets resource
	if resource != "secrets" {
		return nil, fmt.Errorf("only 'secrets' resource is supported, got %q", resource)
	}

	// We need a source account ID to sync from. For simplicity, we'll use the first key in AccountMap.
	// In a real implementation, this would be a parameter.
	if len(peerConfig.AccountMap) == 0 {
		return nil, fmt.Errorf("no account mapping configured for peer %q", peerID)
	}
	
	var sourceAccountID, targetAccountID string
	for src, dst := range peerConfig.AccountMap {
		sourceAccountID = src
		targetAccountID = dst
		break // Use first mapping for now
	}

	// Get manifest from source
	manifest, err := fetchManifest(peerConfig, resource, sourceAccountID)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}

	// Filter items if specific IDs requested
	var itemsToExport []ManifestItem
	if len(itemIDs) == 0 {
		// Export all items
		itemsToExport = manifest.Items
	} else {
		// Filter to requested IDs
		itemMap := make(map[string]ManifestItem)
		for _, item := range manifest.Items {
			itemMap[item.ID] = item
		}
		for _, id := range itemIDs {
			if item, ok := itemMap[id]; ok {
				itemsToExport = append(itemsToExport, item)
			}
		}
	}

	if len(itemsToExport) == 0 {
		return &SyncResult{
			Imported: 0,
			Skipped:  0,
			Errors:   []string{"no items to sync"},
			Items:    []SyncItemResult{},
		}, nil
	}

	// Prepare item IDs for export
	exportItemIDs := make([]string, len(itemsToExport))
	for i, item := range itemsToExport {
		exportItemIDs[i] = item.ID
	}

	// Fetch export from source
	export, err := fetchExport(peerConfig, resource, sourceAccountID, exportItemIDs)
	if err != nil {
		return nil, fmt.Errorf("fetch export: %w", err)
	}

	// Import secrets into local DB
	result := &SyncResult{
		Items: make([]SyncItemResult, 0, len(export.Items)),
	}

	for _, item := range export.Items {
		itemResult := SyncItemResult{
			ID:   item.ID,
			Name: item.Name,
		}

		// Import secret (decrypt with transit key, re-encrypt with master key)
		encryptedValue, err := database.ImportSecret(item.TransitEncryptedValue, peerConfig.TransitKey, masterKey)
		if err != nil {
			itemResult.Action = "error"
			itemResult.Error = err.Error()
			result.Errors = append(result.Errors, fmt.Sprintf("item %s (%s): %v", item.ID, item.Name, err))
			result.Items = append(result.Items, itemResult)
			continue
		}

		// Create or update secret
		_, created, err := database.CreateOrUpdateSecret(targetAccountID, item.Name, item.Provider, string(encryptedValue), masterKey)
		if err != nil {
			itemResult.Action = "error"
			itemResult.Error = err.Error()
			result.Errors = append(result.Errors, fmt.Sprintf("item %s (%s): %v", item.ID, item.Name, err))
		} else {
			if created {
				itemResult.Action = "created"
			} else {
				itemResult.Action = "updated"
			}
			result.Imported++
		}

		result.Items = append(result.Items, itemResult)
	}

	// Log audit entry
	detail := fmt.Sprintf("sync from peer %s: imported %d, skipped %d, errors %d", 
		peerID, result.Imported, result.Skipped, len(result.Errors))
	database.LogAudit(&targetAccountID, nil, nil, "sync.import", detail)

	return result, nil
}

// fetchManifest fetches the manifest from a source broker.
func fetchManifest(peer *config.SyncPeerConfig, resource, sourceAccountID string) (*ManifestResponse, error) {
	url := fmt.Sprintf("%s/sync/v1/manifest?resource=%s&account_id=%s", 
		peer.SourceURL, resource, sourceAccountID)
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+peer.SyncToken)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var manifest ManifestResponse
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &manifest, nil
}

// fetchExport fetches exported items from a source broker.
func fetchExport(peer *config.SyncPeerConfig, resource, sourceAccountID string, itemIDs []string) (*ExportResponse, error) {
	url := fmt.Sprintf("%s/sync/v1/export", peer.SourceURL)
	
	exportReq := ExportRequest{
		Resource:        resource,
		SourceAccountID: sourceAccountID,
		ItemIDs:         itemIDs,
		TransitKeyID:    peer.TransitKeyID,
	}

	body, err := json.Marshal(exportReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+peer.SyncToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var export ExportResponse
	if err := json.NewDecoder(resp.Body).Decode(&export); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &export, nil
}