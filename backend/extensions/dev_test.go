package extensions

// A development row stands beside a person's own repository, so the two must come apart cleanly: the row
// owns the copy the server loaded, the project owns the repository, and nothing under the reserved
// namespace is installable.

import (
	"bytes"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"mike/pkg/sandboxruntime"
)

// devHandler is testHandlerWithStore over an extensions directory of its own, which every path a
// development row derives is measured against.
func devHandler(t *testing.T) (*ExtensionHandler, *ExtensionStore, string) {
	t.Helper()
	dir := t.TempDir()
	extStore := testStore(t)
	return devHandlerAt(t, extStore, dir), extStore, dir
}

// devHandlerAt is the same handler over a caller-owned extensions directory, for the tests whose
// repository has to live inside the very root the handler serves.
func devHandlerAt(t *testing.T, extStore *ExtensionStore, dir string) *ExtensionHandler {
	t.Helper()
	reg := NewRegistryClientWithBoot(bridgeBoot(t))
	sandbox := NewExtensionSandboxManager(reg, extStore, sandboxruntime.Boot{})
	return NewExtensionHandler(extStore, sandbox, reg, &ExtensionConfig{ExtensionsDir: dir})
}

func createDevRow(t *testing.T, store *ExtensionStore, id, name string, loadedDir, taskID string) *Extension {
	t.Helper()
	ext := &Extension{
		ID:            id,
		Name:          name,
		Author:        "operator",
		Version:       "0.1.0",
		Type:          ExtensionTypeUI,
		InstallSource: DevInstallSource,
		ProjectID:     "my-project",
		TaskID:        taskID,
		LoadedDir:     loadedDir,
		// An unloaded row is not enabled: enabling one is refused, and readers must not see it either.
		Enabled: loadedDir != "",
	}
	ext.Manifest = ExtensionManifest{DisplayName: name, Version: "0.1.0", Author: "operator"}
	if err := store.Create(ext); err != nil {
		t.Fatalf("create development row %s: %v", id, err)
	}
	return ext
}

func TestDeletingADevRowTakesItsLoadedCopyNotTheRepository(t *testing.T) {
	h, store, root := devHandler(t)
	repo := DevRepoDir(root, "my-ext")
	writeExtensionFiles(t, repo)
	staging, err := DevStagingDir(root, "my-ext", "task1")
	if err != nil {
		t.Fatal(err)
	}
	writeExtensionFiles(t, staging)
	rel, err := filepath.Rel(root, staging)
	if err != nil {
		t.Fatal(err)
	}
	createDevRow(t, store, "dev1", "My Ext", filepath.ToSlash(rel), "task1")

	body := deleteExtensionRequest(t, h, "dev1")
	if leftover, ok := body["leftover"]; ok && leftover != nil && leftover != "" {
		t.Fatalf("delete reported a leftover the server should own: %v", leftover)
	}
	if exists(staging) {
		t.Error("the loaded copy survived its row: those bytes are still served")
	}
	if !exists(filepath.Join(repo, "manifest.json")) {
		t.Error("deleting the row deleted the author's repository")
	}
}

func TestDeletingAnUnloadedDevRowClaimsNoLeftover(t *testing.T) {
	h, store, root := devHandler(t)
	repo := DevRepoDir(root, "my-ext")
	writeExtensionFiles(t, repo)
	createDevRow(t, store, "dev1", "My Ext", "", "")

	body := deleteExtensionRequest(t, h, "dev1")
	if leftover, ok := body["leftover"]; ok && leftover != nil && leftover != "" {
		t.Fatalf("an unloaded row copied nothing, so it can leave nothing behind: %v", leftover)
	}
	if !exists(filepath.Join(repo, "manifest.json")) {
		t.Error("deleting an unloaded row deleted the author's repository")
	}
}

func TestRemoveDevRepoRemovesOnlyWhatTheServerCreated(t *testing.T) {
	h, _, root := devHandler(t)

	repo := DevRepoDir(root, "my-ext")
	writeExtensionFiles(t, repo)
	if leftover := h.RemoveDevRepo(repo); leftover != "" {
		t.Fatalf("removing a development repository failed: %s", leftover)
	}
	if exists(repo) {
		t.Error("the repository survived its project's deletion")
	}

	// An installed extension's directory is never removed this way, however the row is stored.
	installed := filepath.Join(root, "acme", "pack")
	writeExtensionFiles(t, installed)
	// Nor is the staging tree, the namespace root, or the extensions root itself.
	staging, err := DevStagingDir(root, "another", "task1")
	if err != nil {
		t.Fatal(err)
	}
	writeExtensionFiles(t, staging)
	for _, path := range []string{installed, root, DevRoot(root), filepath.Join(DevRoot(root), "_try"), staging} {
		if leftover := h.RemoveDevRepo(path); leftover != "" {
			t.Errorf("RemoveDevRepo(%s) refused out loud: %s", path, leftover)
		}
		if !exists(path) {
			t.Errorf("RemoveDevRepo(%s) removed a directory it does not own", path)
		}
	}
}

