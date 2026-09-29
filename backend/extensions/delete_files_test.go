package extensions

// Deleting an extension takes its materialized files with it, and the code doing it performs a recursive
// delete built partly from a manifest's own words. Both halves are the point: what gets removed, and
// what must never be removed because a name or a symlink said so.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func storedExtension(t *testing.T, store *ExtensionStore, id, author, name, source string) *Extension {
	t.Helper()
	ext := &Extension{ID: id, Name: name, Author: author, Type: "lsp", Enabled: true, InstallSource: source}
	ext.Manifest = ExtensionManifest{DisplayName: name, Version: "1.0.0", Author: author}
	if err := store.Create(ext); err != nil {
		t.Fatalf("create extension %s: %v", id, err)
	}
	return ext
}

func writeExtensionFiles(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "static"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		filepath.Join(dir, "manifest.json"):     `{"name":"x","version":"1.0.0"}`,
		filepath.Join(dir, "static", "main.js"): `export default function () {}`,
		filepath.Join(dir, "adapter.wasm"):      "not really wasm",
	} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func deleteExtensionRequest(t *testing.T, h *ExtensionHandler, id string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/api/extensions/"+id, nil)
	rec := httptest.NewRecorder()
	h.HandleDelete(rec, req, id)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete %s = %d: %s", id, rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %s", rec.Body.String())
	}
	return body
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestHandleDelete_RemovesTheMaterializedDirectory: the row used to go and the files stayed, so the
// extension kept being served from disk and came back through the local listing with its old manifest.
func TestHandleDelete_RemovesTheMaterializedDirectory(t *testing.T) {
	h, store, root := handlerFixture(t)
	storedExtension(t, store, "go-team-gopls-1", "Go Team", "gopls", "registry")
	storedExtension(t, store, "rust-analyzer-1", "Rust Analyzer Project", "rust-analyzer", "registry")

	goplsDir := filepath.Join(root, "go-team", "gopls")
	otherDir := filepath.Join(root, "rust-analyzer-project", "rust-analyzer")
	writeExtensionFiles(t, goplsDir)
	writeExtensionFiles(t, otherDir)

	body := deleteExtensionRequest(t, h, "go-team-gopls-1")
	if warn, ok := body["leftover"]; ok {
		t.Fatalf("delete reported leftover files: %v", warn)
	}
	if exists(goplsDir) {
		t.Error("the deleted extension's files are still on disk — they are what gets served and installed")
	}
	if exists(filepath.Join(root, "go-team")) {
		t.Error("the author folder left behind holds nothing and shows up as an installable author")
	}
	if !exists(filepath.Join(otherDir, "static", "main.js")) {
		t.Error("another extension's files were removed along with it")
	}
	if _, err := store.GetByID("go-team-gopls-1"); err == nil {
		t.Error("the row survived")
	}
	if _, err := store.GetByID("rust-analyzer-1"); err != nil {
		t.Errorf("the other extension's row should be untouched: %v", err)
	}

	// A second delete of the same id is a 404, not a panic over files that are already gone.
	req := httptest.NewRequest(http.MethodDelete, "/api/extensions/go-team-gopls-1", nil)
	rec := httptest.NewRecorder()
	h.HandleDelete(rec, req, "go-team-gopls-1")
	if rec.Code != http.StatusNotFound {
		t.Errorf("deleting twice = %d, want 404", rec.Code)
	}
}

// TestHandleDelete_RemovesAFlatLocalExtension covers the older layout, where an extension with no
// author folder lives directly under the root.
func TestHandleDelete_RemovesAFlatLocalExtension(t *testing.T) {
	h, _, root := handlerFixture(t)
	storedExtension(t, h.store, "local-my-ui", "local", "my-ui", "local")
	dir := filepath.Join(root, "my-ui")
	writeExtensionFiles(t, dir)

	body := deleteExtensionRequest(t, h, "local-my-ui")
	if warn, ok := body["leftover"]; ok {
		t.Fatalf("delete reported leftover files: %v", warn)
	}
	if exists(dir) {
		t.Error("the flat extension directory survived the delete")
	}
	if !exists(root) {
		t.Fatal("the extensions root itself must never be the delete target")
	}
}

func TestHandleDelete_NeverDeletesOutsideTheExtensionsRoot(t *testing.T) {
	t.Run("a name that is not a single path segment", func(t *testing.T) {
		h, _, root := handlerFixture(t)
		storedExtension(t, h.store, "evil-1", "acme", "../../outside", "registry")

		outside := filepath.Join(filepath.Dir(root), "outside")
		writeExtensionFiles(t, outside)

		body := deleteExtensionRequest(t, h, "evil-1")
		if _, ok := body["leftover"]; !ok {
			t.Error("a refused deletion must be reported, not skipped quietly")
		}
		if !exists(filepath.Join(outside, "manifest.json")) {
			t.Error("content outside the extensions root was deleted by a manifest's name")
		}
	})

	t.Run("an author folder that is a symlink elsewhere", func(t *testing.T) {
		h, _, root := handlerFixture(t)
		storedExtension(t, h.store, "evil-2", "linked", "srv", "registry")

		outside := filepath.Join(t.TempDir(), "acme")
		if err := os.MkdirAll(filepath.Join(outside, "srv"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		writeExtensionFiles(t, filepath.Join(outside, "srv"))

		body := deleteExtensionRequest(t, h, "evil-2")
		if _, ok := body["leftover"]; !ok {
			t.Error("a deletion refused because the path escaped the root must be reported")
		}
		if !exists(filepath.Join(outside, "srv", "manifest.json")) {
			t.Error("following a symlink out of the extensions root deleted somebody else's directory")
		}
	})
}

// TestHandleDelete_ReportsFilesItCouldNotRemove: the row goes either way, so an unremovable directory
// must arrive as a named path rather than silence.
func TestHandleDelete_ReportsFilesItCouldNotRemove(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not bind the superuser")
	}
	h, _, root := handlerFixture(t)
	storedExtension(t, h.store, "go-team-gopls-2", "Go Team", "gopls", "registry")
	goplsDir := filepath.Join(root, "go-team", "gopls")
	writeExtensionFiles(t, goplsDir)
	// Read-only on the extension's own directory: removing its contents needs write permission there,
	// so RemoveAll fails partway and the operator has to finish the job by hand.
	if err := os.Chmod(goplsDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(goplsDir, 0o700) })

	body := deleteExtensionRequest(t, h, "go-team-gopls-2")
	leftover, ok := body["leftover"]
	if !ok {
		t.Fatalf("the leftover was swallowed; body = %v", body)
	}
	if !strings.Contains(leftover.(string), goplsDir) {
		t.Errorf("leftover = %q, want it to name the path the operator has to remove by hand", leftover)
	}
	if _, err := h.store.GetByID("go-team-gopls-2"); err == nil {
		t.Error("an unremovable directory must not make the extension undeletable")
	}
}

// TestHandleDelete_WithNoFilesAtAll: a record whose directory was never written (or was removed by
// hand) is a normal delete, not a warning.
func TestHandleDelete_WithNoFilesAtAll(t *testing.T) {
	h, _, _ := handlerFixture(t)
	storedExtension(t, h.store, "never-written", "acme", "ghost", "registry")

	body := deleteExtensionRequest(t, h, "never-written")
	if warn, ok := body["leftover"]; ok {
		t.Errorf("nothing was on disk, yet the delete warned: %v", warn)
	}
}
