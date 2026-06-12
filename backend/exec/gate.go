package exec

import (
	"context"
	"sync"
	"time"
)

// providerState tracks active slots for a single provider.
type providerState struct {
	active      int
	lastAcquire time.Time
}

// queueEntry represents a goroutine waiting for a provider slot.
type queueEntry struct {
	provider string
	done     chan struct{} // closed when entry is served or cancelled
}

// ConcurrencyGate limits the number of concurrent operations per provider.
// Providers are identified by URL. Each provider has a configurable slot count.
// When all slots are in use, Acquire blocks until one becomes available.
// Queue position and estimated wait time are tracked for status reporting.
type ConcurrencyGate struct {
	mu   sync.Mutex
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

// Acquire blocks until a slot is available for the given provider.
// concurrency is the maximum number of concurrent slots for this provider (min 1).
// Returns queue position (0 = not queued, slot available immediately).
func (g *ConcurrencyGate) Acquire(ctx context.Context, providerURL string, concurrency int) (int, error) {
	if concurrency < 1 {
		concurrency = 1
	}

	g.mu.Lock()
	ps, ok := g.providers[providerURL]
	if !ok {
		ps = &providerState{}
		g.providers[providerURL] = ps
	}

	// Slot available — take it immediately
	if ps.active < concurrency {
		ps.active++
		g.mu.Unlock()
		return 0, nil
	}

	// Must wait — enqueue
	entry := &queueEntry{provider: providerURL, done: make(chan struct{})}
	g.queue = append(g.queue, entry)
	g.mu.Unlock()

	// Block until served or cancelled
	select {
	case <-entry.done:
		return 0, nil
	case <-ctx.Done():
		close(entry.done)
		g.cleanupEntry(entry)
		return 0, ctx.Err()
	}
}

// Release returns a slot for the given provider and serves the next waiter (if any).
func (g *ConcurrencyGate) Release(providerURL string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ps, ok := g.providers[providerURL]
	if !ok || ps.active <= 0 {
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

	// Serve next waiter in queue (skip cancelled entries)
	for i, e := range g.queue {
		if e.provider != providerURL {
			continue
		}
		select {
		case <-e.done:
			// Already cancelled — remove
			g.queue = append(g.queue[:i], g.queue[i+1:]...)
			i-- // adjust index after removal
			continue
		default:
			// Active waiter — serve it
			ps.active++
			ps.lastAcquire = time.Now()
			close(e.done)
			g.queue = append(g.queue[:i], g.queue[i+1:]...)
			return
		}
	}
}

// Position returns the 1-indexed queue position for the given provider.
// Returns 0 if no one is waiting.
func (g *ConcurrencyGate) Position(providerURL string) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	ahead := 0
	for _, e := range g.queue {
		if e.provider != providerURL {
			continue
		}
		select {
		case <-e.done:
			continue // cancelled, skip
		default:
			ahead++
		}
	}
	return ahead
}

// EstimateWait returns the estimated wait time in seconds for the given provider.
// Based on average slot duration × queue position.
func (g *ConcurrencyGate) EstimateWait(providerURL string) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	ahead := 0
	for _, e := range g.queue {
		if e.provider != providerURL {
			continue
		}
		select {
		case <-e.done:
			continue
		default:
			ahead++
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
		select {
		case <-e.done:
		default:
			close(e.done)
		}
	}
	g.queue = nil
}

// cleanupEntry removes a cancelled entry from the queue.
func (g *ConcurrencyGate) cleanupEntry(entry *queueEntry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, e := range g.queue {
		if e == entry {
			g.queue = append(g.queue[:i], g.queue[i+1:]...)
			break
		}
	}
}
