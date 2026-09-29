package extensions

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeLocalLSP lays down extensions/<author>/<name>/manifest.json and nothing else: whether the
// folder ships a Dockerfile is stated at each call site.
func writeLocalLSP(t *testing.T, h *ExtensionHandler, author, name, manifest string) string {
	t.Helper()
	extDir := t.TempDir()
	h.config.ExtensionsDir = extDir
	dir := filepath.Join(extDir, author, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("WriteFile manifest: %v", err)
	}
	return dir
}

func installLocal(t *testing.T, h *ExtensionHandler, author, name string) sseEvent {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"author": author, "name": name})
	req := httptest.NewRequest(http.MethodPost, "/api/extensions/install-local", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleInstallLocal(w, req)
	events := readSSEEvents(t, w.Body.Bytes())
	if len(events) == 0 {
		t.Fatal("install-local answered with no SSE events")
	}
	return events[len(events)-1]
}

const lspManifestFmt = `{
	"display_name": "Fixture Language Server",
	"description": "Local folder that names the image it runs in.",
	"author": "Fixture Author",
	"version": "1.0.0",%s
	"capabilities": {"lsp": {"command": "fixture-lsp", "args": ["--stdio"], "file_types": [{"extension": "fx", "language_id": "fx", "label": "FX"}]}}
}`

// A declared image is stored, never pulled — the sandbox pulls on first use — so install must not
// ask podman for anything at all.
// serial for the reason at fakePodmanExit.
func TestInstallLocalLanguageServerDeclaresItsOwnImage(t *testing.T) {
	h := testHandler(t)
	writeLocalLSP(t, h, "fixturedev", "fixture-lsp",
		strings.Replace(lspManifestFmt, "%s", `
	"image": "localhost/kanban-ext-fixture-lsp:1.0.0",`, 1))
	fake := fakePodmanOK(t)

	last := installLocal(t, h, "fixturedev", "fixture-lsp")
	if last.Error != "" {
		t.Fatalf("a local folder declaring an image must install, got: %s", last.Error)
	}
	if last.Result == nil {
		t.Fatal("expected a result on the done event")
	}
	if last.Result.ImageRef != "localhost/kanban-ext-fixture-lsp:1.0.0" {
		t.Errorf("ImageRef = %q, want the image the manifest declared", last.Result.ImageRef)
	}
	if calls := fake.calls(t); len(calls) != 0 {
		t.Errorf("a declared image was built or pulled at install: %v", calls)
	}
}

// The declared image takes the same qualification rule as a registry entry's: an image naming no
// registry cannot be pulled headless, so the folder is refused rather than installed broken.
// serial for the reason at fakePodmanExit.
func TestInstallLocalRefusesAnUnqualifiedDeclaredImage(t *testing.T) {
	h := testHandler(t)
	writeLocalLSP(t, h, "fixturedev", "fixture-lsp",
		strings.Replace(lspManifestFmt, "%s", `
	"image": "fixture-lsp:1.0.0",`, 1))
	fakePodmanOK(t)

	last := installLocal(t, h, "fixturedev", "fixture-lsp")
	if last.Error == "" {
		t.Fatal("an unqualified image was accepted")
	}
	if !strings.Contains(last.Error, "fully qualified") {
		t.Errorf("the refusal does not name the defect it refuses: %s", last.Error)
	}
}

// Neither image nor Dockerfile leaves nothing to run in a sandbox, and a row that reports healthy
// while serving nothing is the failure to avoid.
// serial for the reason at fakePodmanExit.
func TestInstallLocalStillRefusesALanguageServerWithNoImageAndNoDockerfile(t *testing.T) {
	h := testHandler(t)
	writeLocalLSP(t, h, "fixturedev", "fixture-lsp", strings.Replace(lspManifestFmt, "%s", "", 1))
	fakePodmanOK(t)

	last := installLocal(t, h, "fixturedev", "fixture-lsp")
	if last.Error == "" {
		t.Fatal("an LSP folder with no image and no Dockerfile installed")
	}
	if !strings.Contains(last.Error, "nothing to run in a sandbox") {
		t.Errorf("unexpected refusal: %s", last.Error)
	}
}

// The browse listing is built from the same mapping as the install, or the two disagree about what
// a folder declared.
func TestLocalListingCarriesTheDeclaredImage(t *testing.T) {
	t.Parallel()
	h := testHandler(t)
	writeLocalLSP(t, h, "fixturedev", "fixture-lsp",
		strings.Replace(lspManifestFmt, "%s", `
	"image": "localhost/kanban-ext-fixture-lsp:1.0.0",`, 1))

	req := httptest.NewRequest(http.MethodGet, "/api/extensions/local", nil)
	w := httptest.NewRecorder()
	h.HandleLocalExtensions(w, req)

	var entries RegistryIndex
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode local listing: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("listing holds %d entries, want 1", len(entries))
	}
	if entries[0].Image != "localhost/kanban-ext-fixture-lsp:1.0.0" {
		t.Errorf("listing lost the declared image: %+v", entries[0])
	}
}
