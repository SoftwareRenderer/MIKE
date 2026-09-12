package exec

import (
	"context"
	"sync"
	"time"

	"kanban/pkg/logging"
)

// memoryGatePollInterval is the re-check interval when no Release signal arrives.
// Kept well below DefaultMemSupervisorInterval so a Release-triggered grant beats the supervisor tick.
const memoryGatePollInterval = 500 * time.Millisecond

// memWaiter is one goroutine blocked in Acquire. done is buffered so Release can signal without blocking.
type memWaiter struct {
	amountMB int64
	done     chan struct{}
	// served is set by Release before it signals. It is a wake-up hint; the grant
	// is confirmed by re-reading used under mu.
	served bool
}

func (w *memWaiter) signal() {
	select {
	case w.done <- struct{}{}:
	default:
	}
}

// MemoryGate is a blocking resource gate counting megabytes, not slots.
// capacity 0 disables the gate: every Acquire succeeds immediately.
type MemoryGate struct {
	mu       sync.Mutex
	used     int64
	capacity int64
	queue    []*memWaiter
}

// SetCapacity sets the ceiling in MB. Pass 0 to disable the gate.
func (g *MemoryGate) SetCapacity(capMB int64) {
	g.mu.Lock()
	g.capacity = capMB
	g.mu.Unlock()
}

// Acquire blocks until amountMB are available, or ctx is cancelled.
// A zero/negative amountMB or capacity 0 succeeds immediately.
// On grant the caller owns amountMB and must call Release with the same amount;
// on cancellation the caller owns nothing and must not call Release.
func (g *MemoryGate) Acquire(ctx context.Context, amountMB int64, onEnqueued func(*memWaiter)) error {
	if amountMB <= 0 {
		return nil
	}

	g.mu.Lock()
	if g.capacity == 0 {
		g.mu.Unlock()
		return nil
	}
	if g.used+amountMB <= g.capacity {
		g.used += amountMB
		g.mu.Unlock()
		return nil
	}

	w := &memWaiter{amountMB: amountMB, done: make(chan struct{}, 1)}
	g.queue = append(g.queue, w)
	g.mu.Unlock()

	if onEnqueued != nil {
		onEnqueued(w)
	}

	for {
		g.mu.Lock()
		idx := g.indexOfLocked(w)
		if idx == -1 {
			served := w.served
			g.mu.Unlock()
			if served {
				// Release already bumped used for us. If ctx cancelled in the same window, give it back.
				if ctx.Err() != nil {
					g.Release(amountMB)
					return ctx.Err()
				}
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			return context.Canceled
		}

		front := g.frontLocked()
		if front == idx && g.used+amountMB <= g.capacity {
			g.removeLocked(w)
			g.used += amountMB
			g.mu.Unlock()
			if ctx.Err() != nil {
				g.Release(amountMB)
				return ctx.Err()
			}
			return nil
		}
		g.mu.Unlock()

		timer := time.NewTimer(memoryGatePollInterval)
		select {
		case <-w.done:
			timer.Stop()
		case <-ctx.Done():
			timer.Stop()
			g.mu.Lock()
			// Release may have served us between the last check and this cancel; return the slot.
			if w.served {
				g.used -= amountMB
				if g.used < 0 {
					g.used = 0
				}
				g.mu.Unlock()
				return ctx.Err()
			}
			g.removeLocked(w)
			g.mu.Unlock()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Release returns amountMB to the pool and signals the front waiter if there is now room.
// Releasing more than was acquired clamps to zero rather than going negative.
func (g *MemoryGate) Release(amountMB int64) {
	if amountMB <= 0 {
		return
	}
	g.mu.Lock()
	g.used -= amountMB
	if g.used < 0 {
		logging.For(logging.Exec).Warnf("memory gate: release underflow (used %d, releasing %d) — clamping to 0", g.used+amountMB, amountMB)
		g.used = 0
	}
	if idx := g.frontLocked(); idx != -1 {
		w := g.queue[idx]
		if g.used+w.amountMB <= g.capacity {
			g.removeLocked(w)
			g.used += w.amountMB
			w.served = true
			g.mu.Unlock()
			w.signal()
			return
		}
	}
	g.mu.Unlock()
}

// Capacity returns the gate ceiling in MB. 0 means disabled.
func (g *MemoryGate) Capacity() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.capacity
}

// Used returns the currently tracked memory in MB.
func (g *MemoryGate) Used() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.used
}

// QueueLen returns the number of tasks currently blocked in Acquire.
func (g *MemoryGate) QueueLen() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.queue)
}

// indexOfLocked returns the index of w in g.queue, or -1. Caller must hold g.mu.
func (g *MemoryGate) indexOfLocked(w *memWaiter) int {
	for i, e := range g.queue {
		if e == w {
			return i
		}
	}
	return -1
}

// frontLocked returns 0 if the queue is non-empty, else -1.
// Every waiter in the queue is live: served and cancelled entries are removed under mu before returning.
func (g *MemoryGate) frontLocked() int {
	if len(g.queue) == 0 {
		return -1
	}
	return 0
}

// removeLocked removes w from the queue. Caller must hold g.mu.
func (g *MemoryGate) removeLocked(w *memWaiter) {
	for i, e := range g.queue {
		if e == w {
			g.queue = append(g.queue[:i], g.queue[i+1:]...)
			return
		}
	}
}