func TestRemoveDevRepoRefusesALinkedRepository(t *testing.T) {
	h, _, root := devHandler(t)
	outside := t.TempDir()
	victim := filepath.Join(outside, "work-in-progress")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "manifest.json"), []byte(`{"display_name":"mine"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(DevRoot(root), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(DevRoot(root), "linked")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}

	if leftover := h.RemoveDevRepo(link); leftover == "" {
		t.Fatal("a development path resolving outside the namespace was removed anyway")
	}
	if !exists(filepath.Join(victim, "manifest.json")) {
		t.Fatal("RemoveDevRepo deleted what the link pointed at")
	}
}

func TestLocalListingNeverOffersTheDevelopmentNamespace(t *testing.T) {
	_, _, root := devHandler(t)
	writeExtensionFiles(t, filepath.Join(root, "acme", "pack"))
	writeExtensionFiles(t, DevRepoDir(root, "my-ext"))
	staging, err := DevStagingDir(root, "my-ext", "task1")
	if err != nil {
		t.Fatal(err)
	}
	writeExtensionFiles(t, staging)

	found, err := scanLocalExtensions(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, le := range found {
		if le.Author == DevNamespace || le.Name == "my-ext" {
			t.Errorf("a development directory was offered as installable: %s/%s", le.Author, le.Name)
		}
	}
	if len(found) != 1 || found[0].Name != "pack" {
		t.Fatalf("the installed extension went missing from the listing: %+v", found)
	}
}

func TestInstallLocalRefusesTheDevelopmentNamespace(t *testing.T) {
	h, _, root := devHandler(t)
	writeExtensionFiles(t, DevRepoDir(root, "my-ext"))

	body, err := json.Marshal(map[string]string{"author": DevNamespace, "name": "my-ext"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/install-local", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.HandleInstallLocal(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("installing the development namespace = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestEnablingAnUnloadedDevelopmentRowIsRefused(t *testing.T) {
	h, store, root := devHandler(t)
	createDevRow(t, store, "dev-unloaded", "My Ext", "", "")
	staging, err := DevStagingDir(root, "my-ext", "task1")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, staging)
	if err != nil {
		t.Fatal(err)
	}
	createDevRow(t, store, "dev-loaded", "Loaded Ext", filepath.ToSlash(rel), "task1")

	setEnabled := func(id string) *httptest.ResponseRecorder {
		body, err := json.Marshal(map[string]bool{"enabled": true})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPatch, "/api/extensions/"+id+"/enabled", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		h.HandleSetEnabled(rec, req, id)
		return rec
	}

	rec := setEnabled("dev-unloaded")
	if rec.Code != http.StatusConflict {
		t.Fatalf("enabling an unloaded development row = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	stored, err := store.GetByID("dev-unloaded")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Enabled {
		t.Fatal("the row was enabled anyway, so its unread files would be registered")
	}
	if !stored.IsLoaded() {
		// The refusal's own claim: it is the missing load, not the row, that is the problem.
		t.Log("unloaded row stays inert")
	}

	if rec := setEnabled("dev-loaded"); rec.Code != http.StatusOK {
		t.Fatalf("enabling a loaded development row = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestStartupRepairSkipsDevelopmentRows(t *testing.T) {
	h, store, _ := devHandler(t)
	createDevRow(t, store, "dev1", "My Ext", "", "")
	storedExtension(t, store, "local1", "acme", "pack", "local")

	// The pass classifies before it repairs, and a row it classifies as missing is a row the boot
	// reports as a broken install. That classification is the whole decision, and it needs no network.
	missing, err := h.classifyMissingInstalls()
	if err != nil {
		t.Fatal(err)
	}
	sawLocal := false
	for _, m := range missing {
		if m.ext.ID == "dev1" {
			t.Fatalf("a development repository was classified as a broken install: %s", m.why)
		}
		if m.ext.ID == "local1" {
			sawLocal = true
		}
	}
	if !sawLocal {
		t.Fatal("the pass classified nothing, so the skip proves nothing")
	}
}

func TestDevSlugRefusesTheNamesTheServerOwns(t *testing.T) {
	for _, name := range []string{"My Cool Ext!", "  My Cool Ext!  "} {
		slug, err := DevSlug(name)
		if err != nil {
			t.Fatalf("DevSlug(%q): %v", name, err)
		}
		if slug != "my-cool-ext" {
			t.Errorf("DevSlug(%q) = %q, want my-cool-ext", name, slug)
		}
	}
	for _, name := range []string{"///", "!!!", "   ", "?!?"} {
		if slug, err := DevSlug(name); err == nil {
			t.Errorf("DevSlug(%q) = %q, want a refusal", name, slug)
		}
	}

	// The namespace stays the server's because no name can be slugged into it — the invariant the
	// reserved directory rests on, stated where it can break.
	for _, name := range []string{"_dev", "_try", "_dev/_try", "  _dev  ", "---dev"} {
		slug, err := DevSlug(name)
		if err != nil {
			continue
		}
		if slug == DevNamespace || slug == devStagingNamespace {
			t.Errorf("DevSlug(%q) = %q, which is the server's own directory", name, slug)
		}
	}
}

func TestDevStagingDirRefusesANameThatIsNotAnID(t *testing.T) {
	_, _, root := devHandler(t)
	for _, taskID := range []string{"", "..", "../..", "a/b", "..hidden", "-x"} {
		if dir, err := DevStagingDir(root, "my-ext", taskID); err == nil {
			t.Errorf("DevStagingDir(%q) = %s, want a refusal", taskID, dir)
		}
	}
}
