package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

// TestContainedRootsAreNotWidened: the widening the picker honors must not reach a project jail, and
// no home directory creeps in as a root the caller never named.
func TestContainedRootsAreNotWidened(t *testing.T) {
	dir := t.TempDir()
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		if _, err := NewContainedRoots(dir).Stat(home); err == nil {
			t.Fatal("the home directory is a root of a project-scoped jail")
		}
	}

	other := t.TempDir()
	if _, err := NewContainedRoots(dir).Stat(other); err == nil {
		t.Fatal("a project-scoped root set was widened")
	}
	// NewContainedRoots has no parameter for the widening — that is the rule, not an omission — while
	// the host jail takes it, so the refusal above is this rule rather than a check that never fires.
	if !NewHostRoot(true, dir).Unrestricted() {
		t.Fatal("the host jail stopped honoring the widening it was built with")
	}
}

// TestContainedRootsMkdirAllRefusesSymlink is the property writeContainedFile depends on.
func TestContainedRootsMkdirAllRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	jailed := NewContainedRoots(dir)
	if err := jailed.MkdirAll(filepath.Join(dir, "a", "b"), 0o755); err != nil {
		t.Fatalf("an ordinary chain was refused: %v", err)
	}
	if err := jailed.MkdirAll(filepath.Join(dir, "link", "deep"), 0o755); err == nil {
		t.Fatal("a chain through a symlink was created")
	} else if !IsHostRootRefusal(err) {
		t.Errorf("the refusal should be a jail refusal, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "deep")); !os.IsNotExist(err) {
		t.Fatal("directories were created outside the root")
	}
}
