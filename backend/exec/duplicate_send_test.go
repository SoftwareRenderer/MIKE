package exec

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kanban/backend/types"
)

// TestHandleSend_DuplicateMessage_DoesNotStartExecutor verifies that re-sending a
// user message identical to the last one in the thread (content + attachments) is
// deduped: it is NOT persisted again, and it does NOT trigger the executor. Before
// the fix, a deduped message still went through ForwardUserMessage → handleLazyStart,
// which rebuilt a context ending in the previous message (often an assistant
// message) and called the LLM with a trailing assistant message — producing a
// second assistant message and the "Cannot have 2 or more assistant messages at
// the end of the list" error on every subsequent call.
func TestHandleSend_DuplicateMessage_DoesNotStartExecutor(t *testing.T) {
	te, _, projectID, _ := setupExecutorWithGit(t)
	te.gate = NewConcurrencyGate()
	te.taskMu.Lock()
	te.tasks = make(map[string]*executionState)
	te.taskMu.Unlock()

	taskID := "dup-send-task"
	threadID := "dup-thread"
	task := &types.Task{
		ID:              taskID,
		ProjectID:       projectID,
		Prompt:          "Test task",
		Status:          "review",
		ExecutionState:  "idle",
		LlamaServerURL:  "http://localhost:8080/v1",
		ModelName:       "test-model",
		CurrentThreadID: threadID,
	}
	if err := te.store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := te.store.CreateThread(&types.TaskThread{ID: threadID, TaskID: taskID, IsActive: true}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	// Seed a last user message (the "Yes" approval) and an assistant message after
	// it, mirroring the grace-snow-sunset state that reproduced the bug.
	seed := []*types.TaskMessage{
		{ID: "u1", TaskID: taskID, ThreadID: threadID, Role: "user", Content: "Yes", CreatedAt: time.Now()},
		{ID: "a1", TaskID: taskID, ThreadID: threadID, Role: "assistant", Content: "I've created TODO.md. Here's the full plan", CreatedAt: time.Now()},
	}
	for _, m := range seed {
		if err := te.store.CreateTaskMessage(m); err != nil {
			t.Fatalf("seed message: %v", err)
		}
	}

	// Re-send "Yes" — identical to the last persisted user message. HandleSend must
	// detect the duplicate, NOT persist another user row, and NOT start an executor.
	send := func(content string) *httptest.ResponseRecorder {
		body := bytes.NewBufferString(`{"content":"` + content + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/messages", body)
		req.SetPathValue("projectID", projectID)
		req.SetPathValue("taskID", taskID)
		rec := httptest.NewRecorder()
		te.HandleSend(rec, req)
		return rec
	}

	rec := send("Yes")
	if rec.Code != http.StatusCreated {
		t.Fatalf("duplicate send status = %d, want 201", rec.Code)
	}
	time.Sleep(100 * time.Millisecond) // allow any (incorrect) executor goroutine to start

	te.taskMu.Lock()
	_, exists := te.tasks[taskID]
	te.taskMu.Unlock()
	if exists {
		t.Fatal("duplicate user message must not start an executor")
	}

	// Only one user message with content "Yes" must exist.
	msgs, _ := te.store.GetTaskMessages(taskID, threadID)
	yesCount := 0
	for _, m := range msgs {
		if m.Role == "user" && m.Content == "Yes" {
			yesCount++
		}
	}
	if yesCount != 1 {
		t.Fatalf("expected exactly 1 user message with content \"Yes\", got %d", yesCount)
	}
}

// TestHandleSend_NewMessage_StillStartsExecutor verifies that a genuinely new
// message still persists a user row and triggers the executor.
func TestHandleSend_NewMessage_StillStartsExecutor(t *testing.T) {
	te, _, projectID, _ := setupExecutorWithGit(t)
	te.gate = NewConcurrencyGate()
	te.taskMu.Lock()
	te.tasks = make(map[string]*executionState)
	te.taskMu.Unlock()

	taskID := "new-msg-task"
	threadID := "new-msg-thread"
	task := &types.Task{
		ID:              taskID,
		ProjectID:       projectID,
		Prompt:          "Test task",
		Status:          "review",
		ExecutionState:  "idle",
		LlamaServerURL:  "http://localhost:8080/v1",
		ModelName:       "test-model",
		CurrentThreadID: threadID,
	}
	if err := te.store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := te.store.CreateThread(&types.TaskThread{ID: threadID, TaskID: taskID, IsActive: true}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := te.store.CreateTaskMessage(&types.TaskMessage{ID: "u1", TaskID: taskID, ThreadID: threadID, Role: "user", Content: "Yes", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	body := bytes.NewBufferString(`{"content":"No, do something else"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/messages", body)
	req.SetPathValue("projectID", projectID)
	req.SetPathValue("taskID", taskID)
	rec := httptest.NewRecorder()
	te.HandleSend(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("new send status = %d, want 201", rec.Code)
	}

	// The new message must be persisted.
	msgs, _ := te.store.GetTaskMessages(taskID, threadID)
	found := false
	for _, m := range msgs {
		if m.Role == "user" && m.Content == "No, do something else" {
			found = true
		}
	}
	if !found {
		t.Fatal("new user message must be persisted")
	}

	// The executor must start (it may exit immediately without a live LLM).
	waitForExecutor(t, te, taskID, true)

	// Clean up: stop the executor so its goroutines (worktree watchers, LLM retry)
	// don't leak into subsequent tests in this package. Without a live mock LLM the
	// loop would otherwise keep retrying in the background.
	if err := te.StopTask(taskID); err != nil {
		t.Fatalf("stop task: %v", err)
	}
	waitForExecutor(t, te, taskID, false)
}

// waitForExecutor polls until an executor is (or is not) present for taskID.
func waitForExecutor(t *testing.T, te *TaskExecutor, taskID string, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		te.taskMu.Lock()
		_, exists := te.tasks[taskID]
		te.taskMu.Unlock()
		if exists == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("executor present = %v, want %v", !want, want)
}
