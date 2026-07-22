package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestAuthLoginValidPassword(t *testing.T) {
	s := testServer(t)

	req := testRequest(http.MethodPost, "/api/auth/login", map[string]string{
		"password": testPassword,
	})
	rr := testResponse(t, s, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := mustJSON[map[string]string](t, rr.Body)
	if body["status"] != "ok" {
		t.Errorf("status = %q; want ok", body["status"])
	}

	// Check that a session cookie was set
	header := rr.Header().Get("Set-Cookie")
	if !strings.Contains(header, "PHPSESSID=") {
		t.Error("expected PHPSESSID cookie in Set-Cookie header")
	}
	if !strings.Contains(header, "HttpOnly") {
		t.Error("expected HttpOnly flag on PHPSESSID cookie")
	}
	if !strings.Contains(header, "SameSite=Lax") {
		t.Error("expected SameSite=Lax flag on PHPSESSID cookie")
	}

	// Check that CSRF token is in response header
	csrfToken := rr.Header().Get("X-CSRF-Token")
	if csrfToken == "" {
		t.Error("expected X-CSRF-Token header in login response")
	}
	if len(csrfToken) != 32 {
		t.Errorf("X-CSRF-Token length = %d; want 32", len(csrfToken))
	}
}

func TestAuthLoginInvalidPassword(t *testing.T) {
	s := testServer(t)

	req := testRequest(http.MethodPost, "/api/auth/login", map[string]string{
		"password": "wrong-password",
	})
	rr := testResponse(t, s, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401; body: %s", rr.Code, rr.Body.String())
	}
}

func TestAuthLoginMissingPassword(t *testing.T) {
	s := testServer(t)

	req := testRequest(http.MethodPost, "/api/auth/login", map[string]string{})
	rr := testResponse(t, s, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400; body: %s", rr.Code, rr.Body.String())
	}
}

func TestAuthLoginWrongMethod(t *testing.T) {
	s := testServer(t)

	// GET /api/auth/login is not a public path+method, so middleware blocks it
	req := testRequest(http.MethodGet, "/api/auth/login", nil)
	rr := testResponse(t, s, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401; body: %s", rr.Code, rr.Body.String())
	}
}

func TestAuthSessionNotAuthenticated(t *testing.T) {
	s := testServer(t)

	req := testRequest(http.MethodGet, "/api/auth/session", nil)
	rr := testResponse(t, s, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := mustJSON[map[string]bool](t, rr.Body)
	if body["authenticated"] {
		t.Error("expected authenticated = false")
	}
}

func TestAuthSessionAuthenticated(t *testing.T) {
	s := testServer(t)

	// Login first
	loginReq := testRequest(http.MethodPost, "/api/auth/login", map[string]string{
		"password": testPassword,
	})
	loginRR := testResponse(t, s, loginReq)
	if loginRR.Code != http.StatusOK {
		t.Fatalf("login: status = %d", loginRR.Code)
	}

	// Extract session cookie
	sessionCookie := loginRR.Header().Get("Set-Cookie")
	if sessionCookie == "" {
		t.Fatal("expected PHPSESSID cookie after login")
	}

	// Check session with cookie
	sessionReq := testRequest(http.MethodGet, "/api/auth/session", nil)
	parts := strings.Split(sessionCookie, ";")[0]
	sessionReq.Header.Set("Cookie", strings.TrimSpace(parts))

	rr := testResponse(t, s, sessionReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := mustJSON[map[string]any](t, rr.Body)
	if body["authenticated"] != true {
		t.Error("expected authenticated = true after login")
	}
	// CSRF token should be present in session response
	if body["csrf_token"] == nil || body["csrf_token"] == "" {
		t.Error("expected csrf_token in session response")
	}
}

func TestAuthLogoutNotAuthenticated(t *testing.T) {
	s := testServer(t)

	req := testRequest(http.MethodPost, "/api/auth/logout", nil)
	rr := testResponse(t, s, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401; body: %s", rr.Code, rr.Body.String())
	}
}

func TestAuthLogoutSuccess(t *testing.T) {
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

	// Logout with session cookie and CSRF token
	logoutReq := testRequest(http.MethodPost, "/api/auth/logout", nil)
	logoutReq.Header.Set("Cookie", strings.TrimSpace(parts))
	logoutReq.Header.Set("X-CSRF-Token", csrfToken)
	rr := testResponse(t, s, logoutReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("logout: status = %d; body: %s", rr.Code, rr.Body.String())
	}

	// Session should be unauthenticated now
	sessionReq := testRequest(http.MethodGet, "/api/auth/session", nil)
	sessionReq.Header.Set("Cookie", strings.TrimSpace(parts))
	rr = testResponse(t, s, sessionReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("session check: status = %d", rr.Code)
	}

	body := mustJSON[map[string]bool](t, rr.Body)
	if body["authenticated"] {
		t.Error("expected authenticated = false after logout")
	}
}

func TestAuthLogoutWrongMethod(t *testing.T) {
	s := testServer(t)

	// GET /api/auth/logout requires auth (only POST is the handler, and it requires session)
	req := testRequest(http.MethodGet, "/api/auth/logout", nil)
	rr := testResponse(t, s, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401; body: %s", rr.Code, rr.Body.String())
	}
}

func TestAuthSessionWrongMethod(t *testing.T) {
	s := testServer(t)

	// POST /api/auth/session is not a public path+method, so middleware blocks it
	req := testRequest(http.MethodPost, "/api/auth/session", nil)
	rr := testResponse(t, s, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401; body: %s", rr.Code, rr.Body.String())
	}
}
