package server

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"mike/backend/startup"
)

// login returns the session cookie and CSRF token of a freshly paired device on s. Several
// scenarios below are only interesting for an authenticated caller, so the ceremony that
// opens a session is spelled out once here instead of at the top of every test in this file.
func login(t *testing.T, s *Server) (cookie, csrf string) {
	t.Helper()

	_, finished := registerPasskeyOver(t, s, "test device", plainCeremony)
	raw := finished.Header().Get("Set-Cookie")
	if raw == "" {
		t.Fatal("a finished ceremony set no session cookie")
	}
	return strings.TrimSpace(strings.Split(raw, ";")[0]), finished.Header().Get("X-CSRF-Token")
}

// TestMiddlewareAuthenticationGating is the auth gate as one matrix: which paths and
// methods are public, and what a caller with no cookie, a bogus cookie, or a real
// session is told. Anything outside publicPaths must answer 401. The extension-asset and
// preview-path tests below stay separate because they are public for a reason and are
// judged on "not 401" rather than on an exact status.
func TestMiddlewareAuthenticationGating(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   any
		// cookie: "" sends none, "invalid" a bogus token, "session" a real login.
		cookie string
		want   int
	}{
		{name: "health is public", method: http.MethodGet, path: "/api/health", want: http.StatusOK},
		// The ceremony answered 404 because nothing is registered here, which is the point: the
		// middleware let a POST through with no session because there is no session to have yet.
		{name: "a ceremony route is public", method: http.MethodPost, path: "/api/auth/passkey/assert/begin",
			want: http.StatusNotFound},
		{name: "session check is public", method: http.MethodGet, path: "/api/auth/session", want: http.StatusOK},
		// The device being paired has no session, and the certificate is public data. docs/tls.md.
		{name: "the CA download is public", method: http.MethodGet, path: "/api/tls/ca", want: http.StatusOK},
		{
			// publicPaths matches path AND method, so a POST to a public GET path is gated.
			name:   "public path with the wrong method requires auth",
			method: http.MethodPost, path: "/api/health", want: http.StatusUnauthorized,
		},
		{name: "protected path without a cookie", method: http.MethodGet, path: "/api/projects",
			want: http.StatusUnauthorized},
		{name: "protected path with a bogus cookie", method: http.MethodGet, path: "/api/projects",
			cookie: "invalid", want: http.StatusUnauthorized},
		{name: "protected path with a valid session", method: http.MethodGet, path: "/api/projects",
			cookie: "session", want: http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)

			req := testRequest(tc.method, tc.path, tc.body)
			switch tc.cookie {
			case "invalid":
				req.Header.Set("Cookie", "PHPSESSID=invalid-token-value")
			case "session":
				cookie, _ := login(t, s)
				req.Header.Set("Cookie", cookie)
			}

			if rr := testResponse(t, s, req); rr.Code != tc.want {
				t.Fatalf("status = %d; want %d; body: %s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

// TestCleartextIsRedirectedToHTTPS: this server has no plain-HTTP answer. The hybrid listener still
// accepts a cleartext connection so that the `http://host:3000` a person typed gets this redirect instead
// of a TLS error, and a proxy that says it terminated TLS is answered rather than pointed at itself.
func TestCleartextIsRedirectedToHTTPS(t *testing.T) {
	s := testServer(t)

	req := plainRequest(http.MethodGet, "/api/config", nil)
	req.Host = "localhost"
	rr := httptest.NewRecorder()
	s.middlewareChain(s.mux).ServeHTTP(rr, req)
	if rr.Code != http.StatusMovedPermanently {
		t.Fatalf("cleartext GET = %d %q; want 301", rr.Code, rr.Body.String())
	}
	if got, want := rr.Header().Get("Location"), "https://localhost/api/config"; got != want {
		t.Errorf("Location = %q; want %q", got, want)
	}

	trusted := testServerWith(t, func(c *startup.Config) { c.TrustProxy = true })
	forwarded := plainRequest(http.MethodGet, "/api/config", nil)
	forwarded.Host = "localhost"
	forwarded.Header.Set("X-Forwarded-Proto", "https")
	rr = httptest.NewRecorder()
	trusted.middlewareChain(trusted.mux).ServeHTTP(rr, forwarded)
	if code := rr.Header().Get("Location"); code != "" {
		t.Errorf("a proxy that said https was redirected again to %q", code)
	}
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("forwarded https with no session = %d; want the route's own 401", rr.Code)
	}
}

// TestWorkerScriptNeedsNoSession: the generated worker imports the click handler for every notification
// it shows, and a worker fetches its own scripts with no session to present, so that script is answered
// like the worker itself.
func TestWorkerScriptNeedsNoSession(t *testing.T) {
	s := testServer(t)

	req := secureRequest(httptest.NewRequest(http.MethodGet, "/sw-notifications.js", nil))
	req.Host = "localhost"
	rr := httptest.NewRecorder()
	// The chain Serve installs over the router, which is what serves the frontend — the bare mux answers
	// nothing outside the API.
	s.middlewareChain(s.router).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /sw-notifications.js with no session: got %d %q, want 200", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); !strings.Contains(body, "notificationclick") {
		t.Errorf("body = %q, want the click handler the worker imports", body)
	}
}

// TestPrecachedDocumentNeedsNoSession: the worker precaches `/index.html` by name and abandons its
// install on a non-200, so the document answers with a nonce before anyone has signed in.
func TestPrecachedDocumentNeedsNoSession(t *testing.T) {
	s := testServer(t)

	get := func(path string) string {
		req := secureRequest(httptest.NewRequest(http.MethodGet, path, nil))
		req.Host = "localhost"
		rr := httptest.NewRecorder()
		s.middlewareChain(s.router).ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s with no session: got %d %q, want 200", path, rr.Code, rr.Body.String())
		}
		return rr.Body.String()
	}

	// The nonce is per response, so it is what the two documents are allowed to differ in.
	stripNonce := func(doc string) string {
		match := noncePattern.FindStringSubmatch(doc)
		if match == nil {
			return doc
		}
		return strings.Replace(doc, match[0], `nonce=""`, 1)
	}

	indexed := get("/index.html")
	if strings.Contains(indexed, "{{.Nonce}}") {
		t.Errorf("GET /index.html served the unreplaced placeholder: %q", indexed)
	}
	if noncePattern.FindStringSubmatch(indexed) == nil {
		t.Errorf("GET /index.html carries no nonce, so its one inline script is refused: %q", indexed)
	}
	if got, want := stripNonce(indexed), stripNonce(get("/")); got != want {
		t.Errorf("GET /index.html is a different document than GET /:\n got %q\nwant %q", got, want)
	}
}

