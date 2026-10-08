//go:build unix

package fsutil

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// walkTree's irregular-file rule on the copy side: a named pipe is left out rather than opened.
func TestCopyTreeSkipsAnIrregularFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "proj")
	writeTestFile(t, filepath.Join(src, "keep.txt"), "kept")
	if err := syscall.Mkfifo(filepath.Join(src, "pipe"), 0o644); err != nil {
		t.Skipf("no fifo here: %v", err)
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
}
