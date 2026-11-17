package lsp

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"mike/backend/extensions"
	"mike/backend/store"
	"mike/backend/types"

	"mike/pkg/sandboxruntime"
)

func testExtensionStore(t *testing.T) *extensions.ExtensionStore {
	t.Helper()
	dir := t.TempDir()
	db, err := store.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	es := extensions.NewExtensionStore(db, nil)
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
					Command:   lang + "pls",
					Args:      []string{"-mode=stdio"},
					FileTypes: fileTypesFor(lang, extns),
					Language:  lang,
				},
			},
		},
		Enabled: true,
	}
}

// registryWith answers for exactly the extensions it is handed.
func registryWith(t *testing.T, exts ...*extensions.Extension) *ExtensionRegistry {
	t.Helper()
	es := testExtensionStore(t)
	for _, ext := range exts {
		if err := es.Create(ext); err != nil {
			t.Fatalf("Create(%s): %v", ext.ID, err)
		}
	}
	return NewExtensionRegistry(es)
}

func threeServers() []*extensions.Extension {
	return []*extensions.Extension{
		makeTestLSPExt("gopls", "go"),
		makeTestLSPExt("typescript", "typescript"),
		makeTestLSPExt("pyright", "python"),
	}
}

func withToolCalls(ext *extensions.Extension, on bool) *extensions.Extension {
	v := on
	ext.Manifest.Capabilities.LSP.ToolCallsEnabled = &v
	return ext
}

func withChatDiagnostics(ext *extensions.Extension, on bool) *extensions.Extension {
	v := on
	ext.Manifest.Capabilities.LSP.ChatDiagnosticsEnabled = &v
	return ext
}

func withInitOverride(ext *extensions.Extension, override map[string]any) *extensions.Extension {
	ext.Manifest.Capabilities.LSP.InitParamsOverride = override
	return ext
}

func disabled(ext *extensions.Extension) *extensions.Extension {
	ext.Enabled = false
	return ext
}

func withoutLSPConfig(name string) *extensions.Extension {
	ext := makeTestLSPExt(name, "go")
	ext.Manifest.Capabilities.LSP = nil
	return ext
}

// TestExtensionRegistryLookup is the one place a path's answer is pinned: which installed
// extension becomes the ServerConfig for it, and which of that extension's declarations ride along.
// The rest of the handler reads only this answer, so every way it can be wrong is a way the editor
// loses its server: a suffix nobody declares answers nothing, a path with no suffix answers nothing
// (a workspace directory, a Makefile), a disabled extension answers nothing, and an extension that
// declares no server answers nothing — while a suffix still answers inside such a path, and a
// workspace directory named after the language is the other way a server is found, matched without
// regard to case.
func TestExtensionRegistryLookup(t *testing.T) {
	on, off := true, false
	three := func() []*extensions.Extension {
		return []*extensions.Extension{
			withChatDiagnostics(makeTestLSPExt("gopls", "go"), true),
			makeTestLSPExt("typescript", "typescript"),
			makeTestLSPExt("pyright", "python"),
		}
	}
	cases := []struct {
		name string
		exts []*extensions.Extension
		path string
		want string // "" means no server may answer at all
		cmd  string
		chat *bool
		over map[string]any
	}{
		{"a declared suffix routes to its server", threeServers(), "main.go", "go", "gopls", nil, nil},
		{"another extension's suffix routes elsewhere", threeServers(), "app.ts", "typescript", "typescriptpls", nil, nil},
		{"a suffix nothing declares answers nothing", threeServers(), "unknown.xyz", "", "", nil, nil},
		{"a directory with no suffix answers nothing", threeServers(), "/tmp/workspace", "", "", nil, nil},
		{"a suffixless file answers nothing", threeServers(), "/tmp/workspace/Makefile", "", "", nil, nil},
		{"a suffix still answers inside a directory", threeServers(), "/tmp/workspace/main.go", "go", "gopls", nil, nil},
		{"a workspace named after the language", threeServers(), "/home/dev/go", "go", "gopls", nil, nil},
		{"and that name is matched case-insensitively", threeServers(), "/home/dev/GO", "go", "gopls", nil, nil},
		{"nothing installed answers nothing", nil, "main.go", "", "", nil, nil},
		{"a disabled extension answers nothing", []*extensions.Extension{disabled(makeTestLSPExt("gopls", "go"))}, "main.go", "", "", nil, nil},
		{"an extension declaring no server answers nothing", []*extensions.Extension{withoutLSPConfig("no-lsp")}, "main.go", "", "", nil, nil},
		{"the manifest's initParamsOverride rides along",
			[]*extensions.Extension{withInitOverride(makeTestLSPExt("gopls", "go"), map[string]any{"staticcheck": true})},
			"main.go", "go", "gopls", nil, map[string]any{"staticcheck": true}},
		{"chat diagnostics is its extension's own declaration", three(), "main.go", "go", "gopls", &on, nil},
		{"an extension that never declared it is off", three(), "app.py", "python", "pythonpls", &off, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := registryWith(t, tc.exts...).Lookup(tc.path)
			if tc.want == "" {
				if cfg != nil {
					t.Fatalf("Lookup(%q) = %+v, want no server", tc.path, cfg)
				}
				return
			}
			if cfg == nil {
				t.Fatalf("Lookup(%q) = nil, want the %s server", tc.path, tc.want)
			}
			if cfg.Language != tc.want || cfg.Command != tc.cmd {
				t.Errorf("Lookup(%q) = language %q command %q, want %q/%q", tc.path, cfg.Language, cfg.Command, tc.want, tc.cmd)
			}
			if tc.chat != nil && cfg.ChatDiagnostics != *tc.chat {
				t.Errorf("Lookup(%q) ChatDiagnostics = %v, want %v", tc.path, cfg.ChatDiagnostics, *tc.chat)
			}
			if tc.over != nil && !reflect.DeepEqual(cfg.InitParamsOverride, tc.over) {
				t.Errorf("Lookup(%q) InitParamsOverride = %v, want %v — the manifest is the only way an extension configures its server",
					tc.path, cfg.InitParamsOverride, tc.over)
			}
		})
	}
}

