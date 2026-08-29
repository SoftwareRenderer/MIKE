package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// loginSession logs in through the API with an explicit User-Agent and returns
// the session cookie and CSRF token.
func loginSession(t *testing.T, s *Server, userAgent string) (cookie, csrfToken string) {
	t.Helper()
	req := testRequest(http.MethodPost, "/api/auth/login", map[string]string{
		"password": testPassword,
	})
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rr.Code, rr.Body.String())
	}
	return rr.Header().Get("Set-Cookie"), rr.Header().Get("X-CSRF-Token")
}

// getSessionList calls GET /api/sessions with the given session cookie and
// returns the decoded rows plus the raw body.
func getSessionList(t *testing.T, s *Server, cookie string) ([]sessionInfoResponse, string) {
	t.Helper()
	req := testRequest(http.MethodGet, "/api/sessions", nil)
	if cookie != "" {
		withAuth(req, cookie)
	}
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list sessions: %d %s", rr.Code, rr.Body.String())
	}
	var out []sessionInfoResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode sessions: %v (body: %s)", err, rr.Body)
	}
	return out, rr.Body.String()
}

// currentSessionOf returns the row marked current for the given cookie.
func currentSessionOf(t *testing.T, s *Server, cookie string) sessionInfoResponse {
	t.Helper()
	list, _ := getSessionList(t, s, cookie)
	var found []sessionInfoResponse
	for _, se := range list {
		if se.Current {
			found = append(found, se)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected exactly one current session, got %d in %+v", len(found), list)
	}
	return found[0]
}

func TestSessionsListShowsCurrentSessionAndNoSecrets(t *testing.T) {
	s := testServer(t)

	cookieA, _ := loginSession(t, s, "Agent-A/1.0")
	cookieB, _ := loginSession(t, s, "Agent-B/2.0")

	list, raw := getSessionList(t, s, cookieB)
	if len(list) != 2 {
		t.Fatalf("sessions = %d; want 2 (%+v)", len(list), list)
	}

	// Only the caller's row is current — and it is the same row on a repeat call.
	cur := currentSessionOf(t, s, cookieB)
	if cur.UserAgent != "Agent-B/2.0" {
		t.Errorf("current row is %q; want the Agent-B/2.0 session", cur.UserAgent)
	}
	if curA := currentSessionOf(t, s, cookieA); curA.ID == cur.ID {
		t.Error("both sessions report the same current id")
	} else if curA.UserAgent != "Agent-A/1.0" {
		t.Errorf("current row for cookie A is %q", curA.UserAgent)
	}

	// Shapes T2 codes against: RFC3339 timestamps, login IP, User-Agent.
	for _, se := range list {
		if se.ID == "" {
			t.Error("empty id")
		}
		created, err := time.Parse(time.RFC3339, se.CreatedAt)
		if err != nil {
			t.Errorf("created_at %q is not RFC3339: %v", se.CreatedAt, err)
		}
		expires, err := time.Parse(time.RFC3339, se.ExpiresAt)
		if err != nil {
			t.Errorf("expires_at %q is not RFC3339: %v", se.ExpiresAt, err)
		}
		if err == nil && !expires.After(created) {
			t.Errorf("expires_at %q is not after created_at %q", se.ExpiresAt, se.CreatedAt)
		}
		if se.LastSeen == "" {
			t.Errorf("last_seen empty for %s", se.ID)
		} else if _, err := time.Parse(time.RFC3339, se.LastSeen); err != nil {
			t.Errorf("last_seen %q is not RFC3339: %v", se.LastSeen, err)
		}
		// httptest.NewRequest's RemoteAddr.
		if se.IP != "192.0.2.1" {
			t.Errorf("ip = %q; want 192.0.2.1", se.IP)
		}
	}

	// No credential material, ever: the response has no token / token_hash /
	// csrf_token field and never echoes the bearer token itself.
	for _, banned := range []string{"token", "csrf"} {
		if strings.Contains(strings.ToLower(raw), banned) {
			t.Errorf("GET /api/sessions leaked %q: %s", banned, raw)
		}
	}
	if bearer := strings.Split(strings.Split(cookieB, ";")[0], "=")[1]; strings.Contains(raw, bearer) {
		t.Error("GET /api/sessions echoed the session token")
	}
}

func TestSessionsListRequiresAuth(t *testing.T) {
	s := testServer(t)

	req := testRequest(http.MethodGet, "/api/sessions", nil)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401; body: %s", rr.Code, rr.Body.String())
	}
}

func TestSessionRevokeDeletesOneSession(t *testing.T) {
	s := testServer(t)

	cookieA, csrfA := loginSession(t, s, "Agent-A/1.0")
	cookieB, _ := loginSession(t, s, "Agent-B/2.0")

	idB := currentSessionOf(t, s, cookieB).ID

	req := testRequest(http.MethodDelete, "/api/sessions/"+idB, nil)
	withAuthCSRF(req, cookieA, csrfA)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("revoke: status = %d; want 204; body: %s", rr.Code, rr.Body.String())
	}
	if rr.Body.Len() != 0 {
		t.Errorf("204 carried a body: %q", rr.Body.String())
	}

	list, _ := getSessionList(t, s, cookieA)
	if len(list) != 1 || !list[0].Current {
		t.Fatalf("remaining sessions = %+v; want the single current session", list)
	}

	// The revoked session no longer authenticates.
	sessionReq := testRequest(http.MethodGet, "/api/auth/session", nil)
	withAuth(sessionReq, cookieB)
	if body := mustJSON[map[string]any](t, testResponse(t, s, sessionReq).Body); body["authenticated"] != false {
		t.Error("the revoked session is still authenticated")
	}

	// Revoking it again is a 404.
	req = testRequest(http.MethodDelete, "/api/sessions/"+idB, nil)
	withAuthCSRF(req, cookieA, csrfA)
	if rr = testResponse(t, s, req); rr.Code != http.StatusNotFound {
		t.Fatalf("second revoke: status = %d; want 404", rr.Code)
	}
}

