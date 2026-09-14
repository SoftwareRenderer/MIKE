package exec

// A watched accessor must queue on the same table as the plain one: a different table compiles, never
// blocks, and lets a commit run over a live tool call.

import (
	"testing"
	"time"

	"kanban/pkg/keylock"
)

func TestLockWorktreeWatchedQueuesOnTheSameTableAsLockWorktree(t *testing.T) {
	te := newBareTestExecutor(t)
	const taskID = "task-watched"

	holder := te.LockWorktree(taskID)
	got := make(chan struct{})
	var notices int
	go func() {
		release := te.LockWorktreeWatched(taskID, 5*time.Millisecond, keylock.WaitNotice(func(time.Duration, bool) {
			notices++
		}))
		close(got)
		release()
	}()

	select {
	case <-got:
		t.Fatal("the watched waiter got the task worktree lock while the plain holder held it")
	case <-time.After(120 * time.Millisecond):
	}

	holder()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the watched waiter never proceeded after the release")
	}

	// The wait was announced, not merely endured.
	if notices == 0 {
		t.Error("the waiter waited 120ms and announced nothing")
	}
}
