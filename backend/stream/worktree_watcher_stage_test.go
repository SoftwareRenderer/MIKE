package stream

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"kanban/backend/config"
	"kanban/backend/git"
	"kanban/backend/store"
	"kanban/backend/types"
)

// TestHandleEventMapsGitIndexEventToTask verifies index-dir events (which live
// outside the worktree) map back to their task — a regression where they were
// dropped, so stage/unstage never triggered a diff broadcast.
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
		{"index lock temp file", fsnotify.Event{Name: gitDir + "/index.lock", Op: fsnotify.Create}, true},
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

// TestWorktreeWatcherStageBroadcast verifies stage/unstage trigger a diff
// broadcast (index events were previously dropped, so no broadcast was sent).
func TestWorktreeWatcherStageBroadcast(t *testing.T) {
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

	cfgStore := config.NewConfigStore(s, dbPath, "test")
	cfg, err := cfgStore.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	_ = s.SetConfigValue("workspace_dir", worktreesDir)
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
		Status:         "in_progress",
		ExecutionState: "running",
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

	if err := ww.Watch(taskID, projID, ""); err != nil {
		t.Fatalf("watch task: %v", err)
	}

	// Drain the initial broadcast after Watch (task may have existing changes).
	drainBroadcasts(broadcasts)

	// Modify a tracked file in the worktree.
	writeFile(t, worktreePath, "README.md", "# Test\nchanged\n")
	waitForBroadcast(t, broadcasts, "file modification")

	// Stage the file: git add. Expect a broadcast with README.md in StagedPaths.
	cmd := exec.Command("git", "-C", worktreePath, "add", "--", "README.md")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v: %s", err, out)
	}
	ev := waitForBroadcast(t, broadcasts, "staging")
	if !containsPath(ev.StagedPaths, "README.md") {
		t.Fatalf("after stage: expected README.md in StagedPaths, got %v", ev.StagedPaths)
	}

	// Unstage the file: git reset HEAD. Expect a broadcast with README.md removed.
	cmd = exec.Command("git", "-C", worktreePath, "reset", "HEAD", "--", "README.md")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git reset failed: %v: %s", err, out)
	}
	ev = waitForBroadcast(t, broadcasts, "unstaging")
	if containsPath(ev.StagedPaths, "README.md") {
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

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}
