package exec

// The task's worktree lock and the project's stash lock are the pair a turn's checkpoint and a subtask's
// sync-merge both take. `syncTargetIntoTask()` takes the worktree lock first, so a checkpoint that asks for the
// stash lock first closes a cycle with it — both waits are indefinite, and nothing recovers short of a restart.
//
// The test holds the stash lock and watches the worktree table: holding the worktree lock while the stash lock
// is still out is the evidence the capture asked in the right order.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"mike/backend/git"
	"mike/backend/types"
)

func TestCheckpointTakesTheWorktreeLockBeforeWaitingForTheStashLock(t *testing.T) {
	t.Parallel()
	te, repoDir, projectID, _ := setupExecutorWithGit(t)
	s := te.store
	const (
		taskID  = "amber-lock-order"
		msgID   = "asst-lock-order-1"
		threadA = "th-main"
	)
	branch := "task/" + taskID
	if err := s.CreateTask(&types.Task{
		ID: taskID, ProjectID: projectID, Title: "order", Prompt: "p", Status: types.StatusInProgress,
		ExecutionState: types.ExecStateIdle, Branch: branch, CurrentThreadID: threadA,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.CreateThread(&types.TaskThread{ID: threadA, TaskID: taskID, Name: "Main", IsActive: true, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := s.CreateTaskMessage(&types.TaskMessage{
		ID: msgID, TaskID: taskID, ThreadID: threadA, Role: "assistant", Content: "done", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create message: %v", err)
	}
	wtPath, _, err := te.worktreeMgr.CreateWorktree(projectID, taskID, branch, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	// A worktree with nothing in it is a checkpoint that returns before it reaches any lock.
	if err := os.WriteFile(filepath.Join(wtPath, "turn.txt"), []byte("written this turn\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Somebody else holds this project's stash lock, which is where a sync-merge of another task would be.
	releaseStash := te.worktreeMgr.LockProjectStash(projectID)

	te.createCheckpoint(context.Background(), projectID, taskID, msgID)

	// The capture can only be standing in front of the stash lock, so the worktree lock is either in its hands
	// or it never asked, which is the order that has a cycle in it.
	deadline := time.Now().Add(5 * time.Second)
	for !te.worktreeMu.Held(taskID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !te.worktreeMu.Held(taskID) {
		releaseStash()
		t.Fatal("a checkpoint waited for the project's stash lock without holding the task's worktree lock; " +
			"syncTargetIntoTask() takes that pair in the opposite order and two tasks of one project wedge")
	}
	releaseStash()

	// It was waiting, not wedged: the snapshot it came for arrives.
	ref := git.SnapshotRef(taskID, msgID)
	waitForSnapshot(t, "the snapshot ref "+ref, func() bool {
		return exec.Command("git", "-C", repoDir, "show-ref", "--verify", "--quiet", ref).Run() == nil
	})
}
