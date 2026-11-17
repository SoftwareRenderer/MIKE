package server

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"mike/backend/startup"
	"mike/pkg/logging"
)

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
		// The device list names every signed-in machine, so it is never public beside the routes that
		// sign devices out. docs/sessions.md.
		{name: "the session list is not public", method: http.MethodGet, path: "/api/sessions",
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
				cookie, _ := loginSession(t, s, "test harness")
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

// TestTheWorkersOwnFilesAnswerWithNoSession: a service worker fetches its own scripts with no session to
// present, and it precaches `/index.html` by name and abandons its install on a non-200. The companion
// script it imports and the document it caches are therefore answered before anyone signs in.
func TestTheWorkersOwnFilesAnswerWithNoSession(t *testing.T) {
	s := testServer(t)

	get := func(path string) string {
		req := secureRequest(httptest.NewRequest(http.MethodGet, path, nil))
		req.Host = "localhost"
		rr := httptest.NewRecorder()
		// The chain Serve installs over the router, which is what serves the frontend — the bare mux
		// answers nothing outside the API.
		s.middlewareChain(s.router).ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s with no session: got %d %q, want 200", path, rr.Code, rr.Body.String())
		}
		return rr.Body.String()
	}

	// The companion script, which the worker imports for every notification it shows.
	if body := get("/sw-notifications.js"); !strings.Contains(body, "notificationclick") {
		t.Errorf("body = %q, want the click handler the worker imports", body)
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
	cookie, _ := loginSession(t, s, "test harness")

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
				cookie, csrf := loginSession(t, s, "test harness")
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

// TestPanicRecovery_ReportsTheRouteAndLeaksNothing: a recovered panic answers MIKE's own 500, and
// neither end of the record repeats what it caught. The client never sees the panic value; the log names
// the route and never the request, because a preview path segment is a live bearer token.
func TestPanicRecovery_ReportsTheRouteAndLeaksNothing(t *testing.T) {
	t.Run("the panic value does not reach the client", func(t *testing.T) {
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
	})

	t.Run("the log names the route, not the preview token", func(t *testing.T) {
		sink, restore := logging.TestSink()
		defer restore()

		const token = "0123456789abcdef0123456789abcdef"
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/preview/{token}/{path...}", func(http.ResponseWriter, *http.Request) {
			panic("preview handler exploded")
		})

		rr := httptest.NewRecorder()
		PanicRecoveryMiddleware(mux).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/preview/"+token+"/index.html", nil))

		if rr.Code != http.StatusInternalServerError {
			t.Errorf("a recovered panic answered %d, want 500", rr.Code)
		}
		if out := sink.String(); !strings.Contains(out, "/api/preview/{token}/{path...}") {
			t.Errorf("the panic log names no route, so it cannot be triaged: %s", out)
		} else if strings.Contains(out, token) {
			t.Errorf("the panic log carries the preview token: %s", out)
		}
	})
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

// TestHostPinMiddleware is the allowlist as one matrix: the loopback names, the address the server
// listens on (the container/reverse-proxy escape hatch, which accepts that address with or without its
// port), and nothing else. A rebinding attack has to present its own origin as Host — exactly the value
// that is not allowed — and a missing Host names no origin to check at all.
func TestHostPinMiddleware(t *testing.T) {
	tests := []struct {
		name string
		addr string // what the server listens on; ":0" contributes no host to the allowlist
		host string
		want int
	}{
		{name: "localhost with port", addr: ":0", host: "localhost:3000", want: http.StatusOK},
		{name: "localhost without port", addr: ":0", host: "localhost", want: http.StatusOK},
		{name: "IPv4 loopback", addr: ":0", host: "127.0.0.1:3000", want: http.StatusOK},
		{name: "IPv6 loopback bracketed", addr: ":0", host: "[::1]:3000", want: http.StatusOK},
		{name: "IPv6 loopback bare", addr: ":0", host: "::1", want: http.StatusOK},
		{name: "uppercase LOCALHOST", addr: ":0", host: "LOCALHOST:3000", want: http.StatusOK},
		// A rebinding attack must present its own origin as Host — which is
		// exactly the value that is not allowed.
		{name: "attacker domain", addr: ":0", host: "evil.test:3000", want: http.StatusMisdirectedRequest},
		{name: "attacker domain pointing at loopback", addr: ":0", host: "rebind.attacker.io", want: http.StatusMisdirectedRequest},
		{name: "foreign LAN ip", addr: ":0", host: "10.1.2.3:3000", want: http.StatusMisdirectedRequest},
		{name: "empty host", addr: ":0", host: "", want: http.StatusMisdirectedRequest},
		// Listening on an explicit address pins that address as an accepted Host value too.
		{name: "the listen address", addr: "192.168.1.50:3000", host: "192.168.1.50:3000", want: http.StatusOK},
		{name: "the listen address without its port", addr: "192.168.1.50:3000", host: "192.168.1.50", want: http.StatusOK},
		{name: "loopback reaches an explicit listen address", addr: "192.168.1.50:3000", host: "localhost:3000", want: http.StatusOK},
		{name: "a neighbour of the listen address", addr: "192.168.1.50:3000", host: "192.168.1.51:3000", want: http.StatusMisdirectedRequest},
		{name: "a domain in front of the listen address", addr: "192.168.1.50:3000", host: "evil.test", want: http.StatusMisdirectedRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{addr: tt.addr}
			req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
			req.Host = tt.host
			rr := httptest.NewRecorder()
			s.HostPinMiddleware(okHostHandler()).ServeHTTP(rr, req)

			if rr.Code != tt.want {
				t.Errorf("Host %q: status = %d, want %d (body %q)", tt.host, rr.Code, tt.want, rr.Body.String())
			}
			if tt.want == http.StatusMisdirectedRequest && !strings.Contains(rr.Body.String(), "error") {
				t.Errorf("Host %q: 421 body is not a JSON error: %q", tt.host, rr.Body.String())
			}
		})
	}
}

