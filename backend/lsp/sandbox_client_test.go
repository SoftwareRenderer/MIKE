package lsp

import (
	"bytes"
	"os"
	"testing"
)

// TestNewSandboxClientMapsAreWritable: ensureDocSync writes the three document maps
// unconditionally, so a client built with a struct literal dies on the first tool call.
func TestNewSandboxClientMapsAreWritable(t *testing.T) {
	sc := newSandboxClient(nil, "/work/tree", "go", "ext-1", map[string]string{"/work/tree": "/mnt/tree"})

	sc.openedFiles["file:///work/tree/main.go"] = true
	sc.openedFileContents["file:///work/tree/main.go"] = "package main"
	sc.openedFileVersions["file:///work/tree/main.go"] = 1

	if !sc.openedFiles["file:///work/tree/main.go"] {
		t.Error("openedFiles did not retain the write")
	}
	if got := sc.openedFileContents["file:///work/tree/main.go"]; got != "package main" {
		t.Errorf("openedFileContents = %q, want %q", got, "package main")
	}
	if got := sc.openedFileVersions["file:///work/tree/main.go"]; got != 1 {
		t.Errorf("openedFileVersions = %d, want 1", got)
	}
	if sc.workspace != "/work/tree" || sc.language != "go" || sc.extensionID != "ext-1" {
		t.Errorf("identity fields not carried through: %+v", sc)
	}
	if mount := sc.projectMounts["/work/tree"]; mount != "/mnt/tree" {
		t.Errorf("projectMounts = %q, want %q", mount, "/mnt/tree")
	}
	if sc.initialized {
		t.Error("a fresh client must not claim to be initialized")
	}
}

// TestSandboxClientBuiltOnlyThroughConstructor keeps the two construction sites from drifting
// apart again: a composite literal outside the constructor is how the nil maps came back, and
// a functional test on the constructor alone cannot see one being added elsewhere.
func TestSandboxClientBuiltOnlyThroughConstructor(t *testing.T) {
	src, err := os.ReadFile("extension_manager.go")
	if err != nil {
		t.Fatalf("read extension_manager.go: %v", err)
	}

	start := bytes.Index(src, []byte("func newSandboxClient("))
	if start < 0 {
		t.Fatal("newSandboxClient not found; the constructor must stay the only way to build one")
	}
	end := bytes.Index(src[start:], []byte("\n}\n"))
	if end < 0 {
		t.Fatal("could not find the end of newSandboxClient")
	}
	constructor := start + end + len("\n}\n")

	for i := 0; ; {
		idx := bytes.Index(src[i:], []byte("&sandboxClient{"))
		if idx < 0 {
			break
		}
		abs := i + idx
		if abs < start || abs > constructor {
			t.Errorf("&sandboxClient{ at byte %d is outside newSandboxClient (%d-%d); use newSandboxClient so the document maps are initialized",
				abs, start, constructor)
		}
		i = abs + len("&sandboxClient{")
	}
}
