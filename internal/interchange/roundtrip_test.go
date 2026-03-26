package interchange

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/TheBotsters/botster-broker-go/internal/db"
)

const rtMasterKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testDBRT(t *testing.T) *db.DB {
	t.Helper()
	path := t.TempDir() + "/test.db"
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		d.Close()
		_ = os.Remove(path)
	})
	return d
}

func normalizeExportJSONL(t *testing.T, raw string) []string {
	t.Helper()
	var out []string
	s := bufio.NewScanner(strings.NewReader(raw))
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("unmarshal line: %v", err)
		}
		if typ, _ := m["_type"].(string); typ == TypeHeader {
			delete(m, "exported_at")
			delete(m, "source")
		}
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal normalized: %v", err)
		}
		out = append(out, string(b))
	}
	if err := s.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	sort.Strings(out)
	return out
}

func TestRoundTripExportImportExport(t *testing.T) {
	src := testDBRT(t)
	dst := testDBRT(t)

	acc, err := src.CreateAccount("roundtrip@example.com", "pw")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = src.CreateAgent(acc.ID, "rt-agent")
	if err != nil {
		t.Fatal(err)
	}
	_, err = src.CreateSecret(acc.ID, "OPENAI_API_KEY", "openai", "sk-roundtrip", rtMasterKey)
	if err != nil {
		t.Fatal(err)
	}

	var exportA bytes.Buffer
	if err := WriteExportJSONL(&exportA, src, rtMasterKey, "src-a", time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("export A: %v", err)
	}

	doc, err := ParseJSONL(bytes.NewReader(exportA.Bytes()))
	if err != nil {
		t.Fatalf("parse export A: %v", err)
	}
	if _, err := ExecuteImport(dst, rtMasterKey, doc, false); err != nil {
		t.Fatalf("import into dst: %v", err)
	}

	var exportB bytes.Buffer
	if err := WriteExportJSONL(&exportB, dst, rtMasterKey, "dst-b", time.Date(2026, 3, 9, 1, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("export B: %v", err)
	}

	normA := normalizeExportJSONL(t, exportA.String())
	normB := normalizeExportJSONL(t, exportB.String())
	if len(normA) != len(normB) {
		t.Fatalf("normalized line count mismatch: %d vs %d\nA=%v\nB=%v", len(normA), len(normB), normA, normB)
	}
	for i := range normA {
		if normA[i] != normB[i] {
			t.Fatalf("normalized diff at %d\nA=%s\nB=%s\nA_all=%v\nB_all=%v", i, normA[i], normB[i], normA, normB)
		}
	}
}

func TestRoundTripWithSecretGrants(t *testing.T) {
	src := testDBRT(t)
	dst := testDBRT(t)

	acc, err := src.CreateAccount("grants@example.com", "pw")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := src.CreateAgent(acc.ID, "grant-agent")
	if err != nil {
		t.Fatal(err)
	}
	sec, err := src.CreateSecret(acc.ID, "GRANTED_KEY", "openai", "sk-grant-test", rtMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := src.GrantSecretToAgent(sec.ID, agent.ID); err != nil {
		t.Fatal(err)
	}

	// Export from src
	var exportA bytes.Buffer
	if err := WriteExportJSONL(&exportA, src, rtMasterKey, "src", time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("export: %v", err)
	}

	// Verify grant is in export
	if !strings.Contains(exportA.String(), `"grant-agent"`) {
		t.Fatalf("export missing grant: %s", exportA.String())
	}

	// Import into dst
	doc, err := ParseJSONL(bytes.NewReader(exportA.Bytes()))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	res, err := ExecuteImport(dst, rtMasterKey, doc, false)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Warnings) > 0 {
		t.Fatalf("unexpected warnings: %v", res.Warnings)
	}

	// Verify grant survived in dst
	var exportB bytes.Buffer
	if err := WriteExportJSONL(&exportB, dst, rtMasterKey, "dst", time.Date(2026, 3, 26, 1, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("export B: %v", err)
	}
	if !strings.Contains(exportB.String(), `"grant-agent"`) {
		t.Fatalf("grant not preserved in dst export: %s", exportB.String())
	}

	// Full normalized diff
	normA := normalizeExportJSONL(t, exportA.String())
	normB := normalizeExportJSONL(t, exportB.String())
	if len(normA) != len(normB) {
		t.Fatalf("line count mismatch: %d vs %d\nA=%v\nB=%v", len(normA), len(normB), normA, normB)
	}
	for i := range normA {
		if normA[i] != normB[i] {
			t.Fatalf("diff at line %d\nA=%s\nB=%s", i, normA[i], normB[i])
		}
	}
}
