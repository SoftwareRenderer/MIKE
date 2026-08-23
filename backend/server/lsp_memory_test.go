package server

import (
	"net/http"
	"net/url"
	"testing"

	"kanban/backend/types"
)

// TestHandleLSPMemory_Unauthenticated verifies the LSP sandbox memory
// endpoint requires a session (like the rest of /api).
func TestHandleLSPMemory_Unauthenticated(t *testing.T) {
	s := testServer(t)

	req := testRequest(http.MethodGet, "/api/lsp/memory?workspace=/tmp/ws&file=/tmp/ws/main.go", nil)
	w := testResponse(t, s, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// TestHandleLSPMemory_Authenticated verifies the endpoint responds with a
// valid SandboxMemoryStats payload for an allowed workspace. No LSP
// extension is installed in the test environment, so the sandbox reports
// not running.
func TestHandleLSPMemory_Authenticated(t *testing.T) {
	s := testServer(t)

	// The workspace validator restricts to project roots — create a project.
	dir := t.TempDir()
	initGitRepo(t, dir)
	cookie, csrfToken := loginTestCSRF(t, s)
	projReq := testRequest(http.MethodPost, "/api/projects", map[string]any{
		"name": "lsp-memory-test",
		"path": dir,
	})
	withAuthCSRF(projReq, cookie, csrfToken)
	projRR := testResponse(t, s, projReq)
	if projRR.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", projRR.Code, projRR.Body.String())
	}

	q := url.Values{"workspace": {dir}, "file": {dir + "/main.go"}}
	w := testResponseAuth(t, s, testRequest(http.MethodGet, "/api/lsp/memory?"+q.Encode(), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusOK, w.Body.String())
	}
	stats := mustJSON[types.SandboxMemoryStats](t, w.Body)
	if stats.Running {
		t.Errorf("Running = true, want false (no LSP sandbox in test env)")
	}
}

// TestHandleLSPMemory_WorkspaceNotAProject verifies the workspace validator
// rejects non-project paths on the authenticated route.
func TestHandleLSPMemory_WorkspaceNotAProject(t *testing.T) {
	s := testServer(t)

	q := url.Values{"workspace": {"/tmp/not-a-project"}, "file": {"/tmp/not-a-project/main.go"}}
	w := testResponseAuth(t, s, testRequest(http.MethodGet, "/api/lsp/memory?"+q.Encode(), nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d (body: %s)", w.Code, http.StatusForbidden, w.Body.String())
	}
}

// TestHandleLSPMemory_MissingParams verifies parameter validation on the
// authenticated route.
func TestHandleLSPMemory_MissingParams(t *testing.T) {
	s := testServer(t)

	cookie, csrfToken := loginTestCSRF(t, s)
	req := testRequest(http.MethodGet, "/api/lsp/memory", nil)
	withAuthCSRF(req, cookie, csrfToken)
	w := testResponse(t, s, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}
