package exec

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mike/backend/types"
)

// HandleSend dedups a double submit: the same send landing twice with nothing in
// between. It used to match the last user message however old, so answering a second
// question with the same words dropped the answer — and flipped the task out of
// review first, stranding it as "Pending". It now needs that message to still be the
// thread's last row, and only flips status for a message that will run.

// seedReviewTask creates an awaiting-user task (review/idle) with the given message
// tail. Timestamps are spaced a second apart so "newest row" is unambiguous.
func seedReviewTask(t *testing.T, te *TaskExecutor, projectID, taskID, threadID string, tail ...types.TaskMessage) {
	t.Helper()
	task := &types.Task{
		ID:              taskID,
		ProjectID:       projectID,
		Prompt:          "Test task",
		Status:          types.StatusReview,
		ExecutionState:  types.ExecStateIdle,
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
	base := time.Now().UTC().Add(-time.Duration(len(tail)) * time.Minute)
	for i := range tail {
		m := tail[i]
		m.TaskID, m.ThreadID = taskID, threadID
		if m.CreatedAt.IsZero() {
			m.CreatedAt = base.Add(time.Duration(i) * time.Second)
		}
		if err := te.store.CreateTaskMessage(&m); err != nil {
			t.Fatalf("seed message %d: %v", i, err)
		}
	}
}

// sendUserMessage posts content to HandleSend and returns the recorder.
func sendUserMessage(t *testing.T, te *TaskExecutor, projectID, taskID, content string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/messages",
		bytes.NewBufferString(`{"content":"`+content+`"}`))
	req.SetPathValue("projectID", projectID)
	req.SetPathValue("taskID", taskID)
	rec := httptest.NewRecorder()
	te.HandleSend(rec, req)
	return rec
}

func countUserMessages(t *testing.T, te *TaskExecutor, taskID, threadID, content string) int {
	t.Helper()
	msgs, err := te.store.GetTaskMessages(taskID, threadID)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	n := 0
	for _, m := range msgs {
		if m.Role == "user" && m.Content == content {
			n++
		}
	}
	return n
}

