package extensions

import (
	"io"
	"os/exec"
	"testing"

	sandbox "kanban/pkg/bridge"
)

// testBridgeClient builds a bridge client over a pair of pipes. Nothing is spoken over them.
func testBridgeClient(t *testing.T) *sandbox.Client {
	t.Helper()
	read, write := io.Pipe()
	client := sandbox.NewClient(write, read)
	t.Cleanup(func() {
		_ = write.Close()
		_ = read.Close()
	})
	return client
}

// registerSession records a session the way StartSession would, minus the `podman exec`: these
// tests have no container, only the bookkeeping.
func registerSession(t *testing.T, sm *ExtensionSandboxManager, extID string, clients ...*sandbox.Client) {
	t.Helper()
	sm.mu.Lock()
	defer sm.mu.Unlock()
	state := sm.states[extID]
	if state == nil {
		state = &extensionSandboxState{containerName: "kanban-ext-" + extID, isAlive: true}
		sm.states[extID] = state
	}
	if state.sessions == nil {
		state.sessions = make(map[*sandbox.Client]*exec.Cmd)
	}
	for _, c := range clients {
		state.sessions[c] = nil // no process to reap
		sm.refs[extID]++
	}
}

// TestRefCountFollowsConnections: one reference per bridge connection — taken when it is
// registered, handed back one at a time as they are released.
func TestRefCountFollowsConnections(t *testing.T) {
	sm := NewExtensionSandboxManager(nil, nil)
	const extID = "gopls"
	first, second := testBridgeClient(t), testBridgeClient(t)
	registerSession(t, sm, extID, first, second)

	if got := sm.refs[extID]; got != 2 {
		t.Fatalf("refcount with two sessions = %d, want 2 — one reference per connection", got)
	}

	if !sm.ReleaseSession(extID, first) {
		t.Fatal("ReleaseSession refused a session it had registered")
	}
	if got := sm.refs[extID]; got != 1 {
		t.Fatalf("refcount after one hand-back = %d, want 1 — the sandbox is still in use", got)
	}
	cancelGrace(t, sm, extID)

	if !sm.ReleaseSession(extID, second) {
		t.Fatal("ReleaseSession refused the last session")
	}
	if got := sm.refs[extID]; got != 0 {
		t.Errorf("refcount after the last hand-back = %d, want 0", got)
	}
	cancelGrace(t, sm, extID)
}

// TestReleaseSessionCreditsOnlyRegisteredSessions: a connection whose sandbox was stopped and
// recreated behind its back owns no reference. Handing one back would stop the replacement
// sandbox, which somebody else is using.
func TestReleaseSessionCreditsOnlyRegisteredSessions(t *testing.T) {
	sm := NewExtensionSandboxManager(nil, nil)
	const extID = "gopls"
	first, second := testBridgeClient(t), testBridgeClient(t)

	if sm.ReleaseSession(extID, first) {
		t.Error("a session was credited with no sandbox registered at all")
	}
	if sm.ReleaseSession(extID, nil) {
		t.Error("a nil bridge was credited as a session")
	}

	registerSession(t, sm, extID, first)

	// What stopLocked + Create does: sessions and refcount are discarded, and the replacement
	// sandbox starts with a count of its own.
	sm.mu.Lock()
	delete(sm.refs, extID)
	delete(sm.states, extID)
	sm.mu.Unlock()
	registerSession(t, sm, extID, second)

	if sm.ReleaseSession(extID, first) {
		t.Error("an orphan of a stopped sandbox is still credited: releasing through it would stop the sandbox that replaced it")
	}
	if got := sm.refs[extID]; got != 1 {
		t.Errorf("the replacement sandbox's refcount = %d after an orphan's hand-back, want 1", got)
	}
	if !sm.ReleaseSession(extID, second) {
		t.Error("a session of the sandbox that is actually running is not credited")
	}
	cancelGrace(t, sm, extID)
}

// cancelGrace calls off a scheduled stop, so a test timer never reaches for a container that
// was never created.
func cancelGrace(t *testing.T, sm *ExtensionSandboxManager, extID string) {
	t.Helper()
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if state := sm.states[extID]; state != nil && state.stopTimer != nil {
		state.stopTimer.Stop()
		state.stopTimer = nil
	}
}
