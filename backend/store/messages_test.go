package store

import (
	"kanban/backend/types"
	"testing"
	"time"
)

func TestCreateAndGetTaskMessages(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	msgs := []*types.TaskMessage{
		newMessage("msg-1", "task-1", "user", "hello"),
		newMessage("msg-2", "task-1", "assistant", "hi there"),
		newMessage("msg-3", "task-1", "user", "how are you"),
	}
	for _, m := range msgs {
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	got, err := s.GetTaskMessages("task-1", "")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got))
	}
	// Should be ordered by created_at
	if got[0].Content != "hello" || got[1].Content != "hi there" || got[2].Content != "how are you" {
		t.Fatalf("unexpected order: %v", got)
	}
}

func TestGetTaskMessagesByThread(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Create threads
	th1 := newThread("thread-1", "task-1", "Main")
	th2 := newThread("thread-2", "task-1", "Fork #1")
	if err := s.CreateThread(th1); err != nil {
		t.Fatalf("create thread 1: %v", err)
	}
	if err := s.CreateThread(th2); err != nil {
		t.Fatalf("create thread 2: %v", err)
	}

	// Messages in thread-1
	m1 := newMessage("msg-1", "task-1", "user", "msg in thread 1")
	m1.ThreadID = "thread-1"
	// Messages in thread-2
	m2 := newMessage("msg-2", "task-1", "user", "msg in thread 2")
	m2.ThreadID = "thread-2"
	// Message without thread
	m3 := newMessage("msg-3", "task-1", "user", "no thread")

	for _, m := range []*types.TaskMessage{m1, m2, m3} {
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	// Get messages for thread-1
	thread1Msgs, err := s.GetTaskMessages("task-1", "thread-1")
	if err != nil {
		t.Fatalf("get messages for thread-1: %v", err)
	}
	if len(thread1Msgs) != 1 || thread1Msgs[0].Content != "msg in thread 1" {
		t.Fatalf("unexpected thread-1 messages: %+v", thread1Msgs)
	}

	// Get all messages (no thread filter)
	allMsgs, err := s.GetTaskMessages("task-1", "")
	if err != nil {
		t.Fatalf("get all messages: %v", err)
	}
	if len(allMsgs) != 3 {
		t.Fatalf("expected 3 messages total, got %d", len(allMsgs))
	}
}

func TestDeleteTaskMessagesAfter(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Create messages with explicit timestamps to ensure distinct created_at values
	baseTime := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	msgs := []string{"msg-1", "msg-2", "msg-3", "msg-4"}
	for i, id := range msgs {
		m := newMessage(id, "task-1", "user", "content "+id)
		m.CreatedAt = baseTime.Add(time.Duration(i+1) * time.Second)
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	// Delete after msg-2 (should remove msg-2, msg-3, msg-4)
	if err := s.DeleteTaskMessagesAfter("task-1", "msg-2", ""); err != nil {
		t.Fatalf("delete messages after: %v", err)
	}

	remaining, err := s.GetTaskMessages("task-1", "")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != "msg-1" {
		t.Fatalf("expected only msg-1, got %d messages", len(remaining))
	}
}

func TestDeleteTaskMessagesAfterByThread(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	th1 := newThread("thread-1", "task-1", "Main")
	th2 := newThread("thread-2", "task-1", "Fork")
	if err := s.CreateThread(th1); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := s.CreateThread(th2); err != nil {
		t.Fatalf("create thread: %v", err)
	}

	baseTime := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	// Messages in thread-1
	m1 := newMessage("m1", "task-1", "user", "t1 msg 1")
	m1.ThreadID = "thread-1"
	m1.CreatedAt = baseTime
	m2 := newMessage("m2", "task-1", "assistant", "t1 msg 2")
	m2.ThreadID = "thread-1"
	m2.CreatedAt = baseTime.Add(1 * time.Second)
	// Messages in thread-2
	m3 := newMessage("m3", "task-1", "user", "t2 msg 1")
	m3.ThreadID = "thread-2"
	m3.CreatedAt = baseTime.Add(2 * time.Second)
	m4 := newMessage("m4", "task-1", "assistant", "t2 msg 2")
	m4.ThreadID = "thread-2"
	m4.CreatedAt = baseTime.Add(3 * time.Second)

	for _, m := range []*types.TaskMessage{m1, m2, m3, m4} {
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	// Delete after m1 in thread-1 only
	if err := s.DeleteTaskMessagesAfter("task-1", "m1", "thread-1"); err != nil {
		t.Fatalf("delete messages after: %v", err)
	}

	// Thread-1 should be empty
	t1Msgs, _ := s.GetTaskMessages("task-1", "thread-1")
	if len(t1Msgs) != 0 {
		t.Fatalf("expected 0 messages in thread-1, got %d", len(t1Msgs))
	}
	// Thread-2 should be untouched
	t2Msgs, _ := s.GetTaskMessages("task-1", "thread-2")
	if len(t2Msgs) != 2 {
		t.Fatalf("expected 2 messages in thread-2, got %d", len(t2Msgs))
	}
}

func TestUpdateTaskMessage(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	msg := newMessage("msg-1", "task-1", "user", "original content")
	if err := s.CreateTaskMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	if err := s.UpdateTaskMessage("msg-1", "edited content"); err != nil {
		t.Fatalf("update message: %v", err)
	}

	got, err := s.GetTaskMessages("task-1", "")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(got) != 1 || got[0].Content != "edited content" {
		t.Fatalf("unexpected content: %s", got[0].Content)
	}
}

func TestUpdateTaskMessageToolResult(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Create a tool_call message with empty tool_result (pending)
	msg := &types.TaskMessage{
		ID:        "tc-1",
		TaskID:    "task-1",
		Role:      "tool_call",
		Content:   "",
		ToolCalls: `{"tool_id":"abc","tool_name":"bash","args":{"command":"sleep 10"}}`,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateTaskMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	// Verify tool_result is empty
	got, err := s.GetTaskMessages("task-1", "")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(got) != 1 || got[0].ToolResult != "" {
		t.Fatalf("expected empty tool_result, got: %s", got[0].ToolResult)
	}

	// Update with result
	resultJSON := `{"output":"done","success":true,"duration":10000}`
	if err := s.UpdateTaskMessageToolResult("tc-1", resultJSON); err != nil {
		t.Fatalf("update tool result: %v", err)
	}

	// Verify tool_result is set
	got, err = s.GetTaskMessages("task-1", "")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(got) != 1 || got[0].ToolResult != resultJSON {
		t.Fatalf("unexpected tool_result: %s", got[0].ToolResult)
	}
}

func TestGetLastUserMessageInThread(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	th := newThread("thread-1", "task-1", "Main")
	if err := s.CreateThread(th); err != nil {
		t.Fatalf("create thread: %v", err)
	}

	baseTime := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	msgs := []*types.TaskMessage{
		func() *types.TaskMessage {
			m := newMessage("m1", "task-1", "user", "first")
			m.ThreadID = "thread-1"
			m.CreatedAt = baseTime
			return m
		}(),
		func() *types.TaskMessage {
			m := newMessage("m2", "task-1", "assistant", "reply")
			m.ThreadID = "thread-1"
			m.CreatedAt = baseTime.Add(1 * time.Second)
			return m
		}(),
		func() *types.TaskMessage {
			m := newMessage("m3", "task-1", "user", "second")
			m.ThreadID = "thread-1"
			m.CreatedAt = baseTime.Add(2 * time.Second)
			return m
		}(),
	}
	for _, m := range msgs {
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	lastID, err := s.GetLastUserMessageInThread("thread-1")
	if err != nil {
		t.Fatalf("get last user message: %v", err)
	}
	if lastID != "m3" {
		t.Fatalf("expected m3, got %s", lastID)
	}
}

func TestGetMessageCreatedAt(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	createdAt := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	msg := newMessage("msg-1", "task-1", "user", "hello")
	msg.CreatedAt = createdAt
	if err := s.CreateTaskMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	got, err := s.GetMessageCreatedAt("msg-1")
	if err != nil {
		t.Fatalf("get message created_at: %v", err)
	}
	if got == nil || !got.Equal(createdAt) {
		t.Fatalf("created_at mismatch: %v", got)
	}
}

func TestMessageTokenFields(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	msg := newMessage("msg-1", "task-1", "assistant", "response")
	msg.ModelName = "test-model"
	msg.ProviderName = "test-provider"
	msg.PromptTokens = 100
	msg.PredictedTokens = 50
	msg.CacheTokens = 20
	msg.PromptMs = 500
	msg.PredictedMs = 2000
	msg.Duration = 2500
	if err := s.CreateTaskMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	got, err := s.GetTaskMessages("task-1", "")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	m := got[0]
	if m.PromptTokens != 100 || m.PredictedTokens != 50 || m.CacheTokens != 20 {
		t.Fatalf("token counts mismatch: prompt=%d, predicted=%d, cache=%d", m.PromptTokens, m.PredictedTokens, m.CacheTokens)
	}
	if m.PromptMs != 500 || m.PredictedMs != 2000 || m.Duration != 2500 {
		t.Fatalf("timing mismatch: promptMs=%d, predictedMs=%d, duration=%d", m.PromptMs, m.PredictedMs, m.Duration)
	}
	if m.ModelName != "test-model" || m.ProviderName != "test-provider" {
		t.Fatalf("model/provider mismatch: %s/%s", m.ModelName, m.ProviderName)
	}
}

func TestMessageLoopHintRoundTrip(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	hint := newMessage("hint-1", "task-1", "user", "You repeated yourself in your last response.")
	hint.IsLoopHint = true
	if err := s.CreateTaskMessage(hint); err != nil {
		t.Fatalf("create hint message: %v", err)
	}

	got, err := s.GetTaskMessages("task-1", "")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}
	if !got[0].IsLoopHint {
		t.Fatalf("expected IsLoopHint=true to round-trip, got false")
	}

	single, err := s.GetTaskMessageByID("hint-1")
	if err != nil {
		t.Fatalf("get message by id: %v", err)
	}
	if !single.IsLoopHint {
		t.Fatalf("expected IsLoopHint=true via GetTaskMessageByID, got false")
	}
}

// TestGetTaskMessagesByThreadEmptyTaskID verifies that a thread-scoped lookup with
// an empty taskID still returns the thread's messages. This is required by
// getLastCommitHashInThread, which looks up remaining commit hashes by thread alone.
// Regression: previously the query filtered by task_id = ” and returned nothing,
// so surviving checkpoint messages were never found during delete rollback.
func TestGetTaskMessagesByThreadEmptyTaskID(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	th := newThread("thread-1", "task-1", "Main")
	if err := s.CreateThread(th); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	m1 := newMessage("msg-1", "task-1", "user", "msg 1")
	m1.ThreadID = "thread-1"
	if err := s.CreateTaskMessage(m1); err != nil {
		t.Fatalf("create message: %v", err)
	}

	got, err := s.GetTaskMessages("", "thread-1")
	if err != nil {
		t.Fatalf("get messages by thread with empty taskID: %v", err)
	}
	if len(got) != 1 || got[0].ID != "msg-1" {
		t.Fatalf("expected msg-1 for thread-1 with empty taskID, got %+v", got)
	}
}
