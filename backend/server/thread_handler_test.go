package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kanban/backend/types"
)

func TestThreadList(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	req := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	threads := mustJSON[[]map[string]any](t, rr.Body)
	if len(threads) < 1 {
		t.Fatalf("expected at least 1 thread, got %d", len(threads))
	}
	var hasMain bool
	for _, th := range threads {
		if th["name"] == "Main" {
			hasMain = true
			break
		}
	}
	if !hasMain {
		t.Fatal("expected Main thread")
	}
}

func TestThreadFork(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	// Get the Main thread
	listReq := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)
	listRR := testResponseAuth(t, s, listReq)
	threads := mustJSON[[]map[string]any](t, listRR.Body)
	if len(threads) < 1 {
		t.Fatal("expected at least 1 thread")
	}
	mainThreadID := threads[0]["id"].(string)

	// Create a message first (to have a checkpoint)
	createReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"role":    "user",
		"content": "Message to fork from",
	})
	createRR := testResponseAuth(t, s, createReq)
	msg := mustJSON[map[string]any](t, createRR.Body)
	msgID := msg["id"].(string)

	// Fork from this message
	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/fork", map[string]any{
		"from_thread_id":  mainThreadID,
		"from_message_id": msgID,
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)
	if result["task_id"] != taskID {
		t.Fatalf("wrong task_id: %s", result["task_id"])
	}
	name := result["name"].(string)
	if name != "Fork #1" && name != "Fork #2" {
		t.Fatalf("expected Fork #N, got %s", name)
	}
	if result["is_active"] != true {
		t.Fatal("new fork should be active")
	}

	// Verify task's current_thread_id is updated
	taskReq := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID, nil)
	taskRR := testResponseAuth(t, s, taskReq)
	task := mustJSON[map[string]any](t, taskRR.Body)
	if task["current_thread_id"] != result["id"] {
		t.Fatalf("task current_thread_id not updated: %s vs %s", task["current_thread_id"], result["id"])
	}
}

func TestThreadSwitch(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	// Get existing threads
	listReq := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)
	listRR := testResponseAuth(t, s, listReq)
	threads := mustJSON[[]map[string]any](t, listRR.Body)
	if len(threads) < 1 {
		t.Fatal("expected at least 1 thread")
	}
	mainThreadID := threads[0]["id"].(string)

	createReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"role":    "user",
		"content": "Message for fork",
	})
	createRR := testResponseAuth(t, s, createReq)
	msg := mustJSON[map[string]any](t, createRR.Body)
	msgID := msg["id"].(string)

	// Fork to create a second thread
	forkReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/fork", map[string]any{
		"from_thread_id":  mainThreadID,
		"from_message_id": msgID,
	})
	forkRR := testResponseAuth(t, s, forkReq)
	fork := mustJSON[map[string]any](t, forkRR.Body)
	forkThreadID := fork["id"].(string)

	// Switch back to Main
	switchReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
		"thread_id": mainThreadID,
	})
	switchRR := testResponseAuth(t, s, switchReq)
	if switchRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", switchRR.Code, switchRR.Body.String())
	}

	// Verify task's current_thread_id is updated to Main
	taskReq := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID, nil)
	taskRR := testResponseAuth(t, s, taskReq)
	task := mustJSON[map[string]any](t, taskRR.Body)
	if task["current_thread_id"] != mainThreadID {
		t.Fatalf("expected current_thread_id=%s, got %s", mainThreadID, task["current_thread_id"])
	}

	// Verify thread list shows correct active state
	listReq2 := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)
	listRR2 := testResponseAuth(t, s, listReq2)
	threads2 := mustJSON[[]map[string]any](t, listRR2.Body)

	activeCount := 0
	var activeID string
	for _, th := range threads2 {
		if th["is_active"] == true {
			activeCount++
			activeID = th["id"].(string)
		}
	}
	if activeCount != 1 {
		t.Fatalf("expected 1 active thread, got %d", activeCount)
	}
	if activeID != mainThreadID {
		t.Fatalf("expected Main to be active, got %s", activeID)
	}
	_ = forkThreadID // fork was created, verified by switch working
}

func TestThreadSwitchNotFound(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
		"thread_id": "nonexistent-thread",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusNotFound && rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 404 or 400 for nonexistent thread, got %d", rr.Code)
	}
}

func TestMessageScopedToThread(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	// Get Main thread
	listReq := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)
	listRR := testResponseAuth(t, s, listReq)
	threads := mustJSON[[]map[string]any](t, listRR.Body)
	if len(threads) < 1 {
		t.Fatal("expected at least 1 thread")
	}
	mainThreadID := threads[0]["id"].(string)

	// Create a message to fork from
	createReq2 := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"role":    "user",
		"content": "Fork point",
	})
	createRR2 := testResponseAuth(t, s, createReq2)
	msg := mustJSON[map[string]any](t, createRR2.Body)
	msgID := msg["id"].(string)

	forkReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/fork", map[string]any{
		"from_thread_id":  mainThreadID,
		"from_message_id": msgID,
	})
	forkRR := testResponseAuth(t, s, forkReq)
	fork := mustJSON[map[string]any](t, forkRR.Body)
	forkThreadID := fork["id"].(string)

	// Create a message in the fork thread
	createReq3 := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"role":    "user",
		"content": "Message in Fork",
	})
	testResponseAuth(t, s, createReq3)

	// Get messages — should be scoped to current (fork) thread
	listMsgReq := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", nil)
	listMsgRR := testResponseAuth(t, s, listMsgReq)
	msgs := mustJSON[[]map[string]any](t, listMsgRR.Body)

	// Fork thread should have copied messages + new message
	var hasForkMsg bool
	for _, m := range msgs {
		if m["content"] == "Message in Fork" {
			hasForkMsg = true
			break
		}
	}
	if !hasForkMsg {
		t.Fatal("fork thread should contain 'Message in Fork'")
	}
	// Verify the fork thread ID is set on messages
	for _, m := range msgs {
		if m["thread_id"] != forkThreadID {
			t.Fatalf("message should be in fork thread, got thread_id=%s", m["thread_id"])
		}
	}
}

