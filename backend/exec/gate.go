package exec

import (
	"context"
	"errors"
	"strings"
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
	provider     string
	done         chan struct{}
	once         sync.Once
	served       bool
	slotReturned bool
}

// errGateAbandoned reports that a waiter left the queue without ever holding a
// slot. Reported as an error (never a nil grant) so a caller can never proceed
// thinking it holds a slot it does not have.
var errGateAbandoned = errors.New("concurrency gate: waiter abandoned without a slot")

// newQueueEntry builds a waiter entry for a provider gate key.
func newQueueEntry(provider string) *queueEntry {
	return &queueEntry{provider: provider, done: make(chan struct{}, 1)}
}

// gateKey normalizes the provider URL (trailing slash) so URLs that differ
// only by a trailing slash map to the same gate key, no matter how callers
// format the URL.
func gateKey(providerURL string) string {
	return strings.TrimRight(providerURL, "/")
}

// ConcurrencyGate limits concurrent operations per provider (by URL) and,
// optionally, the rate at which new requests may start (0 = unlimited).
// Acquire blocks until a slot and rate allowance are available.
type ConcurrencyGate struct {
	mu sync.Mutex

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

// grantLocked bumps the active count, slot-duration tracking and rate
// window. Caller must hold g.mu with slot and rate verified available.
func (g *ConcurrencyGate) grantLocked(ps *providerState, maxRPS int) {
	now := time.Now()
	ps.active++
	ps.lastAcquire = now
	g.advanceWindowLocked(ps, maxRPS, now)
}

// advanceWindowLocked pushes the rate window forward one interval for a
// start at now (no-op when maxRPS <= 0). Caller must hold g.mu.
func (g *ConcurrencyGate) advanceWindowLocked(ps *providerState, maxRPS int, now time.Time) {
	if maxRPS > 0 {
		ps.nextAllowed = now.Add(time.Second / time.Duration(maxRPS))
	}
}

// frontUnservedIdxLocked returns the index of the first unserved,
// non-cancelled entry for the provider, or -1
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

// Acquire blocks until a slot and rate allowance are available for the provider
// (concurrency min 1; maxRPS 0 = unlimited) and returns the queue entry (nil = granted
// immediately), or an error if ctx is cancelled.
//
// onEnqueued (may be nil) is called with the caller's entry as soon as it is enqueued,
// before Acquire blocks, so a caller can report its own queue position while waiting —
// Position/EstimateWait on a nil entry count all waiters, not just those ahead.
func (g *ConcurrencyGate) Acquire(ctx context.Context, providerURL string, concurrency, maxRPS int, onEnqueued func(*queueEntry)) (*queueEntry, error) {
	providerURL = gateKey(providerURL)
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

	// Slot and rate open — take immediately (same condition as the queued
	// path; a pending window from an earlier limited caller binds all).
	if ps.active < concurrency && !ps.nextAllowed.After(time.Now()) {
		g.grantLocked(ps, maxRPS)
		g.mu.Unlock()
		return nil, nil
	}

	// Must wait — enqueue
	entry := newQueueEntry(providerURL)
	g.queue = append(g.queue, entry)
	g.mu.Unlock()
	if onEnqueued != nil {
		onEnqueued(entry)
	}

	// Block until granted or cancelled
	for {
		g.mu.Lock()
		idx := g.queueIndexLocked(entry)
		if idx == -1 {
			served := entry.served
			g.mu.Unlock()
			if !served {
				// Removed without being served — cancelled. Never report a grant
				// we do not hold, even if the context has no error yet.
				if err := ctx.Err(); err != nil {
					return entry, err
				}
				return entry, errGateAbandoned
			}
			// Release pre-counted the slot for us. Wait out the rate window,
			// then start — or hand the slot back if we are cancelled first.
			for {
				g.mu.Lock()
				if !ps.nextAllowed.After(time.Now()) {
					// Start consumes the rate allowance — advance the window.
					g.advanceWindowLocked(ps, maxRPS, time.Now())
					g.mu.Unlock()
					return entry, nil
				}
				wait := time.Until(ps.nextAllowed)
				g.mu.Unlock()
				select {
				case <-entry.done:
					// Serve hint — the loop re-checks the window under lock.
				case <-ctx.Done():
					// We hold a slot we will never use. Give it back, or the
					// provider loses that capacity for the process lifetime.
					g.abandonServedSlot(entry)
					return entry, ctx.Err()
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
			return entry, nil
		}

		// Sleep until something can change: rate window opening, a Release
		// serve, or cancellation; else a short re-check backoff.
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
			// Served by Release (or closed on cancel) — re-check under lock.
		case <-ctx.Done():
			timer.Stop()
			entry.once.Do(func() { close(entry.done) })
			g.cleanupEntry(entry)
			// Release may already have pre-counted a slot for us and be on its
			// way to (or already done with) the handoff. Return that slot.
			g.abandonServedSlot(entry)
			return entry, ctx.Err()
		case <-timer.C:
			// Rate window elapsed (or backoff) — re-check.
		}
		timer.Stop()
	}
}

// Release returns a slot and serves the front waiter when the rate window is
// open (pre-counting its slot); a rate-blocked waiter grants itself on its
// rate timer.
func (g *ConcurrencyGate) Release(providerURL string) {
	providerURL = gateKey(providerURL)
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

	// Serve the front waiter only when the rate window is open.
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

	// Signal outside the lock. The send is non-blocking (entry.done is
	// buffered): the slot is already accounted for, and a waiter that cancelled
	// first — closing the channel — must not be able to wedge this caller.
	if signal != nil {
		signal.once.Do(func() {
			select {
			case signal.done <- struct{}{}:
			default:
			}
		})
	}
}

// countAheadLocked returns waiters ahead of currentEntry (nil = any) and
// whether it is queued. Caller must hold g.mu.
func (g *ConcurrencyGate) countAheadLocked(providerURL string, currentEntry *queueEntry) (ahead int, found bool) {
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
	return ahead, found
}

// positionLocked returns the 1-indexed queue position for the provider
// (currentEntry nil counts all waiters); 0 if no one is waiting. Caller must
// hold g.mu.
func (g *ConcurrencyGate) positionLocked(providerURL string, currentEntry *queueEntry) int {
	ahead, found := g.countAheadLocked(providerURL, currentEntry)
	if found {
		return ahead + 1
	}
	return ahead
}

// Position returns the 1-indexed queue position for the provider (currentEntry
// nil counts all waiters); 0 if no one is waiting.
func (g *ConcurrencyGate) Position(providerURL string, currentEntry *queueEntry) int {
	providerURL = gateKey(providerURL)
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.positionLocked(providerURL, currentEntry)
}

// EstimateWait returns the estimated wait in seconds: average slot duration ×
// queue position (Position with the same entry; 30s per slot with no history).
func (g *ConcurrencyGate) EstimateWait(providerURL string, currentEntry *queueEntry) int {
	providerURL = gateKey(providerURL)
	g.mu.Lock()
	defer g.mu.Unlock()

	pos := g.positionLocked(providerURL, currentEntry)
	if pos == 0 {
		return 0
	}
	if g.completions == 0 {
		return pos * 30 // default estimate: 30s per slot
	}
	avgSlot := g.totalSlotTime / time.Duration(g.completions)
	return int((avgSlot * time.Duration(pos)) / time.Second)
}

// cleanupEntry removes a cancelled entry from the queue.
func (g *ConcurrencyGate) cleanupEntry(entry *queueEntry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.removeEntryLocked(entry)
}

// abandonServedSlot returns to the provider pool the slot that Release pre-counted for entry
// when the waiter gives up during the handoff window — its context was cancelled after the
// handoff was made but before the request started. Without this, every such cancel
// permanently inflates ps.active: with concurrency 3, two of them leave a provider stuck at
// one in-flight request until the process restarts. The release path also hands the slot to
// the next waiter. No-op when Release never pre-counted a slot, or it was already returned.
func (g *ConcurrencyGate) abandonServedSlot(entry *queueEntry) {
	g.mu.Lock()
	returnSlot := entry.served && !entry.slotReturned
	if returnSlot {
		entry.slotReturned = true
	}
	g.mu.Unlock()
	if returnSlot {
		g.Release(entry.provider)
	}
}
