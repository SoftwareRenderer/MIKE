package git

// A bracketed merge parks the checkout's own work, merges, then restores it. When the merge touched the
// same lines the parked work did, the apply conflicts: the paths arrive in the working tree unmerged and
// the entry stays on the stack. That is a different fact from "the merge failed" — the branch already
// reached its target — so it has to be classifiable without reading git's wording.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPopStashByHashNamesThePathsItCouldNotRestore: the merge reworded the sentence the parked work
// reworded. The error names the file and the entry, and the entry is still there to restore by hand.
func TestPopStashByHashNamesThePathsItCouldNotRestore(t *testing.T) {
	t.Parallel()
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

// TestPopStashByHashLeavesARefusalUnclassified: an apply git refused outright left the tree alone, so
// there is nothing for a person to resolve. Only an unmerged index is a conflict — sniffing git's text
// would call this one too.
func TestPopStashByHashLeavesARefusalUnclassified(t *testing.T) {
	t.Parallel()
	repoDir, _, _, _, _, wm := setupStashTest(t)

	untracked := filepath.Join(repoDir, "new.txt")
	writeFile(t, repoDir, "new.txt", "parked\n")
	hash, err := wm.PushStash(repoDir, StashMessageForAutoMerge("refused-task"))
	if err != nil {
		t.Fatalf("stash push: %v", err)
	}

	// The path was parked untracked and has since been written again: git refuses rather than merging.
	writeFile(t, repoDir, "new.txt", "written again since the push\n")

	if _, restoreErr := wm.PopStashByHash(repoDir, hash); restoreErr == nil {
		t.Fatal("the restore reported success while the file was left unwritten")
	} else {
		var conflict *RestoreConflictError
		if errors.As(restoreErr, &conflict) {
			t.Errorf("a refusal that left no conflict is reported as one: %v", restoreErr)
		}
	}

	if content, rerr := os.ReadFile(untracked); rerr != nil || string(content) != "written again since the push\n" {
		t.Errorf("the tree was modified by a refused apply: %q (%v)", string(content), rerr)
	}
	if n := countStashEntries(t, repoDir); n != 1 {
		t.Errorf("%d entries left, want 1", n)
	}
}
