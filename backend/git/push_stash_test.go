package git

// The stash stack is shared by every worktree of the project and holds the user's own entries, so
// PushStash has to identify the entry it created instead of taking whatever sits on top: handing back
// somebody else's entry would store their state as this thread's work, and then drop it.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedUserStash puts one stash entry in the repo the way a user would, and returns its hash.
func seedUserStash(t *testing.T, wm *WorktreeManager, repoDir string) string {
	t.Helper()
	writeFile(t, repoDir, "user.txt", "the user's own work\n")
	if err := wm.gitRun(repoDir, "stash", "push", "-u", "-m", "the user's own entry"); err != nil {
		t.Fatalf("seed the user's stash entry: %v", err)
	}
	entries, err := wm.listStashEntries(repoDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one stash entry, got %v (%v)", entries, err)
	}
	return entries[0].Hash
}

func TestPushStashReturnsTheEntryItCreated(t *testing.T) {
	t.Parallel()
	repoDir, _, _, _, _, wm := setupStashTest(t)
	userHash := seedUserStash(t, wm, repoDir)

	writeFile(t, repoDir, "task.txt", "in-flight work\n")
	got, err := wm.PushStash(repoDir, "mike-switch-task")
	if err != nil {
		t.Fatalf("PushStash: %v", err)
	}
	if got == "" || got == userHash {
		t.Fatalf("PushStash returned %q, want the entry it created (the user's is %q)", got, userHash)
	}

	entries, err := wm.listStashEntries(repoDir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("both entries should be listed, got %v (%v)", entries, err)
	}
	// Releasing our entry is by hash for the same reason: the user's has to survive it.
	if err := wm.DropStashByHash(repoDir, got); err != nil {
		t.Fatalf("drop our entry: %v", err)
	}
	if entries, _ = wm.listStashEntries(repoDir); len(entries) != 1 || entries[0].Hash != userHash {
		t.Errorf("dropping our entry took the user's with it: %v", entries)
	}
}

// A clean worktree makes `git stash push` exit successfully without creating anything. That is not a saved
// state, and the entry already in the list is not ours.
func TestPushStashWithNothingToStashNamesNoEntry(t *testing.T) {
	t.Parallel()
	repoDir, _, _, _, _, wm := setupStashTest(t)
	userHash := seedUserStash(t, wm, repoDir)

	got, err := wm.PushStash(repoDir, "mike-switch-task")
	if !errors.Is(err, ErrNothingToStash) {
		t.Errorf("PushStash error = %v, want ErrNothingToStash", err)
	}
	if got != "" {
		t.Errorf("PushStash returned %q for nothing to stash; the only entry is the user's (%q)", got, userHash)
	}
	if entries, _ := wm.listStashEntries(repoDir); len(entries) != 1 || entries[0].Hash != userHash {
		t.Errorf("the user's stash list changed: %v", entries)
	}
}

// Why git refused has to survive into the error: it is what the user is shown when a switch or a fork is
// refused. A lock held by another git process is the ordinary way this happens.
func TestPushStashCarriesGitReason(t *testing.T) {
	t.Parallel()
	repoDir, _, _, _, _, wm := setupStashTest(t)
	writeFile(t, repoDir, "task.txt", "in-flight work\n")

	gitDir := strings.TrimSpace(runGitOutput(t, repoDir, "rev-parse", "--absolute-git-dir"))
	lock := filepath.Join(gitDir, "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatalf("block git: %v", err)
	}
	_, err := wm.PushStash(repoDir, "mike-switch-task")
	_ = os.Remove(lock)

	if err == nil {
		t.Fatal("PushStash succeeded with the index locked")
	}
	if !strings.Contains(err.Error(), "index.lock") {
		t.Errorf("error %q does not say why git refused", err)
	}
}
