// Package api — Inference Proxy
//
// Ports inference.ts from the TypeScript broker reference implementation.
// Proxies LLM API calls on behalf of agents, injecting credentials server-side.
// The brain never sees the API key. The broker logs everything.
//
// ─── Manual Test Plan ─────────────────────────────────────────────────────────
// 1. POST /v1/inference — generic endpoint
//    - Valid agent token + anthropic/openai/xai provider → successful proxy
//    - Missing provider/model/messages → 400 error
//    - Invalid agent token → 401
//    - Missing secret for provider → 404
//    - SSE streaming: curl with -N, verify chunks arrive in real time
//
// 2. POST /v1/proxy/anthropic/v1/messages — transparent proxy
//    - Round-robin: add ANTHROPIC_TOKEN, ANTHROPIC_TOKEN_2 to DB, verify rotation
//    - 429 retry: mock/simulate rate limit, verify retry with different key
//    - OAuth token (sk-ant-oat01-*): verify Authorization: Bearer + anthropic-beta headers
//    - API key (sk-ant-api03-*): verify x-api-key header
//    - SSE streaming passthrough: real model call with stream:true
//    - Forward anthropic-beta header from client
//
// 3. POST /v1/proxy/openai/v1/chat/completions — transparent proxy
//    - API key mode (sk-*): standard proxy
//    - OAuth bundle mode ({access, refresh, expires, accountId}):
//      - Codex backend URL rewrite to chatgpt.com/backend-api/codex/responses
//      - Auto-refresh on 401 with refresh token
//    - Embedding fallback: send embedding model with oauth-bundle token → needs OPENAI_API_KEY
//
// 4. POST /v1/web/search — Brave search proxy
//    - Valid query → results from free tier
//    - Free tier 429 → fallback to paid tier
//    - No tokens → 404
//
// 5. GET /v1/inference/providers — list providers
//    - Returns configured status for each provider
//    - Requires agent auth
// ──────────────────────────────────────────────────────────────────────────────

package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/TheBotsters/botster-broker-go/internal/auth"
	"github.com/TheBotsters/botster-broker-go/internal/tap"
)

// ─── Round-Robin State ─────────────────────────────────────────────────────────

var (
	rrMu       sync.Mutex
	rrCounters = map[string]int{}
)

// nextRoundRobin returns the current index and advances the counter.
// Matches TS: returns current before increment.
func nextRoundRobin(provider string, total int) int {
	rrMu.Lock()
	defer rrMu.Unlock()
	current := rrCounters[provider]
	rrCounters[provider] = (current + 1) % total
	return current
}

func pickKey(provider string, keys []string) string {
	if len(keys) == 1 {
		return keys[0]
	}
	idx := nextRoundRobin(provider, len(keys))
	return keys[idx]
}

// ─── Auth Helper ───────────────────────────────────────────────────────────────

// inferenceAgentInfo is a lightweight auth result for inference handlers.
type inferenceAgentInfo struct {
	ID        string
	AccountID string
	Name      string
}

// authenticateInferenceAgent supports multiple auth methods in priority order:
//  1. X-Agent-Token header
//  2. ?agent_token query param
//  3. Authorization: Bearer <token>
//  4. x-api-key header
//
// For scoped tokens, it also stores the scoped capabilities in the request context.
// This mirrors the TS authenticateAgent() in inference.ts.
func (s *Server) authenticateInferenceAgent(r *http.Request) (*inferenceAgentInfo, error) {
	var token string

	if v := r.Header.Get("X-Agent-Token"); v != "" {
		token = v
	} else if v := r.URL.Query().Get("agent_token"); v != "" {
		token = v
	} else if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
		token = strings.TrimPrefix(a, "Bearer ")
	} else if v := r.Header.Get("x-api-key"); v != "" {
		token = v
	}

	if token == "" {
		return nil, nil
	}

	// Handle scoped tokens — verify HMAC, inject caps into context
	if isScopedToken(token) {
		payload, err := s.verifyScopedForInference(token, r)
		if err != nil {
			return nil, nil // treat expired/invalid as unauthenticated
		}
		agent, err := s.DB.GetAgentByID(payload.AgentID)
		if err != nil || agent == nil {
			return nil, nil
		}
		return &inferenceAgentInfo{
			ID:        agent.ID,
			AccountID: agent.AccountID,
			Name:      agent.Name,
		}, nil
	}

	agent, err := s.DB.GetAgentByToken(token)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		return nil, nil
	}
	return &inferenceAgentInfo{
		ID:        agent.ID,
		AccountID: agent.AccountID,
		Name:      agent.Name,
	}, nil
}

// isScopedToken returns true if the token starts with the scoped prefix.
func isScopedToken(token string) bool {
	return strings.HasPrefix(token, "seks_scoped_")
}

// scopedPayloadForInference holds the minimal fields from a verified scoped token.
type scopedPayloadForInference struct {
	AgentID string
	Caps    []string
}

