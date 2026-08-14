package exec

import (
	"context"
	"sync"
	"time"
)

// providerState tracks active slots and the rate window for a single provider.
type providerState struct {
	active      int
	lastAcquire time.Time
	// nextAllowed is when the next request may start (zero = no rate constraint).
	nextAllowed time.Time
}

// queueEntry represents a goroutine waiting for a provider slot or rate allowance.
type queueEntry struct {
	provider string
	done     chan struct{} // receives when Release serves the entry; closed when cancelled
	once     sync.Once     // ensures exactly one signal (serve or cancel)
	// served is set under the gate lock when Release removes the entry and
	// pre-counts its slot. The entry then waits out the rate window before
	// returning success.
	served bool
}

// ConcurrencyGate limits the number of concurrent operations per provider and
// (optionally) the rate at which new requests may start. Providers are
// identified by URL. Each provider has a configurable slot count and a
// per-second start limit (0 = unlimited). When a slot is unavailable or the
// rate window is pending, Acquire blocks until a grant is possible.
// Queue position and estimated wait time are tracked for status reporting.
type ConcurrencyGate struct {
	mu     sync.Mutex
	closed bool

	// Per-provider state
	providers map[string]*providerState

	// Wait queue — entries are removed when served or cancelled
	queue []*queueEntry

	// Estimated wait tracking (global across all providers)
	totalSlotTime time.Duration
	completions   int
}

// NewConcurrencyGate creates a new ConcurrencyGate.
func NewConcurrencyGate() *ConcurrencyGate {
	return &ConcurrencyGate{
		providers: make(map[string]*providerState),
	}
}

// grantLocked bumps the active count, updates slot-duration tracking and the
// rate window. Caller must hold g.mu and have verified the slot and rate are
// available.
func (g *ConcurrencyGate) grantLocked(ps *providerState, maxRPS int) {
	now := time.Now()
	ps.active++
	ps.lastAcquire = now
	if maxRPS > 0 {
		ps.nextAllowed = now.Add(time.Second / time.Duration(maxRPS))
	}
}

// frontUnservedIdxLocked returns the index in g.queue of the first entry for
// the given provider whose done channel is open (unserved, not cancelled), or
// -1. Caller must hold g.mu.
func (g *ConcurrencyGate) frontUnservedIdxLocked(providerURL string) int {
	for i, e := range g.queue {
		if e.provider != providerURL {
			continue
		}
		select {
		case <-e.done:
			// Cancelled — skip
		default:
			return i
		}
	}
	return -1
}

// queueIndexLocked returns the index of entry in g.queue, or -1. Caller must
// hold g.mu.
func (g *ConcurrencyGate) queueIndexLocked(entry *queueEntry) int {
	for i, e := range g.queue {
		if e == entry {
			return i
		}
	}
	return -1
}

// removeEntryLocked removes entry from g.queue. Caller must hold g.mu.
func (g *ConcurrencyGate) removeEntryLocked(entry *queueEntry) {
	for i, e := range g.queue {
		if e == entry {
			g.queue = append(g.queue[:i], g.queue[i+1:]...)
			return
		}
	}
}

// Acquire blocks until a slot and rate allowance are available for the given
// provider. concurrency is the maximum number of concurrent slots for this
// provider (min 1). maxRPS is the maximum number of new request starts per
// second (0 or <1 = unlimited). Returns queue position (0 = not queued, slot
// available immediately), the entry, and an error if the context is cancelled.
func (g *ConcurrencyGate) Acquire(ctx context.Context, providerURL string, concurrency, maxRPS int) (int, *queueEntry, error) {
	if concurrency < 1 {
		concurrency = 1
	}
	if maxRPS < 1 {
		maxRPS = 0
	}

	g.mu.Lock()
	ps, ok := g.providers[providerURL]
	if !ok {
		ps = &providerState{}
		g.providers[providerURL] = ps
	}

	// Slot and rate allowance — take it immediately
	if ps.active < concurrency && (maxRPS == 0 || !ps.nextAllowed.After(time.Now())) {
		g.grantLocked(ps, maxRPS)
		g.mu.Unlock()
		return 0, nil, nil
	}

	// Must wait — enqueue
	entry := &queueEntry{provider: providerURL, done: make(chan struct{})}
	g.queue = append(g.queue, entry)
	g.mu.Unlock()

	// Block until granted or cancelled. After every wake (Release serve, rate
	// expiry, backoff) the grant condition is re-checked under the lock.
	// A queued entry only takes a slot when it is the front-most unserved
	// waiter for its provider, preserving FIFO. gotSignal tracks whether the
	// serve signal has been received, so the handshake with Release is
	// consumed exactly once.
	var gotSignal bool
	for {
		g.mu.Lock()
		idx := g.queueIndexLocked(entry)
		if idx == -1 {
			served := entry.served
			g.mu.Unlock()
			if !served {
				// Removed without being served — cancelled.
				return 0, entry, ctx.Err()
			}
			// Release pre-counted the slot. Wait out the rate window, then
			// return success. If the serve signal is still in flight (our own
			// timer woke us first), consume it so Release is unblocked.
			for {
				g.mu.Lock()
				if !ps.nextAllowed.After(time.Now()) {
					g.mu.Unlock()
					if !gotSignal {
						<-entry.done
					}
					return 0, entry, nil
				}
				wait := time.Until(ps.nextAllowed)
				g.mu.Unlock()
				select {
				case <-entry.done:
					gotSignal = true
				case <-ctx.Done():
					if !gotSignal {
						// Serve send still in flight — consume it so Release
						// isn't left blocked.
						<-entry.done
					}
					return 0, entry, ctx.Err()
				case <-time.After(wait):
				}
			}
		}

		// Still queued. Grantable only when front-most, slot free, rate open.
		front := g.frontUnservedIdxLocked(providerURL)
		if front == idx && ps.active < concurrency && !ps.nextAllowed.After(time.Now()) {
			g.removeEntryLocked(entry)
			g.grantLocked(ps, maxRPS)
			g.mu.Unlock()
			return 0, entry, nil
		}

		// Not grantable yet — sleep until something can change: the rate
		// window opening, a Release serve, or cancellation. When the rate is
		// already open, use a short re-check backoff so front-slot changes
		// (e.g. the front waiter granting itself and releasing) are picked up.
		var wait time.Duration
		if maxRPS > 0 && ps.nextAllowed.After(time.Now()) {
			wait = time.Until(ps.nextAllowed)
		} else {
			wait = 50 * time.Millisecond
		}
		g.mu.Unlock()

		timer := time.NewTimer(wait)
		select {
		case <-entry.done:
			// Served by Release (or closed on cancel/Close) — re-check.
			gotSignal = true
		case <-ctx.Done():
			timer.Stop()
			entry.once.Do(func() { close(entry.done) })
			g.cleanupEntry(entry)
			// If Release is mid-serve, its send is in flight — drain it so
			// Release isn't left blocked on the unbuffered channel.
			select {
			case <-entry.done:
			default:
			}
			return 0, entry, ctx.Err()
		case <-timer.C:
			// Rate window elapsed (or backoff) — re-check.
		}
		timer.Stop()
	}
}

