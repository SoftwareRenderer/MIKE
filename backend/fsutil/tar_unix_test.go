//go:build unix

package fsutil

import (
	"bytes"
	"context"
	"path/filepath"
	"syscall"
	"testing"
)

// A named pipe left in a working tree is an ordinary thing to find there. Reading one would park the
// download on a writer that never comes, so an irregular entry is skipped and the folder still arrives.
func TestWriteTarGzSkipsAnIrregularFile(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "proj", "keep.txt"), "kept")
	if err := syscall.Mkfifo(filepath.Join(dir, "proj", "pipe"), 0o644); err != nil {
		t.Skipf("no fifo here: %v", err)
	}

	var buf bytes.Buffer
	if err := WriteTarGz(context.Background(), &buf, dir, []string{filepath.Join(dir, "proj")}); err != nil {
		t.Fatalf("WriteTarGz: %v", err)
	}

	entries := unpack(t, &buf)
	if got := entry(t, entries, "proj/keep.txt").body; got != "kept" {
		t.Fatalf("proj/keep.txt body: %q", got)
	}
	if _, seen := entries["proj/pipe"]; seen {
		t.Fatal("the named pipe went into the archive")
	}
}
