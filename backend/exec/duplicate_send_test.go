package exec

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mike/backend/types"
)

// HandleSend dedups a double submit: the same send landing twice with nothing in between. The
// match requires that message to still be the thread's last row, and only a message that will run
// flips the status.

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

// sendUserMessage posts content to HandleSend and returns the recorder. regenerate asks the thread to
// answer the message it already holds instead of storing a new one.
func sendUserMessage(t *testing.T, te *TaskExecutor, projectID, taskID, content string, regenerate bool) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"content":"` + content + `"}`
	if regenerate {
		body = `{"content":"` + content + `","regenerate":true}`
	}
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/send",
		bytes.NewBufferString(body))
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

// TestHandleSend covers the dedup a double submit needs and everything that must not fall out with it.
// The rule is about the thread's last row, not about the words: the same text is a new answer whenever the
// agent has spoken since (including an ask_question, whose tool_call row is the last one), and only a send
// that repeats the row nobody has answered yet is dropped. A dropped duplicate must not start an executor or
// move the card out of Review; a delivered one must do both; a regenerate runs the row it already holds
// instead of storing it twice.
func TestHandleSend(t *testing.T) {
	t.Parallel()
	te, _, projectID, _ := setupExecutorWithGit(t)

	for _, tc := range []struct {
		name       string
		taskID     string
		tail       []types.TaskMessage
		content    string
		regenerate bool
		wantStored int
		wantRun    bool
		wantStatus types.Status
		wantExec   types.ExecutionState
	}{
		{
			name: "a repeated answer the agent has spoken after is delivered", taskID: "dup-answer-task",
			tail: []types.TaskMessage{
				{ID: "u1", Role: "user", Content: "Yes"},
				{Role: "assistant", Content: "Want me to implement the fix?"},
				{Role: "assistant", Content: "I've created TODO.md. Here's the full plan — approve?"},
			},
			content: "Yes", wantStored: 2, wantRun: true, wantStatus: types.StatusInProgress,
		},
		{
			name: "the same words after an ask_question are delivered", taskID: "dup-ask-task",
			tail: []types.TaskMessage{
				{ID: "u1", Role: "user", Content: "Yes"},
				{Role: "assistant", Content: "One more thing — which scope?"},
				{ID: "t1", Role: "tool_call", ToolCalls: `{"tool_name":"ask_question","tool_id":"call-1"}`, ToolResult: `{"output":"Which scope?","success":true}`},
			},
			content: "Yes", wantStored: 2, wantRun: true, wantStatus: types.StatusInProgress,
		},
		{
			name: "a double submit is dropped", taskID: "dup-submit-task",
			tail: []types.TaskMessage{
				{Role: "assistant", Content: "Here's what I found"},
				{ID: "u1", Role: "user", Content: "Yes"},
			},
			content: "Yes", wantStored: 1, wantRun: false, wantStatus: types.StatusReview, wantExec: types.ExecStateIdle,
		},
		{
			name: "a genuinely new message is stored and starts the run", taskID: "new-msg-task",
			tail:       []types.TaskMessage{{ID: "u1", Role: "user", Content: "Yes"}},
			content:    "No, do something else",
			wantStored: 1, wantRun: true, wantStatus: types.StatusInProgress,
		},
		{
			name: "a regenerate runs the row it holds without storing it again", taskID: "regen-task",
			tail: []types.TaskMessage{
				{Role: "assistant", Content: "Here's what I found"},
				{ID: "u1", Role: "user", Content: "Yes, do it"},
			},
			content: "Yes, do it", regenerate: true, wantStored: 1, wantRun: true, wantStatus: types.StatusInProgress,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			threadID := tc.taskID + "-thread"
			// Row ids are unique across the store, and one executor serves every row here.
			tail := make([]types.TaskMessage, len(tc.tail))
			copy(tail, tc.tail)
			for i := range tail {
				tail[i].ID = tc.taskID + "-" + fmt.Sprint(i)
			}
			seedReviewTask(t, te, projectID, tc.taskID, threadID, tail...)
			t.Cleanup(func() { stopExecutorIfStarted(t, te, tc.taskID) })

			if rec := sendUserMessage(t, te, projectID, tc.taskID, tc.content, tc.regenerate); rec.Code != http.StatusCreated {
				t.Fatalf("send status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
			}
			if n := countUserMessages(t, te, tc.taskID, threadID, tc.content); n != tc.wantStored {
				t.Fatalf("stored rows with content %q = %d, want %d", tc.content, n, tc.wantStored)
			}

			if tc.wantRun {
				waitForExecutor(t, te, tc.taskID, true)
			} else {
				time.Sleep(100 * time.Millisecond) // allow any (incorrect) executor goroutine to start
				te.taskMu.Lock()
				_, exists := te.tasks[tc.taskID]
				te.taskMu.Unlock()
				if exists {
					t.Error("a dropped duplicate started an executor")
				}
			}

			task := taskRow(t, te, projectID, tc.taskID)
			if task.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", task.Status, tc.wantStatus)
			}
			if tc.wantExec != "" && task.ExecutionState != tc.wantExec {
				t.Errorf("execution_state = %q, want %q — a dropped send must not move the card", task.ExecutionState, tc.wantExec)
			}
		})
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
