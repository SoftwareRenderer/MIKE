package store

import (
	"os"
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

// TestNewStoreFilePermissions verifies the database and its directory are not
// readable by anyone but the owner — the file holds session token hashes and
// encrypted provider secrets.
func TestNewStoreFilePermissions(t *testing.T) {
	t.Run("creates a private database", func(t *testing.T) {
		root := t.TempDir()
		dbPath := filepath.Join(root, "private", "kanban.db")
		s, err := NewStore(dbPath)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		s.Close()

		dirMode, err := os.Stat(filepath.Dir(dbPath))
		if err != nil {
			t.Fatalf("stat dir: %v", err)
		}
		if perm := dirMode.Mode().Perm(); perm != 0o700 {
			t.Errorf("data dir mode = %o; want 700", perm)
		}
		fileMode, err := os.Stat(dbPath)
		if err != nil {
			t.Fatalf("stat db: %v", err)
		}
		if perm := fileMode.Mode().Perm(); perm != 0o600 {
			t.Errorf("db file mode = %o; want 600", perm)
		}
	})

	t.Run("tightens a pre-existing world-readable database", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "kanban.db")
		if err := os.WriteFile(dbPath, []byte{}, 0o644); err != nil {
			t.Fatalf("seed db: %v", err)
		}
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatalf("chmod dir: %v", err)
		}

		s, err := NewStore(dbPath)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		// WAL sidecars hold the same rows as the main file until checkpointed.
		restrictSidecarModes(dbPath)
		s.Close()

		if perm := mustPerm(t, dir); perm != 0o700 {
			t.Errorf("data dir mode = %o; want 700", perm)
		}
		if perm := mustPerm(t, dbPath); perm != 0o600 {
			t.Errorf("db file mode = %o; want 600", perm)
		}
		if _, err := os.Stat(dbPath + "-wal"); err == nil {
			if perm := mustPerm(t, dbPath+"-wal"); perm != 0o600 {
				t.Errorf("wal sidecar mode = %o; want 600", perm)
			}
		}
	})
}

func mustPerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}
