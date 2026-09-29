package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The bearer-token management surface: GET/POST/DELETE /api/tokens, reachable only with the session
// cookie, and the credentials those routes hand out.

// issueTokenThroughAPI issues a token the way the Settings UI does and answers the create response:
// the row's fields plus the plaintext secret, which appears in that answer and nowhere else.
func issueTokenThroughAPI(t *testing.T, s *Server, label string) map[string]any {
	t.Helper()
	req := testRequest(http.MethodPost, "/api/tokens", map[string]any{"label": label})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create token: %d %s", rr.Code, rr.Body.String())
	}
	body := mustJSON[map[string]any](t, rr.Body)
	for _, field := range []string{"id", "token"} {
		if v, _ := body[field].(string); v == "" {
			t.Fatalf("create token answered %+v; want a non-empty %q", body, field)
		}
	}
	return body
}

func TestAPITokenCreateListRevoke(t *testing.T) {
	t.Parallel()
	s := testServer(t)

	created := issueTokenThroughAPI(t, s, "nightly importer")
	id, secret := created["id"].(string), created["token"].(string)
	if !strings.HasPrefix(secret, "mike_") {
		t.Errorf("issued token %q does not carry the token prefix", secret)
	}

	list := mustJSON[[]map[string]any](t, authGET(t, s, "/api/tokens").Body)
	if len(list) != 1 {
		t.Fatalf("listed %d tokens: %v", len(list), list)
	}
	row := list[0]
	if row["id"] != id || row["label"] != "nightly importer" {
		t.Errorf("listed %+v, want the issued row", row)
	}
	// The create answer and the listing are two views of one row, so they report one time.
	if row["created_at"] != created["created_at"] {
		t.Errorf("created_at is %v in the create response and %v in the listing", created["created_at"], row["created_at"])
	}
	if row["last_used_at"] != "" {
		t.Errorf("a never-used token reports last_used_at %v", row["last_used_at"])
	}

	req := testRequest(http.MethodDelete, "/api/tokens/"+id, nil)
	if rr := testResponseAuth(t, s, req); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rr.Code, rr.Body.String())
	}
	if again := mustJSON[[]map[string]any](t, authGET(t, s, "/api/tokens").Body); len(again) != 0 {
		t.Errorf("a revoked token is still listed: %v", again)
	}
}

// The plaintext appears once, in the create response. A listing that carried it — or its digest —
// would make Settings → About a credential dump for anyone who can open the tab.
func TestAPITokenListingCarriesNoCredential(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	secret := issueTokenThroughAPI(t, s, "runner")["token"].(string)

	rr := authRequest(t, s, http.MethodGet, "/api/tokens", nil)
	var rows []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("listed %d rows: %v", len(rows), rows)
	}
	for _, forbidden := range []string{"token", "token_hash", "secret", "hash"} {
		if _, ok := rows[0][forbidden]; ok {
			t.Errorf("the listing carries %q", forbidden)
		}
	}
	if strings.Contains(rr.Body.String(), secret) {
		t.Error("the listing contains the issued secret")
	}
}

func TestAPITokenManagementNeedsTheSession(t *testing.T) {
	t.Parallel()
	s := testServer(t)

	// No cookie at all.
	if rr := testResponse(t, s, testRequest(http.MethodGet, "/api/tokens", nil)); rr.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated list: %d, want 401", rr.Code)
	}

	// A cookie without the CSRF header: the writes are guarded like every other mutation.
	cookie := loginTest(t, s)
	for _, req := range []*http.Request{
		testRequest(http.MethodPost, "/api/tokens", map[string]any{"label": "runner"}),
		testRequest(http.MethodDelete, "/api/tokens/whatever", nil),
	} {
		withAuth(req, cookie)
		if rr := testResponse(t, s, req); rr.Code != http.StatusForbidden {
			t.Errorf("%s /api/tokens: %d, want 403 without a CSRF token", req.Method, rr.Code)
		}
	}
}

func TestAPITokenCreateValidatesTheLabel(t *testing.T) {
	t.Parallel()
	s := testServer(t)

	cases := []struct {
		name string
		body any
	}{
		{"no body", nil},
		{"absent label", map[string]any{}},
		{"empty label", map[string]any{"label": ""}},
		{"whitespace label", map[string]any{"label": "   "}},
		{"label too long", map[string]any{"label": strings.Repeat("x", 81)}},
		{"label is not a string", map[string]any{"label": 42}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := testRequest(http.MethodPost, "/api/tokens", tc.body)
			rr := testResponseAuth(t, s, req)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
			}
		})
	}

	// A refusal issues nothing: an empty body is not a token named "42".
	if rows := mustJSON[[]map[string]any](t, authGET(t, s, "/api/tokens").Body); len(rows) != 0 {
		t.Errorf("refused requests created tokens: %v", rows)
	}
}

func TestAPITokenRevokeReportsUnknownID(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	req := testRequest(http.MethodDelete, "/api/tokens/does-not-exist", nil)
	if rr := testResponseAuth(t, s, req); rr.Code != http.StatusNotFound {
		t.Fatalf("revoke of an unknown id: %d %s", rr.Code, rr.Body.String())
	}
}

// authGET is a session-authed GET, the shape every Settings read is.
func authGET(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	return authRequest(t, s, http.MethodGet, path, nil)
}

// authRequest runs a session-authed, CSRF-carrying request through the middleware chain.
func authRequest(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := testRequest(method, path, body)
	return testResponseAuth(t, s, req)
}
