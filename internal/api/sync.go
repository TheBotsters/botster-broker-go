package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/siofra-seksbot/botster-broker-go/internal/auth"
	"github.com/siofra-seksbot/botster-broker-go/internal/config"
	"github.com/siofra-seksbot/botster-broker-go/internal/db"
	syncpkg "github.com/siofra-seksbot/botster-broker-go/internal/sync"
)

// SyncImportRequest represents the request to import sync data from a peer.
type SyncImportRequest struct {
	PeerID          string   `json:"peer_id"`
	Resource        string   `json:"resource"`
	SourceAccountID string   `json:"source_account_id"`
	TargetAccountID string   `json:"target_account_id"`
	ItemIDs         []string `json:"item_ids"`
	DryRun          bool     `json:"dry_run"`
}

// SyncImportResponse represents the response from import sync.
type SyncImportResponse struct {
	OK      bool                     `json:"ok"`
	DryRun  bool                     `json:"dry_run"`
	Imported int                     `json:"imported"`
	Skipped  int                     `json:"skipped"`
	Errors  []string                 `json:"errors"`
	Items   []syncpkg.SyncItemResult `json:"items"`
}

// handleSyncImport handles POST /sync/v1/import
func (s *Server) handleSyncImport(w http.ResponseWriter, r *http.Request) {
	// Only root/admin can trigger sync
	if !s.requireRoot(r) {
		jsonError(w, http.StatusForbidden, "root access required")
		return
	}

	var req SyncImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	if req.PeerID == "" {
		jsonError(w, http.StatusBadRequest, "peer_id is required")
		return
	}
	if req.Resource == "" {
		jsonError(w, http.StatusBadRequest, "resource is required")
		return
	}
	if req.SourceAccountID == "" {
		jsonError(w, http.StatusBadRequest, "source_account_id is required")
		return
	}
	if req.TargetAccountID == "" {
		jsonError(w, http.StatusBadRequest, "target_account_id is required")
		return
	}

	// For now, we'll implement a simple version that uses the sync module
	// In a full implementation, we would need to handle account mapping differently
	// since the sync module expects AccountMap in config
	
	// Create a temporary config with the requested account mapping
	tempConfig := *s.Config
	tempConfig.SyncPeers = make([]config.SyncPeerConfig, len(s.Config.SyncPeers))
	copy(tempConfig.SyncPeers, s.Config.SyncPeers)
	
	// Find and update the peer config with the requested account mapping
	peerFound := false
	for i, peer := range tempConfig.SyncPeers {
		if peer.PeerID == req.PeerID {
			// Update account map for this request
			if peer.AccountMap == nil {
				peer.AccountMap = make(map[string]string)
			}
			peer.AccountMap[req.SourceAccountID] = req.TargetAccountID
			tempConfig.SyncPeers[i] = peer
			peerFound = true
			break
		}
	}
	
	if !peerFound {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("peer %q not found in SYNC_PEERS config", req.PeerID))
		return
	}

	// Call sync function
	result, err := syncpkg.SyncFromPeer(s.DB, &tempConfig, s.MasterKey, req.PeerID, req.Resource, req.ItemIDs)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("sync failed: %v", err))
		return
	}

	response := SyncImportResponse{
		OK:      len(result.Errors) == 0,
		DryRun:  req.DryRun,
		Imported: result.Imported,
		Skipped:  result.Skipped,
		Errors:  result.Errors,
		Items:   result.Items,
	}

	jsonResponse(w, http.StatusOK, response)
}

// handleSyncListPeers handles GET /sync/v1/peers (source-side, root-only)
func (s *Server) handleSyncListPeers(w http.ResponseWriter, r *http.Request) {
	// Only root/admin can list sync peers
	if !s.requireRoot(r) {
		jsonError(w, http.StatusForbidden, "root access required")
		return
	}

	peers, err := s.DB.ListSyncPeers()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("failed to list peers: %v", err))
		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{"peers": peers})
}