func TestThreadSwitchConcurrent(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	// Get existing threads
	listReq := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)
	listRR := testResponseAuth(t, s, listReq)
	threads := mustJSON[[]map[string]any](t, listRR.Body)
	if len(threads) < 1 {
		t.Fatal("expected at least 1 thread")
	}
	mainThreadID := threads[0]["id"].(string)

	// Create a message to fork from
	createReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"role":    "user",
		"content": "Fork point",
	})
	createRR := testResponseAuth(t, s, createReq)
	msg := mustJSON[map[string]any](t, createRR.Body)
	msgID := msg["id"].(string)

	// Fork to create a second thread
	forkReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/fork", map[string]any{
		"from_thread_id":  mainThreadID,
		"from_message_id": msgID,
	})
	forkRR := testResponseAuth(t, s, forkReq)
	fork := mustJSON[map[string]any](t, forkRR.Body)
	forkThreadID := fork["id"].(string)

	// Fire two concurrent thread switch requests (back and forth)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var results []int

	// Switch to fork thread
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
			"thread_id": forkThreadID,
		})
		cookie, csrfToken := loginTestCSRF(t, s)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		mu.Lock()
		results = append(results, rr.Code)
		mu.Unlock()
	}()

	// Switch back to main thread
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
			"thread_id": mainThreadID,
		})
		cookie, csrfToken := loginTestCSRF(t, s)
		withAuthCSRF(req, cookie, csrfToken)
		rr := testResponse(t, s, req)
		mu.Lock()
		results = append(results, rr.Code)
		mu.Unlock()
	}()

	wg.Wait()

	// Both requests should succeed (serialized, not rejected)
	for i, code := range results {
		if code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, code)
		}
	}

	// Final state: exactly one active thread
	taskReq := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID, nil)
	taskRR := testResponseAuth(t, s, taskReq)
	task := mustJSON[map[string]any](t, taskRR.Body)

	// Current thread should be one of the two valid threads
	currentThreadID := task["current_thread_id"].(string)
	if currentThreadID != mainThreadID && currentThreadID != forkThreadID {
		t.Fatalf("unexpected current_thread_id: %s", currentThreadID)
	}

	// Verify exactly one active thread in the list
	listReq2 := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)
	listRR2 := testResponseAuth(t, s, listReq2)
	threads2 := mustJSON[[]map[string]any](t, listRR2.Body)

	activeCount := 0
	for _, th := range threads2 {
		if th["is_active"] == true {
			activeCount++
		}
	}
	if activeCount != 1 {
		t.Fatalf("expected 1 active thread after concurrent switches, got %d", activeCount)
	}
}

