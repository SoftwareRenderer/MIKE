package exec

import (
	"testing"
	"time"

	"kanban/backend/types"
)

// TestDeletedMessagesNotSentToLLM verifies that when messages are deleted
// from the DB, the in-memory conversation is re-synced before the next LLM call.
// This is a regression test for: "When chat messages are deleted, why are they
// still being sent to the LLM?"
func TestDeletedMessagesNotSentToLLM(t *testing.T) {
	te := newTestExecutor(t)
	now := time.Now().UTC()

	proj := &types.Project{ID: "proj-del", Name: "Delete Test", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}
	if err := te.store.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	taskID := "task-del"
	task := &types.Task{
		ID: taskID, ProjectID: proj.ID, Title: "Delete Test", Prompt: "Test prompt",
		Status: types.StatusInProgress, ExecutionState: types.ExecStateRunning,
		LlamaServerURL: "http://test:8080", ModelName: "test-model",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := te.store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	threadID := types.GenerateThreadID()
	if err := te.store.CreateThread(&types.TaskThread{
		ID: threadID, TaskID: taskID, Name: "Main", IsActive: true, CreatedAt: now,
	}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	task.CurrentThreadID = threadID
	_ = te.store.UpdateTask(task)

	// Create messages in the DB: system, user1, assistant1, user2, assistant2
	msg1 := &types.TaskMessage{ID: "msg-user1", TaskID: taskID, ThreadID: threadID, Role: "user", Content: "First user message", CreatedAt: now}
	msg2 := &types.TaskMessage{ID: "msg-assist1", TaskID: taskID, ThreadID: threadID, Role: "assistant", Content: "First assistant response", CreatedAt: now.Add(1 * time.Second)}
	msg3 := &types.TaskMessage{ID: "msg-user2", TaskID: taskID, ThreadID: threadID, Role: "user", Content: "Second user message", CreatedAt: now.Add(2 * time.Second)}
	msg4 := &types.TaskMessage{ID: "msg-assist2", TaskID: taskID, ThreadID: threadID, Role: "assistant", Content: "Second assistant response", CreatedAt: now.Add(3 * time.Second)}

	for _, m := range []*types.TaskMessage{msg1, msg2, msg3, msg4} {
		if err := te.store.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	// Build initial messages from DB (simulates executor startup)
	taskMsgs, _ := te.store.GetTaskMessages(taskID, threadID)
	messages := buildMessagesFromDB(task, taskMsgs, "/worktree", "Delete Test", false)

	// Verify: should have system + 4 messages = 5
	if len(messages) != 5 {
		t.Fatalf("expected 5 messages (system + 4), got %d", len(messages))
	}

	// Simulate user deleting msg3 (user2) and everything after it (msg4)
	if _, err := te.store.DeleteTaskMessagesAfter(taskID, "msg-user2", threadID); err != nil {
		t.Fatalf("delete messages: %v", err)
	}

	// Verify DB state: should have system + 2 messages = 3
	dbMsgs, _ := te.store.GetTaskMessages(taskID, threadID)
	if len(dbMsgs) != 2 {
		t.Fatalf("expected 2 messages in DB after delete, got %d", len(dbMsgs))
	}

	// BUG: in-memory messages still has 5 entries (including deleted ones)
	if len(messages) != 5 {
		t.Fatalf("expected stale in-memory to have 5 messages, got %d", len(messages))
	}

	// Simulate a new user message arriving (non-regenerate)
	// WITHOUT the fix: the new message is appended to stale in-memory,
	// so the LLM sees deleted messages.
	//
	// WITH the fix: in-memory is re-synced from DB before appending,
	// so deleted messages are excluded.

	// Simulate the fix: re-sync from DB
	taskMsgs, _ = te.store.GetTaskMessages(taskID, threadID)
	messages = buildMessagesFromDB(task, taskMsgs, "/worktree", "Delete Test", false)

	// Append new user message
	newMsg := types.ChatMessage{Role: "user", Content: "Third user message"}
	messages = append(messages, newMsg)

	// Verify: should have system + 2 (remaining from DB) + 1 (new) = 4
	if len(messages) != 4 {
		t.Errorf("expected 4 messages after sync+append, got %d", len(messages))
		for i, m := range messages {
			t.Logf("  [%d] role=%s content=%q", i, m.Role, m.Content)
		}
	}

	// Verify deleted messages are NOT present
	for _, m := range messages {
		if m.Content == "Second user message" {
			t.Error("deleted message 'Second user message' should not be in conversation")
		}
		if m.Content == "Second assistant response" {
			t.Error("deleted message 'Second assistant response' should not be in conversation")
		}
	}

	// Verify remaining messages ARE present
	contents := make(map[string]bool)
	for _, m := range messages {
		contents[m.Content] = true
	}
	if !contents["First user message"] {
		t.Error("expected 'First user message' to be present")
	}
	if !contents["First assistant response"] {
		t.Error("expected 'First assistant response' to be present")
	}
	if !contents["Third user message"] {
		t.Error("expected 'Third user message' to be present")
	}
}

// TestDeletedMessagesWithRegenerate verifies that the regenerate path
// (truncateForRegenerate) correctly handles deleted messages.
func TestDeletedMessagesWithRegenerate(t *testing.T) {
	te := newTestExecutor(t)
	now := time.Now().UTC()

	proj := &types.Project{ID: "proj-regen", Name: "Regen Test", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}
	if err := te.store.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	taskID := "task-regen"
	task := &types.Task{
		ID: taskID, ProjectID: proj.ID, Title: "Regen Test", Prompt: "Test prompt",
		Status: types.StatusInProgress, ExecutionState: types.ExecStateRunning,
		LlamaServerURL: "http://test:8080", ModelName: "test-model",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := te.store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	threadID := types.GenerateThreadID()
	if err := te.store.CreateThread(&types.TaskThread{
		ID: threadID, TaskID: taskID, Name: "Main", IsActive: true, CreatedAt: now,
	}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	task.CurrentThreadID = threadID
	_ = te.store.UpdateTask(task)

	// Create messages: user1, assistant1, user2, assistant2
	msg1 := &types.TaskMessage{ID: "msg-user1", TaskID: taskID, ThreadID: threadID, Role: "user", Content: "First user message", CreatedAt: now}
	msg2 := &types.TaskMessage{ID: "msg-assist1", TaskID: taskID, ThreadID: threadID, Role: "assistant", Content: "First assistant response", CreatedAt: now.Add(1 * time.Second)}
	msg3 := &types.TaskMessage{ID: "msg-user2", TaskID: taskID, ThreadID: threadID, Role: "user", Content: "Second user message", CreatedAt: now.Add(2 * time.Second)}
	msg4 := &types.TaskMessage{ID: "msg-assist2", TaskID: taskID, ThreadID: threadID, Role: "assistant", Content: "Second assistant response", CreatedAt: now.Add(3 * time.Second)}

	for _, m := range []*types.TaskMessage{msg1, msg2, msg3, msg4} {
		if err := te.store.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	// Build initial messages from DB
	taskMsgs, _ := te.store.GetTaskMessages(taskID, threadID)
	messages := buildMessagesFromDB(task, taskMsgs, "/worktree", "Regen Test", false)

	// Simulate regenerate: DB deletes msg3+msg4, in-memory truncates
	_, _ = te.store.DeleteTaskMessagesAfter(taskID, "msg-user2", threadID)
	messages = truncateForRegenerate(messages)

	// Append re-sent user message
	newMsg := types.ChatMessage{Role: "user", Content: "Second user message (re-sent)"}
	messages = append(messages, newMsg)

	// Verify: system + user1 + assistant1 + re-sent user = 4
	if len(messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(messages))
	}

	// Verify no stale messages
	for _, m := range messages {
		if m.Content == "Second assistant response" {
			t.Error("deleted message should not be in conversation")
		}
	}
}

// TestDeletedSingleMessageNotSentToLLM verifies that deleting a single message
// (e.g., dismissing an error) is also synced.
func TestDeletedSingleMessageNotSentToLLM(t *testing.T) {
	te := newTestExecutor(t)
	now := time.Now().UTC()

	proj := &types.Project{ID: "proj-single", Name: "Single Delete Test", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}
	if err := te.store.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	taskID := "task-single"
	task := &types.Task{
		ID: taskID, ProjectID: proj.ID, Title: "Single Delete Test", Prompt: "Test prompt",
		Status: types.StatusInProgress, ExecutionState: types.ExecStateRunning,
		LlamaServerURL: "http://test:8080", ModelName: "test-model",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := te.store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	threadID := types.GenerateThreadID()
	if err := te.store.CreateThread(&types.TaskThread{
		ID: threadID, TaskID: taskID, Name: "Main", IsActive: true, CreatedAt: now,
	}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	task.CurrentThreadID = threadID
	_ = te.store.UpdateTask(task)

	// Create messages: user1, error (to dismiss), assistant1
	msg1 := &types.TaskMessage{ID: "msg-user1", TaskID: taskID, ThreadID: threadID, Role: "user", Content: "User message", CreatedAt: now}
	msg2 := &types.TaskMessage{ID: "msg-error", TaskID: taskID, ThreadID: threadID, Role: "error", Content: "[timeout] Connection error", CreatedAt: now.Add(1 * time.Second)}
	msg3 := &types.TaskMessage{ID: "msg-assist1", TaskID: taskID, ThreadID: threadID, Role: "assistant", Content: "Assistant response", CreatedAt: now.Add(2 * time.Second)}

	for _, m := range []*types.TaskMessage{msg1, msg2, msg3} {
		if err := te.store.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	// Simulate user dismissing the error message
	if err := te.store.DeleteTaskMessageByID("msg-error"); err != nil {
		t.Fatalf("delete single message: %v", err)
	}

	// Re-sync from DB (the fix)
	taskMsgs, _ := te.store.GetTaskMessages(taskID, threadID)
	messages := buildMessagesFromDB(task, taskMsgs, "/worktree", "Single Delete Test", false)

	// Verify: system + user + assistant = 3 (error removed)
	if len(messages) != 3 {
		t.Fatalf("expected 3 messages after sync, got %d", len(messages))
	}

	// Verify error is not present
	for _, m := range messages {
		if m.Role == "error" {
			t.Error("deleted error message should not be in conversation")
		}
	}
}
