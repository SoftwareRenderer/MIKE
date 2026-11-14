package extensions

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Which situation failed and which half of the read failed are the two things a person can act on:
// "local extension has no manifest.json" means the folder they pointed at, "checked-out extension has no
// manifest.json" means the registry checkout, and "invalid manifest.json in …" means the file is there
// and is not JSON. One reader now answers all four sites, so all four sentences are pinned here.

func manifestDirWith(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return dir
}

func TestReadManifestDecodesWhatTheDirectoryShips(t *testing.T) {
	manifest, err := readManifest(manifestDirWith(t, `{"display_name":"gopls","version":"1.2.3"}`))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if manifest.DisplayName != "gopls" || manifest.Version != "1.2.3" {
		t.Errorf("decoded display_name=%q version=%q", manifest.DisplayName, manifest.Version)
	}
}

func TestManifestErrorSentencesStayWhatTheOperatorIsTold(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent")

	for _, situation := range []string{"local extension", "checked-out extension"} {
		_, err := readManifest(absent)
		if err == nil {
			t.Fatalf("%s: a directory with no manifest.json read successfully", situation)
		}
		got := manifestError(err, situation).Error()
		if want := situation + " has no manifest.json: "; !strings.HasPrefix(got, want) {
			t.Errorf("unreadable manifest: got %q, want it to start with %q", got, want)
		}
		// The repair decides "files are not installed" from this, so wrapping must not bury it.
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: fs.ErrNotExist lost from the read failure (%v)", situation, err)
		}

		corrupt := manifestDirWith(t, "{not json")
		_, err = readManifest(corrupt)
		if err == nil {
			t.Fatalf("%s: a manifest.json that is not JSON read successfully", situation)
		}
		want := "invalid manifest.json in " + situation + ": "
		if got := manifestError(err, situation).Error(); !strings.HasPrefix(got, want) {
			t.Errorf("undecodable manifest: got %q, want it to start with %q", got, want)
		}
		if errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: a manifest that failed to parse was reported as a missing file", situation)
		}
	}
}

func TestReadInstalledManifestSentencesStayWhatTheRepairLogSays(t *testing.T) {
	if _, err := readInstalledManifest(filepath.Join(t.TempDir(), "absent")); err == nil ||
		!strings.HasPrefix(err.Error(), "manifest.json could not be read: ") {
		t.Errorf("absent manifest: got %v, want it to start with %q", err, "manifest.json could not be read: ")
	}
	_, err := readInstalledManifest(manifestDirWith(t, "{not json"))
	if err == nil || !strings.HasPrefix(err.Error(), "manifest.json is not valid JSON: ") {
		t.Errorf("corrupt manifest: got %v, want it to start with %q", err, "manifest.json is not valid JSON: ")
	}
}
