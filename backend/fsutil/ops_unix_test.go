//go:build unix

package fsutil

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestWalkTreeLeavesIrregularFilesOut is one rule with two callers: a named pipe is an ordinary thing to
// find in a tree a person did not write, and opening one parks the caller on a writer that never comes.
// The host paste leaves it out of the copy; the folder download leaves it out of the archive and still
// delivers the folder. Both go through walkTree's mode test, so one regression breaks both.
func TestWalkTreeLeavesIrregularFilesOut(t *testing.T) {
	t.Run("the copy", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "proj")
		writeTestFile(t, filepath.Join(src, "keep.txt"), "kept")
		if err := syscall.Mkfifo(filepath.Join(src, "pipe"), 0o644); err != nil {
			t.Fatalf("plant the named pipe this walk must leave out: %v", err)
		}
		dst := filepath.Join(t.TempDir(), "copy")

		if err := CopyTree(src, dst); err != nil {
			t.Fatalf("CopyTree: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dst, "keep.txt"))
		if err != nil || string(got) != "kept" {
			t.Fatalf("keep.txt: %q, %v", got, err)
		}
		if _, err := os.Lstat(filepath.Join(dst, "pipe")); !os.IsNotExist(err) {
			t.Fatalf("the named pipe was copied: %v", err)
		}
	})

	t.Run("the archive", func(t *testing.T) {
		dir := t.TempDir()
		writeTestFile(t, filepath.Join(dir, "proj", "keep.txt"), "kept")
		if err := syscall.Mkfifo(filepath.Join(dir, "proj", "pipe"), 0o644); err != nil {
			t.Fatalf("plant the named pipe this archive must leave out: %v", err)
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
	})
}
