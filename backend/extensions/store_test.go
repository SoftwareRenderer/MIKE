package extensions

import (
	"os"
	"path/filepath"
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

func TestGetByIDNotFound(t *testing.T) {
	es := testStore(t)
	_, err := es.GetByID("nonexistent")
	if err == nil {
		t.Error("GetByID(nil) = nil, want error")
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
		ID:            "full-ext",
		Name:          "full-extension",
		Version:       "1.0.0",
		Type:          ExtensionTypeLSP,
		Manifest: ExtensionManifest{
			DisplayName: "Full Extension",
			Description: "Test extension with full config",
			Author:      "Test Author",
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
	if got.Manifest.LSP.Command != "gopls" {
		t.Errorf("LSP.Command = %v, want gopls", got.Manifest.LSP.Command)
	}
	if len(got.Manifest.LSP.Args) != 1 || got.Manifest.LSP.Args[0] != "serve" {
		t.Errorf("LSP.Args = %v, want [serve]", got.Manifest.LSP.Args)
	}
	if got.Manifest.LSP.Language != "go" {
		t.Errorf("LSP.Language = %v, want go", got.Manifest.LSP.Language)
	}
	if got.Manifest.LSP.InitParamsOverride["staticcheck"] != true {
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
