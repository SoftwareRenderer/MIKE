package lsp

import (
	"path/filepath"
	"testing"

	"kanban/backend/extensions"
	"kanban/backend/store"
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
			LSP: &extensions.LSPConfig{
				Command:    lang + "pls",
				Args:       []string{"-mode=stdio"},
				Extensions: extns,
				Language:   lang,
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

func TestExtensionRegistry_GetAvailable(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	// Create test extensions
	for _, lang := range []string{"go", "typescript"} {
		ext := makeTestLSPExt(lang, lang)
		if err := store.Create(ext); err != nil {
			t.Fatalf("Create(%s): %v", lang, err)
		}
	}

	reg := NewExtensionRegistry(store)
	langs := reg.GetAvailable()

	if len(langs) != 2 {
		t.Errorf("GetAvailable() = %d, want 2", len(langs))
	}

	// Check languages
	langSet := make(map[string]bool)
	for _, l := range langs {
		langSet[l] = true
	}
	if !langSet["go"] {
		t.Error("GetAvailable() missing go")
	}
	if !langSet["typescript"] {
		t.Error("GetAvailable() missing typescript")
	}
}

func TestExtensionRegistry_GetConfig(t *testing.T) {
	store := testExtensionStore(t)
	if err := store.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	ext := makeTestLSPExt("go", "go")
	ext.Manifest.LSP.InitParamsOverride = map[string]any{
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
			LSP:         nil, // No LSP config
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
