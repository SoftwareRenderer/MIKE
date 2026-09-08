package server

import (
	"os"
	"path/filepath"
	"testing"
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
