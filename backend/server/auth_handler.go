package server

import (
	"net/http"

	"mike/pkg/logging"
)

const (
	sessionCookieName = "PHPSESSID"
	sessionMaxAge     = 30 * 24 * 3600 // 30 days in seconds
)

// The SESSION cookie's Secure flag comes from requestIsHTTPS: TLS terminated here, or a
// TLS-terminating proxy that said so under MIKE_TRUST_PROXY. There is deliberately no variable for
// it — a flag an operator has to know about is a flag that is off during the deployment it is for.

// handleAuthLogout handles POST /api/auth/logout.
// Requires a valid session. Deletes the session and clears the cookie.
func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {

	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		JSONError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	if _, _, err := s.sessionMgr.Validate(r.Context(), cookie.Value); err != nil {
		JSONError(w, http.StatusUnauthorized, "invalid session")
		return
	}

	// Detached like login: a logout whose delete is cancelled leaves the session live.
	ctx, cancel := s.Detached()
	defer cancel()
	_ = s.sessionMgr.Logout(ctx, cookie.Value)
	s.clearSessionCookie(w, r)
	JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleAuthSession handles GET /api/auth/session.
// Returns the current authentication status, and whether this install has any passkey at all — the
// boolean is the login form's branch between "sign in" and "type the code", and nothing else about a
// credential is ever answered here.
func (s *Server) handleAuthSession(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{}
	// A store fault leaves the field out rather than guessing: a form that does not know shows the
	// sign-in branch, and the ceremony it starts answers for itself.
	if has, err := s.passkeys.HasPasskeys(r.Context()); err == nil {
		body["has_passkey"] = has
	} else {
		logging.For(logging.Auth).Warnf("cannot tell the login form whether this install has a passkey: %v", err)
	}

	cookie, err := r.Cookie(sessionCookieName)
	if err == nil && s.sessionMgr != nil {
		if _, csrfToken, err := s.sessionMgr.Validate(r.Context(), cookie.Value); err == nil {
			body["authenticated"] = true
			body["csrf_token"] = csrfToken
			JSONResponse(w, http.StatusOK, body)
			return
		}
	}

	body["authenticated"] = false
	JSONResponse(w, http.StatusOK, body)
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