// verifyScopedForInference verifies a scoped token and injects caps into the request context.
func (s *Server) verifyScopedForInference(token string, r *http.Request) (*scopedPayloadForInference, error) {
	payload, err := auth.VerifyScopedToken(token, s.MasterKey)
	if err != nil {
		return nil, err
	}
	// Inject caps into context so checkScopedCapability works in this handler
	enriched := withScopedCaps(r, payload.Caps)
	*r = *enriched
	return &scopedPayloadForInference{
		AgentID: payload.AgentID,
		Caps:    payload.Caps,
	}, nil
}

// ─── Provider Configs ──────────────────────────────────────────────────────────

type providerConfig struct {
	BaseURL    string
	Path       string
	SecretName string
}

var inferenceProviders = map[string]providerConfig{
	"anthropic": {
		BaseURL:    "https://api.anthropic.com",
		Path:       "/v1/messages",
		SecretName: "ANTHROPIC_TOKEN",
	},
	"openai": {
		BaseURL:    "https://api.openai.com",
		Path:       "/v1/chat/completions",
		SecretName: "OPENAI_TOKEN",
	},
	"xai": {
		BaseURL:    "https://api.x.ai",
		Path:       "/v1/chat/completions",
		SecretName: "XAI_TOKEN",
	},
}

// ─── Anthropic Token Type Detection ───────────────────────────────────────────

type anthropicTokenType string

const (
	anthropicAPIKey anthropicTokenType = "api-key"
	anthropicOAuth  anthropicTokenType = "oauth"
)

func detectAnthropicTokenType(token string) anthropicTokenType {
	if strings.HasPrefix(token, "sk-ant-oat01-") {
		return anthropicOAuth
	}
	return anthropicAPIKey
}

func buildAnthropicAuthHeaders(token string) map[string]string {
	tokenType := detectAnthropicTokenType(token)
	if tokenType == anthropicOAuth {
		return map[string]string{
			"Authorization":     "Bearer " + token,
			"anthropic-version": "2023-06-01",
			"anthropic-beta":    "oauth-2025-04-20",
		}
	}
	return map[string]string{
		"x-api-key":         token,
		"anthropic-version": "2023-06-01",
	}
}

// ─── OpenAI OAuth Bundle ───────────────────────────────────────────────────────

type openAITokenType string

const (
	openAIAPIKey      openAITokenType = "api-key"
	openAIOAuthAccess openAITokenType = "oauth-access"
	openAIOAuthBundle openAITokenType = "oauth-bundle"
)

type openAIOAuthBundleData struct {
	Access    string `json:"access"`
	Refresh   string `json:"refresh,omitempty"`
	Expires   int64  `json:"expires,omitempty"`
	AccountID string `json:"accountId,omitempty"`
}

func parseOpenAIOAuthBundle(raw string) (*openAIOAuthBundleData, bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "{") {
		return nil, false
	}
	var b openAIOAuthBundleData
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		return nil, false
	}
	if b.Access == "" {
		return nil, false
	}
	return &b, true
}

func detectOpenAITokenType(token string) openAITokenType {
	if strings.HasPrefix(token, "sk-") {
		return openAIAPIKey
	}
	if _, ok := parseOpenAIOAuthBundle(token); ok {
		return openAIOAuthBundle
	}
	return openAIOAuthAccess
}

type openAIAuthResult struct {
	AuthHeaderValue string
	Mode            openAITokenType
	Expires         int64
	Bundle          *openAIOAuthBundleData
}

func resolveOpenAIAuth(token string) (*openAIAuthResult, error) {
	mode := detectOpenAITokenType(token)
	if mode == openAIOAuthBundle {
		bundle, ok := parseOpenAIOAuthBundle(token)
		if !ok || bundle.Access == "" {
			return nil, fmt.Errorf("invalid OpenAI OAuth bundle: missing access token")
		}
		return &openAIAuthResult{
			AuthHeaderValue: "Bearer " + bundle.Access,
			Mode:            mode,
			Expires:         bundle.Expires,
			Bundle:          bundle,
		}, nil
	}
	return &openAIAuthResult{
		AuthHeaderValue: "Bearer " + token,
		Mode:            mode,
	}, nil
}

const (
	openAICodexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	openAICodexTokenURL = "https://auth.openai.com/oauth/token"
)

func refreshOpenAICodexBundle(bundle *openAIOAuthBundleData) (*openAIOAuthBundleData, error) {
	if bundle.Refresh == "" {
		return nil, fmt.Errorf("no refresh token")
	}

	formData := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {bundle.Refresh},
		"client_id":     {openAICodexClientID},
	}

	resp, err := http.PostForm(openAICodexTokenURL, formData)
	if err != nil {
		return nil, fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed with status %d", resp.StatusCode)
	}

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode refresh response: %w", err)
	}
	if result.AccessToken == "" || result.RefreshToken == "" {
		return nil, fmt.Errorf("missing tokens in refresh response")
	}

	return &openAIOAuthBundleData{
		Access:    result.AccessToken,
		Refresh:   result.RefreshToken,
		Expires:   time.Now().UnixMilli() + int64(result.ExpiresIn)*1000,
		AccountID: bundle.AccountID,
	}, nil
}

