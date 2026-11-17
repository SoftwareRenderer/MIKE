package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/config"
	"mike/backend/store"
	"mike/backend/types"
)

// setupStashTest creates a repo with an initial commit and a worktree manager.
func setupStashTest(t *testing.T) (string, string, string, *store.Store, *config.ConfigStore, *WorktreeManager) {
	t.Helper()

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	worktreesDir := filepath.Join(tmpDir, "worktrees")
	os.MkdirAll(repoDir, 0o755)
	os.MkdirAll(worktreesDir, 0o755)

	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")
	writeFile(t, repoDir, "README.md", "# Test\n")
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "Initial")

	dbPath := filepath.Join(tmpDir, "test.db")
	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	cfgStore := config.NewConfigStore(s, dbPath, "test", nil, false, false)
	cfgStore.SetWorkspaceDirOverride(worktreesDir)
	cfg, err := cfgStore.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if err := cfgStore.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	projID := "test-project"
	proj := &types.Project{ID: projID, Name: "test", Path: repoDir}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	taskID := "test-task-id"
	task := &types.Task{
		ID:        taskID,
		ProjectID: projID,
		Title:     "Test task",
		Status:    types.StatusInProgress,
		Branch:    "task/" + taskID,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	wm := NewWorktreeManager(s, cfgStore)

	return repoDir, projID, taskID, s, cfgStore, wm
}

// countStashEntries returns the number of stash entries in the repo.
func countStashEntries(t *testing.T, repoDir string) int {
	t.Helper()
	out, err := exec.Command("git", "-C", repoDir, "stash", "list").CombinedOutput()
	if err != nil {
		return 0
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return 0
	}
	return len(lines)
}

// stashSubjects returns just the subject lines from the stash list.
func stashSubjects(t *testing.T, repoDir string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", repoDir, "stash", "list", "--format=%gs").CombinedOutput()
	if err != nil {
		t.Fatalf("git stash list: %s: %v", string(out), err)
	}
	var subjects []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			subjects = append(subjects, line)
		}
	}
	return subjects
}

// createStash pushes a stash with unique content so each stash entry is distinct.
// After pushing, the working tree is clean, so the caller must create new changes
// before the next call.
func createStash(t *testing.T, repoDir, content, msg string) {
	t.Helper()
	writeFile(t, repoDir, "change.txt", content)
	runGit(t, repoDir, "add", "-A")
	runGit(t, repoDir, "stash", "push", "-u", "-m", msg)
}