// TestThreadSwitchWorktreeContent verifies that worktree file content is correctly
// restored when switching between threads with different file states.
// This tests the exact user-reported scenario: fork a thread, modify files differently,
// then switch back and forth — the worktree should reflect the active thread's state.
func TestThreadSwitchWorktreeContent(t *testing.T) {
	s := testServer(t)

	// Create a project with a git repo that has an initial file
	tmpDir := t.TempDir()
	initialContent := "hello world\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte(initialContent), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}
	initGitRepo(t, tmpDir)

	cookie, csrfToken := loginTestCSRF(t, s)
	proj := map[string]any{"name": "thread-worktree-test", "path": tmpDir}
	req := testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	projID := mustJSON[map[string]any](t, rr.Body)["id"].(string)

	taskReq := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]any{"prompt": "test task"})
	withAuthCSRF(taskReq, cookie, csrfToken)
	taskRR := testResponse(t, s, taskReq)
	if taskRR.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", taskRR.Code, taskRR.Body.String())
	}
	taskData := mustJSON[map[string]any](t, taskRR.Body)
	taskID := taskData["id"].(string)

	// Create worktree (simulating task start)
	worktreePath, _, err := s.executor.WorktreeMgr().CreateWorktree(projID, taskID, "task/"+taskID, "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	// Update task with branch info (required for thread switch to manipulate worktree)
	task, err := s.store.GetTask(projID, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	task.Branch = "task/" + taskID
	task.BaseRef = "master"
	if err := s.store.UpdateTask(task); err != nil {
		t.Fatalf("update task branch: %v", err)
	}

	// --- Simulate Thread A (Main): write "hello friend" ---
	friendContent := "hello friend\n"
	if err := os.WriteFile(filepath.Join(worktreePath, "file.txt"), []byte(friendContent), 0o644); err != nil {
		t.Fatalf("write friend content: %v", err)
	}

	// Get the Main thread
	listReq := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)
	listRR := testResponseAuth(t, s, listReq)
	threads := mustJSON[[]map[string]any](t, listRR.Body)
	mainThreadID := threads[0]["id"].(string)

	// Create a checkpoint message (needed for fork)
	msgReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"role":    "user",
		"content": "Change world to friend",
	})
	withAuthCSRF(msgReq, cookie, csrfToken)
	msgRR := testResponse(t, s, msgReq)
	msgData := mustJSON[map[string]any](t, msgRR.Body)
	msgID := msgData["id"].(string)

	// --- Fork to create Thread B ---
	forkReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/fork", map[string]any{
		"from_thread_id":  mainThreadID,
		"from_message_id": msgID,
	})
	withAuthCSRF(forkReq, cookie, csrfToken)
	forkRR := testResponse(t, s, forkReq)
	if forkRR.Code != http.StatusCreated {
		t.Fatalf("fork: %d %s", forkRR.Code, forkRR.Body.String())
	}
	forkData := mustJSON[map[string]any](t, forkRR.Body)
	forkThreadID := forkData["id"].(string)

	// --- Simulate Thread B (fork): write "hello choom" ---
	// After fork, worktree is reset to checkpoint (baseline = "hello world")
	// Write the fork's content
	choomContent := "hello choom\n"
	if err := os.WriteFile(filepath.Join(worktreePath, "file.txt"), []byte(choomContent), 0o644); err != nil {
		t.Fatalf("write choom content: %v", err)
	}

	// Verify worktree has fork's content
	verifyWorktreeContent(t, worktreePath, "file.txt", choomContent, "after fork, before switches")

	// --- Switch 1: Fork → Main ---
	switchReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
		"thread_id": mainThreadID,
	})
	withAuthCSRF(switchReq, cookie, csrfToken)
	switchRR := testResponse(t, s, switchReq)
	if switchRR.Code != http.StatusOK {
		t.Fatalf("switch to main: %d %s", switchRR.Code, switchRR.Body.String())
	}
	verifyWorktreeContent(t, worktreePath, "file.txt", friendContent, "after switch to Main (1)")

	// --- Switch 2: Main → Fork ---
	switchReq = testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
		"thread_id": forkThreadID,
	})
	withAuthCSRF(switchReq, cookie, csrfToken)
	switchRR = testResponse(t, s, switchReq)
	if switchRR.Code != http.StatusOK {
		t.Fatalf("switch to fork: %d %s", switchRR.Code, switchRR.Body.String())
	}
	verifyWorktreeContent(t, worktreePath, "file.txt", choomContent, "after switch to Fork (2)")

	// --- Switch 3: Fork → Main ---
	switchReq = testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
		"thread_id": mainThreadID,
	})
	withAuthCSRF(switchReq, cookie, csrfToken)
	switchRR = testResponse(t, s, switchReq)
	if switchRR.Code != http.StatusOK {
		t.Fatalf("switch to main: %d %s", switchRR.Code, switchRR.Body.String())
	}
	verifyWorktreeContent(t, worktreePath, "file.txt", friendContent, "after switch to Main (3)")

	// --- Switch 4: Main → Fork ---
	switchReq = testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
		"thread_id": forkThreadID,
	})
	withAuthCSRF(switchReq, cookie, csrfToken)
	switchRR = testResponse(t, s, switchReq)
	if switchRR.Code != http.StatusOK {
		t.Fatalf("switch to fork: %d %s", switchRR.Code, switchRR.Body.String())
	}
	verifyWorktreeContent(t, worktreePath, "file.txt", choomContent, "after switch to Fork (4)")

	// --- Switch 5: Fork → Main (final) ---
	switchReq = testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
		"thread_id": mainThreadID,
	})
	withAuthCSRF(switchReq, cookie, csrfToken)
	switchRR = testResponse(t, s, switchReq)
	if switchRR.Code != http.StatusOK {
		t.Fatalf("switch to main: %d %s", switchRR.Code, switchRR.Body.String())
	}
	verifyWorktreeContent(t, worktreePath, "file.txt", friendContent, "after switch to Main (5)")
}

func verifyWorktreeContent(t *testing.T, worktreePath, filePath, expectedContent, label string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(worktreePath, filePath))
	if err != nil {
		t.Fatalf("[%s] read worktree file: %v", label, err)
	}
	if string(data) != expectedContent {
		t.Fatalf("[%s] expected %q, got %q", label, expectedContent, string(data))
	}
}

