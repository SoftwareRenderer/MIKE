package managers

import (
	"os"
	"path/filepath"
	"testing"

	"kanban/backend/types"
)

func TestEnrichWorktreeExists(t *testing.T) {
	tmpDir := t.TempDir()

	// Create worktree directories: workspaceDir/projectID/taskID
	projectID := "test-project"
	taskID1 := "task-1"
	taskID2 := "task-2"

	// task-1 has a worktree, task-2 does not
	os.MkdirAll(filepath.Join(tmpDir, projectID, taskID1), 0o755)

	tasks := []types.Task{
		{ID: taskID1, ProjectID: projectID},
		{ID: taskID2, ProjectID: projectID},
	}

	EnrichWorktreeExists(tasks, tmpDir)

	if !tasks[0].WorktreeExists {
		t.Error("task-1 should have worktree_exists=true")
	}
	if tasks[1].WorktreeExists {
		t.Error("task-2 should have worktree_exists=false")
	}
}

func TestEnrichWorktreeExists_UsesProjectID(t *testing.T) {
	tmpDir := t.TempDir()

	// Create worktree at the CORRECT path: workspaceDir/projectID/taskID
	projectID := "my-project"
	taskID := "my-task"
	os.MkdirAll(filepath.Join(tmpDir, projectID, taskID), 0o755)

	// Also create a directory at the WRONG path: workspaceDir/taskID (no projectID)
	// This should NOT match
	os.MkdirAll(filepath.Join(tmpDir, taskID), 0o755)

	tasks := []types.Task{
		{ID: taskID, ProjectID: projectID},
	}

	EnrichWorktreeExists(tasks, tmpDir)

	// Should find the worktree at workspaceDir/projectID/taskID
	if !tasks[0].WorktreeExists {
		t.Error("task should have worktree_exists=true (found at workspaceDir/projectID/taskID)")
	}
}

func TestEnrichTaskWorktree(t *testing.T) {
	tmpDir := t.TempDir()
	projectID := "proj"
	taskID := "task"
	os.MkdirAll(filepath.Join(tmpDir, projectID, taskID), 0o755)

	task := &types.Task{ID: taskID, ProjectID: projectID}
	enrichTaskWorktree(task, tmpDir)

	if !task.WorktreeExists {
		t.Error("task should have worktree_exists=true")
	}
}
