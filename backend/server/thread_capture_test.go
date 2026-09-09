package server

// A switch and a fork both rewind the shared worktree, so each first moves the current thread's work into
// a snapshot. When git will not let that happen, the reply has to be "nothing happened" — carrying on
// would throw away the only copy of work that was never saved.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/git"
	"kanban/backend/types"
)

// blockGitWrites makes git refuse to write in this worktree, the way a stale index.lock or a concurrent
// git process does. Reading still works, so the failure lands in the capture rather than before it.
func blockGitWrites(t *testing.T, worktreePath string) func() {
	t.Helper()
	gitDir := strings.TrimSpace(mustRunGitOutput(t, worktreePath, "rev-parse", "--absolute-git-dir"))
	lock := filepath.Join(gitDir, "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatalf("block git: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(lock) })
	return func() { _ = os.Remove(lock) }
}

func activeThreadID(t *testing.T, s *Server, projID, taskID string) string {
	t.Helper()
	threads := mustJSON[[]map[string]any](t, testResponseAuth(t, s,
		testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)).Body)
	for _, th := range threads {
		if active, _ := th["is_active"].(bool); active {
			return th["id"].(string)
		}
	}
	return ""
}

func threadCount(t *testing.T, s *Server, projID, taskID string) int {
	t.Helper()
	return len(mustJSON[[]map[string]any](t, testResponseAuth(t, s,
		testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)).Body))
}

func stashCount(t *testing.T, worktreePath string) int {
	t.Helper()
	out := strings.TrimSpace(mustRunGitOutput(t, worktreePath, "stash", "list"))
	if out == "" {
		return 0
	}
	return len(strings.Split(out, "\n"))
}

