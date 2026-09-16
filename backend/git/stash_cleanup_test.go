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

	cfgStore := config.NewConfigStore(s, dbPath, "test")
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

// TestDropStashEntriesForTask_ByHash verifies that DropStashEntriesForTask
// uses hash-based drops. It creates kanban stashes interleaved with unrelated
// stashes, then verifies only the kanban entries are dropped.
func TestDropStashEntriesForTask_ByHash(t *testing.T) {
	repoDir, projID, taskID, _, _, wm := setupStashTest(t)

	// Create some kanban-labeled stashes for this task
	for i, label := range []string{"mike-switch", "mike-fork", "mike-checkpoint"} {
		msg := fmt.Sprintf("%s-%s", label, taskID)
		createStash(t, repoDir, fmt.Sprintf("change for %s\n", label), msg)
		_ = i
	}

	// Create unrelated stashes (non-kanban)
	for i := 0; i < 2; i++ {
		createStash(t, repoDir, fmt.Sprintf("unrelated change %d\n", i), fmt.Sprintf("manual stash %d", i))
	}

	// Verify we have 5 stash entries (3 kanban + 2 unrelated)
	before := countStashEntries(t, repoDir)
	if before != 5 {
		t.Fatalf("expected 5 stash entries before cleanup, got %d", before)
	}

	// Drop kanban entries for this task
	err := wm.DropStashEntriesForTask(projID, taskID)
	if err != nil {
		t.Fatalf("DropStashEntriesForTask: %v", err)
	}

	// Should have exactly 2 entries left (the unrelated ones)
	after := countStashEntries(t, repoDir)
	if after != 2 {
		t.Fatalf("expected 2 stash entries after cleanup, got %d", after)
	}

	// Verify the remaining entries are the unrelated ones
	subjects := stashSubjects(t, repoDir)
	for _, s := range subjects {
		if strings.Contains(s, "mike-switch-"+taskID) ||
			strings.Contains(s, "mike-fork-"+taskID) ||
			strings.Contains(s, "mike-checkpoint-"+taskID) {
			t.Errorf("kanban stash entry should have been dropped, found: %s", s)
		}
	}
}

// TestDropStashEntriesForTask_NoFalsePositives verifies that dropping stashes
// for one task does not affect stashes for a different task, even if the task
// IDs share a substring.
func TestDropStashEntriesForTask_NoFalsePositives(t *testing.T) {
	repoDir, projID, taskID, s, _, wm := setupStashTest(t)

	// Create a second task in the DB
	otherTaskID := "other-task-id"
	otherTask := &types.Task{
		ID:        otherTaskID,
		ProjectID: projID,
		Title:     "Other task",
		Status:    types.StatusInProgress,
		Branch:    "task/" + otherTaskID,
	}
	if err := s.CreateTask(otherTask); err != nil {
		t.Fatalf("create other task: %v", err)
	}

	// Create kanban stashes for BOTH tasks
	for _, tid := range []string{taskID, otherTaskID} {
		msg := fmt.Sprintf("mike-switch-%s", tid)
		createStash(t, repoDir, fmt.Sprintf("change for %s\n", tid), msg)
	}

	before := countStashEntries(t, repoDir)
	if before != 2 {
		t.Fatalf("expected 2 stash entries before cleanup, got %d", before)
	}

	// Drop only the first task's stashes
	err := wm.DropStashEntriesForTask(projID, taskID)
	if err != nil {
		t.Fatalf("DropStashEntriesForTask: %v", err)
	}

	after := countStashEntries(t, repoDir)
	if after != 1 {
		t.Fatalf("expected 1 stash entry after cleanup, got %d", after)
	}

	// The remaining entry should be for the OTHER task
	subjects := stashSubjects(t, repoDir)
	if len(subjects) != 1 {
		t.Fatalf("expected 1 subject, got %d", len(subjects))
	}
	if !strings.Contains(subjects[0], otherTaskID) {
		t.Errorf("remaining stash should belong to other task, got: %s", subjects[0])
	}
}

// TestDropStashEntriesForTask_InterleavedStashes verifies correct behavior
// when kanban and non-kanban stashes are interleaved at various indices.
func TestDropStashEntriesForTask_InterleavedStashes(t *testing.T) {
	repoDir, projID, taskID, _, _, wm := setupStashTest(t)

	// Create an interleaved pattern: unrelated, kanban, unrelated, kanban, unrelated
	stashMessages := []string{
		"manual stash 0",
		fmt.Sprintf("mike-switch-%s", taskID),
		"manual stash 1",
		fmt.Sprintf("mike-fork-%s", taskID),
		"manual stash 2",
	}

	for _, msg := range stashMessages {
		createStash(t, repoDir, fmt.Sprintf("change for %s\n", msg), msg)
	}

	before := countStashEntries(t, repoDir)
	if before != 5 {
		t.Fatalf("expected 5 stash entries before cleanup, got %d", before)
	}

	err := wm.DropStashEntriesForTask(projID, taskID)
	if err != nil {
		t.Fatalf("DropStashEntriesForTask: %v", err)
	}

	after := countStashEntries(t, repoDir)
	if after != 3 {
		t.Fatalf("expected 3 stash entries after cleanup, got %d", after)
	}

	// Verify only manual stashes remain
	subjects := stashSubjects(t, repoDir)
	for _, s := range subjects {
		if strings.Contains(s, "mike-") {
			t.Errorf("kanban stash should have been dropped, found: %s", s)
		}
	}
}

