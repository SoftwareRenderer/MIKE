package server

import (
	"net/http"
	"strings"
	"testing"
)

// TestAuthRequestRejections is the auth endpoint's rejection matrix: one row per way a request may be
// turned away, differing only in method, path, body and the status the caller gets back. Each row gets
// its own server so the ceremony rate-limit budget (per IP per minute) can't be exhausted by an earlier
// row and report a 429 as a wrong verdict.
func TestAuthRequestRejections(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   any
		want   int
	}{
		{
			name:   "registration with the wrong pairing code",
			method: http.MethodPost,
			path:   "/api/auth/passkey/register/begin",
			body:   map[string]string{"code": "NOSUCHCODE"},
			want:   http.StatusUnauthorized,
		},
		{
			name:   "registration without a code",
			method: http.MethodPost,
			path:   "/api/auth/passkey/register/begin",
			body:   map[string]string{},
			want:   http.StatusUnauthorized,
		},
		{
			// The finish half has nothing to verify against until begin has run.
			name:   "registration finish without a ticket",
			method: http.MethodPost,
			path:   "/api/auth/passkey/register/finish",
			body:   map[string]string{},
			want:   http.StatusBadRequest,
		},
		{
			name:   "sign-in on an install with no passkey",
			method: http.MethodPost,
			path:   "/api/auth/passkey/assert/begin",
			want:   http.StatusNotFound,
		},
		{
			// Only POST is public on a ceremony route, so middleware gates GET before the handler runs.
			name:   "GET on a ceremony endpoint",
			method: http.MethodGet,
			path:   "/api/auth/passkey/assert/begin",
			want:   http.StatusUnauthorized,
		},
		{
			name:   "logout while unauthenticated",
			method: http.MethodPost,
			path:   "/api/auth/logout",
			want:   http.StatusUnauthorized,
		},
		{
			name:   "GET on the logout endpoint",
			method: http.MethodGet,
			path:   "/api/auth/logout",
			want:   http.StatusUnauthorized,
		},
		{
			name:   "POST on the session endpoint",
			method: http.MethodPost,
			path:   "/api/auth/session",
			want:   http.StatusUnauthorized,
		},
		{
			name:   "pairing mint while unauthenticated",
			method: http.MethodPost,
			path:   "/api/auth/pairing",
			want:   http.StatusUnauthorized,
		},
		{
			name:   "listing passkeys while unauthenticated",
			method: http.MethodGet,
			path:   "/api/auth/passkeys",
			want:   http.StatusUnauthorized,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			rr := testResponse(t, s, testRequest(tc.method, tc.path, tc.body))
			if rr.Code != tc.want {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

// TestAuthSessionAnswersWhetherTheInstallIsPaired is the login form's branch: a boolean, and no
// credential detail of any kind.
func TestAuthSessionAnswersWhetherTheInstallIsPaired(t *testing.T) {
	s := testServer(t)

	body := mustJSON[map[string]any](t, testResponse(t, s,
		testRequest(http.MethodGet, "/api/auth/session", nil)).Body)
	if body["authenticated"] != false {
		t.Errorf("authenticated = %v; want false", body["authenticated"])
	}
	if body["has_passkey"] != false {
		t.Errorf("has_passkey = %v; want false on an unpaired install", body["has_passkey"])
	}
	// The pairing notice offers the CA download only when this server would answer one, and this is the
	// only answer an unauthenticated browser can get.
	if body["tls_ca_available"] != true {
		t.Errorf("tls_ca_available = %v; want true with the local CA this test server runs", body["tls_ca_available"])
	}

	registerTestPasskey(t, s)

	body = mustJSON[map[string]any](t, testResponse(t, s,
		testRequest(http.MethodGet, "/api/auth/session", nil)).Body)
	if body["has_passkey"] != true {
		t.Errorf("has_passkey = %v; want true once a credential is registered", body["has_passkey"])
	}
	for _, leaked := range []string{"credential_id", "passkeys", "record"} {
		if _, found := body[leaked]; found {
			t.Errorf("the session answer carries %q; a sign-in check answers whether the browser is signed in and "+
				"whether the install is paired, nothing else", leaked)
		}
	}
}

func TestAuthSessionNotAuthenticated(t *testing.T) {
	s := testServer(t)

	rr := testResponse(t, s, testRequest(http.MethodGet, "/api/auth/session", nil))

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

	sessionReq := testRequest(http.MethodGet, "/api/auth/session", nil)
	sessionReq.Header.Set("Cookie", loginTest(t, s))

	rr := testResponse(t, s, sessionReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", rr.Code, rr.Body.String())
	}

	body := mustJSON[map[string]any](t, rr.Body)
	if body["authenticated"] != true {
		t.Error("expected authenticated = true")
	}
	// CSRF token should be present in session response
	if body["csrf_token"] == nil || body["csrf_token"] == "" {
		t.Error("expected csrf_token in session response")
	}
}

func TestAuthLogoutSuccess(t *testing.T) {
	s := testServer(t)

	cookie, csrfToken := loginTestCSRF(t, s)

	logoutReq := testRequest(http.MethodPost, "/api/auth/logout", nil)
	withAuthCSRF(logoutReq, cookie, csrfToken)
	rr := testResponse(t, s, logoutReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("logout: status = %d; body: %s", rr.Code, rr.Body.String())
	}

	// Session should be unauthenticated now
	sessionReq := testRequest(http.MethodGet, "/api/auth/session", nil)
	withAuth(sessionReq, cookie)
	rr = testResponse(t, s, sessionReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("session check: status = %d", rr.Code)
	}

	body := mustJSON[map[string]bool](t, rr.Body)
	if body["authenticated"] {
		t.Error("expected authenticated = false after logout")
	}
}

// TestAuthLogoutClearsTheCookieWithTheFlagsItWasSetWith: the browser only drops a cookie whose
// attributes match, so a logout that clears with a different Secure flag leaves the session live.
func TestAuthLogoutClearsTheCookieWithTheFlagsItWasSetWith(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)

	req := testRequest(http.MethodPost, "/api/auth/logout", nil)
	withAuthCSRF(req, cookie, csrfToken)
	set := testResponse(t, s, req).Header().Values("Set-Cookie")
	if len(set) == 0 {
		t.Fatal("logout set no cookie")
	}
	for _, line := range set {
		if !strings.Contains(line, "Max-Age=0") {
			continue
		}
		if !strings.Contains(line, "HttpOnly") || !strings.Contains(line, "SameSite=Lax") {
			t.Errorf("the clearing cookie lost an attribute the session cookie carried: %s", line)
		}
	}
}
