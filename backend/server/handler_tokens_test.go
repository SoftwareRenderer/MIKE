package server

import (
	"encoding/json/v2"
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

// One token's life through the Settings surface, and the two shapes its two views must keep: the
// plaintext appears in the create answer and nowhere else — a listing that carried it, or its digest,
// would make Settings → About a credential dump for anyone who can open the tab — and a revoked row
// leaves the listing rather than going stale in it.
func TestAPITokenCreateListRevoke(t *testing.T) {
	s := testServer(t)

	created := issueTokenThroughAPI(t, s, "nightly importer")
	id, secret := created["id"].(string), created["token"].(string)
	if !strings.HasPrefix(secret, "mike_") {
		t.Errorf("issued token %q does not carry the token prefix", secret)
	}

	listing := authGET(t, s, "/api/tokens")
	body := listing.Body.String()
	var list []map[string]any
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
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
	for _, forbidden := range []string{"token", "token_hash", "secret", "hash"} {
		if _, ok := row[forbidden]; ok {
			t.Errorf("the listing carries %q", forbidden)
		}
	}
	if strings.Contains(body, secret) {
		t.Error("the listing contains the issued secret")
	}

	if rr := testResponseAuth(t, s, testRequest(http.MethodDelete, "/api/tokens/"+id, nil)); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rr.Code, rr.Body.String())
	}
	if again := mustJSON[[]map[string]any](t, authGET(t, s, "/api/tokens").Body); len(again) != 0 {
		t.Errorf("a revoked token is still listed: %v", again)
	}

	// A second revoke names nothing it deleted: the id is gone, which is the same answer the row
	// would give a caller guessing at it.
	if rr := testResponseAuth(t, s, testRequest(http.MethodDelete, "/api/tokens/"+id, nil)); rr.Code != http.StatusNotFound {
		t.Errorf("revoke of a gone id: %d %s, want 404", rr.Code, rr.Body.String())
	}
}

func TestAPITokenManagementNeedsTheSession(t *testing.T) {
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
