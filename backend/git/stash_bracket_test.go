package git

// WithStash's whole job is that the merge never runs when the push failed: a bracketed section
// begins with `git checkout -f`, which reverts tracked modifications to HEAD with exit 0.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// blockStashRef makes `git stash push` fail while leaving the repository usable — a directory where
// the stash ref belongs. An index lock is the obvious injection and the wrong one: it blocks the
// forced checkout too, so a test built on it passes even when the bracket proceeds and loses work.
func blockStashRef(t *testing.T, dir string) (release func()) {
	t.Helper()
	commonDir := strings.TrimSpace(runGitOutput(t, dir, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	blocked := filepath.Join(commonDir, "refs", "stash")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatalf("block the stash ref: %v", err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "blocker"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("block the stash ref: %v", err)
	}
	return func() { _ = os.RemoveAll(blocked) }
}

// TestWithStashDoesNotRunTheWorkWhenTheStashFails: with the push refused, the work is still in the
// working tree and the caller never reaches the forced checkout.
func TestWithStashDoesNotRunTheWorkWhenTheStashFails(t *testing.T) {
	repoDir, projID, _, _, _, wm := setupStashTest(t)
	release := blockStashRef(t, repoDir)
	defer release()
	writeFile(t, repoDir, "README.md", "uncommitted edit to a tracked file\n")

	ran := false
	err := wm.WithStash(projID, repoDir, StashMessageForAutoMerge("task-1"), func() error {
		ran = true
		// What both merge brackets do first, and what reverts the edit above to HEAD.
		return wm.GitRunInDir(repoDir, "checkout", "-f", "master")
	})

	if err == nil {
		t.Fatal("WithStash reported success while the stash was refused")
	}
	if ran {
		t.Fatal("the work ran with no stash behind it — the checkout inside it discards the tree")
	}

	if status := runGitOutput(t, repoDir, "status", "--porcelain"); !strings.Contains(status, "README.md") {
		t.Errorf("the edit is gone from the working tree: status=%q", status)
	}
	if content, readErr := os.ReadFile(filepath.Join(repoDir, "README.md")); readErr != nil ||
		!strings.Contains(string(content), "uncommitted edit") {
		t.Errorf("the edit was reverted by the bracket: %q (%v)", string(content), readErr)
	}
}

// TestWithStashRunsAndRestoresWithNothingToStash: a clean tree is not a failure. git took nothing,
// so there is nothing to restore and nothing on the stack may be touched — the entry that is
// already there belongs to somebody else.
func TestWithStashRunsAndRestoresWithNothingToStash(t *testing.T) {
	repoDir, projID, _, _, _, wm := setupStashTest(t)
	userHash := seedUserStash(t, wm, repoDir)

	ran := false
	err := wm.WithStash(projID, repoDir, StashMessageForAutoMerge("clean-task"), func() error {
		ran = true
		return nil
	})

	if err != nil {
		t.Fatalf("nothing to stash is not a failure: %v", err)
	}
	if !ran {
		t.Error("the work was skipped because the tree was clean")
	}
	if entries, _ := wm.listStashEntries(repoDir); len(entries) != 1 || entries[0].Hash != userHash {
		t.Errorf("the restore consumed an entry that was never ours: %v", entries)
	}
}

// TestWithStashRestoresTheEntryItCreated: ours comes back, the user's stays on the stack.
func TestWithStashRestoresTheEntryItCreated(t *testing.T) {
	repoDir, projID, _, _, _, wm := setupStashTest(t)
	userHash := seedUserStash(t, wm, repoDir)
	writeFile(t, repoDir, "task.txt", "in-flight work\n")

	parked := false
	err := wm.WithStash(projID, repoDir, StashMessageForAutoMerge("task-1"), func() error {
		_, statErr := os.Stat(filepath.Join(repoDir, "task.txt"))
		parked = os.IsNotExist(statErr)
		return nil
	})
	if err != nil {
		t.Fatalf("WithStash: %v", err)
	}
	if !parked {
		t.Error("the work was still in the working tree while the merge ran — nothing was stashed")
	}

	content, err := os.ReadFile(filepath.Join(repoDir, "task.txt"))
	if err != nil || string(content) != "in-flight work\n" {
		t.Errorf("the work did not come back: %q (%v)", string(content), err)
	}
	if entries, _ := wm.listStashEntries(repoDir); len(entries) != 1 || entries[0].Hash != userHash {
		t.Errorf("the user's stash entry was consumed by our bracket: %v", entries)
	}
}

// TestWithStashLeavesAnUnappliableEntry: a restore that conflicts must not also delete the saved
// state, whatever the caller does next.
func TestWithStashLeavesAnUnappliableEntry(t *testing.T) {
	repoDir, projID, _, _, _, wm := setupStashTest(t)
	wt := stashWorktree(t, wm, repoDir, projID, "bracket-one-opal")

	writeFile(t, wt, "shared.txt", "stashed version\n")
	err := wm.WithStash(projID, wt, StashMessageForAutoMerge("bracket-one-opal"), func() error {
		// The merge lands its own version of the same path, so the apply cannot go through.
		writeFile(t, wt, "shared.txt", "competing version\n")
		return nil
	})

	if err == nil {
		t.Fatal("the bracket reported success with the entry left unapplied")
	}
	if n := countStashEntries(t, repoDir); n != 1 {
		t.Errorf("%d entries left, want 1 — the unapplied entry was dropped, and that is lost work", n)
	}
}

// TestWithStashKeepsTheEntryForTheResolvingTurn pins the asymmetry the sync-merge bracket needs:
// a conflicted sync leaves the entry stacked for the turn that runs `merge --continue`, which finds
// it by label. Popping it here would hand that turn a clean-looking worktree missing its own edits.
func TestWithStashKeepsTheEntryForTheResolvingTurn(t *testing.T) {
	repoDir, projID, _, _, _, wm := setupStashTest(t)
	wt := stashWorktree(t, wm, repoDir, projID, "bracket-two-topaz")

	writeFile(t, wt, "shared.txt", "unstaged work\n")
	err := wm.WithStash(projID, wt, StashMessageForSyncMerge("bracket-two-topaz"), func() error {
		return fmt.Errorf("merge conflict syncing master: %w", ErrKeepStash)
	})

	if !errors.Is(err, ErrKeepStash) {
		t.Fatalf("WithStash returned %v; the caller has to be able to tell a kept entry from a failure", err)
	}
	if n := countStashEntries(t, repoDir); n != 1 {
		t.Errorf("%d entries left, want 1 — the resolving turn has nothing to restore", n)
	}
	if _, statErr := os.Stat(filepath.Join(wt, "shared.txt")); statErr == nil {
		t.Error("the work was re-applied onto a merge in progress")
	}
}