// TestDropStashEntriesForBranch_ByHash verifies that DropStashEntriesForBranch
// uses hash-based drops. It creates stashes on a specific branch and verifies
// only those are dropped.
func TestDropStashEntriesForBranch_ByHash(t *testing.T) {
	repoDir, projID, taskID, _, _, wm := setupStashTest(t)

	branchName := "task/" + taskID

	// Create a branch and stashes while on it (so git records the branch name)
	runGit(t, repoDir, "branch", branchName)
	runGit(t, repoDir, "checkout", branchName)
	for i := 0; i < 3; i++ {
		createStash(t, repoDir, fmt.Sprintf("change %d\n", i), fmt.Sprintf("mike-switch-%s entry %d", taskID, i))
	}

	// Switch to master and create stashes there (different branch in subject)
	runGit(t, repoDir, "checkout", "master")
	for i := 0; i < 2; i++ {
		createStash(t, repoDir, fmt.Sprintf("other branch %d\n", i), fmt.Sprintf("other branch stash %d", i))
	}

	before := countStashEntries(t, repoDir)
	if before != 5 {
		t.Fatalf("expected 5 stash entries before cleanup, got %d", before)
	}

	// Drop stashes for the target branch
	err := wm.DropStashEntriesForBranch(projID, branchName)
	if err != nil {
		t.Fatalf("DropStashEntriesForBranch: %v", err)
	}

	after := countStashEntries(t, repoDir)
	if after != 2 {
		t.Fatalf("expected 2 stash entries after cleanup, got %d", after)
	}

	// Verify remaining entries are from master
	subjects := stashSubjects(t, repoDir)
	for _, s := range subjects {
		if strings.Contains(s, "On "+branchName+":") {
			t.Errorf("stash on branch %s should have been dropped, found: %s", branchName, s)
		}
	}
}

// TestDropStashEntriesForBranch_NoFalsePositives verifies that dropping stashes
// for one branch does not affect stashes on a different branch with a similar name.
func TestDropStashEntriesForBranch_NoFalsePositives(t *testing.T) {
	repoDir, projID, _, _, _, wm := setupStashTest(t)

	// Create stashes on two different branches
	branchA := "task/alpha-bravo-charlie"
	branchB := "task/bravo-charlie-delta" // shares "bravo-charlie" substring

	// Create stash on branchA
	runGit(t, repoDir, "branch", branchA)
	runGit(t, repoDir, "checkout", branchA)
	createStash(t, repoDir, fmt.Sprintf("change for %s\n", branchA), fmt.Sprintf("work on %s", branchA))

	// Create stash on branchB
	runGit(t, repoDir, "branch", branchB)
	runGit(t, repoDir, "checkout", branchB)
	createStash(t, repoDir, fmt.Sprintf("change for %s\n", branchB), fmt.Sprintf("work on %s", branchB))

	before := countStashEntries(t, repoDir)
	if before != 2 {
		t.Fatalf("expected 2 stash entries before cleanup, got %d", before)
	}

	// Drop only branchA's stashes
	err := wm.DropStashEntriesForBranch(projID, branchA)
	if err != nil {
		t.Fatalf("DropStashEntriesForBranch: %v", err)
	}

	after := countStashEntries(t, repoDir)
	if after != 1 {
		t.Fatalf("expected 1 stash entry after cleanup, got %d", after)
	}

	// The remaining entry should be for branchB
	subjects := stashSubjects(t, repoDir)
	if len(subjects) != 1 {
		t.Fatalf("expected 1 subject, got %d", len(subjects))
	}
	if !strings.Contains(subjects[0], branchB) {
		t.Errorf("remaining stash should belong to branchB, got: %s", subjects[0])
	}
}

// TestDropStashEntriesForTask_EmptyStashList verifies no error when there are no stashes.
func TestDropStashEntriesForTask_EmptyStashList(t *testing.T) {
	_, projID, taskID, _, _, wm := setupStashTest(t)

	err := wm.DropStashEntriesForTask(projID, taskID)
	if err != nil {
		t.Fatalf("DropStashEntriesForTask should not error on empty stash list: %v", err)
	}
}

// TestDropStashEntriesForBranch_EmptyStashList verifies no error when there are no stashes.
func TestDropStashEntriesForBranch_EmptyStashList(t *testing.T) {
	_, projID, _, _, _, wm := setupStashTest(t)

	err := wm.DropStashEntriesForBranch(projID, "task/some-task")
	if err != nil {
		t.Fatalf("DropStashEntriesForBranch should not error on empty stash list: %v", err)
	}
}

// TestDropStashEntriesForTask_AllKanbanLabels verifies all kanban label types
// are matched and dropped: switch, fork, checkpoint, sync-merge, auto-merge.
func TestDropStashEntriesForTask_AllKanbanLabels(t *testing.T) {
	repoDir, projID, taskID, _, _, wm := setupStashTest(t)

	labels := []string{
		"mike-switch",
		"mike-fork",
		"mike-checkpoint",
		"mike-sync-merge",
		"mike-auto-merge",
	}

	for _, label := range labels {
		msg := fmt.Sprintf("%s-%s", label, taskID)
		createStash(t, repoDir, fmt.Sprintf("change for %s\n", label), msg)
	}

	before := countStashEntries(t, repoDir)
	if before != 5 {
		t.Fatalf("expected 5 stash entries before cleanup, got %d", before)
	}

	err := wm.DropStashEntriesForTask(projID, taskID)
	if err != nil {
		t.Fatalf("DropStashEntriesForTask: %v", err)
	}

	after := countStashEntries(t, repoDir)
	if after != 0 {
		t.Fatalf("expected 0 stash entries after cleanup, got %d", after)
	}
}
