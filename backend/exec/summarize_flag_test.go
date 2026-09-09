package exec

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"kanban/backend/stream"
	"kanban/backend/types"

	"github.com/google/uuid"
)

// mockSummarizeFlagLLM serves /models discovery, a non-streaming
// summarization call held open until released (so the test can act while the
// flag is set), and streaming chat completions (text-only responses).
// summarizeStarted signals that the summarization call arrived, which proves the handler
// has already snapshotted the messages.
func mockSummarizeFlagLLM(release chan struct{}, summarizeStarted chan struct{}) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		var body struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Stream {
			// Non-streaming context summarization: hold it open until released.
			select {
			case summarizeStarted <- struct{}{}:
			default:
			}
			<-release
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"summary text"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
			return
		}
		// Streaming chat (the lazy-started executor): text-only response.
		// A question so the loop detector treats it as a valid response.
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Does this look good?\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
}

// TestHandleSummarize_DBPath_ClearsFlagBeforeLazyStart is the regression test
// for the stuck context-summarization spinner: when a user message is sent
// while a DB-based summarization is in flight, the message is blocked and the
// task is lazy-started after the summarization completes. The flag must be
// cleared BEFORE that lazy start — the fresh executor loads the task from the
// DB, and if it observed summarizing=true it would re-assert the stale flag on
// its first persist, leaving the UI spinner spinning forever.
func TestHandleSummarize_DBPath_ClearsFlagBeforeLazyStart(t *testing.T) {
	// Summarization renders prompts/summarize.tmpl from the CWD — chdir to
	// the repo root so the template is found (mirrors summarize_idle_test.go).
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	oldwd, _ := os.Getwd()
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("chdir to repo root: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	te, _, projectID, _ := setupExecutorWithGit(t)
	te.gate = NewConcurrencyGate()
	te.blockedMessages.tasks = make(map[string]struct{})
	te.dbSummarizeCancel.tasks = make(map[string]context.CancelFunc)
	taskID := "test-sum-db-flag-task"

	release := make(chan struct{})
	summarizeStarted := make(chan struct{})
	server := mockSummarizeFlagLLM(release, summarizeStarted)
	defer server.Close()

	task := &types.Task{
		ID:              taskID,
		ProjectID:       projectID,
		Title:           "Sum db flag",
		Prompt:          "Do something",
		Status:          types.StatusBacklog,
		ExecutionState:  types.ExecStateIdle,
		LlamaServerURL:  server.URL,
		ModelName:       "test-model",
		OperatingMode:   "act",
		CurrentThreadID: "thread-1",
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}
	if err := te.store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := te.store.CreateTaskMessage(&types.TaskMessage{
		ID: uuid.New().String(), TaskID: taskID, ThreadID: "thread-1",
		Role: "user", Content: "Please do the thing", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create message: %v", err)
	}

	// Trigger the DB-path summarization (no active executor for this task).
	mux := http.NewServeMux()
	mux.HandleFunc("/api/projects/{projectID}/tasks/{taskID}/summarize", te.HandleSummarize)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/tasks/"+taskID+"/summarize", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("summarize endpoint returned %d: %s", rec.Code, rec.Body.String())
	}

	// Wait for the summarization LLM call to be in flight, then verify the
	// flag is set (the precondition the ordering fix must preserve).
	waitFor(t, 10*time.Second, "flag to be set while summarization is in flight", func() bool {
		got, err := te.store.GetTaskByID(taskID)
		return err == nil && got != nil && got.Summarizing
	})
	// The mock holds the summarization call open until release is closed, so it is still in
	// flight here and the handler has already read its message snapshot.
	//
	// The message below must land after that snapshot: the timestamp fix-up moves only rows
	// newer than the snapshot's newest message, so one already in the snapshot can never be
	// re-sorted and buildLLMContext compresses it away. Waiting on the request rather than
	// on the flag (which is set before the read) is what makes that ordering deterministic.
	select {
	case <-summarizeStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("summarization request never reached the LLM")
	}

	// Send a user message while the flag is set: HandleSend persists the
	// message first, then ForwardUserMessage must block the lazy start.
	if err := te.store.CreateTaskMessage(&types.TaskMessage{
		ID: uuid.New().String(), TaskID: taskID, ThreadID: "thread-1",
		Role: "user", Content: "Message sent during summarization", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create blocked message: %v", err)
	}
	te.ForwardUserMessage(projectID, taskID, stream.UserMessage{
		Content:   "Message sent during summarization",
		ModelName: "test-model",
		ServerURL: server.URL,
	})
	te.blockedMessages.mu.Lock()
	_, hasBlocked := te.blockedMessages.tasks[taskID]
	te.blockedMessages.mu.Unlock()
	if !hasBlocked {
		t.Fatal("message sent during summarization should be blocked")
	}

	// Let the summarization complete; the blocked message must lazy-start
	// the task.
	close(release)
	// Wait on what the turn leaves behind, not on ExecutionState: "running" is
	// overwritten as soon as the turn ends, and this mock answers fast enough that a
	// whole turn can fit between two polls. A persisted assistant message is monotone.
	waitFor(t, 30*time.Second, "the blocked message to lazy-start a turn (assistant reply persisted)", func() bool {
		msgs, err := te.store.GetTaskMessages(taskID, "thread-1")
		if err != nil {
			return false
		}
		for _, m := range msgs {
			if m.Role == "assistant" {
				return true
			}
		}
		return false
	})

	// The regression: once the executor is running (it has already persisted
	// its task copy at slot acquisition), the DB flag must be false — and it
	// must NOT flip back to true from a stale in-memory copy.
	waitFor(t, 5*time.Second, "flag to be cleared after summarization completes", func() bool {
		got, err := te.store.GetTaskByID(taskID)
		return err == nil && got != nil && !got.Summarizing
	})
	for i := 0; i < 60; i++ {
		got, err := te.store.GetTaskByID(taskID)
		if err == nil && got != nil && got.Summarizing {
			t.Fatalf("summarizing flag was resurrected after summarization completed (stale executor write)")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The summary must have been persisted.
	msgs, err := te.store.GetTaskMessages(taskID, "thread-1")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	hasSummary := false
	for _, m := range msgs {
		if m.Role == "summarization" {
			hasSummary = true
		}
	}
	if !hasSummary {
		t.Fatal("expected a persisted summarization message")
	}
}
