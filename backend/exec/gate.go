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

// errGateAbandoned reports that a waiter left the queue without ever holding a slot. A caller gets an
// error rather than a nil grant, so it cannot proceed believing it holds a slot.
var errGateAbandoned = errors.New("concurrency gate: waiter abandoned without a slot")

func newQueueEntry(provider string) *queueEntry {
	return &queueEntry{provider: provider, done: make(chan struct{}, 1)}
}

// gateKey drops the trailing slash so URLs that differ only by one share a gate.
func gateKey(providerURL string) string {
	return strings.TrimRight(providerURL, "/")
}

// ConcurrencyGate limits concurrent operations per provider (by URL) and, optionally, how fast new
// requests may start. Acquire blocks until a slot and a rate allowance are available.
type ConcurrencyGate struct {
	mu sync.Mutex

	providers map[string]*providerState
	queue     waitQueue[*queueEntry]

	totalSlotTime time.Duration
	completions   int
}

// NewConcurrencyGate creates a new ConcurrencyGate.
func NewConcurrencyGate() *ConcurrencyGate {
	return &ConcurrencyGate{
		providers: make(map[string]*providerState),
	}
}

// grantLocked takes the slot and advances the rate window. Caller holds g.mu with both verified
// available.
func (g *ConcurrencyGate) grantLocked(ps *providerState, maxRPS int) {
	now := time.Now()
	ps.active++
	ps.lastAcquire = now
	g.advanceWindowLocked(ps, maxRPS, now)
}

// advanceWindowLocked moves the rate window past a start at now. Caller holds g.mu.
func (g *ConcurrencyGate) advanceWindowLocked(ps *providerState, maxRPS int, now time.Time) {
	if maxRPS > 0 {
		ps.nextAllowed = now.Add(time.Second / time.Duration(maxRPS))
	}
}

// frontUnservedIdxLocked returns the index of the first entry still waiting for this provider, or -1.
func (g *ConcurrencyGate) frontUnservedIdxLocked(providerURL string) int {
	for i, e := range g.queue.entries {
		if e.provider != providerURL {
			continue
		}
		select {
		case <-e.done:
		default:
			return i
		}
	}
	return -1
}

// Acquire blocks until the provider has a slot and its rate window is open (concurrency min 1, maxRPS
// 0 = unlimited). It returns the waiter's queue entry, nil when granted immediately, or an error if ctx
// is cancelled.
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

	// The same condition the queued path below checks; one rate window binds every waiter.
	if ps.active < concurrency && !ps.nextAllowed.After(time.Now()) {
		g.grantLocked(ps, maxRPS)
		g.mu.Unlock()
		return nil, nil
	}

	entry := newQueueEntry(providerURL)
	g.queue.push(entry)
	g.mu.Unlock()
	if onEnqueued != nil {
		// Called before blocking, so a waiting caller can report its own position; a nil entry passed to
		// Position counts every waiter, not just those ahead.
		onEnqueued(entry)
	}

	for {
		g.mu.Lock()
		idx := g.queue.index(entry)
		if idx == -1 {
			served := entry.served
			g.mu.Unlock()
			if !served {
				// Removed without being served: cancelled. Report an error rather than a grant we do not
				// hold, even when ctx has no error yet.
				if err := ctx.Err(); err != nil {
					return entry, err
				}
				return entry, errGateAbandoned
			}
			// Release already took the slot for us. Wait out the rate window, or hand the slot back if
			// ctx ends first.
			for {
				g.mu.Lock()
				if !ps.nextAllowed.After(time.Now()) {
					g.advanceWindowLocked(ps, maxRPS, time.Now())
					g.mu.Unlock()
					return entry, nil
				}
				wait := time.Until(ps.nextAllowed)
				g.mu.Unlock()
				select {
				case <-entry.done:
				case <-ctx.Done():
					// A slot we will never use: give it back, or the provider loses that capacity for the
					// process lifetime.
					g.abandonServedSlot(entry)
					return entry, ctx.Err()
				case <-time.After(wait):
				}
			}
		}

		front := g.frontUnservedIdxLocked(providerURL)
		if front == idx && ps.active < concurrency && !ps.nextAllowed.After(time.Now()) {
			g.queue.remove(entry)
			g.grantLocked(ps, maxRPS)
			g.mu.Unlock()
			return entry, nil
		}

		// Nothing else wakes this loop except the window opening, so a slot freeing early is seen on the
		// next backoff.
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
		case <-ctx.Done():
			timer.Stop()
			entry.once.Do(func() { close(entry.done) })
			g.cleanupEntry(entry)
			// Release may have taken a slot for us already; return it.
			g.abandonServedSlot(entry)
			return entry, ctx.Err()
		case <-timer.C:
		}
		timer.Stop()
	}
}

