package api

import (
	"net/http"
	"time"

	"github.com/siofra-seksbot/botster-broker-go/internal/interchange"
)

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
