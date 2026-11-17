package exec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/git"
	"mike/backend/testutil/testgit"
	"mike/backend/testutil/teststore"
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
	testgit.Git(t, repoDir, "init")
	testgit.Git(t, repoDir, "config", "user.email", "test@test.com")
	testgit.Git(t, repoDir, "config", "user.name", "Test")
	os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# Test\n"), 0o644)
	testgit.Git(t, repoDir, "add", "README.md")
	testgit.Git(t, repoDir, "commit", "-m", "Initial")

	// Create store and config
	dbPath := filepath.Join(tmpDir, "test.db")
	s := teststore.StoreIn(t, dbPath)

	cfgStore := teststore.ConfigStore(t, s, dbPath)
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
	testgit.Git(t, repoDir, "checkout", "-b", "task/test-task")
	testgit.Git(t, repoDir, "checkout", "master")

	// Create worktree
	wtPath := filepath.Join(worktreesDir, projectID, taskID)
	os.MkdirAll(wtPath, 0o755)
	testgit.Git(t, repoDir, "worktree", "add", wtPath, "task/test-task")

	// Create an unstaged change in the worktree (simulating LLM edits)
	os.WriteFile(filepath.Join(wtPath, "feature.txt"), []byte("feature content\n"), 0o644)
	testgit.Git(t, wtPath, "add", "feature.txt")

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

// TestCommitAndMerge_LandsWhereAutoMergeSays: the flag is the only thing that decides whether the
// commit also reaches the target branch. Committed stays local when it is off; on, the same commit
// reaches master.
func TestCommitAndMerge_LandsWhereAutoMergeSays(t *testing.T) {
	t.Parallel()
	for _, autoMerge := range []bool{false, true} {
		name := "auto_merge_off_stays_on_the_task_branch"
		if autoMerge {
			name = "auto_merge_on_reaches_the_target_branch"
		}
		t.Run(name, func(t *testing.T) {
			te, repoDir, projectID, wtPath := setupCommitAndMergeTest(t)

			if err := te.merge.CommitAndMerge(projectID, "test-task", wtPath, "Test commit", autoMerge); err != nil {
				t.Fatalf("CommitAndMerge failed: %v", err)
			}

			// The commit is always on the task branch, merged or not.
			if out := runGitCmd(wtPath, "log", "--oneline", "task/test-task"); !strings.Contains(out.output, "Test commit") {
				t.Errorf("the commit is missing from the task branch:\n%s", out.output)
			}
			taskFile := runGitCmd(repoDir, "show", "task/test-task:feature.txt")
			if taskFile.exitCode != 0 || !strings.Contains(taskFile.output, "feature content") {
				t.Errorf("feature.txt on the task branch: %q (exit %d)", taskFile.output, taskFile.exitCode)
			}

			// The point of the flag: whether the work reached the target branch.
			masterHasIt := strings.Contains(runGitCmd(repoDir, "log", "--oneline", "master").output, "Test commit")
			masterFile := runGitCmd(repoDir, "show", "master:feature.txt")
			delivered := masterFile.exitCode == 0 && strings.Contains(masterFile.output, "feature content")
			if masterHasIt != autoMerge || delivered != autoMerge {
				t.Errorf("auto_merge=%v: master has the commit = %v, holds feature.txt = %v (show: %s)",
					autoMerge, masterHasIt, delivered, masterFile.output)
			}
		})
	}
}
