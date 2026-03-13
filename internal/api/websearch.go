// Package api — Web Search Proxy
//
// Ports web-search.ts from the TypeScript broker reference implementation.
// Two-tier Brave Search with automatic fallback:
//   1. FREE_BRAVE_AI_TOKEN  — free tier (2,000/mo cap)
//   2. BRAVE_BASE_AI_TOKEN  — paid tier (no cap, billed per 1,000)
//
// If both exist: try free first, fall back to paid on 429.
// If only one exists: use it.
// If neither exists: 404.

package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	braveSearchEndpoint = "https://api.search.brave.com/res/v1/web/search"
	braveDefaultTimeout = 15 * time.Second
)

type webSearchParams struct {
	Query      string `json:"query"`
	Count      int    `json:"count,omitempty"`
	Country    string `json:"country,omitempty"`
	SearchLang string `json:"search_lang,omitempty"`
	UILang     string `json:"ui_lang,omitempty"`
	Freshness  string `json:"freshness,omitempty"`
}

func buildBraveSearchURL(params webSearchParams) string {
	u, _ := url.Parse(braveSearchEndpoint)
	q := u.Query()
	q.Set("q", params.Query)
	if params.Count > 0 {
		count := params.Count
		if count > 20 {
			count = 20
		}
		q.Set("count", strconv.Itoa(count))
	}
	if params.Country != "" {
		q.Set("country", params.Country)
	}
	if params.SearchLang != "" {
		q.Set("search_lang", params.SearchLang)
	}
	if params.UILang != "" {
		q.Set("ui_lang", params.UILang)
	}
	if params.Freshness != "" {
		q.Set("freshness", params.Freshness)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

type braveSearchResult struct {
	OK     bool        `json:"ok"`
	Status int         `json:"status,omitempty"`
	Tier   string      `json:"tier,omitempty"`
	Body   interface{} `json:"body,omitempty"`
	Error  string      `json:"error,omitempty"`
}

func dosBraveSearch(searchURL, token string) (ok bool, status int, body interface{}, err error) {
	client := &http.Client{Timeout: braveDefaultTimeout}
	req, _ := http.NewRequest(http.MethodGet, searchURL, nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", token)

	resp, err := client.Do(req)
	if err != nil {
		return false, 0, nil, fmt.Errorf("brave search request: %w", err)
	}
	defer resp.Body.Close()

	rawBody, _ := io.ReadAll(resp.Body)
	var parsed interface{}
	if jsonErr := json.Unmarshal(rawBody, &parsed); jsonErr != nil {
		parsed = string(rawBody)
	}

	return resp.StatusCode >= 200 && resp.StatusCode < 300, resp.StatusCode, parsed, nil
}

// handleWebSearch handles POST /v1/web/search — tiered Brave Search proxy.
func (s *Server) handleWebSearch(w http.ResponseWriter, r *http.Request) {
	agent, err := s.authenticateInferenceAgent(r)
	if err != nil {
		log.Printf("[web/search] auth error: %v", err)
		jsonError(w, http.StatusInternalServerError, "[BSA:SPINE/WEBSEARCH] internal error")
		return
	}
	if agent == nil {
		jsonError(w, http.StatusUnauthorized, "[BSA:SPINE/WEBSEARCH] Unauthorized")
		return
	}

	var params webSearchParams
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		jsonError(w, http.StatusBadRequest, "[BSA:SPINE/WEBSEARCH] Invalid JSON body")
		return
	}
	if params.Query == "" {
		jsonError(w, http.StatusBadRequest, "[BSA:SPINE/WEBSEARCH] Missing required field: query")
		return
	}

	// Resolve tokens (best-effort; nil = not configured)
	freeToken, _ := s.DB.GetSecretForAgent(agent.AccountID, agent.ID, "FREE_BRAVE_AI_TOKEN", s.MasterKey)
	paidToken, _ := s.DB.GetSecretForAgent(agent.AccountID, agent.ID, "BRAVE_BASE_AI_TOKEN", s.MasterKey)

	if freeToken == "" && paidToken == "" {
		s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "web_search", "brave denied: no tokens configured")
		jsonError(w, http.StatusNotFound, "[BSA:SPINE/WEBSEARCH] No Brave Search tokens configured (FREE_BRAVE_AI_TOKEN or BRAVE_BASE_AI_TOKEN)")
		return
	}

	searchURL := buildBraveSearchURL(params)

	// Tier 1: free token
	if freeToken != "" {
		ok, status, body, err := dosBraveSearch(searchURL, freeToken)
		if err != nil {
			log.Printf("[web/search] free tier error: %v", err)
		} else if ok {
			s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "web_search", "brave success tier=free")
			jsonResponse(w, http.StatusOK, braveSearchResult{OK: true, Tier: "free", Body: body})
			return
		} else if status == http.StatusTooManyRequests && paidToken != "" {
			s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "web_search", "brave free tier 429, trying paid")
			// fall through to paid
		} else if status == http.StatusTooManyRequests {
			s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "web_search", "brave free tier 429, no paid fallback")
			jsonResponse(w, http.StatusTooManyRequests, braveSearchResult{
				OK:    false,
				Tier:  "free",
				Error: "Free tier rate limited (429) and no paid token configured",
			})
			return
		} else {
			s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "web_search", fmt.Sprintf("brave free tier error %d", status))
			jsonResponse(w, status, braveSearchResult{
				OK:     false,
				Tier:   "free",
				Status: status,
				Error:  fmt.Sprintf("Brave API error (%d)", status),
				Body:   body,
			})
			return
		}
	}

	// Tier 2: paid token
	if paidToken != "" {
		tier := "paid"
		if freeToken != "" {
			tier = "paid (fallback)"
		}
		ok, status, body, err := dosBraveSearch(searchURL, paidToken)
		if err != nil {
			log.Printf("[web/search] paid tier error: %v", err)
			jsonError(w, http.StatusBadGateway, "[BSA:SPINE/WEBSEARCH] Brave Search request failed: "+err.Error())
			return
		}
		if ok {
			s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "web_search", "brave success tier="+tier)
			jsonResponse(w, http.StatusOK, braveSearchResult{OK: true, Tier: "paid", Body: body})
			return
		}
		s.DB.LogAudit(&agent.AccountID, &agent.ID, nil, "web_search", fmt.Sprintf("brave %s error %d", tier, status))
		jsonResponse(w, status, braveSearchResult{
			OK:     false,
			Tier:   "paid",
			Status: status,
			Error:  fmt.Sprintf("Brave API error (%d)", status),
			Body:   body,
		})
		return
	}

	jsonError(w, http.StatusInternalServerError, "[BSA:SPINE/WEBSEARCH] No tokens available")
}
