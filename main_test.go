package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOpenTraceFileIsOwnerOnly covers L8: the flight recorder is always-on and
// holds ~30s of runtime internals, so both the initial file and the rotated
// replacement must be created 0600.
func TestOpenTraceFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kanban.trace")

	f, err := openTraceFile(path)
	if err != nil {
		t.Fatalf("openTraceFile: %v", err)
	}
	// Rotate exactly like the 30s ticker does: truncate the same path again.
	f.Close()
	f2, err := openTraceFile(path)
	if err != nil {
		t.Fatalf("openTraceFile (rotate): %v", err)
	}
	defer f2.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("trace file mode = %#o, want 0600", perm)
	}
}
