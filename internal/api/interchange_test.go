package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TheBotsters/botster-broker-go/internal/config"
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
	s := &Server{DB: d, MasterKey: "mk", Config: &config.Config{AllowImport: true}}

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
	s := &Server{DB: d, MasterKey: "mk", Config: &config.Config{AllowImport: true}}

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

func TestHandleImportInterchangeGate(t *testing.T) {
	d := testDB(t)
	s := &Server{DB: d, MasterKey: "mk", Config: &config.Config{AllowImport: false}}
	body := []byte(`{"_type":"header","version":"0.1.0","exported_at":"2026-03-09T00:00:00Z"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/import", bytes.NewReader(body))
	req.Header.Set("X-Admin-Key", "mk")
	w := httptest.NewRecorder()
	s.handleImportInterchange(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleImportInterchangeDryRunAndConfirm(t *testing.T) {
	d := testDB(t)
	s := &Server{DB: d, MasterKey: "mk", Config: &config.Config{AllowImport: true}}
	jsonl := strings.Join([]string{
		`{"_type":"header","version":"0.1.0","exported_at":"2026-03-09T00:00:00Z"}`,
		`{"_type":"account","email":"a@example.com"}`,
	}, "\n")

	// dry-run
	req := httptest.NewRequest(http.MethodPost, "/api/import", strings.NewReader(jsonl))
	req.Header.Set("X-Admin-Key", "mk")
	w := httptest.NewRecorder()
	s.handleImportInterchange(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("dry-run expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var dry map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &dry); err != nil {
		t.Fatal(err)
	}
	tok, _ := dry["confirm_token"].(string)
	if tok == "" {
		t.Fatalf("missing confirm token: %v", dry)
	}

	// execute with confirm
	req2 := httptest.NewRequest(http.MethodPost, "/api/import?confirm="+tok, strings.NewReader(jsonl))
	req2.Header.Set("X-Admin-Key", "mk")
	w2 := httptest.NewRecorder()
	s.handleImportInterchange(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("execute expected 200, got %d body=%s", w2.Code, w2.Body.String())
	}
}
