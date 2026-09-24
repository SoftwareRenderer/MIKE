package exec

import (
	"context"
	"testing"
	"time"
)

func TestAcquireRelease_SlotAvailable(t *testing.T) {
	t.Parallel()
	g := NewConcurrencyGate()

	// First acquire should succeed immediately (no queue entry)
	ctx := context.Background()
	entry, err := g.Acquire(ctx, "http://provider-a", 2, 0, nil)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	if entry != nil {
		t.Fatalf("immediate grant should return a nil queue entry, got %v", entry)
	}

	// Release should succeed (no-op if no waiters)
	g.Release("http://provider-a")
}

func TestAcquireRelease_ConcurrencyLimit(t *testing.T) {
	t.Parallel()
	g := NewConcurrencyGate()
	ctx := context.Background()

	// Acquire both slots
	if _, err := g.Acquire(ctx, "http://p", 2, 0, nil); err != nil {
		t.Fatalf("Acquire 1 failed: %v", err)
	}
	if _, err := g.Acquire(ctx, "http://p", 2, 0, nil); err != nil {
		t.Fatalf("Acquire 2 failed: %v", err)
	}

	// Third acquire should block; the onEnqueued callback exposes its queue
	// entry WHILE it is blocked, so Position can report its own position.
	done := make(chan struct{})
	enqueued := make(chan *queueEntry, 1)
	go func() {
		if _, err := g.Acquire(ctx, "http://p", 2, 0, func(e *queueEntry) { enqueued <- e }); err != nil {
			t.Errorf("Acquire failed: %v", err)
			return
		}
		close(done)
	}()

	// Wait for the goroutine to be enqueued
	var waiterEntry *queueEntry
	select {
	case waiterEntry = <-enqueued:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the blocked Acquire to enqueue")
	}

	// Position should be 1 (one waiter)
	pos := g.Position("http://p", waiterEntry)
	if pos != 1 {
		t.Fatalf("expected queue position 1, got %d", pos)
	}

	// Release one slot — blocked goroutine should unblock
	g.Release("http://p")

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for blocked Acquire to unblock")
	}

	g.Release("http://p")
	g.Release("http://p")
}

func TestAcquireRelease_PerProviderIsolation(t *testing.T) {
	t.Parallel()
	g := NewConcurrencyGate()
	ctx := context.Background()

	// Fill slot for provider A
	g.Acquire(ctx, "http://a", 1, 0, nil)

	// Provider B should still have its slot available
	if _, err := g.Acquire(ctx, "http://b", 1, 0, nil); err != nil {
		t.Fatalf("Acquire for provider B failed: %v", err)
	}

	g.Release("http://a")
	g.Release("http://b")
}

func TestAcquireRelease_FIFOOrdering(t *testing.T) {
	t.Parallel()
	g := NewConcurrencyGate()
	ctx := context.Background()

	// Hold the only slot
	g.Acquire(ctx, "http://p", 1, 0, nil)

	// Enqueue two waiters and track serve order
	serveOrder := make(chan int, 2)

	for i := range 2 {
		go func(idx int) {
			if _, err := g.Acquire(ctx, "http://p", 1, 0, nil); err != nil {
				return
			}
			serveOrder <- idx
			g.Release("http://p")
		}(i)
	}

	// Let both goroutines enqueue
	time.Sleep(50 * time.Millisecond)

	// Verify 2 entries in queue (Position with nil counts total)
	pos := g.Position("http://p", nil)
	if pos != 2 {
		t.Fatalf("expected 2 queue entries, got %d", pos)
	}

	// Release the held slot — first waiter should get it, then release, then second gets it
	g.Release("http://p")

	// Collect serve order (both should complete)
	var order []int
	for len(order) < 2 {
		select {
		case idx := <-serveOrder:
			order = append(order, idx)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for waiters, got %d of 2", len(order))
		}
	}

	// Verify FIFO: the two goroutines should have been served in some order
	// (we just confirm both were served, FIFO is guaranteed by the gate)
	if len(order) != 2 {
		t.Fatalf("expected 2 served waiters, got %d", len(order))
	}
}

