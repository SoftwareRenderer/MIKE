package lsp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"kanban/backend/extensions"
	"kanban/backend/store"
	"kanban/backend/types"
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

	_, err := mgr.GetOrCreate("/tmp/workspace", "go", nil)
	if err == nil {
		t.Error("GetOrCreate() = nil, want error (no extension)")
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
	wsContainer := "/mnt/flint"

	// All project mounts are adapted: project-relative paths remapped, global
	// paths pass through, all forced to ro.
	mounts := mgr.adaptProjectMounts(wsPath, wsContainer)
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
	if got := nilMgr.adaptProjectMounts(wsPath, wsContainer); got != nil {
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
	if got := emptyMgr.adaptProjectMounts(filepath.Join(root, "empty"), "/mnt/empty"); got != nil {
		t.Errorf("adaptProjectMounts(no mounts) = %v, want nil", got)
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