// TestThreadForkFromInactiveThread verifies that forking from a non-active thread
// does not corrupt that thread's saved worktree stash hash.
// Bug: handleThreadFork always stashes the current worktree and saves it to
// FromThreadID, even when FromThreadID != CurrentThreadID. This overwrites
// the inactive thread's stash with the current (wrong) worktree state.
func TestThreadForkFromInactiveThread(t *testing.T) {
	s := testServer(t)

	// Create a project with a git repo that has an initial committed file
	tmpDir := t.TempDir()
	initialContent := "hello world\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte(initialContent), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}
	initGitRepo(t, tmpDir)
	// Commit file.txt so it is tracked (untracked files are lost by clean -fd)
	mustRunGit(t, tmpDir, "add", "file.txt")
	mustRunGit(t, tmpDir, "commit", "-m", "Add file.txt")

	cookie, csrfToken := loginTestCSRF(t, s)
	proj := map[string]any{"name": "fork-inactive-test", "path": tmpDir}
	req := testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	projID := mustJSON[map[string]any](t, rr.Body)["id"].(string)

	taskReq := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]any{"prompt": "test task"})
	withAuthCSRF(taskReq, cookie, csrfToken)
	taskRR := testResponse(t, s, taskReq)
	if taskRR.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", taskRR.Code, taskRR.Body.String())
	}
	taskData := mustJSON[map[string]any](t, taskRR.Body)
	taskID := taskData["id"].(string)

	// Create worktree
	worktreePath, _, err := s.executor.WorktreeMgr().CreateWorktree(projID, taskID, "task/"+taskID, "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	task, err := s.store.GetTask(projID, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	task.Branch = "task/" + taskID
	task.BaseRef = "master"
	if err := s.store.UpdateTask(task); err != nil {
		t.Fatalf("update task branch: %v", err)
	}

	// --- Thread A (Main): write "hello friend" ---
	friendContent := "hello friend\n"
	if err := os.WriteFile(filepath.Join(worktreePath, "file.txt"), []byte(friendContent), 0o644); err != nil {
		t.Fatalf("write friend content: %v", err)
	}

	// Get the Main thread
	listReq := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)
	listRR := testResponseAuth(t, s, listReq)
	threads := mustJSON[[]map[string]any](t, listRR.Body)
	mainThreadID := threads[0]["id"].(string)

	// Create two checkpoint messages on Main
	msgReq1 := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"role":    "user",
		"content": "Message 1",
	})
	withAuthCSRF(msgReq1, cookie, csrfToken)
	msgRR1 := testResponse(t, s, msgReq1)
	msg1Data := mustJSON[map[string]any](t, msgRR1.Body)
	msg1ID := msg1Data["id"].(string)

	msgReq2 := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]any{
		"role":    "user",
		"content": "Message 2",
	})
	withAuthCSRF(msgReq2, cookie, csrfToken)
	msgRR2 := testResponse(t, s, msgReq2)
	msg2Data := mustJSON[map[string]any](t, msgRR2.Body)
	msg2ID := msg2Data["id"].(string)

	// --- Fork Thread B from Message 1 ---
	forkReq1 := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/fork", map[string]any{
		"from_thread_id":  mainThreadID,
		"from_message_id": msg1ID,
	})
	withAuthCSRF(forkReq1, cookie, csrfToken)
	forkRR1 := testResponse(t, s, forkReq1)
	if forkRR1.Code != http.StatusCreated {
		t.Fatalf("fork B: %d %s", forkRR1.Code, forkRR1.Body.String())
	}
	fork1Data := mustJSON[map[string]any](t, forkRR1.Body)
	forkBID := fork1Data["id"].(string)

	// --- Simulate Thread B: write "hello choom" ---
	choomContent := "hello choom\n"
	if err := os.WriteFile(filepath.Join(worktreePath, "file.txt"), []byte(choomContent), 0o644); err != nil {
		t.Fatalf("write choom content: %v", err)
	}
	verifyWorktreeContent(t, worktreePath, "file.txt", choomContent, "on fork B")

	// --- Fork Thread C from Message 2 on Main (while worktree is on B!) ---
	// This is the bug: fork handler should NOT overwrite Main's stash hash
	forkReq2 := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/fork", map[string]any{
		"from_thread_id":  mainThreadID,
		"from_message_id": msg2ID,
	})
	withAuthCSRF(forkReq2, cookie, csrfToken)
	forkRR2 := testResponse(t, s, forkReq2)
	if forkRR2.Code != http.StatusCreated {
		t.Fatalf("fork C: %d %s", forkRR2.Code, forkRR2.Body.String())
	}
	fork2Data := mustJSON[map[string]any](t, forkRR2.Body)
	forkCID := fork2Data["id"].(string)

	// --- Switch to Main: should show "hello friend" ---
	switchReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
		"thread_id": mainThreadID,
	})
	withAuthCSRF(switchReq, cookie, csrfToken)
	switchRR := testResponse(t, s, switchReq)
	if switchRR.Code != http.StatusOK {
		t.Fatalf("switch to main: %d %s", switchRR.Code, switchRR.Body.String())
	}
	verifyWorktreeContent(t, worktreePath, "file.txt", friendContent, "switch#1 Main")

	// --- Switch back to Fork B: should show "hello choom" ---
	switchReq = testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
		"thread_id": forkBID,
	})
	withAuthCSRF(switchReq, cookie, csrfToken)
	switchRR = testResponse(t, s, switchReq)
	if switchRR.Code != http.StatusOK {
		t.Fatalf("switch to fork B: %d %s", switchRR.Code, switchRR.Body.String())
	}
	verifyWorktreeContent(t, worktreePath, "file.txt", choomContent, "switch#2 Fork B")

	// --- Switch to Fork C: should show checkpoint content (hello world) ---
	switchReq = testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
		"thread_id": forkCID,
	})
	withAuthCSRF(switchReq, cookie, csrfToken)
	switchRR = testResponse(t, s, switchReq)
	if switchRR.Code != http.StatusOK {
		t.Fatalf("switch to fork C: %d %s", switchRR.Code, switchRR.Body.String())
	}
	verifyWorktreeContent(t, worktreePath, "file.txt", initialContent, "switch#3 Fork C")

	// --- Multiple round-trips: Main → B → C → Main → B → C → Main ---
	roundTripSwitch := func(label, want string, threadID string) {
		switchReq = testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
			"thread_id": threadID,
		})
		withAuthCSRF(switchReq, cookie, csrfToken)
		switchRR = testResponse(t, s, switchReq)
		if switchRR.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", label, switchRR.Code, switchRR.Body.String())
		}
		verifyWorktreeContent(t, worktreePath, "file.txt", want, label)
	}

	// Round 2
	roundTripSwitch("round2 Main", friendContent, mainThreadID)
	roundTripSwitch("round2 Fork B", choomContent, forkBID)
	roundTripSwitch("round2 Fork C", initialContent, forkCID)

	// Round 3
	roundTripSwitch("round3 Main", friendContent, mainThreadID)
	roundTripSwitch("round3 Fork B", choomContent, forkBID)
	roundTripSwitch("round3 Fork C", initialContent, forkCID)

	// Round 4 — reverse order: C → B → Main → C → B → Main
	roundTripSwitch("round4 Main", friendContent, mainThreadID)
	roundTripSwitch("round4 Fork B", choomContent, forkBID)
	roundTripSwitch("round4 Fork C", initialContent, forkCID)
	roundTripSwitch("round4 Fork B", choomContent, forkBID)
	roundTripSwitch("round4 Main", friendContent, mainThreadID)
}

