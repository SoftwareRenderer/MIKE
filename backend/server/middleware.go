package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
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
	if path == "/" || path == "/host-client.js" || strings.HasPrefix(path, "/assets/") || strings.HasPrefix(path, "/favicon") {
		return true
	}
	// Extension static assets (extension code, UI entrypoints) are always
	// public: sandboxed UI extensions run in opaque-origin iframes, which are
	// cross-site and cannot send SameSite=Lax session cookies to authenticate.
	// The extension code itself executes inside the sandboxed iframe.
	if strings.HasPrefix(path, "/extensions/") {
		return true
	}
	// Preview docs/assets (the /nav control endpoint stays session-authed)
	if strings.HasPrefix(path, "/api/preview/") && !isPreviewNavPath(path) {
		return method == http.MethodGet || method == http.MethodHead
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

// isPreviewNavPath reports whether the path is exactly
// /api/preview/<token>/nav (4 path segments) — the session-authed watchdog
// endpoint. A file named "nav" inside a subdirectory has more segments, so
// it stays a public preview asset, not this route.
func isPreviewNavPath(path string) bool {
	if !strings.HasPrefix(path, "/api/preview/") || !strings.HasSuffix(path, "/nav") {
		return false
	}
	return strings.Count(path, "/") == 4
}

// CORSMiddleware handles CORS, CSP, and other security headers.
func (s *Server) CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip CORS headers for WebSocket upgrades.
		if strings.Contains(r.Header.Get("Upgrade"), "websocket") {
			next.ServeHTTP(w, r)
			return
		}

		// Security Headers
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")

		if !strings.HasPrefix(r.URL.Path, "/api/preview/") || isPreviewNavPath(r.URL.Path) {
			csp := "default-src 'self'; frame-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self'; form-action 'self'; base-uri 'self'; object-src 'none'; frame-ancestors 'self';"
			if nonce, ok := r.Context().Value(nonceKey).(string); ok && nonce != "" {
				csp += fmt.Sprintf(" script-src 'self' 'nonce-%s';", nonce)
			} else {
				csp += " script-src 'self';"
			}
			w.Header().Set("Content-Security-Policy", csp)
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ctxKey is the type for context keys in this package (avoids empty-struct key collisions).
type ctxKey struct{}

// nonceKey is the context key for the request-specific CSP nonce.
var nonceKey = ctxKey{}

// NonceMiddleware generates a unique nonce for each request and injects it into the context.
func NonceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			next.ServeHTTP(w, r)
			return
		}
		nonce := base64.StdEncoding.EncodeToString(b)
		ctx := context.WithValue(r.Context(), nonceKey, nonce)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// sessionKey is the context key for the authenticated session id.
var sessionKey = ctxKey{}

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
		ctx := context.WithValue(r.Context(), sessionKey, sessionID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// PanicRecoveryMiddleware recovers from panics and returns a JSON error.
func PanicRecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("[PANIC] %s %s: %v\n%s", r.Method, r.URL.Path, err, debug.Stack())
				JSONError(w, http.StatusInternalServerError, "internal server error")
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

		c1 := []byte(csrfHeader)
		c2 := []byte(storedCSRF)

		if subtle.ConstantTimeCompare(c1, c2) != 1 {
			JSONError(w, http.StatusForbidden, "invalid CSRF token")
			return
		}

		// Inject CSRF token into context
		ctx := context.WithValue(r.Context(), csrfKey{}, storedCSRF)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