func taskRow(t *testing.T, te *TaskExecutor, projectID, taskID string) *types.Task {
	t.Helper()
	task, err := te.store.GetTask(projectID, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	return task
}

// stopExecutorIfStarted tears down an executor that the test expects may have started,
// so its retry goroutines (no live LLM in unit tests) don't leak into other tests.
func stopExecutorIfStarted(t *testing.T, te *TaskExecutor, taskID string) {
	t.Helper()
	te.taskMu.Lock()
	_, exists := te.tasks[taskID]
	te.taskMu.Unlock()
	if !exists {
		return
	}
	_ = te.StopTask(taskID)
	waitForExecutor(t, te, taskID, false)
}

// The agent asked something after the previous "Yes", so this "Yes" is a new answer:
// it must be stored, move the task out of review and run it.
func TestHandleSend_RepeatedAnswerAfterAgentReply_IsDelivered(t *testing.T) {
	t.Parallel()
	te, _, projectID, _ := setupExecutorWithGit(t)
	taskID, threadID := "dup-answer-task", "dup-answer-thread"
	seedReviewTask(t, te, projectID, taskID, threadID,
		types.TaskMessage{ID: "u1", Role: "user", Content: "Yes"},
		types.TaskMessage{ID: "a1", Role: "assistant", Content: "Want me to implement the fix?"},
		types.TaskMessage{ID: "a2", Role: "assistant", Content: "I've created TODO.md. Here's the full plan — approve?"},
	)

	if rec := sendUserMessage(t, te, projectID, taskID, "Yes"); rec.Code != http.StatusCreated {
		t.Fatalf("send status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	t.Cleanup(func() { stopExecutorIfStarted(t, te, taskID) })

	if n := countUserMessages(t, te, taskID, threadID, "Yes"); n != 2 {
		t.Fatalf("user messages with content \"Yes\" = %d, want 2 — the answer was deduped and lost", n)
	}
	waitForExecutor(t, te, taskID, true)

	if task := taskRow(t, te, projectID, taskID); task.Status != types.StatusInProgress {
		t.Fatalf("delivered answer must start the task, status = %q, want in_progress", task.Status)
	}
}

// The same words again after an ask_question turn (assistant row + its tool_call row):
// still a new answer.
func TestHandleSend_ReplyAfterAskQuestionToolCall_IsDelivered(t *testing.T) {
	t.Parallel()
	te, _, projectID, _ := setupExecutorWithGit(t)
	taskID, threadID := "dup-ask-task", "dup-ask-thread"
	seedReviewTask(t, te, projectID, taskID, threadID,
		types.TaskMessage{ID: "u1", Role: "user", Content: "Yes"},
		types.TaskMessage{ID: "a1", Role: "assistant", Content: "One more thing — which scope?"},
		types.TaskMessage{ID: "t1", Role: "tool_call", ToolCalls: `{"tool_name":"ask_question","tool_id":"call-1"}`, ToolResult: `{"output":"Which scope?","success":true}`},
	)

	if rec := sendUserMessage(t, te, projectID, taskID, "Yes"); rec.Code != http.StatusCreated {
		t.Fatalf("send status = %d, want 201", rec.Code)
	}
	t.Cleanup(func() { stopExecutorIfStarted(t, te, taskID) })

	if n := countUserMessages(t, te, taskID, threadID, "Yes"); n != 2 {
		t.Fatalf("user messages with content \"Yes\" = %d, want 2", n)
	}
}

// The case the dedup exists for: the identical message is still the last row. Nothing new
// is stored, no executor starts, and the task stays in review.
func TestHandleSend_DoubleSubmit_IsDeduped_AndLeavesReview(t *testing.T) {
	t.Parallel()
	te, _, projectID, _ := setupExecutorWithGit(t)
	taskID, threadID := "dup-submit-task", "dup-submit-thread"
	seedReviewTask(t, te, projectID, taskID, threadID,
		types.TaskMessage{ID: "a0", Role: "assistant", Content: "Here's what I found"},
		types.TaskMessage{ID: "u1", Role: "user", Content: "Yes"},
	)

	if rec := sendUserMessage(t, te, projectID, taskID, "Yes"); rec.Code != http.StatusCreated {
		t.Fatalf("send status = %d, want 201", rec.Code)
	}
	time.Sleep(100 * time.Millisecond) // allow any (incorrect) executor goroutine to start

	if n := countUserMessages(t, te, taskID, threadID, "Yes"); n != 1 {
		t.Fatalf("duplicate send must not persist a second row, got %d", n)
	}
	te.taskMu.Lock()
	_, exists := te.tasks[taskID]
	te.taskMu.Unlock()
	if exists {
		t.Fatal("duplicate user message must not start an executor")
	}
	if task := taskRow(t, te, projectID, taskID); task.Status != types.StatusReview || task.ExecutionState != types.ExecStateIdle {
		t.Fatalf("dropped duplicate moved the card: status=%q state=%q, want review/idle", task.Status, task.ExecutionState)
	}
}

// A genuinely new message still stores, flips review -> in_progress and starts.
func TestHandleSend_NewMessage_StillStartsExecutor(t *testing.T) {
	t.Parallel()
	te, _, projectID, _ := setupExecutorWithGit(t)
	taskID, threadID := "new-msg-task", "new-msg-thread"
	seedReviewTask(t, te, projectID, taskID, threadID,
		types.TaskMessage{ID: "u1", Role: "user", Content: "Yes"},
	)

	if rec := sendUserMessage(t, te, projectID, taskID, "No, do something else"); rec.Code != http.StatusCreated {
		t.Fatalf("new send status = %d, want 201", rec.Code)
	}
	t.Cleanup(func() { stopExecutorIfStarted(t, te, taskID) })

	if n := countUserMessages(t, te, taskID, threadID, "No, do something else"); n != 1 {
		t.Fatalf("new user message must be persisted, got %d", n)
	}
	waitForExecutor(t, te, taskID, true)

	if task := taskRow(t, te, projectID, taskID); task.Status != types.StatusInProgress {
		t.Fatalf("status = %q, want in_progress", task.Status)
	}
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

// A regenerate is the thread being asked to answer the user message it already holds — which is exactly
// what "New Thread" does: the fork writes the edited question into the new thread, then the send runs it.
// It must start, and must not store the same question a second time.
func TestHandleSend_RegenerateOfLastUserMessage_RunsItWithoutInserting(t *testing.T) {
	t.Parallel()
	te, _, projectID, _ := setupExecutorWithGit(t)
	taskID, threadID := "regen-task", "regen-thread"
	seedReviewTask(t, te, projectID, taskID, threadID,
		types.TaskMessage{ID: "a0", Role: "assistant", Content: "Here's what I found"},
		types.TaskMessage{ID: "u1", Role: "user", Content: "Yes, do it"},
	)

	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/send",
		bytes.NewBufferString(`{"content":"Yes, do it","regenerate":true}`))
	req.SetPathValue("projectID", projectID)
	req.SetPathValue("taskID", taskID)
	rec := httptest.NewRecorder()
	te.HandleSend(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("send status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	t.Cleanup(func() { stopExecutorIfStarted(t, te, taskID) })

	if n := countUserMessages(t, te, taskID, threadID, "Yes, do it"); n != 1 {
		t.Errorf("a regenerate must reuse the existing row, got %d copies", n)
	}
	waitForExecutor(t, te, taskID, true)
	if task := taskRow(t, te, projectID, taskID); task.Status != types.StatusInProgress {
		t.Errorf("a regenerate must run the thread, status = %q, want in_progress", task.Status)
	}
}
