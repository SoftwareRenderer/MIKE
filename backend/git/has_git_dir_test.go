package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestHasGitDir pins the cheap check used for per-directory listing badges.
func TestHasGitDir(t *testing.T) {
	t.Parallel()
	base := t.TempDir()

	plain := filepath.Join(base, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	normal := filepath.Join(base, "normal")
	if err := os.MkdirAll(filepath.Join(normal, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A linked worktree keeps a .git file ("gitdir: …"), not a directory.
	worktreeLike := filepath.Join(base, "worktree")
	if err := os.MkdirAll(worktreeLike, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeLike, ".git"), []byte("gitdir: /elsewhere/.git/worktrees/wt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A dangling .git symlink still counts: this checks existence, not validity.
	dangling := filepath.Join(base, "dangling")
	if err := os.MkdirAll(dangling, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "nowhere"), filepath.Join(dangling, ".git")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"plain directory", plain, false},
		{"repository root", normal, true},
		{"linked worktree (.git file)", worktreeLike, true},
		{"dangling .git symlink", dangling, true},
		{"missing directory", filepath.Join(base, "nope"), false},
	}
	for _, tt := range tests {
		if got := HasGitDir(tt.path); got != tt.want {
			t.Errorf("HasGitDir(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestHasGitDirNarrowerThanIsGitRepo: rev-parse walks up the tree, the stat does
// not. A caller that needs the walk-up answer asks IsGitRepo once for the
// directory it is listing, and then knows every entry inside is in that repo.
func TestHasGitDirNarrowerThanIsGitRepo(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	if !IsGitRepo(filepath.Join(repo, "internal")) {
		t.Fatal("fixture: rev-parse should report a subdirectory as inside a repo")
	}
	if HasGitDir(filepath.Join(repo, "internal")) {
		t.Error("HasGitDir must not follow the tree up — it answers about the directory itself")
	}
	if !HasGitDir(repo) {
		t.Error("HasGitDir(repo root) = false")
	}
}