// The sweep a deleted task leaves behind. Entries are matched by label and dropped by hash, so neither
// the stack's order nor another owner's entry may decide what disappears.
func TestDropStashEntriesForTaskDropsOnlyItsOwnLabels(t *testing.T) {
	t.Run("a manual stash of the same shape survives", func(t *testing.T) {
		repoDir, projID, taskID, _, _, wm := setupStashTest(t)

		for _, label := range []string{"mike-switch", "mike-fork", "mike-checkpoint"} {
			createStash(t, repoDir, fmt.Sprintf("change for %s\n", label), fmt.Sprintf("%s-%s", label, taskID))
		}
		for i := 0; i < 2; i++ {
			createStash(t, repoDir, fmt.Sprintf("unrelated change %d\n", i), fmt.Sprintf("manual stash %d", i))
		}
		if before := countStashEntries(t, repoDir); before != 5 {
			t.Fatalf("expected 5 stash entries before cleanup, got %d", before)
		}

		if err := wm.DropStashEntriesForTask(projID, taskID); err != nil {
			t.Fatalf("DropStashEntriesForTask: %v", err)
		}

		if after := countStashEntries(t, repoDir); after != 2 {
			t.Fatalf("expected 2 stash entries after cleanup, got %d", after)
		}
		for _, s := range stashSubjects(t, repoDir) {
			if strings.Contains(s, "mike-switch-"+taskID) ||
				strings.Contains(s, "mike-fork-"+taskID) ||
				strings.Contains(s, "mike-checkpoint-"+taskID) {
				t.Errorf("kanban stash entry should have been dropped, found: %s", s)
			}
		}
	})

	t.Run("the position in the stack does not matter", func(t *testing.T) {
		repoDir, projID, taskID, _, _, wm := setupStashTest(t)

		for _, msg := range []string{
			"manual stash 0",
			fmt.Sprintf("mike-switch-%s", taskID),
			"manual stash 1",
			fmt.Sprintf("mike-fork-%s", taskID),
			"manual stash 2",
		} {
			createStash(t, repoDir, fmt.Sprintf("change for %s\n", msg), msg)
		}

		if err := wm.DropStashEntriesForTask(projID, taskID); err != nil {
			t.Fatalf("DropStashEntriesForTask: %v", err)
		}
		if after := countStashEntries(t, repoDir); after != 3 {
			t.Fatalf("expected 3 stash entries after cleanup, got %d", after)
		}
		for _, s := range stashSubjects(t, repoDir) {
			if strings.Contains(s, "mike-") {
				t.Errorf("kanban stash should have been dropped, found: %s", s)
			}
		}
	})

	t.Run("a sibling task's labels survive", func(t *testing.T) {
		repoDir, projID, taskID, s, _, wm := setupStashTest(t)

		otherTaskID := "other-task-id"
		if err := s.CreateTask(&types.Task{
			ID: otherTaskID, ProjectID: projID, Title: "Other task",
			Status: types.StatusInProgress, Branch: "task/" + otherTaskID,
		}); err != nil {
			t.Fatalf("create other task: %v", err)
		}
		for _, tid := range []string{taskID, otherTaskID} {
			createStash(t, repoDir, fmt.Sprintf("change for %s\n", tid), fmt.Sprintf("mike-switch-%s", tid))
		}

		if err := wm.DropStashEntriesForTask(projID, taskID); err != nil {
			t.Fatalf("DropStashEntriesForTask: %v", err)
		}
		subjects := stashSubjects(t, repoDir)
		if len(subjects) != 1 {
			t.Fatalf("expected 1 stash entry after cleanup, got %d: %v", len(subjects), subjects)
		}
		if !strings.Contains(subjects[0], otherTaskID) {
			t.Errorf("remaining stash should belong to the other task, got: %s", subjects[0])
		}
	})

	t.Run("every kanban label is dropped", func(t *testing.T) {
		repoDir, projID, taskID, _, _, wm := setupStashTest(t)

		for _, label := range []string{"mike-switch", "mike-fork", "mike-checkpoint", "mike-sync-merge", "mike-auto-merge"} {
			msg := fmt.Sprintf("%s-%s", label, taskID)
			createStash(t, repoDir, fmt.Sprintf("change for %s\n", label), msg)
		}
		if before := countStashEntries(t, repoDir); before != 5 {
			t.Fatalf("expected 5 stash entries before cleanup, got %d", before)
		}

		if err := wm.DropStashEntriesForTask(projID, taskID); err != nil {
			t.Fatalf("DropStashEntriesForTask: %v", err)
		}
		if after := countStashEntries(t, repoDir); after != 0 {
			t.Fatalf("expected 0 stash entries after cleanup, got %d", after)
		}
	})
}

