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
