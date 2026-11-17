package extensions

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mike/backend/crypto"
	"mike/backend/errs"
	"mike/backend/store"
)

func testStore(t *testing.T) *ExtensionStore {
	t.Helper()
	return newTestStore(t, testBox())
}

// testStoreNoKey is a store with nothing to seal a secret with.
func testStoreNoKey(t *testing.T) *ExtensionStore {
	t.Helper()
	return newTestStore(t, nil)
}

// testBox is the data key these stores seal with. The derivation is expensive on purpose, so one Box
// serves the package.
var testBox = sync.OnceValue(func() *crypto.Box {
	box, err := crypto.New("extension-test-passphrase", bytes.Repeat([]byte("k"), crypto.SaltSize))
	if err != nil {
		panic(err)
	}
	return box
})

func newTestStore(t *testing.T, box *crypto.Box) *ExtensionStore {
	t.Helper()
	dir := t.TempDir()
	db, err := store.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	es := NewExtensionStore(db, box)
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
	// List must also carry the git fields.
	listed, err := es.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 || listed[0].Commit != ext.Commit {
		t.Errorf("List git source: %+v", listed)
	}
}

func TestGetByIDNotFound(t *testing.T) {
	es := testStore(t)
	_, err := es.GetByID("nonexistent")
	if err == nil {
		t.Error("GetByID(nil) = nil, want error")
	}
	// The handler answers this with 404 only because the error says so; the ID
	// stays in the message because callers show it.
	if !errs.IsNotFound(err) {
		t.Errorf("errs.IsNotFound(%v) = false, want true", err)
	}
	if !errors.Is(err, ErrExtensionNotFound) {
		t.Errorf("errors.Is(err, ErrExtensionNotFound) = false, err = %v", err)
	}
	if msg := err.Error(); !strings.HasPrefix(msg, "extension not found: ") {
		t.Errorf("message = %q, want the %q prefix", msg, "extension not found: ")
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
	} else if !errs.IsNotFound(err) {
		t.Errorf("errs.IsNotFound(%v) = false, want true", err)
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

// Every write that names a row runs through execOne, whose RowsAffected check is what stops a
// statement that matched nothing from answering "done": an update that silently hit no row leaves the
// Settings card showing a value that was never stored, and a delete that hit no row reports the
// extension gone while its files keep being served.
func TestWriteToAnAbsentRowIsAnError(t *testing.T) {
	es := testStore(t)

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"Update", func() error { return es.Update("nonexistent", makeTestExt("x", "x", ExtensionTypeLSP)) }},
		{"Delete", func() error { return es.Delete("nonexistent") }},
		{"SetEnabled", func() error { return es.SetEnabled("nonexistent", true) }},
		{"SetImageRef", func() error { return es.SetImageRef("nonexistent", "localhost/x:1", ContainerConfig{}) }},
		{"SetBuiltAt", func() error { return es.SetBuiltAt("nonexistent") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatalf("%s of an absent row = nil, want an error", tc.name)
			}
			if !strings.Contains(err.Error(), "nonexistent") {
				t.Errorf("%s error = %q, want it to name the row", tc.name, err)
			}
		})
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
					Command:   "gopls",
					Args:      []string{"serve"},
					FileTypes: []LSPFileType{{Extension: "go", LanguageID: "go"}},
					Language:  "go",
					InitParamsOverride: map[string]any{
						"usePlaceholders": true,
						"staticcheck":     true,
					},
				},
				// The UI half rides in the same manifest blob, and its permission list is
				// what the host grants at runtime — order and all.
				UI: &UIConfig{
					InjectionPoints: []string{"createTask"},
					Permissions: []string{
						"createTask.description.insertText",
						"createTask.events.paste",
						"network.proxy",
						"config.get",
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
	lsp := got.Manifest.Capabilities.GetLSP()
	if lsp == nil {
		t.Fatal("LSP config missing after round-trip")
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
	if got.InstallSource != "registry" {
		t.Errorf("InstallSource = %q, want registry", got.InstallSource)
	}
	if !got.Enabled {
		t.Error("Enabled = false, want true")
	}
	if got.Type != ExtensionTypeLSP {
		t.Errorf("Type = %q, want %q", got.Type, ExtensionTypeLSP)
	}

	// Verify the UI half of the manifest came back as it was written, in order.
	ui := got.Manifest.Capabilities.UI
	if ui == nil {
		t.Fatal("Capabilities.UI = nil, want non-nil")
	}
	if len(ui.Permissions) != 4 || ui.Permissions[0] != "createTask.description.insertText" {
		t.Errorf("UI permissions = %v, want the four as declared", ui.Permissions)
	}
}

func TestConcurrentAccess(t *testing.T) {
	es := testStore(t)
	// Create multiple extensions concurrently. The WaitGroup is what makes the reads
	// below meaningful: without it the test raced its own writers on a sleep.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ext := makeTestExt("concurrent-ext", "concurrent-ext", ExtensionTypeLSP)
			_ = es.Create(ext)
		}(i)
	}
	wg.Wait()

	// Read concurrently. count is shared across goroutines, so it needs a counter and
	// not a +=, which the race detector is right to flag.
	var count atomic.Int64
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			extensions, _ := es.List()
			count.Add(int64(len(extensions)))
			done <- true
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	// Should have at least 1 read (all should succeed)
	if count.Load() == 0 {
		t.Error("Concurrent reads returned 0 results")
	}
}

