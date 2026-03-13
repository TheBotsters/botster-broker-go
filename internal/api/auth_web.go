package api

import (
	"encoding/json"
	"log"
	"net/http"
	"time"
)

const sessionCookieName = "broker_session"

// handleLogin authenticates a user and sets a session cookie.
// POST /auth/login {"email": "...", "password": "..."}
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, 400, "[BSA:SPINE/AUTH] Invalid request body")
		return
	}
	if req.Email == "" || req.Password == "" {
		jsonError(w, 400, "[BSA:SPINE/AUTH] Email and password required")
		return
	}

	account, err := s.DB.GetAccountByEmail(req.Email)
	if err != nil {
		log.Printf("[auth] login error: %v", err)
		jsonError(w, 500, "[BSA:SPINE/AUTH] Internal error")
		return
	}
	if account == nil || !account.VerifyPassword(req.Password) {
		// Constant-time-ish: don't reveal whether email exists
		jsonError(w, 401, "[BSA:SPINE/AUTH] Invalid email or password")
		return
	}

	sess := s.Sessions.Create(account.ID, account.Email)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sess.Token,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(s.Sessions.ttl.Seconds()),
	})

	log.Printf("[auth] login: %s (account %s)", account.Email, account.ID)
	jsonResponse(w, 200, map[string]string{"status": "ok", "email": account.Email})
}

// handleLogout clears the session cookie and removes the server-side session.
// POST /auth/logout
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil {
		s.Sessions.Delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	jsonResponse(w, 200, map[string]string{"status": "ok"})
}

// handleAuthStatus returns the current session info.
// GET /auth/status
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	sess := s.authenticateWeb(r)
	if sess == nil {
		jsonResponse(w, 200, map[string]interface{}{"authenticated": false})
		return
	}
	jsonResponse(w, 200, map[string]interface{}{
		"authenticated": true,
		"email":         sess.Email,
		"expiresAt":     sess.ExpiresAt.Format(time.RFC3339),
	})
}

// authenticateWeb extracts and validates the session from a request.
// Returns nil if not authenticated.
func (s *Server) authenticateWeb(r *http.Request) *Session {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil
	}
	return s.Sessions.Get(cookie.Value)
}

// requireAuth is middleware that rejects unauthenticated requests.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := s.authenticateWeb(r)
		if sess == nil {
			// If requesting HTML page, redirect to login
			if r.Header.Get("Accept") != "application/json" && r.Method == "GET" {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			jsonError(w, 401, "[BSA:SPINE/AUTH] Not authenticated")
			return
		}
		// Store session in context for downstream handlers
		r.Header.Set("X-Account-ID", sess.AccountID)
		r.Header.Set("X-Account-Email", sess.Email)
		next.ServeHTTP(w, r)
	})
}

// handleChatPage serves the chat UI for a specific agent.
// GET /chat/{agent}/
func (s *Server) handleChatPage(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "web/chat/agent.html")
}

// handleChatIndex serves the chat agent selector page.
func (s *Server) handleChatIndex(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "web/chat/index.html")
}