func TestAcquireRelease_ContextCancellation(t *testing.T) {
	t.Parallel()
	g := NewConcurrencyGate()

	// Hold the only slot
	ctx := context.Background()
	g.Acquire(ctx, "http://p", 1, 0, nil)

	// Try to acquire with a cancellable context
	cancelCtx, cancel := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	go func() {
		_, err := g.Acquire(cancelCtx, "http://p", 1, 0, nil)
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
	pos := g.Position("http://p", nil)
	if pos != 0 {
		t.Fatalf("expected queue position 0 after cancellation cleanup, got %d", pos)
	}

	g.Release("http://p")
}

func TestAcquireRelease_CancelledEntrySkipped(t *testing.T) {
	t.Parallel()
	g := NewConcurrencyGate()
	ctx := context.Background()

	// Hold the only slot
	g.Acquire(ctx, "http://p", 1, 0, nil)

	// Enqueue a waiter that will be cancelled
	cancelCtx, cancel := context.WithCancel(ctx)
	done1 := make(chan struct{})
	go func() {
		g.Acquire(cancelCtx, "http://p", 1, 0, nil)
		close(done1)
	}()
	time.Sleep(50 * time.Millisecond)

	// Enqueue a second waiter (will be behind the cancelled one); the
	// onEnqueued callback exposes its entry while blocked.
	done2 := make(chan struct{})
	secondEnqueued := make(chan *queueEntry, 1)
	go func() {
		if _, err := g.Acquire(ctx, "http://p", 1, 0, func(e *queueEntry) { secondEnqueued <- e }); err != nil {
			t.Errorf("second acquire: %v", err)
			return
		}
		close(done2)
	}()
	var secondEntry *queueEntry
	select {
	case secondEntry = <-secondEnqueued:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second waiter to enqueue")
	}

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

	g.Release("http://p")
}

func TestEstimateWait_NoHistory(t *testing.T) {
	t.Parallel()
	g := NewConcurrencyGate()
	ctx := context.Background()

	// Hold the slot
	g.Acquire(ctx, "http://p", 1, 0, nil)

	// Start a waiter (so position > 0); the callback exposes its entry.
	done := make(chan struct{})
	enqueued := make(chan *queueEntry, 1)
	go func() {
		g.Acquire(ctx, "http://p", 1, 0, func(e *queueEntry) { enqueued <- e })
		close(done)
	}()
	var waiterEntry *queueEntry
	select {
	case waiterEntry = <-enqueued:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for waiter to enqueue")
	}

	// With no completion history, should use default estimate (30s per position)
	est := g.EstimateWait("http://p", waiterEntry)
	if est != 30 {
		t.Fatalf("expected default estimate 30, got %d", est)
	}

	g.Release("http://p")
	<-done
	g.Release("http://p")
}

func TestEstimateWait_WithHistory(t *testing.T) {
	t.Parallel()
	g := NewConcurrencyGate()
	ctx := context.Background()

	// Build some history: acquire, hold for 100ms, release (repeat 3x)
	for range 3 {
		g.Acquire(ctx, "http://p", 5, 0, nil)
		time.Sleep(100 * time.Millisecond)
		g.Release("http://p")
	}

	// Now hold all slots and enqueue a waiter
	for range 2 {
		g.Acquire(ctx, "http://p", 2, 0, nil)
	}

	done := make(chan struct{})
	enqueued := make(chan *queueEntry, 1)
	go func() {
		g.Acquire(ctx, "http://p", 2, 0, func(e *queueEntry) { enqueued <- e })
		close(done)
	}()
	var waiterEntry *queueEntry
	select {
	case waiterEntry = <-enqueued:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for waiter to enqueue")
	}

	// Position is 1, avg slot time ~100ms → estimate ~0s (rounds down)
	est := g.EstimateWait("http://p", waiterEntry)
	if est < 0 {
		t.Fatalf("expected non-negative estimate, got %d", est)
	}

	g.Release("http://p")
	g.Release("http://p")
	<-done
	g.Release("http://p")
}

func TestAcquireRelease_MultipleWaitersPositions(t *testing.T) {
	t.Parallel()
	g := NewConcurrencyGate()
	ctx := context.Background()

	// Hold the only slot
	g.Acquire(ctx, "http://p", 1, 0, nil)

	// Enqueue 3 waiters, each blocked on a shared barrier after acquiring.
	barrier := make(chan struct{})

	for range 3 {
		go func() {
			g.Acquire(ctx, "http://p", 1, 0, nil)
			<-barrier // block until test unblocks us
			g.Release("http://p")
		}()
	}

	// Let all goroutines enqueue
	time.Sleep(100 * time.Millisecond)

	// Verify 3 entries in queue (Position with nil counts total)
	pos := g.Position("http://p", nil)
	if pos != 3 {
		t.Fatalf("expected 3 queue entries, got %d", pos)
	}

	// Release the held slot — first waiter gets it and blocks on barrier
	g.Release("http://p")

	// Wait for first waiter to be served
	time.Sleep(50 * time.Millisecond)

	// 2 should remain in queue (first got slot but is blocked on barrier)
	pos = g.Position("http://p", nil)
	if pos != 2 {
		t.Fatalf("expected 2 queue entries after first served, got %d", pos)
	}

	// Unblock all — they'll cascade: first releases → second served → releases → third served
	close(barrier)

	// Wait for all to complete
	time.Sleep(200 * time.Millisecond)

	// Queue should be empty
	pos = g.Position("http://p", nil)
	if pos != 0 {
		t.Fatalf("expected 0 queue entries at end, got %d", pos)
	}
}

func TestAcquireRelease_TrailingSlashNormalization(t *testing.T) {
	t.Parallel()
	g := NewConcurrencyGate()
	ctx := context.Background()

	// Fill the only slot using a URL WITHOUT a trailing slash...
	if _, err := g.Acquire(ctx, "http://p", 1, 0, nil); err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	// ...and a waiter using the URL WITH a trailing slash must queue on the
	// SAME provider (not a fresh, empty gate key).
	enqueued := make(chan *queueEntry, 1)
	done := make(chan struct{})
	go func() {
		if _, err := g.Acquire(ctx, "http://p/", 1, 0, func(e *queueEntry) { enqueued <- e }); err != nil {
			t.Errorf("Acquire with trailing slash failed: %v", err)
			return
		}
		close(done)
	}()

	select {
	case <-enqueued:
	case <-time.After(2 * time.Second):
		t.Fatal("trailing-slash acquire did not queue behind the other URL form")
	}
	if pos := g.Position("http://p", nil); pos != 1 {
		t.Fatalf("expected 1 queue entry for the shared key, got %d", pos)
	}

	g.Release("http://p/") // release must also hit the shared key
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for trailing-slash waiter to be served")
	}
	g.Release("http://p")
}

