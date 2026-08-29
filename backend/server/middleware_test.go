package server

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func TestMiddlewarePublicPathHealth(t *testing.T) {
	s := testServer(t)

	req := testRequest(http.MethodGet, "/api/health", nil)
	rr := testResponse(t, s, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", rr.Code, rr.Body.String())
	}
}

func TestMiddlewarePublicPathLogin(t *testing.T) {
	s := testServer(t)

	// Login should be accessible without a session cookie
	req := testRequest(http.MethodPost, "/api/auth/login", map[string]string{
		"password": testPassword,
	})
	rr := testResponse(t, s, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", rr.Code, rr.Body.String())
	}
}

func TestMiddlewarePublicPathSession(t *testing.T) {
	s := testServer(t)

	// Session check should be accessible without a session cookie
	req := testRequest(http.MethodGet, "/api/auth/session", nil)
	rr := testResponse(t, s, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", rr.Code, rr.Body.String())
	}
}

func TestMiddlewarePublicPathExtensions(t *testing.T) {
	s := testServer(t)

	// Extension static assets must be loadable WITHOUT a session cookie, because
	// sandboxed UI extensions run in opaque-origin iframes (cross-site) that
	// cannot send SameSite=Lax session cookies.
	for _, path := range []string{
		"/extensions/lib/host-client.js",
		"/extensions/ui/github-issue-importer/index.html",
	} {
		req := testRequest(http.MethodGet, path, nil)
		rr := testResponse(t, s, req)
		if rr.Code == http.StatusUnauthorized {
			t.Fatalf("%s: status = 401; extension assets must be public", path)
		}
	}
}

func TestMiddlewarePublicPathPreviewNavDistinction(t *testing.T) {
	s := testServer(t)

	// The watchdog control endpoint is session-authed…
	rr := testResponse(t, s, testRequest(http.MethodGet, "/api/preview/tok123/nav", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("/api/preview/{token}/nav without session: status = %d, want 401", rr.Code)
	}

	// …but preview docs/assets stay public — including a file literally
	// named "nav" in a subdir (unknown token → 404 from the handler, not 401
	// from the auth middleware).
	rr = testResponse(t, s, testRequest(http.MethodGet, "/api/preview/tok123/sub/nav", nil))
	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("/api/preview/{token}/sub/nav: status = 401; subdir files named nav must be public")
	}
}

func TestMiddlewareProtectedPathNoCookie(t *testing.T) {
	s := testServer(t)

	// /api/projects requires auth
	req := testRequest(http.MethodGet, "/api/projects", nil)
	rr := testResponse(t, s, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401; body: %s", rr.Code, rr.Body.String())
	}
}

func TestMiddlewareProtectedPathInvalidCookie(t *testing.T) {
	s := testServer(t)

	req := testRequest(http.MethodGet, "/api/projects", nil)
	req.Header.Set("Cookie", "PHPSESSID=invalid-token-value")
	rr := testResponse(t, s, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401; body: %s", rr.Code, rr.Body.String())
	}
}

func TestMiddlewareProtectedPathValidCookie(t *testing.T) {
	s := testServer(t)

	// Login first
	loginReq := testRequest(http.MethodPost, "/api/auth/login", map[string]string{
		"password": testPassword,
	})
	loginRR := testResponse(t, s, loginReq)
	if loginRR.Code != http.StatusOK {
		t.Fatalf("login: status = %d", loginRR.Code)
	}

	sessionCookie := loginRR.Header().Get("Set-Cookie")
	parts := strings.Split(sessionCookie, ";")[0]

	// Access protected path with valid session
	req := testRequest(http.MethodGet, "/api/projects", nil)
	req.Header.Set("Cookie", strings.TrimSpace(parts))
	rr := testResponse(t, s, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", rr.Code, rr.Body.String())
	}
}