// TestMiddlewareSessionIDInContext checks what the status-code rows above cannot: that
// the middleware publishes the validated session id on the context, which is how every
// handler downstream identifies the caller.
func TestMiddlewareSessionIDInContext(t *testing.T) {
	s := testServer(t)
	cookie, _ := login(t, s)

	var gotSession string
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSession, _ = r.Context().Value(sessionKey).(string)
		w.WriteHeader(http.StatusOK)
	})

	req := testRequest(http.MethodGet, "/api/projects", nil)
	req.Header.Set("Cookie", cookie)

	rr := httptest.NewRecorder()
	s.SessionMiddleware(probe).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rr.Code)
	}
	if gotSession == "" {
		t.Error("session id was not injected into the request context")
	}
}

// TestMiddlewareCORSIsSameOriginOnly covers both halves of one rule: no Allow-Origin
// with and without an Origin header. The app is served from its own origin, so
// reflecting the caller would only widen what the SPA can be read by.
func TestMiddlewareCORSIsSameOriginOnly(t *testing.T) {
	for _, origin := range []string{"", "http://localhost:3000"} {
		name := "same-origin request (no Origin header)"
		if origin != "" {
			name = "cross-origin request from " + origin
		}
		t.Run(name, func(t *testing.T) {
			s := testServer(t)

			req := testRequest(http.MethodGet, "/api/health", nil)
			if origin != "" {
				req.Header.Set("Origin", origin)
			}
			rr := testResponse(t, s, req)

			if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
				t.Errorf("Access-Control-Allow-Origin = %q, want none", got)
			}
			if got := rr.Header().Get("Access-Control-Allow-Credentials"); got != "" {
				t.Errorf("Access-Control-Allow-Credentials = %q, want none", got)
			}
		})
	}
}

