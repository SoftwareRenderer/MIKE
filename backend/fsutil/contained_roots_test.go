package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

// TestContainedRootsAreNotWidened: the variable that widens the picker must not widen a
// project jail, and no home directory creeps in as a root the caller never named.
func TestContainedRootsAreNotWidened(t *testing.T) {
	dir := t.TempDir()
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		if _, err := NewContainedRoots(dir).Stat(home); err == nil {
			t.Fatal("the home directory is a root of a project-scoped jail")
		}
	}

	t.Setenv(AllowRootFSEnv, "1")
	other := t.TempDir()
	if _, err := NewContainedRoots(dir).Stat(other); err == nil {
		t.Fatal("MIKE_ALLOW_ROOT_FS widened a project-scoped root set")
	}
	// The variable still widens the jail it was written for, so the refusal above is this rule.
	if !NewHostRoot(dir).Unrestricted() {
		t.Fatal("MIKE_ALLOW_ROOT_FS stopped widening the host jail")
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
