package exec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/config"
	"mike/backend/git"
	"mike/backend/store"
	"mike/backend/types"
)

// setupCommitAndMergeTest creates a TaskExecutor with a real git repo
// containing a task branch with changes, ready for commit-and-merge testing.
func setupCommitAndMergeTest(t *testing.T) (*TaskExecutor, string, string, string) {
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

	// Create store and config
	dbPath := filepath.Join(tmpDir, "test.db")
	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	cfgStore := config.NewConfigStore(s, dbPath, "test", nil)
	cfgStore.SetWorkspaceDirOverride(worktreesDir)

	projectID := "test-project"
	proj := &types.Project{ID: projectID, Name: "test", Path: repoDir}
	s.CreateProject(proj)

	taskID := "test-task"
	task := &types.Task{
		ID:        taskID,
		ProjectID: projectID,
		Title:     "Test task",
		Prompt:    "Test prompt",
		Status:    types.StatusInProgress,
		Branch:    "task/test-task",
		BaseRef:   "master",
	}
	s.CreateTask(task)

	// Create task branch
	runGit(t, repoDir, "checkout", "-b", "task/test-task")
	runGit(t, repoDir, "checkout", "master")

	// Create worktree
	wtPath := filepath.Join(worktreesDir, projectID, taskID)
	os.MkdirAll(wtPath, 0o755)
	runGit(t, repoDir, "worktree", "add", wtPath, "task/test-task")

	// Create an unstaged change in the worktree (simulating LLM edits)
	os.WriteFile(filepath.Join(wtPath, "feature.txt"), []byte("feature content\n"), 0o644)
	runGitInDir(t, wtPath, "add", "feature.txt")

	worktreeMgr := git.NewWorktreeManager(s, cfgStore)
	appStreamMgr := newTestStreamMgr(t, s, worktreeMgr)

	te := newTestExecutorWith(t, Deps{
		Store:        s,
		ConfigStore:  cfgStore,
		AppStreamMgr: appStreamMgr,
		WorktreeMgr:  worktreeMgr,
		CommitQueue:  NewCommitQueue(),
	})

	return te, repoDir, projectID, wtPath
}

// TestCommitAndMerge_AutoMergeFalse_SkipsMerge verifies that when auto_merge
// is false, CommitAndMerge commits the changes but does NOT merge into the
// target branch. The changes remain only on the task branch.
func TestCommitAndMerge_AutoMergeFalse_SkipsMerge(t *testing.T) {
	t.Parallel()
	te, repoDir, projectID, wtPath := setupCommitAndMergeTest(t)

	// feature.txt is unstaged on the task branch
	err := te.merge.CommitAndMerge(projectID, "test-task", wtPath, "Test commit", false)
	if err != nil {
		t.Fatalf("CommitAndMerge failed: %v", err)
	}

	// Verify the commit happened on the task branch
	cmd := exec.Command("git", "-C", wtPath, "log", "--oneline", "task/test-task")
	out, _ := cmd.Output()
	if !strings.Contains(string(out), "Test commit") {
		t.Error("commit should exist on task branch")
	}

	// Verify master does NOT have the commit (merge was skipped)
	cmd = exec.Command("git", "-C", repoDir, "log", "--oneline", "master")
	out, _ = cmd.Output()
	if strings.Contains(string(out), "Test commit") {
		t.Error("master should NOT contain the commit when auto_merge=false")
	}

	// Verify feature.txt exists on task branch but not on master
	cmd = exec.Command("git", "-C", repoDir, "show", "task/test-task:feature.txt")
	out, err = cmd.Output()
	if err != nil {
		t.Fatalf("feature.txt should exist on task branch: %v", err)
	}
	if !strings.Contains(string(out), "feature content") {
		t.Error("feature.txt should have feature content on task branch")
	}

	cmd = exec.Command("git", "-C", repoDir, "show", "master:feature.txt")
	_, err = cmd.Output()
	if err == nil {
		t.Error("feature.txt should NOT exist on master when auto_merge=false")
	}
}

// TestCommitAndMerge_AutoMergeTrue_MergesIntoTarget verifies that when auto_merge
// is true, CommitAndMerge commits AND merges the changes into the target branch.
func TestCommitAndMerge_AutoMergeTrue_MergesIntoTarget(t *testing.T) {
	t.Parallel()
	te, repoDir, projectID, wtPath := setupCommitAndMergeTest(t)

	err := te.merge.CommitAndMerge(projectID, "test-task", wtPath, "Test commit", true)
	if err != nil {
		t.Fatalf("CommitAndMerge failed: %v", err)
	}

	// Verify master HAS the commit (merge succeeded)
	cmd := exec.Command("git", "-C", repoDir, "log", "--oneline", "master")
	out, _ := cmd.Output()
	if !strings.Contains(string(out), "Test commit") {
		t.Errorf("master should contain the commit when auto_merge=true. Log: %s", string(out))
	}

	// Verify feature.txt exists on master
	cmd = exec.Command("git", "-C", repoDir, "show", "master:feature.txt")
	out, err = cmd.Output()
	if err != nil {
		t.Fatalf("feature.txt should exist on master after merge: %v", err)
	}
	if !strings.Contains(string(out), "feature content") {
		t.Error("feature.txt should have feature content on master after merge")
	}
}
