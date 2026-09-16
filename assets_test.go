package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// writeDistDir returns a directory holding the given files.
func writeDistDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte("<html>served from disk</html>"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func readDocument(t *testing.T, assets fs.FS) string {
	t.Helper()
	body, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	return string(body)
}

// The dev loop is `npm run build` with no Go rebuild, which needs the directory to win over the
// compiled-in copy.
func TestDevDirectoryIsTheOneServed(t *testing.T) {
	dir := writeDistDir(t, "index.html")
	embedded := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>compiled in</html>")}}

	assets, source, err := chooseAssets(dir, embedded)
	if err != nil {
		t.Fatalf("chooseAssets: %v", err)
	}
	if got := readDocument(t, assets); got != "<html>served from disk</html>" {
		t.Errorf("served %q, want the directory's document", got)
	}
	if source != dir {
		t.Errorf("source = %q, want the directory named so the log can say which copy answers", source)
	}
}

// After `make clean` the configured directory holds no document and `make run` still comes up, naming
// what it fell back to.
func TestMissingDevDirectoryFallsBackToTheCompiledCopy(t *testing.T) {
	embedded := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>compiled in</html>")}}

	for name, devDir := range map[string]string{
		"absent":              filepath.Join(t.TempDir(), "never-built"),
		"present but empty":   writeDistDir(t, "assets/app.js"),
		"index.html is a dir": writeDistDir(t, "index.html/not-a-document"),
	} {
		t.Run(name, func(t *testing.T) {
			assets, source, err := chooseAssets(devDir, embedded)
			if err != nil {
				t.Fatalf("chooseAssets: %v", err)
			}
			if got := readDocument(t, assets); got != "<html>compiled in</html>" {
				t.Errorf("served %q, want the compiled-in document", got)
			}
			if !strings.Contains(source, devDir) || !strings.Contains(source, "make frontend") {
				t.Errorf("source = %q, want it to name the directory and the command that fixes it", source)
			}
		})
	}
}

// With no document anywhere the boot says so instead of answering 500 per request.
func TestNoDocumentAnywhereIsABootError(t *testing.T) {
	empty := fstest.MapFS{}

	t.Run("with a dev directory configured", func(t *testing.T) {
		dir := writeDistDir(t, "assets/app.js")
		if _, _, err := chooseAssets(dir, empty); err == nil {
			t.Fatal("chooseAssets: got a server with no document, want an error")
		} else if !strings.Contains(err.Error(), "make frontend") {
			t.Errorf("error %q does not name the command that fixes it", err)
		}
	})

	t.Run("with none, which is a release build", func(t *testing.T) {
		if _, _, err := chooseAssets("", empty); err == nil {
			t.Fatal("chooseAssets: got a server with no document, want an error")
		}
	})
}

// An empty devDir is what a `-tags release` build resolves to.
func TestReleaseBuildServesTheCompiledCopy(t *testing.T) {
	embedded := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>compiled in</html>")}}

	assets, source, err := chooseAssets("", embedded)
	if err != nil {
		t.Fatalf("chooseAssets: %v", err)
	}
	if got := readDocument(t, assets); got != "<html>compiled in</html>" {
		t.Errorf("served %q, want the compiled-in document", got)
	}
	if source != "compiled in" {
		t.Errorf("source = %q, want it to say where the document came from", source)
	}
}
