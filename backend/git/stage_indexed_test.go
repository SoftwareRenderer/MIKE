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

// The refresh half: a file the system staged and the person then edited (MM) must land whole in the
// index, and a file that is only the person's must be left alone.
func TestStageIndexedFiles_RefreshesStagedFiles(t *testing.T) {
	repoDir, taskID, _, _, wm := setupTestRepo(t)

	// Create and commit two tracked files
	writeFile(t, repoDir, "hello.txt", "line1\nline2\n")
	writeFile(t, repoDir, "untouched.txt", "base\n")
	runGit(t, repoDir, "add", "hello.txt", "untouched.txt")
	runGit(t, repoDir, "commit", "-m", "Initial")

	if _, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", ""); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	worktreePath, _ := wm.WorktreePath(taskID)

	// "System staged" line3; user then adds line4 (unstaged) after staging.
	writeFile(t, worktreePath, "hello.txt", "line1\nline2\nline3\n")
	runGit(t, worktreePath, "add", "hello.txt")
	writeFile(t, worktreePath, "hello.txt", "line1\nline2\nline3\nline4\n")

	// Unstaged-only change — must NOT be touched by the helper.
	writeFile(t, worktreePath, "untouched.txt", "base\nedited\n")

	// Sanity check: hello.txt is in MM state before the refresh.
	status := runGitOutput(t, worktreePath, "status", "--porcelain")
	if !strings.Contains(status, "MM hello.txt") {
		t.Fatalf("expected hello.txt in MM state, got:\n%s", status)
	}

	wm.StageIndexedFiles(taskID)

	// hello.txt: staged diff includes both line3 and line4; no unstaged diff left.
	stagedDiff := runGitOutput(t, worktreePath, "diff", "--staged", "--", "hello.txt")
	if !strings.Contains(stagedDiff, "+line3") || !strings.Contains(stagedDiff, "+line4") {
		t.Fatalf("expected staged diff to include line3 and line4, got:\n%s", stagedDiff)
	}
	if unstaged := runGitOutput(t, worktreePath, "diff", "--", "hello.txt"); unstaged != "" {
		t.Fatalf("expected no unstaged diff for hello.txt, got:\n%s", unstaged)
	}

	// untouched.txt still has only unstaged changes (contract preserved).
	status = runGitOutput(t, worktreePath, "status", "--porcelain")
	if !strings.Contains(status, " M untouched.txt") {
		t.Fatalf("expected untouched.txt to remain unstaged-only, got:\n%s", status)
	}

	// A commit now captures the manual edit.
	runGit(t, worktreePath, "commit", "-m", "commit after manual edit")
	content := runGitOutput(t, worktreePath, "show", "HEAD:hello.txt")
	if !strings.Contains(content, "line4") {
		t.Fatalf("expected commit to include manual edit line4, got:\n%s", content)
	}
}
