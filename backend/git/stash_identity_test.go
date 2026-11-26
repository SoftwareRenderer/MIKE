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
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// TestInterleavedMergeStashesRestoreTheirOwnWork: merges on one project push to one shared stash
// stack, so each must restore its own entry by hash.
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

// TestStashHashLookupsNeverFallBackToTheTopEntry pins the removal of a fallback that dropped
// stash@{0} when the requested hash was not found. If ours is gone, the top entry is somebody
// else's — the precise hazard the hash-identity API exists to remove — so neither lookup touches it,
// and a missing entry is answered as absent rather than as success or as an error.
func TestStashHashLookupsNeverFallBackToTheTopEntry(t *testing.T) {
	repoDir, _, _, _, _, wm := setupStashTest(t)
	userHash := seedUserStash(t, wm, repoDir)
	const missing = "0123456789abcdef0123456789abcdef01234567"

	t.Run("pop", func(t *testing.T) {
		found, err := wm.PopStashByHash(repoDir, missing)
		if err != nil {
			t.Fatalf("a missing entry is not an error, it is absent: %v", err)
		}
		if found {
			t.Error("PopStashByHash reported restoring an entry that does not exist")
		}
		// An empty hash is the "nothing was stashed" case every merge path hits, and must stay silent.
		if found, err := wm.PopStashByHash(repoDir, ""); found || err != nil {
			t.Errorf("PopStashByHash(\"\") = (%v, %v), want (false, nil)", found, err)
		}
	})

	t.Run("drop", func(t *testing.T) {
		if err := wm.DropStashByHash(repoDir, missing); err != nil {
			t.Fatalf("DropStashByHash for a missing entry: %v", err)
		}
	})

	if entries, _ := wm.listStashEntries(repoDir); len(entries) != 1 || entries[0].Hash != userHash {
		t.Errorf("a lookup for a hash nobody holds consumed the user's entry: %v", entries)
	}
}

// TestDropStashForTaskSparesWhatIsOnTop: dropping a task's entry must not consume stash@{0}, which is
// whichever entry is newest — often the user's own.
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

func mustReadFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// TestPopStashByHashKeepsTheStagedSplit: a plain apply returns a modification unstaged, and
// commit-and-done commits the index only.
func TestPopStashByHashKeepsTheStagedSplit(t *testing.T) {
	repoDir, projID, _, _, _, wm := setupStashTest(t)
	wt := stashWorktree(t, wm, repoDir, projID, "staged-split-one")

	// Everything a finished turn leaves: a modification, a new file, something never staged.
	writeFile(t, wt, "base.txt", "base\nedited by the task\n")
	writeFile(t, wt, "created.txt", "created by the task\n")
	writeFile(t, wt, "scratch.txt", "never staged\n")
	runGit(t, wt, "add", "base.txt", "created.txt")

	hash, err := wm.PushStash(wt, StashMessageForAutoMerge("staged-split-one"))
	if err != nil {
		t.Fatalf("stash push: %v", err)
	}
	found, err := wm.PopStashByHash(wt, hash)
	if err != nil || !found {
		t.Fatalf("restore: found=%v err=%v", found, err)
	}

	staged := strings.Fields(runGitOutput(t, wt, "diff", "--cached", "--name-only", "HEAD"))
	if !slices.Contains(staged, "base.txt") {
		t.Errorf("the task's edit came back unstaged and its own commit would skip it; staged=%q", staged)
	}
	if !slices.Contains(staged, "created.txt") {
		t.Errorf("the task's new file came back unstaged; staged=%q", staged)
	}
	if content := mustReadFile(t, wt, "base.txt"); !strings.Contains(content, "edited by the task") {
		t.Errorf("restored content lost: %q", content)
	}
	// Untracked work stays untracked: restoring it into the index would put it in the commit.
	if status := runGitOutput(t, wt, "status", "--porcelain", "--", "scratch.txt"); !strings.HasPrefix(status, "?? ") {
		t.Errorf("scratch.txt should still be untracked, status=%q", status)
	}
}

// TestPopStashByHashRestoresEvenWhenTheIndexCannotBeRebuilt: git refuses --index when the tree
// has its own claim on the file. The refusal must not cost the task its work.
func TestPopStashByHashRestoresEvenWhenTheIndexCannotBeRebuilt(t *testing.T) {
	repoDir, projID, _, _, _, wm := setupStashTest(t)
	wt := stashWorktree(t, wm, repoDir, projID, "staged-clash-one")

	writeFile(t, wt, "base.txt", "base\nthe task's own edit\n")
	runGit(t, wt, "add", "base.txt")
	hash, err := wm.PushStash(wt, StashMessageForAutoMerge("staged-clash-one"))
	if err != nil {
		t.Fatalf("stash push: %v", err)
	}

	// What the bracket wrapped: a different edit on the same file.
	writeFile(t, wt, "base.txt", "base\nmerged in from a subtask\n")
	runGit(t, wt, "commit", "-qam", "subtask work")

	found, err := wm.PopStashByHash(wt, hash)
	if !found {
		t.Fatal("the entry was on the stack")
	}
	if err == nil {
		t.Fatal("two edits to one file cannot both be applied cleanly; the caller must hear about it")
	}

	// The retry puts the work back as a conflict; without it the edit is simply gone.
	if content := mustReadFile(t, wt, "base.txt"); !strings.Contains(content, "the task's own edit") {
		t.Errorf("the refused --index apply swallowed the task's work; file=%q", content)
	}
	if status := runGitOutput(t, wt, "status", "--porcelain", "--", "base.txt"); !strings.HasPrefix(status, "UU") {
		t.Errorf("the clash must stay visible as an unresolved conflict, status=%q", status)
	}
	if list := runGitOutput(t, wt, "stash", "list"); !strings.Contains(list, hash) && !strings.Contains(list, StashMessageForAutoMerge("staged-clash-one")) {
		t.Errorf("an entry that could not be applied must stay on the stack, list=%q", list)
	}
}

