package hub

import "sync"

// SecretsStore is a simple in-memory secret store for broker-owned secrets.
// This is intentionally hard-coded for now and can be swapped later.
type SecretsStore struct {
	mu      sync.RWMutex
	secrets map[string]string
}

func NewSecretsStore(seed map[string]string) *SecretsStore {
	cp := make(map[string]string, len(seed))
	for k, v := range seed {
		cp[k] = v
	}
	return &SecretsStore{secrets: cp}
}

// Get returns the secret value for key and whether it exists.
func (s *SecretsStore) Get(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.secrets[key]
	return v, ok
}
