package sandbox

import (
	"testing"
	"time"

	"kanban/pkg/sandboxruntime"
)

// TestPodmanCommandBoundsTheWait checks the bound is set, not that it works: proving that needs a
// container that stops answering, which nothing here can wedge on purpose.
func TestPodmanCommandBoundsTheWait(t *testing.T) {
	c := podmanCommand("--version")
	if c.Args[0] != "podman" {
		t.Errorf("podmanCommand argv[0] = %q, want podman", c.Args[0])
	}
	if c.WaitDelay != sandboxruntime.PodmanWaitDelay {
		t.Errorf("WaitDelay = %v, want %v", c.WaitDelay, sandboxruntime.PodmanWaitDelay)
	}
	if c.WaitDelay <= 0 || c.WaitDelay >= 15*time.Second {
		t.Errorf("WaitDelay = %v, want a positive bound inside the 15s shutdown budget", c.WaitDelay)
	}
}
