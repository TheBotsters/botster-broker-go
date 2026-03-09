package api

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/TheBotsters/botster-broker-go/internal/interchange"
)

type importConfirmEntry struct {
	BodyHash         string
	OverwriteSecrets bool
	ExpiresAt        time.Time
	Used             bool
}

var importConfirmStore = struct {
	sync.Mutex
	m map[string]*importConfirmEntry
}{m: map[string]*importConfirmEntry{}}

func newConfirmToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "itk_" + hex.EncodeToString(b), nil
}

// GET /api/export — root only
func (s *Server) handleExportInterchange(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(r) {
		jsonError(w, 401, "Unauthorized: invalid or missing X-Admin-Key")
		return
	}

	source := r.URL.Query().Get("source_label")
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")

	if err := interchange.WriteExportJSONL(w, s.DB, s.MasterKey, source, time.Now()); err != nil {
		jsonError(w, 500, "Export failed: "+err.Error())
		return
	}
}

// POST /api/import — root only, gated by Config.AllowImport
func (s *Server) handleImportInterchange(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(r) {
		jsonError(w, 401, "Unauthorized: invalid or missing X-Admin-Key")
		return
	}
	if s.Config == nil || !s.Config.AllowImport {
		http.NotFound(w, r)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 5<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		jsonError(w, 400, "invalid body")
		return
	}
	doc, err := interchange.ParseJSONL(bytes.NewReader(body))
	if err != nil {
		jsonError(w, 400, "invalid interchange jsonl: "+err.Error())
		return
	}
	overwriteSecrets := r.URL.Query().Get("overwrite_secrets") == "true"
	confirm := r.URL.Query().Get("confirm")
	hash := interchange.ContentHash(body)

	if confirm == "" {
		plan, err := interchange.PlanImport(s.DB, doc, overwriteSecrets)
		if err != nil {
			jsonError(w, 500, "plan failed: "+err.Error())
			return
		}
		tok, tokErr := newConfirmToken()
		if tokErr != nil {
			jsonError(w, 500, "failed to generate confirm token")
			return
		}
		expires := time.Now().UTC().Add(5 * time.Minute)
		importConfirmStore.Lock()
		importConfirmStore.m[tok] = &importConfirmEntry{BodyHash: hash, OverwriteSecrets: overwriteSecrets, ExpiresAt: expires}
		importConfirmStore.Unlock()
		jsonResponse(w, 200, map[string]any{
			"dry_run":       true,
			"confirm_token": tok,
			"expires_at":    expires.Format(time.RFC3339),
			"summary":       plan,
		})
		return
	}

	importConfirmStore.Lock()
	entry := importConfirmStore.m[confirm]
	if entry == nil || entry.Used || time.Now().UTC().After(entry.ExpiresAt) || entry.BodyHash != hash || entry.OverwriteSecrets != overwriteSecrets {
		importConfirmStore.Unlock()
		jsonError(w, 400, "invalid or expired confirm token")
		return
	}
	entry.Used = true
	importConfirmStore.Unlock()

	res, err := interchange.ExecuteImport(s.DB, s.MasterKey, doc, overwriteSecrets)
	if err != nil {
		jsonError(w, 500, "import failed: "+err.Error())
		return
	}
	jsonResponse(w, 200, map[string]any{
		"dry_run":  false,
		"executed": true,
		"summary":  res,
	})
}
