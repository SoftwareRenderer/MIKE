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
