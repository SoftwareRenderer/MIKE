package stream

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"kanban/backend/config"
	"kanban/backend/git"
	"kanban/backend/store"
	"kanban/backend/types"
)

// TestGitStatusPoll verifies that the poller detects file changes and sends diff_changed events.
func TestGitStatusPoll(t *testing.T) {
	// Create a temporary test repo
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	worktreesDir := filepath.Join(tmpDir, "worktrees")
	os.MkdirAll(repoDir, 0o755)
	os.MkdirAll(worktreesDir, 0o755)

	// Initialize git repo
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")
	writeFile(t, repoDir, "README.md", "# Test\n")
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "Initial")

	// Create in-memory store and config
	dbPath := filepath.Join(tmpDir, "test.db")
	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer s.Close()

	cfgStore := config.NewConfigStore(s)
	cfg, err := cfgStore.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.WorkspaceDir = worktreesDir
	if err := cfgStore.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	// Create project and task in store
	projID := "test-project"
	taskID := "test-task"
	branchName := "kanban/" + taskID

	proj := &types.Project{ID: projID, Name: "test", Path: repoDir}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := &types.Task{
		ID:          taskID,
		ProjectID:   projID,
		Title:       "Test task",
		Prompt:      "Test prompt",
		Status:      "in_progress",
		StatusState: "running",
		Branch:      branchName,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Create worktree
	wm := git.NewWorktreeManager(s, cfgStore)
	_, _, err = wm.CreateWorktree(projID, taskID, branchName)
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	// Create broadcaster
	b := newBroadcaster(100)

	// Create stream manager with poller tracking
	sm := &StreamManager{
		worktreeMgr: wm,
		pollers:     make(map[string]context.CancelFunc),
	}

	// Start the poller (simulating first client connecting)
	ctx, cancel := context.WithCancel(context.Background())
	sm.pollers[taskID] = cancel
	go sm.pollGitStatus(ctx, taskID, b)

	// Wait for initial baseline to be established
	time.Sleep(1500 * time.Millisecond)

	// Check initial buffer count (baseline)
	initialCount := b.bufferLength()

	// Modify a file in the worktree
	worktreePath := filepath.Join(worktreesDir, taskID)
	writeFile(t, worktreePath, "new_file.txt", "hello world\n")

	// Wait for poller to detect change (2s poll interval + buffer)
	var detected bool
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		if b.bufferLength() > initialCount {
			detected = true
			break
		}
	}

	if !detected {
		t.Fatalf("poller did not detect file change within 5s; buffer length stayed at %d", initialCount)
	}

	// Verify the event type is diff_changed
	lastMsg := b.getLastMessage()
	if lastMsg == nil || lastMsg.Type != string(types.WSTypeDiffChanged) {
		typ := "<nil>"
		if lastMsg != nil {
			typ = lastMsg.Type
		}
		t.Fatalf("expected diff_changed event, got type=%q", typ)
	}

	// --- Test: modifying the SAME file again also triggers a diff_changed event ---
	// This verifies the fix for the bug where subsequent changes to the same file
	// were not detected (because git status --porcelain shows identical output).
	eventCountAfterFirst := b.bufferLength()

	// Modify the same file again with different content
	writeFile(t, worktreePath, "new_file.txt", "hello world\nsecond line\nthird line\n")

	var sameFileDetected bool
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		if b.bufferLength() > eventCountAfterFirst {
			sameFileDetected = true
			break
		}
	}

	if !sameFileDetected {
		t.Fatalf("poller did not detect same-file modification within 5s; buffer length stayed at %d", eventCountAfterFirst)
	}

	// Verify the new event is also diff_changed
	lastMsg = b.getLastMessage()
	if lastMsg == nil || lastMsg.Type != string(types.WSTypeDiffChanged) {
		typ := "<nil>"
		if lastMsg != nil {
			typ = lastMsg.Type
		}
		t.Fatalf("expected diff_changed event for same-file mod, got type=%q", typ)
	}

	// Stop the poller
	cancel()
	time.Sleep(500 * time.Millisecond)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %s: %v", args, string(out), err)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}
