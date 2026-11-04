package lsp

import "testing"

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
