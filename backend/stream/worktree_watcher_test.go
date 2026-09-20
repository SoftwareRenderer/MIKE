package stream

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"mike/backend/config"
	"mike/backend/git"
	"mike/backend/store"
	"mike/backend/types"
)

// TestWorktreeWatcherRetriesWhileInFlight verifies that a pending diff is not
// dropped when the throttle fires while a compute is already in flight. The
// watcher must re-arm a retry timer instead of discarding the change, otherwise
// the diff panel goes stale until the next fsnotify event or a page refresh.
func TestWorktreeWatcherRetriesWhileInFlight(t *testing.T) {
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

	cfgStore := config.NewConfigStore(s, dbPath, "test", nil)
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
	_, _, err = wm.CreateWorktree(projID, taskID, branchName, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	ww, err := NewWorktreeWatcher(wm, func(event DiffStatsEvent) {}, nil, nil)
	if err != nil {
		t.Skipf("skip retry test (inotify limit): %v", err)
	}
	go ww.StartEventLoop()
	defer ww.Close()

	if err := ww.Watch(taskID, projID, ""); err != nil {
		t.Fatalf("watch task: %v", err)
	}

	// Simulate a diff compute already in flight for the task.
	ww.mu.Lock()
	ww.inFlight[taskID] = true
	ww.mu.Unlock()

	// Trigger a filesystem change -> handleEvent arms the 1s throttle timer.
	worktreePath := filepath.Join(worktreesDir, projID, taskID)
	writeFile(t, worktreePath, "new.txt", "hello\n")

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

func TestCountManifestChanges(t *testing.T) {
	tests := []struct {
		name      string
		tracked   map[string]*git.TrackedDiffFile
		untracked []git.UntrackedFile
		wantAdd   int
		wantDel   int
	}{
		{
			name:      "empty",
			tracked:   nil,
			untracked: nil,
			wantAdd:   0,
			wantDel:   0,
		},
		{
			name: "single tracked file",
			tracked: map[string]*git.TrackedDiffFile{
				"a.txt": {Path: "a.txt", Added: 5, Removed: 3},
			},
			untracked: nil,
			wantAdd:   5,
			wantDel:   3,
		},
		{
			name: "multiple tracked files",
			tracked: map[string]*git.TrackedDiffFile{
				"a.txt": {Path: "a.txt", Added: 5, Removed: 3},
				"b.txt": {Path: "b.txt", Added: 2, Removed: 1},
			},
			untracked: nil,
			wantAdd:   7,
			wantDel:   4,
		},
		{
			name: "tracked with untracked",
			tracked: map[string]*git.TrackedDiffFile{
				"a.txt": {Path: "a.txt", Added: 3, Removed: 2},
			},
			untracked: []git.UntrackedFile{
				{Path: "new.txt", Lines: []string{"line1", "line2", "line3"}},
			},
			wantAdd: 6, // 3 tracked + 3 untracked
			wantDel: 2,
		},
		{
			name: "untracked with OrigAdded (truncated)",
			tracked: map[string]*git.TrackedDiffFile{
				"a.txt": {Path: "a.txt", Added: 1, Removed: 1},
			},
			untracked: []git.UntrackedFile{
				{Path: "new.txt", Lines: []string{"line1"}, OrigAdded: 100, Truncated: true},
			},
			wantAdd: 101, // 1 tracked + 100 untracked (OrigAdded)
			wantDel: 1,
		},
		{
			name: "truncated tracked file uses truncated counts",
			tracked: map[string]*git.TrackedDiffFile{
				// After truncation, Added/Removed reflect truncated lines
				// OrigAdded/OrigRemoved reflect original (full) lines
				"large.txt": {Path: "large.txt", Added: 6, Removed: 0, Truncated: true, OrigAdded: 90, OrigRemoved: 0},
			},
			untracked: nil,
			wantAdd:   6, // truncated count, NOT 90
			wantDel:   0,
		},
		{
			name: "excludes internal files from tracked",
			tracked: map[string]*git.TrackedDiffFile{
				"a.txt":       {Path: "a.txt", Added: 5, Removed: 3},
				".git/config": {Path: ".git/config", Added: 100, Removed: 200},
			},
			untracked: nil,
			wantAdd:   5, // only a.txt, NOT .git/config
			wantDel:   3,
		},
		{
			name: "excludes internal files from untracked",
			tracked: map[string]*git.TrackedDiffFile{
				"a.txt": {Path: "a.txt", Added: 1, Removed: 1},
			},
			untracked: []git.UntrackedFile{
				{Path: "new.txt", Lines: []string{"line1"}},
				{Path: ".git/new", Lines: []string{"spam1", "spam2", "spam3", "spam4"}},
			},
			wantAdd: 2, // 1 tracked + 1 untracked (new.txt), NOT .git/new
			wantDel: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &git.TaskDiff{
				Tracked:   tt.tracked,
				Untracked: tt.untracked,
			}
			add, del := countManifestChanges(d)
			if add != tt.wantAdd || del != tt.wantDel {
				t.Errorf("countManifestChanges() = add=%d, del=%d, want add=%d, del=%d", add, del, tt.wantAdd, tt.wantDel)
			}
		})
	}
}

func TestCountManifestFiles(t *testing.T) {
	tests := []struct {
		name          string
		tracked       map[string]*git.TrackedDiffFile
		untracked     []git.UntrackedFile
		wantTracked   int
		wantUntracked int
	}{
		{
			name:          "empty",
			tracked:       nil,
			untracked:     nil,
			wantTracked:   0,
			wantUntracked: 0,
		},
		{
			name: "mixed with internal files",
			tracked: map[string]*git.TrackedDiffFile{
				"a.txt":        {Path: "a.txt"},
				"b.txt":        {Path: "b.txt"},
				".git/config":  {Path: ".git/config"},
				"kanban.trace": {Path: "kanban.trace"},
			},
			untracked: []git.UntrackedFile{
				{Path: "new.txt"},
				{Path: ".git/new"},
			},
			wantTracked:   2, // a.txt, b.txt (excludes .git/config, kanban.trace)
			wantUntracked: 1, // new.txt (excludes .git/new)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &git.TaskDiff{
				Tracked:   tt.tracked,
				Untracked: tt.untracked,
			}
			tracked, untracked := countManifestFiles(d)
			if tracked != tt.wantTracked || untracked != tt.wantUntracked {
				t.Errorf("countManifestFiles() = tracked=%d, untracked=%d, want tracked=%d, untracked=%d",
					tracked, untracked, tt.wantTracked, tt.wantUntracked)
			}
		})
	}
}
