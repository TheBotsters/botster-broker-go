package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TheBotsters/botster-broker-go/internal/db"
)

func testDB(t *testing.T) *db.DB {
	t.Helper()
	path := t.TempDir() + "/test.db"
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestHandleExportInterchangeAuth(t *testing.T) {
	d := testDB(t)
	s := &Server{DB: d, MasterKey: "mk"}

	req := httptest.NewRequest(http.MethodGet, "/api/export", nil)
	w := httptest.NewRecorder()
	s.handleExportInterchange(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestHandleExportInterchangeOK(t *testing.T) {
	d := testDB(t)
	_, _ = d.CreateAccount("a@example.com", "pw")
	s := &Server{DB: d, MasterKey: "mk"}

	req := httptest.NewRequest(http.MethodGet, "/api/export?source_label=qa", nil)
	req.Header.Set("X-Admin-Key", "mk")
	w := httptest.NewRecorder()
	s.handleExportInterchange(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/x-ndjson") {
		t.Fatalf("unexpected content type: %s", ct)
	}
	if !strings.Contains(w.Body.String(), `"_type":"header"`) {
		t.Fatalf("missing header row: %s", w.Body.String())
	}
}
