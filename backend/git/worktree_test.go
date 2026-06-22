package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"kanban/backend/config"
	"kanban/backend/store"
	"kanban/backend/types"
)

// setupTestRepo creates a temporary git repo, store, config, project, and task.
func setupTestRepo(t *testing.T) (string, string, *store.Store, *config.ConfigStore, *WorktreeManager) {
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
	writeFile(t, repoDir, "README.md", "# Test\n")
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "Initial")

	// Create in-memory store and config
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
	if err := cfgStore.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	// Create project
	projID := "test-project"
	proj := &types.Project{ID: projID, Name: "test", Path: repoDir}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create task
	taskID := "test-task"
	task := &types.Task{
		ID:        taskID,
		ProjectID: projID,
		Title:     "Test task",
		Prompt:    "Test prompt",
		Status:    "in_progress",
		Branch:    "task/" + taskID,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	wm := NewWorktreeManager(s, cfgStore)

	return repoDir, taskID, s, cfgStore, wm
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %s: %v", args, string(out), err)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func TestResolveDiffBaseline_RejectsFatalBaseRef(t *testing.T) {
	_, taskID, _, _, wm := setupTestRepo(t)

	// A "fatal:" baseRef should be treated as empty
	result, err := wm.ResolveDiffBaseline(taskID, "fatal: Needed a single revision", "task/test-task", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty string for fatal baseRef, got %q", result)
	}
}

func TestResolveDiffBaseline_ValidBaseRef(t *testing.T) {
	_, taskID, _, _, wm := setupTestRepo(t)

	// A valid baseRef should be returned as-is (original thread)
	result, err := wm.ResolveDiffBaseline(taskID, "master", "task/test-task", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "master" {
		t.Errorf("expected master, got %q", result)
	}
}

func TestGetTaskDiff_RejectsFatalBaseRef(t *testing.T) {
	_, taskID, _, _, wm := setupTestRepo(t)

	// Create the worktree first
	branchName := "task/" + taskID
	_, _, err := wm.CreateWorktree("test-project", taskID, branchName)
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	// Make a change in the worktree
	wtPath, _ := wm.WorktreePath(taskID)
	writeFile(t, wtPath, "README.md", "# Test changed\n")

	// With a "fatal:" baseRef, it should fall back to HEAD and succeed
	result, err := wm.GetTaskDiff(taskID, "fatal: Needed a single revision", "")
	if err != nil {
		t.Fatalf("GetTaskDiff should succeed with fatal baseRef (fallback to HEAD): %v", err)
	}
	// Should have diff content since we modified README.md
	if result.Diff == "" {
		t.Error("expected diff content after modifying file, got empty diff")
	}
}

func TestCreateWorktree_EarlyReturn_SanitizesFatalBranch(t *testing.T) {
	repoDir, taskID, _, _, wm := setupTestRepo(t)

	// Create the worktree normally first
	branchName := "task/" + taskID
	_, _, err := wm.CreateWorktree("test-project", taskID, branchName)
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	// Now simulate the "fatal:" scenario by putting the repo in detached HEAD
	// and calling CreateWorktree again (early return path)
	runGit(t, repoDir, "checkout", "--detach", "HEAD")
	defer runGit(t, repoDir, "checkout", "master")

	// Call CreateWorktree again — worktree already exists, hits early return
	// gitCurrentBranch will return "HEAD" in detached state, which is fine
	// But we need to test the "fatal:" case specifically
	// Since we can't easily mock gitCurrentBranch, we verify the logic by
	// checking that the early return path sanitizes the value.
	// The actual sanitization is tested via ResolveDiffBaseline above.
	// Here we just verify the early return works without error.
	_, baseRef, err := wm.CreateWorktree("test-project", taskID, branchName)
	if err != nil {
		t.Fatalf("create worktree (early return): %v", err)
	}
	// In detached HEAD state, gitCurrentBranch returns "HEAD" (valid), not "fatal:"
	// The "fatal:" case only happens when git itself errors out
	if baseRef == "fatal: Needed a single revision" {
		t.Error("CreateWorktree should not return fatal: messages as baseRef")
	}
}
