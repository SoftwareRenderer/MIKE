package lsp

import (
	"testing"

	"mike/backend/extensions"

	"github.com/coder/websocket"

	"mike/pkg/sandboxruntime"
)

// TestReleaseHandsTheConnectionBackOnlyToTheLastHolder guards the rule that keeps a turn's
// diagnostics pass from cutting the editor's language server out from under it: there is one
// connection per (workspace, language), every holder that can outlive a call takes a reference,
// and only the last one to hand it back gets to destroy it.
//
// GetOrCreate does the counting on the live path; setting refs directly here stands in for the
// extension sandbox a real GetOrCreate would have to start.
func TestReleaseHandsTheConnectionBackOnlyToTheLastHolder(t *testing.T) {
	m := &ExtensionLSPManager{clients: map[string]*sandboxClient{}}
	key := processKey("/work/tree", "go")
	m.clients[key] = newSandboxClient(nil, "/work/tree", "go", "gopls", nil)

	m.mu.Lock()
	m.clients[key].refs = 2 // an editor WebSocket and one diagnostics pass
	m.mu.Unlock()

	m.Release("/work/tree", "go")
	if _, ok := m.clients[key]; !ok {
		t.Fatal("Release destroyed a connection another holder was still reading")
	}

	m.Release("/work/tree", "go")
	if _, ok := m.clients[key]; ok {
		t.Error("the last holder's Release left the connection in place: nobody uses it, and the extension sandbox is owed back")
	}
}

// TestReleaseWithoutASandboxManager: a manager built without an extension sandbox manager still
// drops the connection instead of panicking on the hand-back.
func TestReleaseWithoutASandboxManager(t *testing.T) {
	m := &ExtensionLSPManager{clients: map[string]*sandboxClient{}}
	key := processKey("/work/tree", "go")
	m.clients[key] = newSandboxClient(nil, "/work/tree", "go", "gopls", nil)

	m.Release("/work/tree", "go")

	if _, ok := m.clients[key]; ok {
		t.Error("the last reference went unreleased")
	}
}

func TestReleaseOfUnknownWorkspaceIsHarmless(t *testing.T) {
	m := &ExtensionLSPManager{clients: map[string]*sandboxClient{}}

	m.Release("/never/attached", "go") // must not panic on the missing entry

	if len(m.clients) != 0 {
		t.Errorf("clients = %v, want none", m.clients)
	}
}

// TestSessionsUsingLockedCountsSessionsNotChannels backs the editor's release decision. A
// session that subscribed three channels to one workspace holds it once, so a count of
// subscribes is a number nobody can ever get back to zero and the language server is never
// handed back.
func TestSessionsUsingLockedCountsSessionsNotChannels(t *testing.T) {
	const key = "some workspace key"
	h := &LSPHandler{
		clients:    map[*websocket.Conn]*ClientSession{},
		workspaces: map[string]*workspaceState{},
	}
	h.clients[&websocket.Conn{}] = newSessionUsing(key, key, key) // three channels of one editor
	h.clients[&websocket.Conn{}] = newSessionUsing(key)
	h.clients[&websocket.Conn{}] = newSessionUsing(processKey("/other", "go"))

	if got := h.sessionsUsingLocked(key); got != 2 {
		t.Errorf("sessions using the workspace = %d, want 2 (channels a session subscribed do not add holders)", got)
	}
	if got := h.sessionsUsingLocked(processKey("/untouched", "typescript")); got != 0 {
		t.Errorf("sessions using an untouched workspace = %d, want 0", got)
	}
}

func newSessionUsing(workspaceKeys ...string) *ClientSession {
	s := newClientSession(&websocket.Conn{}, 0)
	for _, key := range workspaceKeys {
		s.uses(key)
	}
	return s
}

// TestRetireDropsADeadConnectionOnce: a connection that can no longer be used leaves the map,
// and only once. The second retire is what a stale SendToServer does after a Release already
// removed the client — handing the sandbox reference back twice would stop a container that
// another workspace is using.
func TestRetireDropsADeadConnectionOnce(t *testing.T) {
	sm := extensions.NewExtensionSandboxManager(nil, nil, sandboxruntime.Boot{})
	m := &ExtensionLSPManager{clients: map[string]*sandboxClient{}, extSandbox: sm}
	key := processKey("/work/tree", "go")
	m.clients[key] = newSandboxClient(nil, "/work/tree", "go", "gopls", nil)

	m.retire(key, m.clients[key])
	if _, ok := m.clients[key]; ok {
		t.Fatal("retire left a dead connection in the map")
	}

	m.retire(key, newSandboxClient(nil, "/work/tree", "go", "gopls", nil))
	if running := sm.ListRunning(); len(running) != 0 {
		t.Errorf("running sandboxes = %v, want none started by a retire", running)
	}
}

// TestRetireLeavesTheConnectionThatTookItsPlaceAlone: when two attaches race a cold workspace,
// the one that loses must not delete the connection that won. The identity check is what makes
// that true, and it is also what keeps the winner's sandbox reference intact.
func TestRetireLeavesTheConnectionThatTookItsPlaceAlone(t *testing.T) {
	m := &ExtensionLSPManager{clients: map[string]*sandboxClient{}}
	key := processKey("/work/tree", "go")
	installed := newSandboxClient(nil, "/work/tree", "go", "gopls", nil)
	m.clients[key] = installed

	m.retire(key, newSandboxClient(nil, "/work/tree", "go", "gopls", nil))

	if m.clients[key] != installed {
		t.Error("retiring an orphaned connection removed the live one that had taken its place")
	}
}