// TestHostPinMiddlewareAnswersFromTheResolvedList pins the --allowed-hosts / MIKE_ALLOWED_HOSTS plumbing
// end to end. The variable reaches the allowlist only through the one ladder that resolves it
// (variable → startup.Config → middleware), the wired list is the whole answer at request time, and an
// entry is matched literally, case-insensitively and with the port stripped on both sides. The variable
// stays set below precisely to prove the middle part: a middleware that re-read it would let a stale
// environment widen the allowlist of a deployment that switched to the flag, which is why the ladder
// resolves once and the flag replaces rather than extends.
func TestHostPinMiddlewareAnswersFromTheResolvedList(t *testing.T) {
	// env.example.com is in the environment and deliberately absent from the wired list below.
	t.Setenv(startup.EnvAllowedHosts, "kanban.example.com, staging.lan, other.lan, env.example.com")
	t.Setenv(startup.EnvEncryptionKey, "a-long-enough-test-key")
	resolved, err := startup.Resolve(startup.Flags{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(resolved.AllowedHosts) != 4 {
		t.Fatalf("resolved extras = %q, want the four entries split", resolved.AllowedHosts)
	}
	wired := []string{"kanban.example.com", "staging.lan", "other.lan"}
	if strings.Join(resolved.AllowedHosts, ",") != strings.Join(wired, ",")+",env.example.com" {
		t.Fatalf("resolved extras = %q, want the wired entries followed by the one left out below", resolved.AllowedHosts)
	}

	s := testServerWith(t, func(c *startup.Config) { c.AllowedHosts = resolved.AllowedHosts[:3] })

	tests := []struct {
		host string
		want int
	}{
		{host: "kanban.example.com", want: http.StatusOK},
		{host: "kanban.example.com:443", want: http.StatusOK},
		{host: "KANBAN.EXAMPLE.COM", want: http.StatusOK},
		{host: "staging.lan:8443", want: http.StatusOK},
		{host: "OTHER.LAN", want: http.StatusOK},
		{host: "localhost:3000", want: http.StatusOK},
		{host: "evil.test:3000", want: http.StatusMisdirectedRequest},
		// Prefix/suffix tricks must not match an allowlist entry.
		{host: "kanban.example.com.evil.test", want: http.StatusMisdirectedRequest},
		{host: "xkanban.example.com", want: http.StatusMisdirectedRequest},
		// Set in the environment and wired out of the list: the request-time read is gone.
		{host: "env.example.com", want: http.StatusMisdirectedRequest},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
			req.Host = tt.host
			rr := httptest.NewRecorder()
			s.HostPinMiddleware(okHostHandler()).ServeHTTP(rr, req)
			if rr.Code != tt.want {
				t.Errorf("Host %q: status = %d, want %d", tt.host, rr.Code, tt.want)
			}
		})
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
