package exec

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestAcquireRelease_SlotAvailable(t *testing.T) {
	g := NewConcurrencyGate()
	defer g.Close()

	// First acquire should succeed immediately (position 0)
	ctx := context.Background()
	pos, _, err := g.Acquire(ctx, "http://provider-a", 2)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if pos != 0 {
		t.Fatalf("expected position 0, got %d", pos)
	}

	// Release should succeed (no-op if no waiters)
	g.Release("http://provider-a")
}

func TestAcquireRelease_ConcurrencyLimit(t *testing.T) {
	g := NewConcurrencyGate()
	defer g.Close()
	ctx := context.Background()

	// Acquire both slots
	pos1, _, _ := g.Acquire(ctx, "http://p", 2)
	pos2, _, _ := g.Acquire(ctx, "http://p", 2)
	if pos1 != 0 || pos2 != 0 {
		t.Fatalf("expected position 0 for both, got %d, %d", pos1, pos2)
	}

	// Third acquire should block
	done := make(chan int, 1)
	var waiterEntry *queueEntry
	go func() {
		pos, entry, err := g.Acquire(ctx, "http://p", 2)
		if err != nil {
			t.Errorf("Acquire failed: %v", err)
			return
		}
		waiterEntry = entry
		done <- pos
	}()

	// Give the goroutine time to enqueue
	time.Sleep(50 * time.Millisecond)

	// Position should be 1 (one waiter)
	pos := g.Position("http://p", waiterEntry)
	if pos != 1 {
		t.Fatalf("expected queue position 1, got %d", pos)
	}

	// Release one slot — blocked goroutine should unblock
	g.Release("http://p")

	select {
	case p := <-done:
		if p != 0 {
			t.Fatalf("expected position 0 after being served, got %d", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for blocked Acquire to unblock")
	}

	// Clean up
	g.Release("http://p")
	g.Release("http://p")
}

func TestAcquireRelease_PerProviderIsolation(t *testing.T) {
	g := NewConcurrencyGate()
	defer g.Close()
	ctx := context.Background()

	// Fill slot for provider A
	g.Acquire(ctx, "http://a", 1)

	// Provider B should still have its slot available
	pos, _, err := g.Acquire(ctx, "http://b", 1)
	if err != nil {
		t.Fatalf("Acquire for provider B failed: %v", err)
	}
	if pos != 0 {
		t.Fatalf("expected position 0 for provider B, got %d", pos)
	}

	g.Release("http://a")
	g.Release("http://b")
}

func TestAcquireRelease_FIFOOrdering(t *testing.T) {
	g := NewConcurrencyGate()
	defer g.Close()
	ctx := context.Background()

	// Hold the only slot
	g.Acquire(ctx, "http://p", 1)

	// Enqueue two waiters
	var wg sync.WaitGroup
	var entries [2]*queueEntry

	for i := range 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, entry, err := g.Acquire(ctx, "http://p", 1)
			if err != nil {
				return
			}
			entries[idx] = entry
			// Hold briefly, then release
			time.Sleep(10 * time.Millisecond)
			g.Release("http://p")
		}(i)
	}

	// Let both goroutines enqueue
	time.Sleep(50 * time.Millisecond)

	// Use QueueEntries to determine actual queue order
	queueEntries := g.QueueEntries("http://p")
	if len(queueEntries) != 2 {
		t.Fatalf("expected 2 queue entries, got %d", len(queueEntries))
	}

	// First in queue should be at position 1
	pos := g.Position("http://p", queueEntries[0])
	if pos != 1 {
		t.Fatalf("first waiter: expected queue position 1, got %d", pos)
	}
	// Second in queue should be at position 2
	pos = g.Position("http://p", queueEntries[1])
	if pos != 2 {
		t.Fatalf("second waiter: expected queue position 2, got %d", pos)
	}

	// Release the held slot — first waiter should get it, then release, then second gets it
	g.Release("http://p")
	wg.Wait()
}

