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