// persistOpenAIBundle saves a refreshed OAuth bundle back to the DB.
func (s *Server) persistOpenAIBundle(accountID string, bundle *openAIOAuthBundleData) {
	data, err := json.Marshal(bundle)
	if err != nil {
		log.Printf("[inference] failed to marshal bundle for persist: %v", err)
		return
	}
	if err := s.DB.UpdateSecret(accountID, "OPENAI_TOKEN", string(data), s.MasterKey); err != nil {
		log.Printf("[inference] failed to persist refreshed OPENAI_TOKEN: %v", err)
	}
}

// ─── Key Resolution ────────────────────────────────────────────────────────────

// resolveInferenceKeys returns all decrypted API keys for a provider (round-robin ready).
// Returns (keys, httpStatus, errMsg). httpStatus=0 means success.
// Checks the providers table first, falls back to hardcoded inferenceProviders map.
func (s *Server) resolveInferenceKeys(agent *inferenceAgentInfo, provider string) ([]string, int, string) {
	// Try providers table first
	dbProvider, err := s.DB.GetProviderByName(agent.AccountID, provider)
	if err == nil && dbProvider != nil {
		keys, err := s.DB.GetSecretsByPrefix(agent.AccountID, dbProvider.SecretName, s.MasterKey)
		if err != nil || len(keys) == 0 {
			return nil, http.StatusNotFound, fmt.Sprintf("no %s API key configured. Store secret '%s' in the broker.", provider, dbProvider.SecretName)
		}
		return keys, 0, ""
	}

	// Fallback to hardcoded map
	cfg, ok := inferenceProviders[provider]
	if !ok {
		return nil, http.StatusBadRequest, fmt.Sprintf("unsupported provider: %s", provider)
	}

	keys, err := s.DB.GetSecretsByPrefix(agent.AccountID, cfg.SecretName, s.MasterKey)
	if err != nil || len(keys) == 0 {
		return nil, http.StatusNotFound, fmt.Sprintf("no %s API key configured. Store secret '%s' in the broker.", provider, cfg.SecretName)
	}
	return keys, 0, ""
}

// ─── SSE Streaming Helper ──────────────────────────────────────────────────────

// tapAgentContext holds agent info for tap event publishing.
type tapAgentContext struct {
	AgentID   string
	AgentName string
	Provider  string
	Model     string
	Path      string
}