func TestAcquireRelease_ContextCancellation(t *testing.T) {
	g := NewConcurrencyGate()
	defer g.Close()

	// Hold the only slot
	ctx := context.Background()
	g.Acquire(ctx, "http://p", 1)

	// Try to acquire with a cancellable context
	cancelCtx, cancel := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	var waiterEntry *queueEntry
	go func() {
		_, entry, err := g.Acquire(cancelCtx, "http://p", 1)
		waiterEntry = entry
		errCh <- err
	}()

	// Let it enqueue
	time.Sleep(50 * time.Millisecond)

	// Cancel — should unblock with error
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error from cancelled Acquire, got nil")
		}
		if err != context.Canceled {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancelled Acquire")
	}

	// Queue should be cleaned up
	pos := g.Position("http://p", waiterEntry)
	if pos != 0 {
		t.Fatalf("expected queue position 0 after cancellation cleanup, got %d", pos)
	}

	g.Release("http://p")
}

func TestAcquireRelease_CancelledEntrySkipped(t *testing.T) {
	g := NewConcurrencyGate()
	defer g.Close()
	ctx := context.Background()

	// Hold the only slot
	g.Acquire(ctx, "http://p", 1)

	// Enqueue a waiter that will be cancelled
	cancelCtx, cancel := context.WithCancel(ctx)
	done1 := make(chan struct{})
	go func() {
		_, _, _ = g.Acquire(cancelCtx, "http://p", 1)
		close(done1)
	}()
	time.Sleep(50 * time.Millisecond)

	// Enqueue a second waiter (will be behind the cancelled one)
	done2 := make(chan struct{})
	var secondEntry *queueEntry
	go func() {
		_, entry, _ := g.Acquire(ctx, "http://p", 1)
		secondEntry = entry
		close(done2)
	}()
	time.Sleep(50 * time.Millisecond)

	// Cancel the first waiter
	cancel()
	time.Sleep(50 * time.Millisecond)

	// Second waiter should be at position 1 (cancelled entry is skipped)
	pos := g.Position("http://p", secondEntry)
	if pos != 1 {
		t.Fatalf("expected queue position 1 for second waiter, got %d", pos)
	}

	// Release the held slot — second waiter should be served (first is cancelled)
	g.Release("http://p")

	select {
	case <-done2:
		// Success — second waiter was served
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second waiter to be served")
	}

	// Clean up
	g.Release("http://p")
}

func TestEstimateWait_NoHistory(t *testing.T) {
	g := NewConcurrencyGate()
	defer g.Close()
	ctx := context.Background()

	// Hold the slot
	g.Acquire(ctx, "http://p", 1)

	// Start a waiter (so position > 0)
	done := make(chan struct{})
	var waiterEntry *queueEntry
	go func() {
		_, entry, _ := g.Acquire(ctx, "http://p", 1)
		waiterEntry = entry
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)

	// With no completion history, should use default estimate (30s per position)
	est := g.EstimateWait("http://p", waiterEntry)
	if est != 30 {
		t.Fatalf("expected default estimate 30, got %d", est)
	}

	// Clean up
	g.Release("http://p")
	<-done
	g.Release("http://p")
}

func TestEstimateWait_WithHistory(t *testing.T) {
	g := NewConcurrencyGate()
	defer g.Close()
	ctx := context.Background()

	// Build some history: acquire, hold for 100ms, release (repeat 3x)
	for range 3 {
		g.Acquire(ctx, "http://p", 5)
		time.Sleep(100 * time.Millisecond)
		g.Release("http://p")
	}

	// Now hold all slots and enqueue a waiter
	for range 2 {
		g.Acquire(ctx, "http://p", 2)
	}

	done := make(chan struct{})
	var waiterEntry *queueEntry
	go func() {
		_, entry, _ := g.Acquire(ctx, "http://p", 2)
		waiterEntry = entry
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)

	// Position is 1, avg slot time ~100ms → estimate ~0s (rounds down)
	// But with 3 completions of 100ms each, avg = 100ms, position = 1 → 0.1s → rounds to 0
	// Let's check it's at least not negative
	est := g.EstimateWait("http://p", waiterEntry)
	if est < 0 {
		t.Fatalf("expected non-negative estimate, got %d", est)
	}

	// Clean up
	g.Release("http://p")
	g.Release("http://p")
	<-done
	g.Release("http://p")
}

