package api

// A checkpoint's diff is an ordinary commit-to-commit diff against the snapshot commit.

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/types"
)

// A checkpoint taken before a commit must not show what that commit brought in: the panel compares two
// trees, and HEAD is not one of them.
func TestTaskDiffAtCheckpointExcludesWorkCommittedLater(t *testing.T) {
	h, s, wm, worktreesDir := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "ckpt-later-proj", Name: "ckpt-later-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}
	task := &types.Task{ID: "ckpt-later-task", ProjectID: proj.ID, Title: "checkpoint order",
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

	mustRun(t, wtPath, "bash", "-c", "echo early > early.txt")
	mustRun(t, wtPath, "git", "add", "-A")
	snapshot := strings.TrimSpace(mustOut(t, wtPath, "git", "stash", "create"))
	if snapshot == "" {
		t.Fatal("stash create returned empty")
	}
	if err := wm.PinTaskSnapshot(task.ID, "asst-1", snapshot); err != nil {
		t.Fatalf("pin snapshot: %v", err)
	}

	// Work the checkpoint never saw, then committed: HEAD now holds it.
	mustRun(t, wtPath, "bash", "-c", "echo late > late.txt")
	mustRun(t, wtPath, "git", "add", "-A")
	mustRun(t, wtPath, "git", "commit", "-m", "later work")

	mux := newTestGitMux(h)
	rr := doRequest(t, mux, "GET", "/api/projects/"+proj.ID+"/tasks/"+task.ID+"/diff?head="+snapshot+"&parsed=1")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "early.txt") {
		t.Errorf("checkpoint diff lost the file it captured\n%s", body)
	}
	if strings.Contains(body, "late.txt") {
		t.Errorf("checkpoint diff lists a file committed after it\n%s", body)
	}
}

// A checkpoint belongs to the thread that took it: its diff measures from the fork point, the baseline the
// Changes panel, the commit list and the card stats use.
func TestTaskDiffAtCheckpointUsesTheForkBaseline(t *testing.T) {
	h, s, wm, worktreesDir := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "ckpt-fork-proj", Name: "ckpt-fork-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}
	task := &types.Task{ID: "ckpt-fork-task", ProjectID: proj.ID, Title: "fork checkpoint",
		Status: types.StatusInProgress, Branch: "main"}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	wtPath := filepath.Join(worktreesDir, proj.ID, task.ID)
	createWorktreeAt(t, projPath, wtPath, "main")
	task.BaseCommit = strings.TrimSpace(mustOut(t, projPath, "git", "rev-parse", "HEAD"))

	// Work the parent thread committed, then a thread forked off that state.
	mustRun(t, wtPath, "bash", "-c", "echo parent > parent.txt")
	mustRun(t, wtPath, "git", "add", "-A")
	mustRun(t, wtPath, "git", "commit", "-m", "parent thread work")
	// The thread forks at where the task branch stands now: main lives in the task worktree, so name the
	// branch rather than the project checkout's HEAD.
	mustRun(t, projPath, "git", "branch", "thread/forked", "main")
	if err := s.CreateThread(&types.TaskThread{ID: "ckpt-fork-thread", TaskID: task.ID,
		Name: "forked", IsActive: true, GitBranch: "thread/forked"}); err != nil {
		t.Fatal(err)
	}
	task.CurrentThreadID = "ckpt-fork-thread"
	if err := s.UpdateTask(task); err != nil {
		t.Fatal(err)
	}

	mustRun(t, wtPath, "bash", "-c", "echo mine > mine.txt")
	mustRun(t, wtPath, "git", "add", "-A")
	snapshot := strings.TrimSpace(mustOut(t, wtPath, "git", "stash", "create"))
	if snapshot == "" {
		t.Fatal("stash create returned empty")
	}
	if err := wm.PinTaskSnapshot(task.ID, "asst-1", snapshot); err != nil {
		t.Fatalf("pin snapshot: %v", err)
	}

	mux := newTestGitMux(h)
	rr := doRequest(t, mux, "GET", "/api/projects/"+proj.ID+"/tasks/"+task.ID+"/diff?head="+snapshot+"&parsed=1")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "mine.txt") {
		t.Errorf("the checkpoint's own work is missing\n%s", body)
	}
	if strings.Contains(body, "parent.txt") {
		t.Errorf("a forked thread's checkpoint lists the parent thread's pre-fork work\n%s", body)
	}
	// The answer names the side it measured from, so the panel prints both refs instead of leaving the reader
	// to guess which baseline a checkpoint was compared against.
	forkPoint := strings.TrimSpace(mustOut(t, projPath, "git", "rev-parse", "thread/forked"))
	if !strings.Contains(body, `"base_ref":"`+forkPoint+`"`) {
		t.Errorf("the checkpoint's diff did not name the fork point it measured from\n%s", body)
	}
}

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

	// A text edit, a new file and a binary, all untracked or unstaged: the snapshot must carry a
	// binary, which a text patch cannot.
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
	rr := doRequest(t, mux, "GET", "/api/projects/"+proj.ID+"/tasks/"+task.ID+"/diff?head="+snapshot)
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

