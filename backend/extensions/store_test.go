package extensions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kanban/backend/store"
)

func testStore(t *testing.T) *ExtensionStore {
	t.Helper()
	dir := t.TempDir()
	db, err := store.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	es := NewExtensionStore(db)
	if err := es.InitSchema(); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	return es
}

func makeTestExt(id, name, extType string) *Extension {
	now := time.Now().UTC().Format(time.RFC3339)
	return &Extension{
		ID:            id,
		Name:          name,
		Author:        "unknown", // authorDir("") default, matches installs with no registry author
		Version:       "1.0.0",
		Type:          extType,
		Manifest:      ExtensionManifest{DisplayName: name},
		Config:        map[string]any{},
		InstallSource: "manual",
		Enabled:       true,
		InstalledAt:   now,
		BuiltAt:       now,
	}
}

func TestGitSourceRoundTrip(t *testing.T) {
	es := testStore(t)
	ext := makeTestExt("ext-1", "test-ext", ExtensionTypeUI)
	ext.Repo = "https://github.com/author/kanban-extensions"
	ext.Commit = strings.Repeat("deadbeef", 8) // 64-char SHA-256
	ext.Subdir = "myext"
	if err := es.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := es.GetByID("ext-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Repo != ext.Repo || got.Commit != ext.Commit || got.Subdir != ext.Subdir {
		t.Errorf("git source roundtrip: repo=%q commit=%q subdir=%q", got.Repo, got.Commit, got.Subdir)
	}
	if !got.IsGitPinned() {
		t.Error("IsGitPinned() = false, want true")
	}

	// List must also carry the git fields.
	listed, err := es.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 || listed[0].Commit != ext.Commit {
		t.Errorf("List git source: %+v", listed)
	}
}

func TestCreateAndGetExtension(t *testing.T) {
	es := testStore(t)
	ext := makeTestExt("ext-1", "test-ext", ExtensionTypeLSP)
	if err := es.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := es.GetByID("ext-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID != "ext-1" {
		t.Errorf("ID = %v, want ext-1", got.ID)
	}
	if got.Name != "test-ext" {
		t.Errorf("Name = %v, want test-ext", got.Name)
	}
	if got.Type != ExtensionTypeLSP {
		t.Errorf("Type = %v, want lsp", got.Type)
	}
	if !got.Enabled {
		t.Error("Enabled = false, want true")
	}
}

func TestUIConfigSandboxPermissionsRoundTrip(t *testing.T) {
	es := testStore(t)
	ext := makeTestExt("ui-ext", "importer", ExtensionTypeUI)
	ext.Manifest.Capabilities = &Capabilities{
		UI: &UIConfig{
			InjectionPoints: []string{"createTask"},
			Permissions: []string{
				"createTask.description.insertText",
				"createTask.events.paste",
				"network.proxy",
				"config.get",
			},
		},
	}
	if err := es.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := es.GetByID("ui-ext")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	ui := got.Manifest.Capabilities.GetUI()
	if ui == nil {
		t.Fatal("GetUI() = nil, want non-nil")
	}
	if len(ui.Permissions) != 4 {
		t.Fatalf("len(Permissions) = %d, want 4", len(ui.Permissions))
	}
	if ui.Permissions[0] != "createTask.description.insertText" {
		t.Errorf("Permissions[0] = %q", ui.Permissions[0])
	}

}

func TestGetByIDNotFound(t *testing.T) {
	es := testStore(t)
	_, err := es.GetByID("nonexistent")
	if err == nil {
		t.Error("GetByID(nil) = nil, want error")
	}
}

func TestGetByName(t *testing.T) {
	es := testStore(t)
	// GetByName must find records whose ID differs from the name.
	ext := makeTestExt("gopls-1234567890", "gopls", ExtensionTypeLSP)
	if err := es.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := es.GetByName("gopls")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if got.ID != "gopls-1234567890" || got.Name != "gopls" {
		t.Errorf("GetByName = %+v, want timestamped-ID record for gopls", got)
	}

	if _, err := es.GetByName("nonexistent"); err == nil {
		t.Error("GetByName(nonexistent) = nil, want error")
	}
}