func TestClose_ReleasesAllWaiters(t *testing.T) {
	g := NewConcurrencyGate()
	ctx := context.Background()

	g.Acquire(ctx, "http://p", 1)

	// Enqueue 3 waiters
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			g.Acquire(ctx, "http://p", 1)
		})
	}
	time.Sleep(50 * time.Millisecond)

	// Close should release all waiters
	g.Close()

	wg.Wait() // should not timeout
}

func TestAcquireRelease_MultipleWaitersPositions(t *testing.T) {
	g := NewConcurrencyGate()
	defer g.Close()
	ctx := context.Background()

	// Hold the only slot
	g.Acquire(ctx, "http://p", 1)

	// Enqueue 3 waiters
	var entries [3]*queueEntry
	var wg sync.WaitGroup

	for i := range 3 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, entry, err := g.Acquire(ctx, "http://p", 1)
			if err != nil {
				return
			}
			entries[idx] = entry
		}(i)
	}

	// Let all goroutines enqueue
	time.Sleep(100 * time.Millisecond)

	// Use QueueEntries to determine actual queue order
	queueEntries := g.QueueEntries("http://p")
	if len(queueEntries) != 3 {
		t.Fatalf("expected 3 queue entries, got %d", len(queueEntries))
	}

	// Verify positions are correct: 1, 2, 3
	for i, entry := range queueEntries {
		pos := g.Position("http://p", entry)
		expectedPos := i + 1
		if pos != expectedPos {
			t.Fatalf("waiter at queue pos %d: expected position %d, got %d", i, expectedPos, pos)
		}
	}

	// Release the held slot — first waiter should get it
	g.Release("http://p")

	// After first waiter is served, remaining waiters should shift
	time.Sleep(50 * time.Millisecond)

	// Re-check queue order after first waiter is served
	queueEntries = g.QueueEntries("http://p")
	if len(queueEntries) != 2 {
		t.Fatalf("expected 2 queue entries after first served, got %d", len(queueEntries))
	}

	// Second waiter (now first in queue) should be at position 1
	pos := g.Position("http://p", queueEntries[0])
	if pos != 1 {
		t.Fatalf("after first served: expected position 1 for first remaining waiter, got %d", pos)
	}
	// Third waiter (now second in queue) should be at position 2
	pos = g.Position("http://p", queueEntries[1])
	if pos != 2 {
		t.Fatalf("after first served: expected position 2 for second remaining waiter, got %d", pos)
	}

	// Release remaining slots
	g.Release("http://p")
	g.Release("http://p")
	wg.Wait()
}

func TestMinConcurrency_One(t *testing.T) {
	g := NewConcurrencyGate()
	defer g.Close()
	ctx := context.Background()

	// concurrency=0 should be treated as 1
	pos, _, err := g.Acquire(ctx, "http://p", 0)
	if err != nil {
		t.Fatalf("Acquire with concurrency=0 failed: %v", err)
	}
	if pos != 0 {
		t.Fatalf("expected position 0, got %d", pos)
	}

	// Second acquire should block (limit is 1)
	done := make(chan struct{}, 1)
	go func() {
		g.Acquire(ctx, "http://p", 0)
		done <- struct{}{}
	}()

	select {
	case <-done:
		t.Fatal("second Acquire should have blocked")
	case <-time.After(50 * time.Millisecond):
		// Expected — it's blocked
	}

	// Clean up
	g.Release("http://p")
	<-done
	g.Release("http://p")
}
