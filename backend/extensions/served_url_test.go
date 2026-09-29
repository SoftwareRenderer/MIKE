package extensions

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// The browser must be told where an extension's files actually live, and it is
// told from the same derivation the server reads, deletes and repairs through.
// The case that started it: an unpublished pack placed flat at
// <extensions>/<name>, which no client can infer from a manifest's author string.
//
// These go through `toResponse` rather than a helper, because the contract is the
// API field a browser reads, and `toResponse` is the only thing that builds it.
func TestExtensionResponseServedURL(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	manifest := manifestBody("Anyone", "0.0.1")

	// Namespaced layout, written both ways: the record may hold the author as it
	// arrived ("SoftwareRenderer") or already slugged, and both read the same dir.
	writeManifest(t, root, "softwarerenderer", "zh", manifest)
	// Flat: author "local", one level under the root.
	writeManifest(t, root, "", "probe-pack", manifest)
	// Both layouts for one name: the namespaced one wins, as extensionDir says.
	writeManifest(t, root, "local", "dupe", manifest)
	writeManifest(t, root, "", "dupe", manifest)

	tests := []struct {
		name   string
		author string
		ext    string
		want   string
	}{
		{"author-namespaced", "softwarerenderer", "zh", "/extensions/softwarerenderer/zh"},
		{"author-as-it-arrived", "SoftwareRenderer", "zh", "/extensions/softwarerenderer/zh"},
		{"flat-under-root", "local", "probe-pack", "/extensions/probe-pack"},
		{"local-prefers-the-namespaced-directory", "local", "dupe", "/extensions/local/dupe"},
		// Nothing on disk: the standard path is still the answer, because that is
		// where a repair would re-check the extension out to.
		{"nothing-on-disk", "local", "absent", "/extensions/local/absent"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewExtensionHandler(nil, nil, nil, &ExtensionConfig{ExtensionsDir: root})
			resp := h.toResponse(&Extension{
				ID: "x", Name: tc.ext, Author: tc.author, InstallSource: "local",
				Manifest: ExtensionManifest{Author: "Anyone", Version: "0.0.1"},
			}, nil)

			if resp.ServedURL != tc.want {
				t.Errorf("served_url = %q, want %q", resp.ServedURL, tc.want)
			}
			// The on-disk path shown in Settings is the same directory, not a
			// second guess at it.
			if wantDisk := filepath.Join(root, filepath.FromSlash(tc.want[len("/extensions/"):])); resp.LocalPath != wantDisk {
				t.Errorf("local_path = %q, want the same directory as served_url (%q)", resp.LocalPath, wantDisk)
			}
			// And it reaches the browser as a JSON field.
			body, err := json.Marshal(resp)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded["served_url"] != tc.want {
				t.Errorf("served_url in JSON = %v, want %q", decoded["served_url"], tc.want)
			}
		})
	}
}

// The directory-to-URL step on its own: escaping, and the containment that keeps a
// stored name from producing a URL outside the installed-extension tree.
func TestServedURLFromDirEscapesAndStaysInside(t *testing.T) {
	t.Parallel()
	// A name with characters that would otherwise reshape the URL is escaped, so
	// the browser asks for the file rather than for a different path.
	if got := servedURLFromDir("/ext", "/ext/acme/my theme"); got != "/extensions/acme/my%20theme" {
		t.Errorf("space in name: got %q", got)
	}
	// A name cannot climb out: `..` walks the joined path back up, and the root
	// itself has no URL (it is served as a listing-less 404).
	for _, dir := range []string{"/ext/acme/../../etc", "/ext/acme/..", "/ext", "/other/place"} {
		if got := servedURLFromDir("/ext", filepath.FromSlash(dir)); got != "" {
			t.Errorf("directory %q produced %q, want \"\" (no URL outside the tree)", dir, got)
		}
	}
	// A dot-prefixed name is a name, not a traversal.
	if got := servedURLFromDir("/ext", "/ext/acme/.hidden"); got != "/extensions/acme/.hidden" {
		t.Errorf("dot-prefixed name: got %q", got)
	}
	// A relative extensions root still yields a rooted URL — the static tree is
	// registered as `extensions` against the working directory.
	if got := servedURLFromDir("extensions", filepath.Join("extensions", "acme", "pack")); got != "/extensions/acme/pack" {
		t.Errorf("relative root: got %q", got)
	}
}
