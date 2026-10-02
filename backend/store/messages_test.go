package store

import (
	"mike/backend/types"
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
	if _, err := s.DeleteTaskMessagesAfter("task-1", "msg-2", ""); err != nil {
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
	if _, err := s.DeleteTaskMessagesAfter("task-1", "m1", "thread-1"); err != nil {
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

func TestMessageSeverityRoundTrip(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	info := newMessage("info-1", "task-1", "system", "Synced main into task branch (no conflicts).")
	info.Severity = "info"
	if err := s.CreateTaskMessage(info); err != nil {
		t.Fatalf("create info message: %v", err)
	}
	// Default (no severity) system message stays empty.
	warn := newMessage("warn-1", "task-1", "system", "Merge conflict detected")
	if err := s.CreateTaskMessage(warn); err != nil {
		t.Fatalf("create warn message: %v", err)
	}

	got, err := s.GetTaskMessages("task-1", "")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	if got[0].Severity != "info" {
		t.Fatalf("expected severity=info, got %q", got[0].Severity)
	}
	if got[1].Severity != "" {
		t.Fatalf("expected empty severity for default, got %q", got[1].Severity)
	}

	single, err := s.GetTaskMessageByID("info-1")
	if err != nil {
		t.Fatalf("get message by id: %v", err)
	}
	if single.Severity != "info" {
		t.Fatalf("expected severity=info via GetTaskMessageByID, got %q", single.Severity)
	}
}

// TestGetTaskMessagesByThreadEmptyTaskID: a thread-scoped lookup with an empty taskID still
// returns the thread's messages, which is what getLastCommitHashInThread needs to find the
// remaining commit hashes by thread alone.
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

// TestGetLastMessageInThread: the newest row whatever its role, and inside one turn (an
// assistant row and its tool_call rows can share a created_at) the row written last wins.
func TestGetLastMessageInThread(t *testing.T) {
	s := testStore(t)
	if err := s.CreateProject(newProject("proj-lm", "test", "/tmp/test")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-lm", "proj-lm")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Empty thread: no message, no error.
	last, err := s.GetLastMessageInThread("thread-empty")
	if err != nil || last != nil {
		t.Fatalf("empty thread: last=%v err=%v, want nil,nil", last, err)
	}
	if last, err := s.GetLastMessageInThread(""); err != nil || last != nil {
		t.Fatalf("empty thread id: last=%v err=%v, want nil,nil", last, err)
	}

	stamp := time.Now().UTC()
	// Turn 1: user question; turn 2: assistant answer + its tool_call, all with the SAME
	// created_at, so only insertion order (rowid) can order them.
	rows := []*types.TaskMessage{
		newMessage("m-user", "task-lm", "user", "yes"),
		newMessage("m-assistant", "task-lm", "assistant", "asking"),
		newMessage("m-tool", "task-lm", "tool_call", ""),
	}
	for i, m := range rows {
		m.ThreadID = "thread-lm"
		m.CreatedAt = stamp.Add(time.Duration(i) * time.Second)
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message %d: %v", i, err)
		}
	}
	last, err = s.GetLastMessageInThread("thread-lm")
	if err != nil {
		t.Fatalf("get last message: %v", err)
	}
	if last == nil || last.ID != "m-tool" {
		t.Fatalf("last = %+v, want the tool_call row", last)
	}

	// Same timestamp for the assistant and its tool_call row: rowid decides.
	sameStamp := time.Now().UTC().Add(time.Hour)
	for _, m := range []*types.TaskMessage{
		newMessage("m-tie-assistant", "task-lm", "assistant", "done"),
		newMessage("m-tie-tool", "task-lm", "tool_call", ""),
	} {
		m.ThreadID = "thread-lm"
		m.CreatedAt = sameStamp
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create tie message: %v", err)
		}
	}
	if last, err = s.GetLastMessageInThread("thread-lm"); err != nil {
		t.Fatalf("get last message after tie: %v", err)
	}
	if last == nil || last.ID != "m-tie-tool" {
		t.Fatalf("tie broken wrong: %+v, want m-tie-tool (written last)", last)
	}

	// A different thread's rows never leak in.
	other := newMessage("m-other", "task-lm", "user", "elsewhere")
	other.ThreadID = "thread-other"
	if err := s.CreateTaskMessage(other); err != nil {
		t.Fatalf("create other message: %v", err)
	}
	if last, err = s.GetLastMessageInThread("thread-lm"); err != nil || last == nil || last.ID != "m-tie-tool" {
		t.Fatalf("thread filter broken: %+v err=%v", last, err)
	}
}

// A summary row is not a turn of the conversation: it is what the turns before it are
// remembered by, and it is the cut point the LLM context is built around, so a cascade that
// deletes messages must leave it and the rows it replaced consistent.
func TestDeleteTaskMessagesAfter_PreservesSummarization(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	baseTime := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	seed := []struct {
		id, role, content string
	}{
		{"msg-1", "user", "first request"},
		{"msg-2", "assistant", "first work"},
		{"sum-1", "summarization", "SUMMARY OF msg-1 and msg-2"},
		{"msg-3", "user", "later request"},
		{"msg-4", "assistant", "later work"},
	}
	for i, m := range seed {
		msg := newMessage(m.id, "task-1", m.role, m.content)
		msg.CreatedAt = baseTime.Add(time.Duration(i+1) * time.Second)
		if err := s.CreateTaskMessage(msg); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	// Delete from the first user message onward: every conversation row goes, the
	// summary survives standing in for what it summarized.
	if _, err := s.DeleteTaskMessagesAfter("task-1", "msg-1", ""); err != nil {
		t.Fatalf("delete messages after: %v", err)
	}

	remaining, err := s.GetTaskMessages("task-1", "")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("expected only the summary row, got %d: %+v", len(remaining), remaining)
	}
	if remaining[0].ID != "sum-1" || remaining[0].Role != "summarization" {
		t.Fatalf("expected the summarization row to survive, got %+v", remaining[0])
	}
}

// Naming a summarization row as the target is supported and does remove it — the guard
// above is only against collateral. A *later* summary is still collateral though, so it
// survives while ordinary messages after the target go.
func TestDeleteTaskMessagesAfter_TargetIsSummarization(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	baseTime := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	seed := []struct {
		id, role string
	}{
		{"msg-1", "user"},
		{"sum-1", "summarization"},
		{"msg-2", "user"},
		{"sum-2", "summarization"},
		{"msg-3", "user"},
	}
	for i, m := range seed {
		msg := newMessage(m.id, "task-1", m.role, "content "+m.id)
		msg.CreatedAt = baseTime.Add(time.Duration(i+1) * time.Second)
		if err := s.CreateTaskMessage(msg); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	if _, err := s.DeleteTaskMessagesAfter("task-1", "sum-1", ""); err != nil {
		t.Fatalf("delete messages after: %v", err)
	}

	remaining, err := s.GetTaskMessages("task-1", "")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	got := map[string]bool{}
	for _, m := range remaining {
		got[m.ID] = true
	}
	if got["sum-1"] {
		t.Error("the targeted summary row should have been deleted")
	}
	if got["msg-2"] || got["msg-3"] {
		t.Error("messages after the target should have been deleted")
	}
	if !got["msg-1"] {
		t.Error("messages before the target must survive")
	}
	if !got["sum-2"] {
		t.Error("a later summary is collateral and must survive")
	}
}

// The delete reports which of the rows it removed held a snapshot — only its own predicate knows what
// went, since summarization rows stay and rows inside a turn share a created_at. Guessing could release a
// snapshot a surviving row still names.
func TestDeleteTaskMessagesAfter_ReportsDeletedCheckpoints(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	base := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	seed := []struct {
		id, role, hash string
	}{
		{"early-user", "user", ""},
		{"early-checkpoint", "assistant", "hash-survives"},
		{"anchor", "user", ""},
		{"deleted-checkpoint", "assistant", "hash-gone"},
		{"deleted-tool-call", "tool_call", ""},
		{"summary", "summarization", "hash-summary-stays"},
		{"last-checkpoint", "assistant", "hash-last"},
	}
	for i, m := range seed {
		msg := newMessage(m.id, "task-1", m.role, "content "+m.id)
		msg.GitCommitHash = m.hash
		msg.CreatedAt = base.Add(time.Duration(i+1) * time.Second)
		if err := s.CreateTaskMessage(msg); err != nil {
			t.Fatalf("create %s: %v", m.id, err)
		}
	}

	reported, err := s.DeleteTaskMessagesAfter("task-1", "anchor", "")
	if err != nil {
		t.Fatalf("delete messages after: %v", err)
	}
	want := []string{"deleted-checkpoint", "last-checkpoint"}
	if len(reported) != len(want) {
		t.Fatalf("reported %v, want %v", reported, want)
	}
	for i := range want {
		if reported[i] != want[i] {
			t.Fatalf("reported %v, want %v", reported, want)
		}
	}

	remaining, err := s.GetTaskMessages("task-1", "")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(remaining) != 3 {
		t.Fatalf("expected 3 surviving rows, got %d: %+v", len(remaining), remaining)
	}
	if remaining[1].ID != "early-checkpoint" || remaining[2].ID != "summary" {
		t.Errorf("the earlier checkpoint and the summary must survive: %+v", remaining)
	}
}

func TestLastSummarizationMessageID(t *testing.T) {
	s := testStore(t)
	if err := s.CreateProject(newProject("proj-1", "test", "/tmp/test")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-1", "proj-1")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	for _, th := range []*types.TaskThread{
		newThread("thread-1", "task-1", "Main"),
		newThread("thread-2", "task-1", "Fork"),
	} {
		if err := s.CreateThread(th); err != nil {
			t.Fatalf("create thread %s: %v", th.ID, err)
		}
	}

	base := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	in := func(id, threadID, role, content string, offset time.Duration) {
		m := newMessage(id, "task-1", role, content)
		m.ThreadID = threadID
		m.CreatedAt = base.Add(offset)
		if err := s.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message %s: %v", id, err)
		}
	}
	in("turn-1", "thread-1", "user", "first", 0)
	in("summary-old", "thread-1", "summarization", "old summary", time.Second)
	in("turn-2", "thread-1", "user", "second", 2*time.Second)
	// A created_at tie, as one turn writes several rows with the same instant: rowid
	// breaks it, which is the rule GetTaskMessages reads the thread in.
	in("summary-first", "thread-1", "summarization", "tied summary", 3*time.Second)
	in("summary-tied", "thread-1", "summarization", "newest summary", 3*time.Second)
	in("fork-summary", "thread-2", "summarization", "fork summary", 9*time.Second)

	got, err := s.LastSummarizationMessageID("thread-1")
	if err != nil {
		t.Fatalf("last summarization id: %v", err)
	}
	if got != "summary-tied" {
		t.Fatalf("expected summary-tied (newer than the tied row, and not thread-2's), got %q", got)
	}

	// thread-2's answer is its own row, however much later it is.
	got, err = s.LastSummarizationMessageID("thread-2")
	if err != nil {
		t.Fatalf("last summarization id for thread-2: %v", err)
	}
	if got != "fork-summary" {
		t.Fatalf("expected fork-summary, got %q", got)
	}

	if err := s.DeleteTaskMessageByID("fork-summary"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, err = s.LastSummarizationMessageID("thread-2")
	if err != nil {
		t.Fatalf("last summarization id for an unsummarized thread: %v", err)
	}
	if got != "" {
		t.Fatalf("expected an empty id for a thread with no summary, got %q", got)
	}
	if got, err := s.LastSummarizationMessageID(""); err != nil || got != "" {
		t.Fatalf("expected (\"\", nil) for an empty thread id, got (%q, %v)", got, err)
	}
}
