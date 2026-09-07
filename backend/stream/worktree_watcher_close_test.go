package stream

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"kanban/backend/config"
	"kanban/backend/git"
	"kanban/backend/store"
	"kanban/backend/types"
)

// TestCloseDoesNotWaitHoldingTheLock: Close must not hold ww.mu across workerWg.Wait, because
// computeAndBroadcast takes that mutex in its deferred cleanup. The broadcast callback parks the
// worker there while Close runs, so a regression fails by deadlocking, not by timing luck.
func TestCloseDoesNotWaitHoldingTheLock(t *testing.T) {
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
	writeFile(t, repoDir, "README.md", "# Test\n")
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "Initial")

	dbPath := filepath.Join(tmpDir, "test.db")
	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer s.Close()

	cfgStore := config.NewConfigStore(s, dbPath, "test")
	cfgStore.SetWorkspaceDirOverride(worktreesDir)

	const projID, taskID = "close-proj", "close-task"
	if err := s.CreateProject(&types.Project{ID: projID, Name: "test", Path: repoDir}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(&types.Task{
		ID: taskID, ProjectID: projID, Title: "close test", Status: types.StatusInProgress,
		Branch: "kanban/" + taskID,
	}); err != nil {
		t.Fatal(err)
	}

	wm := git.NewWorktreeManager(s, cfgStore)
	worktreePath, _, err := wm.CreateWorktree(projID, taskID, "kanban/"+taskID, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	inCompute := make(chan struct{}, 1)
	release := make(chan struct{})
	ww, err := NewWorktreeWatcher(wm, func(DiffStatsEvent) {
		select {
		case inCompute <- struct{}{}:
		default:
		}
		<-release // park the worker inside computeAndBroadcast
	}, nil, nil)
	if err != nil {
		t.Skipf("skip (inotify limit): %v", err)
	}
	// No StartEventLoop: handleEvent is the same path an fsnotify event takes, and it keeps
	// the test free of a watcher thread.
	if err := ww.Watch(taskID, projID, ""); err != nil {
		t.Fatalf("watch task: %v", err)
	}

	writeFile(t, worktreePath, "README.md", "# Test\nchanged\n")
	ww.handleEvent(fsnotify.Event{Op: fsnotify.Write, Name: filepath.Join(worktreePath, "README.md")})

	select {
	case <-inCompute:
	case <-time.After(10 * time.Second):
		close(release)
		ww.Close()
		t.Fatal("no diff compute reached the worker — the test would prove nothing")
	}
	closed := make(chan struct{})
	go func() {
		ww.Close()
		close(closed)
	}()
	// Releasing the worker makes it need ww.mu for its deferred cleanup. Holding the lock across
	// Wait deadlocks; releasing it cannot, so the failure direction is not a race.
	go func() {
		time.Sleep(300 * time.Millisecond)
		close(release)
	}()

	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Close deadlocked: it waited for the worker while holding the mutex the worker needs")
	}
}
