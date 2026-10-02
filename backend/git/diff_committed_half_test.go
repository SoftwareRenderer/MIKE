package git

import (
	"strings"
	"testing"
)

// A ref comparison answers with its two trees: HEAD's commits are a third thing and never belong in the
// result.
func TestGetTaskDiff_RefComparisonExcludesWorkCommittedAfterItsHead(t *testing.T) {
	repoDir, taskID, _, _, wm := setupTestRepo(t)
	base := strings.TrimSpace(runGitOutput(t, repoDir, "rev-parse", "HEAD"))

	if _, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", ""); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	wt, err := wm.WorktreePath(taskID)
	if err != nil {
		t.Fatalf("worktree path: %v", err)
	}

	writeFile(t, wt, "early.txt", "early\n")
	runGit(t, wt, "add", "-A")
	runGit(t, wt, "commit", "-m", "early work")
	at := strings.TrimSpace(runGitOutput(t, wt, "rev-parse", "HEAD"))

	// Work committed after that ref: HEAD holds it, the comparison does not.
	writeFile(t, wt, "late.txt", "late\n")
	runGit(t, wt, "add", "-A")
	runGit(t, wt, "commit", "-m", "later work")

	res, err := wm.GetTaskDiff(taskID, base, at)
	if err != nil {
		t.Fatalf("GetTaskDiff: %v", err)
	}
	if _, ok := res.Tracked["late.txt"]; ok {
		t.Errorf("the comparison listed a file committed after its head ref: %v", keysOf(res.Tracked))
	}
	early := res.Tracked["early.txt"]
	if early == nil {
		t.Fatalf("the comparison lost the file it compares: %v", keysOf(res.Tracked))
	}
	if len(early.CommittedRows) != 0 {
		t.Errorf("a ref comparison's rows are the comparison; it has no committed half: %d rows", len(early.CommittedRows))
	}
	if res.CommittedDiff != "" {
		t.Errorf("CommittedDiff describes the working-tree comparison, not this one: %q", res.CommittedDiff)
	}
	if !res.ComparesRefs {
		t.Error("a comparison of two named refs must say so; the stats frame's committed work is derived from it")
	}
	if !res.HasCommittedWork() {
		t.Error("a ref comparison is entirely committed work, so the stats frame must report it as such")
	}

	// The control: the working-tree comparison keeps the committed half of a file it also holds dirty.
	writeFile(t, wt, "early.txt", "early\nmore\n")
	working, err := wm.GetTaskDiff(taskID, base, "")
	if err != nil {
		t.Fatalf("GetTaskDiff (working tree): %v", err)
	}
	if working.CommittedDiff == "" {
		t.Error("the working-tree comparison lost its committed diff")
	}
	if f := working.Tracked["early.txt"]; f == nil || len(f.CommittedRows) == 0 {
		t.Errorf("the working-tree comparison lost the committed half of early.txt: %+v", f)
	}
}
