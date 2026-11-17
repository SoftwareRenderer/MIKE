package stream

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mike/backend/git"
	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

// TestWorktreeWatcherRetriesWhileInFlight verifies that a pending diff is not
// dropped when the throttle fires while a compute is already in flight. The
// watcher must re-arm a retry timer instead of discarding the change, otherwise
// the diff panel goes stale until the next fsnotify event or a page refresh.
func TestWorktreeWatcherRetriesWhileInFlight(t *testing.T) {
	projID := "test-project"
	f := newWatcherFixture(t, projID, "test")

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
	if err := f.Store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	wm := git.NewWorktreeManager(f.Store, f.Config)
	if _, _, err := wm.CreateWorktree(projID, taskID, branchName, ""); err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	ww, err := NewWorktreeWatcher(wm, func(event DiffStatsEvent) {}, nil, nil)
	if err != nil {
		t.Skipf("skip retry test (inotify limit): %v", err)
	}
	go ww.StartEventLoop()
	defer ww.Close()

	if err := ww.Watch(taskID, projID); err != nil {
		t.Fatalf("watch task: %v", err)
	}

	// Simulate a diff compute already in flight for the task.
	ww.mu.Lock()
	ww.inFlight[taskID] = true
	ww.mu.Unlock()

	// Trigger a filesystem change -> handleEvent arms the 1s throttle timer.
	worktreePath := filepath.Join(f.WorktreesDir, projID, taskID)
	testgit.WriteFile(t, worktreePath, "new.txt", "hello\n")

	// Wait long enough for the 1s throttle to fire (while still in flight).
	time.Sleep(1400 * time.Millisecond)

	// A retry timer must have been re-armed, proving the pending change was
	// preserved rather than dropped. With the old code throttles[taskID] would
	// be nil here (the pending change silently lost).
	ww.mu.Lock()
	retryArmed := ww.throttles[taskID] != nil
	ww.mu.Unlock()
	if !retryArmed {
		t.Fatal("pending diff was dropped: no retry timer re-armed while in flight")
	}
}

// TestResolveGitIndexDir pins the only two answers this function may return: the gitdir
// path for a worktree's .git POINTER FILE (so the watcher resolves the index the worktree
// actually uses), and "" for everything that is not such a pointer — a real .git
// directory, no .git at all, or a .git file with no gitdir line.
func TestResolveGitIndexDir(t *testing.T) {
	const worktreeGitdir = "/home/user/projects/myrepo/.git/worktrees/abc-123-def"

	cases := []struct {
		name string
		// write lays out dir/.git before resolving; nil leaves the directory empty.
		write func(t *testing.T, dir string)
		want  string
	}{
		{
			name:  "worktree pointer file",
			write: gitFileWriter("gitdir: " + worktreeGitdir + "\n"),
			want:  worktreeGitdir,
		},
		{
			name:  "gitdir line surrounded by other lines",
			write: gitFileWriter("# comment\ngitdir: /some/path/.git/worktrees/task-xyz\n# another comment\n"),
			want:  "/some/path/.git/worktrees/task-xyz",
		},
		{
			name:  "regular repository: .git is a directory",
			write: mkdirGit,
			want:  "",
		},
		{
			name:  "git file with no gitdir line",
			write: gitFileWriter("some random content\n"),
			want:  "",
		},
		{
			name:  "no .git at all",
			write: nil,
			want:  "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.write != nil {
				tc.write(t, dir)
			}
			if got := resolveGitIndexDir(dir); got != tc.want {
				t.Errorf("resolveGitIndexDir() = %q, want %q", got, tc.want)
			}
		})
	}
}