func writeWorktreeFile(t *testing.T, worktreePath, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(worktreePath, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// switchThreadOK switches to threadID, failing the test if the server refused.
func switchThreadOK(t *testing.T, s *Server, fx forkFixture, threadID string) {
	t.Helper()
	rr := testResponseAuth(t, s, testRequest("POST",
		"/api/projects/"+fx.projID+"/tasks/"+fx.taskID+"/threads/switch",
		map[string]any{"thread_id": threadID}))
	if rr.Code != http.StatusOK {
		t.Fatalf("switch to %s: %d %s", threadID, rr.Code, rr.Body.String())
	}
}

func readWorktreeFile(t *testing.T, worktreePath, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(worktreePath, name))
	if err != nil {
		return "<missing: " + err.Error() + ">"
	}
	return string(b)
}

// The work stays where it is and the user is told the switch did not happen — then a retry works.
func TestThreadSwitchRefusesToDiscardWorkItCouldNotSave(t *testing.T) {
	s := testServer(t)
	fx := setupForkWithCheckpoint(t, s, "capture-switch")
	if got := activeThreadID(t, s, fx.projID, fx.taskID); got != fx.forkID {
		t.Fatalf("fixture should leave the fork active, got %q", got)
	}

	const dirty = "work that was never saved\n"
	writeWorktreeFile(t, fx.worktreePath, "file.txt", dirty)
	writeWorktreeFile(t, fx.worktreePath, "new-work.txt", "a new file\n")
	stashesBefore := stashCount(t, fx.worktreePath)

	unblock := blockGitWrites(t, fx.worktreePath)
	rr := testResponseAuth(t, s, testRequest("POST",
		"/api/projects/"+fx.projID+"/tasks/"+fx.taskID+"/threads/switch",
		map[string]any{"thread_id": fx.mainThreadID}))
	unblock()

	if rr.Code != http.StatusConflict {
		t.Fatalf("switch with git unable to write: %d %s, want 409", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); !strings.Contains(body, "could not save") || !strings.Contains(body, "nothing was switched") {
		t.Errorf("the reply does not say nothing happened: %s", body)
	}
	if got := activeThreadID(t, s, fx.projID, fx.taskID); got != fx.forkID {
		t.Errorf("the switch happened anyway: active thread is %q, want %q", got, fx.forkID)
	}
	if got := readWorktreeFile(t, fx.worktreePath, "file.txt"); got != dirty {
		t.Errorf("the tracked edit was discarded: %q", got)
	}
	if got := readWorktreeFile(t, fx.worktreePath, "new-work.txt"); got != "a new file\n" {
		t.Errorf("the new file was discarded: %q", got)
	}
	if got := stashCount(t, fx.worktreePath); got != stashesBefore {
		t.Errorf("a refused capture left %d stash entries, was %d", got, stashesBefore)
	}
	if refExists(t, fx.worktreePath, git.SnapshotRef(fx.taskID, git.ThreadSnapshotOwner(fx.forkID))) {
		t.Errorf("a refused capture pinned a snapshot anyway")
	}

	// Once git can write again, the same click works and the work is still recoverable.
	switchThreadOK(t, s, fx, fx.mainThreadID)
	if got := readWorktreeFile(t, fx.worktreePath, "file.txt"); got == dirty {
		t.Errorf("switching away did not move the work out of the worktree")
	}
	switchThreadOK(t, s, fx, fx.forkID)
	if got := readWorktreeFile(t, fx.worktreePath, "file.txt"); got != dirty {
		t.Errorf("the saved work did not come back: %q", got)
	}
	if got := readWorktreeFile(t, fx.worktreePath, "new-work.txt"); got != "a new file\n" {
		t.Errorf("the saved new file did not come back: %q", got)
	}
}

func TestThreadForkRefusesToDiscardWorkItCouldNotSave(t *testing.T) {
	s := testServer(t)
	fx := setupForkWithCheckpoint(t, s, "capture-fork")
	msgID := postMessageIn(t, s, fx, "and then count higher")

	const dirty = "work in flight when the fork was asked for\n"
	writeWorktreeFile(t, fx.worktreePath, "file.txt", dirty)
	threadsBefore := threadCount(t, s, fx.projID, fx.taskID)
	stashesBefore := stashCount(t, fx.worktreePath)

	unblock := blockGitWrites(t, fx.worktreePath)
	rr := testResponseAuth(t, s, testRequest("POST",
		"/api/projects/"+fx.projID+"/tasks/"+fx.taskID+"/threads/fork",
		map[string]any{"from_thread_id": fx.forkID, "from_message_id": msgID}))
	unblock()

	if rr.Code != http.StatusConflict {
		t.Fatalf("fork with git unable to write: %d %s, want 409", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); !strings.Contains(body, "could not save") || !strings.Contains(body, "nothing was forked") {
		t.Errorf("the reply does not say nothing happened: %s", body)
	}
	if got := threadCount(t, s, fx.projID, fx.taskID); got != threadsBefore {
		t.Errorf("the fork was created anyway: %d threads, was %d", got, threadsBefore)
	}
	if got := activeThreadID(t, s, fx.projID, fx.taskID); got != fx.forkID {
		t.Errorf("active thread changed: %q, want %q", got, fx.forkID)
	}
	if got := readWorktreeFile(t, fx.worktreePath, "file.txt"); got != dirty {
		t.Errorf("the work in flight was discarded: %q", got)
	}
	if got := stashCount(t, fx.worktreePath); got != stashesBefore {
		t.Errorf("a refused capture left %d stash entries, was %d", got, stashesBefore)
	}
}

// Stopping a running task is a real change, so a refusal has to report it and record it: a card still
// showing "running" after the switch was refused is a lie about the board.
func TestRefusedSwitchReportsTheTaskItStopped(t *testing.T) {
	s := testServer(t)
	fx := setupForkWithCheckpoint(t, s, "capture-running")
	task, err := s.store.GetTask(fx.projID, fx.taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	// UpdateTask will not start a run by design, so this goes through the claim the executor uses.
	task.ExecutionState = types.ExecStateRunning
	if err := s.store.UpdateTaskClaimingExecution(task); err != nil {
		t.Fatalf("mark task running: %v", err)
	}
	writeWorktreeFile(t, fx.worktreePath, "file.txt", "work mid-turn\n")

	unblock := blockGitWrites(t, fx.worktreePath)
	rr := testResponseAuth(t, s, testRequest("POST",
		"/api/projects/"+fx.projID+"/tasks/"+fx.taskID+"/threads/switch",
		map[string]any{"thread_id": fx.mainThreadID}))
	unblock()

	if rr.Code != http.StatusConflict {
		t.Fatalf("switch with git unable to write: %d %s, want 409", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); !strings.Contains(body, "stopped") {
		t.Errorf("the reply hides that the task was stopped: %s", body)
	}
	after, err := s.store.GetTask(fx.projID, fx.taskID)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if after.ExecutionState != types.ExecStateIdle {
		t.Errorf("the task is recorded as %q after a refusal; the board would show it running", after.ExecutionState)
	}
	if got := activeThreadID(t, s, fx.projID, fx.taskID); got != fx.forkID {
		t.Errorf("the switch happened anyway: active thread is %q, want %q", got, fx.forkID)
	}
}