func TestMiddlewareProtectedPathWrongMethod(t *testing.T) {
	s := testServer(t)

	// POST to /api/health is not a public path+method combo, so it requires auth
	req := testRequest(http.MethodPost, "/api/health", nil)
	rr := testResponse(t, s, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401; body: %s", rr.Code, rr.Body.String())
	}
}

func TestMiddlewareSessionIDInContext(t *testing.T) {
	s := testServer(t)

	// Login first
	loginReq := testRequest(http.MethodPost, "/api/auth/login", map[string]string{
		"password": testPassword,
	})
	loginRR := testResponse(t, s, loginReq)
	if loginRR.Code != http.StatusOK {
		t.Fatalf("login: status = %d", loginRR.Code)
	}

	sessionCookie := loginRR.Header().Get("Set-Cookie")
	parts := strings.Split(sessionCookie, ";")[0]

	// Access any protected path
	req := testRequest(http.MethodGet, "/api/projects", nil)
	req.Header.Set("Cookie", strings.TrimSpace(parts))

	// Use the middleware directly to check context
	rr := httptest.NewRecorder()
	s.SessionMiddleware(s.mux).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", rr.Code, rr.Body.String())
	}
}

func TestMiddlewareCORSReflectOrigin(t *testing.T) {
	s := testServer(t)

	req := testRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rr := testResponse(t, s, req)

	origin := rr.Header().Get("Access-Control-Allow-Origin")
	if origin != "" {
		t.Errorf("expected no Allow-Origin header (same-origin policy), got %q", origin)
	}

	creds := rr.Header().Get("Access-Control-Allow-Credentials")
	if creds != "" {
		t.Errorf("expected no Allow-Credentials header, got %q", creds)
	}
}

func TestMiddlewareCORSSameOrigin(t *testing.T) {
	s := testServer(t)

	// Same-origin request has no Origin header
	req := testRequest(http.MethodGet, "/api/health", nil)
	rr := testResponse(t, s, req)

	// Should not set Allow-Origin for same-origin requests
	origin := rr.Header().Get("Access-Control-Allow-Origin")
	if origin != "" {
		t.Errorf("expected no Allow-Origin header for same-origin, got %q", origin)
	}
}

func TestCSRFMiddlewareGETExempt(t *testing.T) {
	s := testServer(t)

	// Login first
	loginReq := testRequest(http.MethodPost, "/api/auth/login", map[string]string{
		"password": testPassword,
	})
	loginRR := testResponse(t, s, loginReq)
	if loginRR.Code != http.StatusOK {
		t.Fatalf("login: status = %d", loginRR.Code)
	}
	sessionCookie := loginRR.Header().Get("Set-Cookie")
	parts := strings.Split(sessionCookie, ";")[0]

	// GET request should work without CSRF token
	req := testRequest(http.MethodGet, "/api/projects", nil)
	req.Header.Set("Cookie", strings.TrimSpace(parts))
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET without CSRF: status = %d; want 200", rr.Code)
	}
}

func TestCSRFMiddlewareMissingToken(t *testing.T) {
	s := testServer(t)

	// Login first
	loginReq := testRequest(http.MethodPost, "/api/auth/login", map[string]string{
		"password": testPassword,
	})
	loginRR := testResponse(t, s, loginReq)
	if loginRR.Code != http.StatusOK {
		t.Fatalf("login: status = %d", loginRR.Code)
	}
	sessionCookie := loginRR.Header().Get("Set-Cookie")
	parts := strings.Split(sessionCookie, ";")[0]

	// POST without CSRF token should be rejected
	req := testRequest(http.MethodPost, "/api/projects", map[string]string{"path": "/tmp/test"})
	req.Header.Set("Cookie", strings.TrimSpace(parts))
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("POST without CSRF: status = %d; want 403", rr.Code)
	}
}

