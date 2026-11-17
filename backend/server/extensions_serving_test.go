package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestExtensionsTreeRefusesSymlinkEscape (security): /extensions/ is served with no session
// (isPublicPath) and http.Dir follows symlinks, so a link makes its target world-readable.
func TestExtensionsTreeRefusesSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "id_ed25519")
	if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plain", "manifest.json"), []byte(`{"name":"plain"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "acme")); err != nil {
		t.Fatal(err)
	}

	extFS := noEscapeFS{dir: root}

	if f, err := extFS.Open("acme/id_ed25519"); err == nil {
		f.Close()
		t.Fatal("a file outside the extensions root was opened through a symlink")
	}
	if f, err := extFS.Open("acme"); err == nil {
		f.Close()
		t.Fatal("a symlinked author directory was opened")
	}

	// Control: the tree still serves, so the refusal is the link.
	f, err := extFS.Open("plain/manifest.json")
	if err != nil {
		t.Fatalf("an ordinary extension asset was refused: %v", err)
	}
	defer f.Close()
}

// TestExtensionsTreeRefusesANameThatIsNotAFile (security): opening a FIFO never returns, and this
// prefix answers without a session, so an anonymous request would park a server worker on the pipe.
func TestExtensionsTreeRefusesANameThatIsNotAFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "acme"), 0o755); err != nil {
		t.Fatal(err)
	}
	mkFifo(t, filepath.Join(root, "acme", "theme.css"))

	h := http.FileServer(noListingFS{noEscapeFS{dir: root}})
	serve := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/acme/theme.css", nil))
		return rr
	}
	rr := answeredIn(t, 10*time.Second, "GET /extensions/acme/theme.css on a FIFO", serve)
	if rr.Code != http.StatusNotFound {
		t.Errorf("a FIFO at a served name = %d, want 404: %s", rr.Code, rr.Body.String())
	}
}

// TestExtensionsTreeRefusesDotDirectories (security): an extension author's git checkout is created
// under the served root, and this prefix is public, so `.git` — whose remote URL can carry a
// credential — and the authoring files under `.agents` must not be readable. Nothing an extension
// ships is reached through a dot path, so the control beside each refusal is the extension itself.
func TestExtensionsTreeRefusesDotDirectories(t *testing.T) {
	root := t.TempDir()
	ext := filepath.Join(root, "acme", "my-ext")
	write := func(rel, body string) {
		p := filepath.Join(ext, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".git/config", "[remote \"origin\"]\n\turl = https://user:sekrit@example/x.git\n")
	write(".git/HEAD", "ref: refs/heads/main\n")
	write(".agents/skills/extension-authoring/SKILL.md", "# what the author wants\n")
	write(".env", "TOKEN=sekrit\n")
	write("manifest.json", `{"display_name":"My Ext"}`)
	write("index.html", "<!doctype html><title>ext</title>")
	write("MyExt.js", "export default function init() { return () => {}; }")

	h := http.FileServer(noListingFS{noEscapeFS{dir: root}})
	get := func(name string) int {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, name, nil))
		return rr.Code
	}

	for _, name := range []string{
		"/acme/my-ext/.git/config",
		"/acme/my-ext/.git/HEAD",
		"/acme/my-ext/.agents/skills/extension-authoring/SKILL.md",
		"/acme/my-ext/.env",
		"/acme/my-ext/%2Egit/config",      // the same name, spelled by the client's encoder
		"/acme/my-ext/sub/../.git/config", // reached only by climbing back out
	} {
		if code := get(name); code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", name, code)
		}
	}

	for _, name := range []string{"/acme/my-ext/manifest.json", "/acme/my-ext/MyExt.js"} {
		if code := get(name); code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 — the dot rule reached past the dot directories", name, code)
		}
	}
	// The entry contract still works: the host mounts the extension's *directory* — never its
	// index.html, which http.FileServer redirects to the directory as a matter of contract.
	if code := get("/acme/my-ext/"); code != http.StatusOK {
		t.Errorf("GET the extension directory = %d, want 200", code)
	}
}

// TestExtensionsTreeRefusesADotDirectoryThroughALink (security): a symlink is ordinary repository
// content, so a committed link naming `.git` would hand the same bytes out under a URL with no dot
// in it. The rule judges what the name resolves to, not how it was spelled.
func TestExtensionsTreeRefusesADotDirectoryThroughALink(t *testing.T) {
	root := t.TempDir()
	ext := filepath.Join(root, "acme", "my-ext")
	if err := os.MkdirAll(filepath.Join(ext, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ext, ".git", "config"), []byte("url = https://user:sekrit@example/x.git"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ext, "manifest.json"), []byte(`{"display_name":"My Ext"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".git", filepath.Join(ext, "hooks")); err != nil {
		t.Fatal(err)
	}

	extFS := noEscapeFS{dir: root}
	if f, err := extFS.Open("acme/my-ext/hooks/config"); err == nil {
		f.Close()
		t.Fatal("a committed symlink opened .git under a dot-free name")
	}
	if f, err := extFS.Open("acme/my-ext/hooks"); err == nil {
		f.Close()
		t.Fatal("a symlink naming a dot directory was opened")
	}

	f, err := extFS.Open("acme/my-ext/manifest.json")
	if err != nil {
		t.Fatalf("an ordinary extension asset was refused: %v", err)
	}
	f.Close()
}