// streamInferenceResponse pipes an upstream SSE/streaming response body to the client.
// If t is non-nil, it publishes chunk events to the inference tap.
func streamInferenceResponse(w http.ResponseWriter, body io.ReadCloser, latencyMs int64, provider string, t *tap.InferenceTap, tapCtx *tapAgentContext) {
	defer body.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Spine-Latency-Ms", fmt.Sprintf("%d", latencyMs))
	w.Header().Set("X-Spine-Provider", provider)
	w.WriteHeader(http.StatusOK)

	flusher, canFlush := w.(http.Flusher)

	if t == nil || tapCtx == nil {
		// No tap — simple passthrough
		buf := make([]byte, 4096)
		for {
			n, err := body.Read(buf)
			if n > 0 {
				w.Write(buf[:n]) //nolint:errcheck
				if canFlush {
					flusher.Flush()
				}
			}
			if err != nil {
				break
			}
		}
		return
	}

	// With tap: read line-by-line (SSE is line-based), publish chunks, write to client.
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	for scanner.Scan() {
		line := scanner.Text()
		// Write the raw line + newline to client
		fmt.Fprintln(w, line) //nolint:errcheck
		if canFlush {
			flusher.Flush()
		}
		// Publish chunk event for data lines
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data != "[DONE]" && data != "" {
				t.Publish(tap.InferenceEvent{
					Type:      "chunk",
					AgentID:   tapCtx.AgentID,
					AgentName: tapCtx.AgentName,
					Provider:  tapCtx.Provider,
					Model:     tapCtx.Model,
					Path:      tapCtx.Path,
					Timestamp: time.Now().UTC().Format(time.RFC3339),
					Data:      data,
				})
			}
		}
	}

	// Publish complete event
	t.Publish(tap.InferenceEvent{
		Type:      "complete",
		AgentID:   tapCtx.AgentID,
		AgentName: tapCtx.AgentName,
		Provider:  tapCtx.Provider,
		Model:     tapCtx.Model,
		Path:      tapCtx.Path,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
}

// ─── Generic Inference Body Builders ──────────────────────────────────────────

func buildInferenceBody(provider string, rawBody map[string]interface{}) map[string]interface{} {
	body := make(map[string]interface{})
	model, _ := rawBody["model"].(string)
	messages := rawBody["messages"]
	stream, hasStream := rawBody["stream"].(bool)
	if !hasStream {
		stream = true
	}

	switch provider {
	case "anthropic":
		body["model"] = model
		body["messages"] = messages
		if mt, ok := rawBody["max_tokens"]; ok {
			body["max_tokens"] = mt
		} else {
			body["max_tokens"] = 4096
		}
		body["stream"] = stream
		for _, key := range []string{"system", "temperature", "top_p", "metadata", "stop_sequences", "tools", "tool_choice", "thinking"} {
			if v, ok := rawBody[key]; ok {
				body[key] = v
			}
		}

	case "openai":
		body["stream"] = stream
		body["model"] = model
		if sys, ok := rawBody["system"]; ok {
			msgs := []interface{}{map[string]interface{}{"role": "system", "content": sys}}
			if ms, ok := messages.([]interface{}); ok {
				msgs = append(msgs, ms...)
			}
			body["messages"] = msgs
		} else {
			body["messages"] = messages
		}
		for _, key := range []string{"max_tokens", "temperature", "top_p", "tools", "tool_choice", "response_format", "frequency_penalty", "presence_penalty", "logprobs"} {
			if v, ok := rawBody[key]; ok {
				body[key] = v
			}
		}

	case "xai":
		body["model"] = model
		body["messages"] = messages
		body["stream"] = stream
		for _, key := range []string{"max_tokens", "temperature", "top_p", "stop", "frequency_penalty", "presence_penalty", "tools", "tool_choice", "response_format"} {
			if v, ok := rawBody[key]; ok {
				body[key] = v
			}
		}

	default:
		for k, v := range rawBody {
			if k != "provider" {
				body[k] = v
			}
		}
	}

	return body
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

// handleInference handles POST /v1/inference — generic single-key, no retry.
func (s *Server) handleInference(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()

	agent, err := s.authenticateInferenceAgent(r)
	if err != nil {
		log.Printf("[inference] auth error: %v", err)
		jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if agent == nil {
		jsonError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var rawBody map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&rawBody); err != nil {
		jsonError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	providerName, _ := rawBody["provider"].(string)
	model, _ := rawBody["model"].(string)
	if providerName == "" {
		jsonError(w, http.StatusBadRequest, `Missing "provider" field`)
		return
	}
	if model == "" {
		jsonError(w, http.StatusBadRequest, `Missing "model" field`)
		return
	}
	if _, ok := rawBody["messages"]; !ok {
		jsonError(w, http.StatusBadRequest, `Missing or invalid "messages" field`)
		return
	}

	// Look up provider config — try providers table, fallback to hardcoded
	var cfg providerConfig
	dbProv, _ := s.DB.GetProviderByName(agent.AccountID, providerName)
	if dbProv != nil {
		cfg = providerConfig{BaseURL: dbProv.BaseURL, Path: "", SecretName: dbProv.SecretName}
		// Infer path from hardcoded if available
		if hc, ok := inferenceProviders[providerName]; ok {
			cfg.Path = hc.Path
		}
	} else {
		var ok bool
		cfg, ok = inferenceProviders[providerName]
		if !ok {
			jsonError(w, http.StatusBadRequest, fmt.Sprintf("unsupported provider: %s", providerName))
			return
		}
	}

	// Check scoped capability for this provider
	if !checkScopedCapability(r, "inference/"+providerName) {
		jsonError(w, http.StatusForbidden, "Scoped token does not have capability: inference/"+providerName)
		return
	}

	keys, statusCode, errMsg := s.resolveInferenceKeys(agent, providerName)
	if statusCode != 0 {
		s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "inference.request", fmt.Sprintf("inference/%s denied: %s", providerName, errMsg))
		jsonError(w, statusCode, errMsg)
		return
	}
	apiKey := pickKey(providerName, keys)

	stream, _ := rawBody["stream"].(bool)
	var oauthBundle *openAIOAuthBundleData
	var oauthAuthMode openAITokenType

	headers := map[string]string{
		"Content-Type": "application/json",
	}

	targetURL := cfg.BaseURL + cfg.Path

	switch providerName {
	case "anthropic":
		for k, v := range buildAnthropicAuthHeaders(apiKey) {
			headers[k] = v
		}
	case "openai":
		authResult, err := resolveOpenAIAuth(apiKey)
		if err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		oauthAuthMode = authResult.Mode
		oauthBundle = authResult.Bundle

		if authResult.Expires > 0 && time.Now().UnixMilli() > authResult.Expires && oauthBundle != nil && oauthBundle.Refresh != "" {
			refreshed, err := refreshOpenAICodexBundle(oauthBundle)
			if err == nil {
				s.persistOpenAIBundle(agent.AccountID, refreshed)
				oauthBundle = refreshed
				authResult = &openAIAuthResult{
					AuthHeaderValue: "Bearer " + refreshed.Access,
					Mode:            openAIOAuthBundle,
					Expires:         refreshed.Expires,
					Bundle:          refreshed,
				}
			}
		}
		if authResult.Expires > 0 && time.Now().UnixMilli() > authResult.Expires {
			jsonError(w, http.StatusUnauthorized, "OpenAI OAuth token appears expired in broker secret; re-auth required.")
			return
		}

		headers["Authorization"] = authResult.AuthHeaderValue
		if oauthAuthMode == openAIOAuthBundle {
			targetURL = "https://chatgpt.com/backend-api/codex/responses"
			headers["User-Agent"] = "CodexBar"
			headers["Accept"] = "text/event-stream"
			if oauthBundle != nil && oauthBundle.AccountID != "" {
				headers["ChatGPT-Account-Id"] = oauthBundle.AccountID
			}
			stream = true
		}
	default:
		headers["Authorization"] = "Bearer " + apiKey
	}

	msgCount := 0
	if ms, ok := rawBody["messages"].([]interface{}); ok {
		msgCount = len(ms)
	}
	s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "inference.request",
		fmt.Sprintf(`inference/%s model=%s stream=%v msgs=%d authMode=%s`,
			providerName, model, stream, msgCount, string(oauthAuthMode)))

	// Publish tap request event
	tapCtxInference := &tapAgentContext{
		AgentID:   agent.ID,
		AgentName: agent.Name,
		Provider:  providerName,
		Model:     model,
		Path:      "/v1/inference",
	}
	if s.Tap != nil {
		s.Tap.Publish(tap.InferenceEvent{
			Type:      "request",
			AgentID:   agent.ID,
			AgentName: agent.Name,
			Provider:  providerName,
			Model:     model,
			Method:    r.Method,
			Path:      "/v1/inference",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
	}

	providerBody := buildInferenceBody(providerName, rawBody)
	bodyBytes, _ := json.Marshal(providerBody)

	client := &http.Client{Timeout: 5 * time.Minute}

	req, _ := http.NewRequest(http.MethodPost, targetURL, strings.NewReader(string(bodyBytes)))
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		latencyMs := time.Since(startTime).Milliseconds()
		s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "inference.error",
			fmt.Sprintf(`inference/%s model=%s latencyMs=%d error=%s`, providerName, model, latencyMs, err.Error()))
		if s.Tap != nil {
			s.Tap.Publish(tap.InferenceEvent{
				Type:      "error",
				AgentID:   agent.ID,
				AgentName: agent.Name,
				Provider:  providerName,
				Model:     model,
				Path:      "/v1/inference",
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Data:      err.Error(),
			})
		}
		jsonError(w, http.StatusBadGateway, "Provider request failed: "+err.Error())
		return
	}

	// Retry on 401 for OpenAI OAuth
	if providerName == "openai" && resp.StatusCode == http.StatusUnauthorized && oauthBundle != nil && oauthBundle.Refresh != "" {
		refreshed, refreshErr := refreshOpenAICodexBundle(oauthBundle)
		if refreshErr == nil {
			s.persistOpenAIBundle(agent.AccountID, refreshed)
			headers["Authorization"] = "Bearer " + refreshed.Access
			resp.Body.Close()
			req2, _ := http.NewRequest(http.MethodPost, targetURL, strings.NewReader(string(bodyBytes)))
			for k, v := range headers {
				req2.Header.Set(k, v)
			}
			resp2, err2 := client.Do(req2)
			if err2 == nil {
				resp = resp2
			}
		}
	}

	latencyMs := time.Since(startTime).Milliseconds()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errorText, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "inference.error",
			fmt.Sprintf(`inference/%s model=%s status=%d latencyMs=%d error=%s`,
				providerName, model, resp.StatusCode, latencyMs, truncate(string(errorText), 500)))
		ct := resp.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/json"
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(resp.StatusCode)
		w.Write(errorText) //nolint:errcheck
		return
	}

	ct := resp.Header.Get("Content-Type")
	isSSE := strings.Contains(ct, "text/event-stream")
	forceStream := providerName == "openai" && oauthAuthMode == openAIOAuthBundle

	if (stream || forceStream) && (isSSE || forceStream) {
		s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "inference.streaming",
			fmt.Sprintf(`inference/%s model=%s latencyMs=%d`, providerName, model, latencyMs))
		streamInferenceResponse(w, resp.Body, latencyMs, providerName, s.Tap, tapCtxInference)
		return
	}

	responseBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	latencyMs = time.Since(startTime).Milliseconds()

	var tokensIn, tokensOut int
	var parsed map[string]interface{}
	if json.Unmarshal(responseBody, &parsed) == nil {
		if usage, ok := parsed["usage"].(map[string]interface{}); ok {
			tokensIn = inferenceIntFromMap(usage, "input_tokens", "prompt_tokens")
			tokensOut = inferenceIntFromMap(usage, "output_tokens", "completion_tokens")
		}
	}

	s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "inference.complete",
		fmt.Sprintf(`inference/%s model=%s latencyMs=%d tokensIn=%d tokensOut=%d`,
			providerName, model, latencyMs, tokensIn, tokensOut))

	if s.Tap != nil {
		s.Tap.Publish(tap.InferenceEvent{
			Type:      "complete",
			AgentID:   agent.ID,
			AgentName: agent.Name,
			Provider:  providerName,
			Model:     model,
			Path:      "/v1/inference",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			TokensIn:  tokensIn,
			TokensOut: tokensOut,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Spine-Latency-Ms", fmt.Sprintf("%d", latencyMs))
	w.Header().Set("X-Spine-Provider", providerName)
	w.Header().Set("X-Spine-Tokens-In", fmt.Sprintf("%d", tokensIn))
	w.Header().Set("X-Spine-Tokens-Out", fmt.Sprintf("%d", tokensOut))
	w.WriteHeader(http.StatusOK)
	w.Write(responseBody) //nolint:errcheck
}

// handleProxyAnthropic handles POST /v1/proxy/anthropic/* — transparent proxy with round-robin + retry.
func (s *Server) handleProxyAnthropic(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()

	agent, err := s.authenticateInferenceAgent(r)
	if err != nil {
		log.Printf("[proxy/anthropic] auth error: %v", err)
		jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if agent == nil {
		jsonError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	keys, statusCode, errMsg := s.resolveInferenceKeys(agent, "anthropic")
	if statusCode != 0 {
		s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "proxy.request", "inference/anthropic denied: "+errMsg)
		jsonError(w, statusCode, errMsg)
		return
	}

	// Check scoped capability
	if !checkScopedCapability(r, "inference/anthropic") {
		jsonError(w, http.StatusForbidden, "Scoped token does not have capability: inference/anthropic")
		return
	}

	fullPath := r.URL.Path
	providerPath := strings.TrimPrefix(fullPath, "/v1/proxy/anthropic")
	if providerPath == "" {
		providerPath = "/"
	}
	upstreamURL := "https://api.anthropic.com" + providerPath
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}

	requestBody, err := io.ReadAll(r.Body)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "Failed to read request body")
		return
	}

	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}

	model := "unknown"
	var parsedBody map[string]interface{}
	if json.Unmarshal(requestBody, &parsedBody) == nil {
		if m, ok := parsedBody["model"].(string); ok {
			model = m
		}
	}

	clientBeta := r.Header.Get("anthropic-beta")
	clientVersion := r.Header.Get("anthropic-version")

	// Publish tap request event
	tapCtxAnthropic := &tapAgentContext{
		AgentID:   agent.ID,
		AgentName: agent.Name,
		Provider:  "anthropic",
		Model:     model,
		Path:      providerPath,
	}
	if s.Tap != nil {
		s.Tap.Publish(tap.InferenceEvent{
			Type:      "request",
			AgentID:   agent.ID,
			AgentName: agent.Name,
			Provider:  "anthropic",
			Model:     model,
			Method:    r.Method,
			Path:      providerPath,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
	}

	maxAttempts := len(keys)
	if maxAttempts > 5 {
		maxAttempts = 5
	}

	var lastRateLimitResp []byte
	var lastRateLimitStatus int

	client := &http.Client{Timeout: 5 * time.Minute}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		apiKey := pickKey("anthropic", keys)

		authHeaders := buildAnthropicAuthHeaders(apiKey)
		headers := map[string]string{
			"Content-Type": contentType,
		}
		for k, v := range authHeaders {
			headers[k] = v
		}

		if clientBeta != "" {
			if existing, ok := headers["anthropic-beta"]; ok && existing != clientBeta {
				headers["anthropic-beta"] = existing + "," + clientBeta
			} else {
				headers["anthropic-beta"] = clientBeta
			}
		}
		if clientVersion != "" {
			headers["anthropic-version"] = clientVersion
		}

		s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "proxy.request",
			fmt.Sprintf(`inference/anthropic model=%s path=%s attempt=%d totalKeys=%d`,
				model, providerPath, attempt+1, len(keys)))

		req, _ := http.NewRequest(http.MethodPost, upstreamURL, strings.NewReader(string(requestBody)))
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := client.Do(req)
		if err != nil {
			s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "proxy.error",
				fmt.Sprintf(`inference/anthropic model=%s error=%s attempt=%d`, model, err.Error(), attempt+1))
			if attempt == maxAttempts-1 {
				jsonError(w, http.StatusBadGateway, "Provider request failed: "+err.Error())
				return
			}
			continue
		}

		latencyMs := time.Since(startTime).Milliseconds()

		if (resp.StatusCode == 429 || resp.StatusCode == 529) && attempt < maxAttempts-1 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastRateLimitStatus = resp.StatusCode
			lastRateLimitResp = body
			s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "proxy.rate_limited",
				fmt.Sprintf(`inference/anthropic model=%s status=%d attempt=%d latencyMs=%d`,
					model, resp.StatusCode, attempt+1, latencyMs))
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			errorText, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "proxy.error",
				fmt.Sprintf(`inference/anthropic model=%s status=%d latencyMs=%d`, model, resp.StatusCode, latencyMs))
			ct := resp.Header.Get("Content-Type")
			if ct == "" {
				ct = "application/json"
			}
			w.Header().Set("Content-Type", ct)
			w.WriteHeader(resp.StatusCode)
			w.Write(errorText) //nolint:errcheck
			return
		}

		if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
			s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "proxy.streaming",
				fmt.Sprintf(`inference/anthropic model=%s latencyMs=%d keyIdx=%d totalKeys=%d`,
					model, latencyMs, attempt+1, len(keys)))
			streamInferenceResponse(w, resp.Body, latencyMs, "anthropic", s.Tap, tapCtxAnthropic)
			return
		}

		responseBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "proxy.complete",
			fmt.Sprintf(`inference/anthropic model=%s latencyMs=%d keyIdx=%d totalKeys=%d`,
				model, latencyMs, attempt+1, len(keys)))

		if s.Tap != nil {
			s.Tap.Publish(tap.InferenceEvent{
				Type:      "complete",
				AgentID:   agent.ID,
				AgentName: agent.Name,
				Provider:  "anthropic",
				Model:     model,
				Path:      providerPath,
				Timestamp: time.Now().UTC().Format(time.RFC3339),
			})
		}

		ct := resp.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/json"
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(http.StatusOK)
		w.Write(responseBody) //nolint:errcheck
		return
	}

	if lastRateLimitStatus != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(lastRateLimitStatus)
		w.Write(lastRateLimitResp) //nolint:errcheck
		return
	}
	jsonError(w, http.StatusTooManyRequests, "All provider keys exhausted")
}

