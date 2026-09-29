package git

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"mike/backend/types"
)

// conflictedWorktree leaves a worktree holding one unmerged file (base.txt) and one staged
// file (extra.txt), as a refused stash restore does.
func conflictedWorktree(t *testing.T, wm *WorktreeManager, repoDir, projID, taskID string) string {
	t.Helper()
	wt := stashWorktree(t, wm, repoDir, projID, taskID)

	writeFile(t, wt, "base.txt", "base\nthe task's own edit\n")
	writeFile(t, wt, "extra.txt", "another file the task touched\n")
	runGit(t, wt, "add", "base.txt", "extra.txt")

	hash, err := wm.PushStash(wt, StashMessageForAutoMerge(taskID))
	if err != nil {
		t.Fatalf("stash push: %v", err)
	}
	writeFile(t, wt, "base.txt", "base\nmerged in from a subtask\n")
	runGit(t, wt, "commit", "-qam", "subtask work")
	if found, err := wm.PopStashByHash(wt, hash); err == nil || !found {
		t.Fatalf("the clash must reach the caller as a failed restore: found=%v err=%v", found, err)
	}
	if status := runGitOutput(t, wt, "status", "--porcelain", "--", "base.txt"); !strings.HasPrefix(status, "UU") {
		t.Fatalf("fixture did not produce a conflict, status=%q", status)
	}
	return wt
}

func TestStageIndexedFilesLeavesAnUnmergedFileUnmerged(t *testing.T) {
	repoDir, projID, _, s, _, wm := setupStashTest(t)
	const taskID = "unmerged-stage-one"
	// StageIndexedFiles takes a task, so the row must exist.
	if err := s.CreateTask(&types.Task{ID: taskID, ProjectID: projID, Title: "unmerged",
		Status: types.StatusReview, Branch: "task/" + taskID}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	wt := conflictedWorktree(t, wm, repoDir, projID, taskID)

	skipped := wm.StageIndexedFiles(taskID)
	if !slices.Contains(skipped, "base.txt") {
		t.Errorf("the caller must be told which files were left for a person, got %q", skipped)
	}
	if out := runGitOutput(t, wt, "ls-files", "--unmerged", "--", "base.txt"); out == "" {
		t.Error("base.txt was marked resolved; its conflict markers are now committable")
	}
	// The guard is about the conflicted file alone; the rest stays staged.
	if out := runGitOutput(t, wt, "diff", "--cached", "--name-only", "HEAD"); !strings.Contains(out, "extra.txt") {
		t.Errorf("extra.txt should still be staged, staged=%q", out)
	}

	// git still refuses, so nothing lands on the branch.
	cmd := exec.Command("git", "commit", "-m", "commit and done")
	cmd.Dir = wt
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("commit should have been refused; it succeeded instead:\n%s", out)
	} else if !strings.Contains(string(out), "unmerged") {
		t.Logf("refused for: %s", strings.TrimSpace(string(out)))
	}
}
