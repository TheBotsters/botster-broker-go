package interchange

import (
	"encoding/json"
	"testing"
)

func TestTypeRoundTrips(t *testing.T) {
	tests := []struct {
		name string
		v    any
	}{
		{"header", Header{Type: TypeHeader, Version: CurrentVersion, ExportedAt: "2026-03-09T00:00:00Z", Source: "src"}},
		{"account", Account{Type: TypeAccount, Email: "a@example.com", Name: "A"}},
		{"agent", Agent{Type: TypeAgent, Name: "agent1", AccountEmail: "a@example.com", Capabilities: []string{"exec"}}},
		{"secret", Secret{Type: TypeSecret, Name: "API_KEY", Value: "v", Provider: "openai", Grants: []string{"agent1"}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.v)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			switch tc.name {
			case "header":
				var got Header
				if err := json.Unmarshal(b, &got); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if got.Type != TypeHeader || got.Version != CurrentVersion {
					t.Fatalf("unexpected header: %+v", got)
				}
			case "account":
				var got Account
				_ = json.Unmarshal(b, &got)
				if got.Email == "" {
					t.Fatal("email empty")
				}
			case "agent":
				var got Agent
				_ = json.Unmarshal(b, &got)
				if got.Name == "" || got.AccountEmail == "" {
					t.Fatalf("unexpected agent: %+v", got)
				}
			case "secret":
				var got Secret
				_ = json.Unmarshal(b, &got)
				if got.Name == "" || got.Value == "" {
					t.Fatalf("unexpected secret: %+v", got)
				}
			}
		})
	}
}