// Release returns a slot for the given provider and serves the front waiter
// (if any): the slot is pre-counted for it and it is signalled. If the rate
// window is still pending the waiter stays queued and grants itself when its
// rate timer fires. The serve signal is sent outside the lock on an
// unbuffered channel, which blocks the sender until the receiver is
// scheduled. This ensures fair ordering: the waiter runs before Release
// returns, preventing the releasing goroutine from racing ahead.
func (g *ConcurrencyGate) Release(providerURL string) {
	g.mu.Lock()

	ps, ok := g.providers[providerURL]
	if !ok || ps.active <= 0 {
		g.mu.Unlock()
		return
	}

	// Track slot duration for estimated wait
	if ps.lastAcquire.IsZero() {
		ps.lastAcquire = time.Now()
	}
	elapsed := time.Since(ps.lastAcquire)
	g.totalSlotTime += elapsed
	g.completions++
	ps.lastAcquire = time.Time{} // reset

	ps.active--

	// Find the front-most unserved waiter for this provider. Serve it only
	// when the rate window is open; a rate-blocked waiter stays queued.
	var signal *queueEntry
	if idx := g.frontUnservedIdxLocked(providerURL); idx != -1 {
		e := g.queue[idx]
		if !ps.nextAllowed.After(time.Now()) {
			g.removeEntryLocked(e)
			e.served = true
			ps.active++
			ps.lastAcquire = time.Now()
			signal = e
		}
	}

	g.mu.Unlock()

	// Signal outside the lock. Send on unbuffered channel blocks until
	// the receiver is scheduled, ensuring the waiter runs before this
	// goroutine can compete for the lock again.
	if signal != nil {
		signal.once.Do(func() { signal.done <- struct{}{} })
	}
}

// Position returns the 1-indexed queue position for the given provider.
// currentEntry is the entry to find the position of; if nil, counts all active
// entries (legacy behavior for tests that don't track per-entry positions).
// Returns 0 if no one is waiting.
func (g *ConcurrencyGate) Position(providerURL string, currentEntry *queueEntry) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	ahead := 0
	found := false
	for _, e := range g.queue {
		if e.provider != providerURL {
			continue
		}
		select {
		case <-e.done:
			continue // cancelled, skip
		default:
			if currentEntry != nil && e == currentEntry {
				found = true
				continue
			}
			if !found {
				ahead++
			}
		}
	}
	if found {
		return ahead + 1
	}
	return ahead
}

// EstimateWait returns the estimated wait time in seconds for the given provider.
// Based on average slot duration × queue position.
// currentEntry is the entry to find the position of; if nil, counts all active
// entries (legacy behavior for tests that don't track per-entry positions).
func (g *ConcurrencyGate) EstimateWait(providerURL string, currentEntry *queueEntry) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	ahead := 0
	found := false
	for _, e := range g.queue {
		if e.provider != providerURL {
			continue
		}
		select {
		case <-e.done:
			continue
		default:
			if currentEntry != nil && e == currentEntry {
				found = true
				continue
			}
			if !found {
				ahead++
			}
		}
	}
	if ahead == 0 {
		return 0
	}
	if g.completions == 0 {
		return ahead * 30 // default estimate: 30s per slot
	}
	avgSlot := g.totalSlotTime / time.Duration(g.completions)
	return int((avgSlot * time.Duration(ahead)) / time.Second)
}

// Close shuts down the gate and releases all waiting goroutines.
func (g *ConcurrencyGate) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	for _, e := range g.queue {
		e.once.Do(func() { close(e.done) })
	}
	g.queue = nil
}

// cleanupEntry removes a cancelled entry from the queue.
func (g *ConcurrencyGate) cleanupEntry(entry *queueEntry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.removeEntryLocked(entry)
}
