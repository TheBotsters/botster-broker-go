// Package auth handles token hashing and validation.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// HashToken returns the SHA-256 hex hash of a token string.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// GenerateToken creates a new random token with the given prefix.
// Format: prefix_<32 random alphanumeric chars>
func GenerateToken(prefix string) (string, error) {
	b := make([]byte, 24) // 24 bytes = 32 base64-ish chars
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(b)[:32]), nil
}
