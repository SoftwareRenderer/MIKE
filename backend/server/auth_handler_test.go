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
			// Only POST is public on a ceremony route, so middleware gates GET before the handler runs.
			// An unpaired install's sign-in answers 404, which TestMiddlewareAuthenticationGating holds: it
			// posts the same route with the same expectation.
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

// TestAuthSessionAnswersTheLoginFormAndNothingElse is the login form's branch and its ceiling in one
// pass: the unpaired install, the paired install with no session, and the signed-in browser. Two
// booleans decide which form the browser shows; a CSRF token rides only on the answer for a session
// that exists; no credential detail rides on any of them.
func TestAuthSessionAnswersTheLoginFormAndNothingElse(t *testing.T) {
	s := testServer(t)

	ask := func(cookie string) map[string]any {
		t.Helper()
		req := testRequest(http.MethodGet, "/api/auth/session", nil)
		if cookie != "" {
			withAuth(req, cookie)
		}
		return mustJSON[map[string]any](t, testResponse(t, s, req).Body)
	}

	unpaired := ask("")
	if unpaired["authenticated"] != false {
		t.Errorf("authenticated = %v; want false", unpaired["authenticated"])
	}
	if unpaired["has_passkey"] != false {
		t.Errorf("has_passkey = %v; want false on an unpaired install", unpaired["has_passkey"])
	}
	// The pairing notice offers the CA download only when this server would answer one, and this is the
	// only answer an unauthenticated browser can get.
	if unpaired["tls_ca_available"] != true {
		t.Errorf("tls_ca_available = %v; want true with the local CA this test server runs", unpaired["tls_ca_available"])
	}
	if _, found := unpaired["csrf_token"]; found {
		t.Error("the anonymous answer carries a csrf_token; the token belongs to a session, and this route is public")
	}

	_, cookie := registerTestPasskey(t, s)

	// The gate's other branch, still with no session: the install can sign in, so the form offers the
	// passkey rather than the code field.
	paired := ask("")
	if paired["has_passkey"] != true {
		t.Errorf("has_passkey = %v; want true once a credential is registered", paired["has_passkey"])
	}
	if paired["authenticated"] != false {
		t.Errorf("authenticated = %v; want false with no session presented", paired["authenticated"])
	}

	signedIn := ask(cookie)
	if signedIn["authenticated"] != true {
		t.Error("expected authenticated = true")
	}
	if csrf, _ := signedIn["csrf_token"].(string); csrf == "" {
		t.Error("expected csrf_token in session response")
	}

	for name, answer := range map[string]map[string]any{
		"unpaired": unpaired, "paired": paired, "signed in": signedIn,
	} {
		for _, leaked := range []string{"credential_id", "passkeys", "record"} {
			if _, found := answer[leaked]; found {
				t.Errorf("the %s answer carries %q; a sign-in check answers whether the browser is signed in and "+
					"whether the install is paired, nothing else", name, leaked)
			}
		}
	}
}

// TestAuthSessionNamesTheBrowserThatAsked: the form's prefilled label comes from this answer, read by the
// function that names a row nobody named.
func TestAuthSessionNamesTheBrowserThatAsked(t *testing.T) {
	s := testServer(t)

	named := testRequest(http.MethodGet, "/api/auth/session", nil)
	named.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "+
		"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	if body := mustJSON[map[string]any](t, testResponse(t, s, named).Body); body["device_name"] != "Chrome 120 \u00b7 Macintosh" {
		t.Errorf("device_name = %v; want the label this User-Agent reads as", body["device_name"])
	}

	// An unreadable User-Agent gets no name, not the placeholder a stored row gets.
	unreadable := testRequest(http.MethodGet, "/api/auth/session", nil)
	unreadable.Header.Set("User-Agent", "Go-http-client/1.1")
	if body := mustJSON[map[string]any](t, testResponse(t, s, unreadable).Body); body["device_name"] != nil {
		t.Errorf("device_name = %v; want the field left out when the User-Agent says nothing", body["device_name"])
	}
}

// TestAuthLogoutEndsTheSessionAndClearsTheCookie: the browser drops only a cookie whose attributes
// match the one it holds, so a clearing response that lost HttpOnly or SameSite=Lax leaves the session
// live in the jar while the screen says signed out. Both halves are the same decision: the answer to
// the click has to end the session for the browser as well as in the store.
func TestAuthLogoutEndsTheSessionAndClearsTheCookie(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)

	logoutReq := testRequest(http.MethodPost, "/api/auth/logout", nil)
	withAuthCSRF(logoutReq, cookie, csrfToken)
	rr := testResponse(t, s, logoutReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("logout: status = %d; body: %s", rr.Code, rr.Body.String())
	}

	var cleared bool
	for _, line := range rr.Header().Values("Set-Cookie") {
		if !strings.Contains(line, "Max-Age=0") {
			continue
		}
		cleared = true
		for _, flag := range []string{"HttpOnly", "SameSite=Lax"} {
			if !strings.Contains(line, flag) {
				t.Errorf("the clearing cookie lost an attribute the session cookie carried: %s", line)
			}
		}
	}
	if !cleared {
		t.Fatalf("logout set no clearing cookie: %q", rr.Header().Values("Set-Cookie"))
	}

	sessionReq := testRequest(http.MethodGet, "/api/auth/session", nil)
	withAuth(sessionReq, cookie)
	rr = testResponse(t, s, sessionReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("session check: status = %d", rr.Code)
	}
	if body := mustJSON[map[string]bool](t, rr.Body); body["authenticated"] {
		t.Error("expected authenticated = false after logout")
	}
}
