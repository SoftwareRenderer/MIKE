package managers

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/git"
	"kanban/backend/types"
)

type taskDiffBody struct {
	Diff        string                `json:"diff"`
	ParsedFiles []git.TrackedDiffFile `json:"parsed_files"`
}

// A done task's diff is served from the stored snapshot byte for byte, on the read that computes it
// and every read after it, so the stored body is what a ?parsed=1 caller gets.
func TestTaskDiffDoneTaskServesParsedFilesFromTheSnapshot(t *testing.T) {
	h, s, _, worktreesDir := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "done-parsed-proj", Name: "done-parsed-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}
	task := &types.Task{ID: "done-parsed-task", ProjectID: proj.ID, Title: "done parsed diff",
		Status: types.StatusInProgress, Branch: "main"}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	wtPath := filepath.Join(worktreesDir, proj.ID, task.ID)
	createWorktreeAt(t, projPath, wtPath, "main")
	base := strings.TrimSpace(mustOut(t, projPath, "git", "rev-parse", "HEAD"))
	mustRun(t, wtPath, "bash", "-c", "printf 'one\\ntwo\\n' > done.txt")
	mustRun(t, wtPath, "git", "add", "done.txt")
	mustRun(t, wtPath, "git", "commit", "-m", "Add done.txt")
	tip := strings.TrimSpace(mustOut(t, wtPath, "git", "rev-parse", "HEAD"))
	task.Status = types.StatusDone
	task.BaseCommit = base
	task.BranchTip = tip
	if err := s.UpdateTask(task); err != nil {
		t.Fatal(err)
	}

	mux := newTestGitMux(h)
	url := "/api/projects/" + proj.ID + "/tasks/" + task.ID + "/diff?head=" + tip + "&parsed=1"
	for i, label := range []string{"first read (computes and persists the snapshot)", "cached read (serves the stored body)"} {
		rr := doRequest(t, mux, "GET", url)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d: %s", label, rr.Code, rr.Body.String())
		}
		var body taskDiffBody
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: unmarshal: %v\n%s", label, err, rr.Body.String())
		}
		if !strings.Contains(body.Diff, "done.txt") {
			t.Fatalf("%s: patch text lost the commit\n%s", label, body.Diff)
		}
		if len(body.ParsedFiles) != 1 || body.ParsedFiles[0].Path != "done.txt" {
			t.Fatalf("%s: the done-task diff carries no parsed files, so the panel renders it empty\n%s",
				label, rr.Body.String())
		}
		if i == 0 {
			snap, err := s.GetTaskDiffSnapshot(task.ID)
			if err != nil || snap == nil {
				t.Fatalf("expected a persisted snapshot: %v", err)
			}
			if !strings.Contains(snap.DiffJSON, `"parsed_files"`) {
				t.Fatalf("the stored snapshot has no parsed view, and every later read serves it byte for byte\n%s", snap.DiffJSON)
			}
		}
	}
}
