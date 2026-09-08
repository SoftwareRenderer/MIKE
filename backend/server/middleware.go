package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strings"

	"kanban/backend/httputil"
	"kanban/pkg/logging"
)

// Re-export JSON helpers from httputil.
var (
	JSONResponse = httputil.JSONResponse
	JSONError    = httputil.JSONError
	// Fail answers a domain error with the status the error declares, so a store
	// fault is never reported to the browser as "not found".
	Fail = httputil.Fail
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

// requestIsHTTPS reports whether the browser's side of this request was encrypted: TLS terminated
// here, or a TLS-terminating proxy that said so in X-Forwarded-Proto under MIKE_TRUST_PROXY. It is
// the one reading of that pair, shared by the two places that act on it — the HTTPS redirect and
// the session cookie's Secure flag — because a deployment where they disagree is the whole point of
// the flag: the redirect would believe the connection is HTTPS while the cookie goes out in
// cleartext anyway.
//
// X-Forwarded-Proto counts only under MIKE_TRUST_PROXY, exactly as it does for the redirect: any
// client can set the header, and a cookie marked Secure on the strength of a lie is worse than one
// marked honestly. The one answer that stays unencrypted is a server with TLS off (`--no-tls`) and
// no proxy in front of it — cleartext to the browser, where a Secure cookie would never be stored
// or sent at all, so the app could not log in.
func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return trustProxy() && r.Header.Get("X-Forwarded-Proto") == "https"
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
		w.Header().Set("Permissions-Policy", permissionsPolicy)
		// Only the two trees an opaque-origin frame loads as a subresource are left unmarked —
		// see isOpaqueOriginAsset. Everything else answers same-origin, so no other site can
		// embed this install's pages, its API responses or its bundle as a subresource of its own.
		if !isOpaqueOriginAsset(r.URL.Path) {
			w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		}

		if !strings.HasPrefix(r.URL.Path, "/api/preview/") || isPreviewNavPath(r.URL.Path) {
			// 'self' is required — the app's own hashed bundle is a same-origin script — and it is
			// also the ceiling: extension code is served from this same origin under /extensions/,
			// so the nonce cannot be the control here, only a statement about index.html's one
			// inline step. Narrowing that means a second origin for /extensions/ (see docs/tls.md),
			// not a longer nonce. Extension frames never script this document: they are sandboxed
			// without allow-same-origin and talk to the host only through the armed channel.
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

// permissionsPolicy denies the browser features this app has no caller for, so that a future
// markup bug cannot reach them either. Deliberately absent: clipboard (the terminal's paste reads
// it), fullscreen (a terminal may want it), and the display of the document itself. A sandboxed
// opaque-origin frame is denied these by its origin anyway — this is the top-level document, which
// renders model-authored markup and would otherwise be the granting principal.
const permissionsPolicy = "camera=(), microphone=(), geolocation=(), usb=(), serial=(), hid=(), " +
	"bluetooth=(), payment=(), display-capture=(), wake-lock=(), idle-detection=(), " +
	"keyboard-map=(), window-management=()"

// isOpaqueOriginAsset reports whether a path is fetched as a subresource by a document that has no
// origin to name: a sandboxed extension iframe loading its own files from /extensions/, and a
// preview document loading its siblings from /api/preview/. Cross-Origin-Resource-Policy:
// same-origin on those would refuse them both — the frame's opaque origin is not this origin — so
// they are the two paths the header is not applied to.
func isOpaqueOriginAsset(path string) bool {
	if strings.HasPrefix(path, "/extensions/") {
		return true
	}
	return strings.HasPrefix(path, "/api/preview/") && !isPreviewNavPath(path)
}

// allowedHostsEnv lists extra hostnames accepted in the Host header,
// comma-separated (e.g. "kanban.example.com,staging.lan"). It is the opt-out for
// reverse proxies, which forward a public domain the server cannot infer from
// the address it listens on. The --allowed-hosts flag takes priority over it.
const allowedHostsEnv = "MIKE_ALLOWED_HOSTS"

// effectiveAllowedHosts resolves the extra accepted Host values: the value the
// server was constructed with (the --allowed-hosts flag) wins, and
// MIKE_ALLOWED_HOSTS is the fallback for an unset/blank flag.
func (s *Server) effectiveAllowedHosts() string {
	if strings.TrimSpace(s.allowedHosts) != "" {
		return s.allowedHosts
	}
	return os.Getenv(allowedHostsEnv)
}

// loopbackHosts are always accepted: they can only name this machine.
var loopbackHosts = []string{"localhost", "127.0.0.1", "::1"}

// hostAllowlist is the set of Host-header hostnames this server answers.
type hostAllowlist struct {
	hosts map[string]struct{}
}

// newHostAllowlist builds the allowlist from the listen address and the
// extra-hosts value (flag over MIKE_ALLOWED_HOSTS): loopback names, the address
// the server listens on (unless it is a wildcard, which names no host), plus
// every explicit extra entry.
func newHostAllowlist(addr, extraHosts string) *hostAllowlist {
	a := &hostAllowlist{hosts: make(map[string]struct{})}
	for _, h := range loopbackHosts {
		a.hosts[h] = struct{}{}
	}
	if listenHost := normalizeHost(addr); listenHost != "" && listenHost != "0.0.0.0" && listenHost != "::" {
		a.hosts[listenHost] = struct{}{}
	}
	for _, entry := range strings.Split(extraHosts, ",") {
		if h := normalizeHost(entry); h != "" {
			a.hosts[h] = struct{}{}
		}
	}
	return a
}

// allows reports whether a Host header value names this server. Missing/empty
// Host values are rejected: without an origin there is nothing to check.
func (a *hostAllowlist) allows(rawHost string) bool {
	h := normalizeHost(rawHost)
	if h == "" {
		return false
	}
	_, ok := a.hosts[h]
	return ok
}

// normalizeHost reduces a Host header value (or allowlist entry) to a bare,
// lowercase hostname: strip the port (falling back to the raw value when there
// is none), unwrap IPv6 brackets, drop the FQDN root dot.
func normalizeHost(raw string) string {
	h := strings.TrimSpace(raw)
	if h == "" {
		return ""
	}
	if stripped, _, err := net.SplitHostPort(h); err == nil {
		h = stripped
	}
	h = strings.TrimSuffix(strings.Trim(h, "[]"), ".")
	return strings.ToLower(h)
}

// HostPinMiddleware rejects requests whose Host header names a different authority than this
// server (HTTP 421).
//
// This is DNS-rebinding protection, not authentication: a page at evil.test can point its A
// record at 127.0.0.1, and once that navigation is same-origin, SameSite=Lax, CORS and the
// publicly readable CSRF token all stop protecting. It never replaces the session check.
func (s *Server) HostPinMiddleware(next http.Handler) http.Handler {
	allowed := newHostAllowlist(s.addr, s.effectiveAllowedHosts())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if allowed.allows(r.Host) {
			next.ServeHTTP(w, r)
			return
		}
		logging.For(logging.Host).Infof("rejected request for Host %q (allow it with --allowed-hosts or %s)", r.Host, allowedHostsEnv)
		JSONError(w, http.StatusMisdirectedRequest, "invalid Host header")
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

// MaxBodyMiddleware bounds the body of every request that may carry one. Handlers
// that decode r.Body directly (git, fs, task, thread) are not covered by
// httputil.DecodeJSON's own limit, so the bound goes on the reader here.
//
// A declared Content-Length over the cap is refused before a byte is read; a chunked
// body has no length to trust, so the wrapped reader refuses it once past the cap.
func MaxBodyMiddleware(limit int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// WebSocket upgrades are GETs; bounding them would have nothing to bound.
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			// ContentLength is -1 for a chunked body, which is the reader's case.
			if r.ContentLength > limit {
				JSONError(w, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}

// PanicRecoveryMiddleware recovers from panics and returns a JSON error.
func PanicRecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				logging.For(logging.Recovery).Infof("%s %s: %v\n%s", r.Method, r.URL.Path, err, debug.Stack())
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