// A truncated file inside the checkpoint view expands through GET /tasks/{id}/diff/file. Without the refs it
// names, that route answers the worktree — the panel would show work done after the checkpoint under the
// checkpoint's own heading, and its "show full diff" would write to the other panel.
func TestDiffFileAtRefAnswersTheSnapshotNotTheWorktree(t *testing.T) {
	h, s, wm, worktreesDir := newTestGitHandler(t)

	projPath := t.TempDir()
	initGitRepo(t, projPath)
	proj := &types.Project{ID: "fileref-proj", Name: "fileref-proj", Path: projPath}
	if err := s.CreateProject(proj); err != nil {
		t.Fatal(err)
	}
	task := &types.Task{ID: "fileref-task", ProjectID: proj.ID, Title: "file at ref",
		Status: types.StatusInProgress, Branch: "main"}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	wtPath := filepath.Join(worktreesDir, proj.ID, task.ID)
	createWorktreeAt(t, projPath, wtPath, "main")
	task.BaseCommit = strings.TrimSpace(mustOut(t, projPath, "git", "rev-parse", "HEAD"))
	if err := s.UpdateTask(task); err != nil {
		t.Fatal(err)
	}

	// The state the checkpoint captured.
	mustRun(t, wtPath, "bash", "-c", "printf 'one\\ntwo\\n' > shared.txt")
	mustRun(t, wtPath, "git", "add", "-A")
	snapshot := strings.TrimSpace(mustOut(t, wtPath, "git", "stash", "create"))
	if snapshot == "" {
		t.Fatal("stash create returned empty")
	}
	if err := wm.PinTaskSnapshot(task.ID, "asst-1", snapshot); err != nil {
		t.Fatalf("pin snapshot: %v", err)
	}

	// Work the checkpoint never saw, now sitting in the worktree.
	mustRun(t, wtPath, "bash", "-c", "printf 'one\\ntwo\\nthree-later\\n' > shared.txt")

	mux := newTestGitMux(h)
	mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/diff/file", h.HandleTaskDiffFile)
	rr := doRequest(t, mux, "GET", "/api/projects/"+proj.ID+"/tasks/"+task.ID+"/diff/file?path=shared.txt&head="+snapshot+"&full=1")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if strings.Contains(body, "three-later") {
		t.Errorf("the ref read returned the worktree, not the snapshot\n%s", body)
	}
	if !strings.Contains(body, "\"two\"") {
		t.Errorf("the ref read lost the line the snapshot holds\n%s", body)
	}

	// A path the comparison does not name is still a 404, not an empty body about a file nobody changed.
	missing := doRequest(t, mux, "GET", "/api/projects/"+proj.ID+"/tasks/"+task.ID+"/diff/file?path=elsewhere.txt&head="+snapshot)
	if missing.Code != http.StatusNotFound {
		t.Errorf("a path outside the comparison is %d, want 404: %s", missing.Code, missing.Body.String())
	}
}
