package server

import (
	"net/http"
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
