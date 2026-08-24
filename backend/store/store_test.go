package store

import (
	"path/filepath"
	"testing"
	"time"

	"kanban/backend/types"
)

// testStore creates a new Store with a temporary database for testing.
// The temp directory is cleaned up by t.Cleanup.
func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// newTask creates a Task with default values for testing.
func newTask(id, projectID string) *types.Task {
	now := time.Now().UTC()
	return &types.Task{
		ID:             id,
		ProjectID:      projectID,
		Title:          id,
		Prompt:         "test prompt",
		Status:         "backlog",
		ExecutionState: "idle",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// newProject creates a Project with default values for testing.
func newProject(id, name, path string) *types.Project {
	now := time.Now().UTC()
	return &types.Project{
		ID:        id,
		Name:      name,
		Path:      path,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// newMessage creates a TaskMessage with default values for testing.
func newMessage(id, taskID, role, content string) *types.TaskMessage {
	return &types.TaskMessage{
		ID:        id,
		TaskID:    taskID,
		Role:      role,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}
}

// newThread creates a TaskThread with default values for testing.
func newThread(id, taskID, name string) *types.TaskThread {
	return &types.TaskThread{
		ID:        id,
		TaskID:    taskID,
		Name:      name,
		IsActive:  false,
		CreatedAt: time.Now().UTC(),
	}
}

// newDependency creates a TaskDependency with default values for testing.
func newDependency(id, parentID, childID string) *types.TaskDependency {
	return &types.TaskDependency{
		ID:           id,
		ParentTaskID: parentID,
		ChildTaskID:  childID,
		AutoStart:    true,
		CreatedAt:    time.Now().UTC(),
	}
}

// newReviewComment creates a ReviewComment with default values for testing.
func newReviewComment(id, taskID, filePath, comment string, lineNum int) *types.ReviewComment {
	now := time.Now().UTC()
	return &types.ReviewComment{
		ID:        id,
		TaskID:    taskID,
		FilePath:  filePath,
		LineNum:   lineNum,
		Comment:   comment,
		Resolved:  false,
		CreatedAt: now,
		UpdatedAt: now,
	}
}