func TestCSRFMiddlewareValidToken(t *testing.T) {
	s := testServer(t)

	// Login first
	loginReq := testRequest(http.MethodPost, "/api/auth/login", map[string]string{
		"password": testPassword,
	})
	loginRR := testResponse(t, s, loginReq)
	if loginRR.Code != http.StatusOK {
		t.Fatalf("login: status = %d", loginRR.Code)
	}
	sessionCookie := loginRR.Header().Get("Set-Cookie")
	parts := strings.Split(sessionCookie, ";")[0]
	csrfToken := loginRR.Header().Get("X-CSRF-Token")

	// Create a temp dir and init git
	tmpDir := t.TempDir()
	exec.Command("git", "init", tmpDir).Run()

	// POST with valid CSRF token should succeed
	req := testRequest(http.MethodPost, "/api/projects", map[string]string{"path": tmpDir})
	req.Header.Set("Cookie", strings.TrimSpace(parts))
	req.Header.Set("X-CSRF-Token", csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("POST with valid CSRF: status = %d; want 201; body: %s", rr.Code, rr.Body.String())
	}
}

func TestCSRFMiddlewareInvalidToken(t *testing.T) {
	s := testServer(t)

	// Login first
	loginReq := testRequest(http.MethodPost, "/api/auth/login", map[string]string{
		"password": testPassword,
	})
	loginRR := testResponse(t, s, loginReq)
	if loginRR.Code != http.StatusOK {
		t.Fatalf("login: status = %d", loginRR.Code)
	}
	sessionCookie := loginRR.Header().Get("Set-Cookie")
	parts := strings.Split(sessionCookie, ";")[0]

	// POST with invalid CSRF token should be rejected
	req := testRequest(http.MethodPost, "/api/projects", map[string]string{"path": "/tmp/test"})
	req.Header.Set("Cookie", strings.TrimSpace(parts))
	req.Header.Set("X-CSRF-Token", "invalid-token")
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("POST with invalid CSRF: status = %d; want 403", rr.Code)
	}
}

func TestCSRFMiddlewarePublicEndpointExempt(t *testing.T) {
	s := testServer(t)

	// Login (public endpoint, no CSRF needed)
	req := testRequest(http.MethodPost, "/api/auth/login", map[string]string{
		"password": testPassword,
	})
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("login without CSRF: status = %d; want 200", rr.Code)
	}
}

func TestPanicRecovery_DoesNotLeakPanicValue(t *testing.T) {
	panicValue := "SENSITIVE-INTERNAL-DETAIL"
	h := PanicRecoveryMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(panicValue)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if strings.Contains(rr.Body.String(), panicValue) {
		t.Fatalf("panic value leaked to client: %q", rr.Body.String())
	}
}

// --- Host pinning (DNS-rebinding protection) --------------------------------

// okHostHandler is the sentinel downstream handler: reaching it means the
// request passed the Host check.
func okHostHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func TestHostPinMiddleware(t *testing.T) {
	s := testServer(t) // addr ":0" — no bind host contributes to the allowlist

	tests := []struct {
		name string
		host string
		want int
	}{
		{name: "localhost with port", host: "localhost:3000", want: http.StatusOK},
		{name: "localhost without port", host: "localhost", want: http.StatusOK},
		{name: "IPv4 loopback", host: "127.0.0.1:3000", want: http.StatusOK},
		{name: "IPv6 loopback bracketed", host: "[::1]:3000", want: http.StatusOK},
		{name: "IPv6 loopback bare", host: "::1", want: http.StatusOK},
		{name: "uppercase LOCALHOST", host: "LOCALHOST:3000", want: http.StatusOK},
		// A rebinding attack must present its own origin as Host — which is
		// exactly the value that is not allowed.
		{name: "attacker domain", host: "evil.test:3000", want: http.StatusMisdirectedRequest},
		{name: "attacker domain pointing at loopback", host: "rebind.attacker.io", want: http.StatusMisdirectedRequest},
		{name: "foreign LAN ip", host: "10.1.2.3:3000", want: http.StatusMisdirectedRequest},
		{name: "empty host", host: "", want: http.StatusMisdirectedRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
			req.Host = tt.host
			rr := httptest.NewRecorder()
			s.HostPinMiddleware(okHostHandler()).ServeHTTP(rr, req)

			if rr.Code != tt.want {
				t.Fatalf("Host %q: status = %d, want %d (body %q)", tt.host, rr.Code, tt.want, rr.Body.String())
			}
			if tt.want == http.StatusMisdirectedRequest && !strings.Contains(rr.Body.String(), "error") {
				t.Fatalf("Host %q: 421 body is not a JSON error: %q", tt.host, rr.Body.String())
			}
		})
	}
}