// Release returns a slot and serves the front waiter when the rate window is open, taking its slot up
// front. A rate-blocked waiter grants itself on its rate timer.
func (g *ConcurrencyGate) Release(providerURL string) {
	providerURL = gateKey(providerURL)
	g.mu.Lock()

	ps, ok := g.providers[providerURL]
	if !ok || ps.active <= 0 {
		g.mu.Unlock()
		return
	}

	if ps.lastAcquire.IsZero() {
		ps.lastAcquire = time.Now()
	}
	elapsed := time.Since(ps.lastAcquire)
	g.totalSlotTime += elapsed
	g.completions++
	ps.lastAcquire = time.Time{}

	ps.active--

	var signal *queueEntry
	if idx := g.frontUnservedIdxLocked(providerURL); idx != -1 {
		e := g.queue.entries[idx]
		if !ps.nextAllowed.After(time.Now()) {
			g.queue.remove(e)
			e.served = true
			ps.active++
			ps.lastAcquire = time.Now()
			signal = e
		}
	}

	g.mu.Unlock()

	// Signalled outside the lock, and never blocking: the slot is already accounted for, and a waiter
	// that cancelled first has closed the channel.
	if signal != nil {
		signal.once.Do(func() { wake(signal.done) })
	}
}

// Promote moves a queued waiter to the front of the queue, so the next freed slot goes to it ahead of the
// waiters already queued. It returns false when the entry is no longer queued: it was granted, it gave
// up, or it was handed a slot and is waiting out the rate window — in every case the reorder would decide
// nothing.
//
// The waiter is not woken. It re-reads the gate on its own backoff, and entry.done is closed by its
// cancellation path, which a wake here would race with.
func (g *ConcurrencyGate) Promote(entry *queueEntry) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	i := g.queue.index(entry)
	if i == -1 {
		return false
	}
	g.queue.moveToFront(i)
	return true
}

// countAheadLocked returns the waiters ahead of currentEntry (nil = any) and whether it is queued.
// Caller holds g.mu.
func (g *ConcurrencyGate) countAheadLocked(providerURL string, currentEntry *queueEntry) (ahead int, found bool) {
	for _, e := range g.queue.entries {
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
	return ahead, found
}

// positionLocked is Position under the caller's lock.
func (g *ConcurrencyGate) positionLocked(providerURL string, currentEntry *queueEntry) int {
	ahead, found := g.countAheadLocked(providerURL, currentEntry)
	if found {
		return ahead + 1
	}
	return ahead
}

// Position returns the 1-indexed queue position for the provider, counting every waiter when
// currentEntry is nil, or 0 when no one is waiting.
func (g *ConcurrencyGate) Position(providerURL string, currentEntry *queueEntry) int {
	providerURL = gateKey(providerURL)
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.positionLocked(providerURL, currentEntry)
}

// EstimateWait returns the estimated wait in seconds: the average slot duration times the queue
// position.
func (g *ConcurrencyGate) EstimateWait(providerURL string, currentEntry *queueEntry) int {
	providerURL = gateKey(providerURL)
	g.mu.Lock()
	defer g.mu.Unlock()

	pos := g.positionLocked(providerURL, currentEntry)
	if pos == 0 {
		return 0
	}
	if g.completions == 0 {
		return pos * 30
	}
	avgSlot := g.totalSlotTime / time.Duration(g.completions)
	return int((avgSlot * time.Duration(pos)) / time.Second)
}

func (g *ConcurrencyGate) cleanupEntry(entry *queueEntry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.queue.remove(entry)
}

// abandonServedSlot returns the slot Release took for entry when the waiter gives up during the
// handoff; without it ps.active stays inflated until the process restarts. It is a no-op when no slot was
// taken or it was already returned, and releases through Release so the slot reaches the next waiter.
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