// handleCreateSyncPeer handles POST /api/sync/peers (source-side, root-only)
func (s *Server) handleCreateSyncPeer(w http.ResponseWriter, r *http.Request) {
	// Only root/admin can create sync peers
	if !s.requireRoot(r) {
		jsonError(w, http.StatusForbidden, "root access required")
		return
	}

	var req struct {
		ID               string `json:"id"`
		Label            string `json:"label"`
		TransitKeyHex    string `json:"transit_key_hex"`
		TransitKeyID     string `json:"transit_key_id"`
		AllowedResources string `json:"allowed_resources"`
		AllowedAccounts  string `json:"allowed_accounts"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	if req.ID == "" {
		jsonError(w, http.StatusBadRequest, "id is required")
		return
	}
	if req.Label == "" {
		jsonError(w, http.StatusBadRequest, "label is required")
		return
	}
	if req.TransitKeyHex == "" {
		jsonError(w, http.StatusBadRequest, "transit_key_hex is required")
		return
	}
	if len(req.TransitKeyHex) != 64 {
		jsonError(w, http.StatusBadRequest, "transit_key_hex must be 64 hex characters")
		return
	}
	if req.TransitKeyID == "" {
		jsonError(w, http.StatusBadRequest, "transit_key_id is required")
		return
	}
	if req.AllowedResources == "" {
		req.AllowedResources = "secrets"
	}

	token, err := s.DB.CreateSyncPeer(req.ID, req.Label, req.TransitKeyHex, req.TransitKeyID, req.AllowedResources, req.AllowedAccounts)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create sync peer: %v", err))
		return
	}

	jsonResponse(w, http.StatusCreated, map[string]interface{}{
		"id":    req.ID,
		"token": token,
	})
}

// handleDeleteSyncPeer handles DELETE /api/sync/peers/{id} (source-side, root-only)
func (s *Server) handleDeleteSyncPeer(w http.ResponseWriter, r *http.Request) {
	// Only root/admin can delete sync peers
	if !s.requireRoot(r) {
		jsonError(w, http.StatusForbidden, "root access required")
		return
	}

	id := chi.URLParam(r, "id")
	if id == "" {
		jsonError(w, http.StatusBadRequest, "id is required")
		return
	}

	if err := s.DB.DeleteSyncPeer(id); err != nil {
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("failed to delete sync peer: %v", err))
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleRotateSyncPeerToken handles POST /api/sync/peers/{id}/rotate (source-side, root-only)
func (s *Server) handleRotateSyncPeerToken(w http.ResponseWriter, r *http.Request) {
	// Only root/admin can rotate sync peer tokens
	if !s.requireRoot(r) {
		jsonError(w, http.StatusForbidden, "root access required")
		return
	}

	id := chi.URLParam(r, "id")
	if id == "" {
		jsonError(w, http.StatusBadRequest, "id is required")
		return
	}

	token, err := s.DB.RotateSyncPeerToken(id)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("failed to rotate sync peer token: %v", err))
		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"id":    id,
		"token": token,
	})
}

// handleSyncManifest handles GET /sync/v1/manifest (source-side, sync token auth)
func (s *Server) handleSyncManifest(w http.ResponseWriter, r *http.Request) {
	// Authenticate via sync token
	peer, err := s.authenticateSyncToken(r)
	if err != nil {
		jsonError(w, http.StatusUnauthorized, "invalid sync token")
		return
	}

	// Get query parameters
	resource := r.URL.Query().Get("resource")
	accountID := r.URL.Query().Get("account_id")

	if resource == "" {
		jsonError(w, http.StatusBadRequest, "resource parameter is required")
		return
	}
	if accountID == "" {
		jsonError(w, http.StatusBadRequest, "account_id parameter is required")
		return
	}

	// Check if peer is allowed to access this resource and account
	if !peer.IsResourceAllowed(resource) {
		jsonError(w, http.StatusForbidden, "peer not allowed to access this resource")
		return
	}
	if !peer.IsAccountAllowed(accountID) {
		jsonError(w, http.StatusForbidden, "peer not allowed to access this account")
		return
	}

	// For now, only support secrets resource
	if resource != "secrets" {
		jsonError(w, http.StatusBadRequest, "only 'secrets' resource is supported")
		return
	}

	// Get secrets for the account
	secrets, err := s.DB.ListSecrets(accountID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("failed to list secrets: %v", err))
		return
	}

	// Build manifest items
	items := make([]syncpkg.ManifestItem, 0, len(secrets))
	for _, secret := range secrets {
		checksum, err := s.DB.ChecksumSecret(secret, s.MasterKey)
		if err != nil {
			// Log but continue with other items
			s.DB.LogAudit(&accountID, nil, nil, "sync.manifest.error", 
				fmt.Sprintf("failed to compute checksum for secret %s: %v", secret.ID, err))
			continue
		}

		items = append(items, syncpkg.ManifestItem{
			ID:        secret.ID,
			Name:      secret.Name,
			Provider:  secret.Provider,
			UpdatedAt: secret.UpdatedAt,
			Checksum:  checksum,
		})
	}

	manifest := syncpkg.ManifestResponse{
		Resource:        resource,
		SourceAccountID: accountID,
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
		Items:           items,
	}

	// Log audit entry
	s.DB.LogAudit(&accountID, nil, nil, "sync.manifest", 
		fmt.Sprintf("manifest generated for peer %s: %d items", peer.ID, len(items)))

	jsonResponse(w, http.StatusOK, manifest)
}

// handleSyncExport handles POST /sync/v1/export (source-side, sync token auth)
func (s *Server) handleSyncExport(w http.ResponseWriter, r *http.Request) {
	// Authenticate via sync token
	peer, err := s.authenticateSyncToken(r)
	if err != nil {
		jsonError(w, http.StatusUnauthorized, "invalid sync token")
		return
	}

	var req syncpkg.ExportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	if req.Resource == "" {
		jsonError(w, http.StatusBadRequest, "resource is required")
		return
	}
	if req.SourceAccountID == "" {
		jsonError(w, http.StatusBadRequest, "source_account_id is required")
		return
	}
	if req.TransitKeyID == "" {
		jsonError(w, http.StatusBadRequest, "transit_key_id is required")
		return
	}
	if len(req.ItemIDs) == 0 {
		jsonError(w, http.StatusBadRequest, "item_ids cannot be empty")
		return
	}

	// Check if peer is allowed to access this resource and account
	if !peer.IsResourceAllowed(req.Resource) {
		jsonError(w, http.StatusForbidden, "peer not allowed to access this resource")
		return
	}
	if !peer.IsAccountAllowed(req.SourceAccountID) {
		jsonError(w, http.StatusForbidden, "peer not allowed to access this account")
		return
	}

	// Verify transit key ID matches
	if req.TransitKeyID != peer.TransitKeyID {
		jsonError(w, http.StatusBadRequest, "invalid transit_key_id")
		return
	}

	// For now, only support secrets resource
	if req.Resource != "secrets" {
		jsonError(w, http.StatusBadRequest, "only 'secrets' resource is supported")
		return
	}

	// Get secrets for export
	exportItems := make([]syncpkg.ExportItem, 0, len(req.ItemIDs))
	for _, itemID := range req.ItemIDs {
		secret, err := s.DB.GetSecretByID(itemID)
		if err != nil {
			// Log but continue with other items
			s.DB.LogAudit(&req.SourceAccountID, nil, nil, "sync.export.error", 
				fmt.Sprintf("failed to get secret %s: %v", itemID, err))
			continue
		}
		if secret == nil {
			// Secret not found, skip
			continue
		}

		// Verify secret belongs to the requested account
		if secret.AccountID != req.SourceAccountID {
			s.DB.LogAudit(&req.SourceAccountID, nil, nil, "sync.export.error", 
				fmt.Sprintf("secret %s belongs to different account", itemID))
			continue
		}

		// Export secret (decrypt with master key, re-encrypt with transit key)
		transitEncrypted, err := s.DB.ExportSecret(secret, s.MasterKey, peer.TransitKeyHex)
		if err != nil {
			// Sanitize error message to avoid leaking decryption details
			s.DB.LogAudit(&req.SourceAccountID, nil, nil, "sync.export.error", 
				fmt.Sprintf("failed to export secret %s", itemID))
			continue
		}

		exportItems = append(exportItems, syncpkg.ExportItem{
			ID:                    secret.ID,
			Name:                  secret.Name,
			Provider:              secret.Provider,
			TransitEncryptedValue: transitEncrypted,
			Metadata:              secret.Metadata.String,
			UpdatedAt:             secret.UpdatedAt,
		})
	}

	export := syncpkg.ExportResponse{
		Resource:        req.Resource,
		SourceAccountID: req.SourceAccountID,
		SourceBrokerID:  "", // Not currently used, can be added to config if needed
		TransitKeyID:    req.TransitKeyID,
		ExportedAt:      time.Now().UTC().Format(time.RFC3339),
		SchemaVersion:   1,
		Items:           exportItems,
	}

	// Log audit entry
	s.DB.LogAudit(&req.SourceAccountID, nil, nil, "sync.export", 
		fmt.Sprintf("export for peer %s: %d items", peer.ID, len(exportItems)))

	jsonResponse(w, http.StatusOK, export)
}

// authenticateSyncToken extracts and validates a sync token from the request.
func (s *Server) authenticateSyncToken(r *http.Request) (*db.SyncPeer, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil, fmt.Errorf("no authorization header")
	}

	// Expect "Bearer <token>"
	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || parts[0] != "Bearer" {
		return nil, fmt.Errorf("invalid authorization format")
	}

	token := parts[1]
	peer, err := s.DB.GetSyncPeerByToken(token)
	if err != nil {
		return nil, fmt.Errorf("token lookup failed: %w", err)
	}
	if peer == nil {
		return nil, fmt.Errorf("invalid token")
	}

	return peer, nil
}

// syncRateLimiter creates a simple rate limiter for sync endpoints.
// Limits: 10 requests per minute per peer for manifest/export, 5 per minute for import.
func (s *Server) syncRateLimiter(maxRequests int, window time.Duration) func(http.Handler) http.Handler {
	type peerLimit struct {
		count     int
		windowEnd time.Time
	}
	
	limits := make(map[string]*peerLimit)
	var mu sync.RWMutex
	
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Get peer identifier
			var peerID string
			
			// For sync token endpoints, extract from token
			if strings.HasPrefix(r.URL.Path, "/sync/v1/manifest") || 
			   strings.HasPrefix(r.URL.Path, "/sync/v1/export") {
				authHeader := r.Header.Get("Authorization")
				if authHeader != "" {
					parts := strings.Split(authHeader, " ")
					if len(parts) == 2 && parts[0] == "Bearer" {
						// Use token hash as identifier
						peerID = auth.HashToken(parts[1])
					}
				}
			}
			
			// For import endpoint, use source IP
			if peerID == "" {
				peerID = r.RemoteAddr
			}
			
			if peerID == "" {
				peerID = "unknown"
			}
			
			now := time.Now()
			mu.Lock()
			limit, exists := limits[peerID]
			if !exists || now.After(limit.windowEnd) {
				limit = &peerLimit{
					count:     1,
					windowEnd: now.Add(window),
				}
				limits[peerID] = limit
				mu.Unlock()
				next.ServeHTTP(w, r)
				return
			}
			
			if limit.count >= maxRequests {
				mu.Unlock()
				jsonError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
			
			limit.count++
			mu.Unlock()
			next.ServeHTTP(w, r)
		})
	}
}

// RegisterSyncRoutes registers sync-related routes to the router.
func (s *Server) RegisterSyncRoutes(r chi.Router) {
	// Sync v1 endpoints (receiver-side import)
	r.Route("/sync/v1", func(r chi.Router) {
		// Rate limiting: 5 requests per minute for import
		r.With(s.syncRateLimiter(5, time.Minute)).Post("/import", s.handleSyncImport)
		// Source endpoints (require sync token auth)
		// Rate limiting: 10 requests per minute for manifest/export
		r.With(s.syncRateLimiter(10, time.Minute)).Get("/manifest", s.handleSyncManifest)
		r.With(s.syncRateLimiter(10, time.Minute)).Post("/export", s.handleSyncExport)
	})

	// Sync admin endpoints (source-side peer management)
	r.Route("/api/sync", func(r chi.Router) {
		r.Get("/peers", s.handleSyncListPeers)
		r.Post("/peers", s.handleCreateSyncPeer)
		r.Delete("/peers/{id}", s.handleDeleteSyncPeer)
		r.Post("/peers/{id}/rotate", s.handleRotateSyncPeerToken)
	})
}