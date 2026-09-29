package bridge

import (
	"testing"
	"time"
)

// awaitTrue waits for a condition another goroutine is responsible for, and says so when it does
// not arrive. It exists so a test never sleeps its way past the event it is waiting for: the poll
// interval is how often it re-looks, not how long it waits.
func awaitTrue(t *testing.T, what string, timeout time.Duration, probe func() bool) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	for {
		if probe() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out after %v waiting for %s", timeout, what)
		case <-tick.C:
		}
	}
}