func TestGetByAuthorName(t *testing.T) {
	es := testStore(t)
	// Same name from two different authors must be distinct identities.
	if err := es.Create(makeTestExt("a-1", "markdown-preview", ExtensionTypeUI)); err != nil {
		t.Fatalf("Create(a-1): %v", err)
	}
	ext := makeTestExt("dev-1", "markdown-preview", ExtensionTypeUI)
	ext.Author = "dev"
	if err := es.Create(ext); err != nil {
		t.Fatalf("Create(dev-1): %v", err)
	}

	got, err := es.GetByAuthorName("unknown", "markdown-preview")
	if err != nil {
		t.Fatalf("GetByAuthorName(unknown, markdown-preview): %v", err)
	}
	if got.ID != "a-1" {
		t.Errorf("GetByAuthorName(unknown) ID = %q, want a-1", got.ID)
	}

	got, err = es.GetByAuthorName("dev", "markdown-preview")
	if err != nil {
		t.Fatalf("GetByAuthorName(dev, markdown-preview): %v", err)
	}
	if got.ID != "dev-1" {
		t.Errorf("GetByAuthorName(dev) ID = %q, want dev-1", got.ID)
	}

	if _, err := es.GetByAuthorName("dev", "nonexistent"); err == nil {
		t.Error("GetByAuthorName(dev, nonexistent) = nil, want error")
	}
}

func TestListExtensions(t *testing.T) {
	es := testStore(t)
	// Create extensions in reverse alphabetical order
	for _, name := range []string{"zebra", "alpha", "beta"} {
		ext := makeTestExt(name, name, ExtensionTypeLSP)
		if err := es.Create(ext); err != nil {
			t.Fatalf("Create(%s): %v", name, err)
		}
	}

	extensions, err := es.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(extensions) != 3 {
		t.Fatalf("List count = %v, want 3", len(extensions))
	}
	// Should be ordered by name
	if extensions[0].Name != "alpha" {
		t.Errorf("First = %v, want alpha", extensions[0].Name)
	}
	if extensions[2].Name != "zebra" {
		t.Errorf("Last = %v, want zebra", extensions[2].Name)
	}
}

