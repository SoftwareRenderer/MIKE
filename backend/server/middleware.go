package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"strings"

	"mike/backend/httputil"
	"mike/pkg/logging"

	"mike/backend/startup"
)

// Re-export JSON helpers from httputil.
var (
	JSONResponse = httputil.JSONResponse
	JSONError    = httputil.JSONError
	// Fail answers a domain error with the status the error declares, so a store
	// fault is never reported to the browser as "not found".
	Fail = httputil.Fail
	// Fault is the same mapper for the one outcome with nothing richer to declare: the operation
	// failed and the cause is MIKE's to read, not the browser's.
	Fault = httputil.Fault
)

// Paths that don't require authentication.
var publicPaths = map[string]map[string]struct{}{
	"/api/health":       {http.MethodGet: {}},
	"/api/auth/login":   {http.MethodPost: {}},
	"/api/auth/session": {http.MethodGet: {}},
}

// hostClientPath is the extension bridge (frontend/public/host-client.js), which every UI
// extension's entry document loads. Named once because two rules read it: the auth exemption below
// and the CORP exemption in isOpaqueOriginAsset.
const hostClientPath = "/host-client.js"

// isPublicPath returns true if the given path+method combination is publicly accessible.
// Static assets (frontend) are always public.
func isPublicPath(path, method string) bool {
	// Frontend static assets are always public
	if path == "/" || path == hostClientPath || strings.HasPrefix(path, "/assets/") || strings.HasPrefix(path, "/favicon") {
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
	// PWA assets (manifest, service worker, icons) are always public. A worker fetches its own scripts
	// with no session to present, so its companion scripts are public beside it.
	if path == "/manifest.webmanifest" ||
		path == "/sw.js" ||
		path == "/sw-notifications.js" ||
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

// requestIsHTTPS reports whether the browser reached this server over TLS: terminated here, or a proxy
// said X-Forwarded-Proto: https and MIKE_TRUST_PROXY is set. The HTTPS redirect and the session
// cookie's Secure flag read this one answer, so they cannot disagree about the same request.
func requestIsHTTPS(r *http.Request, trustProxy bool) bool {
	if r.TLS != nil {
		return true
	}
	return trustProxy && r.Header.Get("X-Forwarded-Proto") == "https"
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
		// Only the paths an opaque-origin frame loads as a subresource are left unmarked —
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
			// An extension document is public on this origin, so its response carries the sandbox the
			// mounted frame asks for: opened at the top level it would otherwise run as the signed-in
			// user. Same directive as previewDocCSPStrict; docs/ui-extensions.md.
			if strings.HasPrefix(r.URL.Path, "/extensions/") {
				csp += " sandbox allow-scripts;"
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

// isOpaqueOriginAsset reports the paths fetched as subresources by a document with no origin to name:
// a sandboxed extension iframe reading its own files and /host-client.js, and a preview document
// reading its own assets. CORP: same-origin on those breaks them, because the browser has no origin
// to match the header against.
func isOpaqueOriginAsset(path string) bool {
	if strings.HasPrefix(path, "/extensions/") || path == hostClientPath {
		return true
	}
	return strings.HasPrefix(path, "/api/preview/") && !isPreviewNavPath(path)
}

// effectiveAllowedHosts returns the extra accepted Host values (e.g.
// "kanban.example.com,staging.lan") — the opt-out for reverse proxies, which forward a public
// domain the server cannot infer from the address it listens on. It is the resolved value of the
// --allowed-hosts / MIKE_ALLOWED_HOSTS ladder, so the flag-over-variable choice is made once at boot
// and every request is measured against the same list.
func (s *Server) effectiveAllowedHosts() string {
	return strings.Join(s.settings.AllowedHosts, ",")
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
		logging.For(logging.Host).Infof("rejected request for Host %q (allow it with --allowed-hosts or %s)", r.Host, startup.EnvAllowedHosts)
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

// sessionKey is the context key for the authenticated session id. It needs a type of its own:
// values of an empty struct type compare equal, so every key declared as `ctxKey{}` reads and writes
// the same context slot — with nonceKey sharing it, a request that reached a handler with no session
// in its context would answer currentSessionID with the CSP nonce.
type sessionIDKeyType struct{}

var sessionKey = sessionIDKeyType{}

// SessionMiddleware validates the PHPSESSID cookie and injects auth state into context.
// Public paths (health check, login, session check) bypass validation.
func (s *Server) SessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow public paths without auth
		if isPublicPath(r.URL.Path, r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		// A request a bearer token authenticated is already answered: it carries no session cookie,
		// and its route scope was checked where the token was.
		if apiTokenID(r) != "" {
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

		// Skip a request a bearer token authenticated. CSRF protection exists because a browser sends
		// a session cookie on its own; a token reaches this server only if the caller put it in the
		// header, which no other page can do.
		if apiTokenID(r) != "" {
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