// handleProxyOpenAI handles POST /v1/proxy/openai/* — transparent OpenAI proxy.
func (s *Server) handleProxyOpenAI(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()

	agent, err := s.authenticateInferenceAgent(r)
	if err != nil {
		log.Printf("[proxy/openai] auth error: %v", err)
		jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if agent == nil {
		jsonError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	keys, statusCode, errMsg := s.resolveInferenceKeys(agent, "openai")
	if statusCode != 0 {
		s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "proxy.request", "inference/openai denied: "+errMsg)
		jsonError(w, statusCode, errMsg)
		return
	}

	// Check scoped capability
	if !checkScopedCapability(r, "inference/openai") {
		jsonError(w, http.StatusForbidden, "Scoped token does not have capability: inference/openai")
		return
	}

	apiKey := pickKey("openai", keys)

	authResult, err := resolveOpenAIAuth(apiKey)
	if err != nil {
		s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "proxy.request", "inference/openai denied: "+err.Error())
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	if authResult.Expires > 0 && time.Now().UnixMilli() > authResult.Expires && authResult.Bundle != nil && authResult.Bundle.Refresh != "" {
		refreshed, refreshErr := refreshOpenAICodexBundle(authResult.Bundle)
		if refreshErr == nil {
			s.persistOpenAIBundle(agent.AccountID, refreshed)
			authResult = &openAIAuthResult{
				AuthHeaderValue: "Bearer " + refreshed.Access,
				Mode:            openAIOAuthBundle,
				Expires:         refreshed.Expires,
				Bundle:          refreshed,
			}
		}
	}

	if authResult.Expires > 0 && time.Now().UnixMilli() > authResult.Expires {
		errMsg := "OpenAI OAuth token appears expired in broker secret; re-auth required."
		s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "proxy.request", "inference/openai denied: "+errMsg)
		jsonError(w, http.StatusUnauthorized, errMsg)
		return
	}

	fullPath := r.URL.Path
	rawProviderPath := strings.TrimPrefix(fullPath, "/v1/proxy/openai")
	if rawProviderPath == "" {
		rawProviderPath = "/"
	}
	providerPath := rawProviderPath
	if providerPath == "/embeddings" {
		providerPath = "/v1/embeddings"
	}

	requestBody, err := io.ReadAll(r.Body)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "Failed to read request body")
		return
	}

	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}

	model := "unknown"
	var parsedBody map[string]interface{}
	if json.Unmarshal(requestBody, &parsedBody) == nil {
		if m, ok := parsedBody["model"].(string); ok {
			model = m
		}
	}

	isEmbedding := strings.HasPrefix(providerPath, "/v1/embeddings") || strings.Contains(model, "embedding")
	effectiveAuth := authResult.AuthHeaderValue
	effectiveMode := authResult.Mode

	if isEmbedding && authResult.Mode == openAIOAuthBundle {
		embKey, err := s.DB.GetSecretForAgent(agent.AccountID, agent.ID, "OPENAI_API_KEY", s.MasterKey)
		if err != nil {
			errMsg := "Embedding request requires OPENAI_API_KEY in broker secret store (OPENAI_TOKEN oauth bundle cannot access embeddings)."
			s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "proxy.request", "inference/openai denied: "+errMsg)
			jsonError(w, http.StatusBadRequest, errMsg)
			return
		}
		effectiveAuth = "Bearer " + embKey
		effectiveMode = openAIAPIKey
	}

	shouldUseCodex := effectiveMode == openAIOAuthBundle
	var finalBody []byte
	var targetURL string
	headers := map[string]string{
		"Authorization": effectiveAuth,
	}

	if shouldUseCodex && parsedBody != nil {
		targetURL = "https://chatgpt.com/backend-api/codex/responses"
		instructions := "You are a helpful assistant."
		if v, ok := parsedBody["instructions"]; ok {
			instructions = fmt.Sprintf("%v", v)
		}
		inputVal := parsedBody["input"]
		if inputVal == nil {
			inputVal = []interface{}{}
		}
		codexBody := map[string]interface{}{
			"model":        model,
			"instructions": instructions,
			"input":        inputVal,
			"stream":       true,
			"store":        false,
		}
		finalBody, _ = json.Marshal(codexBody)
		headers["Content-Type"] = "application/json"
		headers["User-Agent"] = "CodexBar"
		headers["Accept"] = "text/event-stream"
		if authResult.Bundle != nil && authResult.Bundle.AccountID != "" {
			headers["ChatGPT-Account-Id"] = authResult.Bundle.AccountID
		}
	} else {
		targetURL = "https://api.openai.com" + providerPath
		if r.URL.RawQuery != "" {
			targetURL += "?" + r.URL.RawQuery
		}
		finalBody = requestBody
		headers["Content-Type"] = contentType
	}

	logPath := providerPath
	if shouldUseCodex {
		logPath = "/codex/responses"
	}
	s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "proxy.request",
		fmt.Sprintf(`inference/openai model=%s path=%s authMode=%s oauthExpires=%d`,
			model, logPath, string(effectiveMode), authResult.Expires))

	tapCtxOpenAI := &tapAgentContext{
		AgentID:   agent.ID,
		AgentName: agent.Name,
		Provider:  "openai",
		Model:     model,
		Path:      logPath,
	}
	if s.Tap != nil {
		s.Tap.Publish(tap.InferenceEvent{
			Type:      "request",
			AgentID:   agent.ID,
			AgentName: agent.Name,
			Provider:  "openai",
			Model:     model,
			Method:    r.Method,
			Path:      logPath,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
	}

	client := &http.Client{Timeout: 5 * time.Minute}

	makeReq := func(body []byte) (*http.Response, error) {
		req, _ := http.NewRequest(http.MethodPost, targetURL, strings.NewReader(string(body)))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		return client.Do(req)
	}

	resp, err := makeReq(finalBody)
	if err != nil {
		s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "proxy.error",
			fmt.Sprintf(`inference/openai model=%s error=%s`, model, err.Error()))
		jsonError(w, http.StatusBadGateway, "Provider request failed: "+err.Error())
		return
	}

	if resp.StatusCode == http.StatusUnauthorized && effectiveMode == openAIOAuthBundle && authResult.Bundle != nil && authResult.Bundle.Refresh != "" {
		refreshed, refreshErr := refreshOpenAICodexBundle(authResult.Bundle)
		if refreshErr == nil {
			s.persistOpenAIBundle(agent.AccountID, refreshed)
			headers["Authorization"] = "Bearer " + refreshed.Access
			resp.Body.Close()
			resp, err = makeReq(finalBody)
			if err != nil {
				jsonError(w, http.StatusBadGateway, "Provider request failed after token refresh: "+err.Error())
				return
			}
		}
	}

	latencyMs := time.Since(startTime).Milliseconds()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errorText, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "proxy.error",
			fmt.Sprintf(`inference/openai model=%s status=%d latencyMs=%d`, model, resp.StatusCode, latencyMs))
		ct := resp.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/json"
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(resp.StatusCode)
		w.Write(errorText) //nolint:errcheck
		return
	}

	upstreamCT := resp.Header.Get("Content-Type")
	if strings.Contains(upstreamCT, "text/event-stream") || shouldUseCodex {
		s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "proxy.streaming",
			fmt.Sprintf(`inference/openai model=%s latencyMs=%d`, model, latencyMs))
		streamInferenceResponse(w, resp.Body, latencyMs, "openai", s.Tap, tapCtxOpenAI)
		return
	}

	responseBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "proxy.complete",
		fmt.Sprintf(`inference/openai model=%s latencyMs=%d`, model, latencyMs))

	if s.Tap != nil {
		s.Tap.Publish(tap.InferenceEvent{
			Type:      "complete",
			AgentID:   agent.ID,
			AgentName: agent.Name,
			Provider:  "openai",
			Model:     model,
			Path:      logPath,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
	}

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(http.StatusOK)
	w.Write(responseBody) //nolint:errcheck
}

// handleInferenceProviders handles GET /v1/inference/providers — list configured providers.
func (s *Server) handleInferenceProviders(w http.ResponseWriter, r *http.Request) {
	agent, err := s.authenticateInferenceAgent(r)
	if err != nil {
		log.Printf("[inference/providers] auth error: %v", err)
		jsonError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if agent == nil {
		jsonError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	type providerStatus struct {
		Name       string `json:"name"`
		Configured bool   `json:"configured"`
		SecretName string `json:"secretName"`
	}

	// Collect from providers table first
	seen := make(map[string]bool)
	result := make([]providerStatus, 0)

	dbProviders, _ := s.DB.ListProviders(agent.AccountID)
	for _, p := range dbProviders {
		_, err := s.DB.GetSecretForAgent(agent.AccountID, agent.ID, p.SecretName, s.MasterKey)
		result = append(result, providerStatus{
			Name:       p.Name,
			Configured: err == nil,
			SecretName: p.SecretName,
		})
		seen[p.Name] = true
	}

	// Fallback: add hardcoded providers not already in the table
	for name, cfg := range inferenceProviders {
		if seen[name] {
			continue
		}
		_, err := s.DB.GetSecretForAgent(agent.AccountID, agent.ID, cfg.SecretName, s.MasterKey)
		result = append(result, providerStatus{
			Name:       name,
			Configured: err == nil,
			SecretName: cfg.SecretName,
		})
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"ok":        true,
		"providers": result,
	})
}

// ─── Helpers ───────────────────────────────────────────────────────────────────

func inferenceIntFromMap(m map[string]interface{}, keys ...string) int {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch n := v.(type) {
			case float64:
				return int(n)
			case int:
				return n
			}
		}
	}
	return 0
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
