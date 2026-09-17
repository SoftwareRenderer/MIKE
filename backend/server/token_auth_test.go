package server

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// What a bearer token may and may not do. Every request here runs through the middleware Serve
// installs, with no session cookie — a token is the only credential the client has.

// bearerCall runs a request carrying `Authorization: Bearer <secret>` (an empty secret sends no
// header) through the real middleware chain.
func bearerCall(t *testing.T, s *Server, method, path, secret string, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := testRequest(method, path, body)
	// The chain is Host-pinned before anything else, and httptest's default Host is not one
	// this server accepts.
	req.Host = "localhost"
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	rr := httptest.NewRecorder()
	s.middlewareChain(s.mux).ServeHTTP(rr, req)
	return rr
}

// newProjectAndTask is a project with one backlog task, built through the API so the rows carry
// the same shape a real client would have put there.
func newProjectAndTask(t *testing.T, s *Server) (string, string) {
	t.Helper()
	projectID := createTestProject(t, s)
	return projectID, createTestTask(t, s, projectID, "a task a token may reach")
}

func TestBearerTokenReachesItsScopedRoutes(t *testing.T) {
	s := testServer(t)
	projectID, taskID := newProjectAndTask(t, s)
	secret := issueTokenThroughAPI(t, s, "ci")["token"].(string)

	t.Run("reads", func(t *testing.T) {
		for _, path := range []string{"/api/projects", "/api/projects/" + projectID, "/api/projects/" + projectID + "/tasks"} {
			if rr := bearerCall(t, s, http.MethodGet, path, secret, nil); rr.Code != http.StatusOK {
				t.Errorf("GET %s: %d %s", path, rr.Code, rr.Body.String())
			}
		}
		if rr := bearerCall(t, s, http.MethodGet, "/api/projects/"+projectID+"/tasks/"+taskID, secret, nil); rr.Code != http.StatusOK {
			t.Errorf("reading one task: %d %s", rr.Code, rr.Body.String())
		}
	})

	// No CSRF header is sent by any request in this test: the credential is the header the
	// caller chose to send, not a cookie the browser attaches on its own.
	t.Run("writes", func(t *testing.T) {
		created := bearerCall(t, s, http.MethodPost, "/api/projects/"+projectID+"/tasks", secret,
			map[string]any{"prompt": "created with a token"})
		if created.Code != http.StatusCreated {
			t.Fatalf("creating a task: %d %s", created.Code, created.Body.String())
		}
		newID := mustJSON[map[string]any](t, created.Body)["id"].(string)

		updated := bearerCall(t, s, http.MethodPut, "/api/projects/"+projectID+"/tasks/"+newID, secret,
			map[string]any{"title": "renamed with a token"})
		if updated.Code != http.StatusOK {
			t.Fatalf("updating a task: %d %s", updated.Code, updated.Body.String())
		}
		if got := mustJSON[map[string]any](t, updated.Body)["title"]; got != "renamed with a token" {
			t.Errorf("title after the update = %v", got)
		}

		stopped := bearerCall(t, s, http.MethodPost, "/api/projects/"+projectID+"/tasks/"+newID+"/stop", secret, nil)
		if stopped.Code != http.StatusOK {
			t.Fatalf("stopping a task: %d %s", stopped.Code, stopped.Body.String())
		}

		deleted := bearerCall(t, s, http.MethodDelete, "/api/projects/"+projectID+"/tasks/"+newID, secret, "")
		if deleted.Code != http.StatusNoContent {
			t.Fatalf("deleting a task: %d %s", deleted.Code, deleted.Body.String())
		}
	})

	// The executor routes start real work, so they are proven authorized rather than exercised:
	// 404 is the handler answering for a task that does not exist, which only happens past the
	// scope check.
	t.Run("the start routes are authorized", func(t *testing.T) {
		for _, suffix := range []string{"/start", "/start-subtask"} {
			rr := bearerCall(t, s, http.MethodPost, "/api/projects/"+projectID+"/tasks/no-such-task"+suffix, secret, nil)
			if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
				t.Errorf("POST …%s: %d %s — the scope check refused a route on the list", suffix, rr.Code, rr.Body.String())
			}
			if rr.Code != http.StatusNotFound {
				t.Errorf("POST …%s: %d %s, want the handler's own 404", suffix, rr.Code, rr.Body.String())
			}
		}
	})
}

