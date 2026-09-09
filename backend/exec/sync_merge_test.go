package exec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/git"
	"kanban/backend/types"
)

// TestSyncTargetIntoTask_NoNewCommits verifies that sync returns early
// when the target branch has no new commits beyond the task branch.
func TestSyncTargetIntoTask_NoNewCommits(t *testing.T) {
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	// Create a task with a branch
	taskID := "task-no-new"
	task := &types.Task{
		ID:             taskID,
		ProjectID:      projectID,
		Title:          "Test Task",
		Prompt:         "Test prompt",
		Status:         types.StatusInProgress,
		Branch:         "task/test-branch",
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.rawStore().CreateTask(task)

	// Create the task branch from master
	runGit(t, repoDir, "checkout", "-b", "task/test-branch")
	runGit(t, repoDir, "checkout", "master")

	// Create worktree for the task
	wtPath := filepath.Join(func() string { cfg, _ := te.configStore.GetConfig(); return cfg.WorkspaceDir }(), projectID, taskID)
	os.MkdirAll(wtPath, 0o755)
	runGit(t, repoDir, "worktree", "add", "--detach", wtPath, "task/test-branch")
	runGitInDir(t, wtPath, "checkout", "task/test-branch")

	// No new commits on master — sync should be a no-op
	err := te.syncTargetIntoTask(projectID, taskID, "master", "task/test-branch", task)
	if err != nil {
		t.Fatalf("syncTargetIntoTask should succeed with no new commits: %v", err)
	}

	// Task state should be unchanged
	taskFromDB, _ := te.rawStore().GetTaskByID(taskID)
	if taskFromDB.ExecutionState != "" && taskFromDB.ExecutionState != types.ExecStateIdle {
		t.Errorf("execution state should be idle, got %q", taskFromDB.ExecutionState)
	}
}

// TestSyncTargetIntoTask_CleanMerge verifies that when target has new commits
// that don't conflict, the sync merges cleanly.
func TestSyncTargetIntoTask_CleanMerge(t *testing.T) {
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	taskID := "task-clean"
	task := &types.Task{
		ID:             taskID,
		ProjectID:      projectID,
		Title:          "Test Task",
		Prompt:         "Test prompt",
		Status:         types.StatusInProgress,
		Branch:         "task/clean-branch",
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.rawStore().CreateTask(task)

	// Create task branch with a change to file A
	runGit(t, repoDir, "checkout", "-b", "task/clean-branch")
	os.WriteFile(filepath.Join(repoDir, "fileA.txt"), []byte("task version\n"), 0o644)
	runGit(t, repoDir, "add", "fileA.txt")
	runGit(t, repoDir, "commit", "-m", "task change file A")
	runGit(t, repoDir, "checkout", "master")

	// Create worktree for the task
	wtPath := filepath.Join(func() string { cfg, _ := te.configStore.GetConfig(); return cfg.WorkspaceDir }(), projectID, taskID)
	os.MkdirAll(wtPath, 0o755)
	runGit(t, repoDir, "worktree", "add", "--detach", wtPath, "task/clean-branch")
	runGitInDir(t, wtPath, "checkout", "task/clean-branch")

	// Add a non-conflicting change to master (file B)
	os.WriteFile(filepath.Join(repoDir, "fileB.txt"), []byte("master version\n"), 0o644)
	runGit(t, repoDir, "add", "fileB.txt")
	runGit(t, repoDir, "commit", "-m", "master change file B")

	// Sync should merge cleanly
	err := te.syncTargetIntoTask(projectID, taskID, "master", "task/clean-branch", task)
	if err != nil {
		t.Fatalf("syncTargetIntoTask should succeed with clean merge: %v", err)
	}

	// Verify fileB.txt exists in the task branch (merged from master)
	mergedFile := filepath.Join(wtPath, "fileB.txt")
	content, err := os.ReadFile(mergedFile)
	if err != nil {
		t.Fatalf("fileB.txt should exist in task worktree after sync: %v", err)
	}
	if string(content) != "master version\n" {
		t.Errorf("fileB.txt should have master content, got %q", string(content))
	}
}

// TestSyncTargetIntoTask_Conflict_EntersMergeConflictState verifies that
// when a sync merge has conflicts, the task enters merge_conflict state
// and the merge is kept in progress for the LLM to resolve.
func TestSyncTargetIntoTask_Conflict_EntersMergeConflictState(t *testing.T) {
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	taskID := "task-conflict"
	task := &types.Task{
		ID:             taskID,
		ProjectID:      projectID,
		Title:          "Test Task",
		Prompt:         "Test prompt",
		Status:         types.StatusInProgress,
		Branch:         "task/conflict-branch",
		LlamaServerURL: "",
		ModelName:      "",
	}
	te.rawStore().CreateTask(task)

	// Create task branch with a change to shared file
	runGit(t, repoDir, "checkout", "-b", "task/conflict-branch")
	os.WriteFile(filepath.Join(repoDir, "shared.txt"), []byte("task version\n"), 0o644)
	runGit(t, repoDir, "add", "shared.txt")
	runGit(t, repoDir, "commit", "-m", "task change shared")
	runGit(t, repoDir, "checkout", "master")

	// Create worktree for the task
	wtPath := filepath.Join(func() string { cfg, _ := te.configStore.GetConfig(); return cfg.WorkspaceDir }(), projectID, taskID)
	os.MkdirAll(wtPath, 0o755)
	runGit(t, repoDir, "worktree", "add", "--detach", wtPath, "task/conflict-branch")
	runGitInDir(t, wtPath, "checkout", "task/conflict-branch")

	// Add conflicting change to master
	os.WriteFile(filepath.Join(repoDir, "shared.txt"), []byte("master version\n"), 0o644)
	runGit(t, repoDir, "add", "shared.txt")
	runGit(t, repoDir, "commit", "-m", "master change shared")

	// Sync should conflict and enter merge_conflict state
	err := te.syncTargetIntoTask(projectID, taskID, "master", "task/conflict-branch", task)
	if err != nil {
		t.Fatalf("syncTargetIntoTask should return nil (enters merge_conflict state): %v", err)
	}

	// Verify task is in merge_conflict state
	taskFromDB, _ := te.rawStore().GetTaskByID(taskID)
	if !taskFromDB.HasMergeConflict {
		t.Fatalf("expected has_merge_conflict=true, got false")
	}

	// Verify merge is still in progress (MERGE_HEAD exists)
	if !isMergeInProgress(wtPath) {
		t.Fatal("merge should still be in progress (MERGE_HEAD should exist)")
	}

	// Verify conflicted file has conflict markers
	sharedContent, _ := os.ReadFile(filepath.Join(wtPath, "shared.txt"))
	if !strings.Contains(string(sharedContent), "<<<<<<<") {
		t.Error("shared.txt should have conflict markers")
	}
}

// TestSyncTargetIntoTask_WorktreeMissing verifies that sync returns nil
// (no error) when the task's worktree doesn't exist.
func TestSyncTargetIntoTask_WorktreeMissing(t *testing.T) {
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	taskID := "task-no-worktree"
	task := &types.Task{
		ID:             taskID,
		ProjectID:      projectID,
		Title:          "Test Task",
		Prompt:         "Test prompt",
		Status:         types.StatusInProgress,
		Branch:         "task/no-wt-branch",
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.rawStore().CreateTask(task)

	// Create the task branch
	runGit(t, repoDir, "checkout", "-b", "task/no-wt-branch")
	os.WriteFile(filepath.Join(repoDir, "task.txt"), []byte("task\n"), 0o644)
	runGit(t, repoDir, "add", "task.txt")
	runGit(t, repoDir, "commit", "-m", "task change")
	runGit(t, repoDir, "checkout", "master")

	// Add a commit to master (so there are new commits)
	os.WriteFile(filepath.Join(repoDir, "master.txt"), []byte("master\n"), 0o644)
	runGit(t, repoDir, "add", "master.txt")
	runGit(t, repoDir, "commit", "-m", "master change")

	// Don't create a worktree — sync should skip gracefully
	err := te.syncTargetIntoTask(projectID, taskID, "master", "task/no-wt-branch", task)
	if err != nil {
		t.Fatalf("syncTargetIntoTask should return nil when worktree is missing: %v", err)
	}
}

// TestSyncTargetIntoTask_SubtaskChain verifies that sync works correctly
// in a subtask chain: child task syncs parent branch (not origin).
func TestSyncTargetIntoTask_SubtaskChain(t *testing.T) {
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	// Create parent task
	parentID := "parent-sync"
	parent := &types.Task{
		ID:             parentID,
		ProjectID:      projectID,
		Title:          "Parent Task",
		Prompt:         "Parent prompt",
		Status:         types.StatusInProgress,
		Branch:         "task/parent-branch",
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.rawStore().CreateTask(parent)

	// Create parent branch
	runGit(t, repoDir, "checkout", "-b", "task/parent-branch")
	os.WriteFile(filepath.Join(repoDir, "parent.txt"), []byte("parent v1\n"), 0o644)
	runGit(t, repoDir, "add", "parent.txt")
	runGit(t, repoDir, "commit", "-m", "parent initial")
	runGit(t, repoDir, "checkout", "master")

	// Create parent worktree
	parentWtPath := filepath.Join(func() string { cfg, _ := te.configStore.GetConfig(); return cfg.WorkspaceDir }(), projectID, parentID)
	os.MkdirAll(parentWtPath, 0o755)
	runGit(t, repoDir, "worktree", "add", "--detach", parentWtPath, "task/parent-branch")
	runGitInDir(t, parentWtPath, "checkout", "task/parent-branch")

	// Create child task
	childID := "child-sync"
	child := &types.Task{
		ID:             childID,
		ProjectID:      projectID,
		Title:          "Child Task",
		Prompt:         "Child prompt",
		Status:         types.StatusInProgress,
		ParentTaskID:   parentID,
		Branch:         "task/child-branch",
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.rawStore().CreateTask(child)

	// Create child branch from parent branch (in parent worktree since it's locked there)
	runGitInDir(t, parentWtPath, "checkout", "-b", "task/child-branch")
	os.WriteFile(filepath.Join(parentWtPath, "child.txt"), []byte("child v1\n"), 0o644)
	runGitInDir(t, parentWtPath, "add", "child.txt")
	runGitInDir(t, parentWtPath, "commit", "-m", "child initial")
	runGitInDir(t, parentWtPath, "checkout", "task/parent-branch")

	// Create child worktree
	childWtPath := filepath.Join(func() string { cfg, _ := te.configStore.GetConfig(); return cfg.WorkspaceDir }(), projectID, childID)
	os.MkdirAll(childWtPath, 0o755)
	runGit(t, repoDir, "worktree", "add", "--detach", childWtPath, "task/child-branch")
	runGitInDir(t, childWtPath, "checkout", "task/child-branch")

	// Add a non-conflicting commit to parent branch
	os.WriteFile(filepath.Join(parentWtPath, "parent_new.txt"), []byte("parent new\n"), 0o644)
	runGitInDir(t, parentWtPath, "add", "parent_new.txt")
	runGitInDir(t, parentWtPath, "commit", "-m", "parent new file")

	// Reload child task
	child, _ = te.rawStore().GetTaskByID(childID)

	// Sync parent into child — should merge cleanly
	err := te.syncTargetIntoTask(projectID, childID, "task/parent-branch", "task/child-branch", child)
	if err != nil {
		t.Fatalf("syncTargetIntoTask should succeed: %v", err)
	}

	// Verify parent_new.txt exists in child worktree
	mergedFile := filepath.Join(childWtPath, "parent_new.txt")
	if _, err := os.Stat(mergedFile); err != nil {
		t.Fatalf("parent_new.txt should exist in child worktree after sync: %v", err)
	}
}

// TestSyncTargetIntoTask_KeepsUnstagedWorkWhenTheStashFails is the lost-work regression. The sync
// begins with `git checkout -f`, which reverts tracked modifications to HEAD with exit 0 and no
// warning, so the stash taken before it is the only thing protecting the task's unstaged work. When
// that stash cannot be taken, the sync has to refuse.
func TestSyncTargetIntoTask_KeepsUnstagedWorkWhenTheStashFails(t *testing.T) {
	te, repoDir, projectID := setupExecutorWithGitForSubtask(t)

	taskID := "task-refuse"
	task := &types.Task{
		ID:             taskID,
		ProjectID:      projectID,
		Title:          "Test Task",
		Prompt:         "Test prompt",
		Status:         types.StatusInProgress,
		Branch:         "task/refuse-branch",
		LlamaServerURL: "http://localhost:8080",
		ModelName:      "test-model",
	}
	te.rawStore().CreateTask(task)

	runGit(t, repoDir, "checkout", "-b", "task/refuse-branch")
	os.WriteFile(filepath.Join(repoDir, "fileA.txt"), []byte("committed version\n"), 0o644)
	runGit(t, repoDir, "add", "fileA.txt")
	runGit(t, repoDir, "commit", "-m", "task change file A")
	runGit(t, repoDir, "checkout", "master")

	wtPath := filepath.Join(func() string { cfg, _ := te.configStore.GetConfig(); return cfg.WorkspaceDir }(), projectID, taskID)
	os.MkdirAll(wtPath, 0o755)
	runGit(t, repoDir, "worktree", "add", "--detach", wtPath, "task/refuse-branch")
	runGitInDir(t, wtPath, "checkout", "task/refuse-branch")

	// master moves, so there is something to sync.
	os.WriteFile(filepath.Join(repoDir, "fileB.txt"), []byte("master version\n"), 0o644)
	runGit(t, repoDir, "add", "fileB.txt")
	runGit(t, repoDir, "commit", "-m", "master change file B")

	// The work at risk: an unstaged edit to a tracked file, which is what a forced checkout reverts.
	os.WriteFile(filepath.Join(wtPath, "fileA.txt"), []byte("uncommitted work\n"), 0o644)

	// The stash cannot be taken: the stash ref is unusable, while the rest of the repository — the
	// checkout below included — still works. An index lock would be the obvious injection and the
	// wrong one, because it stops that checkout too and the loss never happens.
	commonDirCmd := git.Command("", "rev-parse", "--path-format=absolute", "--git-common-dir")
	commonDirCmd.Dir = wtPath
	commonDirOut, err := commonDirCmd.Output()
	if err != nil {
		t.Fatalf("resolve the repository's common dir: %v", err)
	}
	blocked := filepath.Join(strings.TrimSpace(string(commonDirOut)), "refs", "stash")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatalf("block the stash ref: %v", err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "blocker"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("block the stash ref: %v", err)
	}
	defer func() { _ = os.RemoveAll(blocked) }()

	err = te.syncTargetIntoTask(projectID, taskID, "master", "task/refuse-branch", task)
	if err == nil {
		t.Fatal("the sync ran with nothing protecting the worktree's unstaged work")
	}

	if content, readErr := os.ReadFile(filepath.Join(wtPath, "fileA.txt")); readErr != nil || string(content) != "uncommitted work\n" {
		t.Errorf("the unstaged edit is gone: %q (%v)", string(content), readErr)
	}
	if isMergeInProgress(wtPath) {
		t.Error("a merge is in progress in a worktree whose sync was refused")
	}
	if refreshed, _ := te.rawStore().GetTaskByID(taskID); refreshed.HasMergeConflict {
		t.Error("the task entered merge_conflict state without a merge having been attempted")
	}
}
