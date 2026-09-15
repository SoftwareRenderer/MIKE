package lsp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"kanban/backend/extensions"

	"kanban/pkg/bridge"
	"kanban/pkg/bridge/protocol"
)

// bridgePair hands back a bridge client wired to a stand-in bridge that reads every frame and
// answers each request with an empty LSP result. Notifications are counted rather than
// answered, which is the only distinction the code under test makes.
func bridgePair(t *testing.T) (*bridge.Client, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	bridgeRead, hostWrite := io.Pipe() // the client writes, the stand-in bridge reads
	hostRead, bridgeWrite := io.Pipe() // the stand-in bridge writes, the client reads

	cl := bridge.NewClient(hostWrite, hostRead)
	var requests, notifications atomic.Int32

	go func() {
		for {
			frame, err := protocol.ReadFrame(bridgeRead)
			if err != nil {
				return
			}
			switch frame.Type {
			case protocol.TypeRequest:
				requests.Add(1)
				_ = protocol.WriteFrame(bridgeWrite, protocol.NewResult(frame.MsgID, []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)))
			case protocol.TypeNotification:
				notifications.Add(1)
			}
		}
	}()

	t.Cleanup(func() {
		_ = cl.Close()
		_ = hostWrite.Close()
		_ = bridgeWrite.Close()
	})
	return cl, &requests, &notifications
}

// goCfg is the ServerConfig a caller gets from the registry for a .go file.
func goCfg() *ServerConfig {
	return &ServerConfig{Language: "go", Command: "gopls",
		FileTypes: []extensions.LSPFileType{{Extension: "go", LanguageID: "go"}}}
}

func waitForCount(t *testing.T, counter *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if counter.Load() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("counter = %d, want %d", counter.Load(), want)
}

// TestEnsureInitializedHandshakesOnce pins that the handshake belongs to the connection. Every
// caller — an agent tool, an editor channel, the diagnostics pass — goes through
// EnsureInitialized, and everyone after the first must be answered from the flag: an initialize
// is a second of server work (gopls answers it 1.06s after a cold start) that teaches the
// server nothing it does not already know.
func TestEnsureInitializedHandshakesOnce(t *testing.T) {
	extStore := testExtensionStore(t)
	if err := extStore.Create(makeTestLSPExt("gopls", "go")); err != nil {
		t.Fatalf("Create(gopls): %v", err)
	}
	cl, requests, _ := bridgePair(t)

	m := &ExtensionLSPManager{
		extStore: extStore,
		clients:  map[string]*sandboxClient{},
		serverCh: make(chan json.RawMessage, 8),
	}
	m.clients[processKey("/work/tree", "go")] = newSandboxClient(cl, "/work/tree", "go", "gopls", nil)

	for i := 1; i <= 3; i++ {
		if err := m.EnsureInitialized(context.Background(), "/work/tree", goCfg()); err != nil {
			t.Fatalf("EnsureInitialized call %d: %v", i, err)
		}
	}

	if got := requests.Load(); got != 1 {
		t.Errorf("initialize requests = %d, want 1 for three callers on one connection", got)
	}
}

// TestEnsureInitializedConcurrentAsksOneHandshake: two turns finishing in the same instant
// must not both send initialize. initMu is what makes the flag check and the handshake one
// decision.
func TestEnsureInitializedConcurrentAsksOneHandshake(t *testing.T) {
	extStore := testExtensionStore(t)
	if err := extStore.Create(makeTestLSPExt("gopls", "go")); err != nil {
		t.Fatalf("Create(gopls): %v", err)
	}
	cl, requests, _ := bridgePair(t)
	m := &ExtensionLSPManager{extStore: extStore, clients: map[string]*sandboxClient{}, serverCh: make(chan json.RawMessage, 8)}
	m.clients[processKey("/work/tree", "go")] = newSandboxClient(cl, "/work/tree", "go", "gopls", nil)

	var failed atomic.Int32
	done := make(chan struct{})
	for i := 0; i < 6; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			if err := m.EnsureInitialized(context.Background(), "/work/tree", goCfg()); err != nil {
				failed.Add(1)
			}
		}()
	}
	for i := 0; i < 6; i++ {
		<-done
	}

	if failed.Load() != 0 {
		t.Errorf("%d concurrent EnsureInitialized calls failed", failed.Load())
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("initialize requests = %d, want 1 under concurrent callers", got)
	}
}

func TestEnsureInitializedRequiresLiveConnection(t *testing.T) {
	m := &ExtensionLSPManager{clients: map[string]*sandboxClient{}, serverCh: make(chan json.RawMessage, 1)}

	err := m.EnsureInitialized(context.Background(), "/work/tree", goCfg())
	if err == nil {
		t.Fatal("EnsureInitialized reported success with no connection to initialize")
	}
}

