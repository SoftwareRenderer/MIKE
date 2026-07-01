package exec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/config"
	"kanban/backend/git"
	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/types"
)

// setupExecutorWithGit creates a TaskExecutor with a real git repo for testing.
func setupExecutorWithGit(t *testing.T) (*TaskExecutor, string, string, string) {
	t.Helper()
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	worktreesDir := filepath.Join(tmpDir, "worktrees")
	os.MkdirAll(repoDir, 0o755)
	os.MkdirAll(worktreesDir, 0o755)

	// Initialize git repo
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")
	os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# Test\n"), 0o644)
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "Initial")

	// Get the initial commit SHA
	var initialSHA string
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD")
	out, _ := cmd.Output()
	initialSHA = strings.TrimSpace(string(out))

	// Create store and config
	dbPath := filepath.Join(tmpDir, "test.db")
	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	cfgStore := config.NewConfigStore(s, dbPath)
	cfg, err := cfgStore.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.WorkspaceDir = worktreesDir
	cfgStore.UpdateConfig(cfg)

	// Create project
	projectID := "test-project"
	proj := &types.Project{ID: projectID, Name: "test", Path: repoDir}
	s.CreateProject(proj)

	// Create worktree manager and executor
	worktreeMgr := git.NewWorktreeManager(s, cfgStore)
	appStreamMgr := stream.NewAppStreamManager(s, worktreeMgr)

	te := &TaskExecutor{
		store:        s,
		configStore:  cfgStore,
		appStreamMgr: appStreamMgr,
		worktreeMgr:  worktreeMgr,
		tasks:        make(map[string]*executionState),
	}

	return te, repoDir, projectID, initialSHA
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %s: %v", args, string(out), err)
	}
}

// TestPrepareTaskExecution_PreservesBaseRefOnRestart verifies that when a task
// is restarted (worktree already exists), the stored BaseRef (commit SHA) is
// preserved and not overwritten with the main repo's branch name.
func TestPrepareTaskExecution_PreservesBaseRefOnRestart(t *testing.T) {
	te, _, projectID, initialSHA := setupExecutorWithGit(t)
	taskID := "test-baseref-task"
	branchName := "task/" + taskID

	// Create task with BaseRef already set (simulating first run completed)
	task := &types.Task{
		ID:            taskID,
		ProjectID:     projectID,
		Title:         "Test task",
		Prompt:        "Test prompt",
		Status:        "in_progress",
		BaseRef:       initialSHA,
		Branch:        branchName,
		LlamaServerURL: "http://localhost:8080",
		ModelName:     "test-model",
	}
	te.store.CreateTask(task)

	// Create the worktree (simulating it already exists from first run)
	_, _, err := te.worktreeMgr.CreateWorktree(projectID, taskID, branchName, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	// Now simulate restart: prepareTaskExecution is called again
	// Work the worktree exists, CreateWorktree returns early with branch name
	setup, err := te.prepareTaskExecution(projectID, taskID, "", "")
	if err != nil {
		t.Fatalf("prepare task execution: %v", err)
	}
	if setup == nil {
		t.Fatal("prepare task execution returned nil")
	}

	// Verify BaseRef is still the SHA, not a branch name
	if setup.task.BaseRef != initialSHA {
		t.Errorf("BaseRef was overwritten on restart: expected %q (SHA), got %q", initialSHA, setup.task.BaseRef)
	}
	if len(setup.task.BaseRef) != 40 {
		t.Errorf("BaseRef should be a 40-char SHA, got %q (len=%d)", setup.task.BaseRef, len(setup.task.BaseRef))
	}

	// Cleanup
	setup.cancel()
	te.StopTask(taskID)
}

// TestPrepareTaskExecution_SetsBaseRefOnFirstRun verifies that on first run
// (no existing BaseRef), the BaseRef is correctly set from CreateWorktree.
func TestPrepareTaskExecution_SetsBaseRefOnFirstRun(t *testing.T) {
	te, _, projectID, _ := setupExecutorWithGit(t)
	taskID := "test-baseref-first-run"
	branchName := "task/" + taskID

	// Create task WITHOUT BaseRef (first run)
	task := &types.Task{
		ID:            taskID,
		ProjectID:     projectID,
		Title:         "Test task",
		Prompt:        "Test prompt",
		Status:        "in_progress",
		Branch:        branchName,
		LlamaServerURL: "http://localhost:8080",
		ModelName:     "test-model",
	}
	te.store.CreateTask(task)

	// prepareTaskExecution on first run (no existing worktree)
	setup, err := te.prepareTaskExecution(projectID, taskID, "", "")
	if err != nil {
		t.Fatalf("prepare task execution: %v", err)
	}
	if setup == nil {
		t.Fatal("prepare task execution returned nil")
	}

	// Verify BaseRef is set to a SHA (40 hex chars)
	if setup.task.BaseRef == "" {
		t.Error("BaseRef should be set on first run")
	}
	if len(setup.task.BaseRef) != 40 {
		t.Errorf("BaseRef should be a 40-char SHA, got %q (len=%d)", setup.task.BaseRef, len(setup.task.BaseRef))
	}

	// Cleanup
	setup.cancel()
	te.StopTask(taskID)
}
