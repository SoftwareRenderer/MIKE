package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kanban/backend/extensions"
	"kanban/backend/store"
	"kanban/backend/types"
	sandbox "kanban/pkg/bridge"
	"kanban/pkg/bridge/protocol"
)

func testExtensionStore(t *testing.T) *extensions.ExtensionStore {
	t.Helper()
	dir := t.TempDir()
	db, err := store.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	es := extensions.NewExtensionStore(db)
	if err := es.InitSchema(); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	return es
}

func makeTestLSPExt(name, lang string) *extensions.Extension {
	// Map language to common file extensions
	extMap := map[string][]string{
		"go":         {"go"},
		"typescript": {"ts", "tsx"},
		"python":     {"py"},
	}
	extns := extMap[lang]
	if len(extns) == 0 {
		extns = []string{lang}
	}

	return &extensions.Extension{
		ID:      name,
		Name:    name,
		Version: "1.0.0",
		Type:    extensions.ExtensionTypeLSP,
		Manifest: extensions.ExtensionManifest{
			DisplayName: name,
			Version:     "1.0.0",
			Capabilities: &extensions.Capabilities{
				LSP: &extensions.LSPConfig{
					Command:    lang + "pls",
					Args:       []string{"-mode=stdio"},
					Extensions: extns,
					Language:   lang,
				},
			},
		},
		Enabled: true,
	}
}

func TestExtensionRegistry_Lookup(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	// Create test extensions
	for _, lang := range []string{"go", "typescript", "python"} {
		ext := makeTestLSPExt(lang, lang)
		if err := store.Create(ext); err != nil {
			t.Fatalf("Create(%s): %v", lang, err)
		}
	}

	reg := NewExtensionRegistry(store)

	// Test lookup by file extension
	cfg := reg.Lookup("main.go")
	if cfg == nil {
		t.Fatal("Lookup(main.go) = nil, want config")
	}
	if cfg.Language != "go" {
		t.Errorf("Language = %q, want go", cfg.Language)
	}
	if cfg.Command != "gopls" {
		t.Errorf("Command = %q, want gopls", cfg.Command)
	}

	// Test lookup by .ts extension
	cfg = reg.Lookup("app.ts")
	if cfg == nil {
		t.Fatal("Lookup(app.ts) = nil, want config")
	}
	if cfg.Language != "typescript" {
		t.Errorf("Language = %q, want typescript", cfg.Language)
	}

	// Test unknown extension
	cfg = reg.Lookup("unknown.xyz")
	if cfg != nil {
		t.Errorf("Lookup(unknown.xyz) = %+v, want nil", cfg)
	}
}

func TestExtensionRegistry_Lookup_NoExtensions(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	reg := NewExtensionRegistry(store)
	cfg := reg.Lookup("main.go")
	if cfg != nil {
		t.Errorf("Lookup(main.go) = %+v, want nil (no extensions)", cfg)
	}
}

