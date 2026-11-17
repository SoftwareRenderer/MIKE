package api

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An absent row and a failing lookup are different outcomes and only the first is a 404: a broken
// database must not reach the UI as an empty project. Both halves are asserted on both lookups a git
// log route makes — the project's and the task's — because a handler that folded one fault into
// "not found" would still answer the other correctly.

func errorBody(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not the JSON error envelope: %q (%v)", rr.Body.String(), err)
	}
	return body["error"]
}

func TestGitLogRoutesTellAnAbsentRowFromAFailingLookup(t *testing.T) {
	for _, tt := range []struct {
		name string
		// drop removes the table the lookup reads: a failure that is not the row being absent.
		drop string
		url  string
		// want is the status, and wantError the sentence the response must carry.
		want       int
		wantError  string
		mustNotSay string
	}{
		{
			name: "an unknown project is a 404",
			url:  "/api/projects/ghost/git/log",
			want: http.StatusNotFound, wantError: "project not found",
		},
		{
			name: "a failing project lookup is a 500, not a phantom 404",
			drop: "projects",
			url:  "/api/projects/anything/git/log",
			want: http.StatusInternalServerError, wantError: "internal error",
			mustNotSay: "no such table",
		},
		{
			name: "an unknown task is a 404",
			url:  "/api/projects/proj-1/tasks/ghost/git/log",
			want: http.StatusNotFound, wantError: "task not found",
		},
		{
			name: "a failing task lookup is a 500",
			drop: "tasks",
			url:  "/api/projects/proj-1/tasks/any-task/git/log",
			want: http.StatusInternalServerError, wantError: "internal error",
			mustNotSay: "no such table",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, s, _, _ := newTestGitHandler(t)
			if tt.drop != "" {
				if _, err := s.Exec("DROP TABLE " + tt.drop); err != nil {
					t.Fatalf("drop table: %v", err)
				}
			}

			rr := doRequest(t, newTestGitMux(h), http.MethodGet, tt.url)
			if rr.Code != tt.want {
				t.Fatalf("status = %d, want %d (body %q)", rr.Code, tt.want, rr.Body.String())
			}
			if got := errorBody(t, rr); got != tt.wantError {
				t.Errorf("error = %q, want %q", got, tt.wantError)
			}
			if tt.mustNotSay != "" && strings.Contains(rr.Body.String(), tt.mustNotSay) {
				t.Errorf("response leaked database internals: %s", rr.Body.String())
			}
		})
	}
}
