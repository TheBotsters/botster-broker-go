package interchange

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/siofra-seksbot/botster-broker-go/internal/db"
)

const testMasterKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testDB(t *testing.T) *db.DB {
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

func TestWriteExportJSONL(t *testing.T) {
	d := testDB(t)

	acc, err := d.CreateAccount("a@example.com", "pw")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := d.CreateAgent(acc.ID, "agent1")
	if err != nil {
		t.Fatal(err)
	}
	sec, err := d.CreateSecret(acc.ID, "OPENAI_API_KEY", "openai", "sekret", testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.GrantSecretAccess(sec.ID, agent.ID); err != nil {
		t.Fatal(err)
	}

	var b bytes.Buffer
	if err := WriteExportJSONL(&b, d, testMasterKey, "test-src", time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("WriteExportJSONL: %v", err)
	}

	out := b.String()
	for _, want := range []string{
		`"_type":"header"`,
		`"version":"0.1.0"`,
		`"source":"test-src"`,
		`"_type":"account"`,
		`"email":"a@example.com"`,
		`"_type":"agent"`,
		`"name":"agent1"`,
		`"account_email":"a@example.com"`,
		`"_type":"secret"`,
		`"name":"OPENAI_API_KEY"`,
		`"value":"sekret"`,
		`"grants":["agent1"]`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected output to contain %q\n%s", want, out)
		}
	}
}
