package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mike/backend/stream"
	"mike/backend/types"

	"github.com/google/uuid"
)

// mockToolThenTextLLM answers the first streaming call with one tool call — the name and the raw argument
// text come from the test — and every later one with a short text answer. Nothing else drives the executor,
// so a second streaming call can only happen if the executor loops back to the LLM on its own.
//
// The text answer ends in a question on purpose: a plain statement with finish_reason=stop reads as a
// repeated-statement loop to the loop detector, which would end the run for an unrelated reason.
func mockToolThenTextLLM(streamCalls *atomic.Int32, toolName, toolArgs string) *httptest.Server {
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
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"summary"},"finish_reason":"stop"}]}`))
			return
		}
		n := streamCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		if n == 1 {
			// The arguments travel as escaped JSON text, exactly as a provider sends them.
			escaped, _ := json.Marshal(toolArgs)
			_, _ = w.Write([]byte(fmt.Sprintf(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":%q,"arguments":%s}}]},"finish_reason":"tool_calls"}]}`+"\n\n", toolName, escaped)))
		} else {
			_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"I could not read it. Should I stop?"},"finish_reason":null}]}` + "\n\n"))
			_, _ = w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":6,"total_tokens":16}}` + "\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
}

// startMockedTurn registers a running task with one user message, puts its executor in the registry and
// starts the loop against the mock LLM. The turn itself is the test's to judge.
//
// The harness the other full-run tests share has a worktreeMgr with no store, so a tool turn panics in
// createCheckpoint and executeTask's recover turns it into a stored error message - hence
// setupExecutorWithGit and a real worktree here.
func startMockedTurn(t *testing.T, te *TaskExecutor, projectID, taskID, branch, srvURL, initialSHA string) *taskExecSetup {
	t.Helper()

	if err := te.store.CreateTask(&types.Task{
		ID:              taskID,
		ProjectID:       projectID,
		Title:           "Tool loop",
		Prompt:          "Read the file",
		Status:          types.StatusInProgress,
		ExecutionState:  types.ExecStateRunning,
		LlamaServerURL:  srvURL,
		ModelName:       "test-model",
		OperatingMode:   "act",
		Branch:          branch,
		BaseCommit:      initialSHA,
		CurrentThreadID: "thread-1",
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := te.store.CreateTaskMessage(&types.TaskMessage{
		ID: uuid.New().String(), TaskID: taskID, ThreadID: "thread-1",
		Role: "user", Content: "Read no-such-file.txt", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create message: %v", err)
	}

	worktreePath, _, err := te.worktreeMgr.CreateWorktree(projectID, taskID, branch, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	setup := &taskExecSetup{
		task:           mustGetTask(t, te, taskID),
		project:        mustGetProject(t, te, projectID),
		projectID:      projectID,
		worktreePath:   worktreePath,
		serverURL:      srvURL,
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
			t.Error("executor did not exit after cancel")
		}
	})
	return setup
}

// waitForStreamCalls reports whether the mock LLM was reached `want` times. A second call can only come
// from the executor looping back on its own, so this is the measurement of "the turn carried on".
func waitForStreamCalls(calls *atomic.Int32, want int32) bool {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() >= want {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return calls.Load() >= want
}

// storedErrorRows lists the messages that ended a turn with a server-side fault.
func storedErrorRows(t *testing.T, te *TaskExecutor, taskID string) []string {
	t.Helper()
	msgs, err := te.store.GetTaskMessages(taskID, "thread-1")
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}
	var out []string
	for _, m := range msgs {
		if m.Role == "error" {
			out = append(out, m.Content)
		}
	}
	return out
}

// TestExecuteTask_ToolCallLoopsBackWithoutUserInput pins the distinction in finalizeTurn that cannot be
// collapsed: turnNextLLMCall (call again now) versus turnWaitIdle (block for the user). Merge the two
// into one "keep going" and the executor blocks in waitForIdleInput, so the second call never happens.
// It also pins that a plain text answer settles instead of calling again.
func TestExecuteTask_ToolCallLoopsBackWithoutUserInput(t *testing.T) {
	t.Parallel()
	var streamCalls atomic.Int32
	te, _, projectID, initialSHA := setupExecutorWithGit(t)
	const taskID = "tool-loop-task"
	const branch = "task/" + taskID
	srv := mockToolThenTextLLM(&streamCalls, types.ToolReadFile, `{"path":"no-such-file.txt"}`)
	defer srv.Close()

	setup := startMockedTurn(t, te, projectID, taskID, branch, srv.URL, initialSHA)

	// The second call is the whole point: it can only come from the executor itself.
	if !waitForStreamCalls(&streamCalls, 2) {
		t.Fatalf("streaming LLM calls = %d, want >= 2: a tool result must go straight back to the LLM "+
			"without waiting for the user, and this test never sent a message (a stored role=error message "+
			"in the DB would mean the turn died before reaching the second call); stored errors: %v",
			streamCalls.Load(), storedErrorRows(t, te, taskID))
	}

	// It really was unprovoked: nothing arrived on the user-message channel.
	select {
	case um := <-setup.userMsgChan:
		t.Fatalf("a user message reached the executor even though the test sent none: %+v", um)
	default:
	}

	// And after the text answer it settles: no third call, and the task is no longer running.
	time.Sleep(500 * time.Millisecond)
	if got := streamCalls.Load(); got != 2 {
		t.Errorf("streaming LLM calls = %d after a text answer, want 2: a settled turn must wait for the user, not loop", got)
	}
	row, err := te.store.GetTaskByID(taskID)
	if err != nil {
		t.Fatalf("read task row: %v", err)
	}
	if row.ExecutionState == types.ExecStateRunning {
		t.Errorf("execution state still running after the text answer; the executor never went idle")
	}
}

// TestExecuteTask_QuotedSeparatorEndsNoTurn is the regression for a review turn that died because the loop
// detector read a bash command by splitting its raw text on `&&`: the separator sat inside a quoted
// argument, the text after it began with `|`, and the read-only classification panicked on the executor's
// own goroutine — outside the protection around tool execution, so a hint decision ended the run.
func TestExecuteTask_QuotedSeparatorEndsNoTurn(t *testing.T) {
	t.Parallel()
	var streamCalls atomic.Int32
	te, _, projectID, initialSHA := setupExecutorWithGit(t)
	const taskID = "quoted-separator-task"
	const branch = "task/" + taskID

	bashArgs, err := json.Marshal(map[string]string{"command": `grep "a&&|b" missing.txt`})
	if err != nil {
		t.Fatalf("marshal tool args: %v", err)
	}
	srv := mockToolThenTextLLM(&streamCalls, types.ToolBash, string(bashArgs))
	defer srv.Close()

	startMockedTurn(t, te, projectID, taskID, branch, srv.URL, initialSHA)

	if !waitForStreamCalls(&streamCalls, 2) {
		t.Fatalf("streaming LLM calls = %d, want >= 2: the turn must survive the tool round", streamCalls.Load())
	}
	if errs := storedErrorRows(t, te, taskID); len(errs) > 0 {
		t.Errorf("stored error rows = %v, want none: the run ended on a server fault", errs)
	}
	if row := mustGetTask(t, te, taskID); strings.Contains(row.Output, "Paused by user") {
		t.Errorf("task output = %q: a turn that completed must not read as a stopped one", row.Output)
	}
}

func mustGetTask(t *testing.T, te *TaskExecutor, id string) *types.Task {
	t.Helper()
	task, err := te.store.GetTaskByID(id)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	return task
}

func mustGetProject(t *testing.T, te *TaskExecutor, id string) *types.Project {
	t.Helper()
	proj, err := te.store.GetProject(id)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	return proj
}
