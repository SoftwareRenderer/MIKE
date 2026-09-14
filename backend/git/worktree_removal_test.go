package git

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRemoveWorktreeClearsDirectoryRegistrationAndBranch(t *testing.T) {
	repoDir, taskID, _, _, wm := setupTestRepo(t)
	branch := "task/" + taskID
	wtPath, _, err := wm.CreateWorktree("test-project", taskID, branch, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	// Work that is committed but unmerged, and work that is not committed at all.
	writeFile(t, wtPath, "work.txt", "unmerged commit\n")
	runGit(t, wtPath, "add", "work.txt")
	runGit(t, wtPath, "commit", "-m", "unmerged work")
	writeFile(t, wtPath, "scratch.txt", "never committed\n")

	if err := wm.RemoveWorktree("test-project", taskID, branch); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}

	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree directory still there: %v", err)
	}
	if regs := worktreeRegistrations(t, repoDir); regs != 0 {
		t.Errorf("%d worktree registration(s) left in .git/worktrees after removal", regs)
	}
	if slices.Contains(getBranches(t, repoDir), branch) {
		t.Errorf("branch %s survived RemoveWorktree - it is unreachable from the board but not from git", branch)
	}
}

func TestPruneOrphanedBranchesSweepsBranchHeldByStaleRegistration(t *testing.T) {
	repoDir, taskID, s, _, wm := setupTestRepo(t)
	branch := "task/" + taskID
	wtPath, _, err := wm.CreateWorktree("test-project", taskID, branch, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	// Directory gone, registration and branch still there: a stale registration.
	if err := os.RemoveAll(wtPath); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTaskByID(taskID); err != nil {
		t.Fatalf("fixture task: %v", err)
	}
	if err := s.DeleteTask("test-project", taskID); err != nil {
		t.Fatalf("delete task row: %v", err)
	}
	if listed := runGitOutput(t, repoDir, "branch", "--list", "task/*"); !strings.Contains(listed, "+ "+branch) {
		t.Fatalf("git does not decorate the branch with '+' here, so this test measures nothing: %q", listed)
	}

	if removed := wm.PruneOrphanedBranches("test-project"); removed != 1 {
		t.Errorf("PruneOrphanedBranches removed %d branches, want 1", removed)
	}
	if slices.Contains(getBranches(t, repoDir), branch) {
		t.Errorf("orphaned branch %s survived the sweep", branch)
	}
	if regs := worktreeRegistrations(t, repoDir); regs != 0 {
		t.Errorf("%d worktree registration(s) left after the sweep", regs)
	}
}

func TestPruneOrphanedBranchesKeepsBranchOfLiveTask(t *testing.T) {
	repoDir, taskID, _, _, wm := setupTestRepo(t)
	branch := "task/" + taskID
	if _, _, err := wm.CreateWorktree("test-project", taskID, branch, ""); err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	if removed := wm.PruneOrphanedBranches("test-project"); removed != 0 {
		t.Errorf("PruneOrphanedBranches removed %d branches, want 0 - the task row is alive", removed)
	}
	if !slices.Contains(getBranches(t, repoDir), branch) {
		t.Errorf("the sweep deleted branch %s of a live task", branch)
	}
}

// worktreeRegistrations counts the project repo's .git/worktrees entries.
func worktreeRegistrations(t *testing.T, repoDir string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repoDir, ".git", "worktrees"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read worktree registrations: %v", err)
	}
	return len(entries)
}