// TestExtensionRegistryHasAnyLSPToolCalls decides whether the agent gets LSP tools at all. One
// enabled extension that leaves them on is enough; one extension's "off" never answers for another's
// "on", and a store where every one is off is a store with no tools.
func TestExtensionRegistryHasAnyLSPToolCalls(t *testing.T) {
	cases := []struct {
		name string
		exts []*extensions.Extension
		want bool
	}{
		{"nothing installed", nil, false},
		{"one extension at the default", []*extensions.Extension{makeTestLSPExt("gopls", "go")}, true},
		{"one explicitly on", []*extensions.Extension{withToolCalls(makeTestLSPExt("gopls", "go"), true)}, true},
		{"the only extension off", []*extensions.Extension{withToolCalls(makeTestLSPExt("gopls", "go"), false)}, false},
		{"one off, one at the default",
			[]*extensions.Extension{withToolCalls(makeTestLSPExt("gopls", "go"), false), makeTestLSPExt("typescript", "typescript")}, true},
		{"every extension off",
			[]*extensions.Extension{withToolCalls(makeTestLSPExt("gopls", "go"), false), withToolCalls(makeTestLSPExt("typescript", "typescript"), false)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := registryWith(t, tc.exts...).HasAnyLSPToolCallsExtension(); got != tc.want {
				t.Errorf("HasAnyLSPToolCallsExtension() = %v, want %v", got, tc.want)
			}
		})
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
	sandbox := extensions.NewExtensionSandboxManager(reg, store, sandboxruntime.Boot{})
	mgr := NewExtensionLSPManager(store, sandbox, nil)

	// Must return an error, not panic, for an extensionless file.
	if _, err := mgr.lookupExtensionByFile("/tmp/workspace/Makefile"); err == nil {
		t.Error("expected error for extensionless file")
	}
}

func TestNewExtensionLSPManager(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	reg := extensions.NewRegistryClient()
	sandbox := extensions.NewExtensionSandboxManager(reg, store, sandboxruntime.Boot{})
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
	sandbox := extensions.NewExtensionSandboxManager(reg, store, sandboxruntime.Boot{})
	mgr := NewExtensionLSPManager(store, sandbox, nil)

	_, err := mgr.GetOrCreate(context.Background(), "/tmp/workspace", "go")
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
	sandbox := extensions.NewExtensionSandboxManager(reg, store, sandboxruntime.Boot{})
	mgr := NewExtensionLSPManager(store, sandbox, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() {
		_, err := mgr.GetOrCreate(ctx, "/tmp/workspace", "typescript")
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

func TestExtensionLSPManager_SubscribeAndUnsubscribe(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	reg := extensions.NewRegistryClient()
	sandbox := extensions.NewExtensionSandboxManager(reg, store, sandboxruntime.Boot{})
	mgr := NewExtensionLSPManager(store, sandbox, nil)

	// Subscribe two clients
	sub1 := mgr.Subscribe()
	sub2 := mgr.Subscribe()

	// Simulate a server message being broadcast (normally from SendToServer or notification forwarding)
	msg := jsontext.Value(`{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics","params":{"uri":"file:///tmp/test.go","diagnostics":[]}}`)

	// Directly invoke broadcastToSubscribers (simulating what SendToServer does)
	mgr.broadcastToSubscribers(msg)

	// Both subscribers should receive the message
	for i, ch := range []chan jsontext.Value{sub1, sub2} {
		select {
		case received := <-ch:
			if string(received) != string(msg) {
				t.Errorf("subscriber %d: got %s, want %s", i+1, received, msg)
			}
		case <-time.After(1 * time.Second):
			t.Fatalf("subscriber %d: did not receive message", i+1)
		}
	}

	mgr.Unsubscribe(sub1)

	// Broadcast another message
	msg2 := jsontext.Value(`{"jsonrpc":"2.0","method":"window/logMessage","params":{"type":1,"message":"test"}}`)
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

// mountAt finds the borrowed mount landing on one container path, or nil.
func mountAt(mounts []types.SandboxMount, containerPath string) *types.SandboxMount {
	for i := range mounts {
		if mounts[i].ContainerPath == containerPath {
			return &mounts[i]
		}
	}
	return nil
}

// TestAdaptProjectMounts: borrowing a project's mounts is how the language server reads the
// dependency roots a person configured for the task, and the borrowing has three cuts. Everything
// comes back read-only however the project configured it; a path under the task's container root
// means the same place under the LSP workspace; and a mount the server needs to write, or that core
// has already granted, is not borrowed at all — the server's cache is its own to write, and two -v
// specs on one container path make podman refuse the create. A container path that merely *contains*
// a granted one is a different mount point, and nested binds are legal, so the cut stays exact.
func TestAdaptProjectMounts(t *testing.T) {
	root := t.TempDir()
	projPath := filepath.Join(root, "MIKE")
	wsPath := filepath.Join(projPath, "worktrees", "MIKE", "flint")
	if err := os.MkdirAll(wsPath, 0o755); err != nil {
		t.Fatal(err)
	}
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
			{HostPath: "/tmp/kanban-mount/mod", ContainerPath: "/go/pkg/mod", Mode: "rw"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	mgr := NewExtensionLSPManager(nil, nil, projStore)
	const modPath = "/go/pkg/mod"
	reserved := []types.SandboxMount{{HostPath: "/host/gomodcache", ContainerPath: modPath, Mode: "ro"}}

	borrowed := mgr.adaptProjectMounts(wsPath, "/mnt/flint", nil)
	if got := mountAt(borrowed, "/root/.cache"); got != nil {
		t.Errorf("the server's own cache was borrowed at %q: a gopls with an unwritable cache publishes nothing",
			got.HostPath)
	}
	// Without a grant nothing reserves the path, so the project's mount is just a mount.
	if got := mountAt(borrowed, modPath); got == nil {
		t.Errorf("without a grant the project's mount onto %q was dropped: %+v", modPath, borrowed)
	}

	withheld := mgr.adaptProjectMounts(wsPath, "/mnt/flint", reserved)
	if got := mountAt(withheld, modPath); got != nil {
		t.Errorf("the granted dependency directory was also borrowed from the project (%s)", got.HostPath)
	}
	if len(withheld)+1 != len(borrowed) {
		t.Errorf("the reservation dropped %d mounts, want exactly the one at %q: %v vs %v", len(borrowed)-len(withheld), modPath, borrowed, withheld)
	}

	// A container path that merely contains core's is a different mount point.
	if got := mountAt(withheld, "/go"); got == nil {
		t.Errorf("a mount that only contains the granted path was dropped: %v", withheld)
	}

	for _, mo := range borrowed {
		if mo.Mode != "ro" {
			t.Errorf("%s -> %s Mode = %q, want ro — a borrowed mount is never writable", mo.HostPath, mo.ContainerPath, mo.Mode)
		}
	}
	// Project-relative mount: remapped from the task's container root to the LSP workspace's.
	nodeModules := mountAt(withheld, "/mnt/flint/frontend/node_modules")
	if nodeModules == nil {
		t.Fatalf("the project-relative mount was not remapped under the workspace: %v", withheld)
	}
	if nodeModules.HostPath != "/tmp/kanban-mount/node_modules" {
		t.Errorf("node_modules HostPath = %q, want the project's host path", nodeModules.HostPath)
	}

	// A mount the project never declared stays a nil answer, and so does no project store at all.
	if got := NewExtensionLSPManager(nil, nil, nil).adaptProjectMounts(wsPath, "/mnt/flint", nil); got != nil {
		t.Errorf("adaptProjectMounts(nil projects) = %v, want nil", got)
	}
	emptyStore, err := store.NewStore(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { emptyStore.Close() })
	emptyPath := filepath.Join(root, "empty")
	if err := os.MkdirAll(emptyPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := emptyStore.CreateProject(&types.Project{ID: "empty", Name: "empty", Path: emptyPath}); err != nil {
		t.Fatal(err)
	}
	if got := NewExtensionLSPManager(nil, nil, emptyStore).adaptProjectMounts(emptyPath, "/mnt/empty", nil); got != nil {
		t.Errorf("adaptProjectMounts(no mounts) = %v, want nil", got)
	}

	// The cut is equality, never a prefix test.
	if !isReservedContainerPath(modPath, reserved) || isReservedContainerPath("/go", reserved) {
		t.Error("isReservedContainerPath must be exact equality, not a prefix test")
	}
}

func sandboxMemoryManager(t *testing.T) *ExtensionLSPManager {
	t.Helper()
	es := testExtensionStore(t)
	if err := es.Create(makeTestLSPExt("gopls", "go")); err != nil {
		t.Fatalf("Create(gopls): %v", err)
	}
	return NewExtensionLSPManager(es, extensions.NewExtensionSandboxManager(extensions.NewRegistryClient(), es, sandboxruntime.Boot{}), nil)
}

// TestExtensionLSPManager_SandboxMemory: the memory panel reads through the manager for whatever file
// the editor has open, and there is always an answer. It is the zero answer whenever there is no
// container to ask — no extension owns the file, this workspace has no connection, or the connection
// it has was never given a sandbox.
func TestExtensionLSPManager_SandboxMemory(t *testing.T) {
	const workspace = "/tmp/workspace"
	cases := []struct {
		name   string
		file   string
		attach bool
	}{
		{"no extension owns the file", "script.py", false},
		{"no connection for this workspace", "main.go", false},
		{"a connection whose sandbox was never created", "main.go", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr := sandboxMemoryManager(t)
			if tc.attach {
				mgr.clients[processKey(workspace, "go")] = newSandboxClient(nil, workspace, "go", "gopls", nil)
			}
			stats := mgr.SandboxMemory(workspace, filepath.Join(workspace, tc.file))
			if stats == nil {
				t.Fatal("SandboxMemory() = nil; the panel reads it unconditionally")
			}
			if stats.Running || stats.CurrentMB != 0 || stats.LimitMB != 0 {
				t.Errorf("SandboxMemory() = %+v, want zero stats", stats)
			}
		})
	}
}

// fileTypesFor builds the declaration a test manifest needs; the per-file ids come from the test, so
// a core table could never satisfy the fixture by accident.
func fileTypesFor(language string, exts []string) []extensions.LSPFileType {
	out := make([]extensions.LSPFileType, 0, len(exts))
	for _, e := range exts {
		out = append(out, extensions.LSPFileType{Extension: e, LanguageID: language})
	}
	return out
}