func TestExtensionRegistry_Lookup_ExtensionlessPath(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := store.Create(makeTestLSPExt("gopls", "go")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	reg := NewExtensionRegistry(store)
	// Extensionless paths (workspace directories, Makefile-style files) must
	// not panic (previously panicked slicing the empty extension) and must
	// simply not match.
	if cfg := reg.Lookup("/tmp/workspace"); cfg != nil {
		t.Errorf("Lookup(\"/tmp/workspace\") = %+v, want nil", cfg)
	}
	if cfg := reg.Lookup("/tmp/workspace/Makefile"); cfg != nil {
		t.Errorf("Lookup(\"/tmp/workspace/Makefile\") = %+v, want nil", cfg)
	}
	// Extension matching still works after the fix.
	if cfg := reg.Lookup("/tmp/workspace/main.go"); cfg == nil || cfg.Language != "go" {
		t.Errorf("Lookup(main.go) = %+v, want go config", cfg)
	}
}

func TestLookupExtensionByFile_Extensionless(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := store.Create(makeTestLSPExt("gopls", "go")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	reg := extensions.NewRegistryClient()
	sandbox := extensions.NewExtensionSandboxManager(reg, store)
	mgr := NewExtensionLSPManager(store, sandbox, nil)

	// Must return an error, not panic, for an extensionless file.
	if _, err := mgr.lookupExtensionByFile("/tmp/workspace/Makefile"); err == nil {
		t.Error("expected error for extensionless file")
	}
}

func TestExtensionRegistry_Lookup_Disabled(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	ext := makeTestLSPExt("go", "go")
	ext.Enabled = false
	if err := store.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	reg := NewExtensionRegistry(store)
	cfg := reg.Lookup("main.go")
	if cfg != nil {
		t.Errorf("Lookup(main.go) = %+v, want nil (disabled)", cfg)
	}
}

func TestExtensionRegistry_GetConfig(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	ext := makeTestLSPExt("go", "go")
	ext.Manifest.Capabilities.GetLSP().InitParamsOverride = map[string]any{
		"hover": map[string]any{"actions": map[string]any{"enabled": true}},
	}
	if err := store.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	reg := NewExtensionRegistry(store)
	cfg := reg.GetConfig("go")
	if cfg == nil {
		t.Fatal("GetConfig(go) = nil, want config")
	}
	if cfg.Language != "go" {
		t.Errorf("Language = %q, want go", cfg.Language)
	}
	if cfg.InitParamsOverride == nil {
		t.Error("InitParamsOverride = nil, want non-nil")
	}
}

func TestExtensionRegistry_GetConfig_NotFound(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	reg := NewExtensionRegistry(store)
	cfg := reg.GetConfig("unknown")
	if cfg != nil {
		t.Errorf("GetConfig(unknown) = %+v, want nil", cfg)
	}
}

func TestExtensionRegistry_NoLSPConfig(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	// Create extension without LSP config
	ext := &extensions.Extension{
		ID:      "no-lsp",
		Name:    "no-lsp",
		Version: "1.0.0",
		Type:    extensions.ExtensionTypeLSP,
		Manifest: extensions.ExtensionManifest{
			DisplayName: "No LSP",
			Version:     "1.0.0",
			Capabilities: &extensions.Capabilities{
				LSP: nil, // No LSP config
			},
		},
		Enabled: true,
	}
	if err := store.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	reg := NewExtensionRegistry(store)
	cfg := reg.Lookup("main.go")
	if cfg != nil {
		t.Errorf("Lookup(main.go) = %+v, want nil (no LSP config)", cfg)
	}
}

func TestToServerConfig_NonTypescriptNoOverride(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	// Create a non-TypeScript extension
	ext := makeTestLSPExt("gopls", "go")
	ext.Manifest.Capabilities.GetLSP().InitParamsOverride = map[string]any{
		"staticcheck": true,
	}
	if err := store.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	reg := NewExtensionRegistry(store)
	cfg := reg.Lookup("main.go")
	if cfg == nil {
		t.Fatal("Lookup(main.go) = nil, want config")
	}

	// InitParamsOverride should pass through unchanged from the manifest.
	if cfg.InitParamsOverride == nil {
		t.Fatal("InitParamsOverride = nil")
	}
	if cfg.InitParamsOverride["staticcheck"] != true {
		t.Error("original config not preserved")
	}
}

func TestNewExtensionLSPManager(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	reg := extensions.NewRegistryClient()
	sandbox := extensions.NewExtensionSandboxManager(reg, store)
	mgr := NewExtensionLSPManager(store, sandbox, nil)

	if mgr == nil {
		t.Fatal("NewExtensionLSPManager() = nil")
	}
	if mgr.serverCh == nil {
		t.Error("serverCh is nil")
	}
}

func TestExtensionLSPManager_GetOrCreate_NoExtension(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	reg := extensions.NewRegistryClient()
	sandbox := extensions.NewExtensionSandboxManager(reg, store)
	mgr := NewExtensionLSPManager(store, sandbox, nil)

	_, err := mgr.GetOrCreate(context.Background(), "/tmp/workspace", "go", nil)
	if err == nil {
		t.Error("GetOrCreate() = nil, want error (no extension)")
	}
}

// TestExtensionLSPManager_GetOrCreate_ContextCancelled: a cancelled caller must not go
// on creating sandboxes and starting servers for a caller that already left.
func TestExtensionLSPManager_GetOrCreate_ContextCancelled(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	// Installed, so only the cancelled context can fail this call.
	ext := makeTestLSPExt("typescript", "typescript")
	if err := store.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}
	reg := extensions.NewRegistryClient()
	sandbox := extensions.NewExtensionSandboxManager(reg, store)
	mgr := NewExtensionLSPManager(store, sandbox, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() {
		_, err := mgr.GetOrCreate(ctx, "/tmp/workspace", "typescript", nil)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("GetOrCreate() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GetOrCreate ignored a cancelled context")
	}
}

func TestExtensionLSPManager_Release_NoClient(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	reg := extensions.NewRegistryClient()
	sandbox := extensions.NewExtensionSandboxManager(reg, store)
	mgr := NewExtensionLSPManager(store, sandbox, nil)

	// Should not panic
	mgr.Release("/tmp/workspace", "go")
}

func TestExtensionLSPManager_ServerMessageCh(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	reg := extensions.NewRegistryClient()
	sandbox := extensions.NewExtensionSandboxManager(reg, store)
	mgr := NewExtensionLSPManager(store, sandbox, nil)

	ch := mgr.ServerMessageCh()
	if ch == nil {
		t.Error("ServerMessageCh() = nil")
	}
}

func TestExtensionLSPManager_SubscribeAndUnsubscribe(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	reg := extensions.NewRegistryClient()
	sandbox := extensions.NewExtensionSandboxManager(reg, store)
	mgr := NewExtensionLSPManager(store, sandbox, nil)

	// Subscribe two clients
	sub1 := mgr.Subscribe()
	sub2 := mgr.Subscribe()

	// Simulate a server message being broadcast (normally from SendToServer or notification forwarding)
	msg := json.RawMessage(`{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics","params":{"uri":"file:///tmp/test.go","diagnostics":[]}}`)

	// Directly invoke broadcastToSubscribers (simulating what SendToServer does)
	mgr.broadcastToSubscribers(msg)

	// Both subscribers should receive the message
	for i, ch := range []chan json.RawMessage{sub1, sub2} {
		select {
		case received := <-ch:
			if string(received) != string(msg) {
				t.Errorf("subscriber %d: got %s, want %s", i+1, received, msg)
			}
		case <-time.After(1 * time.Second):
			t.Fatalf("subscriber %d: did not receive message", i+1)
		}
	}

	// Unsubscribe sub1
	mgr.Unsubscribe(sub1)

	// Broadcast another message
	msg2 := json.RawMessage(`{"jsonrpc":"2.0","method":"window/logMessage","params":{"type":1,"message":"test"}}`)
	mgr.broadcastToSubscribers(msg2)

	// sub1 should NOT receive it (channel closed)
	select {
	case _, ok := <-sub1:
		if ok {
			t.Error("unsubscribed subscriber received a message")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("unsubscribed channel should have been closed")
	}

	// sub2 should receive it
	select {
	case received := <-sub2:
		if string(received) != string(msg2) {
			t.Errorf("subscriber 2: got %s, want %s", received, msg2)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("subscriber 2: did not receive message")
	}

	mgr.Unsubscribe(sub2)
}

func TestParsePublishDiagnostics(t *testing.T) {
	// Simulates the JSON parsing logic used in triggerLSPDiagnostics
	raw := json.RawMessage(`{
		"jsonrpc": "2.0",
		"method": "textDocument/publishDiagnostics",
		"params": {
			"uri": "file:///tmp/main.go",
			"diagnostics": [
				{
					"range": {"start": {"line": 5}, "end": {"line": 5}},
					"severity": 1,
					"message": "undefined: foo"
				},
				{
					"range": {"start": {"line": 10}, "end": {"line": 10}},
					"severity": 2,
					"message": "unused variable"
				},
				{
					"range": {"start": {"line": 15}, "end": {"line": 15}},
					"severity": 1,
					"message": "syntax error"
				}
			]
		}
	}`)

	var envelope struct {
		Method string `json:"method"`
		Params struct {
			Uri         string `json:"uri"`
			Diagnostics []struct {
				Range struct {
					Start struct {
						Line int `json:"line"`
					} `json:"start"`
				} `json:"range"`
				Message  string `json:"message"`
				Severity int    `json:"severity"`
			} `json:"diagnostics"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if envelope.Method != "textDocument/publishDiagnostics" {
		t.Errorf("method = %q, want %q", envelope.Method, "textDocument/publishDiagnostics")
	}
	if envelope.Params.Uri != "file:///tmp/main.go" {
		t.Errorf("uri = %q, want %q", envelope.Params.Uri, "file:///tmp/main.go")
	}

	// Filter for errors (severity <= 1)
	var errors []string
	for _, d := range envelope.Params.Diagnostics {
		if d.Severity <= 1 {
			errors = append(errors, fmt.Sprintf("Line %d: %s", d.Range.Start.Line+1, d.Message))
		}
	}

	if len(errors) != 2 {
		t.Fatalf("got %d errors, want 2", len(errors))
	}
	if errors[0] != "Line 6: undefined: foo" {
		t.Errorf("errors[0] = %q, want %q", errors[0], "Line 6: undefined: foo")
	}
	if errors[1] != "Line 16: syntax error" {
		t.Errorf("errors[1] = %q, want %q", errors[1], "Line 16: syntax error")
	}
}

func TestHasAnyLSPToolCallsExtension(t *testing.T) {
	store := testExtensionStore(t)
	reg := NewExtensionRegistry(store)

	// No extensions — should return false
	if reg.HasAnyLSPToolCallsExtension() {
		t.Error("HasAnyLSPToolCallsExtension() = true, want false (no extensions)")
	}

	// Add an enabled LSP extension (tool calls default to enabled)
	ext := makeTestLSPExt("gopls", "go")
	if err := store.Create(ext); err != nil {
		t.Fatalf("create extension: %v", err)
	}

	if !reg.HasAnyLSPToolCallsExtension() {
		t.Error("HasAnyLSPToolCallsExtension() = false, want true (gopls enabled, tool calls default true)")
	}

	// Disable tool calls
	falseVal := false
	ext.Manifest.Capabilities.LSP.ToolCallsEnabled = &falseVal
	if err := store.Update(ext.ID, ext); err != nil {
		t.Fatalf("update extension: %v", err)
	}

	if reg.HasAnyLSPToolCallsExtension() {
		t.Error("HasAnyLSPToolCallsExtension() = true, want false (tool calls disabled)")
	}

	// Re-enable tool calls
	trueVal := true
	ext.Manifest.Capabilities.LSP.ToolCallsEnabled = &trueVal
	if err := store.Update(ext.ID, ext); err != nil {
		t.Fatalf("update extension: %v", err)
	}

	if !reg.HasAnyLSPToolCallsExtension() {
		t.Error("HasAnyLSPToolCallsExtension() = false, want true (tool calls re-enabled)")
	}
}

func TestHasAnyLSPToolCallsExtension_MultipleExtensions(t *testing.T) {
	store := testExtensionStore(t)
	reg := NewExtensionRegistry(store)

	// Add two LSP extensions — one with tool calls disabled, one enabled
	ext1 := makeTestLSPExt("gopls", "go")
	falseVal := false
	ext1.Manifest.Capabilities.LSP.ToolCallsEnabled = &falseVal
	if err := store.Create(ext1); err != nil {
		t.Fatalf("create extension: %v", err)
	}

	ext2 := makeTestLSPExt("typescript", "typescript")
	// ToolCallsEnabled is nil (defaults to true)
	if err := store.Create(ext2); err != nil {
		t.Fatalf("create extension: %v", err)
	}

	// Should return true because ext2 has tool calls enabled (default)
	if !reg.HasAnyLSPToolCallsExtension() {
		t.Error("HasAnyLSPToolCallsExtension() = false, want true (ext2 has tool calls enabled)")
	}

	// Disable tool calls on ext2 as well
	ext2.Manifest.Capabilities.LSP.ToolCallsEnabled = &falseVal
	if err := store.Update(ext2.ID, ext2); err != nil {
		t.Fatalf("update extension: %v", err)
	}

	// Now both have tool calls disabled — should return false
	if reg.HasAnyLSPToolCallsExtension() {
		t.Error("HasAnyLSPToolCallsExtension() = true, want false (all tool calls disabled)")
	}
}

func TestIsToolCallsEnabledForFile(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	// Create two LSP extensions: Go (tool calls disabled) and TypeScript (enabled)
	goExt := makeTestLSPExt("gopls", "go")
	falseVal := false
	goExt.Manifest.Capabilities.LSP.ToolCallsEnabled = &falseVal
	if err := store.Create(goExt); err != nil {
		t.Fatalf("create gopls: %v", err)
	}

	tsExt := makeTestLSPExt("typescript", "typescript")
	// ToolCallsEnabled is nil (defaults to true)
	if err := store.Create(tsExt); err != nil {
		t.Fatalf("create typescript: %v", err)
	}

	mgr := NewExtensionLSPManager(store, nil, nil)

	// Go file — tool calls disabled
	if mgr.IsToolCallsEnabledForFile("main.go") {
		t.Error("IsToolCallsEnabledForFile(main.go) = true, want false (gopls tool calls disabled)")
	}

	// TypeScript file — tool calls enabled
	if !mgr.IsToolCallsEnabledForFile("app.ts") {
		t.Error("IsToolCallsEnabledForFile(app.ts) = false, want true (typescript tool calls enabled)")
	}

	// Unknown file type — no LSP extension
	if mgr.IsToolCallsEnabledForFile("README.md") {
		t.Error("IsToolCallsEnabledForFile(README.md) = true, want false (no LSP extension)")
	}
}

func TestResolveWorkspaceMounts(t *testing.T) {
	projectMounts := map[string]string{
		"/home/user/projects/kanban/worktrees/MIKE/steel-prism-crypt": "/mnt/steel-prism-crypt",
	}
	wsHost := "/home/user/projects/kanban/worktrees/MIKE/steel-prism-crypt"

	mounts := []types.SandboxMount{
		{
			HostPath:      "/host/share/node_modules",
			ContainerPath: "{workspace}/frontend/node_modules",
			Mode:          "ro",
		},
	}
	resolved := resolveWorkspaceMounts(mounts, wsHost, projectMounts)

	if len(resolved) != 1 {
		t.Fatalf("resolveWorkspaceMounts returned %d mounts, want 1", len(resolved))
	}
	// {workspace} is replaced with the container path of the opened workspace.
	if resolved[0].ContainerPath != "/mnt/steel-prism-crypt/frontend/node_modules" {
		t.Errorf("ContainerPath = %q, want %q", resolved[0].ContainerPath, "/mnt/steel-prism-crypt/frontend/node_modules")
	}
	// Non-placeholder fields pass through unchanged.
	if resolved[0].HostPath != "/host/share/node_modules" {
		t.Errorf("HostPath = %q, want %q", resolved[0].HostPath, "/host/share/node_modules")
	}
	if resolved[0].Mode != "ro" {
		t.Errorf("Mode = %q, want ro", resolved[0].Mode)
	}

	// No mounts -> no-op (nil).
	if got := resolveWorkspaceMounts(nil, wsHost, projectMounts); got != nil {
		t.Errorf("resolveWorkspaceMounts(nil) = %v, want nil", got)
	}

	// The original slice is not mutated.
	if mounts[0].ContainerPath != "{workspace}/frontend/node_modules" {
		t.Errorf("original mount mutated: %q", mounts[0].ContainerPath)
	}
}

func TestAdaptProjectMounts(t *testing.T) {
	root := t.TempDir()
	projPath := filepath.Join(root, "MIKE")
	if err := os.MkdirAll(filepath.Join(projPath, "worktrees", "MIKE", "flint"), 0o755); err != nil {
		t.Fatal(err)
	}
	wsPath := filepath.Join(projPath, "worktrees", "MIKE", "flint")

	projStore, err := store.NewStore(filepath.Join(t.TempDir(), "proj.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { projStore.Close() })
	if err := projStore.CreateProject(&types.Project{
		ID:   "MIKE",
		Name: "MIKE",
		Path: projPath,
		SandboxMounts: []types.SandboxMount{
			{HostPath: "/tmp/kanban-mount/node_modules", ContainerPath: "/home/sandbox/MIKE/frontend/node_modules", Mode: "rw"},
			{HostPath: "/tmp/kanban-mount/go", ContainerPath: "/go", Mode: "rw"},
			{HostPath: "/tmp/kanban-mount/.cache", ContainerPath: "/root/.cache", Mode: "rw"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	mgr := NewExtensionLSPManager(nil, nil, projStore)
	projectMounts := map[string]string{projPath: "/mnt/MIKE"}

	// All project mounts are adapted: project-relative paths remapped, global
	// paths pass through, all forced to ro.
	mounts := mgr.adaptProjectMounts(wsPath, projectMounts)
	if len(mounts) != 3 {
		t.Fatalf("adaptProjectMounts returned %d mounts, want 3", len(mounts))
	}

	// Project-relative mount: remapped from /home/sandbox/MIKE/ to /mnt/flint/.
	nm := mounts[0]
	if nm.HostPath != "/tmp/kanban-mount/node_modules" {
		t.Errorf("node_modules HostPath = %q", nm.HostPath)
	}
	if nm.ContainerPath != "/mnt/flint/frontend/node_modules" {
		t.Errorf("node_modules ContainerPath = %q, want /mnt/flint/frontend/node_modules", nm.ContainerPath)
	}
	if nm.Mode != "ro" {
		t.Errorf("node_modules Mode = %q, want ro", nm.Mode)
	}

	// Global mount: passes through unchanged (except mode -> ro).
	goMount := mounts[1]
	if goMount.ContainerPath != "/go" {
		t.Errorf("go ContainerPath = %q, want /go", goMount.ContainerPath)
	}
	if goMount.Mode != "ro" {
		t.Errorf("go Mode = %q, want ro", goMount.Mode)
	}

	// Another global mount.
	cacheMount := mounts[2]
	if cacheMount.ContainerPath != "/root/.cache" {
		t.Errorf("cache ContainerPath = %q, want /root/.cache", cacheMount.ContainerPath)
	}

	// No project store -> nil.
	nilMgr := NewExtensionLSPManager(nil, nil, nil)
	if got := nilMgr.adaptProjectMounts(wsPath, projectMounts); got != nil {
		t.Errorf("adaptProjectMounts(nil projects) = %v, want nil", got)
	}

	// No SandboxMounts -> nil.
	projStore2, err := store.NewStore(filepath.Join(t.TempDir(), "proj2.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { projStore2.Close() })
	if err := projStore2.CreateProject(&types.Project{
		ID:   "empty",
		Name: "empty",
		Path: filepath.Join(root, "empty"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	emptyMgr := NewExtensionLSPManager(nil, nil, projStore2)
	if got := emptyMgr.adaptProjectMounts(filepath.Join(root, "empty"), nil); got != nil {
		t.Errorf("adaptProjectMounts(no mounts) = %v, want nil", got)
	}
}

// TestAdaptProjectMountsUnionsProjects pins the shared-sandbox property: the
// sandbox serves every project, so every project's *global* cache mount is
// mirrored regardless of which workspace is open, while a project-relative mount
// (a node_modules bind, which must sit inside the opened tree) is not dragged in
// from an unrelated project. Duplicates collapse because podman rejects two mounts
// on one destination.
func TestAdaptProjectMountsUnionsProjects(t *testing.T) {
	root := t.TempDir()
	alphaPath := filepath.Join(root, "alpha")
	betaPath := filepath.Join(root, "beta")
	for _, p := range []string{alphaPath, betaPath} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	projStore, err := store.NewStore(filepath.Join(t.TempDir(), "proj.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { projStore.Close() })

	if err := projStore.CreateProject(&types.Project{
		ID: "alpha", Name: "alpha", Path: alphaPath,
		SandboxMounts: []types.SandboxMount{
			{HostPath: "/host/go-cache", ContainerPath: "/go", Mode: "rw"},
			{HostPath: "/host/alpha-node", ContainerPath: "/home/sandbox/alpha/frontend/node_modules", Mode: "rw"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := projStore.CreateProject(&types.Project{
		ID: "beta", Name: "beta", Path: betaPath,
		SandboxMounts: []types.SandboxMount{
			// Same host+destination as alpha's: must collapse to one mount.
			{HostPath: "/host/go-cache", ContainerPath: "/go", Mode: "rw"},
			{HostPath: "/host/beta-cache", ContainerPath: "/root/.cache", Mode: "rw"},
			{HostPath: "/host/beta-node", ContainerPath: "/home/sandbox/beta/node_modules", Mode: "rw"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	mgr := NewExtensionLSPManager(nil, nil, projStore)
	projectMounts := map[string]string{alphaPath: "/mnt/alpha", betaPath: "/mnt/beta"}

	mounts := mgr.adaptProjectMounts(alphaPath, projectMounts)

	byContainer := map[string]types.SandboxMount{}
	for _, mo := range mounts {
		if _, dup := byContainer[mo.ContainerPath]; dup {
			t.Fatalf("duplicate destination %q in %v — podman would reject the container", mo.ContainerPath, mounts)
		}
		byContainer[mo.ContainerPath] = mo
	}

	// Both projects' global caches are present even though only alpha is open.
	if got, ok := byContainer["/go"]; !ok {
		t.Error("alpha's module cache mount missing")
	} else if got.Mode != "ro" {
		t.Errorf("/go mount is %q, want ro", got.Mode)
	}
	if got, ok := byContainer["/root/.cache"]; !ok {
		t.Error("beta's global cache mount is missing while alpha is open; the shared sandbox must carry both")
	} else if got.HostPath != "/host/beta-cache" || got.Mode != "ro" {
		t.Errorf("beta cache mount = %+v, want /host/beta-cache ro", got)
	}

	// The open project's workspace-scoped mount lands inside its own tree.
	if got, ok := byContainer["/mnt/alpha/frontend/node_modules"]; !ok {
		t.Errorf("alpha's node_modules bind was not remapped into the workspace: %v", mounts)
	} else if got.HostPath != "/host/alpha-node" {
		t.Errorf("node_modules host = %q, want /host/alpha-node", got.HostPath)
	}

	// A different project's workspace-scoped mount must not be borrowed.
	for container := range byContainer {
		if strings.HasPrefix(container, "/mnt/alpha/node_modules") {
			t.Errorf("beta's workspace-scoped mount leaked into alpha's tree: %v", container)
		}
	}
}

// ─── SandboxMemory ──────────────────────────────────────────────────────────

func TestExtensionLSPManager_SandboxMemory_NoClient(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := store.Create(makeTestLSPExt("gopls", "go")); err != nil {
		t.Fatalf("Create(gopls): %v", err)
	}
	reg := extensions.NewRegistryClient()
	sandbox := extensions.NewExtensionSandboxManager(reg, store)
	mgr := NewExtensionLSPManager(store, sandbox, nil)

	stats := mgr.SandboxMemory("/tmp/workspace", "/tmp/workspace/main.go")
	if stats == nil || stats.Running || stats.CurrentMB != 0 || stats.LimitMB != 0 {
		t.Errorf("SandboxMemory() = %+v, want zero stats (no client)", stats)
	}
}

func TestExtensionLSPManager_SandboxMemory_NoExtensionForFile(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := store.Create(makeTestLSPExt("gopls", "go")); err != nil {
		t.Fatalf("Create(gopls): %v", err)
	}
	reg := extensions.NewRegistryClient()
	sandbox := extensions.NewExtensionSandboxManager(reg, store)
	mgr := NewExtensionLSPManager(store, sandbox, nil)

	// Python file with only a Go extension installed → no lookup match.
	stats := mgr.SandboxMemory("/tmp/workspace", "/tmp/workspace/script.py")
	if stats == nil || stats.Running || stats.CurrentMB != 0 {
		t.Errorf("SandboxMemory() = %+v, want zero stats (no extension for file)", stats)
	}
}

func TestExtensionLSPManager_SandboxMemory_ClientWithoutSandbox(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := store.Create(makeTestLSPExt("gopls", "go")); err != nil {
		t.Fatalf("Create(gopls): %v", err)
	}
	reg := extensions.NewRegistryClient()
	sandbox := extensions.NewExtensionSandboxManager(reg, store)
	mgr := NewExtensionLSPManager(store, sandbox, nil)

	// Inject a client whose extension sandbox was never created.
	mgr.clients[processKey("/tmp/workspace", "go")] = &sandboxClient{
		workspace:   "/tmp/workspace",
		language:    "go",
		extensionID: "gopls",
	}

	stats := mgr.SandboxMemory("/tmp/workspace", "/tmp/workspace/main.go")
	if stats == nil || stats.Running {
		t.Errorf("SandboxMemory() = %+v, want Running=false (sandbox not created)", stats)
	}
}

// --- Session attribution -----------------------------------------------------
//
// The extension sandbox is shared by every project, so one bridge connection
// carries the notifications of several workspaces. Attribution is what stops one
// project's diagnostics being served to another with the wrong URI translation.

// quietBridgeClient returns a bridge client whose pipes are never used: these
// tests only need a stable pointer to identify the connection a session lives on.
func quietBridgeClient(t *testing.T) *sandbox.Client {
	t.Helper()
	stdinRead, stdinWrite := io.Pipe()   // the client writes into stdinWrite
	stdoutRead, stdoutWrite := io.Pipe() // the client reads from stdoutRead
	client := sandbox.NewClient(stdinWrite, stdoutRead)
	t.Cleanup(func() {
		_ = stdoutWrite.Close()
		_ = stdinRead.Close()
		_ = client.Close()
	})
	return client
}

func attribClient(bridge *sandbox.Client, hostPath string) *sandboxClient {
	return &sandboxClient{
		bridgeClient:  bridge,
		workspace:     hostPath,
		language:      "go",
		extensionID:   "ext-go",
		projectMounts: map[string]string{hostPath: "/mnt/" + filepath.Base(hostPath)},
		session:       sessionFor(hostPath),
	}
}

func TestSessionForIsStablePerWorkspace(t *testing.T) {
	dir := t.TempDir()
	alpha := filepath.Join(dir, "alpha")
	beta := filepath.Join(dir, "beta")

	if got, want := sessionFor(alpha), sessionFor(alpha+"/"); got != want {
		t.Errorf("sessionFor differs for the same workspace: %q vs %q", got, want)
	}
	if sessionFor(alpha) == sessionFor(beta) {
		t.Error("two workspaces share a session id; their servers would be the same session")
	}
	if !strings.HasPrefix(sessionFor(alpha), "ws-") {
		t.Errorf("session id %q does not look like a session id", sessionFor(alpha))
	}
}

func diagnosticsFor(path string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{
		"method": "textDocument/publishDiagnostics",
		"params": map[string]any{"uri": "file://" + path, "diagnostics": []any{}},
	})
	return raw
}

func TestRouteNotificationGoesToItsOwnWorkspace(t *testing.T) {
	bridge := quietBridgeClient(t)
	m := NewExtensionLSPManager(nil, nil, nil)
	m.clients[processKey("/host/alpha", "go")] = attribClient(bridge, "/host/alpha")
	m.clients[processKey("/host/beta", "go")] = attribClient(bridge, "/host/beta")

	sub := m.Subscribe()
	defer m.Unsubscribe(sub)

	m.routeNotification(bridge, "ext-go", protocol.LSPNotification{
		Session: sessionFor("/host/beta"),
		Raw:     diagnosticsFor("/mnt/beta/main.go"),
	})

	select {
	case got := <-sub:
		if !strings.Contains(string(got), "file:///host/beta/main.go") {
			t.Errorf("notification for beta was not translated with beta's mounts: %s", got)
		}
		if strings.Contains(string(got), "alpha") {
			t.Errorf("beta's notification carried alpha's workspace: %s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("attributed notification was never delivered")
	}
}

func TestRouteNotificationNeverGuessedWhenUnattributed(t *testing.T) {
	bridge := quietBridgeClient(t)
	m := NewExtensionLSPManager(nil, nil, nil)
	m.clients[processKey("/host/alpha", "go")] = attribClient(bridge, "/host/alpha")
	m.clients[processKey("/host/beta", "go")] = attribClient(bridge, "/host/beta")

	sub := m.Subscribe()
	defer m.Unsubscribe(sub)

	// A bridge older than sessions cannot say who published this, and two
	// candidates could have: dropping it is the only honest option.
	m.routeNotification(bridge, "ext-go", protocol.LSPNotification{Raw: diagnosticsFor("/mnt/alpha/main.go")})

	select {
	case got := <-sub:
		t.Errorf("ambiguous notification was delivered to somebody: %s", got)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestRouteNotificationUnattributedNeedsOnlyOneCandidate(t *testing.T) {
	bridge := quietBridgeClient(t)
	m := NewExtensionLSPManager(nil, nil, nil)
	m.clients[processKey("/host/alpha", "go")] = attribClient(bridge, "/host/alpha")

	sub := m.Subscribe()
	defer m.Unsubscribe(sub)

	m.routeNotification(bridge, "ext-go", protocol.LSPNotification{Raw: diagnosticsFor("/mnt/alpha/main.go")})

	select {
	case got := <-sub:
		if !strings.Contains(string(got), "file:///host/alpha/main.go") {
			t.Errorf("single-candidate notification mistranslated: %s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the only possible owner never received the notification")
	}
}

func TestRouteNotificationIgnoresForeignSessions(t *testing.T) {
	bridge := quietBridgeClient(t)
	m := NewExtensionLSPManager(nil, nil, nil)
	m.clients[processKey("/host/alpha", "go")] = attribClient(bridge, "/host/alpha")

	sub := m.Subscribe()
	defer m.Unsubscribe(sub)

	// Nobody owns this session.
	m.routeNotification(bridge, "ext-go", protocol.LSPNotification{
		Session: sessionFor("/host/nowhere"), Raw: diagnosticsFor("/mnt/nowhere/main.go"),
	})
	// This session belongs to a workspace, but to a different extension's sandbox.
	m.routeNotification(bridge, "ext-other", protocol.LSPNotification{
		Session: sessionFor("/host/alpha"), Raw: diagnosticsFor("/mnt/alpha/main.go"),
	})

	select {
	case got := <-sub:
		t.Errorf("a notification with no owner here was delivered: %s", got)
	case <-time.After(200 * time.Millisecond):
	}
}

// --- one initialize per session ---------------------------------------------
//
// A session is shared now: the editor's channel, a turn's diagnostics pass, and
// an agent tool call all drive the same language-server process. That process
// answers a second `initialize` with an error and never re-reads the workspace,
// so the manager has to recognise the request and answer it itself.

func TestIsInitializeRequest(t *testing.T) {
	yes := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":"7","method":"initialize"}`,
	}
	no := []string{
		`{"jsonrpc":"2.0","method":"initialized","params":{}}`,   // notification, safe to repeat
		`{"jsonrpc":"2.0","id":2,"method":"textDocument/hover"}`, // a different request
		`{"jsonrpc":"2.0","id":1,"result":{"capabilities":{}}}`,  // a response
		`{"jsonrpc":"2.0","id":null,"method":"initialize"}`,      // no id: not a request
		`not json`,
	}
	for _, raw := range yes {
		if !isInitializeRequest(json.RawMessage(raw)) {
			t.Errorf("initialize not recognised: %s", raw)
		}
	}
	for _, raw := range no {
		if isInitializeRequest(json.RawMessage(raw)) {
			t.Errorf("not an initialize request: %s", raw)
		}
	}
}

func TestReplayInitializeResponseCarriesTheAskingID(t *testing.T) {
	cached := json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"capabilities":{"hoverProvider":true}}}`)

	// A numeric id stays numeric: an LSP client that sent 7 wants 7 back, not "7".
	replayed := replayInitializeResponse(cached, json.RawMessage(`{"jsonrpc":"2.0","id":7,"method":"initialize"}`))
	if string(replayed) == "" {
		t.Fatal("a well-formed pair produced no replay")
	}
	var got struct {
		ID     float64 `json:"id"`
		Result struct {
			Capabilities map[string]any `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal(replayed, &got); err != nil {
		t.Fatalf("replay is not valid JSON: %s", replayed)
	}
	if got.ID != 7 {
		t.Errorf("replayed id = %v, want 7 (the first requester's id would route this to the wrong client)", got.ID)
	}
	if got.Result.Capabilities["hoverProvider"] != true {
		t.Errorf("replay lost the capabilities: %s", replayed)
	}

	// A string id is preserved as a string.
	stringID := replayInitializeResponse(cached, json.RawMessage(`{"jsonrpc":"2.0","id":"abc","method":"initialize"}`))
	if !strings.Contains(string(stringID), `"id":"abc"`) {
		t.Errorf("string id not preserved: %s", stringID)
	}

	// Garbage in, no replay: the caller forwards instead of inventing an answer.
	for _, bad := range []string{`not json`, `{"jsonrpc":"2.0"}`, `{}`} {
		if out := replayInitializeResponse(cached, json.RawMessage(bad)); out != nil {
			t.Errorf("replayed from %q: %s", bad, out)
		}
	}
	if out := replayInitializeResponse(json.RawMessage(`broken`), json.RawMessage(`{"id":3}`)); out != nil {
		t.Errorf("replayed a corrupt cached response: %s", out)
	}
}

func TestRememberInitResponseOnlyTrustsRealHandshakes(t *testing.T) {
	mgr := NewExtensionLSPManager(nil, nil, nil)
	key := processKey("/ws/one", "go")
	sc := &sandboxClient{workspace: "/ws/one", language: "go", session: sessionFor("/ws/one")}
	mgr.clients[key] = sc

	if mgr.rememberInitResponse(key, sc, json.RawMessage(`{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"already initialized"}}`)) {
		t.Error("an error response was remembered as the handshake")
	}
	if sc.initialized {
		t.Error("a failed initialize marked the session initialized")
	}

	if !mgr.rememberInitResponse(key, sc, json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"capabilities":{}}}`)) {
		t.Fatal("a real handshake was not remembered")
	}
	if !sc.initialized || len(sc.initResponse) == 0 {
		t.Fatalf("initialized=%v response=%q", sc.initialized, sc.initResponse)
	}

	// A recycled session (a different client under the same key) must not be
	// written to by the previous process's answer.
	replaced := &sandboxClient{workspace: "/ws/one", language: "go"}
	mgr.clients[key] = replaced
	if mgr.rememberInitResponse(key, sc, json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"capabilities":{}}}`)) {
		t.Error("a stale client wrote through the current one")
	}
	if replaced.initialized {
		t.Error("the replacement session claims a handshake it never made")
	}
}
