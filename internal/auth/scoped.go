// Package auth — scoped token creation and verification.
//
// Scoped tokens are stateless, HMAC-signed JWTs-like tokens that carry
// a restricted set of capabilities. They are created by agents and have a TTL.
//
// Token format: seks_scoped_<base64url-payload>.<hmac-hex-signature>
// Signature: HMAC-SHA256 of the base64url payload using the broker master key.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	scopedTokenPrefix = "seks_scoped_"
	scopedTTLMin      = 10
	scopedTTLMax      = 1800
	scopedTTLDefault  = 300
)

// ScopedTokenPayload is the decoded payload of a scoped token.
type ScopedTokenPayload struct {
	Type      string   `json:"type"`
	AgentID   string   `json:"agent_id"`
	AccountID string   `json:"account_id"`
	Skill     string   `json:"skill"`
	Caps      []string `json:"caps"`
	IssuedAt  int64    `json:"iat"` // milliseconds
	ExpiresAt int64    `json:"exp"` // milliseconds
}

// CreateScopedToken creates a new stateless scoped token signed with masterKey.
func CreateScopedToken(masterKey, agentID, accountID, skillName string, capabilities []string, ttlSeconds int) (string, time.Time, error) {
	// Clamp TTL
	if ttlSeconds <= 0 {
		ttlSeconds = scopedTTLDefault
	}
	if ttlSeconds < scopedTTLMin {
		ttlSeconds = scopedTTLMin
	}
	if ttlSeconds > scopedTTLMax {
		ttlSeconds = scopedTTLMax
	}

	now := time.Now()
	expiresAt := now.Add(time.Duration(ttlSeconds) * time.Second)

	payload := ScopedTokenPayload{
		Type:      "scoped",
		AgentID:   agentID,
		AccountID: accountID,
		Skill:     skillName,
		Caps:      capabilities,
		IssuedAt:  now.UnixMilli(),
		ExpiresAt: expiresAt.UnixMilli(),
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("marshal scoped payload: %w", err)
	}

	b64Payload := base64.RawURLEncoding.EncodeToString(payloadJSON)
	sig := computeScopedSig(masterKey, b64Payload)
	token := scopedTokenPrefix + b64Payload + "." + sig

	return token, expiresAt, nil
}

// VerifyScopedToken verifies a scoped token and returns its payload.
// Returns an error if the token is invalid, expired, or has wrong type.
func VerifyScopedToken(token, masterKey string) (*ScopedTokenPayload, error) {
	if !strings.HasPrefix(token, scopedTokenPrefix) {
		return nil, fmt.Errorf("not a scoped token")
	}

	rest := strings.TrimPrefix(token, scopedTokenPrefix)
	dot := strings.LastIndex(rest, ".")
	if dot < 0 {
		return nil, fmt.Errorf("invalid scoped token format: missing signature")
	}

	b64Payload := rest[:dot]
	sig := rest[dot+1:]

	// Verify HMAC
	expectedSig := computeScopedSig(masterKey, b64Payload)
	if !hmac.Equal([]byte(sig), []byte(expectedSig)) {
		return nil, fmt.Errorf("invalid scoped token signature")
	}

	// Decode payload
	payloadJSON, err := base64.RawURLEncoding.DecodeString(b64Payload)
	if err != nil {
		return nil, fmt.Errorf("decode scoped payload: %w", err)
	}

	var payload ScopedTokenPayload
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal scoped payload: %w", err)
	}

	// Verify type
	if payload.Type != "scoped" {
		return nil, fmt.Errorf("invalid scoped token type: %s", payload.Type)
	}

	// Verify expiry
	if time.Now().UnixMilli() > payload.ExpiresAt {
		return nil, fmt.Errorf("scoped token expired")
	}

	return &payload, nil
}

// IsScopedToken returns true if the token string looks like a scoped token.
func IsScopedToken(token string) bool {
	return strings.HasPrefix(token, scopedTokenPrefix)
}

// computeScopedSig computes HMAC-SHA256 of the base64 payload using masterKey.
func computeScopedSig(masterKey, b64Payload string) string {
	mac := hmac.New(sha256.New, []byte(masterKey))
	mac.Write([]byte(b64Payload))
	return hex.EncodeToString(mac.Sum(nil))
}