// TestThreadSwitchRestoresForkCheckpoint verifies that switching back to a fork
// thread whose changes were committed (so it has no worktree stash) restores the
// fork's checkpoint commit rather than resetting to baseRef. Bug: handleThreadSwitch
// fell through to `reset --hard baseRef` for any thread without a stash hash, which
// discarded the fork's committed file state and left an empty diff.
func TestThreadSwitchRestoresForkCheckpoint(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	// Create worktree (simulating task start)
	worktreePath, _, err := s.executor.WorktreeMgr().CreateWorktree(projID, taskID, "task/"+taskID, "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	task, err := s.store.GetTask(projID, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	task.Branch = "task/" + taskID
	task.BaseRef = "master"
	if err := s.store.UpdateTask(task); err != nil {
		t.Fatalf("update task branch: %v", err)
	}

	// Commit hello.cs to the task branch so the fork's checkpoint contains it.
	csContent := "class Hello {}\n"
	if err := os.WriteFile(filepath.Join(worktreePath, "hello.cs"), []byte(csContent), 0o644); err != nil {
		t.Fatalf("write hello.cs: %v", err)
	}
	mustRunGit(t, worktreePath, "add", "hello.cs")
	mustRunGit(t, worktreePath, "commit", "-m", "Add hello.cs")
	commitHash := strings.TrimSpace(mustRunGitOutput(t, worktreePath, "rev-parse", "HEAD"))

	// Get the Main thread
	threads := mustJSON[[]map[string]any](t, testResponseAuth(t, s, testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)).Body)
	if len(threads) < 1 {
		t.Fatal("expected at least 1 thread")
	}
	mainThreadID := threads[0]["id"].(string)

	// Create an assistant message carrying the commit hash, then a later checkpoint
	// (user) message. GetLastCommitHashBeforeMessage uses created_at < cutoff, so the
	// commit hash must live on a message strictly before the checkpoint message.
	base := time.Now().UTC()
	assistantMsg := &types.TaskMessage{
		ID: "forkcheck-msg-assistant", TaskID: taskID, ThreadID: mainThreadID, Role: "assistant",
		Content: "done", GitCommitHash: commitHash, CreatedAt: base,
	}
	if err := s.store.CreateTaskMessage(assistantMsg); err != nil {
		t.Fatalf("create assistant msg: %v", err)
	}
	checkpointMsg := &types.TaskMessage{
		ID: "forkcheck-msg-checkpoint", TaskID: taskID, ThreadID: mainThreadID, Role: "user",
		Content: "add a counter to 7", CreatedAt: base.Add(time.Second),
	}
	if err := s.store.CreateTaskMessage(checkpointMsg); err != nil {
		t.Fatalf("create checkpoint msg: %v", err)
	}

	// Fork from the checkpoint message.
	forkData := mustJSON[map[string]any](t, testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/fork", map[string]any{
		"from_thread_id":  mainThreadID,
		"from_message_id": checkpointMsg.ID,
	})).Body)
	forkThreadID := forkData["id"].(string)
	forkThread, err := s.store.GetThread(forkThreadID)
	if err != nil {
		t.Fatalf("get fork thread: %v", err)
	}
	if forkThread.CheckpointCommitHash != commitHash {
		t.Fatalf("expected fork checkpoint_commit_hash=%s, got %s", commitHash, forkThread.CheckpointCommitHash)
	}
	if forkThread.WorktreeStashHash != "" {
		t.Fatal("expected fresh fork to have no worktree stash hash")
	}

	// The fork branch is checked out at the checkpoint commit with hello.cs present.
	verifyWorktreeContent(t, worktreePath, "hello.cs", csContent, "after fork")

	// Switch to Main (clean fork -> no stash is created).
	testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
		"thread_id": mainThreadID,
	}))
	// Main uses the task branch, which holds hello.cs (committed before the fork).
	// With no stash it force-checks out that branch HEAD, so hello.cs is present.
	verifyWorktreeContent(t, worktreePath, "hello.cs", csContent, "after switch to Main")

	// Switch back to the fork. It has no stash, so it must restore its checkpoint
	// commit (hello.cs present) instead of resetting to baseRef.
	testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
		"thread_id": forkThreadID,
	}))
	verifyWorktreeContent(t, worktreePath, "hello.cs", csContent, "after switch back to fork")
}

