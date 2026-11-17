package exec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestCommitAndMerge_CarriesTheProjectsOwnIdentity: the pair a person set under Settings → project is
// the pair the commit carries, committer included, and a half the row leaves blank stays the
// repository's own answer.
func TestCommitAndMerge_CarriesTheProjectsOwnIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		rowName       string
		rowEmail      string
		wantAuthor    string
		wantCommitter string
	}{
		{
			"the pair the row names",
			"Ada Lovelace", "ada@example.com",
			"Ada Lovelace <ada@example.com>", "Ada Lovelace <ada@example.com>",
		},
		{
			"a row naming only the name leaves the email to the repository",
			"Ada Lovelace", "",
			"Ada Lovelace <test@test.com>", "Ada Lovelace <test@test.com>",
		},
		{
			"a row naming nothing leaves both to the repository",
			"", "",
			"Test <test@test.com>", "Test <test@test.com>",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			te, _, projectID, wtPath := setupCommitAndMergeTest(t)
			if err := te.store.UpdateProjectGitAuthorName(projectID, tc.rowName); err != nil {
				t.Fatalf("set the author name: %v", err)
			}
			if err := te.store.UpdateProjectGitAuthorEmail(projectID, tc.rowEmail); err != nil {
				t.Fatalf("set the author email: %v", err)
			}

			if err := te.merge.CommitAndMerge(projectID, "test-task", wtPath, "Identified commit", false); err != nil {
				t.Fatalf("CommitAndMerge failed: %v", err)
			}

			got := strings.TrimSpace(runGitCmd(wtPath, "log", "-1", "--format=%an <%ae>|%cn <%ce>").output)
			want := tc.wantAuthor + "|" + tc.wantCommitter
			if got != want {
				t.Errorf("the commit reads %s, want %s", got, want)
			}
		})
	}
}

// TestCommitAndMerge_TwoProjectsCommitAsThemselves: two projects over one repository, each naming a
// different person, committing in one store. A process-wide identity that one commit left behind would
// land on the other, which is exactly what the per-project row exists to prevent.
func TestCommitAndMerge_TwoProjectsCommitAsThemselves(t *testing.T) {
	t.Parallel()
	te, _, projectID, wtPath := setupCommitAndMergeTest(t)
	if err := te.store.UpdateProjectGitAuthorName(projectID, "Ada Lovelace"); err != nil {
		t.Fatalf("set the first name: %v", err)
	}
	if err := te.store.UpdateProjectGitAuthorEmail(projectID, "ada@example.com"); err != nil {
		t.Fatalf("set the first email: %v", err)
	}

	// The second project is a repository of its own — projects.path is UNIQUE — naming someone else,
	// and its git config names a third person nobody should see in the log.
	const otherID = "other-project"
	otherDir := t.TempDir()
	testgit.Git(t, otherDir, "init")
	testgit.Git(t, otherDir, "config", "user.name", "Other Repo")
	testgit.Git(t, otherDir, "config", "user.email", "repo@other.example")
	os.WriteFile(filepath.Join(otherDir, "README.md"), []byte("# Other\n"), 0o644)
	testgit.Git(t, otherDir, "add", "README.md")
	testgit.Git(t, otherDir, "commit", "-m", "Initial")
	// The branch is made without being checked out: the main worktree holding it would refuse the
	// task worktree that wants it.
	testgit.Git(t, otherDir, "branch", "task/other-task")
	otherWT := filepath.Join(t.TempDir(), "other-task")
	if err := os.MkdirAll(otherWT, 0o755); err != nil {
		t.Fatalf("mkdir the second worktree: %v", err)
	}
	testgit.Git(t, otherDir, "worktree", "add", otherWT, "task/other-task")
	os.WriteFile(filepath.Join(otherWT, "other.txt"), []byte("other work\n"), 0o644)
	testgit.Git(t, otherWT, "add", "other.txt")
	if err := te.store.CreateProject(&types.Project{
		ID: otherID, Name: "other", Path: otherDir,
		GitAuthorName: "Grace Hopper", GitAuthorEmail: "grace@example.com",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create the second project: %v", err)
	}

	if err := te.merge.CommitAndMerge(projectID, "test-task", wtPath, "first commit", false); err != nil {
		t.Fatalf("commit for the first project: %v", err)
	}
	if err := te.merge.CommitAndMerge(otherID, "other-task", otherWT, "second commit", false); err != nil {
		t.Fatalf("commit for the second project: %v", err)
	}

	for _, tc := range []struct {
		where, subject, want string
	}{
		{wtPath, "first commit", "Ada Lovelace <ada@example.com>"},
		{otherWT, "second commit", "Grace Hopper <grace@example.com>"},
	} {
		if got := strings.TrimSpace(runGitCmd(tc.where, "log", "-1", "--format=%an <%ae>").output); got != tc.want {
			t.Errorf("%s: %q was authored by %q, want %q", tc.where, tc.subject, got, tc.want)
		}
	}
}
