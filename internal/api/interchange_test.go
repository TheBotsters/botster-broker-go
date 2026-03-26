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

func TestHandleImportInterchangeAuth(t *testing.T) {
	d := testDB(t)
	s := &Server{DB: d, MasterKey: "mk", Config: &config.Config{AllowImport: true}}
	body := []byte(`{"_type":"header","version":"0.1.0","exported_at":"2026-03-09T00:00:00Z"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/import", bytes.NewReader(body))
	// no X-Admin-Key
	w := httptest.NewRecorder()
	s.handleImportInterchange(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestHandleImportInterchangeTokenSingleUse(t *testing.T) {
	d := testDB(t)
	s := &Server{DB: d, MasterKey: "mk", Config: &config.Config{AllowImport: true}}
	jsonl := "{\"\x5f\x74\x79\x70\x65\":\"header\",\"version\":\"0.1.0\",\"exported_at\":\"2026-03-09T00:00:00Z\"}\n{\"_type\":\"account\",\"email\":\"a@example.com\"}"

	// dry-run to get token
	req := httptest.NewRequest(http.MethodPost, "/api/import", strings.NewReader(jsonl))
	req.Header.Set("X-Admin-Key", "mk")
	w := httptest.NewRecorder()
	s.handleImportInterchange(w, req)
	if w.Code != 200 {
		t.Fatalf("dry-run: %d %s", w.Code, w.Body.String())
	}
	var dry map[string]any
	json.Unmarshal(w.Body.Bytes(), &dry)
	tok := dry["confirm_token"].(string)

	// first confirm — ok
	req2 := httptest.NewRequest(http.MethodPost, "/api/import?confirm="+tok, strings.NewReader(jsonl))
	req2.Header.Set("X-Admin-Key", "mk")
	w2 := httptest.NewRecorder()
	s.handleImportInterchange(w2, req2)
	if w2.Code != 200 {
		t.Fatalf("first confirm: %d %s", w2.Code, w2.Body.String())
	}

	// second confirm — must fail (token consumed)
	req3 := httptest.NewRequest(http.MethodPost, "/api/import?confirm="+tok, strings.NewReader(jsonl))
	req3.Header.Set("X-Admin-Key", "mk")
	w3 := httptest.NewRecorder()
	s.handleImportInterchange(w3, req3)
	if w3.Code != 400 {
		t.Fatalf("second confirm should return 400, got %d", w3.Code)
	}
}

func TestHandleImportInterchangeTokenContentBound(t *testing.T) {
	d := testDB(t)
	s := &Server{DB: d, MasterKey: "mk", Config: &config.Config{AllowImport: true}}

	jsonlA := "{\"_type\":\"header\",\"version\":\"0.1.0\",\"exported_at\":\"2026-03-09T00:00:00Z\"}\n{\"_type\":\"account\",\"email\":\"a@example.com\"}"
	jsonlB := "{\"_type\":\"header\",\"version\":\"0.1.0\",\"exported_at\":\"2026-03-09T00:00:00Z\"}\n{\"_type\":\"account\",\"email\":\"b@example.com\"}"

	// dry-run against A
	req := httptest.NewRequest(http.MethodPost, "/api/import", strings.NewReader(jsonlA))
	req.Header.Set("X-Admin-Key", "mk")
	w := httptest.NewRecorder()
	s.handleImportInterchange(w, req)
	var dry map[string]any
	json.Unmarshal(w.Body.Bytes(), &dry)
	tok := dry["confirm_token"].(string)

	// confirm with B — must fail (content hash mismatch)
	req2 := httptest.NewRequest(http.MethodPost, "/api/import?confirm="+tok, strings.NewReader(jsonlB))
	req2.Header.Set("X-Admin-Key", "mk")
	w2 := httptest.NewRecorder()
	s.handleImportInterchange(w2, req2)
	if w2.Code != 400 {
		t.Fatalf("content-bound check: expected 400, got %d", w2.Code)
	}
}

func TestHandleImportInterchangeIdempotent(t *testing.T) {
	d := testDB(t)
	s := &Server{DB: d, MasterKey: "mk", Config: &config.Config{AllowImport: true}}
	jsonl := "{\"_type\":\"header\",\"version\":\"0.1.0\",\"exported_at\":\"2026-03-09T00:00:00Z\"}\n{\"_type\":\"account\",\"email\":\"idem@example.com\"}"

	doImport := func() map[string]any {
		// dry-run
		req := httptest.NewRequest(http.MethodPost, "/api/import", strings.NewReader(jsonl))
		req.Header.Set("X-Admin-Key", "mk")
		w := httptest.NewRecorder()
		s.handleImportInterchange(w, req)
		var dry map[string]any
		json.Unmarshal(w.Body.Bytes(), &dry)
		tok := dry["confirm_token"].(string)
		// execute
		req2 := httptest.NewRequest(http.MethodPost, "/api/import?confirm="+tok, strings.NewReader(jsonl))
		req2.Header.Set("X-Admin-Key", "mk")
		w2 := httptest.NewRecorder()
		s.handleImportInterchange(w2, req2)
		if w2.Code != 200 {
			t.Fatalf("execute: %d %s", w2.Code, w2.Body.String())
		}
		var res map[string]any
		json.Unmarshal(w2.Body.Bytes(), &res)
		return res
	}

	r1 := doImport()
	r2 := doImport()

	sum1 := r1["summary"].(map[string]any)
	sum2 := r2["summary"].(map[string]any)

	if sum1["accounts_create"] != float64(1) {
		t.Fatalf("first import: expected 1 account created, got %v", sum1)
	}
	if sum2["accounts_create"] != float64(0) || sum2["accounts_skip"] != float64(1) {
		t.Fatalf("second import: expected 0 created / 1 skipped, got %v", sum2)
	}
}

func TestHandleImportInterchangeBadVersion(t *testing.T) {
	d := testDB(t)
	s := &Server{DB: d, MasterKey: "mk", Config: &config.Config{AllowImport: true}}
	body := []byte(`{"_type":"header","version":"9.9.9","exported_at":"2026-03-09T00:00:00Z"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/import", bytes.NewReader(body))
	req.Header.Set("X-Admin-Key", "mk")
	w := httptest.NewRecorder()
	s.handleImportInterchange(w, req)
	if w.Code != 400 {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleExportImportEmptyBroker(t *testing.T) {
	d := testDB(t)
	s := &Server{DB: d, MasterKey: "mk", Config: &config.Config{AllowImport: true}}

	req := httptest.NewRequest(http.MethodGet, "/api/export", nil)
	req.Header.Set("X-Admin-Key", "mk")
	w := httptest.NewRecorder()
	s.handleExportInterchange(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n")
	// Empty broker: only the header line
	if len(lines) != 1 {
		t.Fatalf("empty broker export: expected 1 line (header), got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], `"_type":"header"`) {
		t.Fatalf("expected header line, got: %s", lines[0])
	}
}
