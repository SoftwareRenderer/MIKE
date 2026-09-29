package exec

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCommitQueue_ConcurrentSameProjectBlocks(t *testing.T) {
	t.Parallel()
	cq := NewCommitQueue()

	var order atomic.Int64
	var wg sync.WaitGroup

	// First acquire — should succeed immediately
	release1 := cq.Acquire("proj-1")

	// Second acquire — should block
	wg.Go(func() {
		release2 := cq.Acquire("proj-1")
		order.Add(1)
		release2()
	})

	// A blocked goroutine has nothing to report: CommitQueue exposes no waiter, so the wait here is
	// the window in which the wrong thing (order reaching 1) would have shown up. Cut it short and
	// "still 0" stops meaning anything. A waiter callback, as the two gates have, is what would turn
	// this into a wait on a condition.
	time.Sleep(50 * time.Millisecond)

	// Verify the second goroutine hasn't proceeded yet
	if order.Load() != 0 {
		t.Fatal("second acquire should have been blocked")
	}

	// Release first — second should proceed
	release1()
	wg.Wait()

	if order.Load() != 1 {
		t.Fatal("second acquire should have proceeded after first release")
	}
}

func TestCommitQueue_ConcurrentDifferentProjectsParallel(t *testing.T) {
	t.Parallel()
	cq := NewCommitQueue()

	var order atomic.Int64
	var wg sync.WaitGroup

	release1 := cq.Acquire("proj-1")

	// Concurrent acquire on proj-2 — should NOT block
	wg.Go(func() {
		release2 := cq.Acquire("proj-2")
		order.Add(1)
		release2()
	})

	// proj-2 should proceed immediately
	awaitCounter(t, 5*time.Second, 1, "the other project's acquire to proceed", func() int { return int(order.Load()) })

	release1()
	wg.Wait()
}

func TestCommitQueue_MultipleSequential(t *testing.T) {
	t.Parallel()
	cq := NewCommitQueue()

	var order atomic.Int64
	var wg sync.WaitGroup

	// Launch 3 sequential acquires
	for range 3 {
		wg.Go(func() {
			release := cq.Acquire("proj-1")
			order.Add(1)
			release()
		})
	}

	// Wait for all to complete
	wg.Wait()

	if order.Load() != 3 {
		t.Fatalf("expected order 3, got %d", order.Load())
	}
}

// taskLockIsHeld asks the question the old test-only mutex accessor answered — is this task's lock held
// — without handing a lock to the test. The bounded form is used so the probe cannot itself block the
// suite if the behaviour it is checking has regressed.
func taskLockIsHeld(te *TaskExecutor, taskID string) bool {
	release, got := te.worktreeMu.TryLock(taskID, 10*time.Millisecond)
	if !got {
		return true
	}
	release()
	return false
}

// TestLockWorktreeIsPerTask is the table's reason for existing: one task's worktree work must not stop
// another's, and two calls for the same task must land on the same lock.
func TestLockWorktreeIsPerTask(t *testing.T) {
	t.Parallel()
	te := newBareTestExecutor(t)

	release := te.LockWorktree("task-1")
	if !taskLockIsHeld(te, "task-1") {
		release()
		t.Fatal("LockWorktree returned without holding the task's lock")
	}
	if taskLockIsHeld(te, "task-2") {
		release()
		t.Fatal("a second task's lock is the first task's — the table is keyed on something else")
	}
	release()
	if taskLockIsHeld(te, "task-1") {
		t.Fatal("the release function did not release")
	}
}

// TestLockWorktreeConcurrentWaitersQueue is the same property reached the way production reaches it,
// with a third task proving the table does not become a global lock under load.
func TestLockWorktreeConcurrentWaitersQueue(t *testing.T) {
	t.Parallel()
	te := newBareTestExecutor(t)

	var observed int
	var mu sync.Mutex
	done := make(chan struct{})

	// G1: holds task-1 mutex
	go func() {
		release := te.LockWorktree("task-1")
		mu.Lock()
		observed++
		mu.Unlock()
		<-done
		release()
	}()

	// G2 may only queue behind a held lock, or "G2 waited" is an artefact of G1 not having started.
	waitFor(t, 5*time.Second, "G1 to hold task-1's lock", func() bool { return taskLockIsHeld(te, "task-1") })

	// G2: blocks on task-1 mutex
	gotSecond := make(chan struct{})
	go func() {
		release := te.LockWorktree("task-1")
		mu.Lock()
		observed++
		mu.Unlock()
		release()
		close(gotSecond)
	}()

	// G3: different task, should not block
	gotThird := make(chan struct{})
	go func() {
		release := te.LockWorktree("task-2")
		mu.Lock()
		observed++
		mu.Unlock()
		release()
		close(gotThird)
	}()

	// G3 must reach its own lock while G2 is still waiting on task-1's.
	select {
	case <-gotThird:
	case <-time.After(1 * time.Second):
		t.Fatal("goroutine 3 should not have blocked")
	}

	// G2 should still be blocked
	select {
	case <-gotSecond:
		t.Fatal("goroutine 2 should still be blocked")
	default:
	}

	close(done)

	// G2 should now proceed
	select {
	case <-gotSecond:
	case <-time.After(1 * time.Second):
		t.Fatal("goroutine 2 should have unblocked")
	}

	mu.Lock()
	finalObserved := observed
	mu.Unlock()
	if finalObserved != 3 {
		t.Fatalf("expected 3 observations, got %d", finalObserved)
	}
}