func TestStorageRoundTrip(t *testing.T) {
	es := testStore(t)
	if _, err := es.StorageSet("ext1", "p:proj1", "tags", jsontext.Value(`["a","b"]`)); err != nil {
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
	es.StorageSet("ext1", "p:proj1", "k", jsontext.Value(`1`))
	es.StorageSet("ext1", "p:proj1", "k", jsontext.Value(`2`))
	entry, _ := es.StorageGet("ext1", "p:proj1", "k")
	if string(entry.Value) != `2` {
		t.Fatalf("expected overwritten value, got %s", entry.Value)
	}
}

func TestStorageNamespacesAreIsolated(t *testing.T) {
	es := testStore(t)
	es.StorageSet("ext1", "nsA", "k", jsontext.Value(`1`))
	es.StorageSet("ext1", "nsB", "k", jsontext.Value(`2`))
	es.StorageSet("ext2", "nsA", "k", jsontext.Value(`3`))

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

// A prefix is data, not a pattern: its LIKE wildcards must not widen the listing, and what it does
// match comes back in key order, which is the only ordering the caller can page on.
func TestStorageListMatchesAPrefixLiterallyAndInOrder(t *testing.T) {
	es := testStore(t)
	es.StorageSet("ext1", "ns", "a%c", jsontext.Value(`1`))
	es.StorageSet("ext1", "ns", "axc", jsontext.Value(`2`))
	es.StorageSet("ext1", "ns", `a\c`, jsontext.Value(`3`))
	es.StorageSet("ext1", "ns", "a_c", jsontext.Value(`4`))
	es.StorageSet("ext1", "ns", "b", jsontext.Value(`5`))
	es.StorageSet("ext1", "ns", "abc", jsontext.Value(`6`))
	es.StorageSet("ext1", "ns", "abd", jsontext.Value(`7`))
	es.StorageSet("ext1", "ns", "x", jsontext.Value(`8`))

	for _, tc := range []struct {
		prefix string
		want   []string
	}{
		{"a%", []string{"a%c"}},
		{"a_", []string{"a_c"}},
		{`a\`, []string{`a\c`}},
		{"ab", []string{"abc", "abd"}},
		{"a", []string{"a%c", `a\c`, "a_c", "abc", "abd", "axc"}},
	} {
		entries, err := es.StorageList("ext1", "ns", tc.prefix)
		if err != nil {
			t.Fatalf("list %q: %v", tc.prefix, err)
		}
		var got []string
		for _, e := range entries {
			got = append(got, e.Key)
		}
		if len(got) != len(tc.want) {
			t.Errorf("prefix %q listed %v, want %v", tc.prefix, got, tc.want)
			continue
		}
		for i, w := range tc.want {
			if got[i] != w {
				t.Errorf("prefix %q listed %v, want %v", tc.prefix, got, tc.want)
				break
			}
		}
	}
}

func TestStorageDelete(t *testing.T) {
	es := testStore(t)
	es.StorageSet("ext1", "ns", "k", jsontext.Value(`1`))
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
	if _, err := es.StorageSet("ext1", "ns", "k", jsontext.Value(`not-json`)); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestStorageRejectsOversizedValue(t *testing.T) {
	es := testStore(t)
	big := jsontext.Value(`"` + strings.Repeat("a", maxExtensionValueBytes+1) + `"`)
	if _, err := es.StorageSet("ext1", "ns", "k", big); err == nil {
		t.Fatal("expected error for oversized value")
	}
}

func TestStorageDeleteAll(t *testing.T) {
	es := testStore(t)
	es.StorageSet("ext1", "nsA", "k1", jsontext.Value(`1`))
	es.StorageSet("ext1", "nsA", "k2", jsontext.Value(`2`))
	es.StorageSet("ext1", "nsB", "k1", jsontext.Value(`3`))
	es.StorageSet("ext2", "nsA", "k1", jsontext.Value(`4`))

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
