package stream

// A file the person deletes is a diff change like any other. The panel holds a row per file, so a watcher
// that reports only what appeared keeps offering a file that is gone.

import (
	"os"
	"path/filepath"
	"testing"

	"mike/backend/config"
	"mike/backend/git"
	"mike/backend/store"
	"mike/backend/types"
)

// newUntrackedWatcherEnv is a watched worktree with nothing in it but its own commit: the only diff change
// is the file the test adds and takes away.
func newUntrackedWatcherEnv(t *testing.T) (*WorktreeWatcher, chan DiffStatsEvent, string, string, string) {
	t.Helper()

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	worktreesDir := filepath.Join(tmpDir, "worktrees")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")
	writeTestFile(t, repoDir, "README.md", "# Test\n")
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "Initial")

	dbPath := filepath.Join(tmpDir, "test.db")
	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	cfgStore := config.NewConfigStore(s, dbPath, "test", nil, false, false)
	cfgStore.SetWorkspaceDirOverride(worktreesDir)
	cfg, err := cfgStore.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfgStore.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}

	const projectID, taskID = "untracked-project", "untracked-task"
	if err := s.CreateProject(&types.Project{ID: projectID, Name: "untracked", Path: repoDir}); err != nil {
		t.Fatal(err)
	}
	taskBranch := "task/" + taskID
	if err := s.CreateTask(&types.Task{
		ID: taskID, ProjectID: projectID, Title: "untracked", Status: types.StatusInProgress, Branch: taskBranch,
	}); err != nil {
		t.Fatal(err)
	}
	wm := git.NewWorktreeManager(s, cfgStore)
	worktreePath, _, err := wm.CreateWorktree(projectID, taskID, taskBranch, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	broadcasts := make(chan DiffStatsEvent, 16)
	ww, err := NewWorktreeWatcher(wm, func(event DiffStatsEvent) { broadcasts <- event }, nil, nil)
	if err != nil {
		t.Skipf("skip (inotify limit): %v", err)
	}
	go ww.StartEventLoop()
	t.Cleanup(ww.Close)
	if err := ww.Watch(taskID, projectID); err != nil {
		t.Fatalf("watch task: %v", err)
	}
	drainBroadcasts(broadcasts)
	return ww, broadcasts, taskID, projectID, worktreePath
}

// TestWatcherReportsADeletedUntrackedFile: the untracked file that disappears is the one case where the
// worktree ends up with no diff at all, so the frame that carries it is the one computed for a clean tree.
func TestWatcherReportsADeletedUntrackedFile(t *testing.T) {
	_, broadcasts, taskID, _, worktreePath := newUntrackedWatcherEnv(t)
	name := "delete-me.txt"
	path := filepath.Join(worktreePath, name)

	writeTestFile(t, worktreePath, name, "content to delete\n")
	appeared := waitForBroadcast(t, broadcasts, "creating an untracked file")
	if appeared.UntrackedN != 1 {
		t.Fatalf("after create: untracked_n = %d, want 1 (task %s)", appeared.UntrackedN, taskID)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove the file: %v", err)
	}
	gone := waitForBroadcast(t, broadcasts, "deleting the untracked file")
	if !containsPath(gone.RemovedFiles, name) {
		t.Fatalf("after delete: removed_files = %v, want %s among them", gone.RemovedFiles, name)
	}
	if gone.UntrackedN != 0 {
		t.Errorf("after delete: untracked_n = %d, want 0 — the count still charges the deleted file", gone.UntrackedN)
	}
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}