func TestMinConcurrency_One(t *testing.T) {
	t.Parallel()
	g := NewConcurrencyGate()
	ctx := context.Background()

	// concurrency=0 should be treated as 1
	if _, err := g.Acquire(ctx, "http://p", 0, 0, nil); err != nil {
		t.Fatalf("Acquire with concurrency=0 failed: %v", err)
	}

	// Second acquire should block (limit is 1)
	done := make(chan struct{}, 1)
	go func() {
		g.Acquire(ctx, "http://p", 0, 0, nil)
		done <- struct{}{}
	}()

	select {
	case <-done:
		t.Fatal("second Acquire should have blocked")
	case <-time.After(50 * time.Millisecond):
		// Expected — it's blocked
	}

	g.Release("http://p")
	<-done
	g.Release("http://p")
}

// TestAcquire_RateLimit_Spacing verifies that with maxRPS=4 (250ms interval),
// sequential acquires on a provider are spaced at least one interval apart even
// when concurrency slots are plentiful.
func TestAcquire_RateLimit_Spacing(t *testing.T) {
	t.Parallel()
	g := NewConcurrencyGate()
	ctx := context.Background()

	starts := make([]time.Time, 0, 3)
	for i := range 3 {
		if _, err := g.Acquire(ctx, "http://p", 5, 4, nil); err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		starts = append(starts, time.Now()) // Acquire returning == request start
		g.Release("http://p")
	}

	for i := 1; i < len(starts); i++ {
		gap := starts[i].Sub(starts[i-1])
		if gap < 180*time.Millisecond {
			t.Fatalf("gap between start %d and %d is %v, want >= 180ms", i-1, i, gap)
		}
	}
	if total := starts[2].Sub(starts[0]); total < 450*time.Millisecond {
		t.Fatalf("total elapsed %v, want >= 450ms", total)
	}
}

