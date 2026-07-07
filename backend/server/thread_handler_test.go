package server

import (
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
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

	// Create a message
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

	// Fork
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

	// Create a task
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
