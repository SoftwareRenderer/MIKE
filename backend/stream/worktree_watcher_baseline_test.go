package stream

// The watcher resolves a task's diff baseline per compute, so a thread forked mid-run cannot leave the card's
// stats and the panel's diff computed against different commits.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/config"
	"mike/backend/git"
	"mike/backend/store"
	"mike/backend/types"
)

type watcherBaselineEnv struct {
	wm        *git.WorktreeManager
	store     *store.Store
	projectID string
	taskID    string
	repoDir   string
}

func newWatcherBaselineEnv(t *testing.T) *watcherBaselineEnv {
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
	mainBranch := strings.TrimSpace(runGitTestOutput(t, repoDir, "rev-parse", "--abbrev-ref", "HEAD"))

	dbPath := filepath.Join(tmpDir, "test.db")
	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	cfgStore := config.NewConfigStore(s, dbPath, "test", nil, false)
	cfgStore.SetWorkspaceDirOverride(worktreesDir)
	cfg, err := cfgStore.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfgStore.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}

	projectID := "baseline-project"
	if err := s.CreateProject(&types.Project{ID: projectID, Name: "baseline", Path: repoDir}); err != nil {
		t.Fatal(err)
	}
	taskID := "baseline-task"
	taskBranch := "task/" + taskID
	if err := s.CreateTask(&types.Task{
		ID: taskID, ProjectID: projectID, Title: "baseline", Status: types.StatusInProgress,
		Branch: taskBranch, BaseRef: mainBranch,
	}); err != nil {
		t.Fatal(err)
	}
	wm := git.NewWorktreeManager(s, cfgStore)
	if _, _, err := wm.CreateWorktree(projectID, taskID, taskBranch, ""); err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	e := &watcherBaselineEnv{wm: wm, store: s, projectID: projectID, taskID: taskID, repoDir: repoDir}
	e.commitOnTaskBranch(t, "before-fork.txt", "work before the fork\n", "commit before the fork")
	return e
}

func (e *watcherBaselineEnv) commitOnTaskBranch(t *testing.T, name, content, message string) {
	t.Helper()
	wtPath, err := e.wm.WorktreePath(e.taskID)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, wtPath, name, content)
	runGit(t, wtPath, "add", name)
	runGit(t, wtPath, "commit", "-m", message)
}

// forkThread points the task's current thread at a branch forked off its own.
func (e *watcherBaselineEnv) forkThread(t *testing.T, threadID, forkBranch string) {
	t.Helper()
	task, err := e.store.GetTaskByID(e.taskID)
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, e.repoDir, "branch", forkBranch, task.Branch)
	if err := e.store.CreateThread(&types.TaskThread{
		ID: threadID, TaskID: e.taskID, Name: threadID, IsActive: true, GitBranch: forkBranch,
	}); err != nil {
		t.Fatal(err)
	}
	task.CurrentThreadID = threadID
	if err := e.store.UpdateTask(task); err != nil {
		t.Fatal(err)
	}
}

func TestWatcherReResolvesTheBaselineWhenAThreadForksMidRun(t *testing.T) {
	e := newWatcherBaselineEnv(t)

	var cached *git.TaskDiff
	ww, err := NewWorktreeWatcher(e.wm, func(DiffStatsEvent) {},
		func(_ string, d *git.TaskDiff) { cached = d }, nil)
	if err != nil {
		t.Skipf("skip watcher baseline test (inotify limit): %v", err)
	}
	defer ww.Close()
	if err := ww.Watch(e.taskID, e.projectID); err != nil {
		t.Fatalf("watch task: %v", err)
	}

	e.forkThread(t, "thread-row-1", "thread/forked-idea")
	e.commitOnTaskBranch(t, "after-fork.txt", "work after the fork\n", "commit after the fork")

	ww.computeAndBroadcast(e.taskID, e.projectID)

	if cached == nil {
		t.Fatal("the watcher cached no diff")
	}
	if !strings.Contains(cached.CommittedDiff, "work after the fork") {
		t.Errorf("the committed half lost the task's own commit:\n%s", cached.CommittedDiff)
	}
	if strings.Contains(cached.CommittedDiff, "work before the fork") {
		t.Errorf("the watcher is still diffing against the baseline captured when the watch started, so its "+
			"stats describe commits the forked thread already holds:\n%s", cached.CommittedDiff)
	}
}

// A done task's diff is BaseCommit..BranchTip, so it must not become the diff the per-file route answers
// from.
func TestWatcherKeepsADoneTaskDiffOutOfTheFileCache(t *testing.T) {
	e := newWatcherBaselineEnv(t)

	called := false
	ww, err := NewWorktreeWatcher(e.wm, func(DiffStatsEvent) {},
		func(string, *git.TaskDiff) { called = true }, nil)
	if err != nil {
		t.Skipf("skip watcher cache test (inotify limit): %v", err)
	}
	defer ww.Close()
	if err := ww.Watch(e.taskID, e.projectID); err != nil {
		t.Fatalf("watch task: %v", err)
	}

	task, err := e.store.GetTaskByID(e.taskID)
	if err != nil {
		t.Fatal(err)
	}
	task.Status = types.StatusDone
	task.BranchTip = strings.TrimSpace(runGitTestOutput(t, e.repoDir, "rev-parse", task.Branch))
	if err := e.store.UpdateTask(task); err != nil {
		t.Fatal(err)
	}

	ww.computeAndBroadcast(e.taskID, e.projectID)

	if called {
		t.Error("a done task's commit-to-commit diff was handed to the cache the panel's per-file fetch reads")
	}
}

// The client's "+N" is TrackedAdd + UntrackedLines, so the watcher must report an untracked file's
// lines only in UntrackedLines, or every untracked line is counted twice.
func TestWatcherReportsUntrackedLinesApartFromTrackedAdditions(t *testing.T) {
	e := newWatcherBaselineEnv(t)

	wtPath, err := e.wm.WorktreePath(e.taskID)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, wtPath, "README.md", "# Test\nline one\nline two\n")
	writeTestFile(t, wtPath, "scratch.txt", "a\nb\nc\n")

	var events []DiffStatsEvent
	ww, err := NewWorktreeWatcher(e.wm, func(ev DiffStatsEvent) { events = append(events, ev) }, nil, nil)
	if err != nil {
		t.Skipf("skip watcher stats test (inotify limit): %v", err)
	}
	defer ww.Close()
	if err := ww.Watch(e.taskID, e.projectID); err != nil {
		t.Fatal(err)
	}

	ww.computeAndBroadcast(e.taskID, e.projectID)

	if len(events) != 1 {
		t.Fatalf("broadcast %d frames, want 1", len(events))
	}
	ev := events[0]
	// 2 README additions + the 1 line the environment committed; the folding this test guards against
	// reported 6 here.
	if ev.TrackedAdd != 3 {
		t.Errorf("TrackedAdd = %d, want 3 — tracked additions only, with the untracked file's lines left out", ev.TrackedAdd)
	}
	if ev.UntrackedLines != 3 {
		t.Errorf("UntrackedLines = %d, want 3", ev.UntrackedLines)
	}
	if ev.UntrackedN != 1 || ev.FilesN != 3 {
		t.Errorf("UntrackedN/FilesN = %d/%d, want 1/3", ev.UntrackedN, ev.FilesN)
	}
}
