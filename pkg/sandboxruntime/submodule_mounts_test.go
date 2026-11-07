package sandboxruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// subFixture lays out worktreePath as a task worktree holding a populated submodule at subPath, its gitdir
// under the project's shared store.
func subFixture(t *testing.T, worktreePath, projectGitDir, subPath string) {
	t.Helper()
	admin := filepath.Join(projectGitDir, "modules", filepath.FromSlash(subPath), "worktrees", filepath.Base(subPath))
	if err := os.MkdirAll(admin, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(worktreePath, filepath.FromSlash(subPath))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, GitPointerFile), []byte("gitdir: "+admin+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSubmoduleMountsPinsAncestorsAndPointer(t *testing.T) {
	worktree, gitDir, mount := t.TempDir(), t.TempDir(), "/work/example.com"
	subFixture(t, worktree, gitDir, "vendor/x/lib")

	got, err := SubmoduleMounts(worktree, mount, []string{"vendor/x/lib"}, "rw")
	if err != nil {
		t.Fatalf("SubmoduleMounts: %v", err)
	}
	want := []string{
		"-v", filepath.Join(worktree, "vendor") + ":" + mount + "/vendor:rprivate,rw",
		"-v", filepath.Join(worktree, "vendor", "x") + ":" + mount + "/vendor/x:rprivate,rw",
		"-v", filepath.Join(worktree, "vendor", "x", "lib", ".git") + ":" + mount + "/vendor/x/lib/.git:rprivate,ro",
	}
	if !slices.Equal(got, want) {
		t.Errorf("SubmoduleMounts() =\n%q\nwant\n%q", got, want)
	}
}

// TestSubmoduleMountsTopLevelNeedsNoAncestor: a submodule at the worktree root already has an unmovable
// parent — the worktree's own mount point — so binding one more would only cost a mount.
func TestSubmoduleMountsTopLevelNeedsNoAncestor(t *testing.T) {
	worktree, gitDir, mount := t.TempDir(), t.TempDir(), "/work/example.com"
	subFixture(t, worktree, gitDir, "lib")

	got, err := SubmoduleMounts(worktree, mount, []string{"lib"}, "rw")
	if err != nil {
		t.Fatalf("SubmoduleMounts: %v", err)
	}
	want := []string{"-v", filepath.Join(worktree, "lib", ".git") + ":" + mount + "/lib/.git:rprivate,ro"}
	if !slices.Equal(got, want) {
		t.Errorf("SubmoduleMounts() = %q, want only the pointer pin %q", got, want)
	}
}

// TestSubmoduleMountsSharesOneAncestorAcrossSubmodules: two submodules under `vendor` pin `vendor` once —
// podman would otherwise layer the same destination twice.
func TestSubmoduleMountsSharesOneAncestorAcrossSubmodules(t *testing.T) {
	worktree, gitDir, mount := t.TempDir(), t.TempDir(), "/work/example.com"
	subFixture(t, worktree, gitDir, "vendor/a")
	subFixture(t, worktree, gitDir, "vendor/b")

	got, err := SubmoduleMounts(worktree, mount, []string{"vendor/a", "vendor/b"}, "rw")
	if err != nil {
		t.Fatalf("SubmoduleMounts: %v", err)
	}
	vendor := filepath.Join(worktree, "vendor") + ":" + mount + "/vendor:rprivate,rw"
	if count := slices.Index(got, vendor); count == -1 {
		t.Fatalf("the shared ancestor is not mounted at all: %q", got)
	}
	for i, a := range got {
		if a == vendor && i != 1 {
			t.Errorf("the ancestor mount appears more than once: %q", got)
		}
	}
	if len(got) != 6 { // one ancestor + two pointers, each as a "-v" pair
		t.Errorf("SubmoduleMounts() = %q, want three mounts", got)
	}
}

// TestSubmoduleMountsContentModeIsTheWorktreesMode: only a pointer is read-only on a writable worktree,
// and a plan-mode worktree makes everything under it read-only.
func TestSubmoduleMountsContentModeIsTheWorktreesMode(t *testing.T) {
	worktree, gitDir, mount := t.TempDir(), t.TempDir(), "/work/example.com"
	subFixture(t, worktree, gitDir, "vendor/lib")

	ro, err := SubmoduleMounts(worktree, mount, []string{"vendor/lib"}, "ro")
	if err != nil {
		t.Fatalf("SubmoduleMounts: %v", err)
	}
	if !slices.Contains(ro, filepath.Join(worktree, "vendor")+":"+mount+"/vendor:rprivate,ro") {
		t.Errorf("a read-only worktree must mount the submodule read-only: %q", ro)
	}
	for _, mode := range []string{"", "rw,", "mode=rw", "RO"} {
		if got, err := SubmoduleMounts(worktree, mount, []string{"vendor/lib"}, mode); err == nil {
			t.Errorf("SubmoduleMounts(mode %q) = %q, want a refusal: only rw and ro are modes", mode, got)
		}
	}
}

// TestValidateSubmodulePathRefusesWhatBindMountSpecCannotCarry: git accepts these paths in a tree, and a
// `:` or a `,` in one moves the `,ro` suffix that makes a pin read-only.
func TestValidateSubmodulePathRefusesWhatBindMountSpecCannotCarry(t *testing.T) {
	bad := []string{
		"vendor:lib", "vendor,lib", "../outside", "vendor/../../outside", "/vendor/lib",
		"", "vendor//lib", "./vendor", "vendor/./lib", ".git", "vendor/.git", "vendor/lib/",
		"vendor/lib\n", "vendor$(id)", "vendor/lib;rm", "vendor/ lib",
	}
	for _, p := range bad {
		if err := ValidateSubmodulePath(p); err == nil {
			t.Errorf("ValidateSubmodulePath(%q) accepted a path that must not reach a mount spec", p)
		}
	}
	for _, p := range []string{"lib", "vendor/lib", "a/b/c/d", "vendor-1.0/lib_x", ".hidden/lib"} {
		if err := ValidateSubmodulePath(p); err != nil {
			t.Errorf("ValidateSubmodulePath(%q): %v", p, err)
		}
	}
}

// TestSubmoduleMountsRefusesAMissingAncestor: a skipped mount is a container that believes it is pinned.
func TestSubmoduleMountsRefusesAMissingAncestor(t *testing.T) {
	worktree, gitDir, mount := t.TempDir(), t.TempDir(), "/work/example.com"
	subFixture(t, worktree, gitDir, "vendor/lib")
	if err := os.RemoveAll(filepath.Join(worktree, "vendor")); err != nil {
		t.Fatal(err)
	}
	if got, err := SubmoduleMounts(worktree, mount, []string{"vendor/lib"}, "rw"); err == nil {
		t.Fatalf("a missing ancestor must refuse the container, got %q", got)
	}
}

// TestSubmoduleMountsRefusesADirectoryWhereThePointerBelongs: the pin is a bind of a regular file, and
// podman would turn a directory into something else entirely.
func TestSubmoduleMountsRefusesADirectoryWhereThePointerBelongs(t *testing.T) {
	worktree, gitDir, mount := t.TempDir(), t.TempDir(), "/work/example.com"
	subFixture(t, worktree, gitDir, "vendor/lib")
	pointer := filepath.Join(worktree, "vendor", "lib", ".git")
	if err := os.Remove(pointer); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pointer, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := SubmoduleMounts(worktree, mount, []string{"vendor/lib"}, "rw"); err == nil {
		t.Fatalf("a directory where the pointer belongs must refuse the container, got %q", got)
	}
}

func TestSubmoduleMountsRefusesMoreThanItCanPin(t *testing.T) {
	worktree, gitDir, mount := t.TempDir(), t.TempDir(), "/work/example.com"
	var paths []string
	for i := range maxSubmoduleMounts + 1 {
		p := fmt.Sprintf("vendor/lib%d", i)
		subFixture(t, worktree, gitDir, p)
		paths = append(paths, p)
	}
	if _, err := SubmoduleMounts(worktree, mount, paths, "rw"); err == nil {
		t.Fatal("more submodules than the container can pin must be refused, not truncated")
	}
}

// TestVerifySubmoduleGitPointerAcceptsTheStoresOwnShape mirrors what `git worktree add` inside the
// project's shared store leaves behind.
func TestVerifySubmoduleGitPointerAcceptsTheStoresOwnShape(t *testing.T) {
	worktree, gitDir := t.TempDir(), t.TempDir()
	subFixture(t, worktree, gitDir, "vendor/lib")
	if err := VerifySubmoduleGitPointer(worktree, "vendor/lib", gitDir); err != nil {
		t.Errorf("VerifySubmoduleGitPointer: %v", err)
	}
}

// TestVerifySubmoduleGitPointerRefusesAHandedPointer: every form of "the guest chose the gitdir" — a
// relative walk out, an absolute path elsewhere, and a sibling directory whose name merely starts with
// `modules`.
func TestVerifySubmoduleGitPointerRefusesAHandedPointer(t *testing.T) {
	cases := map[string]string{
		"relative escape":  "gitdir: ./evil\n",
		"absolute outside": "gitdir: /home/attacker/.git\n",
		"not a pointer":    "/home/attacker/.git\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			worktree, gitDir := t.TempDir(), t.TempDir()
			subFixture(t, worktree, gitDir, "vendor/lib")
			if err := os.WriteFile(filepath.Join(worktree, "vendor", "lib", ".git"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := VerifySubmoduleGitPointer(worktree, "vendor/lib", gitDir); err == nil {
				t.Errorf("pointer %q was accepted", strings.TrimSpace(content))
			}
		})
	}

	// Text that keeps the store prefix and walks out of it with `..` names a directory git reaches after
	// its own cleaning, so the raw prefix is not what may be trusted.
	worktree, root := t.TempDir(), t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "modules", "vendor", "lib", "worktrees", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(gitDir, "modules", "..", "modulesEvil"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(worktree, "vendor", "lib")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	walkOut := filepath.Join(gitDir, "modules") + "/../modulesEvil"
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+walkOut+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifySubmoduleGitPointer(worktree, "vendor/lib", gitDir); err == nil {
		t.Errorf("a pointer that keeps the store prefix and walks out of it was accepted: %q", walkOut)
	}

	// A store directory beside `modules`, not under it: the prefix test alone would pass it.
	if err := os.MkdirAll(filepath.Join(gitDir, "modulesEvil", "vendor", "lib", "worktrees", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(gitDir, "modulesEvil", "vendor", "lib", "worktrees", "lib")
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+target+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifySubmoduleGitPointer(worktree, "vendor/lib", gitDir); err == nil {
		t.Error("a gitdir beside the project's module store was accepted; only a path under it counts")
	}
}
