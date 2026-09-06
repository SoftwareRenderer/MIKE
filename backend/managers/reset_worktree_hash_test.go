package managers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/types"
)

// TestResetWorktreeRequiresObjectID covers the last client-supplied ref that reached
// git unvalidated. `commit_hash` becomes an argument to
//
//	git rev-parse --verify <hash>^{tree}
//	git read-tree --reset -u <hash>
//
// so an option-shaped value is not a strange commit, it is a flag. Every hash the
// product writes is a `git stash create` output, so requiring an object id costs no
// legitimate request — which the last row asserts: an absent-but-well-formed id is a
// repository answer (not 400), while a forged one never reaches git at all.
func TestResetWorktreeRequiresObjectID(t *testing.T) {
	h, s, _, worktreesDir := newTestGitHandler(t)
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "reset-hash-proj", Name: "reset-hash-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}
	task := &types.Task{ID: "reset-hash-task", ProjectID: proj.ID,
		Title: "reset task", Status: types.StatusInProgress, Branch: "main"}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(worktreesDir, proj.ID, task.ID)
	createWorktreeAt(t, projPath, wt, "main")

	createFile(t, wt, "a.txt", "one\n")
	mustRun(t, wt, "git", "add", "a.txt")
	mustRun(t, wt, "git", "commit", "-m", "one")
	head := strings.TrimSpace(mustOut(t, wt, "git", "rev-parse", "HEAD"))

	mux := http.NewServeMux()
	mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/reset-worktree", h.HandleResetWorktree)

	reset := func(hash string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"commit_hash": hash})
		req := httptest.NewRequest(http.MethodPost,
			"/api/projects/reset-hash-proj/tasks/reset-hash-task/reset-worktree", strings.NewReader(string(body)))
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr
	}

	canary := "/tmp/mike_reset_hash_canary"
	_ = os.Remove(canary)
	t.Cleanup(func() { _ = os.Remove(canary) })

	for _, forged := range []string{
		"--output=" + canary,  // git show's file-write option
		"-b",                  // an option, whatever the subcommand
		"HEAD",                // a rev, not an id: the shape is the point
		"main~1",              // revision syntax
		"0000000000000000x",   // one character off a hash
		"0000 0000 0000 0000", // spaces would split into more arguments
	} {
		rr := reset(forged)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("commit_hash %q: got %d, want 400: %s", forged, rr.Code, rr.Body.String())
		}
		if _, err := os.Stat(canary); err == nil {
			t.Fatalf("commit_hash %q reached git as an option: %s exists", forged, canary)
		}
	}

	// A well-formed id that simply isn't in this repository is a snapshot answer, not
	// a shape refusal — otherwise this would be a blocklist that also breaks restore.
	if rr := reset("0000000000000000000000000000000000000000"); rr.Code == http.StatusBadRequest {
		t.Errorf("a valid object id was refused as malformed: %s", rr.Body.String())
	}

	// And the real one still restores.
	if rr := reset(head); rr.Code != http.StatusOK {
		t.Errorf("the task's own commit: got %d, want 200: %s", rr.Code, rr.Body.String())
	}
}
