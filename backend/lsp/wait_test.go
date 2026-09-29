package lsp

import (
	"testing"
	"time"
)

// awaitInt waits for a count one of the handler's own goroutines moves, and reports the value it
// last saw: "still 0" and "still 3" are different bugs and need different sentences.
func awaitInt(t *testing.T, want int, what string, probe func() int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	for {
		got := probe()
		if got == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s: last saw %d, want %d", what, got, want)
		case <-tick.C:
		}
	}
}

// queuedWrites counts the messages waiting their turn on one workspace's writer. A send blocked in
// the process manager has already left the channel, so what is in here is exactly what the writer
// has not been handed yet.
func queuedWrites(t *testing.T, h *LSPHandler, workspaceKey string) int {
	t.Helper()
	h.mu.Lock()
	ws := h.workspaces[workspaceKey]
	h.mu.Unlock()
	if ws == nil {
		t.Fatalf("no workspace entry for %s", workspaceKey)
	}
	return len(ws.writes)
}
