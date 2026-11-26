package exec

// A merge failure is either the person's own state stopping it or this server failing, and the two answer
// apart: 409 with a sentence naming what to resolve, or a server fault whose browser text is MIKE's own and
// whose cause stays in the log. Both are decided where the failure is found, because only there is it known
// whether the merge landed and the parked work is merely stranded.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/errs"
	"mike/backend/git"
	"mike/backend/testutil/testgit"
)

// leaveCheckoutMidConflict leaves dir holding an unresolved merge, the state a parked-work restore that
// came back conflicted leaves behind.
func leaveCheckoutMidConflict(t *testing.T, dir string) {
	t.Helper()
	testgit.Git(t, dir, "checkout", "-qb", "conflicting-side")
	if err := os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("the side's wording\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testgit.Git(t, dir, "add", "shared.txt")
	testgit.Git(t, dir, "commit", "-m", "the side's wording")
	testgit.Git(t, dir, "checkout", "master")
	if err := os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("the person's wording\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testgit.Git(t, dir, "add", "shared.txt")
	testgit.Git(t, dir, "commit", "-m", "the person's wording")
	if _, err := git.Run(dir, "merge", "conflicting-side", "--no-edit"); err == nil {
		t.Fatal("the fixture must leave the checkout mid-merge")
	}
}

// TestMergeBranchRefusesToParkAConflictedCheckout: the click's merge has to park the project's own work, and
// a checkout that is already mid-conflict cannot be parked. That is the person's state to resolve, not a
// server fault, and the answer names the file so they know which one to open.
func TestMergeBranchRefusesToParkAConflictedCheckout(t *testing.T) {
	te, repoDir, projectID, _ := setupCommitAndMergeTest(t)
	leaveCheckoutMidConflict(t, repoDir)

	task, err := te.store.GetTaskByID("test-task")
	if err != nil {
		t.Fatal(err)
	}
	mergeErr := te.MergeBranch(projectID, "test-task", task)

	if !errs.IsConflict(mergeErr) {
		t.Fatalf("MergeBranch = %v, want an errs.Conflict", mergeErr)
	}
	if !strings.Contains(errs.Message(mergeErr), "shared.txt") {
		t.Errorf("the answer does not name the conflicted file: %q", errs.Message(mergeErr))
	}
	if MergedDelivered(mergeErr) {
		t.Error("nothing reached the target branch, so the task must not read as delivered")
	}
}

// TestMergeBranchReportsAnOrdinaryGitRefusalAsAFault: a git failure nobody caused is a server fault, and it
// still says what MIKE was doing. git's own text — a path, a driver message — stays in the cause the log
// reads.
func TestMergeBranchReportsAnOrdinaryGitRefusalAsAFault(t *testing.T) {
	te, repoDir, projectID, _ := setupCommitAndMergeTest(t)
	lock := filepath.Join(repoDir, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(lock) })

	task, err := te.store.GetTaskByID("test-task")
	if err != nil {
		t.Fatal(err)
	}
	mergeErr := te.MergeBranch(projectID, "test-task", task)

	if !errs.IsOperation(mergeErr) {
		t.Fatalf("MergeBranch = %v, want an errs.Operation", mergeErr)
	}
	if !strings.Contains(errs.Message(mergeErr), "master") {
		t.Errorf("the answer does not say which merge failed: %q", errs.Message(mergeErr))
	}
	if errs.Cause(mergeErr) == nil {
		t.Error("the cause is gone: git's reason is the only thing that says why the merge stopped")
	}
}

// TestParkedWorkRefusalSaysWhereTheStrandedWorkIs: the bracket parked the person's work, the merge landed,
// and the apply came back refused with nothing unmerged. The work is neither lost nor back in the tree, and
// only the entry's label says where it is.
func TestParkedWorkRefusalSaysWhereTheStrandedWorkIs(t *testing.T) {
	t.Parallel()

	refused := &git.RestoreRefusedError{Hash: "0f1e2d3c4b5a", GitMessage: "could not restore untracked files from stash"}
	err := parkedWorkRefusal("the project's checkout", "master", "mike-auto-merge-task-1", true,
		fmt.Errorf("restore the stashed work: %w", refused))

	if !errs.IsConflict(err) {
		t.Fatalf("parkedWorkRefusal = %v, want an errs.Conflict", err)
	}
	sentence := errs.Message(err)
	for _, want := range []string{"master", "could not restore untracked files from stash", "mike-auto-merge-task-1"} {
		if !strings.Contains(sentence, want) {
			t.Errorf("the answer omits %q: %q", want, sentence)
		}
	}
	if !strings.Contains(sentence, "merged into master") {
		t.Errorf("the answer does not say the work reached its target: %q", sentence)
	}
}

// TestParkedWorkRefusalListsConflictedFilesButStatesTheirCount: a toast is not a file listing. The count is
// always stated, so the list can stop at a few and still be honest about how many there are.
func TestParkedWorkRefusalListsConflictedFilesButStatesTheirCount(t *testing.T) {
	t.Parallel()

	var paths []string
	for i := range 7 {
		paths = append(paths, fmt.Sprintf("file%d.go", i))
	}
	err := parkedWorkRefusal("the project's checkout", "master", "mike-auto-merge-task-1", false,
		&git.PushConflictError{Paths: paths})

	sentence := errs.Message(err)
	if !strings.Contains(sentence, "7 file(s)") {
		t.Errorf("the count is missing: %q", sentence)
	}
	if strings.Contains(sentence, "file5.go") || strings.Contains(sentence, "file6.go") {
		t.Errorf("every path was listed: %q", sentence)
	}
	if !strings.Contains(sentence, "file0.go") {
		t.Errorf("no path was named: %q", sentence)
	}
}

// TestMergeFaultNeverRewritesADeliveredMerge: errs.Operation hides its cause, so a merge that delivered —
// reported through a sentinel inside the error — must reach the caller unclassified or its Done test breaks.
func TestMergeFaultNeverRewritesADeliveredMerge(t *testing.T) {
	t.Parallel()

	delivered := fmt.Errorf("%w: %w", errMergedWithConflictedRestore, conflictedRestore)
	if got := mergeFault("merge task/x into master", delivered); got != delivered {
		t.Errorf("a delivered merge was re-written as %v", got)
	}
	if !MergedDelivered(mergeFault("merge task/x into master", delivered)) {
		t.Error("classification hid the sentinel MergedDelivered reads")
	}

	already := errs.Conflict("already said")
	if got := mergeFault("merge task/x into master", already); got != already {
		t.Errorf("a classified error was re-written as %v", got)
	}
}
