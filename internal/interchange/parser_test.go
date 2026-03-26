package interchange

import (
	"strings"
	"testing"
)

func TestParseJSONL_Complete(t *testing.T) {
	input := strings.Join([]string{
		`{"_type":"header","version":"0.1.0","exported_at":"2026-03-09T00:00:00Z","source":"prod"}`,
		`{"_type":"account","email":"a@example.com","name":"Alice"}`,
		`{"_type":"agent","name":"agent1","account_email":"a@example.com","capabilities":["exec"]}`,
		`{"_type":"secret","name":"API_KEY","value":"secret","grants":["agent1"]}`,
	}, "\n")

	doc, err := ParseJSONL(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseJSONL: %v", err)
	}
	if len(doc.Accounts) != 1 || len(doc.Agents) != 1 || len(doc.Secrets) != 1 {
		t.Fatalf("unexpected counts: %+v", doc)
	}
}

func TestParseJSONL_MissingHeader(t *testing.T) {
	_, err := ParseJSONL(strings.NewReader(`{"_type":"account","email":"a@example.com"}`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseJSONL_UnknownTypeWarning(t *testing.T) {
	input := strings.Join([]string{
		`{"_type":"header","version":"0.1.0","exported_at":"2026-03-09T00:00:00Z"}`,
		`{"_type":"mystery","x":1}`,
	}, "\n")

	doc, err := ParseJSONL(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseJSONL: %v", err)
	}
	if len(doc.Warnings) != 1 {
		t.Fatalf("expected warning, got %v", doc.Warnings)
	}
}

func TestParseJSONL_UnsupportedVersion(t *testing.T) {
	_, err := ParseJSONL(strings.NewReader(`{"_type":"header","version":"9.9.9","exported_at":"2026-03-09T00:00:00Z"}`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseJSONL_EmptyInput(t *testing.T) {
	_, err := ParseJSONL(strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestParseJSONL_EmptyLines(t *testing.T) {
	// Empty lines should be skipped; header still required
	_, err := ParseJSONL(strings.NewReader("\n\n\n"))
	if err == nil {
		t.Fatal("expected error — no header")
	}
}

func TestParseJSONL_DuplicateHeader(t *testing.T) {
	input := strings.Join([]string{
		`{"_type":"header","version":"0.1.0","exported_at":"2026-03-09T00:00:00Z"}`,
		`{"_type":"header","version":"0.1.0","exported_at":"2026-03-09T00:00:01Z"}`,
		`{"_type":"account","email":"a@example.com"}`,
	}, "\n")
	doc, err := ParseJSONL(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(doc.Warnings) == 0 {
		t.Fatal("expected warning for duplicate header")
	}
	if len(doc.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(doc.Accounts))
	}
}

func TestParseJSONL_OutOfOrder(t *testing.T) {
	// Agents before accounts — parser should still collect both
	input := strings.Join([]string{
		`{"_type":"header","version":"0.1.0","exported_at":"2026-03-09T00:00:00Z"}`,
		`{"_type":"agent","name":"agent1","account_email":"a@example.com"}`,
		`{"_type":"account","email":"a@example.com"}`,
	}, "\n")
	doc, err := ParseJSONL(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(doc.Agents) != 1 || len(doc.Accounts) != 1 {
		t.Fatalf("expected 1 agent + 1 account, got agents=%d accounts=%d", len(doc.Agents), len(doc.Accounts))
	}
}

func TestParseJSONL_UnknownFieldsIgnored(t *testing.T) {
	input := strings.Join([]string{
		`{"_type":"header","version":"0.1.0","exported_at":"2026-03-09T00:00:00Z","future_field":"x"}`,
		`{"_type":"account","email":"a@example.com","unknown_field":42}`,
	}, "\n")
	doc, err := ParseJSONL(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(doc.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(doc.Accounts))
	}
}
