package exec

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"kanban/backend/stream"
	"kanban/backend/types"

	"github.com/google/uuid"
)

// mockSummarizeIdleLLM serves /models discovery, streaming chat completions
// (text-only responses), and non-streaming summarization completions. It counts
// streaming (agent) chat calls and non-streaming summarization calls so tests
// can assert whether the executor issues an LLM call after summarization.
func mockSummarizeIdleLLM(streamCalls, summarizeCalls *atomic.Int32, firstChat chan<- struct{}) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		var body struct {
			Stream   bool `json:"stream"`
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Stream {
			streamCalls.Add(1)
			select {
			case firstChat <- struct{}{}:
			default:
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			// Text-only response — no tool calls — so the loop ends the turn and
			// blocks for a user message with the task in review/idle.
			// The text is a question so the loop detector treats it as a valid
			// response (plain statements with finish_reason=stop count as loops).
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Does this look good?\"},\"finish_reason\":null}]}\n\n"))
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		// Non-streaming: context summarization (summarizeContext).
		// Strict chat templates (e.g. Qwen3.8 on llama.cpp) reject system-only
		// requests, so mirror that: the call must carry a user message.
		hasUser := false
		for _, m := range body.Messages {
			if m.Role == "user" {
				hasUser = true
				break
			}
		}
		if !hasUser {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"No user query found in messages."}}`))
			return
		}
		summarizeCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"summary text"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
}

// waitFor polls cond until it returns true or the timeout elapses, failing the test.
func waitFor(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", desc)
}

// waitForCondition polls cond until it returns true or the timeout elapses.
// Returns true when cond became true within the window.
func waitForCondition(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// summarizeIdleSetup builds the common executor + task + setup for the manual
// summarize tests: a task with one user message, run via executeTask against the
// mock LLM. Returns the executor, the setup, and the task ID.
func summarizeIdleSetup(t *testing.T, projID, projName, taskID string, serverURL string) (*TaskExecutor, *taskExecSetup) {
	t.Helper()
	te := newSessionCostTestExecutor(t)
	s := te.rawStore()
	proj := createProject(t, s, projID, projName)

	task := &types.Task{
		ID:              taskID,
		ProjectID:       proj.ID,
		Title:           "Sum task",
		Prompt:          "Do something",
		Status:          types.StatusBacklog,
		ExecutionState:  types.ExecStateIdle,
		LlamaServerURL:  serverURL,
		ModelName:       "test-model",
		OperatingMode:   "act",
		CurrentThreadID: "thread-1",
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.CreateTaskMessage(&types.TaskMessage{
		ID: uuid.New().String(), TaskID: taskID, ThreadID: "thread-1",
		Role: "user", Content: "Please do the thing", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create message: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	setup := &taskExecSetup{
		task:           task,
		project:        proj,
		projectID:      proj.ID,
		worktreePath:   t.TempDir(),
		serverURL:      serverURL,
		modelName:      "test-model",
		ctx:            ctx,
		cancel:         cancel,
		userMsgChan:    make(chan stream.UserMessage, 16),
		summarizeCh:    make(chan *summarizeRequest, 16),
		sandboxEnabled: false,
		done:           done,
	}
	te.taskMu.Lock()
	te.tasks[taskID] = &executionState{
		ctx:          ctx,
		cancel:       cancel,
		userMsgChan:  setup.userMsgChan,
		summarizeCh:  setup.summarizeCh,
		executorType: "llm",
		done:         done,
	}
	te.taskMu.Unlock()

	go te.executeTask(setup)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Log("executor did not stop after cancel")
		}
	})
	return te, setup
}

// TestExecuteTask_ManualSummarize_StaysIdleWithoutQueuedMessage is the regression
// test for: a task in review that gets manually summarized with NO queued user
// message must NOT start a fresh LLM call. It must remain review/idle.
func TestExecuteTask_ManualSummarize_StaysIdleWithoutQueuedMessage(t *testing.T) {
	// Summarization renders prompts/summarize.tmpl from the CWD — chdir to the
	// repo root so the template is found (mirrors how the running server resolves it).
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	oldwd, _ := os.Getwd()
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("chdir to repo root: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	var streamCalls atomic.Int32
	var summarizeCalls atomic.Int32
	firstChat := make(chan struct{}, 1)
	server := mockSummarizeIdleLLM(&streamCalls, &summarizeCalls, firstChat)
	defer server.Close()

	te, setup := summarizeIdleSetup(t, "proj-sum-idle", "Sum Idle", "sum-idle-task", server.URL)
	taskID := setup.task.ID

	// Wait for the first (streaming) LLM call, then for the task to reach review/idle.
	select {
	case <-firstChat:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for first LLM request")
	}
	waitFor(t, 10*time.Second, "task to reach review/idle after first response", func() bool {
		got, err := te.rawStore().GetTaskByID(taskID)
		return err == nil && got != nil && got.Status == types.StatusReview && got.ExecutionState == types.ExecStateIdle
	})

	// Manual summarize request with NO queued user message.
	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()
	resultCh := make(chan error, 1)
	setup.summarizeCh <- &summarizeRequest{ctx: reqCtx, resultCh: resultCh, cancel: reqCancel}

	// Summarization must complete...
	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("summarization failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for summarization to complete")
	}

	// ...and the task must NOT start a fresh LLM call — there is no queued message.
	if waitForCondition(700*time.Millisecond, func() bool { return streamCalls.Load() >= 2 }) {
		t.Fatalf("task started a new LLM call after summarization with no queued message (streamCalls=%d)", streamCalls.Load())
	}

	// Task must still be review/idle.
	got, err := te.rawStore().GetTaskByID(taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Status != types.StatusReview || got.ExecutionState != types.ExecStateIdle {
		t.Fatalf("task should remain review/idle after summarization, got status=%q state=%q", got.Status, got.ExecutionState)
	}

	// The summary must be persisted, and the summarization endpoint must have been hit.
	msgs, _ := te.rawStore().GetTaskMessages(taskID, "thread-1")
	hasSummary := false
	for _, m := range msgs {
		if m.Role == "summarization" {
			hasSummary = true
		}
	}
	if !hasSummary {
		t.Fatal("expected a summarization message to be persisted")
	}
	if summarizeCalls.Load() == 0 {
		t.Fatal("expected the summarization endpoint to be called")
	}
	// Manual summarization must NOT inject a "Proceed" user message.
	for _, m := range msgs {
		if m.Role == "user" && m.Content == "Proceed" {
			t.Fatal("manual summarization must not inject a 'Proceed' user message")
		}
	}
}

// TestExecuteTask_ManualSummarize_StartsWithQueuedMessage verifies the intended
// behavior that the previous test protects: when a user message IS queued while
// the task is in review, summarizing must still start the task (a new LLM call).
func TestExecuteTask_ManualSummarize_StartsWithQueuedMessage(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	oldwd, _ := os.Getwd()
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("chdir to repo root: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	var streamCalls atomic.Int32
	var summarizeCalls atomic.Int32
	firstChat := make(chan struct{}, 16)
	server := mockSummarizeIdleLLM(&streamCalls, &summarizeCalls, firstChat)
	defer server.Close()

	te, setup := summarizeIdleSetup(t, "proj-sum-queued", "Sum Queued", "sum-queued-task", server.URL)
	taskID := setup.task.ID

	select {
	case <-firstChat:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for first LLM request")
	}
	waitFor(t, 10*time.Second, "task to reach review/idle after first response", func() bool {
		got, err := te.rawStore().GetTaskByID(taskID)
		return err == nil && got != nil && got.Status == types.StatusReview && got.ExecutionState == types.ExecStateIdle
	})

	// Queue a new user message (persisted in the DB exactly as HandleSend does,
	// before the executor is notified) and request summarization.
	if err := te.rawStore().CreateTaskMessage(&types.TaskMessage{
		ID: uuid.New().String(), TaskID: taskID, ThreadID: "thread-1",
		Role: "user", Content: "Second message", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create second message: %v", err)
	}
	setup.userMsgChan <- stream.UserMessage{Content: "Second message", ServerURL: server.URL, ModelName: "test-model"}
	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()
	resultCh := make(chan error, 1)
	setup.summarizeCh <- &summarizeRequest{ctx: reqCtx, resultCh: resultCh, cancel: reqCancel}

	// With a queued message, the task must start: a second streaming LLM call happens.
	waitFor(t, 10*time.Second, "second LLM call after queued message + summarization", func() bool {
		return streamCalls.Load() >= 2
	})

	// Drain the result (summarization completes either before or after the call).
	select {
	case <-resultCh:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for summarization to complete")
	}
}