// TestSwitchToMainRestoresTaskBranch verifies that switching back to the Main
// thread (which has no dedicated git branch) after a fork restores the task's
// original branch rather than leaving the worktree on the fork's branch.
func TestSwitchToMainRestoresTaskBranch(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	worktreePath, _, err := s.executor.WorktreeMgr().CreateWorktree(projID, taskID, "task/"+taskID, "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	task, err := s.store.GetTask(projID, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	task.Branch = "task/" + taskID
	task.BaseRef = "master"
	if err := s.store.UpdateTask(task); err != nil {
		t.Fatalf("update task branch: %v", err)
	}

	// Commit hello.cs to the task branch.
	if err := os.WriteFile(filepath.Join(worktreePath, "hello.cs"), []byte("class Hello {}\n"), 0o644); err != nil {
		t.Fatalf("write hello.cs: %v", err)
	}
	mustRunGit(t, worktreePath, "add", "hello.cs")
	mustRunGit(t, worktreePath, "commit", "-m", "Add hello.cs")

	threads := mustJSON[[]map[string]any](t, testResponseAuth(t, s, testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)).Body)
	mainThreadID := threads[0]["id"].(string)

	base := time.Now().UTC()
	assistantMsg := &types.TaskMessage{
		ID: "switchmain-msg-assistant", TaskID: taskID, ThreadID: mainThreadID, Role: "assistant",
		Content: "done", GitCommitHash: strings.TrimSpace(mustRunGitOutput(t, worktreePath, "rev-parse", "HEAD")), CreatedAt: base,
	}
	if err := s.store.CreateTaskMessage(assistantMsg); err != nil {
		t.Fatalf("create assistant msg: %v", err)
	}
	checkpointMsg := &types.TaskMessage{
		ID: "switchmain-msg-checkpoint", TaskID: taskID, ThreadID: mainThreadID, Role: "user",
		Content: "add a counter to 7", CreatedAt: base.Add(time.Second),
	}
	if err := s.store.CreateTaskMessage(checkpointMsg); err != nil {
		t.Fatalf("create checkpoint msg: %v", err)
	}

	// Fork (becomes active on thread/<forkID> branch).
	forkData := mustJSON[map[string]any](t, testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/fork", map[string]any{
		"from_thread_id":  mainThreadID,
		"from_message_id": checkpointMsg.ID,
	})).Body)
	forkThreadID := forkData["id"].(string)
	if got := mustRunGitOutput(t, worktreePath, "branch", "--show-current"); got != "thread/"+forkThreadID {
		t.Fatalf("after fork, expected branch thread/%s, got %s", forkThreadID, got)
	}

	// Switch back to Main.
	testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
		"thread_id": mainThreadID,
	}))
	got := mustRunGitOutput(t, worktreePath, "branch", "--show-current")
	if got != "task/"+taskID {
		t.Fatalf("after switch to Main, expected branch task/%s, got %s", taskID, got)
	}
}

// TestSwitchToAdvancedForkPreservesCommittedWork verifies switching to a fork that
// committed work past its checkpoint does not roll it back to the checkpoint.
func TestSwitchToAdvancedForkPreservesCommittedWork(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	worktreePath, _, err := s.executor.WorktreeMgr().CreateWorktree(projID, taskID, "task/"+taskID, "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	task, err := s.store.GetTask(projID, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	task.Branch = "task/" + taskID
	task.BaseRef = "master"
	if err := s.store.UpdateTask(task); err != nil {
		t.Fatalf("update task branch: %v", err)
	}

	csContent := "class Hello {}\n"
	if err := os.WriteFile(filepath.Join(worktreePath, "hello.cs"), []byte(csContent), 0o644); err != nil {
		t.Fatalf("write hello.cs: %v", err)
	}
	mustRunGit(t, worktreePath, "add", "hello.cs")
	mustRunGit(t, worktreePath, "commit", "-m", "Add hello.cs")
	commitHash := strings.TrimSpace(mustRunGitOutput(t, worktreePath, "rev-parse", "HEAD"))

	threads := mustJSON[[]map[string]any](t, testResponseAuth(t, s, testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)).Body)
	mainThreadID := threads[0]["id"].(string)

	base := time.Now().UTC()
	assistantMsg := &types.TaskMessage{
		ID: "advfork-msg-assistant", TaskID: taskID, ThreadID: mainThreadID, Role: "assistant",
		Content: "done", GitCommitHash: commitHash, CreatedAt: base,
	}
	if err := s.store.CreateTaskMessage(assistantMsg); err != nil {
		t.Fatalf("create assistant msg: %v", err)
	}
	checkpointMsg := &types.TaskMessage{
		ID: "advfork-msg-checkpoint", TaskID: taskID, ThreadID: mainThreadID, Role: "user",
		Content: "add a counter to 7", CreatedAt: base.Add(time.Second),
	}
	if err := s.store.CreateTaskMessage(checkpointMsg); err != nil {
		t.Fatalf("create checkpoint msg: %v", err)
	}

	// Fork (becomes active on thread/<forkID> branch at the checkpoint commit).
	forkData := mustJSON[map[string]any](t, testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/fork", map[string]any{
		"from_thread_id":  mainThreadID,
		"from_message_id": checkpointMsg.ID,
	})).Body)
	forkThreadID := forkData["id"].(string)
	if got := mustRunGitOutput(t, worktreePath, "branch", "--show-current"); got != "thread/"+forkThreadID {
		t.Fatalf("after fork, expected branch thread/%s, got %s", forkThreadID, got)
	}

	// On the fork branch, commit counter work (simulating the fork doing its task
	// and committing). This advances the fork branch past its checkpoint.
	counterContent := csContent + "// counter to 7\n"
	if err := os.WriteFile(filepath.Join(worktreePath, "hello.cs"), []byte(counterContent), 0o644); err != nil {
		t.Fatalf("write counter: %v", err)
	}
	mustRunGit(t, worktreePath, "add", "hello.cs")
	mustRunGit(t, worktreePath, "commit", "-m", "Add counter to 7")
	forkAdvHash := strings.TrimSpace(mustRunGitOutput(t, worktreePath, "rev-parse", "HEAD"))

	// Switch to Main (fork is clean/committed -> no stash created).
	testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
		"thread_id": mainThreadID,
	}))

	// Switch back to the fork. Its committed work must be preserved, not reset to
	// the checkpoint (which would strip the counter).
	testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
		"thread_id": forkThreadID,
	}))
	if got := mustRunGitOutput(t, worktreePath, "branch", "--show-current"); got != "thread/"+forkThreadID {
		t.Fatalf("after switch back to fork, expected branch thread/%s, got %s", forkThreadID, got)
	}
	if got := strings.TrimSpace(mustRunGitOutput(t, worktreePath, "rev-parse", "HEAD")); got != forkAdvHash {
		t.Fatalf("expected fork branch HEAD %s after switch back, got %s (work was rolled back to checkpoint)", forkAdvHash, got)
	}
	verifyWorktreeContent(t, worktreePath, "hello.cs", counterContent, "after switch back to advanced fork")
}

