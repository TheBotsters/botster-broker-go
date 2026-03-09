package interchange

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/TheBotsters/botster-broker-go/internal/db"
)

type ImportPlan struct {
	AccountsCreate int `json:"accounts_create"`
	AccountsSkip   int `json:"accounts_skip"`
	AgentsCreate   int `json:"agents_create"`
	AgentsSkip     int `json:"agents_skip"`
	SecretsCreate  int `json:"secrets_create"`
	SecretsSkip    int `json:"secrets_skip"`
	SecretsUpdate  int `json:"secrets_update"`
}

type ImportResult struct {
	ImportPlan
	Warnings []string `json:"warnings,omitempty"`
}

func ContentHash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func PlanImport(database *db.DB, doc Document, overwriteSecrets bool) (ImportResult, error) {
	res := ImportResult{Warnings: append([]string{}, doc.Warnings...)}

	accountByEmail := map[string]*db.Account{}
	for _, a := range doc.Accounts {
		email := strings.TrimSpace(a.Email)
		if email == "" {
			res.Warnings = append(res.Warnings, "account with empty email skipped")
			continue
		}
		acc, err := database.GetAccountByEmail(email)
		if err != nil {
			return res, fmt.Errorf("get account %s: %w", email, err)
		}
		if acc == nil {
			res.AccountsCreate++
		} else {
			res.AccountsSkip++
			accountByEmail[email] = acc
		}
	}

	for _, a := range doc.Agents {
		email := strings.TrimSpace(a.AccountEmail)
		if email == "" || strings.TrimSpace(a.Name) == "" {
			res.Warnings = append(res.Warnings, "agent missing name/account_email skipped")
			continue
		}
		acc := accountByEmail[email]
		if acc == nil {
			accObj, err := database.GetAccountByEmail(email)
			if err != nil {
				return res, fmt.Errorf("get account for agent %s: %w", a.Name, err)
			}
			if accObj == nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("agent %s skipped: unknown account %s", a.Name, email))
				continue
			}
			acc = accObj
			accountByEmail[email] = acc
		}
		agents, err := database.ListAgentsByAccount(acc.ID)
		if err != nil {
			return res, fmt.Errorf("list agents for %s: %w", email, err)
		}
		exists := false
		for _, ex := range agents {
			if ex.Name == a.Name {
				exists = true
				break
			}
		}
		if exists {
			res.AgentsSkip++
		} else {
			res.AgentsCreate++
		}
	}

	for _, s := range doc.Secrets {
		email := strings.TrimSpace(s.AccountEmail)
		if email == "" || strings.TrimSpace(s.Name) == "" {
			res.Warnings = append(res.Warnings, "secret missing name/account_email skipped")
			continue
		}
		acc, err := database.GetAccountByEmail(email)
		if err != nil {
			return res, fmt.Errorf("get account for secret %s: %w", s.Name, err)
		}
		if acc == nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("secret %s skipped: unknown account %s", s.Name, email))
			continue
		}
		meta, err := database.ListSecrets(acc.ID)
		if err != nil {
			return res, fmt.Errorf("list secrets for %s: %w", email, err)
		}
		exists := false
		for _, m := range meta {
			if m.Name == s.Name {
				exists = true
				break
			}
		}
		if exists {
			if overwriteSecrets {
				res.SecretsUpdate++
			} else {
				res.SecretsSkip++
			}
		} else {
			res.SecretsCreate++
		}
	}

	return res, nil
}

func ExecuteImport(database *db.DB, masterKey string, doc Document, overwriteSecrets bool) (ImportResult, error) {
	res := ImportResult{Warnings: append([]string{}, doc.Warnings...)}
	accountByEmail := map[string]*db.Account{}
	agentByAccountAndName := map[string]*db.Agent{}

	for _, a := range doc.Accounts {
		email := strings.TrimSpace(a.Email)
		if email == "" {
			res.Warnings = append(res.Warnings, "account with empty email skipped")
			continue
		}
		acc, err := database.GetAccountByEmail(email)
		if err != nil {
			return res, err
		}
		if acc == nil {
			acc, err = database.CreateAccount(email, "imported-"+time.Now().UTC().Format("20060102150405"))
			if err != nil {
				return res, err
			}
			res.AccountsCreate++
		} else {
			res.AccountsSkip++
		}
		accountByEmail[email] = acc
	}

	for _, a := range doc.Agents {
		email := strings.TrimSpace(a.AccountEmail)
		if email == "" || strings.TrimSpace(a.Name) == "" {
			res.Warnings = append(res.Warnings, "agent missing name/account_email skipped")
			continue
		}
		acc := accountByEmail[email]
		if acc == nil {
			var err error
			acc, err = database.GetAccountByEmail(email)
			if err != nil {
				return res, err
			}
			if acc == nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("agent %s skipped: unknown account %s", a.Name, email))
				continue
			}
			accountByEmail[email] = acc
		}
		agents, err := database.ListAgentsByAccount(acc.ID)
		if err != nil {
			return res, err
		}
		var found *db.Agent
		for _, ex := range agents {
			if ex.Name == a.Name {
				found = ex
				break
			}
		}
		if found == nil {
			newAgent, _, err := database.CreateAgent(acc.ID, a.Name)
			if err != nil {
				return res, err
			}
			found = newAgent
			res.AgentsCreate++
		} else {
			res.AgentsSkip++
		}
		agentByAccountAndName[acc.ID+"::"+found.Name] = found
	}

	for _, s := range doc.Secrets {
		email := strings.TrimSpace(s.AccountEmail)
		if email == "" || strings.TrimSpace(s.Name) == "" {
			res.Warnings = append(res.Warnings, "secret missing name/account_email skipped")
			continue
		}
		acc := accountByEmail[email]
		if acc == nil {
			var err error
			acc, err = database.GetAccountByEmail(email)
			if err != nil {
				return res, err
			}
			if acc == nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("secret %s skipped: unknown account %s", s.Name, email))
				continue
			}
			accountByEmail[email] = acc
		}
		meta, err := database.ListSecrets(acc.ID)
		if err != nil {
			return res, err
		}
		exists := false
		for _, m := range meta {
			if m.Name == s.Name {
				exists = true
				break
			}
		}
		if exists {
			if overwriteSecrets {
				if err := database.UpdateSecret(acc.ID, s.Name, s.Value, masterKey); err != nil {
					return res, err
				}
				res.SecretsUpdate++
			} else {
				res.SecretsSkip++
				continue
			}
		} else {
			created, err := database.CreateSecret(acc.ID, s.Name, s.Provider, s.Value, masterKey)
			if err != nil {
				return res, err
			}
			for _, grantName := range s.Grants {
				ag := agentByAccountAndName[acc.ID+"::"+grantName]
				if ag != nil {
					_ = database.GrantSecretAccess(created.ID, ag.ID)
				}
			}
			res.SecretsCreate++
		}
	}
	return res, nil
}
