package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"mike/backend/auth"
)

const (
	sessionCookieName = "PHPSESSID"
	sessionMaxAge     = 30 * 24 * 3600 // 30 days in seconds
)

// The SESSION cookie's Secure flag comes from requestIsHTTPS: TLS terminated here, or a
// TLS-terminating proxy that said so under MIKE_TRUST_PROXY. There is deliberately no variable for
// it — a flag an operator has to know about is a flag that is off during the deployment it is for.

// handleAuthLogin handles POST /api/auth/login.
// Accepts {"password": "..."}, validates against MIKE_LOGIN_PASSWORD,
// sets SESSION cookie on success.
func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Password == "" {
		JSONError(w, http.StatusBadRequest, "password required")
		return
	}

	_, token, csrfToken, err := s.sessionMgr.Login(req.Password, clientIP(r, s.settings.TrustProxy), r.UserAgent())
	if err != nil {
		if errors.Is(err, auth.ErrInvalidPassword) {
			JSONError(w, http.StatusUnauthorized, "invalid password")
		} else {
			JSONError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}

	s.setSessionCookie(w, r, token, sessionMaxAge)
	w.Header().Set("X-CSRF-Token", csrfToken)
	JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleAuthLogout handles POST /api/auth/logout.
// Requires a valid session. Deletes the session and clears the cookie.
func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {

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
	s.clearSessionCookie(w, r)
	JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleAuthSession handles GET /api/auth/session.
// Returns the current authentication status.
func (s *Server) handleAuthSession(w http.ResponseWriter, r *http.Request) {

	cookie, err := r.Cookie(sessionCookieName)
	if err == nil && s.sessionMgr != nil {
		if _, csrfToken, err := s.sessionMgr.Validate(cookie.Value); err == nil {
			JSONResponse(w, http.StatusOK, map[string]any{"authenticated": true, "csrf_token": csrfToken})
			return
		}
	}

	JSONResponse(w, http.StatusOK, map[string]bool{"authenticated": false})
}

// sessionCookie is the SESSION cookie with its secure flags. The Secure flag is decided per request
// (see requestIsHTTPS), never once at boot: the same server answers cleartext on loopback for
// development and HTTPS behind a proxy, and a boot-time answer is wrong for one of them.
func (s *Server) sessionCookie(r *http.Request, token string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsHTTPS(r, s.settings.TrustProxy),
	}
}

// setSessionCookie sets the SESSION cookie.
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, maxAge int) {
	http.SetCookie(w, s.sessionCookie(r, token, maxAge))
}

// clearSessionCookie removes the SESSION cookie. It has to be written with the same Secure flag it
// was set with, or the browser keeps the cookie it already has and "logout" leaves a live session
// in the jar.
func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, s.sessionCookie(r, "", -1))
}