// TestSwitchToMainRecoversSlugBranchPreservesFork verifies switching to Main on a
// titled task (slug-named Main branch) and back to the fork preserves committed work.
func TestSwitchToMainRecoversSlugBranchPreservesFork(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Hey")

	slugBranch := "task/hey-zimxpq"
	worktreePath, _, err := s.executor.WorktreeMgr().CreateWorktree(projID, taskID, slugBranch, "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	task, err := s.store.GetTask(projID, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	task.Title = "Hey"
	task.Branch = slugBranch
	task.BaseRef = "master"
	if err := s.store.UpdateTask(task); err != nil {
		t.Fatalf("update task: %v", err)
	}

	csContent := "class Hello {}\n"
	if err := os.WriteFile(filepath.Join(worktreePath, "hello.cs"), []byte(csContent), 0o644); err != nil {
		t.Fatalf("write hello.cs: %v", err)
	}
	mustRunGit(t, worktreePath, "add", "hello.cs")
	mustRunGit(t, worktreePath, "commit", "-m", "Add hello.cs")
	commitHash := strings.TrimSpace(mustRunGitOutput(t, worktreePath, "rev-parse", "HEAD"))

	threads := mustJSON[[]map[string]any](t, testResponseAuth(t, s, testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)).Body)
	mainThreadID := threads[0]["id"].(string)

	base := time.Now().UTC()
	assistantMsg := &types.TaskMessage{
		ID: "slugmsg-assistant", TaskID: taskID, ThreadID: mainThreadID, Role: "assistant",
		Content: "done", GitCommitHash: commitHash, CreatedAt: base,
	}
	if err := s.store.CreateTaskMessage(assistantMsg); err != nil {
		t.Fatalf("create assistant msg: %v", err)
	}
	checkpointMsg := &types.TaskMessage{
		ID: "slugmsg-checkpoint", TaskID: taskID, ThreadID: mainThreadID, Role: "user",
		Content: "add a counter to 7", CreatedAt: base.Add(time.Second),
	}
	if err := s.store.CreateTaskMessage(checkpointMsg); err != nil {
		t.Fatalf("create checkpoint msg: %v", err)
	}

	forkData := mustJSON[map[string]any](t, testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/fork", map[string]any{
		"from_thread_id":  mainThreadID,
		"from_message_id": checkpointMsg.ID,
	})).Body)
	forkThreadID := forkData["id"].(string)
	if got := mustRunGitOutput(t, worktreePath, "branch", "--show-current"); got != "thread/"+forkThreadID {
		t.Fatalf("after fork, expected branch thread/%s, got %s", forkThreadID, got)
	}

	// Commit counter work on the fork branch.
	counterContent := csContent + "// counter to 7\n"
	if err := os.WriteFile(filepath.Join(worktreePath, "hello.cs"), []byte(counterContent), 0o644); err != nil {
		t.Fatalf("write counter: %v", err)
	}
	mustRunGit(t, worktreePath, "add", "hello.cs")
	mustRunGit(t, worktreePath, "commit", "-m", "Add counter to 7")
	forkAdvHash := strings.TrimSpace(mustRunGitOutput(t, worktreePath, "rev-parse", "HEAD"))

	// Switch to Main. Its git_branch is empty and task/<taskID> does not exist, so
	// ResolveMainBranch must recover the slug Main branch task/hey-zimxpq.
	testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
		"thread_id": mainThreadID,
	}))
	if got := mustRunGitOutput(t, worktreePath, "branch", "--show-current"); got != slugBranch {
		t.Fatalf("after switch to Main, expected recovered slug branch %s, got %s", slugBranch, got)
	}

	// Switch back to the fork: its committed work must be preserved (HEAD = forkAdvHash).
	testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
		"thread_id": forkThreadID,
	}))
	if got := strings.TrimSpace(mustRunGitOutput(t, worktreePath, "rev-parse", "HEAD")); got != forkAdvHash {
		t.Fatalf("expected fork HEAD %s after switch back, got %s (fork work lost)", forkAdvHash, got)
	}
	verifyWorktreeContent(t, worktreePath, "hello.cs", counterContent, "after switch back to fork")
}

