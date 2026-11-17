package stream

// What the branch watcher tells the board when it watches a project's .git directory over the
// real fsnotify loop: a branch switch arrives, a detached HEAD arrives as its short SHA, the same
// value twice arrives once, a directory that is not a checkout says nothing, and a project that
// was never watched has nothing to unwatch. Which project an event belongs to, given two watched
// directories, is pinned deterministically in branch_watcher_watch_test.go.

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mike/backend/config"
	"mike/backend/store"
	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

// TestBranchWatcherLifecycle verifies that the branch watcher starts, detects
// a branch change via fsnotify, and stops cleanly. Uses channels for fast signaling.
func TestBranchWatcherLifecycle(t *testing.T) {
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

	testgit.Git(t, repoDir, "init")
	testgit.Git(t, repoDir, "config", "user.email", "test@test.com")
	testgit.Git(t, repoDir, "config", "user.name", "Test")
	testgit.WriteFile(t, repoDir, "README.md", "# Test\n")
	testgit.Git(t, repoDir, "add", "README.md")
	testgit.Git(t, repoDir, "commit", "-m", "Initial")

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

	testgit.Git(t, repoDir, "init")
	testgit.Git(t, repoDir, "config", "user.email", "test@test.com")
	testgit.Git(t, repoDir, "config", "user.name", "Test")
	testgit.WriteFile(t, repoDir, "README.md", "# Test\n")
	testgit.Git(t, repoDir, "add", "README.md")
	testgit.Git(t, repoDir, "commit", "-m", "Initial")

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
