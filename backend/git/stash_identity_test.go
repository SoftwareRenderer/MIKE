package git

// The merge paths stash a task's uncommitted work, merge, and restore it. Two merges can run on one
// project at once, so their pushes interleave on the repository's single stash stack.
//
// `git stash pop` pops stash@{0} and has no idea who pushed it. Measured on git 2.55: A pushes, B pushes,
// A pops - A's worktree receives B's work while A's own entry stays on the stack for B to pop. Both tasks
// carry the other's changes and neither operation reports an error.
//
// PushStash returns the hash of the entry it created and PopStashByHash restores exactly that entry.

import (
	"os"
	"path/filepath"
	"testing"
)

// stashWorktree creates a linked worktree with one committed file, so it can hold uncommitted work of
// its own that is distinguishable from every other worktree's.
func stashWorktree(t *testing.T, wm *WorktreeManager, repoDir, projID, taskID string) string {
	t.Helper()
	branch := "task/" + taskID
	wtPath, _, err := wm.CreateWorktree(projID, taskID, branch, "HEAD")
	if err != nil {
		t.Fatalf("create worktree for %s: %v", taskID, err)
	}
	t.Cleanup(func() { _ = wm.RemoveWorktree(projID, taskID, branch) })
	if err := os.WriteFile(filepath.Join(wtPath, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("seed base: %v", err)
	}
	runGit(t, wtPath, "add", "base.txt")
	runGit(t, wtPath, "commit", "-qm", "base for "+taskID)
	return wtPath
}

// TestInterleavedMergeStashesRestoreTheirOwnWork is the regression for the crossing.
func TestInterleavedMergeStashesRestoreTheirOwnWork(t *testing.T) {
	repoDir, projID, _, _, _, wm := setupStashTest(t)

	wtA := stashWorktree(t, wm, repoDir, projID, "alpha-one-quartz")
	wtB := stashWorktree(t, wm, repoDir, projID, "bravo-two-jade")

	// Each task has uncommitted work in its own worktree, untracked so `-u` must carry it.
	writeFile(t, wtA, "alpha-work.txt", "written by task A\n")
	writeFile(t, wtB, "bravo-work.txt", "written by task B\n")

	// Both merge paths push before merging. Interleaved, exactly as they do under concurrency.
	hashA, err := wm.PushStash(wtA, StashMessageForAutoMerge("alpha-one-quartz"))
	if err != nil {
		t.Fatalf("task A stash push: %v", err)
	}
	hashB, err := wm.PushStash(wtB, StashMessageForAutoMerge("bravo-two-jade"))
	if err != nil {
		t.Fatalf("task B stash push: %v", err)
	}
	if hashA == "" || hashB == "" || hashA == hashB {
		t.Fatalf("the two pushes must yield distinct identities, got %q and %q", hashA, hashB)
	}

	// A restores FIRST, while B's entry is the newest on the stack. This is the ordering that
	// crossed work when the restore was a bare `git stash pop`.
	restoredA, err := wm.PopStashByHash(wtA, hashA)
	if err != nil {
		t.Fatalf("task A restore: %v", err)
	}
	if !restoredA {
		t.Fatal("task A's entry was not found to restore")
	}

	if _, err := os.Stat(filepath.Join(wtA, "alpha-work.txt")); err != nil {
		t.Errorf("task A's own work did not come back to A's worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtB, "alpha-work.txt")); err == nil {
		t.Error("task A's work landed in task B's worktree — the stash crossing")
	}

	restoredB, err := wm.PopStashByHash(wtB, hashB)
	if err != nil {
		t.Fatalf("task B restore: %v", err)
	}
	if !restoredB {
		t.Fatal("task B's entry was not found to restore")
	}
	if _, err := os.Stat(filepath.Join(wtB, "bravo-work.txt")); err != nil {
		t.Errorf("task B's own work did not come back to B's worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtA, "bravo-work.txt")); err == nil {
		t.Error("task B's work landed in task A's worktree — the stash crossing")
	}

	// Both entries consumed: nothing left behind for the next task to trip over.
	if n := countStashEntries(t, repoDir); n != 0 {
		t.Errorf("%d stash entries left after both tasks restored; want 0", n)
	}
}

// TestPopStashByHashDoesNotTakeTheUsersEntry: the stack holds the user's own entries too, and a
// restore must never consume one of those. This is the same property from the other side — not
// "another task" but "a human's work".
func TestPopStashByHashDoesNotTakeTheUsersEntry(t *testing.T) {
	repoDir, _, _, _, _, wm := setupStashTest(t)
	userHash := seedUserStash(t, wm, repoDir)

	writeFile(t, repoDir, "task.txt", "in-flight work\n")
	ours, err := wm.PushStash(repoDir, StashMessageForSyncMerge("task-1"))
	if err != nil {
		t.Fatalf("PushStash: %v", err)
	}

	ok, err := wm.PopStashByHash(repoDir, ours)
	if err != nil || !ok {
		t.Fatalf("PopStashByHash = (%v, %v), want (true, nil)", ok, err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "task.txt")); err != nil {
		t.Errorf("our work did not come back: %v", err)
	}

	entries, err := wm.listStashEntries(repoDir)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 || entries[0].Hash != userHash {
		t.Errorf("the user's stash entry was consumed by our restore: %v", entries)
	}
}

// TestPopStashByHashLeavesAnEntryItCouldNotApply: a conflicted apply must not also delete the saved
// state. Dropping what could not be applied turns a conflict the user can resolve into work that no
// longer exists anywhere.
func TestPopStashByHashLeavesAnEntryItCouldNotApply(t *testing.T) {
	repoDir, projID, _, _, _, wm := setupStashTest(t)
	wt := stashWorktree(t, wm, repoDir, projID, "conflict-one-opal")

	writeFile(t, wt, "shared.txt", "stashed version\n")
	hash, err := wm.PushStash(wt, StashMessageForAutoMerge("conflict-one-opal"))
	if err != nil {
		t.Fatalf("PushStash: %v", err)
	}

	// Make the same path unmergeable, the way a conflicted merge leaves it.
	writeFile(t, wt, "shared.txt", "competing version\n")

	ok, err := wm.PopStashByHash(wt, hash)
	if err == nil {
		t.Fatal("apply succeeded; the test no longer covers the conflict path")
	}
	if !ok {
		t.Error("the entry was reported absent; it is present and merely unapplicable")
	}
	if n := countStashEntries(t, repoDir); n != 1 {
		t.Errorf("the unapplied entry was dropped: %d entries left, want 1 — that is lost work", n)
	}
}

// TestDropStashByHashNeverFallsBackToTheTopEntry pins the removal of a fallback that dropped
// stash@{0} when the requested hash was not found. If ours is gone, the top entry is somebody
// else's — the precise hazard the hash-identity API exists to remove.
func TestDropStashByHashNeverFallsBackToTheTopEntry(t *testing.T) {
	repoDir, _, _, _, _, wm := setupStashTest(t)
	userHash := seedUserStash(t, wm, repoDir)

	// A hash that is not in the list (ours was already swept).
	if err := wm.DropStashByHash(repoDir, "0123456789abcdef0123456789abcdef01234567"); err != nil {
		t.Fatalf("DropStashByHash for a missing entry: %v", err)
	}
	entries, _ := wm.listStashEntries(repoDir)
	if len(entries) != 1 || entries[0].Hash != userHash {
		t.Errorf("dropping a missing entry consumed the user's: %v", entries)
	}
}

// TestDropStashForTaskSparesWhatIsOnTop is the regression for the conflict-resolution path, which
// discarded stash@{0} to tidy up its own pre-sync edits. The entry on top is whichever entry is
// newest, so when ours was already swept or another task pushed later, the tidy-up deleted that one.
func TestDropStashForTaskSparesWhatIsOnTop(t *testing.T) {
	repoDir, _, _, _, _, wm := setupStashTest(t)

	// Ours, pushed under the sync-merge label, then the user pushed something newer.
	writeFile(t, repoDir, "pre-sync.txt", "edits that are already committed\n")
	ours, err := wm.PushStash(repoDir, StashMessageForSyncMerge("task-1"))
	if err != nil {
		t.Fatalf("PushStash: %v", err)
	}
	// Pushed after ours so it sits on top — the case a bare `stash drop` gets wrong. Inlined rather
	// than via seedUserStash, which asserts the stack holds exactly its own entry.
	writeFile(t, repoDir, "user.txt", "the user's own work\n")
	if err := wm.gitRun(repoDir, "stash", "push", "-u", "-m", "the user's own entry"); err != nil {
		t.Fatalf("seed the user's stash entry: %v", err)
	}
	entries, err := wm.listStashEntries(repoDir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("setup: expected two entries, got %v (%v)", entries, err)
	}
	userHash := entries[0].Hash

	if userHash == ours {
		t.Fatalf("setup: the two entries are the same (%q)", userHash)
	}

	found, err := wm.DropStashForTask(repoDir, StashMessageForSyncMerge("task-1"))
	if err != nil {
		t.Fatalf("DropStashForTask: %v", err)
	}
	if !found {
		t.Error("our entry was not found to drop")
	}

	entries, _ = wm.listStashEntries(repoDir)
	if len(entries) != 1 || entries[0].Hash != userHash {
		t.Errorf("dropping our entry consumed the one on top instead: %v (ours %q, user's %q)", entries, shortHash(ours), shortHash(userHash))
	}
}

// TestRestoreStashForTaskFindsItsOwnLabelAcrossTasks: the deferred restores never saw the push, so the
// label is their only identity. Two tasks' entries on the stack, and each restore must take its own.
func TestRestoreStashForTaskFindsItsOwnLabelAcrossTasks(t *testing.T) {
	repoDir, projID, _, _, _, wm := setupStashTest(t)
	wtA := stashWorktree(t, wm, repoDir, projID, "label-one-onyx")
	wtB := stashWorktree(t, wm, repoDir, projID, "label-two-topaz")

	writeFile(t, wtA, "alpha.txt", "A's work\n")
	if _, err := wm.PushStash(wtA, StashMessageForSyncMerge("label-one-onyx")); err != nil {
		t.Fatalf("push A: %v", err)
	}
	writeFile(t, wtB, "bravo.txt", "B's work\n")
	if _, err := wm.PushStash(wtB, StashMessageForSyncMerge("label-two-topaz")); err != nil {
		t.Fatalf("push B: %v", err)
	}

	for _, c := range []struct{ taskID, file, other string }{
		{"label-one-onyx", "alpha.txt", "bravo.txt"},
		{"label-two-topaz", "bravo.txt", "alpha.txt"},
	} {
		found, err := wm.RestoreStashForTask(wtA, StashMessageForSyncMerge(c.taskID))
		if err != nil {
			t.Fatalf("restore %s: %v", c.taskID, err)
		}
		if !found {
			t.Fatalf("restore %s found nothing to restore", c.taskID)
		}
		if _, err := os.Stat(filepath.Join(wtA, c.file)); err != nil {
			t.Errorf("%s did not restore its own file %s: %v", c.taskID, c.file, err)
		}
		if _, err := os.Stat(filepath.Join(wtA, c.other)); err == nil {
			t.Errorf("%s restored the other task's file %s", c.taskID, c.other)
		}
		_ = os.Remove(filepath.Join(wtA, c.file))
	}
}

// TestPopStashByHashReportsAMissingEntryAsAbsent: silently continuing is how lost work stays lost, so
// the caller gets a distinguishable "it was not there" rather than success.
func TestPopStashByHashReportsAMissingEntryAsAbsent(t *testing.T) {
	repoDir, _, _, _, _, wm := setupStashTest(t)

	ok, err := wm.PopStashByHash(repoDir, "0123456789abcdef0123456789abcdef01234567")
	if err != nil {
		t.Fatalf("a missing entry is not an error, it is absent: %v", err)
	}
	if ok {
		t.Error("PopStashByHash reported restoring an entry that does not exist")
	}

	// An empty hash is the "nothing was stashed" case every merge path hits, and must stay silent.
	if ok, err := wm.PopStashByHash(repoDir, ""); ok || err != nil {
		t.Errorf("PopStashByHash(\"\") = (%v, %v), want (false, nil)", ok, err)
	}
}
