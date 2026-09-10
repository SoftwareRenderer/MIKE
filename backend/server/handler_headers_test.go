package server

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kanban/backend/startup"
)

// TestSessionCookieSecureFlag pins what decides the Secure flag. A TLS-terminating proxy is the
// case that cannot be read from this process's own configuration — nothing about MIKE says
// "encrypted" — so the answer comes from the request, and a boot-time answer would be wrong for
// one of the two deployments the same binary serves.
func TestSessionCookieSecureFlag(t *testing.T) {
	cases := []struct {
		name       string
		tls        bool // the request arrived over TLS terminated here
		forwarded  string
		trustProxy bool
		wantSecure bool
	}{
		{name: "TLS terminated here", tls: true, wantSecure: true},
		{
			// The only case that stays unencrypted: --no-tls with no proxy, which is how
			// development and the e2e harness run. Secure here would mean a cookie the
			// browser never stores, so an app nobody can log into.
			name:       "cleartext with no proxy",
			wantSecure: false,
		},
		{
			name:       "a TLS-terminating proxy that says so, under MIKE_TRUST_PROXY",
			forwarded:  "https",
			trustProxy: true,
			wantSecure: true,
		},
		{
			// The same header from a client nobody vouches for. Honouring it would let a
			// request pick its own protection level — and a Secure cookie the browser will
			// not send back over the cleartext the operator is actually on is its own
			// denial of service.
			name:       "X-Forwarded-Proto from an untrusted client",
			forwarded:  "https",
			wantSecure: false,
		},
		{
			name:       "a proxy forwarding http under MIKE_TRUST_PROXY",
			forwarded:  "http",
			trustProxy: true,
			wantSecure: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Proxy trust is the resolved decision the server was built with, so the case says which
			// process it is pretending to have booted rather than editing this one.
			s := testServerWith(t, func(c *startup.Config) { c.TrustProxy = tc.trustProxy })

			req := testRequest(http.MethodPost, "/api/auth/login", map[string]string{"password": testPassword})
			req.Host = "localhost"
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-Proto", tc.forwarded)
			}
			rr := testResponse(t, s, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("login: %d %s", rr.Code, rr.Body.String())
			}

			set := rr.Header().Get("Set-Cookie")
			if !strings.Contains(set, "HttpOnly") || !strings.Contains(set, "SameSite=Lax") {
				t.Fatalf("the other flags drifted, so this test proves nothing: %q", set)
			}
			if got := strings.Contains(set, "Secure"); got != tc.wantSecure {
				t.Errorf("Secure = %v, want %v in %q", got, tc.wantSecure, set)
			}

			// Logout has to clear the cookie with the flag it was set with, or the jar
			// keeps a session the user believes they ended.
			value := strings.SplitN(strings.TrimPrefix(set, "PHPSESSID="), ";", 2)[0]
			if value == "" {
				t.Fatalf("login set no session token: %q", set)
			}
			logout := testRequest(http.MethodPost, "/api/auth/logout", nil)
			logout.Host = "localhost"
			withAuthCSRF(logout, set, rr.Header().Get("X-CSRF-Token"))
			if tc.tls {
				logout.TLS = &tls.ConnectionState{}
			}
			if tc.forwarded != "" {
				logout.Header.Set("X-Forwarded-Proto", tc.forwarded)
			}
			lr := testResponse(t, s, logout)
			if lr.Code != http.StatusOK {
				t.Fatalf("logout: %d %s", lr.Code, lr.Body.String())
			}
			out := lr.Header().Get("Set-Cookie")
			if got := strings.Contains(out, "Secure"); got != tc.wantSecure {
				t.Errorf("the clearing cookie's Secure = %v, want %v in %q", got, tc.wantSecure, out)
			}
		})
	}
}

// TestIsolationHeaders lists what every response carries and, more usefully, the paths that must not
// carry Cross-Origin-Resource-Policy: an opaque-origin frame cannot fetch a subresource marked
// same-origin, so marking one of them switches that extension or preview off.
func TestIsolationHeaders(t *testing.T) {
	s := testServer(t)

	cases := []struct {
		path       string
		wantCORP   bool
		wantPerms  bool
		wantNoCorp string
	}{
		{path: "/", wantCORP: true, wantPerms: true},
		{path: "/api/config", wantCORP: true, wantPerms: true},
		{path: "/api/preview/deadbeef/nav", wantCORP: true, wantPerms: true},
		{path: "/api/tls/ca", wantCORP: true, wantPerms: true},
		{
			// The bridge every extension entry loads from an opaque-origin frame.
			path:       "/host-client.js",
			wantPerms:  true,
			wantNoCorp: "no extension can fetch its bridge if this is marked",
		},
		{
			// One exact path, not any file that happens to end in the name.
			path:      "/assets/host-client.js",
			wantCORP:  true,
			wantPerms: true,
		},
		{
			path:       "/extensions/acme/pack/index.html",
			wantPerms:  true,
			wantNoCorp: "an opaque-origin extension iframe loads its own files as subresources",
		},
		{
			path:       "/api/preview/deadbeef/style.css",
			wantPerms:  true,
			wantNoCorp: "an opaque-origin preview document loads its own assets the same way",
		},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Host = "localhost"
			rr := httptest.NewRecorder()
			// The chain Serve installs, not the bare mux: a header set by a middleware
			// that is not wired is a header no response carries.
			s.middlewareChain(s.mux).ServeHTTP(rr, req)

			if got := rr.Header().Get("Cross-Origin-Resource-Policy"); (got == "same-origin") != tc.wantCORP {
				if tc.wantCORP {
					t.Errorf("Cross-Origin-Resource-Policy = %q; want same-origin", got)
				} else {
					t.Errorf("Cross-Origin-Resource-Policy = %q; want none — %s", got, tc.wantNoCorp)
				}
			}
			pp := rr.Header().Get("Permissions-Policy")
			if tc.wantPerms && !strings.Contains(pp, "camera=()") {
				t.Errorf("Permissions-Policy = %q; want camera denied", pp)
			}
			// A clipboard rule would break the terminal's paste, which reads it.
			if strings.Contains(pp, "clipboard") {
				t.Errorf("Permissions-Policy restricts the clipboard: %q", pp)
			}
		})
	}
}
