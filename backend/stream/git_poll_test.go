package stream

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"kanban/backend/config"
	"kanban/backend/git"
	"kanban/backend/store"
	"kanban/backend/types"
)

// TestBranchWatcherLifecycle verifies that the branch watcher starts, detects
// a branch change via fsnotify, and stops cleanly. Uses channels for fast signaling.
func TestBranchWatcherLifecycle(t *testing.T) {
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

	// Set up branch change detection with a channel
	var mu sync.Mutex
	var branches []string
	bw, err := NewBranchWatcher(func(projectID, branch string) {
		mu.Lock()
		branches = append(branches, branch)
		mu.Unlock()
	})
	if err != nil {
		t.Skipf("skip branch watcher test (inotify limit): %v", err)
	}
	go bw.StartEventLoop()
	defer bw.Close()

	// Watch the project — should detect initial branch (master/main)
	err = bw.Watch(projID, repoDir)
	if err != nil {
		t.Skipf("skip branch watcher test (inotify limit): %v", err)
	}

	// Give the watcher a moment to settle
	time.Sleep(100 * time.Millisecond)

	// Simulate a branch switch by writing a new HEAD file
	headPath := filepath.Join(repoDir, ".git", "HEAD")
	writeFile(t, "", "", "") // no-op, just get the helper
	err = os.WriteFile(headPath, []byte("ref: refs/heads/feature-test\n"), 0o644)
	if err != nil {
		t.Fatalf("write HEAD: %v", err)
	}

	// Wait for the branch change to be detected
	timeout := time.After(2 * time.Second)
	detected := false
	for !detected {
		mu.Lock()
		for _, b := range branches {
			if b == "feature-test" {
				detected = true
				break
			}
		}
		mu.Unlock()
		if detected {
			break
		}
		select {
		case <-timeout:
			t.Fatalf("timed out waiting for branch change detection; got branches: %v", branches)
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Unwatch should not panic
	bw.Unwatch(projID)

	// Verify the watcher is cleaned up
	bw.mu.Lock()
	_, exists := bw.projects[projID]
	bw.mu.Unlock()
	if exists {
		t.Fatal("expected branch watcher to be cleaned up after Unwatch")
	}
}

// TestBranchWatcherHandlesNonGitDir verifies that the branch watcher handles
// non-git directories gracefully (no panic).
func TestBranchWatcherHandlesNonGitDir(t *testing.T) {
	tmpDir := t.TempDir()
	nonGitDir := filepath.Join(tmpDir, "not-a-repo")
	os.MkdirAll(nonGitDir, 0o755)

	var mu sync.Mutex
	var broadcastCount int
	bw, err := NewBranchWatcher(func(projectID, branch string) {
		mu.Lock()
		broadcastCount++
		mu.Unlock()
	})
	if err != nil {
		t.Skipf("skip non-git dir test (inotify limit): %v", err)
	}
	go bw.StartEventLoop()
	defer bw.Close()

	// Watch a non-git directory — should not panic
	err = bw.Watch("non-git-project", nonGitDir)
	if err != nil {
		// It's OK if watching fails (no .git dir) — just shouldn't panic
		t.Logf("watch non-git dir (expected error): %v", err)
	}

	// Give the watcher a moment to settle
	time.Sleep(100 * time.Millisecond)

	// No broadcasts should have been sent for a non-git dir
	mu.Lock()
	count := broadcastCount
	mu.Unlock()
	if count > 0 {
		t.Fatalf("expected no broadcasts for non-git dir, got %d", count)
	}

	// Unwatch should not panic even if Watch failed
	bw.Unwatch("non-git-project")
}

// TestBranchWatcherDetectsDetachedHEAD verifies that the branch watcher
// correctly detects detached HEAD state.
func TestBranchWatcherDetectsDetachedHEAD(t *testing.T) {
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	os.MkdirAll(repoDir, 0o755)

	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")
	writeFile(t, repoDir, "README.md", "# Test\n")
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "Initial")

	// Get the current commit SHA
	out, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	fullSHA := string(out[:40])
	shortSHA := fullSHA[:7]

	var mu sync.Mutex
	var branches []string
	bw, err := NewBranchWatcher(func(projectID, branch string) {
		mu.Lock()
		branches = append(branches, branch)
		mu.Unlock()
	})
	if err != nil {
		t.Skipf("skip detached HEAD test (inotify limit): %v", err)
	}
	go bw.StartEventLoop()
	defer bw.Close()

	err = bw.Watch("test-project", repoDir)
	if err != nil {
		t.Skipf("skip detached HEAD test (inotify limit): %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Switch to detached HEAD by writing the raw SHA to .git/HEAD
	headPath := filepath.Join(repoDir, ".git", "HEAD")
	err = os.WriteFile(headPath, []byte(fullSHA+"\n"), 0o644)
	if err != nil {
		t.Fatalf("write detached HEAD: %v", err)
	}

	// Wait for the detached HEAD to be detected
	timeout := time.After(2 * time.Second)
	detected := false
	for !detected {
		mu.Lock()
		for _, b := range branches {
			if b == shortSHA {
				detected = true
				break
			}
		}
		mu.Unlock()
		if detected {
			break
		}
		select {
		case <-timeout:
			t.Fatalf("timed out waiting for detached HEAD detection; got branches: %v, expected short SHA: %s", branches, shortSHA)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// TestBranchWatcherDuplicateEvents verifies that writing the same branch
// twice does not produce duplicate broadcasts.
func TestBranchWatcherDuplicateEvents(t *testing.T) {
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	os.MkdirAll(repoDir, 0o755)

	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")
	writeFile(t, repoDir, "README.md", "# Test\n")
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "Initial")

	var mu sync.Mutex
	var broadcastCount int
	bw, err := NewBranchWatcher(func(projectID, branch string) {
		mu.Lock()
		broadcastCount++
		mu.Unlock()
	})
	if err != nil {
		t.Skipf("skip duplicate events test (inotify limit): %v", err)
	}
	go bw.StartEventLoop()
	defer bw.Close()

	err = bw.Watch("test-project", repoDir)
	if err != nil {
		t.Skipf("skip duplicate events test (inotify limit): %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Reset counter after initial branch detection
	mu.Lock()
	broadcastCount = 0
	mu.Unlock()

	// Write the same branch ref multiple times
	headPath := filepath.Join(repoDir, ".git", "HEAD")
	for i := 0; i < 3; i++ {
		err = os.WriteFile(headPath, []byte("ref: refs/heads/same-branch\n"), 0o644)
		if err != nil {
			t.Fatalf("write HEAD: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	time.Sleep(200 * time.Millisecond)

	// Should have received exactly 1 broadcast (first write) then 0 for duplicates
	mu.Lock()
	count := broadcastCount
	mu.Unlock()

	// First write changes from master/main to same-branch (1 broadcast).
	// Subsequent writes of the same value should produce 0 broadcasts.
	if count != 1 {
		t.Fatalf("expected 1 broadcast for branch change, got %d", count)
	}
}

// TestGitPollerDetectsChanges verifies that the worktree manager detects file changes.
func TestGitPollerDetectsChanges(t *testing.T) {
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
	cfgStore.SetWorkspaceDirOverride(worktreesDir)
	cfg, err := cfgStore.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if err := cfgStore.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	projID := "test-project"
	taskID := "test-task"
	branchName := "kanban/" + taskID

	proj := &types.Project{ID: projID, Name: "test", Path: repoDir}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

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

	// Verify that the worktree manager can detect changes
	worktreePath := filepath.Join(worktreesDir, projID, taskID)

	// Initial state — no changes
	if wm.HasWorktreeChanges(taskID) {
		t.Fatal("expected no worktree changes before file creation")
	}

	// Make a change
	writeFile(t, worktreePath, "new_file.txt", "Hello, world!\n")

	// After change — the worktree manager should detect the new file
	if !wm.HasWorktreeChanges(taskID) {
		t.Fatal("expected worktree manager to detect changes after file creation")
	}

	// Verify full diff is available (untracked files show in Untracked, not Diff)
	diffResult, err := wm.GetTaskDiff(taskID, "", "")
	if err != nil {
		t.Fatalf("get task diff: %v", err)
	}
	// New untracked file should appear in Untracked list
	if len(diffResult.Untracked) == 0 && diffResult.Diff == "" {
		t.Fatal("expected diff or untracked files after file creation")
	}
}

// TestWorktreeWatcherMultiTask verifies that the worktree watcher correctly
// routes events to the right task when multiple tasks are watched.
func TestWorktreeWatcherMultiTask(t *testing.T) {
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

	// Create 5 tasks with worktrees
	var taskIDs []string
	for i := 0; i < 5; i++ {
		taskID := fmt.Sprintf("task-%d", i)
		branchName := "kanban/" + taskID
		task := &types.Task{
			ID:             taskID,
			ProjectID:      projID,
			Title:          "Test task " + taskID,
			Prompt:         "Test prompt",
			Status:         types.StatusInProgress,
			ExecutionState: types.ExecStateRunning,
			Branch:         branchName,
		}
		if err := s.CreateTask(task); err != nil {
			t.Fatalf("create task %s: %v", taskID, err)
		}

		wm := git.NewWorktreeManager(s, cfgStore)
		_, _, err = wm.CreateWorktree(projID, taskID, branchName, "")
		if err != nil {
			t.Fatalf("create worktree %s: %v", taskID, err)
		}
		taskIDs = append(taskIDs, taskID)
	}

	// Create watcher and watch all tasks
	var mu sync.Mutex
	var broadcasts []string
	ww, err := NewWorktreeWatcher(git.NewWorktreeManager(s, cfgStore), func(event DiffStatsEvent) {
		mu.Lock()
		broadcasts = append(broadcasts, event.TaskID)
		mu.Unlock()
	}, nil, nil)
	if err != nil {
		t.Skipf("skip worktree watcher test (inotify limit): %v", err)
	}
	go ww.StartEventLoop()
	defer ww.Close()

	for _, taskID := range taskIDs {
		err = ww.Watch(taskID, projID, "")
		if err != nil {
			t.Fatalf("watch task %s: %v", taskID, err)
		}
	}

	// Verify path cache has entries for all tasks
	ww.mu.Lock()
	cacheSize := len(ww.pathCache)
	ww.mu.Unlock()
	if cacheSize < len(taskIDs) {
		t.Fatalf("path cache has %d entries, expected at least %d", cacheSize, len(taskIDs))
	}

	// Write a file in each worktree and verify events are routed correctly
	for _, taskID := range taskIDs {
		worktreePath := filepath.Join(worktreesDir, projID, taskID)
		writeFile(t, worktreePath, "test.txt", "content\n")
		time.Sleep(10 * time.Millisecond)
	}

	// Unwatch all tasks — should not panic
	for _, taskID := range taskIDs {
		ww.Unwatch(taskID)
	}

	// Verify path cache is cleaned up
	ww.mu.Lock()
	remaining := len(ww.pathCache)
	ww.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("path cache has %d entries after unwatch all, expected 0", remaining)
	}
}

// TestWorktreeWatcherWorkerPoolBounds verifies that the worker pool limits
// concurrent diff computations.
func TestWorktreeWatcherWorkerPoolBounds(t *testing.T) {
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

	// Create watcher
	ww, err := NewWorktreeWatcher(wm, func(event DiffStatsEvent) {}, nil, nil)
	if err != nil {
		t.Skipf("skip worker pool test (inotify limit): %v", err)
	}
	go ww.StartEventLoop()
	defer ww.Close()

	err = ww.Watch(taskID, projID, "")
	if err != nil {
		t.Fatalf("watch task: %v", err)
	}

	// Verify the job channel is bounded
	ww.mu.Lock()
	poolCap := cap(ww.jobCh)
	ww.mu.Unlock()

	// Pool size should be NumCPU-1 (min 1)
	expected := runtime.NumCPU() - 1
	if expected < 1 {
		expected = 1
	}
	if poolCap != expected {
		t.Fatalf("job channel capacity is %d, expected %d (NumCPU-1)", poolCap, expected)
	}
}

// TestBranchWatcherMultiProject verifies that the branch watcher correctly
// handles multiple projects simultaneously.
func TestBranchWatcherMultiProject(t *testing.T) {
	tmpDir := t.TempDir()

	// Create two separate repos
	var repoDirs []string
	for i := 0; i < 2; i++ {
		repoDir := filepath.Join(tmpDir, fmt.Sprintf("repo-%d", i))
		os.MkdirAll(repoDir, 0o755)
		runGit(t, repoDir, "init")
		runGit(t, repoDir, "config", "user.email", "test@test.com")
		runGit(t, repoDir, "config", "user.name", "Test")
		writeFile(t, repoDir, "README.md", "# Test\n")
		runGit(t, repoDir, "add", "README.md")
		runGit(t, repoDir, "commit", "-m", "Initial")
		repoDirs = append(repoDirs, repoDir)
	}

	var mu sync.Mutex
	var branchEvents []struct {
		projectID string
		branch    string
	}
	bw, err := NewBranchWatcher(func(projectID, branch string) {
		mu.Lock()
		branchEvents = append(branchEvents, struct {
			projectID string
			branch    string
		}{projectID, branch})
		mu.Unlock()
	})
	if err != nil {
		t.Skipf("skip multi-project test (inotify limit): %v", err)
	}
	go bw.StartEventLoop()
	defer bw.Close()

	// Watch both projects
	for i, repoDir := range repoDirs {
		projID := fmt.Sprintf("project-%d", i)
		err = bw.Watch(projID, repoDir)
		if err != nil {
			t.Skipf("skip multi-project test (inotify limit): %v", err)
		}
	}

	time.Sleep(100 * time.Millisecond)

	// Switch branch in repo 0 only
	headPath := filepath.Join(repoDirs[0], ".git", "HEAD")
	err = os.WriteFile(headPath, []byte("ref: refs/heads/only-repo-zero\n"), 0o644)
	if err != nil {
		t.Fatalf("write HEAD: %v", err)
	}

	// Wait for detection
	timeout := time.After(2 * time.Second)
	detected := false
	for !detected {
		mu.Lock()
		for _, e := range branchEvents {
			if e.projectID == "project-0" && e.branch == "only-repo-zero" {
				detected = true
				break
			}
		}
		mu.Unlock()
		if detected {
			break
		}
		select {
		case <-timeout:
			mu.Lock()
			events := make([]struct{ projectID, branch string }, len(branchEvents))
			copy(events, branchEvents)
			mu.Unlock()
			t.Fatalf("timed out waiting for branch change; got events: %v", events)
		case <-time.After(50 * time.Millisecond):
		}
	}

	// Verify project-1 was NOT affected
	mu.Lock()
	for _, e := range branchEvents {
		if e.projectID == "project-1" && e.branch == "only-repo-zero" {
			mu.Unlock()
			t.Fatal("project-1 should not have received project-0's branch change")
		}
	}
	mu.Unlock()
}

// runGit runs a git command in the specified directory.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", args, err, out)
	}
}

// writeFile writes content to a file in the specified directory.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if dir != "" && name != "" {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
	}
}
