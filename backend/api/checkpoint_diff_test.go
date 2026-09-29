package api

// A checkpoint's diff is an ordinary commit-to-commit diff against the snapshot commit. It used to be
// taken against a stash entry, and when that resolved to the baseline the handler searched the stash list
// by label and then by position — which could return another thread's work.

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/types"
)

// mustOut runs a command and returns its stdout.
func mustOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return string(out)
}

func TestTaskDiffAtCheckpointShowsEveryCapturedFile(t *testing.T) {
	h, s, wm, worktreesDir := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "ckpt-diff-proj", Name: "ckpt-diff-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}
	task := &types.Task{ID: "ckpt-diff-task", ProjectID: proj.ID, Title: "checkpoint diff",
		Status: types.StatusInProgress, Branch: "main"}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	wtPath := filepath.Join(worktreesDir, proj.ID, task.ID)
	createWorktreeAt(t, projPath, wtPath, "main")
	base := strings.TrimSpace(mustOut(t, projPath, "git", "rev-parse", "HEAD"))
	task.BaseCommit = base
	if err := s.UpdateTask(task); err != nil {
		t.Fatal(err)
	}

	// A text edit, a new file and a binary, all untracked or unstaged. The binary matters because a
	// checkpoint used to be replayed as a text patch, which cannot carry one.
	mustRun(t, wtPath, "bash", "-c", "echo 'count to 10' > count.bas")
	if err := os.WriteFile(filepath.Join(wtPath, "img.png"), []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01}, 0o644); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	mustRun(t, wtPath, "git", "add", "-A")
	snapshot := strings.TrimSpace(mustOut(t, wtPath, "git", "stash", "create"))
	if snapshot == "" {
		t.Fatal("stash create returned empty")
	}
	if err := wm.PinTaskSnapshot(task.ID, "asst-1", snapshot); err != nil {
		t.Fatalf("pin snapshot: %v", err)
	}

	mux := newTestGitMux(h)
	rr := doRequest(t, mux, "GET", "/api/projects/"+proj.ID+"/tasks/"+task.ID+"/diff?head="+snapshot+"&checkpointIdx=0")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{"count.bas", "img.png"} {
		if !strings.Contains(body, want) {
			t.Errorf("checkpoint diff does not mention %s — the state it captured is not what the UI shows\n%s", want, body)
		}
	}
}