func TestBearerTokenRefusedOutsideItsScope(t *testing.T) {
	s := testServer(t)
	projectID, taskID := newProjectAndTask(t, s)
	issued := issueTokenThroughAPI(t, s, "ci")
	tokenID, secret := issued["id"].(string), issued["token"].(string)

	denied := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"the configuration", http.MethodGet, "/api/config", nil},
		{"a config write", http.MethodPut, "/api/config", map[string]any{}},
		{"the session list", http.MethodGet, "/api/sessions", nil},
		{"signing other devices out", http.MethodPost, "/api/sessions/revoke-others", nil},
		{"the token list", http.MethodGet, "/api/tokens", nil},
		{"minting another token", http.MethodPost, "/api/tokens", map[string]any{"label": "escalation"}},
		{"revoking a token", http.MethodDelete, "/api/tokens/" + tokenID, nil},
		{"creating a project", http.MethodPost, "/api/projects", map[string]any{"name": "x", "path": "/tmp"}},
		{"deleting a project", http.MethodDelete, "/api/projects/" + projectID, nil},
		{"the board", http.MethodGet, "/api/projects/" + projectID + "/board", nil},
		{"clearing done tasks", http.MethodDelete, "/api/projects/" + projectID + "/tasks/done", nil},
		{"a task transcript", http.MethodGet, "/api/projects/" + projectID + "/tasks/" + taskID + "/messages", nil},
		{"sending the agent a message", http.MethodPost, "/api/projects/" + projectID + "/tasks/" + taskID + "/send", map[string]any{"content": "hi"}},
		{"the git surface", http.MethodGet, "/api/projects/" + projectID + "/git/status", nil},
		{"the file surface", http.MethodGet, "/api/fs", nil},
		{"the extension list", http.MethodGet, "/api/extensions", nil},
	}

	for _, tc := range denied {
		t.Run(tc.name, func(t *testing.T) {
			rr := bearerCall(t, s, tc.method, tc.path, secret, tc.body)
			if rr.Code != http.StatusForbidden {
				t.Errorf("%s %s: %d %s, want 403", tc.method, tc.path, rr.Code, rr.Body.String())
			}
		})
	}
}

// A token that is not live is answered 401, and a request that presents one is never served by the
// session cookie it happens to travel with: a revoked credential must not degrade into "still
// signed in as whoever's cookie this is".
func TestBearerTokenMustBeLive(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)

	t.Run("unknown token", func(t *testing.T) {
		rr := bearerCall(t, s, http.MethodGet, "/api/projects", "mike_never-issued", nil)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rr.Code)
		}
		if got := rr.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
			t.Errorf("the refusal does not name the scheme: %q", got)
		}
	})

	t.Run("empty bearer value", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/projects", nil)
		req.Host = "localhost"
		req.Header.Set("Authorization", "Bearer")
		rr := httptest.NewRecorder()
		s.middlewareChain(s.mux).ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rr.Code)
		}
	})

	t.Run("a revoked token stops working on the next request", func(t *testing.T) {
		revoked := issueTokenThroughAPI(t, s, "revoked soon")
		id, secret := revoked["id"].(string), revoked["token"].(string)
		if rr := bearerCall(t, s, http.MethodGet, "/api/projects", secret, nil); rr.Code != http.StatusOK {
			t.Fatalf("before revoking: %d %s", rr.Code, rr.Body.String())
		}
		if rr := testResponseAuth(t, s, testRequest(http.MethodDelete, "/api/tokens/"+id, nil)); rr.Code != http.StatusNoContent {
			t.Fatalf("revoke: %d %s", rr.Code, rr.Body.String())
		}
		rr := bearerCall(t, s, http.MethodGet, "/api/projects", secret, nil)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("after revoking: %d %s, want 401", rr.Code, rr.Body.String())
		}
	})

	t.Run("an invalid token does not fall back to the cookie", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/projects", nil)
		req.Host = "localhost"
		withAuthCSRF(req, cookie, csrf)
		req.Header.Set("Authorization", "Bearer mike_not-a-real-token")
		rr := httptest.NewRecorder()
		s.middlewareChain(s.mux).ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 rather than the cookie's answer: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("a non-bearer scheme is not a token attempt", func(t *testing.T) {
		req := testRequest(http.MethodGet, "/api/projects", nil)
		req.Host = "localhost"
		withAuthCSRF(req, cookie, csrf)
		req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
		rr := httptest.NewRecorder()
		s.middlewareChain(s.mux).ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want the cookie path to answer: %s", rr.Code, rr.Body.String())
		}
	})
}

