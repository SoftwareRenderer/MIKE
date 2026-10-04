package server

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/startup"
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
			// Cleartext that reached a handler at all: the chain's HTTPS redirect answers
			// this request in a real deployment, and it is pinned here because the flag and
			// that redirect read one function, and because logout must clear with the flag
			// login set whatever the answer is.
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

			// The ceremony is the one path that mints a session cookie, and its expected origin comes
			// from the same answer as the cookie's Secure flag — so the transport is said once here.
			_, finished := registerPasskeyOver(t, s, "test laptop", ceremonyTransport{
				TLS:       tc.tls,
				Forwarded: tc.forwarded,
				Secure:    tc.wantSecure,
			})
			set := finished.Header().Get("Set-Cookie")
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
				t.Fatalf("the ceremony set no session token: %q", set)
			}
			logout := testRequest(http.MethodPost, "/api/auth/logout", nil)
			logout.Host = "localhost"
			withAuthCSRF(logout, set, finished.Header().Get("X-CSRF-Token"))
			// The clearing request has to arrive on the connection the ceremony did, or the flag it
			// clears with is not the flag login set.
			logout.TLS = nil
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
		path        string
		wantCORP    bool
		wantPerms   bool
		wantSandbox bool
		wantNoCorp  string
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
			path:        "/extensions/acme/pack/index.html",
			wantPerms:   true,
			wantSandbox: true,
			wantNoCorp:  "an opaque-origin extension iframe loads its own files as subresources",
		},
		{
			path:       "/api/preview/deadbeef/style.css",
			wantPerms:  true,
			wantNoCorp: "an opaque-origin preview document loads its own assets the same way",
		},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			req := secureRequest(httptest.NewRequest(http.MethodGet, tc.path, nil))
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

			// The sandbox directive belongs to documents this app did not write.
			csp := rr.Header().Get("Content-Security-Policy")
			if got := strings.Contains(csp, "sandbox allow-scripts"); got != tc.wantSandbox {
				if tc.wantSandbox {
					t.Errorf("Content-Security-Policy = %q; want sandbox allow-scripts", csp)
				} else {
					t.Errorf("Content-Security-Policy = %q; want no sandbox directive", csp)
				}
			}
			if tc.wantSandbox && !strings.Contains(csp, "frame-ancestors 'self'") {
				t.Errorf("Content-Security-Policy = %q; want frame-ancestors 'self'", csp)
			}
		})
	}
}

// TestExtensionListsAreStoredThenRevalidated pins the lifetime of an extension list answer, which is
// what Settings → Extensions → Browse paints from. `Cache-Control: no-cache` is the HTTP sentence
// "keep it, then ask before showing it", and the thing it asks with is the digest CompressMiddleware
// computes from the body — so a handler that set its own ETag would replace a correct cache with no
// revalidation at all. A 304 that loses either header leaves the client holding an answer whose
// lifetime it cannot tell.
func TestExtensionListsAreStoredThenRevalidated(t *testing.T) {
	indexPath := filepath.Join(t.TempDir(), "registry.json")
	if err := os.WriteFile(indexPath, []byte(`[{"name":"gopls","version":"1.0.0","type":"lsp","display_name":"gopls"}]`), 0o644); err != nil {
		t.Fatalf("write registry index: %v", err)
	}
	s := testServerWith(t, func(c *startup.Config) { c.RegistryURL = "file://" + indexPath })
	cookie := loginTest(t, s)

	get := func(path, ifNoneMatch string) *httptest.ResponseRecorder {
		req := testRequest(http.MethodGet, path, nil)
		req.Header.Set("Cookie", cookie)
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		return testResponse(t, s, req)
	}

	for _, path := range []string{"/api/extensions", "/api/extensions/registry", "/api/extensions/local"} {
		first := get(path, "")
		if first.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200: %s", path, first.Code, first.Body.String())
		}
		if got := first.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("GET %s Cache-Control = %q, want %q", path, got, "no-cache")
		}
		etag := first.Header().Get("ETag")
		if etag == "" {
			t.Fatalf("GET %s carried no ETag, so nothing can revalidate a stored copy", path)
		}

		second := get(path, etag)
		if second.Code != http.StatusNotModified {
			t.Errorf("GET %s with If-None-Match = %d, want 304: %s", path, second.Code, second.Body.String())
		}
		if second.Body.Len() != 0 {
			t.Errorf("GET %s answered 304 with %d bytes of body", path, second.Body.Len())
		}
		if got := second.Header().Get("ETag"); got != etag {
			t.Errorf("GET %s 304 ETag = %q, want %q", path, got, etag)
		}
		if got := second.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("GET %s 304 Cache-Control = %q, want %q", path, got, "no-cache")
		}
	}
}
