package stream

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"mike/backend/config"
	"mike/backend/git"
	"mike/backend/store"
	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

// TestHandleEventMapsGitIndexEventToTask: index-dir events live outside the worktree and must still
// map back to their task, or stage/unstage never triggers a diff broadcast.
func TestHandleEventMapsGitIndexEventToTask(t *testing.T) {
	gitDir := "/home/user/repo/.git/worktrees/task-abc"
	ww := &WorktreeWatcher{
		worktrees: map[string]worktreeInfo{
			"task-abc": {
				projectID:   "proj-1",
				path:        "/home/user/worktrees/proj-1/task-abc",
				gitIndexDir: gitDir,
			},
		},
		throttles:      make(map[string]*time.Timer),
		pathCache:      make(map[string]string),
		indexDirToTask: map[string]string{gitDir: "task-abc"},
	}

	tests := []struct {
		name  string
		event fsnotify.Event
		want  bool // true = event maps to a task and arms a diff throttle
	}{
		{"index file write", fsnotify.Event{Name: gitDir + "/index", Op: fsnotify.Write}, true},
		// index.lock is git's own write lock: every status/diff the watcher runs creates and removes it,
		// so reacting to it makes each diff schedule the next one, forever, on an idle worktree.
		{"index lock create skipped", fsnotify.Event{Name: gitDir + "/index.lock", Op: fsnotify.Create}, false},
		{"index lock remove skipped", fsnotify.Event{Name: gitDir + "/index.lock", Op: fsnotify.Remove}, false},
		{"other temp file in index dir", fsnotify.Event{Name: gitDir + "/index.tmp", Op: fsnotify.Create}, true},
		{"git metadata (HEAD) skipped", fsnotify.Event{Name: gitDir + "/HEAD", Op: fsnotify.Write}, false},
		{"unrelated path skipped", fsnotify.Event{Name: "/home/user/other/index", Op: fsnotify.Write}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ww.throttles = make(map[string]*time.Timer)
			ww.handleEvent(tc.event)
			timer, armed := ww.throttles["task-abc"]
			if armed {
				timer.Stop() // prevent the throttle from firing after the test
			}
			if armed != tc.want {
				t.Errorf("handleEvent(%q) armed throttle=%v, want %v", tc.event.Name, armed, tc.want)
			}
		})
	}
}

// TestWorktreeWatcherStageBroadcast: stage/unstage trigger a diff broadcast.
func TestWorktreeWatcherStageBroadcast(t *testing.T) {
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	worktreesDir := filepath.Join(tmpDir, "worktrees")
	os.MkdirAll(repoDir, 0o755)
	os.MkdirAll(worktreesDir, 0o755)

	testgit.Git(t, repoDir, "init")
	testgit.Git(t, repoDir, "config", "user.email", "test@test.com")
	testgit.Git(t, repoDir, "config", "user.name", "Test")
	testgit.WriteFile(t, repoDir, "README.md", "# Test\n")
	testgit.Git(t, repoDir, "add", "README.md")
	testgit.Git(t, repoDir, "commit", "-m", "Initial")

	dbPath := filepath.Join(tmpDir, "test.db")
	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer s.Close()

	cfgStore := config.NewConfigStore(s, dbPath, "test", nil, false, false)
	cfgStore.SetWorkspaceDirOverride(worktreesDir)
	cfg, err := cfgStore.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if err := cfgStore.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	projID := "test-project"
	proj := &types.Project{ID: projID, Name: "test", Path: repoDir}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	taskID := "test-task"
	branchName := "kanban/" + taskID
	task := &types.Task{
		ID:             taskID,
		ProjectID:      projID,
		Title:          "Test task",
		Prompt:         "Test prompt",
		Status:         types.StatusInProgress,
		ExecutionState: types.ExecStateRunning,
		Branch:         branchName,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	wm := git.NewWorktreeManager(s, cfgStore)
	worktreePath, _, err := wm.CreateWorktree(projID, taskID, branchName, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	broadcasts := make(chan DiffStatsEvent, 10)
	ww, err := NewWorktreeWatcher(wm, func(event DiffStatsEvent) {
		broadcasts <- event
	}, nil, nil)
	if err != nil {
		t.Skipf("skip (inotify limit): %v", err)
	}
	go ww.StartEventLoop()
	defer ww.Close()

	if err := ww.Watch(taskID, projID); err != nil {
		t.Fatalf("watch task: %v", err)
	}

	// Drain the initial broadcast after Watch (task may have existing changes).
	drainBroadcasts(broadcasts)

	// Modify a tracked file in the worktree.
	testgit.WriteFile(t, worktreePath, "README.md", "# Test\nchanged\n")
	waitForBroadcast(t, broadcasts, "file modification")

	// Stage the file: git add. Expect a broadcast with README.md in StagedPaths.
	cmd := exec.Command("git", "-C", worktreePath, "add", "--", "README.md")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v: %s", err, out)
	}
	ev := waitForBroadcast(t, broadcasts, "staging")
	if !slices.Contains(ev.StagedPaths, "README.md") {
		t.Fatalf("after stage: expected README.md in StagedPaths, got %v", ev.StagedPaths)
	}

	// Unstage the file: git reset HEAD. Expect a broadcast with README.md removed.
	cmd = exec.Command("git", "-C", worktreePath, "reset", "HEAD", "--", "README.md")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git reset failed: %v: %s", err, out)
	}
	ev = waitForBroadcast(t, broadcasts, "unstaging")
	if slices.Contains(ev.StagedPaths, "README.md") {
		t.Fatalf("after unstage: expected README.md NOT in StagedPaths, got %v", ev.StagedPaths)
	}
}

func waitForBroadcast(t *testing.T, ch chan DiffStatsEvent, what string) DiffStatsEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatalf("no broadcast after %s", what)
		return DiffStatsEvent{}
	}
}

func drainBroadcasts(ch chan DiffStatsEvent) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