// A token authenticates a request without making it look signed in. Handlers ask
// currentSessionID to decide who is asking; a token caller has no answer to that question, and
// reporting one would let a scoped credential be treated as a session identity.
func TestBearerTokenRequestCarriesNoSessionID(t *testing.T) {
	s := testServer(t)
	secret := issueTokenThroughAPI(t, s, "ci")["token"].(string)

	var seen, token string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = currentSessionID(r)
		token = apiTokenID(r)
	})
	req := testRequest(http.MethodGet, "/api/projects", nil)
	req.Host = "localhost"
	req.Header.Set("Authorization", "Bearer "+secret)
	s.APITokenMiddleware(s.CSRFMiddleware(s.SessionMiddleware(inner))).ServeHTTP(httptest.NewRecorder(), req)

	if seen != "" {
		t.Errorf("a token-authenticated request reports session %q, want none", seen)
	}
	if token == "" {
		t.Error("the token identity did not reach the handler")
	}
}

// The scheme token is case-insensitive (RFC 7235); the secret is not.
func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	s := testServer(t)
	secret := issueTokenThroughAPI(t, s, "ci")["token"].(string)

	req := testRequest(http.MethodGet, "/api/projects", nil)
	req.Host = "localhost"
	req.Header.Set("Authorization", "bearer "+secret)
	rr := httptest.NewRecorder()
	s.middlewareChain(s.mux).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
}

// A token on a path the mux has no route for is the mux's answer, not the scope check's: 404 says
// what is wrong, where 403 would claim the caller lacks a permission it was never asking about.
func TestBearerTokenOnAnUnregisteredPath(t *testing.T) {
	s := testServer(t)
	secret := issueTokenThroughAPI(t, s, "ci")["token"].(string)

	if rr := bearerCall(t, s, http.MethodGet, "/api/definitely-not-a-route", secret, nil); rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// The allowlist is the product's security boundary, so widening it is a deliberate edit to this
// list rather than a line added to a map somewhere.
func TestTokenScopeIsTheApprovedSet(t *testing.T) {
	approved := []string{
		"DELETE /api/projects/{projectID}/tasks/{taskID}",
		"GET /api/projects",
		"GET /api/projects/{projectID}",
		"GET /api/projects/{projectID}/tasks",
		"GET /api/projects/{projectID}/tasks/{taskID}",
		"POST /api/projects/{projectID}/tasks",
		"POST /api/projects/{projectID}/tasks/{taskID}/start",
		"POST /api/projects/{projectID}/tasks/{taskID}/start-subtask",
		"POST /api/projects/{projectID}/tasks/{taskID}/stop",
		"PUT /api/projects/{projectID}/tasks/{taskID}",
	}
	got := make([]string, 0, len(tokenScope))
	for pattern := range tokenScope {
		got = append(got, pattern)
	}
	sort.Strings(got)

	if strings.Join(got, "\n") != strings.Join(approved, "\n") {
		t.Errorf("tokenScope changed:\n got: %s\nwant: %s\n"+
			"docs/api.md and the Settings help text describe this set; a route added here is a route a leaked token can drive.",
			strings.Join(got, "\n"), strings.Join(approved, "\n"))
	}

	registered := map[string]bool{}
	for _, r := range registeredRoutes(t) {
		registered[r] = true
	}
	for pattern := range tokenScope {
		if !registered[pattern] {
			t.Errorf("%s is scoped for tokens but not registered — a renamed route keeps its grant and loses its meaning", pattern)
		}
	}
}