// A branch's own entries are named in the reflog subject; the match is " <branch>:", so a branch whose
// name is contained in another's is not swept with it.
func TestDropStashEntriesForBranchDropsOnlyThatBranch(t *testing.T) {
	t.Run("the other branch's entries survive", func(t *testing.T) {
		repoDir, projID, taskID, _, _, wm := setupStashTest(t)
		branchName := "task/" + taskID

		runGit(t, repoDir, "branch", branchName)
		runGit(t, repoDir, "checkout", branchName)
		for i := 0; i < 3; i++ {
			createStash(t, repoDir, fmt.Sprintf("change %d\n", i), fmt.Sprintf("mike-switch-%s entry %d", taskID, i))
		}
		runGit(t, repoDir, "checkout", "master")
		for i := 0; i < 2; i++ {
			createStash(t, repoDir, fmt.Sprintf("other branch %d\n", i), fmt.Sprintf("other branch stash %d", i))
		}
		if before := countStashEntries(t, repoDir); before != 5 {
			t.Fatalf("expected 5 stash entries before cleanup, got %d", before)
		}

		if err := wm.DropStashEntriesForBranch(projID, branchName); err != nil {
			t.Fatalf("DropStashEntriesForBranch: %v", err)
		}
		if after := countStashEntries(t, repoDir); after != 2 {
			t.Fatalf("expected 2 stash entries after cleanup, got %d", after)
		}
		for _, s := range stashSubjects(t, repoDir) {
			if strings.Contains(s, "On "+branchName+":") {
				t.Errorf("stash on branch %s should have been dropped, found: %s", branchName, s)
			}
		}
	})

	t.Run("a branch whose name contains the other's survives", func(t *testing.T) {
		repoDir, projID, _, _, _, wm := setupStashTest(t)

		branchA := "task/alpha-bravo-charlie"
		branchB := "task/bravo-charlie-delta" // shares "bravo-charlie" with branchA's tail

		runGit(t, repoDir, "branch", branchA)
		runGit(t, repoDir, "checkout", branchA)
		createStash(t, repoDir, fmt.Sprintf("change for %s\n", branchA), fmt.Sprintf("work on %s", branchA))

		runGit(t, repoDir, "branch", branchB)
		runGit(t, repoDir, "checkout", branchB)
		createStash(t, repoDir, fmt.Sprintf("change for %s\n", branchB), fmt.Sprintf("work on %s", branchB))

		if err := wm.DropStashEntriesForBranch(projID, branchA); err != nil {
			t.Fatalf("DropStashEntriesForBranch: %v", err)
		}
		subjects := stashSubjects(t, repoDir)
		if len(subjects) != 1 {
			t.Fatalf("expected 1 stash entry after cleanup, got %d: %v", len(subjects), subjects)
		}
		if !strings.Contains(subjects[0], branchB) {
			t.Errorf("remaining stash should belong to %s, got: %s", branchB, subjects[0])
		}
	})
}

// An empty stack is the ordinary case, not an error.
func TestDropStashEntriesForTaskAndBranchOnAnEmptyStack(t *testing.T) {
	repoDir, projID, taskID, _, _, wm := setupStashTest(t)

	if err := wm.DropStashEntriesForTask(projID, taskID); err != nil {
		t.Fatalf("DropStashEntriesForTask should not error on an empty stash list: %v", err)
	}
	if err := wm.DropStashEntriesForBranch(projID, "task/some-task"); err != nil {
		t.Fatalf("DropStashEntriesForBranch should not error on an empty stash list: %v", err)
	}
	if n := countStashEntries(t, repoDir); n != 0 {
		t.Errorf("the empty-stack sweep changed the stack: %d entries", n)
	}
}

// The label is the identity a deferred restore reads back, so its shape and its cap are part of the
// hash-identity contract, not a formatting detail.
func TestStashLabelsCarryTheirPrefixAndStayWithinTheCap(t *testing.T) {
	for _, tc := range []struct {
		name   string
		label  func(string) string
		prefix string
	}{
		{"switch", StashMessageForSwitch, "mike-switch-"},
		{"fork", StashMessageForFork, "mike-fork-"},
		{"sync-merge", StashMessageForSyncMerge, "mike-sync-merge-"},
		{"auto-merge", StashMessageForAutoMerge, "mike-auto-merge-"},
	} {
		const taskID = "test-task-id"
		if got := tc.label(taskID); got != tc.prefix+taskID {
			t.Errorf("StashMessageFor%s = %q, want %q", tc.name, got, tc.prefix+taskID)
		}
		long := "waterfall-lightning-thunder-waterfall-lightning-thunder-x"
		if n := len(tc.label(long)); n > 50 {
			t.Errorf("StashMessageFor%s len = %d, want ≤ 50", tc.name, n)
		}
	}
}
