package git

import (
	"os"
	"path/filepath"
	"testing"

	"mike/backend/config"
	"mike/backend/store"
	"mike/backend/types"
)

// TestWorktreePathIgnoresProviderConfig pins where a task's worktree lives to the workspace dir alone.
// Answering it from the full config makes every path lookup fail when an unrelated config row is
// unreadable, and decrypts every provider API key to name a directory.
func TestWorktreePathIgnoresProviderConfig(t *testing.T) {
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	worktreesDir := filepath.Join(tmpDir, "worktrees")
	os.MkdirAll(repoDir, 0o755)

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
	defer s.Close()

	cfgStore := config.NewConfigStore(s, dbPath, "test", nil, false)
	cfgStore.SetWorkspaceDirOverride(worktreesDir)

	const projID = "cfg-project"
	if err := s.CreateProject(&types.Project{ID: projID, Name: "cfg", Path: repoDir}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	const taskID = "cfg-task"
	task := &types.Task{
		ID:        taskID,
		ProjectID: projID,
		Title:     "Config task",
		Prompt:    "Test prompt",
		Status:    types.StatusInProgress,
		Branch:    "task/" + taskID,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	wm := NewWorktreeManager(s, cfgStore)

	if err := s.SetConfigValue("providers", "{not json"); err != nil {
		t.Fatalf("corrupt providers row: %v", err)
	}
	if _, err := cfgStore.GetConfig(); err == nil {
		t.Fatal("test premise: an unparseable providers row must make GetConfig fail")
	}

	got, err := wm.WorktreePath(taskID)
	if err != nil {
		t.Fatalf("worktree path with an unreadable providers row: %v", err)
	}
	if want := filepath.Join(worktreesDir, projID, taskID); got != want {
		t.Errorf("worktree path = %q, want %q", got, want)
	}
}