func TestSessionRevokeUnknownID(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginSession(t, s, "Agent-A/1.0")

	req := testRequest(http.MethodDelete, "/api/sessions/does-not-exist", nil)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d; want 404; body: %s", rr.Code, rr.Body.String())
	}
}

// TestSessionRevokeCurrentSessionIsAllowed: signing out everywhere else must not
// prevent the user from killing the device they are on — the SPA then 401s on
// its next request and falls back to the login modal.
func TestSessionRevokeCurrentSessionIsAllowed(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginSession(t, s, "Agent-A/1.0")

	id := currentSessionOf(t, s, cookie).ID

	req := testRequest(http.MethodDelete, "/api/sessions/"+id, nil)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d; want 204; body: %s", rr.Code, rr.Body.String())
	}

	req = testRequest(http.MethodGet, "/api/sessions", nil)
	withAuth(req, cookie)
	if rr = testResponse(t, s, req); rr.Code != http.StatusUnauthorized {
		t.Fatalf("after revoking itself, status = %d; want 401", rr.Code)
	}
}

func TestSessionsRevokeOthers(t *testing.T) {
	s := testServer(t)
	cookieA, _ := loginSession(t, s, "Agent-A/1.0")
	cookieB, csrfB := loginSession(t, s, "Agent-B/2.0")
	cookieC, _ := loginSession(t, s, "Agent-C/3.0")

	req := testRequest(http.MethodPost, "/api/sessions/revoke-others", nil)
	withAuthCSRF(req, cookieB, csrfB)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body: %s", rr.Code, rr.Body.String())
	}
	body := mustJSON[map[string]int](t, rr.Body)
	if body["revoked"] != 2 {
		t.Fatalf("revoked = %d; want 2", body["revoked"])
	}

	list, _ := getSessionList(t, s, cookieB)
	if len(list) != 1 || !list[0].Current {
		t.Fatalf("remaining sessions = %+v; want only the caller's", list)
	}

	for _, other := range []string{cookieA, cookieC} {
		sessionReq := testRequest(http.MethodGet, "/api/auth/session", nil)
		withAuth(sessionReq, other)
		if got := mustJSON[map[string]any](t, testResponse(t, s, sessionReq).Body); got["authenticated"] != false {
			t.Error("a non-current session survived revoke-others")
		}
	}

	// Nothing left to revoke.
	req = testRequest(http.MethodPost, "/api/sessions/revoke-others", nil)
	withAuthCSRF(req, cookieB, csrfB)
	if rr = testResponse(t, s, req); rr.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rr.Code)
	}
	if again := mustJSON[map[string]int](t, rr.Body); again["revoked"] != 0 {
		t.Errorf("revoked = %d; want 0", again["revoked"])
	}
}

// TestSessionMutationsRequireCSRF: the CSRF middleware covers the new routes
// automatically because they are not public paths.
func TestSessionMutationsRequireCSRF(t *testing.T) {
	s := testServer(t)
	cookie, _ := loginSession(t, s, "Agent-A/1.0")
	id := currentSessionOf(t, s, cookie).ID

	req := testRequest(http.MethodDelete, "/api/sessions/"+id, nil)
	withAuth(req, cookie) // no X-CSRF-Token
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("DELETE without CSRF: status = %d; want 403; body: %s", rr.Code, rr.Body.String())
	}

	req = testRequest(http.MethodPost, "/api/sessions/revoke-others", nil)
	withAuth(req, cookie)
	if rr = testResponse(t, s, req); rr.Code != http.StatusForbidden {
		t.Fatalf("POST without CSRF: status = %d; want 403; body: %s", rr.Code, rr.Body.String())
	}

	// The session survived both rejected calls.
	if _, raw := getSessionList(t, s, cookie); raw == "" {
		t.Fatal("listing failed after rejected mutations")
	}
}

// TestSessionRoutesUnauthenticated: mutations without any session are rejected
// by SessionMiddleware before CSRF handling.
func TestSessionRoutesUnauthenticated(t *testing.T) {
	s := testServer(t)

	req := testRequest(http.MethodDelete, "/api/sessions/whatever", nil)
	if rr := testResponse(t, s, req); rr.Code != http.StatusUnauthorized {
		t.Fatalf("DELETE unauthenticated: status = %d; want 401", rr.Code)
	}
	req = testRequest(http.MethodPost, "/api/sessions/revoke-others", nil)
	if rr := testResponse(t, s, req); rr.Code != http.StatusUnauthorized {
		t.Fatalf("POST unauthenticated: status = %d; want 401", rr.Code)
	}
}

// TestSessionsRecordLoginIPAndUserAgent: the metadata comes from the login
// request itself, so the Sessions UI can describe each device.
func TestSessionsRecordLoginIPAndUserAgent(t *testing.T) {
	s := testServer(t)

	req := testRequest(http.MethodPost, "/api/auth/login", map[string]string{"password": testPassword})
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) MIKE-Test")
	req.RemoteAddr = "198.51.100.23:41234"
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rr.Code, rr.Body.String())
	}
	cookie := rr.Header().Get("Set-Cookie")

	cur := currentSessionOf(t, s, cookie)
	if cur.UserAgent != "Mozilla/5.0 (X11; Linux x86_64) MIKE-Test" {
		t.Errorf("user_agent = %q", cur.UserAgent)
	}
	if cur.IP != "198.51.100.23" {
		t.Errorf("ip = %q; want 198.51.100.23", cur.IP)
	}
}
