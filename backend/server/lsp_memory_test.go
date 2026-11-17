package server

import (
	"net/http"
	"net/url"
	"testing"

	"mike/backend/types"
)

// What only the server can prove about this route: that it is registered, that it is not one of the
// public paths, and that the workspace jail is actually wired into it — a handler whose validator was
// never installed answers 200 for any directory on the host, because the bind-mount source of the
// language server's container comes out of this query string.
func TestLSPMemoryRouteIsGatedAndJailed(t *testing.T) {
	s := testServer(t)

	dir := t.TempDir()
	initGitRepo(t, dir)
	cookie, csrfToken := loginTestCSRF(t, s)
	projReq := testRequest(http.MethodPost, "/api/projects", map[string]any{
		"name": "lsp-memory-test",
		"path": dir,
	})
	withAuthCSRF(projReq, cookie, csrfToken)
	if rr := testResponse(t, s, projReq); rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}

	withQuery := func(q url.Values) string { return "/api/lsp/memory?" + q.Encode() }
	projectWS := url.Values{"workspace": {dir}, "file": {dir + "/main.go"}}

	cases := []struct {
		name string
		path string
		auth bool
		want int
	}{
		{"no session", withQuery(projectWS), false, http.StatusUnauthorized},
		{"a project's own checkout", withQuery(projectWS), true, http.StatusOK},
		{"a directory no project holds", withQuery(url.Values{
			"workspace": {"/tmp/not-a-project"}, "file": {"/tmp/not-a-project/main.go"},
		}), true, http.StatusForbidden},
		{"no workspace at all", "/api/lsp/memory", true, http.StatusBadRequest},
		{"a workspace with no file", withQuery(url.Values{"workspace": {dir}}), true, http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := testRequest(http.MethodGet, tc.path, nil)
			if tc.auth {
				withAuthCSRF(req, cookie, csrfToken)
			}
			rr := testResponse(t, s, req)
			if rr.Code != tc.want {
				t.Fatalf("GET %s: %d %s, want %d", tc.path, rr.Code, rr.Body.String(), tc.want)
			}
			// The accepted answer is the payload the panel reads, not a bare 200.
			if tc.want == http.StatusOK {
				mustJSON[types.SandboxMemoryStats](t, rr.Body)
			}
		})
	}
}