// gitFileWriter returns a fixture that writes content into dir/.git (a file, as git
// leaves it in a linked worktree).
func gitFileWriter(content string) func(t *testing.T, dir string) {
	return func(t *testing.T, dir string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// mkdirGit lays out a regular repository's .git directory.
func mkdirGit(t *testing.T, dir string) {
	t.Helper()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// watchedWorktreesEnv is one project with n tasks, each holding its own worktree: the shape the
// watcher has to route between.
type watchedWorktreesEnv struct {
	wm        *git.WorktreeManager
	projectID string
	taskIDs   []string
}

func newWatchedTaskWorktrees(t *testing.T, n int) *watchedWorktreesEnv {
	t.Helper()

	const projectID = "routing-project"
	f := newWatcherFixture(t, projectID, "routing")

	env := &watchedWorktreesEnv{projectID: projectID}
	for i := range n {
		taskID := fmt.Sprintf("routing-task-%d", i)
		if err := f.Store.CreateTask(&types.Task{
			ID: taskID, ProjectID: projectID, Title: taskID, Status: types.StatusInProgress,
			Branch: "task/" + taskID,
		}); err != nil {
			t.Fatalf("create task %s: %v", taskID, err)
		}
		env.taskIDs = append(env.taskIDs, taskID)
	}
	// One manager for every task: the watcher asks it for each worktree's path and diff.
	env.wm = git.NewWorktreeManager(f.Store, f.Config)
	for _, taskID := range env.taskIDs {
		if _, _, err := env.wm.CreateWorktree(projectID, taskID, "task/"+taskID, ""); err != nil {
			t.Fatalf("create worktree %s: %v", taskID, err)
		}
	}
	return env
}

func (e *watchedWorktreesEnv) worktreePath(t *testing.T, taskID string) string {
	t.Helper()
	path, err := e.wm.WorktreePath(taskID)
	if err != nil {
		t.Fatalf("worktree path for %s: %v", taskID, err)
	}
	return path
}

// TestWorktreeWatcherRoutesEachWorktreeToItsOwnTask: every watched worktree shares one fsnotify
// watcher, one path cache and one index-dir map, so a change inside one is computed and broadcast
// for that task alone — and Unwatch leaves nothing that could answer for a task this watcher no
// longer holds.
func TestWorktreeWatcherRoutesEachWorktreeToItsOwnTask(t *testing.T) {
	env := newWatchedTaskWorktrees(t, 3)

	broadcasts := make(chan DiffStatsEvent, 32)
	ww, err := NewWorktreeWatcher(env.wm, func(ev DiffStatsEvent) { broadcasts <- ev }, nil, nil)
	if err != nil {
		t.Skipf("skip (inotify limit): %v", err)
	}
	go ww.StartEventLoop()
	t.Cleanup(ww.Close)
	for _, taskID := range env.taskIDs {
		if err := ww.Watch(taskID, env.projectID); err != nil {
			t.Fatalf("watch task %s: %v", taskID, err)
		}
	}
	drainBroadcasts(broadcasts)

	watched := map[string]bool{}
	for _, taskID := range env.taskIDs {
		watched[taskID] = true
		testgit.WriteFile(t, env.worktreePath(t, taskID), "change.txt", "written by the test\n")
	}

	// One write per task, and each task is throttled on a timer of its own, so exactly one compute
	// is owed to each of them — and the frame it publishes names the worktree that changed.
	seen := map[string]int{}
	for len(seen) < len(env.taskIDs) {
		select {
		case ev := <-broadcasts:
			if !watched[ev.TaskID] {
				t.Fatalf("broadcast named task %q, which this watcher was never asked to watch (%v)", ev.TaskID, env.taskIDs)
			}
			seen[ev.TaskID]++
		case <-time.After(10 * time.Second):
			t.Fatalf("only %v of the %d watched tasks were broadcast after 10s, and each of them holds a written file",
				seen, len(env.taskIDs))
		}
	}
	for _, taskID := range env.taskIDs {
		if seen[taskID] != 1 {
			t.Errorf("task %s was broadcast %d times for one change, want once", taskID, seen[taskID])
		}
	}

	for _, taskID := range env.taskIDs {
		ww.Unwatch(taskID)
	}
	ww.mu.Lock()
	paths, indexDirs := len(ww.pathCache), len(ww.indexDirToTask)
	ww.mu.Unlock()
	if paths != 0 || indexDirs != 0 {
		t.Fatalf("Unwatch left %d path and %d index-dir entries for %d tasks", paths, indexDirs, len(env.taskIDs))
	}

	// Nothing is watched, so a change in a former worktree is nobody's diff.
	drainBroadcasts(broadcasts)
	testgit.WriteFile(t, env.worktreePath(t, env.taskIDs[0]), "after-unwatch.txt", "nobody is watching\n")
	select {
	case ev := <-broadcasts:
		t.Fatalf("an unwatched worktree still broadcast task %s's stats", ev.TaskID)
	case <-time.After(3 * time.Second):
	}
}
