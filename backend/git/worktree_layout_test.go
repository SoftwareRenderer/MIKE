package git

// The worktree layout is derived here and nowhere else: <worktreesDir>/<projectID>/<taskID>, from
// worktreesRoot → projectWorktreesDir → WorktreePath. Removal takes the task's own directory, the
// project's removal takes the parent, and the sweeps that follow them are the only thing that clears a
// leftover — GarbageCollect runs at startup, so nothing else is self-healing.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"mike/backend/config"
	"mike/backend/store"
	"mike/backend/types"
)

// getBranches reads the undecorated ref list — `git branch --list` decorates, and anything compared
// against that output misses both markers.
func getBranches(t *testing.T, dir string) []string {
	t.Helper()
	out := runGitOutput(t, dir, "branch", "--format=%(refname:short)")
	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if b := strings.TrimSpace(line); b != "" {
			branches = append(branches, b)
		}
	}
	return branches
}

// worktreeRegistrations counts the project repo's .git/worktrees entries.
func worktreeRegistrations(t *testing.T, repoDir string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repoDir, ".git", "worktrees"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read worktree registrations: %v", err)
	}
	return len(entries)
}

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

	cfgStore := config.NewConfigStore(s, dbPath, "test", nil, false, false)
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

// A task's own removal takes only its directory, so only this removal takes the project's directory
// itself — which is what clears a directory a failed per-task removal left behind.
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

// The registration in .git/worktrees, not the directory, is what makes a branch undeletable, so the
// removal goes directory → prune → branch and no other order deletes anything.
func TestRemoveWorktreeClearsDirectoryRegistrationAndBranch(t *testing.T) {
	repoDir, taskID, _, _, wm := setupTestRepo(t)
	branch := "task/" + taskID
	wtPath, _, err := wm.CreateWorktree("test-project", taskID, branch, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	// Work that is committed but unmerged, and work that is not committed at all.
	writeFile(t, wtPath, "work.txt", "unmerged commit\n")
	runGit(t, wtPath, "add", "work.txt")
	runGit(t, wtPath, "commit", "-m", "unmerged work")
	writeFile(t, wtPath, "scratch.txt", "never committed\n")

	if err := wm.RemoveWorktree("test-project", taskID, branch); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}

	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree directory still there: %v", err)
	}
	if regs := worktreeRegistrations(t, repoDir); regs != 0 {
		t.Errorf("%d worktree registration(s) left in .git/worktrees after removal", regs)
	}
	if slices.Contains(getBranches(t, repoDir), branch) {
		t.Errorf("branch %s survived RemoveWorktree - it is unreachable from the board but not from git", branch)
	}
}

