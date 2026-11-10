package fsutil

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// file writes a file of n bytes.
func file(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// The budget is checked before an entry is priced, so a stopped walk reports what it counted.
func TestTreeUsageForCountsEveryRegularFileUnderTheTree(t *testing.T) {
	dir := t.TempDir()
	file(t, filepath.Join(dir, "top.txt"), 100)
	if err := os.MkdirAll(filepath.Join(dir, "nested", "deeper"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	file(t, filepath.Join(dir, "nested", "one.txt"), 40)
	file(t, filepath.Join(dir, "nested", "deeper", "two.txt"), 7)

	usage, err := TreeUsageFor(dir, WalkBudget{})
	if err != nil {
		t.Fatalf("TreeUsageFor(%q): %v", dir, err)
	}
	if usage.Bytes != 147 {
		t.Fatalf("bytes = %d, want 147", usage.Bytes)
	}
	if !usage.Complete {
		t.Fatal("a walk under no budget is incomplete")
	}
}

// A worktree's .git is a pointer file, and an unpacked tree can hold a link to anywhere.
func TestTreeUsageForNeverFollowsASymlinkOutOfTheTree(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	file(t, outside, 4096)
	file(t, filepath.Join(dir, "real.txt"), 5)
	if err := os.Symlink(outside, filepath.Join(dir, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "dangling")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	usage, err := TreeUsageFor(dir, WalkBudget{})
	if err != nil {
		t.Fatalf("TreeUsageFor(%q): %v", dir, err)
	}
	if usage.Bytes != 5 {
		t.Fatalf("bytes = %d, want only the 5 counted from the tree itself", usage.Bytes)
	}
	if !usage.Complete {
		t.Fatal("a link is an entry like any other, not a reason to stop")
	}
}

func TestTreeUsageForReportsAPartialFigureAtTheEntryCap(t *testing.T) {
	dir := t.TempDir()
	for i := range 10 {
		file(t, filepath.Join(dir, string(rune('a'+i))+".txt"), 10)
	}

	usage, err := TreeUsageFor(dir, WalkBudget{MaxEntries: 4})
	if err != nil {
		t.Fatalf("TreeUsageFor(%q): %v", dir, err)
	}
	if usage.Complete {
		t.Fatal("a walk that stopped at the cap is complete")
	}
	if usage.Bytes >= 100 {
		t.Fatalf("bytes = %d, want the walk to have stopped before the whole tree", usage.Bytes)
	}

	whole, err := TreeUsageFor(dir, WalkBudget{MaxEntries: 11})
	if err != nil {
		t.Fatalf("TreeUsageFor(%q): %v", dir, err)
	}
	if !whole.Complete || whole.Bytes != 100 {
		t.Fatalf("one entry past the cap = %+v, want the whole 100 bytes", whole)
	}
}

// A deadline that passes mid-tree is an incomplete figure, not a smaller one.
func TestTreeUsageForReportsIncompleteAtTheDeadline(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for i := range 200 {
		file(t, filepath.Join(dir, string(rune('a'+i%26))+string(rune('a'+i/26))+".txt"), 1)
	}

	usage, err := TreeUsageFor(dir, WalkBudget{Timeout: time.Microsecond})
	if err != nil {
		t.Fatalf("TreeUsageFor(%q): %v", dir, err)
	}
	if usage.Complete {
		t.Fatal("a walk whose deadline passed mid-tree is complete")
	}
}

// A tree that is not there answers an error, not an empty figure: a caller cannot tell an empty
// checkout from a missing one.
func TestTreeUsageForRefusesATreeThatDoesNotExist(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-created")

	usage, err := TreeUsageFor(missing, WalkBudget{})
	if err == nil {
		t.Fatalf("TreeUsageFor(%q) answered %+v, want an error", missing, usage)
	}
	if usage.Bytes != 0 || usage.Complete {
		t.Fatalf("refused walk = %+v, want an empty one", usage)
	}
}

// A directory that cannot be read is not a smaller tree: the figure would be wrong.
func TestTreeUsageForRefusesATreeItCannotRead(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "locked")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	file(t, filepath.Join(blocked, "hidden.txt"), 100)
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })
	if _, err := os.ReadDir(blocked); err == nil {
		t.Skip("this host reads a 0o000 directory anyway (running as root)")
	}

	usage, err := TreeUsageFor(dir, WalkBudget{})
	if err == nil {
		t.Fatalf("TreeUsageFor(%q) answered %+v, want an error", dir, usage)
	}
}