// A bracketed merge parks the checkout's own work, merges, then restores it. When the merge touched the
// same lines the parked work did, the apply conflicts: the paths arrive in the working tree unmerged and
// the entry stays on the stack. That is a different fact from "the merge failed" — the branch already
// reached its target — so it has to be classifiable without reading git's wording.

// TestPopStashByHashNamesThePathsItCouldNotRestore: the merge reworded the sentence the parked work
// reworded. The error names the file and the entry, and the entry is still there to restore by hand.
func TestPopStashByHashNamesThePathsItCouldNotRestore(t *testing.T) {
	repoDir, _, _, _, _, wm := setupStashTest(t)
	writeFile(t, repoDir, "shared.txt", "the original sentence\n")
	runGit(t, repoDir, "add", "shared.txt")
	runGit(t, repoDir, "commit", "-qm", "shared")

	writeFile(t, repoDir, "shared.txt", "the parked wording\n")
	hash, err := wm.PushStash(repoDir, StashMessageForAutoMerge("conflict-task"))
	if err != nil {
		t.Fatalf("stash push: %v", err)
	}

	// What the merge brings in: a competing change to the same line, now committed.
	writeFile(t, repoDir, "shared.txt", "the merged wording\n")
	runGit(t, repoDir, "commit", "-qam", "merged wording")

	found, restoreErr := wm.PopStashByHash(repoDir, hash)
	if restoreErr == nil {
		t.Fatal("the restore reported success with conflict markers in the tree")
	}
	if !found {
		t.Error("found=false: the entry is on the stack, and a caller reading this loses track of it")
	}
	var conflict *RestoreConflictError
	if !errors.As(restoreErr, &conflict) {
		t.Fatalf("want *RestoreConflictError, got %T: %v", restoreErr, restoreErr)
	}
	if strings.Join(conflict.Paths, ",") != "shared.txt" {
		t.Errorf("Paths = %v, want the one conflicted file", conflict.Paths)
	}
	if conflict.Hash != hash {
		t.Errorf("Hash = %q, want %q — the message tells the user where their words are", conflict.Hash, hash)
	}
	if n := countStashEntries(t, repoDir); n != 1 {
		t.Errorf("%d entries left, want 1 — a conflicted apply must keep the saved state", n)
	}
}

// TestPopStashByHashTypesARefusalApartFromAConflict: an apply git refused outright left the tree alone, so
// there is nothing for a person to resolve. Only an unmerged index is a conflict — sniffing git's text
// would call this one too — and each shape gets its own type, because the person is told to resolve files
// in one case and to look in the stash in the other. A refusal keeps the saved state either way, because
// dropping what could not be applied turns a recoverable restore into work that no longer exists anywhere.
func TestPopStashByHashTypesARefusalApartFromAConflict(t *testing.T) {
	repoDir, _, _, _, _, wm := setupStashTest(t)

	untracked := filepath.Join(repoDir, "new.txt")
	writeFile(t, repoDir, "new.txt", "parked\n")
	hash, err := wm.PushStash(repoDir, StashMessageForAutoMerge("refused-task"))
	if err != nil {
		t.Fatalf("stash push: %v", err)
	}

	// The path was parked untracked and has since been written again: git refuses rather than merging.
	writeFile(t, repoDir, "new.txt", "written again since the push\n")

	found, restoreErr := wm.PopStashByHash(repoDir, hash)
	if restoreErr == nil {
		t.Fatal("the restore reported success while the file was left unwritten")
	}
	if !found {
		t.Error("the entry was reported absent; it is present and merely unapplicable")
	}
	var conflict *RestoreConflictError
	if errors.As(restoreErr, &conflict) {
		t.Errorf("a refusal that left no conflict is reported as one: %v", restoreErr)
	}
	var refused *RestoreRefusedError
	if !errors.As(restoreErr, &refused) {
		t.Fatalf("want *RestoreRefusedError, got %T: %v — the caller cannot tell the person where their work is", restoreErr, restoreErr)
	}
	if refused.Hash != hash {
		t.Errorf("Hash = %q, want %q", refused.Hash, hash)
	}
	if refused.GitMessage == "" {
		t.Error("GitMessage is empty: the only thing that says why the apply was refused is gone")
	}

	if content, rerr := os.ReadFile(untracked); rerr != nil || string(content) != "written again since the push\n" {
		t.Errorf("the tree was modified by a refused apply: %q (%v)", string(content), rerr)
	}
	if n := countStashEntries(t, repoDir); n != 1 {
		t.Errorf("%d entries left, want 1", n)
	}
}