// TestSyncDocumentSendsOnlyWhenContentDiffers pins the rule the diagnostics pass reads as
// "no change, no hint": the first look opens the document, a repeat look sends nothing (and
// says so, so nobody waits for a notification that is not coming), and an edit re-syncs.
func TestSyncDocumentSendsOnlyWhenContentDiffers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("package a\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cl, requests, notifications := bridgePair(t)
	m := &ExtensionLSPManager{clients: map[string]*sandboxClient{}, serverCh: make(chan json.RawMessage, 8)}
	m.clients[processKey(dir, "go")] = newSandboxClient(cl, dir, "go", "gopls", nil)
	ctx := context.Background()

	uri, synced, err := m.SyncDocument(ctx, dir, "main.go", goCfg())
	if err != nil {
		t.Fatalf("first SyncDocument: %v", err)
	}
	if !synced {
		t.Error("first SyncDocument reported no sync; the server has never seen this file")
	}
	if want := "file://" + path; uri != want {
		t.Errorf("uri = %q, want %q (a caller waiting for publishDiagnostics matches on this)", uri, want)
	}
	waitForCount(t, notifications, 1)

	_, synced, err = m.SyncDocument(ctx, dir, "main.go", goCfg())
	if err != nil {
		t.Fatalf("second SyncDocument: %v", err)
	}
	if synced {
		t.Error("unchanged file reported a sync; a server that already has this text will not report it again")
	}
	if got := notifications.Load(); got != 1 {
		t.Errorf("notifications after an unchanged look = %d, want 1 (nothing new sent)", got)
	}

	if err := os.WriteFile(path, []byte("package a\n\nvar edited = true\n"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	_, synced, err = m.SyncDocument(ctx, dir, "main.go", goCfg())
	if err != nil {
		t.Fatalf("SyncDocument after edit: %v", err)
	}
	if !synced {
		t.Error("edited file reported no sync; the server would answer from stale content")
	}
	waitForCount(t, notifications, 2)

	if got := requests.Load(); got != 0 {
		t.Errorf("document sync sent %d requests, want 0 (didOpen/didChange are notifications)", got)
	}
}

func TestSyncDocumentRequiresConnection(t *testing.T) {
	m := &ExtensionLSPManager{clients: map[string]*sandboxClient{}, serverCh: make(chan json.RawMessage, 1)}

	uri, synced, err := m.SyncDocument(context.Background(), "/work/tree", "main.go", goCfg())
	if err == nil {
		t.Fatal("SyncDocument succeeded with no connection")
	}
	if uri != "" || synced {
		t.Errorf("on failure got uri=%q synced=%v, want empty/false", uri, synced)
	}
}

// TestGetOrCreateRetriesThroughADeadConnection: a connection whose bridge has gone is unusable
// for everybody, so it is retired rather than handed to the next caller. This fixture has no
// installable extension, so what is pinned is that the dead one is not left registered.
func TestGetOrCreateRetriesThroughADeadConnection(t *testing.T) {
	extStore := testExtensionStore(t) // empty: the rebuilt connection cannot be completed here
	cl, _, _ := bridgePair(t)
	m := &ExtensionLSPManager{
		extStore: extStore,
		clients:  map[string]*sandboxClient{},
		serverCh: make(chan json.RawMessage, 8),
	}
	key := processKey("/work/tree", "go")
	sc := newSandboxClient(cl, "/work/tree", "go", "gopls", nil)
	sc.refs = 1
	m.clients[key] = sc
	_ = cl.Close() // the bridge connection died (its exec went away with the sandbox)

	if _, err := m.GetOrCreate(context.Background(), "/work/tree", "go"); err == nil {
		t.Fatal("GetOrCreate succeeded — this fixture has no installable extension")
	}
	if _, ok := m.clients[key]; ok {
		t.Error("the dead connection was left registered for the next caller to trust")
	}
}

// TestUsableNeedsBothHalvesStanding: a live pipe is not enough. The bridge connection outlives
// the language server on the far end of it, and requests through a dead server all fail.
func TestUsableNeedsBothHalvesStanding(t *testing.T) {
	cl, _, _ := bridgePair(t)
	sc := newSandboxClient(cl, "/work/tree", "go", "gopls", nil)
	if !sc.usable() {
		t.Fatal("a live connection with a live session must be usable")
	}

	sc.dead.Store(true)
	if sc.usable() {
		t.Error("a connection whose language server is gone was reported usable; every request through it fails")
	}
	sc.dead.Store(false)

	_ = cl.Close()
	if sc.usable() {
		t.Error("a closed bridge connection was reported usable")
	}
	if (*sandboxClient)(nil).usable() {
		t.Error("a nil connection was reported usable")
	}
}

// TestGetOrCreateReusesTheServerItStarted: a standing connection is reused, not rebuilt — a new
// server would have to load the package and be told about the documents all over again.
func TestGetOrCreateReusesTheServerItStarted(t *testing.T) {
	extStore := testExtensionStore(t)
	cl, _, _ := bridgePair(t)
	m := &ExtensionLSPManager{
		extStore: extStore,
		clients:  map[string]*sandboxClient{},
		serverCh: make(chan json.RawMessage, 8),
	}
	key := processKey("/work/tree", "go")
	sc := newSandboxClient(cl, "/work/tree", "go", "gopls", nil)
	sc.refs = 1
	m.clients[key] = sc

	if _, err := m.GetOrCreate(context.Background(), "/work/tree", "go"); err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if m.clients[key] != sc {
		t.Error("the live connection was replaced even though it is still standing")
	}
	if got := m.clients[key].refs; got != 2 {
		t.Errorf("refs = %d, want 2 — the new caller holds it as well", got)
	}
}
