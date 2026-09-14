package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An absent row and a failing lookup are different outcomes and only the first is a 404:
// a broken database must not reach the UI as an empty project.

func errorBody(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not the JSON error envelope: %q (%v)", rr.Body.String(), err)
	}
	return body["error"]
}

func TestHandleLog_UnknownProjectIs404(t *testing.T) {
	h, _, _, _ := newTestGitHandler(t)

	rr := doRequest(t, newTestGitMux(h), http.MethodGet, "/api/projects/ghost/git/log")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}
	if got := errorBody(t, rr); got != "project not found" {
		t.Errorf("error = %q, want %q", got, "project not found")
	}
}

func TestHandleLog_ProjectLookupFaultIs500NotPhantom404(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)

	// Make the lookup fail for a reason that is not "absent": drop the table.
	if _, err := s.Exec(`DROP TABLE projects`); err != nil {
		t.Fatalf("drop table: %v", err)
	}

	rr := doRequest(t, newTestGitMux(h), http.MethodGet, "/api/projects/anything/git/log")

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("a database fault answered %d (body %q); want 500 — the bug this test exists for",
			rr.Code, rr.Body.String())
	}
	if got := errorBody(t, rr); got != "internal error" {
		t.Errorf("error = %q, want %q", got, "internal error")
	}
	if strings.Contains(rr.Body.String(), "no such table") {
		t.Errorf("response leaked database internals: %s", rr.Body.String())
	}
}

func TestHandleTaskLog_UnknownTaskIs404(t *testing.T) {
	h, _, _, _ := newTestGitHandler(t)

	rr := doRequest(t, newTestGitMux(h), http.MethodGet, "/api/projects/proj-1/tasks/ghost/git/log")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}
	if got := errorBody(t, rr); got != "task not found" {
		t.Errorf("error = %q, want %q", got, "task not found")
	}
}

func TestHandleTaskLog_TaskLookupFaultIs500(t *testing.T) {
	h, s, _, _ := newTestGitHandler(t)

	if _, err := s.Exec(`DROP TABLE tasks`); err != nil {
		t.Fatalf("drop table: %v", err)
	}

	rr := doRequest(t, newTestGitMux(h), http.MethodGet, "/api/projects/proj-1/tasks/any-task/git/log")

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body: %s)", rr.Code, rr.Body.String())
	}
}
