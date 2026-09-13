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
//
// Admission is `mikeMB + reservations + candidate <= capacity`, with capacity a percentage of
// installed RAM. mikeMB is what MIKE's own containers and processes cost — each container at
// max(held, limit) — so the ceiling is MIKE's share of the machine and the other users of the host are
// not in the sum. Host free RAM is kept only for HostMemoryTight, which is what pauses tasks when the
// machine itself is nearly out.
type MemoryGate struct {
	mu       sync.Mutex
	used     int64 // reservations for tasks that are admitted but have no container yet
	capacity int64
	queue    []*memWaiter

	mikeMB      int64 // what MIKE's containers and its own process cost the ceiling, limits included
	heldMB      int64 // what they actually hold, reported beside mikeMB so a cap-dominated budget reads clearly
	availableMB int64 // MemAvailable at the last sample
	totalMB     int64 // MemTotal at the last sample
	measured    bool  // false until the first sample arrives, while the reservations alone decide
}

// memoryAvailableFloorPct is the percentage of total RAM below which the host is nearly out, and
// making a task wait is then how an OOM kill happens: the supervisor pauses one instead. The
// admission ceiling answers a different question — how much of the machine MIKE may hold — and is set
// in backend/server from MIKE_MEMORY_BUDGET_PCT.
const memoryAvailableFloorPct = 5

// SetMeasurements records one budget tick's readings, and wakes the waiters the new numbers admit.
// mikeMB is what MIKE's own containers and processes cost (each container at its limit when the limit
// is the larger figure) and heldMB what they actually hold, which the ceiling does not use but the
// budget log reports beside it. The two host figures decide only the pause floor. A gate that has never
// been measured decides from its reservations alone.
func (g *MemoryGate) SetMeasurements(mikeMB, heldMB, availableMB, totalMB int64) {
	g.mu.Lock()
	g.mikeMB, g.heldMB, g.availableMB, g.totalMB, g.measured = mikeMB, heldMB, availableMB, totalMB, true
	g.mu.Unlock()
}

// HostMemoryTight reports whether the host has less free RAM than the floor allows. A host that has
// never been measured is never tight.
func (g *MemoryGate) HostMemoryTight() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.measured || g.totalMB <= 0 {
		return false
	}
	return g.availableMB*100 < g.totalMB*memoryAvailableFloorPct
}

// pressureLocked returns what MIKE itself holds against the ceiling: its containers and processes at
// their limit. A gate that has never been sampled answers 0 and leaves the decision to the reservation
// ledger. Caller holds mu.
func (g *MemoryGate) pressureLocked() int64 {
	if !g.measured {
		return 0
	}
	return g.mikeMB
}

// roomLocked reports whether amountMB more fits under the ceiling. Caller holds mu. A candidate of
// zero size is admitted only while the budget is already under the ceiling, because there is no
// reservation to test.
func (g *MemoryGate) roomLocked(amountMB int64) bool {
	if g.capacity == 0 {
		return true
	}
	inUse := g.pressureLocked() + g.used
	if amountMB == 0 {
		return inUse < g.capacity
	}
	return inUse+amountMB <= g.capacity
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

	g.mu.Lock()
	if g.capacity == 0 {
		g.mu.Unlock()
		return nil
	}
	if g.roomLocked(amountMB) {
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
		if front == idx && g.roomLocked(amountMB) {
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
		if g.roomLocked(w.amountMB) {
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

// Used returns the reservations held for tasks whose container is not yet accounted, in MB.
func (g *MemoryGate) Used() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.used
}

// InUse returns what counts against the ceiling: MIKE's own charge plus the reservations.
func (g *MemoryGate) InUse() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pressureLocked() + g.used
}

// BudgetSnapshot is one read of everything the gate decides and reports with, taken under the lock: a
// struct because the figures travel together to one log line, and a positional return of this many
// megabytes is a mistake waiting to be made.
type BudgetSnapshot struct {
	CapacityMB     int64
	InUseMB        int64 // MikeMB + ReservationsMB
	MikeMB         int64
	HeldMB         int64
	ReservationsMB int64
	AvailableMB    int64
	Measured       bool
}

// Budget reports the numbers the gate is currently deciding with.
func (g *MemoryGate) Budget() BudgetSnapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	mike := g.pressureLocked()
	return BudgetSnapshot{
		CapacityMB:     g.capacity,
		InUseMB:        mike + g.used,
		MikeMB:         mike,
		HeldMB:         g.heldMB,
		ReservationsMB: g.used,
		AvailableMB:    g.availableMB,
		Measured:       g.measured,
	}
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
