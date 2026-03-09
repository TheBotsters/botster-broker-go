package interchange

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/siofra-seksbot/botster-broker-go/internal/db"
)

func WriteExportJSONL(w io.Writer, database *db.DB, masterKey, source string, now time.Time) error {
	enc := json.NewEncoder(w)
	header := Header{
		Type:       TypeHeader,
		Version:    CurrentVersion,
		ExportedAt: now.UTC().Format(time.RFC3339),
		Source:     source,
	}
	if err := enc.Encode(header); err != nil {
		return fmt.Errorf("encode header: %w", err)
	}

	accounts, err := database.ListAccounts()
	if err != nil {
		return fmt.Errorf("list accounts: %w", err)
	}
	for _, acc := range accounts {
		if err := enc.Encode(Account{Type: TypeAccount, Email: acc.Email}); err != nil {
			return fmt.Errorf("encode account %s: %w", acc.Email, err)
		}

		agents, err := database.ListAgentsByAccount(acc.ID)
		if err != nil {
			return fmt.Errorf("list agents for account %s: %w", acc.ID, err)
		}
		for _, ag := range agents {
			if err := enc.Encode(Agent{
				Type:         TypeAgent,
				Name:         ag.Name,
				AccountEmail: acc.Email,
				Capabilities: []string{},
			}); err != nil {
				return fmt.Errorf("encode agent %s: %w", ag.Name, err)
			}
		}

		secrets, err := database.ListSecrets(acc.ID)
		if err != nil {
			return fmt.Errorf("list secrets for account %s: %w", acc.ID, err)
		}
		for _, sec := range secrets {
			val, err := database.GetSecret(acc.ID, sec.Name, masterKey)
			if err != nil {
				return fmt.Errorf("decrypt secret %s: %w", sec.Name, err)
			}
			grants, err := database.ListSecretGrantAgentNames(sec.ID)
			if err != nil {
				return fmt.Errorf("list grants for secret %s: %w", sec.Name, err)
			}
			if err := enc.Encode(Secret{
				Type:     TypeSecret,
				Name:     sec.Name,
				Value:    val,
				Provider: sec.Provider,
				Grants:   grants,
			}); err != nil {
				return fmt.Errorf("encode secret %s: %w", sec.Name, err)
			}
		}
	}

	return nil
}