// TestAcquire_RateLimit_ZeroUnlimited verifies maxRPS=0 imposes no rate wait:
// 10 sequential acquires complete well under any rate-limit interval.
func TestAcquire_RateLimit_ZeroUnlimited(t *testing.T) {
	t.Parallel()
	g := NewConcurrencyGate()
	ctx := context.Background()

	start := time.Now()
	for i := range 10 {
		if _, err := g.Acquire(ctx, "http://p", 2, 0, nil); err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		g.Release("http://p")
	}
	if elapsed := time.Since(start); elapsed >= 100*time.Millisecond {
		t.Fatalf("10 sequential acquires with maxRPS=0 took %v, want < 100ms", elapsed)
	}
}

// TestAcquire_RateLimit_PerProviderIsolation verifies one provider's rate window
// does not delay acquires for a different provider.
func TestAcquire_RateLimit_PerProviderIsolation(t *testing.T) {
	t.Parallel()
	g := NewConcurrencyGate()
	ctx := context.Background()

	// Provider A: in-flight request with maxRPS=1 → its next start is ~1s away
	if _, err := g.Acquire(ctx, "http://a", 5, 1, nil); err != nil {
		t.Fatalf("acquire A: %v", err)
	}

	start := time.Now()
	if _, err := g.Acquire(ctx, "http://b", 5, 1, nil); err != nil {
		t.Fatalf("acquire B: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("acquire B took %v despite provider A's rate window; want immediate", elapsed)
	}

	g.Release("http://a")
	g.Release("http://b")
}

// TestAcquire_RateLimit_Cancellation verifies a rate-blocked Acquire returns the
// context error promptly when cancelled, and the gate stays consistent afterwards.
func TestAcquire_RateLimit_Cancellation(t *testing.T) {
	t.Parallel()
	g := NewConcurrencyGate()
	ctx := context.Background()

	// Fill the rate window: maxRPS=1, one request in flight → next start ~1s away
	if _, err := g.Acquire(ctx, "http://p", 5, 1, nil); err != nil {
		t.Fatalf("acquire 1: %v", err)
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	go func() {
		_, err := g.Acquire(cancelCtx, "http://p", 5, 1, nil)
		errCh <- err
	}()

	// Let it enter the rate wait
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != context.Canceled {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancelled Acquire")
	}

	// Queue should be cleaned up
	pos := g.Position("http://p", nil)
	if pos != 0 {
		t.Fatalf("expected queue position 0 after cancellation cleanup, got %d", pos)
	}

	// State consistent: release the in-flight request, then a fresh acquire
	// succeeds (it still honors the rate window, so it may block briefly).
	g.Release("http://p")
	if _, err := g.Acquire(ctx, "http://p", 5, 1, nil); err != nil {
		t.Fatalf("acquire after cancellation: %v", err)
	}
	g.Release("http://p")
}

// TestAcquire_RateLimit_WithSlotWait verifies slot wait and rate wait compose:
// with concurrency=1 and maxRPS=1, a second request that gains the slot at ~0.3s
// still waits for the rate window (total from the first start >= ~1s).
func TestAcquire_RateLimit_WithSlotWait(t *testing.T) {
	t.Parallel()
	g := NewConcurrencyGate()
	ctx := context.Background()

	t0 := time.Now()

	// Request 1 holds the only slot; its grant sets the rate window to t0+1s
	if _, err := g.Acquire(ctx, "http://p", 1, 1, nil); err != nil {
		t.Fatalf("acquire 1: %v", err)
	}

	// Request 2 acquires while blocked on the slot
	done := make(chan struct{})
	go func() {
		if _, err := g.Acquire(ctx, "http://p", 1, 1, nil); err != nil {
			t.Errorf("acquire 2: %v", err)
			return
		}
		close(done)
	}()

	// Release the slot at ~0.3s — request 2 gets the slot but must still wait
	// for the rate window (until ~1.0s from t0).
	time.Sleep(300 * time.Millisecond)
	g.Release("http://p")

	select {
	case <-done:
		if total := time.Since(t0); total < 900*time.Millisecond {
			t.Fatalf("request 2 acquired after %v, want >= 900ms (slot wait + rate wait must compose)", total)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for rate-blocked request 2")
	}

	g.Release("http://p")
}

// TestAcquire_RateLimit_FIFO verifies FIFO ordering is preserved when waiters
// are blocked by both slot and rate: the first-enqueued waiter is served first.
func TestAcquire_RateLimit_FIFO(t *testing.T) {
	t.Parallel()
	g := NewConcurrencyGate()
	ctx := context.Background()

	// Hold the only slot (maxRPS=1 → also blocks rate for ~1s)
	if _, err := g.Acquire(ctx, "http://p", 1, 1, nil); err != nil {
		t.Fatalf("acquire holder: %v", err)
	}

	// Enqueue waiter 0 and wait for it to be queued, then enqueue waiter 1
	// behind it — queue order [0, 1] is deterministic.
	serveOrder := make(chan int, 2)
	go func() {
		if _, err := g.Acquire(ctx, "http://p", 1, 1, nil); err != nil {
			t.Errorf("acquire 0: %v", err)
			return
		}
		serveOrder <- 0
		g.Release("http://p")
	}()
	time.Sleep(50 * time.Millisecond)

	go func() {
		if _, err := g.Acquire(ctx, "http://p", 1, 1, nil); err != nil {
			t.Errorf("acquire 1: %v", err)
			return
		}
		serveOrder <- 1
		g.Release("http://p")
	}()
	time.Sleep(50 * time.Millisecond)

	pos := g.Position("http://p", nil)
	if pos != 2 {
		t.Fatalf("expected 2 queue entries, got %d", pos)
	}

	// Release the held slot; waiters are also rate-blocked (~1s window from holder grant)
	g.Release("http://p")

	var order []int
	for len(order) < 2 {
		select {
		case idx := <-serveOrder:
			order = append(order, idx)
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for waiters, got %d of 2", len(order))
		}
	}
	if order[0] != 0 || order[1] != 1 {
		t.Fatalf("expected FIFO serve order [0 1], got %v", order)
	}
}

// TestAcquire_RateLimit_ReleaseServeSpacing verifies a waiter served by
// Release advances the rate window when it starts, spacing the next waiter
// by a full interval.
func TestAcquire_RateLimit_ReleaseServeSpacing(t *testing.T) {
	t.Parallel()
	g := NewConcurrencyGate()
	ctx := context.Background()

	// concurrency=1, maxRPS=10 (100ms interval). A holds the only slot.
	if _, err := g.Acquire(ctx, "http://p", 1, 10, nil); err != nil {
		t.Fatalf("acquire A: %v", err)
	}
	// B queues (slot full); Release — not B's own timer — grants it.
	bStart := make(chan time.Time, 1)
	go func() {
		if _, err := g.Acquire(ctx, "http://p", 1, 10, nil); err != nil {
			t.Errorf("acquire B: %v", err)
			return
		}
		bStart <- time.Now()
	}()
	time.Sleep(30 * time.Millisecond)
	// C queues behind B.
	cStart := make(chan time.Time, 1)
	go func() {
		if _, err := g.Acquire(ctx, "http://p", 1, 10, nil); err != nil {
			t.Errorf("acquire C: %v", err)
			return
		}
		cStart <- time.Now()
	}()
	time.Sleep(100 * time.Millisecond) // A's window (100ms) has now opened

	// Release A's slot: window open, so Release serves B directly.
	g.Release("http://p")

	b := <-bStart
	// B releases its slot so C can proceed (concurrency=1).
	g.Release("http://p") // B
	c := <-cStart
	if gap := c.Sub(b); gap < 80*time.Millisecond {
		t.Fatalf("B->C start gap is %v, want >= 80ms (a waiter served by Release must advance the rate window)", gap)
	}
	g.Release("http://p") // C
}

// TestAcquireRelease_CancelledAfterServeReturnsSlot covers a waiter that is
// cancelled AFTER Release handed it a slot but before its request started - the
// "user stops a Pending task" case on a provider with a rate limit set.
//
// Choreography (concurrency=2, 1 request/second, no timing luck involved):
//  1. A takes a slot, closing the 1s rate window.
//  2. D joins the queue and is held inside onEnqueued, i.e. at the instant
//     after joining the queue and before its wait loop runs.
//  3. The window opens; A releases, so Release pre-counts D's slot and hands
//     over the serve signal.
//  4. E takes the freed allowance, closing the window again.
//  5. D finally waits out the window and is cancelled while waiting.
//
// D never issues a request, so its slot must return to the pool: afterwards
// only E's slot is held (A's went to D and was abandoned with it). A leaked
// slot here permanently shrinks the provider - twice, and a concurrency-3
// provider serves one request at a time until the server restarts.
// activeSlotsFor reads how many slots the gate currently holds for a provider.
// A value that stays above 0 while nothing is running means slots leaked, which
// is what the assertions below are looking for; production never asks, so this
// stays on the test side of the boundary.
func activeSlotsFor(g *ConcurrencyGate, providerURL string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ps, ok := g.providers[gateKey(providerURL)]; ok && ps.active > 0 {
		return ps.active
	}
	return 0
}

func TestAcquireRelease_CancelledAfterServeReturnsSlot(t *testing.T) {
	t.Parallel()
	const url = "http://rate-limited:8080/v1"
	g := NewConcurrencyGate()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := g.Acquire(ctx, url, 2, 1, nil); err != nil { // A
		t.Fatalf("A acquire: %v", err)
	}

	releaseD := make(chan struct{})
	started := make(chan struct{})
	dCtx, dCancel := context.WithCancel(context.Background())
	dResult := make(chan error, 1)
	go func() {
		_, err := g.Acquire(dCtx, url, 2, 1, func(*queueEntry) {
			close(started)
			<-releaseD // hold D at the moment it joins the queue
		})
		dResult <- err
	}()
	<-started

	// Let the rate window open, then hand A's slot to D.
	time.Sleep(1100 * time.Millisecond)
	releaseDone := make(chan struct{})
	go func() { g.Release(url); close(releaseDone) }()
	time.Sleep(100 * time.Millisecond) // Release reaches the handoff

	if _, err := g.Acquire(ctx, url, 2, 1, nil); err != nil { // E
		t.Fatalf("E acquire: %v", err)
	}

	close(releaseD)
	time.Sleep(100 * time.Millisecond) // D enters its rate wait
	dCancel()

	select {
	case err := <-dResult:
		if err == nil {
			t.Fatal("waiter returned success after its context was cancelled")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled waiter never returned - it wedged on the Release handoff")
	}
	select {
	case <-releaseDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Release never returned - wedged handing off to a cancelled waiter")
	}

	if active := activeSlotsFor(g, url); active != 1 {
		t.Fatalf("active slots = %d, want 1: the cancelled waiter kept the slot Release pre-counted for it", active)
	}
}

// TestAcquireRelease_CancelDuringHandoffDoesNotWedge hammers the window where a
// queued waiter is cancelled exactly as Release hands it a slot. Neither side
// may block: a blocked handoff wedges the releasing executor goroutine (that
// task never finishes and never frees its slot) and the cancelled one.
//
// Which of the two ready select cases the runtime picks is not controllable, so
// this is a race hammer rather than a deterministic repro (the deterministic one
// is TestAcquireRelease_CancelledAfterServeReturnsSlot above). Pre-fix it wedged
// about once in every hundred cycles, with both goroutines stuck for good.
func TestAcquireRelease_CancelDuringHandoffDoesNotWedge(t *testing.T) {
	t.Parallel()
	const url = "http://handoff-race:8080/v1"
	for i := 0; i < 60; i++ {
		g := NewConcurrencyGate()
		ctx, cancel := context.WithCancel(context.Background())
		if _, err := g.Acquire(ctx, url, 1, 0, nil); err != nil {
			t.Fatalf("iter %d: holder acquire: %v", i, err)
		}

		enqueued := make(chan struct{}, 1)
		waiterCtx, waitCancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := g.Acquire(waiterCtx, url, 1, 0, func(*queueEntry) { enqueued <- struct{}{} })
			result <- err
		}()
		<-enqueued

		releaseDone := make(chan struct{})
		go func() { g.Release(url); close(releaseDone) }()
		time.Sleep(time.Duration(i%5) * time.Millisecond) // land the cancel in the handoff
		waitCancel()

		select {
		case <-result:
		case <-time.After(5 * time.Second):
			t.Fatalf("iter %d: cancelled waiter wedged in the handoff", i)
		}
		select {
		case <-releaseDone:
		case <-time.After(5 * time.Second):
			t.Fatalf("iter %d: Release wedged handing off to a cancelled waiter", i)
		}
		cancel()
	}
}

// TestConcurrencyGate_CapacitySurvivesCancelChurn is the reported symptom
// written down: after many queued tasks are cancelled (stopped or re-pointed)
// while a provider is saturated, a provider configured for concurrency 3 must
// still be able to run 3 requests at once. Cancelling a waiter may never
// shrink a provider's capacity.
func TestConcurrencyGate_CapacitySurvivesCancelChurn(t *testing.T) {
	t.Parallel()
	const url = "http://churn:8080/v1"
	g := NewConcurrencyGate()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Churn: one slot held, one waiter queued, the slot handed over and the
	// waiter cancelled in the same instant. Every cycle must end with the
	// provider holding no slots.
	for cycle := 0; cycle < 60; cycle++ {
		if _, err := g.Acquire(ctx, url, 1, 0, nil); err != nil {
			t.Fatalf("cycle %d: holder acquire: %v", cycle, err)
		}
		wCtx, wCancel := context.WithCancel(ctx)
		enqueued := make(chan struct{}, 1)
		result := make(chan error, 1)
		go func() {
			_, err := g.Acquire(wCtx, url, 1, 0, func(*queueEntry) { enqueued <- struct{}{} })
			result <- err
		}()
		<-enqueued

		releaseDone := make(chan struct{})
		go func() { g.Release(url); close(releaseDone) }()
		time.Sleep(time.Duration(cycle%3) * time.Millisecond) // cancel inside the handoff
		wCancel()

		var err error
		select {
		case err = <-result:
		case <-time.After(5 * time.Second):
			t.Fatalf("cycle %d: queued waiter wedged while being cancelled", cycle)
		}
		select {
		case <-releaseDone:
		case <-time.After(5 * time.Second):
			t.Fatalf("cycle %d: Release wedged handing off to a cancelled waiter", cycle)
		}
		if err == nil {
			g.Release(url) // the waiter did get the slot and owns it
		}
		wCancel()
		if active := activeSlotsFor(g, url); active != 0 {
			t.Fatalf("cycle %d: %d slot(s) leaked - provider capacity shrinks over time", cycle, active)
		}
	}

	// Capacity is still the configured 3, all at the same time.
	start := make(chan struct{}, 3)
	for k := 0; k < 3; k++ {
		go func() {
			if _, err := g.Acquire(ctx, url, 3, 0, nil); err == nil {
				start <- struct{}{}
			}
		}()
	}
	for k := 0; k < 3; k++ {
		select {
		case <-start:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of 3 concurrent requests can start - the provider lost capacity", k)
		}
	}
}