func TestHostPinMiddlewareAllowedHostsEnv(t *testing.T) {
	s := testServer(t)

	// The reverse-proxy opt-out: a proxy forwards the public domain in Host.
	t.Setenv("MIKE_ALLOWED_HOSTS", "kanban.example.com, staging.lan")

	tests := []struct {
		host string
		want int
	}{
		{host: "kanban.example.com", want: http.StatusOK},
		{host: "kanban.example.com:443", want: http.StatusOK},
		{host: "KANBAN.EXAMPLE.COM", want: http.StatusOK},
		{host: "staging.lan:8443", want: http.StatusOK},
		{host: "localhost:3000", want: http.StatusOK},
		{host: "evil.test:3000", want: http.StatusMisdirectedRequest},
		// Prefix/suffix tricks must not match an allowlist entry.
		{host: "kanban.example.com.evil.test", want: http.StatusMisdirectedRequest},
		{host: "xkanban.example.com", want: http.StatusMisdirectedRequest},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
			req.Host = tt.host
			rr := httptest.NewRecorder()
			s.HostPinMiddleware(okHostHandler()).ServeHTTP(rr, req)
			if rr.Code != tt.want {
				t.Fatalf("Host %q: status = %d, want %d", tt.host, rr.Code, tt.want)
			}
		})
	}
}

func TestHostPinMiddlewareBindHostAllowed(t *testing.T) {
	// Binding to an explicit address (the container/reverse-proxy escape hatch)
	// pins that address as an accepted Host value too.
	s := &Server{addr: "192.168.1.50:3000"}

	for _, host := range []string{"192.168.1.50:3000", "192.168.1.50", "localhost:3000"} {
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		req.Host = host
		rr := httptest.NewRecorder()
		s.HostPinMiddleware(okHostHandler()).ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("Host %q: status = %d, want 200", host, rr.Code)
		}
	}

	for _, host := range []string{"192.168.1.51:3000", "evil.test"} {
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		req.Host = host
		rr := httptest.NewRecorder()
		s.HostPinMiddleware(okHostHandler()).ServeHTTP(rr, req)
		if rr.Code != http.StatusMisdirectedRequest {
			t.Errorf("Host %q: status = %d, want 421", host, rr.Code)
		}
	}
}

// TestHostPinMiddlewareCoversUpgradesAndPublicPaths pins down the "mounted
// early" requirement: the check runs before routing, so it also guards the
// WebSocket upgrades and paths that are exempt from authentication.
func TestHostPinMiddlewareCoversUpgradesAndPublicPaths(t *testing.T) {
	s := testServer(t)

	// Public path (no session needed) is still Host-checked.
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Host = "evil.test:3000"
	rr := httptest.NewRecorder()
	s.HostPinMiddleware(s.mux).ServeHTTP(rr, req)
	if rr.Code != http.StatusMisdirectedRequest {
		t.Fatalf("public path with foreign Host: status = %d, want 421", rr.Code)
	}

	// WebSocket upgrade (stream / lsp / terminal) is likewise Host-checked —
	// it must not reach the upgrade handler with a 401/400 from downstream.
	req = httptest.NewRequest(http.MethodGet, "/api/stream", nil)
	req.Host = "evil.test:3000"
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	rr = httptest.NewRecorder()
	s.HostPinMiddleware(s.mux).ServeHTTP(rr, req)
	if rr.Code != http.StatusMisdirectedRequest {
		t.Fatalf("websocket upgrade with foreign Host: status = %d, want 421", rr.Code)
	}
}
