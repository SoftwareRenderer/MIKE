package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"kanban/backend/auth"
)

const (
	sessionCookieName = "PHPSESSID"
	sessionMaxAge     = 30 * 24 * 3600 // 30 days in seconds
)

// sessionCookieSecure is set at server startup based on the bind address.
var sessionCookieSecure bool

// handleAuthLogin handles POST /api/auth/login.
// Accepts {"password": "..."}, validates against KANBAN_PASSWORD,
// sets SESSION cookie on success.
func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		JSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Password == "" {
		JSONError(w, http.StatusBadRequest, "password required")
		return
	}

	_, token, csrfToken, err := s.sessionMgr.Login(req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidPassword) {
			JSONError(w, http.StatusUnauthorized, "invalid password")
		} else {
			JSONError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}

	setSessionCookie(w, token, sessionMaxAge)
	w.Header().Set("X-CSRF-Token", csrfToken)
	JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleAuthLogout handles POST /api/auth/logout.
// Requires a valid session. Deletes the session and clears the cookie.
func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		JSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		JSONError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	if _, _, err := s.sessionMgr.Validate(cookie.Value); err != nil {
		JSONError(w, http.StatusUnauthorized, "invalid session")
		return
	}

	_ = s.sessionMgr.Logout(cookie.Value)
	clearSessionCookie(w)
	JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleAuthSession handles GET /api/auth/session.
// Returns the current authentication status.
func (s *Server) handleAuthSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		JSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	cookie, err := r.Cookie(sessionCookieName)
	if err == nil && s.sessionMgr != nil {
		if _, csrfToken, err := s.sessionMgr.Validate(cookie.Value); err == nil {
			JSONResponse(w, http.StatusOK, map[string]any{"authenticated": true, "csrf_token": csrfToken})
			return
		}
	}

	JSONResponse(w, http.StatusOK, map[string]bool{"authenticated": false})
}

// setSessionCookie sets the SESSION cookie with secure flags.
func setSessionCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   sessionCookieSecure,
	})
}

// clearSessionCookie removes the SESSION cookie.
func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   sessionCookieSecure,
	})
}

// sessionDuration returns the session max age.
var sessionDuration = 30 * 24 * time.Hour
