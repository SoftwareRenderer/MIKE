package agent

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mike/backend/extensions"
	"mike/pkg/agent"
)

// What this file owns is the host side of the WASM boundary: the production loader finding and starting
// the module, and every exported call surviving the wazero allocation pattern (Alloc → Write → Call →
// Free) with the store_result callback handing the answer back. What the module says — its JSONL, its
// tool names, its event shapes — is the Pi adapter's own contract and is tested natively in
// ../MIKE-extensions/agent/pi/adapter_test.go, which runs the same adapter code without the host under
// it. Nothing here reads a protocol field: this package knows no agent protocols, and a test that named
// one would make that untrue.

// withProjectRoot changes the working directory to a scratch directory holding the built module under
// its bare name, which is the last candidate resolveWasmPath tries. It restores the original directory
// on test cleanup.
func withProjectRoot(t *testing.T) {
	t.Helper()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	module := filepath.Join(oldWd, "..", "..", "..", "MIKE-extensions", "agent", "pi", "pi.wasm")
	if _, err := os.Stat(module); err != nil {
		t.Skipf("no pi.wasm at %s — build it with ../MIKE-extensions/agent/pi/build.sh", module)
	}
	dir := t.TempDir()
	if err := os.Symlink(module, filepath.Join(dir, "pi.wasm")); err != nil {
		t.Fatalf("link the built module into the scratch root: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(oldWd) })
}

// loadPiWasm loads the pi.wasm module for integration tests. It changes
// to a scratch root holding the module the sibling checkout built, so the
// production loader resolves it by its bare name. The adapter is
// automatically closed on test cleanup.
func loadPiWasm(t *testing.T) agent.AgentAdapter {
	t.Helper()
	withProjectRoot(t)
	ext := &extensions.Extension{
		Name:   "pi",
		Author: "getmike.dev",
		Type:   extensions.ExtensionTypeAgent,
		Manifest: extensions.ExtensionManifest{
			Version: "1.0.0",
			Capabilities: &extensions.Capabilities{
				Agent: &extensions.AgentConfig{
					WasmModule: "pi.wasm",
				},
			},
		},
	}
	// A module nobody built is a missing prerequisite: skip, and say how to build it. A module that is
	// on disk but will not load is a broken build, which must fail.
	adapter, err := loadWasmAdapter(ext)
	if err != nil {
		t.Fatalf("loadWasmAdapter with a module on disk: %v", err)
	}
	t.Cleanup(func() { adapter.Close() })
	return adapter
}

// initAdapter runs the adapter's Init over the RunnerConfig JSON, which is the first call the host makes
// and the one that exercises _initialize plus a string written into guest memory.
func initAdapter(t *testing.T, adapter agent.AgentAdapter, session string) {
	t.Helper()
	cfg := RunnerConfig{
		ExtensionName: "pi",
		SessionName:   session,
		WorktreePath:  t.TempDir(),
		Config:        map[string]any{},
	}
	cfgBytes, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal the runner config: %v", err)
	}
	if err := adapter.Init(context.Background(), cfgBytes); err != nil {
		t.Fatalf("Init: %v", err)
	}
}

// TestWasmPiAdapter_EveryExportedCallRoundTripsThroughTheHost loads the module through the production
// loader and calls each exported entry point once. A guest that cannot be allocated, called or read back
// fails here on every endpoint at once, whatever the protocol says.
func TestWasmPiAdapter_EveryExportedCallRoundTripsThroughTheHost(t *testing.T) {
	adapter := loadPiWasm(t)
	ctx := context.Background()
	initAdapter(t, adapter, "host-round-trip")

	// SubprocessConfig: the sole source of the executable's name, read back across the boundary.
	if binary := adapter.SubprocessConfig(ctx).Binary; binary == "" {
		t.Error("SubprocessConfig answered no binary: the host read guest memory wrongly, or the module never initialized")
	}

	// EncodeCommand: a string in, bytes out.
	encoded := adapter.EncodeCommand(ctx, agent.CommandPrompt, nil)
	if len(encoded) == 0 {
		t.Error("EncodeCommand returned no bytes")
	}

	// ParseOutput: bytes in, events out — the answer arrives through the store_result callback, so an
	// event here is the only proof that callback still fires, and its text coming back whole is the proof
	// that the bytes were read out of guest memory as they went in.
	const delta = "hello from the host round trip"
	events := adapter.ParseOutput(ctx, []byte(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"`+delta+`"}}
`))
	if len(events) == 0 {
		t.Fatal("ParseOutput answered no event: store_result never reached the host")
	}
	if events[0].Text != delta {
		t.Errorf("the text came back altered: got %q, want %q (%+v)", events[0].Text, delta, events[0])
	}

	// HandleUIRequest: the id is what must come back, because it is what the browser is waiting on.
	const id = "req-123"
	resp := adapter.HandleUIRequest(ctx, id, "confirm_action")
	if !strings.Contains(string(resp), id) {
		t.Errorf("HandleUIRequest(%q) = %s, want the id it was handed", id, resp)
	}
}

// TestWasmPiAdapter_ConcurrentAccess verifies that the wasmAdapter mutex
// prevents concurrent calls from corrupting the WASM module state.
// This simulates the race between readOutput's ParseOutput and Prompt's
// EncodeCommand that caused unsafe.Slice: len out of range crashes.
func TestWasmPiAdapter_ConcurrentAccess(t *testing.T) {
	adapter := loadPiWasm(t)

	initAdapter(t, adapter, "conc-test")

	ctx := context.Background()
	var wg sync.WaitGroup
	errors := make(chan error, 20)

	// Simulate concurrent ParseOutput (from readOutput goroutine) and
	// EncodeCommand (from Prompt goroutine) calls
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			data := []byte(fmt.Sprintf(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"turn-%d"}}`, i) + "\n")
			events := adapter.ParseOutput(ctx, data)
			if len(events) != 1 {
				errors <- fmt.Errorf("goroutine %d: ParseOutput got %d events, want 1", i, len(events))
				return
			}
			if events[0].Text != fmt.Sprintf("turn-%d", i) {
				errors <- fmt.Errorf("goroutine %d: text = %q, want %q", i, events[0].Text, fmt.Sprintf("turn-%d", i))
				return
			}
		}(i)
	}

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := adapter.EncodeCommand(ctx, agent.CommandPrompt, nil)
			if len(cmd) == 0 {
				errors <- fmt.Errorf("goroutine %d: EncodeCommand returned empty", i)
				return
			}
		}(i)
	}

	// Also interleave HandleUIRequest and SubprocessConfig calls
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			adapter.HandleUIRequest(ctx, fmt.Sprintf("req-%d", i), "confirm")
			adapter.SubprocessConfig(ctx)
		}(i)
	}

	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}