// PruneOrphanedBranches prunes first: the branches it is looking for are the ones a worktree removed
// moments ago still holds, and `git branch -D` refuses one a registration names.
func TestPruneOrphanedBranchesSweepsBranchHeldByStaleRegistration(t *testing.T) {
	repoDir, taskID, s, _, wm := setupTestRepo(t)
	branch := "task/" + taskID
	wtPath, _, err := wm.CreateWorktree("test-project", taskID, branch, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	// Directory gone, registration and branch still there: a stale registration.
	if err := os.RemoveAll(wtPath); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTaskByID(taskID); err != nil {
		t.Fatalf("fixture task: %v", err)
	}
	if err := s.DeleteTask("test-project", taskID); err != nil {
		t.Fatalf("delete task row: %v", err)
	}
	if listed := runGitOutput(t, repoDir, "branch", "--list", "task/*"); !strings.Contains(listed, "+ "+branch) {
		t.Fatalf("git does not decorate the branch with '+' here, so this test measures nothing: %q", listed)
	}

	if removed := wm.PruneOrphanedBranches("test-project"); removed != 1 {
		t.Errorf("PruneOrphanedBranches removed %d branches, want 1", removed)
	}
	if slices.Contains(getBranches(t, repoDir), branch) {
		t.Errorf("orphaned branch %s survived the sweep", branch)
	}
	if regs := worktreeRegistrations(t, repoDir); regs != 0 {
		t.Errorf("%d worktree registration(s) left after the sweep", regs)
	}
}

// A row that exists, and a worktree that holds the branch, both keep it out of the sweep.
func TestPruneOrphanedBranchesKeepsBranchOfLiveTask(t *testing.T) {
	repoDir, taskID, _, _, wm := setupTestRepo(t)
	branch := "task/" + taskID
	if _, _, err := wm.CreateWorktree("test-project", taskID, branch, ""); err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	if removed := wm.PruneOrphanedBranches("test-project"); removed != 0 {
		t.Errorf("PruneOrphanedBranches removed %d branches, want 0 - the task row is alive", removed)
	}
	if !slices.Contains(getBranches(t, repoDir), branch) {
		t.Errorf("the sweep deleted branch %s of a live task", branch)
	}
}

// The sweep reads refs/heads/<prefix>* with for-each-ref and the glob is one path segment wide, so it
// catches the branches both task forms and the thread form write, and nothing a person nested by hand.
func TestPruneOrphanedBranchesSweepsOrphanedTaskAndThreadBranches(t *testing.T) {
	t.Run("task branches", func(t *testing.T) {
		repoDir, taskID, s, _, wm := setupTestRepo(t)

		runGit(t, repoDir, "branch", "task/orphan-one-branch")
		runGit(t, repoDir, "branch", "task/orphan-two-branch")
		runGit(t, repoDir, "branch", "task/"+taskID)

		proj, err := s.GetProject("test-project")
		if err != nil {
			t.Fatalf("get project: %v", err)
		}
		if removed := wm.PruneOrphanedBranches(proj.ID); removed != 2 {
			t.Fatalf("expected 2 branches removed, got %d", removed)
		}

		branches := getBranches(t, repoDir)
		for _, gone := range []string{"task/orphan-one-branch", "task/orphan-two-branch"} {
			if slices.Contains(branches, gone) {
				t.Errorf("%s should be pruned", gone)
			}
		}
		if !slices.Contains(branches, "task/"+taskID) {
			t.Error("active task branch should be kept")
		}
	})

	t.Run("thread branches", func(t *testing.T) {
		repoDir, taskID, s, _, wm := setupTestRepo(t)

		runGit(t, repoDir, "branch", "thread/orphan-thread-1")
		runGit(t, repoDir, "branch", "thread/orphan-thread-2")

		activeThreadID := "active-thread-xyz"
		if err := s.CreateThread(&types.TaskThread{
			ID: activeThreadID, TaskID: taskID, Name: "Active Fork", IsActive: true,
			GitBranch: "thread/" + activeThreadID, CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("create thread: %v", err)
		}
		runGit(t, repoDir, "branch", "thread/"+activeThreadID)

		proj, err := s.GetProject("test-project")
		if err != nil {
			t.Fatalf("get project: %v", err)
		}
		if removed := wm.PruneOrphanedBranches(proj.ID); removed != 2 {
			t.Fatalf("expected 2 branches removed, got %d", removed)
		}

		branches := getBranches(t, repoDir)
		for _, gone := range []string{"thread/orphan-thread-1", "thread/orphan-thread-2"} {
			if slices.Contains(branches, gone) {
				t.Errorf("%s should be pruned", gone)
			}
		}
		if !slices.Contains(branches, "thread/"+activeThreadID) {
			t.Error("active thread branch should be kept")
		}
	})
}

// GarbageCollect runs at startup only: a thread row the CASCADE deleted leaves its branch behind until
// this sweeps it, and a live thread's branch must survive the same pass.
func TestGarbageCollectPrunesThreadBranchesWithoutRowsAndKeepsLiveOnes(t *testing.T) {
	repoDir, taskID, s, _, wm := setupTestRepo(t)

	goneThreadID, liveThreadID := "test-thread-abc", "test-thread-xyz"
	for _, id := range []string{goneThreadID, liveThreadID} {
		if err := s.CreateThread(&types.TaskThread{
			ID: id, TaskID: taskID, Name: "Fork " + id, IsActive: true,
			GitBranch: "thread/" + id, CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("create thread %s: %v", id, err)
		}
		runGit(t, repoDir, "branch", "thread/"+id)
	}
	branches := getBranches(t, repoDir)
	for _, id := range []string{goneThreadID, liveThreadID} {
		if !slices.Contains(branches, "thread/"+id) {
			t.Fatalf("fixture: thread/%s should exist before GC", id)
		}
	}

	// The deletion path that lost its branch: the row is gone, the branch is not.
	if err := s.DeleteThread(goneThreadID); err != nil {
		t.Fatalf("delete thread: %v", err)
	}

	if _, err := wm.GarbageCollect(false); err != nil {
		t.Fatalf("GarbageCollect: %v", err)
	}

	branches = getBranches(t, repoDir)
	if slices.Contains(branches, "thread/"+goneThreadID) {
		t.Error("orphaned thread branch should be pruned by GC")
	}
	if !slices.Contains(branches, "thread/"+liveThreadID) {
		t.Error("active thread branch should be kept by GC")
	}
}

// The Main branch of a titled task is named by the model's title, not by the task ID, and the task's
// current branch may be a fork. A sweep that parses a different shape than the executor writes deletes
// a live branch.
func TestGarbageCollect_KeepsTitledTaskSlugMainBranch(t *testing.T) {
	repoDir, taskID, s, _, wm := setupTestRepo(t)

	// Simulate a fork being the task's active branch: task.Branch points to the fork
	// thread branch, and the task has a title whose slug names its Main branch.
	task, err := s.GetTask("test-project", taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	task.Title = "Hey" // slugifies to "hey"
	task.Branch = "thread/some-fork"
	if err := s.UpdateTask(task); err != nil {
		t.Fatalf("update task: %v", err)
	}

	// The titled task's Main branch is slug-named (task/hey-<random6>).
	runGit(t, repoDir, "branch", "task/hey-zimxpq")
	// An unrelated orphaned branch must still be removed.
	runGit(t, repoDir, "branch", "task/orphan-one-branch")

	_, err = wm.GarbageCollect(false)
	if err != nil {
		t.Fatalf("GarbageCollect: %v", err)
	}

	branches := getBranches(t, repoDir)
	if !slices.Contains(branches, "task/hey-zimxpq") {
		t.Error("titled task's slug Main branch should be kept by GC when a fork is active")
	}
	if slices.Contains(branches, "task/orphan-one-branch") {
		t.Error("unrelated orphaned task branch should be removed by GC")
	}
}

// A stash entry labelled for a task that no longer exists is work nobody will restore; one labelled for
// a task that does is that task's own.
func TestGarbageCollect_DropsStaleSyncMergeAndAutoMergeStashes(t *testing.T) {
	repoDir, taskID, _, _, wm := setupTestRepo(t)

	// Create a stale sync-merge stash for a task that does NOT exist in DB
	staleTaskID := "stale-deleted-task"
	writeFile(t, repoDir, "changed.txt", "stale sync\n")
	runGit(t, repoDir, "add", "changed.txt")
	runGit(t, repoDir, "stash", "push", "-u", "-m", StashMessageForSyncMerge(staleTaskID))

	writeFile(t, repoDir, "changed.txt", "stale auto\n")
	runGit(t, repoDir, "add", "changed.txt")
	runGit(t, repoDir, "stash", "push", "-u", "-m", StashMessageForAutoMerge(staleTaskID))

	// Create an active sync-merge stash for the task that DOES exist in DB
	writeFile(t, repoDir, "changed.txt", "active sync\n")
	runGit(t, repoDir, "add", "changed.txt")
	runGit(t, repoDir, "stash", "push", "-u", "-m", StashMessageForSyncMerge(taskID))

	// Verify all stashes exist
	listOut := runGitOutput(t, repoDir, "stash", "list", "--format=%s")
	if !strings.Contains(listOut, "mike-sync-merge-"+staleTaskID) {
		t.Errorf("expected stale sync-merge stash, got:\n%s", listOut)
	}
	if !strings.Contains(listOut, "mike-auto-merge-"+staleTaskID) {
		t.Errorf("expected stale auto-merge stash, got:\n%s", listOut)
	}
	if !strings.Contains(listOut, "mike-sync-merge-"+taskID) {
		t.Errorf("expected active sync-merge stash, got:\n%s", listOut)
	}

	// Run GC
	if _, err := wm.GarbageCollect(false); err != nil {
		t.Fatalf("GarbageCollect: %v", err)
	}

	// Verify stale stashes are gone but active stash remains
	listOut = runGitOutput(t, repoDir, "stash", "list", "--format=%s")
	if strings.Contains(listOut, staleTaskID) {
		t.Errorf("stale stashes for %s should be gone, got:\n%s", staleTaskID, listOut)
	}
	if !strings.Contains(listOut, "mike-sync-merge-"+taskID) {
		t.Errorf("active sync-merge stash for %s should remain, got:\n%s", taskID, listOut)
	}
}
