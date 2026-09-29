package exec

import (
	"sync"
	"testing"
	"time"
)

// awaitSignal waits for a sibling goroutine to reach the point where it closes ch. Nothing may wait
// on such a close without a deadline: a goroutine that dies on a t.Fatalf never closes it, and the
// wait then becomes the package's 10-minute timeout instead of a sentence about what failed.
func awaitSignal(t *testing.T, timeout time.Duration, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(timeout):
		t.Fatalf("timed out after %v waiting for the goroutine to %s", timeout, what)
	}
}

// awaitWaitGroup waits for goroutines the test spawned, for the same reason.
func awaitWaitGroup(t *testing.T, wg *sync.WaitGroup, timeout time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("timed out after %v waiting for %s", timeout, what)
	}
}

// awaitCounter waits for a counter another goroutine moves and reports the value it last saw when
// the deadline passes — "still 0" and "still 3" name different bugs, and a bare "timed out" names
// neither.
func awaitCounter(t *testing.T, timeout time.Duration, want int, what string, probe func() int) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	for {
		got := probe()
		if got >= want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out after %v waiting for %s: last saw %d, want >= %d", timeout, what, got, want)
		case <-tick.C:
		}
	}
}

// awaitQueuePosition waits until the gate reports want entries ahead for provider url. Position is
// the queue's own count, so a test never has to guess how long enqueueing takes.
func awaitQueuePosition(t *testing.T, g *ConcurrencyGate, url string, entry *queueEntry, want int) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	for {
		got := g.Position(url, entry)
		if got == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("queue for %s reports position %d, want %d", url, got, want)
		case <-tick.C:
		}
	}
}
