package api

// A checkpoint's diff is an ordinary commit-to-commit diff against the snapshot commit.

import (
	"encoding/json/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/git"
	"mike/backend/store"
	"mike/backend/types"
)

// checkpointFixture is a task with a worktree of its own, whose captured state is one `git stash
// create` commit — the shape a turn leaves behind for the checkpoint panel to read back.
type checkpointFixture struct {
	h        *GitHandler
	s        *store.Store
	wm       *git.WorktreeManager
	mux      *http.ServeMux
	projID   string
	taskID   string
	projPath string
	wtPath   string
	base     string
}

func newCheckpointFixture(t *testing.T, suffix string) *checkpointFixture {
	t.Helper()
	h, s, wm, worktreesDir := newTestGitHandler(t)

	projID, taskID := "ckpt-"+suffix+"-proj", "ckpt-"+suffix+"-task"
	projPath := t.TempDir()
	initGitRepo(t, projPath)
	if err := s.CreateProject(&types.Project{ID: projID, Name: projID, Path: projPath}); err != nil {
		t.Fatal(err)
	}
	task := &types.Task{ID: taskID, ProjectID: projID, Title: "checkpoint " + suffix,
		Status: types.StatusInProgress, Branch: "main"}
	if err := s.CreateTask(task); err != nil {
		t.Fatal(err)
	}
	wtPath := filepath.Join(worktreesDir, projID, taskID)
	createWorktreeAt(t, projPath, wtPath, "main")
	base := strings.TrimSpace(mustOut(t, projPath, "git", "rev-parse", "HEAD"))
	task.BaseCommit = base
	if err := s.UpdateTask(task); err != nil {
		t.Fatal(err)
	}

	return &checkpointFixture{
		h: h, s: s, wm: wm, mux: newTestGitMux(h),
		projID: projID, taskID: taskID, projPath: projPath, wtPath: wtPath, base: base,
	}
}

// write puts a file in the task's worktree.
func (f *checkpointFixture) write(t *testing.T, name, content string) {
	t.Helper()
	createFile(t, f.wtPath, name, content)
}

// pin captures the worktree as a stash commit and pins it to the task the way a turn does, returning
// the ref the panel later names as ?head=.
func (f *checkpointFixture) pin(t *testing.T) string {
	t.Helper()
	mustRun(t, f.wtPath, "git", "add", "-A")
	snapshot := strings.TrimSpace(mustOut(t, f.wtPath, "git", "stash", "create"))
	if snapshot == "" {
		t.Fatal("stash create returned empty, so nothing was captured to diff")
	}
	if err := f.wm.PinTaskSnapshot(f.taskID, "asst-1", snapshot); err != nil {
		t.Fatalf("pin snapshot: %v", err)
	}
	return snapshot
}

// commitWork moves the worktree's work into a commit, so HEAD holds what the checkpoint never saw.
func (f *checkpointFixture) commitWork(t *testing.T, message string) {
	t.Helper()
	mustRun(t, f.wtPath, "git", "add", "-A")
	mustRun(t, f.wtPath, "git", "commit", "-m", message)
}

// diff asks the panel's route, which names only the task: the refs being compared are the ones the
// pinned snapshot and the task row carry.
func (f *checkpointFixture) diff(t *testing.T, query string) string {
	t.Helper()
	rr := doRequest(t, f.mux, http.MethodGet, "/api/projects/"+f.projID+"/tasks/"+f.taskID+"/diff"+query)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET diff%s: %d %s", query, rr.Code, rr.Body.String())
	}
	return rr.Body.String()
}

