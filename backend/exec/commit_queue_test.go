package exec

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCommitQueue_SequentialAcquireRelease(t *testing.T) {
	cq := NewCommitQueue()

	release1 := cq.Acquire("proj-1")
	release1()

	// Second acquire should succeed immediately after release
	release2 := cq.Acquire("proj-1")
	release2()
}

func TestCommitQueue_ConcurrentSameProjectBlocks(t *testing.T) {
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

	// Small delay to ensure the goroutine is blocked
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
	cq := NewCommitQueue()

	var order atomic.Int64
	var wg sync.WaitGroup

	// First acquire on proj-1
	release1 := cq.Acquire("proj-1")

	// Concurrent acquire on proj-2 — should NOT block
	wg.Go(func() {
		release2 := cq.Acquire("proj-2")
		order.Add(1)
		release2()
	})

	// proj-2 should proceed immediately
	time.Sleep(50 * time.Millisecond)
	if order.Load() != 1 {
		t.Fatal("proj-2 acquire should have proceeded in parallel")
	}

	release1()
	wg.Wait()
}

func TestCommitQueue_MultipleSequential(t *testing.T) {
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

// worktreeLockFor reaches the task's mutex directly, for tests that must observe lock identity or that a
// lock was released. Production has no such accessor on purpose (see LockWorktree).
func worktreeLockFor(te *TaskExecutor, taskID string) *sync.Mutex {
	mu, _ := te.worktreeMu.LoadOrStore(taskID, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

func TestWorktreeLockFor_SameTaskReturnsSameMutex(t *testing.T) {
	te := &TaskExecutor{}

	mu1 := worktreeLockFor(te, "task-1")
	mu2 := worktreeLockFor(te, "task-1")

	if mu1 != mu2 {
		t.Fatal("expected same mutex for same task ID")
	}
}

func TestWorktreeLockFor_DifferentTasksReturnDifferentMutexes(t *testing.T) {
	te := &TaskExecutor{}

	mu1 := worktreeLockFor(te, "task-1")
	mu2 := worktreeLockFor(te, "task-2")

	if mu1 == mu2 {
		t.Fatal("expected different mutexes for different task IDs")
	}
}

// TestLockWorktreeSerializesSameTask is the same property reached the way production reaches it.
func TestLockWorktreeSerializesSameTask(t *testing.T) {
	te := &TaskExecutor{}

	release := te.LockWorktree("task-1")
	if worktreeLockFor(te, "task-1").TryLock() {
		release()
		t.Fatal("LockWorktree returned without holding the task's mutex")
	}
	release()
	if !worktreeLockFor(te, "task-1").TryLock() {
		t.Fatal("the release function did not release the mutex")
	}
	worktreeLockFor(te, "task-1").Unlock()
}

func TestWorktreeLockFor_ConcurrentAccess(t *testing.T) {
	te := &TaskExecutor{}

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

	time.Sleep(50 * time.Millisecond)

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
	go func() {
		release := te.LockWorktree("task-2")
		mu.Lock()
		observed++
		mu.Unlock()
		release()
	}()

	// G3 should complete quickly
	select {
	case <-time.After(100 * time.Millisecond):
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
