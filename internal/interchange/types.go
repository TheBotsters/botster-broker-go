package interchange

const (
	TypeHeader  = "header"
	TypeAccount = "account"
	TypeAgent   = "agent"
	TypeSecret  = "secret"

	CurrentVersion = "0.1.0"
)

type Header struct {
	Type       string `json:"_type"`
	Version    string `json:"version"`
	ExportedAt string `json:"exported_at"`
	Source     string `json:"source,omitempty"`
}

type Account struct {
	Type  string `json:"_type"`
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type Agent struct {
	Type         string   `json:"_type"`
	Name         string   `json:"name"`
	AccountEmail string   `json:"account_email"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type Secret struct {
	Type         string   `json:"_type"`
	Name         string   `json:"name"`
	AccountEmail string   `json:"account_email,omitempty"`
	Value        string   `json:"value"`
	Provider     string   `json:"provider,omitempty"`
	Grants       []string `json:"grants,omitempty"`
}

type Document struct {
	Header   Header
	Accounts []Account
	Agents   []Agent
	Secrets  []Secret
	Warnings []string
}
