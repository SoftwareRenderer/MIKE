package exec

import (
	"context"
	"sync"
	"time"

	"mike/pkg/logging"
)

// memoryGatePollInterval is how often a waiter re-checks when no Release signal arrives. It stays well
// under DefaultMemSupervisorInterval so a Release-triggered grant beats the supervisor tick.
const memoryGatePollInterval = 500 * time.Millisecond

// memWaiter is one goroutine blocked in Acquire. done is buffered so Release can signal without
// blocking.
type memWaiter struct {
	amountMB int64
	done     chan struct{}
	// served is set by Release before it signals. It is only a hint: the grant is confirmed by
	// re-reading used under mu.
	served bool
}

// MemoryGate is a blocking resource gate counting megabytes, not slots; capacity 0 disables it.
// Admission is `mikeMB + reservations + candidate <= capacity`, where mikeMB is what MIKE's own
// containers and processes cost, so the ceiling is MIKE's share of the machine and the other users of
// the host stay out of the sum.
type MemoryGate struct {
	mu       sync.Mutex
	used     int64 // reserved by tasks admitted but with no container yet
	capacity int64
	queue    waitQueue[*memWaiter]

	mikeMB      int64 // what MIKE's containers and its own process cost, limits included
	heldMB      int64 // what they actually hold; reported, never admitted against
	availableMB int64 // MemAvailable at the last sample
	totalMB     int64 // MemTotal at the last sample
	measured    bool  // false until the first sample; until then the reservations alone decide
}

// memoryAvailableFloorPct is the share of total RAM below which a task is made to wait rather than the
// host being pushed into an OOM kill. The admission ceiling answers a different question and is set in
// backend/server from MIKE_MEMORY_BUDGET_PCT.
const memoryAvailableFloorPct = 5

// SetMeasurements records one budget tick's readings. mikeMB is what MIKE's containers and process
// cost, heldMB what they actually hold; the two host figures decide only the pause floor.
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

// pressureLocked returns what MIKE itself costs the ceiling; a gate that has never been sampled answers
// 0 and leaves the reservation ledger to decide. Caller holds mu.
func (g *MemoryGate) pressureLocked() int64 {
	if !g.measured {
		return 0
	}
	return g.mikeMB
}

// roomLocked reports whether amountMB more fits under the ceiling; caller holds mu. A zero-size
// candidate is admitted only while the budget is already under the ceiling.
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

// Acquire blocks until amountMB are available or ctx is cancelled; a non-positive amount and a capacity
// of 0 succeed at once. On grant the caller owns amountMB and must Release the same amount; on
// cancellation it owns nothing.
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
	g.queue.push(w)
	g.mu.Unlock()

	if onEnqueued != nil {
		onEnqueued(w)
	}

	for {
		g.mu.Lock()
		idx := g.queue.index(w)
		if idx == -1 {
			served := w.served
			g.mu.Unlock()
			if served {
				// Release took this amount already. If ctx ended in the same window, give it back.
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
			g.queue.remove(w)
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
			// Release may have served us between the last check and this cancel.
			if w.served {
				g.used -= amountMB
				if g.used < 0 {
					g.used = 0
				}
				g.mu.Unlock()
				return ctx.Err()
			}
			g.queue.remove(w)
			g.mu.Unlock()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Release returns amountMB to the pool and serves the front waiter when there is room. Releasing more
// than was acquired clamps to zero.
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
		w := g.queue.entries[idx]
		if g.roomLocked(w.amountMB) {
			g.queue.remove(w)
			g.used += w.amountMB
			w.served = true
			g.mu.Unlock()
			wake(w.done)
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

// InUse returns what counts against the ceiling: MIKE's own charge plus the reservations.
func (g *MemoryGate) InUse() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.pressureLocked() + g.used
}

// BudgetSnapshot is one read of everything the gate decides and reports with, taken under the lock. The
// figures travel to one log line together.
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
	return g.queue.Len()
}

// frontLocked returns 0 when the queue holds a waiter, else -1. Every entry in it is live: served and
// cancelled entries are removed under mu.
func (g *MemoryGate) frontLocked() int {
	if len(g.queue.entries) == 0 {
		return -1
	}
	return 0
}
