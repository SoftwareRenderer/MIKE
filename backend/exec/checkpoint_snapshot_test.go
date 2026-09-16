package exec

// A checkpoint has to outlive the process and the garbage collector, so it is pinned under
// refs/mike/snap/. A hash the database holds for an object git has pruned is a restore that fails later.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mike/backend/git"
	"mike/backend/types"
)

// gitOut runs git for assertions, failing the test on error.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

// waitForSnapshot polls until f reports success.
func waitForSnapshot(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestCheckpointPinsItsSnapshot(t *testing.T) {
	te, repoDir, projectID, _ := setupExecutorWithGit(t)
	s := te.store
	const (
		taskID  = "amber-checkpoint"
		msgID   = "asst-snapshot-1"
		threadA = "th-main"
	)
	branch := "task/" + taskID
	if err := s.CreateTask(&types.Task{
		ID: taskID, ProjectID: projectID, Title: "snap", Prompt: "p", Status: types.StatusInProgress,
		ExecutionState: types.ExecStateIdle, Branch: branch, CurrentThreadID: threadA,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	wtPath, _, err := te.worktreeMgr.CreateWorktree(projectID, taskID, branch, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if err := s.CreateThread(&types.TaskThread{ID: threadA, TaskID: taskID, Name: "Main", IsActive: true, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	// The assistant message the checkpoint belongs to — createCheckpoint records the snapshot on it.
	if err := s.CreateTaskMessage(&types.TaskMessage{
		ID: msgID, TaskID: taskID, ThreadID: threadA, Role: "assistant", Content: "done", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create message: %v", err)
	}

	// The turn's changes: one tracked edit, one new text file.
	if err := os.WriteFile(filepath.Join(wtPath, "README.md"), []byte("# Changed\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "new.txt"), []byte("created this turn\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	te.createCheckpoint(context.Background(), projectID, taskID, msgID)

	ref := git.SnapshotRef(taskID, msgID)
	waitForSnapshot(t, "the snapshot ref "+ref, func() bool {
		return exec.Command("git", "-C", repoDir, "show-ref", "--verify", "--quiet", ref).Run() == nil
	})
	pinned := gitOut(t, repoDir, "rev-parse", ref)

	// The database holds the hash, the ref is what keeps it alive; they must name the same object.
	waitForSnapshot(t, "the checkpoint hash on the assistant message", func() bool {
		msg, err := s.GetTaskMessageByID(msgID)
		return err == nil && msg.GitCommitHash != ""
	})
	msg, err := s.GetTaskMessageByID(msgID)
	if err != nil {
		t.Fatalf("get message: %v", err)
	}
	if msg.GitCommitHash != pinned {
		t.Errorf("message holds %s but the ref pins %s", msg.GitCommitHash, pinned)
	}

	// A checkpoint must never appear in the user's stash list — that list is theirs.
	if out := gitOut(t, repoDir, "stash", "list"); out != "" {
		t.Errorf("a checkpoint must not create a stash entry, got: %q", out)
	}

	// Restoring the snapshot must land the worktree back on the turn's files.
	if out := gitOut(t, repoDir, "show", pinned+":new.txt"); out != "created this turn" {
		t.Errorf("snapshot does not hold the new file: %q", out)
	}
	if out := gitOut(t, repoDir, "show", pinned+":README.md"); out != "# Changed" {
		t.Errorf("snapshot does not hold the tracked edit: %q", out)
	}

	// GC cannot take a pinned snapshot. (The unpinned equivalent is shown to be collectable in
	// backend/git/snapshots_test.go.)
	gitOut(t, repoDir, "reflog", "expire", "--expire=now", "--all")
	gitOut(t, repoDir, "gc", "--prune=now", "--quiet")
	if exec.Command("git", "-C", repoDir, "cat-file", "-e", pinned+"^{commit}").Run() != nil {
		t.Error("a pinned snapshot was garbage-collected")
	}
}