// A checkpoint diff answers the tree that was captured and nothing else: every file the snapshot
// holds — a binary included, which a text patch cannot carry — and none of the work that arrived
// after it, because HEAD is not one of the two trees it compares. The panel reads both forms, so the
// checkpoint path answers ?parsed=1 like every other diff path.
func TestTaskDiffAtCheckpointAnswersOnlyTheCapturedTree(t *testing.T) {
	f := newCheckpointFixture(t, "captured")
	f.write(t, "early.txt", "early\n")
	f.write(t, "count.bas", "count to 10\n")
	if err := os.WriteFile(filepath.Join(f.wtPath, "img.png"),
		[]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01}, 0o644); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	snapshot := f.pin(t)

	f.write(t, "late.txt", "late\n")
	f.commitWork(t, "later work")

	captured := []string{"early.txt", "count.bas", "img.png"}
	for _, query := range []string{"?head=" + snapshot, "?head=" + snapshot + "&parsed=1"} {
		body := f.diff(t, query)
		for _, name := range captured {
			if !strings.Contains(body, name) {
				t.Errorf("diff%s does not mention %s — the state the checkpoint captured is not what the UI shows\n%s", query, name, body)
			}
		}
		if strings.Contains(body, "late.txt") {
			t.Errorf("diff%s lists a file committed after the checkpoint\n%s", query, body)
		}
	}

	// The parsed form is the one the panel draws, so the captured files have to be in it by name.
	var parsed taskDiffBody
	if err := json.Unmarshal([]byte(f.diff(t, "?head="+snapshot+"&parsed=1")), &parsed); err != nil {
		t.Fatalf("decode the parsed checkpoint answer: %v", err)
	}
	names := map[string]bool{}
	for _, file := range parsed.ParsedFiles {
		names[file.Path] = true
	}
	for _, want := range []string{"early.txt", "count.bas"} {
		if !names[want] {
			t.Errorf("parsed_files leaves out %s, so the panel draws nothing for it: %+v", want, parsed.ParsedFiles)
		}
	}
}

// A checkpoint belongs to the thread that took it: its diff measures from the fork point, the baseline the
// Changes panel, the commit list and the card stats use.
func TestTaskDiffAtCheckpointUsesTheForkBaseline(t *testing.T) {
	f := newCheckpointFixture(t, "fork")

	// Work the parent thread committed, then a thread forked off that state.
	f.write(t, "parent.txt", "parent\n")
	f.commitWork(t, "parent thread work")
	// The thread forks at where the task branch stands now: main lives in the task worktree, so name the
	// branch rather than the project checkout's HEAD.
	mustRun(t, f.projPath, "git", "branch", "thread/forked", "main")
	if err := f.s.CreateThread(&types.TaskThread{ID: "ckpt-fork-thread", TaskID: f.taskID,
		Name: "forked", IsActive: true, GitBranch: "thread/forked"}); err != nil {
		t.Fatal(err)
	}
	task, err := f.s.GetTaskByID(f.taskID)
	if err != nil {
		t.Fatal(err)
	}
	task.CurrentThreadID = "ckpt-fork-thread"
	if err := f.s.UpdateTask(task); err != nil {
		t.Fatal(err)
	}

	f.write(t, "mine.txt", "mine\n")
	snapshot := f.pin(t)

	body := f.diff(t, "?head="+snapshot+"&parsed=1")
	if !strings.Contains(body, "mine.txt") {
		t.Errorf("the checkpoint's own work is missing\n%s", body)
	}
	if strings.Contains(body, "parent.txt") {
		t.Errorf("a forked thread's checkpoint lists the parent thread's pre-fork work\n%s", body)
	}
	// The answer names the side it measured from, so the panel prints both refs instead of leaving the reader
	// to guess which baseline a checkpoint was compared against.
	forkPoint := strings.TrimSpace(mustOut(t, f.projPath, "git", "rev-parse", "thread/forked"))
	if !strings.Contains(body, `"base_ref":"`+forkPoint+`"`) {
		t.Errorf("the checkpoint's diff did not name the fork point it measured from\n%s", body)
	}
}

// A truncated file inside the checkpoint view expands through GET /tasks/{id}/diff/file. Without the refs it
// names, that route answers the worktree — the panel would show work done after the checkpoint under the
// checkpoint's own heading, and its "show full diff" would write to the other panel.
func TestDiffFileAtRefAnswersTheSnapshotNotTheWorktree(t *testing.T) {
	f := newCheckpointFixture(t, "fileref")

	// The state the checkpoint captured.
	f.write(t, "shared.txt", "one\ntwo\n")
	snapshot := f.pin(t)

	// Work the checkpoint never saw, now sitting in the worktree.
	f.write(t, "shared.txt", "one\ntwo\nthree-later\n")

	f.mux.HandleFunc("GET /api/projects/{projectID}/tasks/{taskID}/diff/file", f.h.HandleTaskDiffFile)
	rr := doRequest(t, f.mux, http.MethodGet, "/api/projects/"+f.projID+"/tasks/"+f.taskID+
		"/diff/file?path=shared.txt&head="+snapshot+"&full=1")
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
	missing := doRequest(t, f.mux, http.MethodGet, "/api/projects/"+f.projID+"/tasks/"+f.taskID+
		"/diff/file?path=elsewhere.txt&head="+snapshot)
	if missing.Code != http.StatusNotFound {
		t.Errorf("a path outside the comparison is %d, want 404: %s", missing.Code, missing.Body.String())
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
