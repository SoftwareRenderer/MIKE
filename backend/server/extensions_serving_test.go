package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// TestExtensionsAssetRouteServes (feature): an extension's scripts, styles, entry document and
// ranged font fetches all arrive through this public file server, so a jail here breaks the UI.
func TestExtensionsAssetRouteServes(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "acme", "theme")
	if err := os.MkdirAll(entry, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(entry, "index.html"), "<html><body>entry</body></html>")
	write(filepath.Join(entry, "styles.css"), "body{color:red}")
	write(filepath.Join(entry, "font.woff2"), strings.Repeat("x", 200))

	h := http.FileServer(noListingFS{noEscapeFS{dir: root}})

	t.Run("a plain asset", func(t *testing.T) {
		rr := serveExtensions(t, h, "/acme/theme/styles.css")
		if rr.Code != http.StatusOK || rr.Body.String() != "body{color:red}" {
			t.Errorf("css = %d %q", rr.Code, rr.Body.String())
		}
	})
	t.Run("an extension entry document", func(t *testing.T) {
		rr := serveExtensions(t, h, "/acme/theme/")
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "entry") {
			t.Errorf("entry = %d %q", rr.Code, rr.Body.String())
		}
	})
	t.Run("a ranged fetch", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/acme/theme/font.woff2", nil)
		req.Header.Set("Range", "bytes=0-9")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusPartialContent || rr.Body.Len() != 10 {
			t.Errorf("range = %d %d bytes, want 206 and 10", rr.Code, rr.Body.Len())
		}
	})
	t.Run("a directory with no entry document", func(t *testing.T) {
		if rr := serveExtensions(t, h, "/acme/"); rr.Code != http.StatusNotFound {
			t.Errorf("directory listing = %d, want 404", rr.Code)
		}
	})
}

func serveExtensions(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
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
	rr := answeredIn(t, 10*time.Second, "GET /extensions/acme/theme.css on a FIFO", func() *httptest.ResponseRecorder {
		return serveExtensions(t, h, "/acme/theme.css")
	})
	if rr.Code != http.StatusNotFound {
		t.Errorf("a FIFO at a served name = %d, want 404: %s", rr.Code, rr.Body.String())
	}
}
