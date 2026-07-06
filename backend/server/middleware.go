package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"strings"

	"kanban/backend/httputil"
)

// Re-export JSON helpers from httputil.
var (
	JSONResponse = httputil.JSONResponse
	JSONError    = httputil.JSONError
	JSONErrorf   = httputil.JSONErrorf
)

// Paths that don't require authentication.
var publicPaths = map[string]map[string]struct{}{
	"/api/health":       {http.MethodGet: {}},
	"/api/auth/login":   {http.MethodPost: {}},
	"/api/auth/session": {http.MethodGet: {}},
}

// isPublicPath returns true if the given path+method combination is publicly accessible.
// Static assets (frontend) are always public.
func isPublicPath(path, method string) bool {
	// Frontend static assets are always public
	if path == "/" || strings.HasPrefix(path, "/assets/") || strings.HasPrefix(path, "/favicon") {
		return true
	}
	// PWA assets (manifest, service worker, icons) are always public
	if path == "/manifest.webmanifest" ||
		path == "/sw.js" ||
		path == "/registerSW.js" ||
		strings.HasPrefix(path, "/workbox-") ||
		strings.HasPrefix(path, "/pwa-icon-") {
		return true
	}
	if methods, ok := publicPaths[path]; ok {
		_, ok := methods[method]
		return ok
	}
	return false
}

// CORS middleware.
func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip CORS headers for WebSocket upgrades.
		// WebSockets don't use CORS, and extra headers on the 101 upgrade
		// response can delay nginx's protocol switch detection.
		if strings.Contains(r.Header.Get("Upgrade"), "websocket") {
			next.ServeHTTP(w, r)
			return
		}

		// Reflect the request Origin (required for cookies with credentials).
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		} else {
			// Same-origin request (e.g., browser navigating directly)
			// No CORS headers needed
		}

		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Cookie")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sessionKey is the context key for the authenticated session id.
type sessionKey struct{}

// SessionMiddleware validates the PHPSESSID cookie and injects auth state into context.
// Public paths (health check, login, session check) bypass validation.
func (s *Server) SessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow public paths without auth
		if isPublicPath(r.URL.Path, r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			JSONError(w, http.StatusUnauthorized, "not authenticated")
			return
		}

		sessionID, _, err := s.sessionMgr.Validate(cookie.Value)
		if err != nil {
			JSONError(w, http.StatusUnauthorized, "invalid session")
			return
		}

		// Inject session id into context for downstream handlers
		ctx := context.WithValue(r.Context(), sessionKey{}, sessionID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// PanicRecoveryMiddleware recovers from panics and returns a JSON error.
func PanicRecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("[PANIC] %s %s: %v\n%s", r.Method, r.URL.Path, err, debug.Stack())
				JSONError(w, http.StatusInternalServerError, fmt.Sprintf("internal server error: %v", err))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// csrfKey is the context key for the CSRF token.
type csrfKey struct{}

// CSRFMiddleware validates the X-CSRF-Token header against the stored token.
// GET, HEAD, and OPTIONS requests are exempt. Public paths are exempt.
func (s *Server) CSRFMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip safe/idempotent methods
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		// Skip public paths
		if isPublicPath(r.URL.Path, r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		// Extract CSRF token from header
		csrfHeader := r.Header.Get("X-CSRF-Token")
		if csrfHeader == "" {
			JSONError(w, http.StatusForbidden, "missing CSRF token")
			return
		}

		// Look up session to get stored CSRF token
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			JSONError(w, http.StatusForbidden, "missing session")
			return
		}

		_, storedCSRF, err := s.sessionMgr.Validate(cookie.Value)
		if err != nil {
			JSONError(w, http.StatusForbidden, "invalid session")
			return
		}

		if csrfHeader != storedCSRF {
			JSONError(w, http.StatusForbidden, "invalid CSRF token")
			return
		}

		// Inject CSRF token into context
		ctx := context.WithValue(r.Context(), csrfKey{}, storedCSRF)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}


