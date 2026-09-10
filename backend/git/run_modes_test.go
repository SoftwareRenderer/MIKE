package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The six exported GitRun* spellings are three places × two modes, and the modes are not interchangeable:
// gitRun pins GIT_DIR=<dir>/.git, gitRunOutput runs plain. That is the only difference between the silent
// and the output spelling of the same place, so it is pinned here in both directions — equal where every
// caller lives, and deliberately different where no caller may go. Merging the spellings on the theory that
// they are one mechanism (as the Phase 0 review predicted) would silently move 16 write call sites from
// one row of this table to the other.

func TestGitRunModesResolveIdenticallyAtRepositoryRoots(t *testing.T) {
	skipWithoutGit(t)
	wm := &WorktreeManager{}
	repo := initRepo(t)
	worktree := filepath.Join(filepath.Dir(repo), "wt")
	if err := wm.gitRun(repo, "worktree", "add", "-q", "-b", "taskbr", worktree); err != nil {
		t.Fatalf("worktree add: %v", err)
	}

	// A linked worktree keeps a .git *file* ("gitdir: …"), and pinned mode follows that pointer: it answers
	// about the worktree, not the checkout the pointer leads to.
	for _, dir := range []string{repo, worktree} {
		for _, probe := range []struct{ flag, want string }{{"--show-toplevel", dir}, {"--abbrev-ref", ""}} {
			plain, plainErr := wm.gitRunOutput(dir, "rev-parse", probe.flag, "HEAD")
			pinned, pinnedErr := wm.gitRunInGitDir(dir, "rev-parse", probe.flag, "HEAD")
			if plainErr != nil || pinnedErr != nil {
				t.Fatalf("%s %s: plain=%v pinned=%v", filepath.Base(dir), probe.flag, plainErr, pinnedErr)
			}
			if strings.TrimSpace(plain) != strings.TrimSpace(pinned) {
				t.Errorf("%s: rev-parse %s plain=%q pinned=%q — the two modes must agree at a repository root",
					filepath.Base(dir), probe.flag, strings.TrimSpace(plain), strings.TrimSpace(pinned))
			}
		}
	}

	// And a write through pinned mode lands in the worktree's own index, which is the claim that actually
	// matters: the alternative is a task's staged files appearing in the project checkout.
	if err := os.WriteFile(filepath.Join(worktree, "n.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := wm.gitRun(worktree, "add", "--", "n.txt"); err != nil {
		t.Fatalf("git add through the pinned mode: %v", err)
	}
	inWorktree := strings.TrimSpace(mustRun(t, wm, worktree))
	inCheckout := strings.TrimSpace(mustRun(t, wm, repo))
	if inWorktree != "n.txt" {
		t.Errorf("staged in the worktree = %q, want n.txt", inWorktree)
	}
	if inCheckout != "" {
		t.Errorf("staged in the project checkout = %q, want nothing — a worktree write must not reach the checkout's index", inCheckout)
	}
}

func mustRun(t *testing.T, wm *WorktreeManager, dir string) string {
	t.Helper()
	out, err := wm.gitRunOutput(dir, "diff", "--cached", "--name-only")
	if err != nil {
		t.Fatalf("diff --cached in %s: %v", dir, err)
	}
	return out
}

// The guard the pin is worth: plain mode follows the tree *up*, so a command aimed at a subdirectory acts
// on the enclosing repository — from the wrong cwd, with pathspecs resolved against a directory the caller
// did not name. Pinned mode refuses instead. Every current call site passes a repository root; this is what
// keeps it that way.
func TestGitRunPinnedModeRefusesASubdirectoryThatPlainModeWouldWalkUp(t *testing.T) {
	skipWithoutGit(t)
	wm := &WorktreeManager{}
	repo := initRepo(t)
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := wm.gitRunOutput(sub, "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatalf("fixture: plain mode should resolve upward, got %v", err)
	}
	if strings.TrimSpace(out) != repo {
		t.Fatalf("fixture: plain mode resolved to %q, want %q", strings.TrimSpace(out), repo)
	}
	if _, err := wm.gitRunInGitDir(sub, "rev-parse", "--show-toplevel"); err == nil {
		t.Error("pinned mode resolved a subdirectory — the guard that makes every GitRun* call site a repository root is gone")
	}
}

func skipWithoutGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}
