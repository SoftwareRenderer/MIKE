package git

import (
	"os"
	"path/filepath"
	"testing"
)

// A project's worktrees all live under <worktreesDir>/<projectID>, and a task's own removal takes only
// its directory, so only this removal takes the project's directory itself.

func TestRemoveProjectWorktreesTakesTheWholeProjectDirectory(t *testing.T) {
	_, taskID, _, _, wm := setupTestRepo(t)

	worktreePath, _, err := wm.CreateWorktree("test-project", taskID, "task/"+taskID, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	projectDir := filepath.Dir(worktreePath)
	// A task whose own removal failed leaves its directory behind inside the project's.
	leftover := filepath.Join(projectDir, "task-that-refused-removal")
	if err := os.MkdirAll(leftover, 0o755); err != nil {
		t.Fatal(err)
	}
	// Another project's worktrees are not in range.
	foreign := filepath.Join(filepath.Dir(projectDir), "other-project", "other-task")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := wm.RemoveProjectWorktrees("test-project"); err != nil {
		t.Fatalf("remove project worktrees: %v", err)
	}
	for _, gone := range []string{worktreePath, leftover, projectDir} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s survived the removal of its project", gone)
		}
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("another project's worktree was removed: %v", err)
	}
}