func TestGetByType(t *testing.T) {
	es := testStore(t)
	// Create LSP and UI extensions
	for _, ext := range []*Extension{
		makeTestExt("lsp-1", "gopls", ExtensionTypeLSP),
		makeTestExt("lsp-2", "typescript", ExtensionTypeLSP),
		makeTestExt("ui-1", "theme-dark", ExtensionTypeUI),
	} {
		if err := es.Create(ext); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	lspExts, err := es.GetByType(ExtensionTypeLSP)
	if err != nil {
		t.Fatalf("GetByType(lsp): %v", err)
	}
	if len(lspExts) != 2 {
		t.Errorf("LSP count = %v, want 2", len(lspExts))
	}

	uiExts, err := es.GetByType(ExtensionTypeUI)
	if err != nil {
		t.Fatalf("GetByType(ui): %v", err)
	}
	if len(uiExts) != 1 {
		t.Errorf("UI count = %v, want 1", len(uiExts))
	}
}

func TestUpdateExtension(t *testing.T) {
	es := testStore(t)
	ext := makeTestExt("ext-1", "test-ext", ExtensionTypeLSP)
	if err := es.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Update version and config
	ext.Version = "2.0.0"
	ext.Config = map[string]any{"memoryLimit": 512}
	ext.Manifest.DisplayName = "Updated Name"
	if err := es.Update("ext-1", ext); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := es.GetByID("ext-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Version != "2.0.0" {
		t.Errorf("Version = %v, want 2.0.0", got.Version)
	}
	if got.Manifest.DisplayName != "Updated Name" {
		t.Errorf("DisplayName = %v, want Updated Name", got.Manifest.DisplayName)
	}
	if got.Config["memoryLimit"] != float64(512) {
		t.Errorf("Config.memoryLimit = %v, want 512", got.Config["memoryLimit"])
	}
}

func TestUpdateNotFound(t *testing.T) {
	es := testStore(t)
	ext := makeTestExt("ext-1", "test-ext", ExtensionTypeLSP)
	err := es.Update("nonexistent", ext)
	if err == nil {
		t.Error("Update(nonexistent) = nil, want error")
	}
}

func TestDeleteExtension(t *testing.T) {
	es := testStore(t)
	ext := makeTestExt("ext-1", "test-ext", ExtensionTypeLSP)
	if err := es.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := es.Delete("ext-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := es.GetByID("ext-1")
	if err == nil {
		t.Error("GetByID after Delete = nil, want error")
	}

	extensions, _ := es.List()
	if len(extensions) != 0 {
		t.Errorf("List count after Delete = %v, want 0", len(extensions))
	}
}

func TestDeleteNotFound(t *testing.T) {
	es := testStore(t)
	err := es.Delete("nonexistent")
	if err == nil {
		t.Error("Delete(nonexistent) = nil, want error")
	}
}

func TestSetEnabled(t *testing.T) {
	es := testStore(t)
	ext := makeTestExt("ext-1", "test-ext", ExtensionTypeLSP)
	if err := es.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Disable
	if err := es.SetEnabled("ext-1", false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	got, _ := es.GetByID("ext-1")
	if got.Enabled {
		t.Error("Enabled = true, want false")
	}

	// Re-enable
	if err := es.SetEnabled("ext-1", true); err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}
	got, _ = es.GetByID("ext-1")
	if !got.Enabled {
		t.Error("Enabled = false, want true")
	}
}

func TestSetEnabledNotFound(t *testing.T) {
	es := testStore(t)
	err := es.SetEnabled("nonexistent", true)
	if err == nil {
		t.Error("SetEnabled(nonexistent) = nil, want error")
	}
}

func TestExtensionWithFullConfig(t *testing.T) {
	es := testStore(t)
	ext := &Extension{
		ID:      "full-ext",
		Name:    "full-extension",
		Version: "1.0.0",
		Type:    ExtensionTypeLSP,
		Manifest: ExtensionManifest{
			DisplayName: "Full Extension",
			Description: "Test extension with full config",
			Author:      "Test Author",
			Version:     "1.0.0",
			Capabilities: &Capabilities{
				LSP: &LSPConfig{
					Command:    "gopls",
					Args:       []string{"serve"},
					Extensions: []string{"go"},
					Language:   "go",
					InitParamsOverride: map[string]any{
						"usePlaceholders": true,
						"staticcheck":     true,
					},
				},
			},
		},
		Config: map[string]any{
			"memoryLimit": 512,
			"customArg":   "test",
		},
		ContainerConfig: ContainerConfig{
			Dockerfile: "Dockerfile",
			Entrypoint: []string{"/usr/bin/gopls", "serve"},
			WorkingDir: "/workspace",
		},
		SandboxConfig: SandboxConfig{
			NetworkIsolation: true,
			MemoryLimit:      512,
		},
		InstallSource: "registry",
		Enabled:       true,
	}
	ext.InstalledAt = time.Now().UTC().Format(time.RFC3339)
	ext.BuiltAt = ext.InstalledAt

	if err := es.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := es.GetByID("full-ext")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	// Verify LSP config
	if !got.HasLSPConfig() {
		t.Fatal("HasLSPConfig = false, want true")
	}
	if got.Manifest.Capabilities.GetLSP().Command != "gopls" {
		t.Errorf("LSP.Command = %v, want gopls", got.Manifest.Capabilities.GetLSP().Command)
	}
	if len(got.Manifest.Capabilities.GetLSP().Args) != 1 || got.Manifest.Capabilities.GetLSP().Args[0] != "serve" {
		t.Errorf("LSP.Args = %v, want [serve]", got.Manifest.Capabilities.GetLSP().Args)
	}
	if got.Manifest.Capabilities.GetLSP().Language != "go" {
		t.Errorf("LSP.Language = %v, want go", got.Manifest.Capabilities.GetLSP().Language)
	}
	if got.Manifest.Capabilities.GetLSP().InitParamsOverride["staticcheck"] != true {
		t.Error("LSP.InitParamsOverride.staticcheck = false, want true")
	}

	// Verify container config
	if got.ContainerConfig.Dockerfile != "Dockerfile" {
		t.Errorf("ContainerConfig.Dockerfile = %v, want Dockerfile", got.ContainerConfig.Dockerfile)
	}
	if len(got.ContainerConfig.Entrypoint) != 2 {
		t.Errorf("ContainerConfig.Entrypoint = %v, want 2 entries", got.ContainerConfig.Entrypoint)
	}

	// Verify sandbox config
	if !got.SandboxConfig.NetworkIsolation {
		t.Error("SandboxConfig.NetworkIsolation = false, want true")
	}
	if got.SandboxConfig.MemoryLimit != 512 {
		t.Errorf("SandboxConfig.MemoryLimit = %v, want 512", got.SandboxConfig.MemoryLimit)
	}

	// Verify install source
	if !got.IsRegistryInstalled() {
		t.Error("IsRegistryInstalled = false, want true")
	}
}

func TestEmptyList(t *testing.T) {
	es := testStore(t)
	extensions, err := es.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(extensions) != 0 {
		t.Errorf("List count = %v, want 0", len(extensions))
	}
}

func TestConcurrentAccess(t *testing.T) {
	es := testStore(t)
	// Create multiple extensions concurrently
	for i := 0; i < 10; i++ {
		go func(i int) {
			ext := makeTestExt("concurrent-ext", "concurrent-ext", ExtensionTypeLSP)
			_ = es.Create(ext)
		}(i)
	}
	time.Sleep(100 * time.Millisecond) // let goroutines finish

	// Read concurrently
	var count int
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			extensions, _ := es.List()
			count += len(extensions)
			done <- true
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	// Should have at least 1 read (all should succeed)
	if count == 0 {
		t.Error("Concurrent reads returned 0 results")
	}
}

func TestTempDirCleanup(t *testing.T) {
	// Verify that temp DB files are cleaned up
	dir := t.TempDir()
	db, err := store.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	db.Close()

	// Check that files exist
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) == 0 {
		t.Error("No files in temp dir")
	}
	// t.TempDir() will clean up after test
}

func TestStorageRoundTrip(t *testing.T) {
	es := testStore(t)
	if _, err := es.StorageSet("ext1", "p:proj1", "tags", json.RawMessage(`["a","b"]`)); err != nil {
		t.Fatalf("set: %v", err)
	}
	entry, err := es.StorageGet("ext1", "p:proj1", "tags")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if entry == nil || string(entry.Value) != `["a","b"]` {
		t.Fatalf("unexpected entry: %+v", entry)
	}
}

func TestStorageGetMissingReturnsNil(t *testing.T) {
	es := testStore(t)
	entry, err := es.StorageGet("ext1", "p:proj1", "nope")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if entry != nil {
		t.Fatalf("expected nil, got %+v", entry)
	}
}

func TestStorageOverwrite(t *testing.T) {
	es := testStore(t)
	es.StorageSet("ext1", "p:proj1", "k", json.RawMessage(`1`))
	es.StorageSet("ext1", "p:proj1", "k", json.RawMessage(`2`))
	entry, _ := es.StorageGet("ext1", "p:proj1", "k")
	if string(entry.Value) != `2` {
		t.Fatalf("expected overwritten value, got %s", entry.Value)
	}
}

func TestStorageNamespacesAreIsolated(t *testing.T) {
	es := testStore(t)
	es.StorageSet("ext1", "nsA", "k", json.RawMessage(`1`))
	es.StorageSet("ext1", "nsB", "k", json.RawMessage(`2`))
	es.StorageSet("ext2", "nsA", "k", json.RawMessage(`3`))

	// Same key in different namespace is distinct.
	a, _ := es.StorageGet("ext1", "nsA", "k")
	b, _ := es.StorageGet("ext1", "nsB", "k")
	if string(a.Value) != `1` || string(b.Value) != `2` {
		t.Fatalf("namespaces not isolated: %s / %s", a.Value, b.Value)
	}
	// Different extension in same namespace is distinct.
	other, _ := es.StorageGet("ext2", "nsA", "k")
	if string(other.Value) != `3` {
		t.Fatalf("extensions not isolated: %s", other.Value)
	}
}

func TestStorageListWithPrefix(t *testing.T) {
	es := testStore(t)
	es.StorageSet("ext1", "ns", "b", json.RawMessage(`1`))
	es.StorageSet("ext1", "ns", "abc", json.RawMessage(`2`))
	es.StorageSet("ext1", "ns", "abd", json.RawMessage(`3`))
	es.StorageSet("ext1", "ns", "x", json.RawMessage(`4`))

	entries, err := es.StorageList("ext1", "ns", "ab")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Key != "abc" || entries[1].Key != "abd" {
		t.Fatalf("unexpected ordering: %+v", entries)
	}
}

func TestStorageDelete(t *testing.T) {
	es := testStore(t)
	es.StorageSet("ext1", "ns", "k", json.RawMessage(`1`))
	if err := es.StorageDelete("ext1", "ns", "k"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	entry, _ := es.StorageGet("ext1", "ns", "k")
	if entry != nil {
		t.Fatalf("expected deleted, got %+v", entry)
	}
}

func TestStorageRejectsInvalidJSON(t *testing.T) {
	es := testStore(t)
	if _, err := es.StorageSet("ext1", "ns", "k", json.RawMessage(`not-json`)); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestStorageRejectsOversizedValue(t *testing.T) {
	es := testStore(t)
	big := json.RawMessage(`"` + strings.Repeat("a", maxExtensionValueBytes+1) + `"`)
	if _, err := es.StorageSet("ext1", "ns", "k", big); err == nil {
		t.Fatal("expected error for oversized value")
	}
}

func TestStorageDeleteAll(t *testing.T) {
	es := testStore(t)
	es.StorageSet("ext1", "nsA", "k1", json.RawMessage(`1`))
	es.StorageSet("ext1", "nsA", "k2", json.RawMessage(`2`))
	es.StorageSet("ext1", "nsB", "k1", json.RawMessage(`3`))
	es.StorageSet("ext2", "nsA", "k1", json.RawMessage(`4`))

	if err := es.StorageDeleteAll("ext1"); err != nil {
		t.Fatalf("delete all: %v", err)
	}
	// ext1 storage is gone across all namespaces.
	entries, err := es.StorageList("ext1", "nsA", "")
	if err != nil || len(entries) != 0 {
		t.Fatalf("expected ext1 nsA empty, got %d (%v)", len(entries), err)
	}
	entries, _ = es.StorageList("ext1", "nsB", "")
	if len(entries) != 0 {
		t.Fatalf("expected ext1 nsB empty, got %d", len(entries))
	}
	// ext2 is untouched.
	other, _ := es.StorageGet("ext2", "nsA", "k1")
	if other == nil || string(other.Value) != `4` {
		t.Fatalf("ext2 storage should be untouched, got %+v", other)
	}
}

func TestUpdateMemoryLimit_PersistsAndSyncsConfig(t *testing.T) {
	es := testStore(t)
	reg := NewRegistryClient()
	sm := NewExtensionSandboxManager(reg, es)

	ext := makeTestExt("ext-mem", "ext-mem", ExtensionTypeLSP)
	ext.Config["other"] = "keep-me"
	if err := es.Create(ext); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// No sandbox is running — UpdateMemoryLimit must persist and sync config;
	// the live-apply path is a no-op.
	if err := sm.UpdateMemoryLimit("ext-mem", 512); err != nil {
		t.Fatalf("UpdateMemoryLimit(512) = %v", err)
	}
	got, err := es.GetByID("ext-mem")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.SandboxConfig.MemoryLimit != 512 {
		t.Errorf("SandboxConfig.MemoryLimit = %d, want 512", got.SandboxConfig.MemoryLimit)
	}
	// JSON round-trip: numbers come back as float64.
	if ml, ok := got.Config["memory_limit"]; !ok || ml != float64(512) {
		t.Errorf("Config[memory_limit] = %v, want 512", got.Config["memory_limit"])
	}
	if got.Config["other"] != "keep-me" {
		t.Errorf("Config[other] = %v, want keep-me (unrelated keys preserved)", got.Config["other"])
	}

	// 0 clears the limit (run unlimited).
	if err := sm.UpdateMemoryLimit("ext-mem", 0); err != nil {
		t.Fatalf("UpdateMemoryLimit(0) = %v", err)
	}
	got, err = es.GetByID("ext-mem")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.SandboxConfig.MemoryLimit != 0 {
		t.Errorf("SandboxConfig.MemoryLimit = %d, want 0 (cleared)", got.SandboxConfig.MemoryLimit)
	}
	if ml, ok := got.Config["memory_limit"]; !ok || ml != float64(0) {
		t.Errorf("Config[memory_limit] = %v, want 0", got.Config["memory_limit"])
	}

	// Negative input clamps to 0.
	if err := sm.UpdateMemoryLimit("ext-mem", -64); err != nil {
		t.Fatalf("UpdateMemoryLimit(-64) = %v", err)
	}
	got, err = es.GetByID("ext-mem")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.SandboxConfig.MemoryLimit != 0 {
		t.Errorf("SandboxConfig.MemoryLimit = %d, want 0 (negative clamped)", got.SandboxConfig.MemoryLimit)
	}

	// Unknown extension is an error.
	if err := sm.UpdateMemoryLimit("no-such-ext", 100); err == nil {
		t.Error("UpdateMemoryLimit(no-such-ext) = nil, want error")
	}
}