// setupForkWithCheckpointStash creates a task + fork thread and simulates the fork
// doing uncommitted work: it writes a counter to hello.cs, checkpoint-stashes it, and
// stores the stash hash on the fork's assistant message.
func setupForkWithCheckpointStash(t *testing.T, s *Server, projID, taskID string) (worktreePath, forkThreadID, stashHash, counterContent string) {
	t.Helper()
	var err error
	worktreePath, _, err = s.executor.WorktreeMgr().CreateWorktree(projID, taskID, "task/"+taskID, "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	task, err := s.store.GetTask(projID, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	task.Branch = "task/" + taskID
	task.BaseRef = "master"
	if err := s.store.UpdateTask(task); err != nil {
		t.Fatalf("update task: %v", err)
	}

	csContent := "class Hello {}\n"
	if err := os.WriteFile(filepath.Join(worktreePath, "hello.cs"), []byte(csContent), 0o644); err != nil {
		t.Fatalf("write hello.cs: %v", err)
	}
	mustRunGit(t, worktreePath, "add", "hello.cs")
	mustRunGit(t, worktreePath, "commit", "-m", "Add hello.cs")
	commitHash := strings.TrimSpace(mustRunGitOutput(t, worktreePath, "rev-parse", "HEAD"))

	threads := mustJSON[[]map[string]any](t, testResponseAuth(t, s, testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)).Body)
	mainThreadID := threads[0]["id"].(string)

	base := time.Now().UTC()
	assistantMsg := &types.TaskMessage{
		ID: "ckpt-msg-assistant", TaskID: taskID, ThreadID: mainThreadID, Role: "assistant",
		Content: "done", GitCommitHash: commitHash, CreatedAt: base,
	}
	if err := s.store.CreateTaskMessage(assistantMsg); err != nil {
		t.Fatalf("create assistant msg: %v", err)
	}
	checkpointMsg := &types.TaskMessage{
		ID: "ckpt-msg-checkpoint", TaskID: taskID, ThreadID: mainThreadID, Role: "user",
		Content: "add a counter to 7", CreatedAt: base.Add(time.Second),
	}
	if err := s.store.CreateTaskMessage(checkpointMsg); err != nil {
		t.Fatalf("create checkpoint msg: %v", err)
	}

	// Fork becomes active on thread/<forkID> at the checkpoint.
	forkData := mustJSON[map[string]any](t, testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/fork", map[string]any{
		"from_thread_id":  mainThreadID,
		"from_message_id": checkpointMsg.ID,
	})).Body)
	forkThreadID = forkData["id"].(string)
	if got := mustRunGitOutput(t, worktreePath, "branch", "--show-current"); got != "thread/"+forkThreadID {
		t.Fatalf("after fork, expected branch thread/%s, got %s", forkThreadID, got)
	}

	// Fork does uncommitted work: write counter to hello.cs (do NOT commit).
	counterContent = csContent + "// counter to 7\n"
	if err := os.WriteFile(filepath.Join(worktreePath, "hello.cs"), []byte(counterContent), 0o644); err != nil {
		t.Fatalf("write counter: %v", err)
	}
	// Simulate createCheckpoint: stage + stash create/store, keep worktree dirty.
	mustRunGit(t, worktreePath, "add", "-A")
	stashHash = strings.TrimSpace(mustRunGitOutput(t, worktreePath, "stash", "create"))
	if stashHash == "" {
		t.Fatal("stash create returned empty")
	}
	mustRunGit(t, worktreePath, "stash", "store", "-m", "On thread/"+forkThreadID+": kanban-checkpoint-"+taskID, stashHash)
	forkAssistant := &types.TaskMessage{
		ID: "ckpt-msg-forkassistant", TaskID: taskID, ThreadID: forkThreadID, Role: "assistant",
		Content: "done counter", GitCommitHash: stashHash, CreatedAt: base.Add(2 * time.Second),
	}
	if err := s.store.CreateTaskMessage(forkAssistant); err != nil {
		t.Fatalf("create fork assistant msg: %v", err)
	}
	return
}

// TestSameThreadSwitchDoesNotWipeUncommittedWork verifies a same-thread switch (page
// load with a thread URL param) does not wipe the thread's uncommitted work.
func TestSameThreadSwitchDoesNotWipeUncommittedWork(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	worktreePath, forkThreadID, _, counterContent := setupForkWithCheckpointStash(t, s, projID, taskID)

	// Verify the fork's uncommitted counter work is in the worktree before the switch.
	verifyWorktreeContent(t, worktreePath, "hello.cs", counterContent, "before same-thread switch")

	// Simulate a page load: switch to the fork, which is already the active thread.
	testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
		"thread_id": forkThreadID,
	}))

	// The counter must still be present — the switch must NOT have wiped the worktree.
	verifyWorktreeContent(t, worktreePath, "hello.cs", counterContent, "after same-thread switch")
}

// TestSwitchRestoresForkCheckpointStash verifies a fork with an empty worktree stash
// gets its last checkpoint stash re-applied on switch, restoring its real content.
func TestSwitchRestoresForkCheckpointStash(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	worktreePath, forkThreadID, _, counterContent := setupForkWithCheckpointStash(t, s, projID, taskID)

	// Simulate the old bug's damage: the fork's uncommitted work was wiped, leaving the
	// worktree clean at the branch checkpoint (counter lives only in the checkpoint stash).
	mustRunGit(t, worktreePath, "checkout", "-f", "HEAD")
	mustRunGit(t, worktreePath, "clean", "-fd")

	// Get Main thread id.
	threads := mustJSON[[]map[string]any](t, testResponseAuth(t, s, testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)).Body)
	mainThreadID := threads[0]["id"].(string)

	// Switch to Main first (fork worktree is clean, so no switch-away stash is saved).
	testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
		"thread_id": mainThreadID,
	}))
	// Switch back to the fork: its checkpoint stash must be re-applied.
	testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]any{
		"thread_id": forkThreadID,
	}))
	if got := mustRunGitOutput(t, worktreePath, "branch", "--show-current"); got != "thread/"+forkThreadID {
		t.Fatalf("after switch back, expected branch thread/%s, got %s", forkThreadID, got)
	}
	verifyWorktreeContent(t, worktreePath, "hello.cs", counterContent, "after switching back to fork (checkpoint stash re-applied)")
}
