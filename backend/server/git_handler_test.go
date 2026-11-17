package server

import (
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"mike/backend/testutil/testgit"
)

// createTestProjectWithPath creates a project and returns its ID and filesystem path.
func createTestProjectWithPath(t *testing.T, s *Server) (string, string) {
	t.Helper()
	dir := t.TempDir()
	initGitRepo(t, dir)
	cookie, csrfToken := loginTestCSRF(t, s)
	proj := map[string]any{
		"name": "test-project",
		"path": dir,
	}
	req := testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	result := mustJSON[map[string]any](t, rr.Body)
	return result["id"].(string), dir
}

// gitLogRepo is one repository shaped for the history panel: three commits on master, and a release
// branch pointing at the middle one so a ref can be seen decorating the commit it names.
func gitLogRepo(t *testing.T) (*Server, string, string) {
	t.Helper()
	s := testServer(t)
	projID, dir := createTestProjectWithPath(t, s)
	testgit.Git(t, dir, "commit", "--allow-empty", "-m", "Commit A")
	testgit.Git(t, dir, "commit", "--allow-empty", "-m", "Commit B")
	release := strings.TrimSpace(testgit.GitOutput(t, dir, "rev-parse", "HEAD~1"))
	testgit.Git(t, dir, "branch", "release", release)
	return s, projID, dir
}

// gitLog reads ?full=1 and hands back the wrapper alongside its commits.
func gitLog(t *testing.T, s *Server, projID, query string) (map[string]any, []map[string]any) {
	t.Helper()
	rr := testResponseAuth(t, s, testRequest("GET", "/api/projects/"+projID+"/git/log"+query, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("git/log%s: %d %s", query, rr.Code, rr.Body.String())
	}
	resp := mustJSON[map[string]any](t, rr.Body)
	raw, _ := resp["commits"].([]any)
	commits := make([]map[string]any, len(raw))
	for i, c := range raw {
		commits[i] = c.(map[string]any)
	}
	return resp, commits
}

// refsOn reads the refs the answer says point at a commit. The field is omitted when a commit carries
// none, so absence is an empty list here — the loop that calls it still insists every ref decorates
// exactly one commit, which is what a dropped field would trip.
func refsOn(commit map[string]any) []string {
	raw, ok := commit["branches"].([]any)
	if !ok {
		return nil
	}
	refs := make([]string, 0, len(raw))
	for _, b := range raw {
		refs = append(refs, b.(string))
	}
	return refs
}

// The full log is what the history panel draws: newest first, every field the row shows, the parents
// the graph is drawn from, the refs decorating each commit, and the branch the checkout is on.
// ?branch= asks for another branch's history than the one HEAD holds.
func TestGitLogAnswersTheGraph(t *testing.T) {
	t.Run("the checked-out branch", func(t *testing.T) {
		s, projID, _ := gitLogRepo(t)

		resp, commits := gitLog(t, s, projID, "?full=1")
		if len(commits) != 3 {
			t.Fatalf("expected 3 commits, got %d", len(commits))
		}
		if !strings.Contains(commits[0]["subject"].(string), "Commit B") {
			t.Fatalf("the log is not newest first: %v", commits[0]["subject"])
		}
		if branch, ok := resp["current_branch"].(string); !ok || branch != "master" {
			t.Fatalf("current_branch = %v, want master", resp["current_branch"])
		}
		// No remote is configured, so there is nothing for an origin tip to name.
		if tip, ok := resp["origin_tip"].(string); ok && tip != "" {
			t.Fatalf("expected an empty origin_tip, got %q", tip)
		}

		first := commits[0]
		for _, field := range []string{"hash", "short", "subject", "date"} {
			if v, ok := first[field].(string); !ok || v == "" {
				t.Fatalf("expected a non-empty %s in the full log, got %v", field, first[field])
			}
		}
		// Parents — the newest commit has one parent, and it is the commit below it. Read out of the
		// answer, not behind a nil check: an absent field satisfies every assertion that only runs
		// when the field is there.
		parents, ok := first["parents"].([]any)
		if !ok || len(parents) != 1 {
			t.Fatalf("expected 1 parent, got %v", first["parents"])
		}
		if parents[0] != commits[1]["hash"] {
			t.Fatalf("parent hash mismatch: %v != %v", parents[0], commits[1]["hash"])
		}
		// The root commit names no parent at all.
		if parents, ok := commits[len(commits)-1]["parents"].([]any); ok && len(parents) != 0 {
			t.Fatalf("expected 0 parents for the root commit, got %v", parents)
		}

		// Each ref decorates the commit it points at.
		for ref, wantSubject := range map[string]string{"release": "Commit A", "master": "Commit B"} {
			var seen []string
			for _, c := range commits {
				if slices.Contains(refsOn(c), ref) {
					seen = append(seen, c["subject"].(string))
				}
			}
			if len(seen) != 1 || !strings.Contains(seen[0], wantSubject) {
				t.Errorf("%s decorates %v, want the commit whose subject contains %q", ref, seen, wantSubject)
			}
		}
	})

	t.Run("a branch HEAD is not on", func(t *testing.T) {
		s, projID, dir := gitLogRepo(t)
		testgit.Git(t, dir, "checkout", "-b", "feature")
		testgit.Git(t, dir, "commit", "--allow-empty", "-m", "On feature")

		resp, commits := gitLog(t, s, projID, "?full=1&branch=feature")
		if len(commits) != 4 {
			t.Fatalf("expected 4 commits on the feature branch, got %d", len(commits))
		}
		if !strings.Contains(commits[0]["subject"].(string), "On feature") {
			t.Fatalf("?branch= answered with somebody else's history: %v", commits[0]["subject"])
		}
		// current_branch stays what HEAD holds, not what was asked for.
		if branch := resp["current_branch"].(string); branch != "feature" {
			t.Fatalf("expected current_branch 'feature', got %q", branch)
		}
	})
}

func TestGitLogNotFound(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)

	req := testRequest("GET", "/api/projects/nonexistent/git/log?full=1", nil)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestGitCheckout(t *testing.T) {
	s := testServer(t)
	projID, dir := createTestProjectWithPath(t, s)
	// Create a develop branch from master
	testgit.Git(t, dir, "checkout", "-b", "develop")

	cookie, csrfToken := loginTestCSRF(t, s)
	req := testRequest("POST", "/api/projects/"+projID+"/git/checkout", map[string]any{
		"branch": "master",
	})
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify checkout worked
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse: %s: %v", string(out), err)
	}
	branch := strings.TrimSpace(string(out))
	if branch != "master" {
		t.Fatalf("expected branch 'master', got '%s'", branch)
	}
}
