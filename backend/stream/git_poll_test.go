package stream

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"kanban/backend/config"
	"kanban/backend/git"
	"kanban/backend/store"
	"kanban/backend/types"
)

// TestBranchPollerLifecycle verifies that the branch poller starts, runs, and stops cleanly.
func TestBranchPollerLifecycle(t *testing.T) {
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
	defer s.Close()

	cfgStore := config.NewConfigStore(s, dbPath)
	cfg, err := cfgStore.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.WorkspaceDir = worktreesDir
	if err := cfgStore.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	projID := "test-project"
	taskID := "test-task"
	branchName := "kanban/" + taskID

	proj := &types.Project{ID: projID, Name: "test", Path: repoDir}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := &types.Task{
		ID:          taskID,
		ProjectID:   projID,
		Title:       "Test task",
		Prompt:      "Test prompt",
		Status:      "in_progress",
		StatusState: "running",
		Branch:      branchName,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	wm := git.NewWorktreeManager(s, cfgStore)
	_, _, err = wm.CreateWorktree(projID, taskID, branchName)
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	asm := NewAppStreamManager(s, wm)

	// Start branch poller — should not panic
	asm.startBranchPoller(projID)

	// Wait for a few cycles
	time.Sleep(5500 * time.Millisecond)

	// Stop branch poller — should not panic
	asm.stopBranchPoller(projID)

	// Verify poller is stopped
	asm.branchPollersMu.Lock()
	_, exists := asm.branchPollers[projID]
	asm.branchPollersMu.Unlock()
	if exists {
		t.Fatal("expected branch poller to be stopped")
	}
}

// TestBranchPollerSkipsBacklog verifies that the branch poller handles tasks without worktrees gracefully.
func TestBranchPollerSkipsBacklog(t *testing.T) {
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
	defer s.Close()

	cfgStore := config.NewConfigStore(s, dbPath)
	cfg, err := cfgStore.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.WorkspaceDir = worktreesDir
	if err := cfgStore.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	projID := "test-project"
	taskID := "test-task"

	proj := &types.Project{ID: projID, Name: "test", Path: repoDir}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Task in in_progress but NO worktree (simulating backlog)
	task := &types.Task{
		ID:          taskID,
		ProjectID:   projID,
		Title:       "Test task",
		Prompt:      "Test prompt",
		Status:      "in_progress",
		StatusState: "running",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	wm := git.NewWorktreeManager(s, cfgStore)
	asm := NewAppStreamManager(s, wm)

	// Start branch poller — should not panic even without worktree
	asm.startBranchPoller(projID)
	defer asm.stopBranchPoller(projID)

	// Wait for a few cycles — should not panic
	time.Sleep(5500 * time.Millisecond)
}

// TestGitPollerDetectsChanges verifies that the worktree manager detects file changes.
func TestGitPollerDetectsChanges(t *testing.T) {
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
	defer s.Close()

	cfgStore := config.NewConfigStore(s, dbPath)
	cfg, err := cfgStore.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.WorkspaceDir = worktreesDir
	if err := cfgStore.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	projID := "test-project"
	taskID := "test-task"
	branchName := "kanban/" + taskID

	proj := &types.Project{ID: projID, Name: "test", Path: repoDir}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := &types.Task{
		ID:          taskID,
		ProjectID:   projID,
		Title:       "Test task",
		Prompt:      "Test prompt",
		Status:      "in_progress",
		StatusState: "running",
		Branch:      branchName,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	wm := git.NewWorktreeManager(s, cfgStore)
	_, _, err = wm.CreateWorktree(projID, taskID, branchName)
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	// Verify that the worktree manager can detect changes
	worktreePath := filepath.Join(worktreesDir, taskID)

	// Initial state — no diff
	raw1, err := wm.GitDiffRaw(taskID, "")
	if err != nil {
		t.Fatalf("initial diff raw: %v", err)
	}

	// Make a change
	writeFile(t, worktreePath, "new_file.txt", "Hello, world!\n")

	// After change — should detect diff
	raw2, err := wm.GitDiffRaw(taskID, "")
	if err != nil {
		t.Fatalf("diff raw after change: %v", err)
	}

	if raw1 == raw2 {
		t.Fatal("expected git diff raw to change after file creation")
	}

	// Verify full diff is available (untracked files show in Untracked, not Diff)
	diffResult, err := wm.GetTaskDiff(taskID, "", "")
	if err != nil {
		t.Fatalf("get task diff: %v", err)
	}
	// New untracked file should appear in Untracked list
	if len(diffResult.Untracked) == 0 && diffResult.Diff == "" {
		t.Fatal("expected diff or untracked files after file creation")
	}
}

// runGit runs a git command in the specified directory.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", args, err, out)
	}
}

// writeFile writes content to a file in the specified directory.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}