// TestCSRFMiddleware is the CSRF gate as one matrix: a session-authenticated caller must
// send the header on mutating requests and never on GETs, and public endpoints are exempt
// because there is no session to ride. The 201 row is the positive control — proof the
// accepted token came from a path that reaches the handler.
func TestCSRFMiddleware(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		// body is built per subtest so fixtures (a temp git repo) stay isolated.
		body  func(t *testing.T) any
		login bool   // CSRF only gates session-authenticated requests
		token string // "" sends no X-CSRF-Token header; "valid" uses the session's own
		want  int
	}{
		{
			name: "GET is exempt", method: http.MethodGet, path: "/api/projects",
			login: true, want: http.StatusOK,
		},
		{
			name: "mutating request without a token", method: http.MethodPost, path: "/api/projects",
			body: staticBody(map[string]string{"path": "/tmp/test"}), login: true,
			want: http.StatusForbidden,
		},
		{
			name: "mutating request with a bogus token", method: http.MethodPost, path: "/api/projects",
			body: staticBody(map[string]string{"path": "/tmp/test"}), login: true,
			token: "invalid-token", want: http.StatusForbidden,
		},
		{
			name: "mutating request with a valid token reaches the handler", method: http.MethodPost,
			path: "/api/projects",
			body: func(t *testing.T) any {
				dir := t.TempDir()
				if err := exec.Command("git", "init", dir).Run(); err != nil {
					t.Fatal(err)
				}
				return map[string]string{"path": dir}
			},
			login: true, token: "valid", want: http.StatusCreated,
		},
		{
			name: "public endpoint needs no token", method: http.MethodPost, path: "/api/auth/passkey/assert/begin",
			want: http.StatusNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)

			var body any
			if tc.body != nil {
				body = tc.body(t)
			}
			req := testRequest(tc.method, tc.path, body)

			if tc.login {
				cookie, csrf := login(t, s)
				req.Header.Set("Cookie", cookie)
				if tc.token == "valid" {
					req.Header.Set("X-CSRF-Token", csrf)
				}
			}
			if tc.token != "" && tc.token != "valid" {
				req.Header.Set("X-CSRF-Token", tc.token)
			}

			if rr := testResponse(t, s, req); rr.Code != tc.want {
				t.Fatalf("status = %d; want %d; body: %s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

// staticBody adapts a fixed request body to the per-subtest body builder.
func staticBody(v any) func(t *testing.T) any {
	return func(*testing.T) any { return v }
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
	s := testServer(t) // addr ":0" — no listen host contributes to the allowlist

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
	// The reverse-proxy opt-out: a proxy forwards the public domain in Host. The variable alone, with
	// no flag, must reach the allowlist — and it reaches it through the one ladder that resolves it,
	// so what is pinned here is the whole chain (variable -> startup.Config -> middleware) rather than
	// a middleware that reads the environment on each request.
	t.Setenv(startup.EnvAllowedHosts, "kanban.example.com, staging.lan")
	t.Setenv(startup.EnvEncryptionKey, "a-long-enough-test-key")
	resolved, err := startup.Resolve(startup.Flags{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(resolved.AllowedHosts) != 2 {
		t.Fatalf("resolved extras = %q, want the two entries split", resolved.AllowedHosts)
	}
	s := testServerWith(t, func(c *startup.Config) { c.AllowedHosts = resolved.AllowedHosts })

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

// TestHostPinMiddlewareUsesTheWiredAllowedHosts pins the --allowed-hosts / MIKE_ALLOWED_HOSTS
// plumbing end to end: the resolved list reaches the allowlist, and the environment is not consulted
// again at request time. The variable stays set below precisely to prove that — a middleware that
// re-read it would let a stale environment widen the allowlist of a deployment that switched to the
// flag, which is the reason the ladder resolves once (startup.Resolve) and the flag replaces rather
// than extends.
func TestHostPinMiddlewareUsesTheWiredAllowedHosts(t *testing.T) {
	t.Setenv("MIKE_ALLOWED_HOSTS", "env.example.com")

	s := &Server{addr: ":0", settings: startup.Config{AllowedHosts: []string{"flag.example.com", "other.lan"}}}

	tests := []struct {
		host string
		want int
	}{
		{host: "flag.example.com", want: http.StatusOK},
		{host: "flag.example.com:443", want: http.StatusOK},
		{host: "OTHER.LAN", want: http.StatusOK},
		{host: "localhost:3000", want: http.StatusOK},
		// Set in the environment and wired out of the list: the request-time read is gone.
		{host: "env.example.com", want: http.StatusMisdirectedRequest},
		{host: "evil.test", want: http.StatusMisdirectedRequest},
	}

	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		req.Host = tt.host
		rr := httptest.NewRecorder()
		s.HostPinMiddleware(okHostHandler()).ServeHTTP(rr, req)
		if rr.Code != tt.want {
			t.Errorf("Host %q: status = %d, want %d", tt.host, rr.Code, tt.want)
		}
	}
}

// TestEffectiveAllowedHosts pins that the middleware answers from the resolved list and from nothing
// else: the joining of entries is the middleware's, the choosing between flag and variable is
// startup's (startup.TestResolveAllowedHosts).
func TestEffectiveAllowedHosts(t *testing.T) {
	t.Setenv(startup.EnvAllowedHosts, "env.example.com")

	tests := []struct {
		name  string
		hosts []string
		want  string
	}{
		{name: "nothing wired means no extras", hosts: nil, want: ""},
		{name: "entries are joined for the allowlist", hosts: []string{"a.example", "b.example"}, want: "a.example,b.example"},
		{name: "the wired list is the whole answer", hosts: []string{"flag.example.com"}, want: "flag.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{settings: startup.Config{AllowedHosts: tt.hosts}}
			if got := s.effectiveAllowedHosts(); got != tt.want {
				t.Errorf("effectiveAllowedHosts() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHostPinMiddlewareListenHostAllowed(t *testing.T) {
	// Listening on an explicit address (the container/reverse-proxy escape hatch)
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
