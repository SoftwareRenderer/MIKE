package server

import (
	"net/http"
	"testing"
)

func TestThreadList(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Test task")

	req := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	threads := mustJSON[[]map[string]interface{}](t, rr.Body)
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
	listRR := testResponse(t, s, listReq)
	threads := mustJSON[[]map[string]interface{}](t, listRR.Body)
	if len(threads) < 1 {
		t.Fatal("expected at least 1 thread")
	}
	mainThreadID := threads[0]["id"].(string)

	// Create a message first (to have a checkpoint)
	createReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]interface{}{
		"role":    "user",
		"content": "Message to fork from",
	})
	createRR := testResponse(t, s, createReq)
	msg := mustJSON[map[string]interface{}](t, createRR.Body)
	msgID := msg["id"].(string)

	// Fork from this message
	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/fork", map[string]interface{}{
		"from_thread_id":  mainThreadID,
		"from_message_id": msgID,
	})
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]interface{}](t, rr.Body)
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
	taskRR := testResponse(t, s, taskReq)
	task := mustJSON[map[string]interface{}](t, taskRR.Body)
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
	listRR := testResponse(t, s, listReq)
	threads := mustJSON[[]map[string]interface{}](t, listRR.Body)
	if len(threads) < 1 {
		t.Fatal("expected at least 1 thread")
	}
	mainThreadID := threads[0]["id"].(string)

	// Create a message
	createReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]interface{}{
		"role":    "user",
		"content": "Message for fork",
	})
	createRR := testResponse(t, s, createReq)
	msg := mustJSON[map[string]interface{}](t, createRR.Body)
	msgID := msg["id"].(string)

	// Fork to create a second thread
	forkReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/fork", map[string]interface{}{
		"from_thread_id":  mainThreadID,
		"from_message_id": msgID,
	})
	forkRR := testResponse(t, s, forkReq)
	fork := mustJSON[map[string]interface{}](t, forkRR.Body)
	forkThreadID := fork["id"].(string)

	// Switch back to Main
	switchReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]interface{}{
		"thread_id": mainThreadID,
	})
	switchRR := testResponse(t, s, switchReq)
	if switchRR.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", switchRR.Code, switchRR.Body.String())
	}

	// Verify task's current_thread_id is updated to Main
	taskReq := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID, nil)
	taskRR := testResponse(t, s, taskReq)
	task := mustJSON[map[string]interface{}](t, taskRR.Body)
	if task["current_thread_id"] != mainThreadID {
		t.Fatalf("expected current_thread_id=%s, got %s", mainThreadID, task["current_thread_id"])
	}

	// Verify thread list shows correct active state
	listReq2 := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/threads", nil)
	listRR2 := testResponse(t, s, listReq2)
	threads2 := mustJSON[[]map[string]interface{}](t, listRR2.Body)

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

	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/switch", map[string]interface{}{
		"thread_id": "nonexistent-thread",
	})
	rr := testResponse(t, s, req)
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
	listRR := testResponse(t, s, listReq)
	threads := mustJSON[[]map[string]interface{}](t, listRR.Body)
	if len(threads) < 1 {
		t.Fatal("expected at least 1 thread")
	}
	mainThreadID := threads[0]["id"].(string)

	// Create a message to fork from
	createReq2 := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]interface{}{
		"role":    "user",
		"content": "Fork point",
	})
	createRR2 := testResponse(t, s, createReq2)
	msg := mustJSON[map[string]interface{}](t, createRR2.Body)
	msgID := msg["id"].(string)

	// Fork
	forkReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/threads/fork", map[string]interface{}{
		"from_thread_id":  mainThreadID,
		"from_message_id": msgID,
	})
	forkRR := testResponse(t, s, forkReq)
	fork := mustJSON[map[string]interface{}](t, forkRR.Body)
	forkThreadID := fork["id"].(string)

	// Create a message in the fork thread
	createReq3 := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", map[string]interface{}{
		"role":    "user",
		"content": "Message in Fork",
	})
	testResponse(t, s, createReq3)

	// Get messages — should be scoped to current (fork) thread
	listMsgReq := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/messages", nil)
	listMsgRR := testResponse(t, s, listMsgReq)
	msgs := mustJSON[[]map[string]interface{}](t, listMsgRR.Body)

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
