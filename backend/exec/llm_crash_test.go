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

// A mock that ends the body without sseDone leaves what a dying provider leaves: streamed frames and
// no terminal frame. Reading that to EOF is no error at the HTTP layer, so only the client decides
// whether the provider died.
func sseFrame(content, finishReason string) string {
	return fmt.Sprintf(`data: {"choices":[{"delta":{"content":%q},"finish_reason":%s}]}`+"\n\n", content, finishReason)
}

const sseDone = "data: [DONE]\n\n"

// sseText answers with a text completion. It ends in a question on purpose: a statement with
// finish_reason=stop reads as a repeated-statement loop, which ends the run for an unrelated reason.
func sseText(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, sseFrame(content, "null"))
	fmt.Fprint(w, sseFrame("", `"stop"`))
	fmt.Fprint(w, sseDone)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// sseToolCall answers with one read_file call. The path need not exist: the turn only needs a result.
func sseToolCall(w http.ResponseWriter, id, path string) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":%q,"type":"function","function":{"name":"read_file","arguments":%q}}]},"finish_reason":"tool_calls"}]}`+"\n\n",
		id, fmt.Sprintf(`{"path":%q}`, path))
	fmt.Fprint(w, sseDone)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// sseStreamedThenAbort streams text, then ends the body with no [DONE] and no finish_reason.
func sseStreamedThenAbort(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, sseFrame(content, "null"))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// sseEmptyCompletion is a well-formed completion with nothing in it: the terminal frames are there,
// the answer is not.
func sseEmptyCompletion(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, sseFrame("", `"stop"`))
	fmt.Fprint(w, sseDone)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// sseAbortedBeforeFirstFrame answers 200 with headers and nothing else.
func sseAbortedBeforeFirstFrame(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// crashLLM serves a scripted sequence of streaming answers, one per call, and counts the calls. A
// sequence shorter than the calls made repeats its last answer.
func crashLLM(t *testing.T, answers ...func(http.ResponseWriter)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		n := int(calls.Add(1)) - 1
		if n >= len(answers) {
			n = len(answers) - 1
		}
		answers[n](w)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// startExecRun runs an executor for a task that already has one user message. The caller waits for
// whatever it asserts on; cleanup cancels the run.
func startExecRun(t *testing.T, te *TaskExecutor, projectID, taskID, serverURL, worktreePath string) *taskExecSetup {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	setup := &taskExecSetup{
		task:           mustGetTask(t, te, taskID),
		project:        mustGetProject(t, te, projectID),
		projectID:      projectID,
		worktreePath:   worktreePath,
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
		case <-time.After(20 * time.Second):
			t.Error("executor did not exit after cancel")
		}
	})
	return setup
}

// crashTask creates the task, its user message and the worktree a tool turn needs, on the project
// setupExecutorWithGit built.
func crashTask(t *testing.T, te *TaskExecutor, projectID, initialSHA, taskID, serverURL string) (worktreePath string) {
	t.Helper()
	now := time.Now().UTC()
	if err := te.store.CreateTask(&types.Task{
		ID:              taskID,
		ProjectID:       projectID,
		Title:           "Crash",
		Prompt:          "Read the file",
		Status:          types.StatusInProgress,
		ExecutionState:  types.ExecStateRunning,
		LlamaServerURL:  serverURL,
		ModelName:       "test-model",
		OperatingMode:   "act",
		Branch:          "task/" + taskID,
		BaseCommit:      initialSHA,
		CurrentThreadID: "thread-" + taskID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := te.store.CreateTaskMessage(&types.TaskMessage{
		ID: uuid.New().String(), TaskID: taskID, ThreadID: "thread-" + taskID,
		Role: "user", Content: "Read no-such-file.txt", CreatedAt: now,
	}); err != nil {
		t.Fatalf("create message: %v", err)
	}
	wt, _, err := te.worktreeMgr.CreateWorktree(projectID, taskID, "task/"+taskID, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	return wt
}

// rolesInOrder lists the thread's stored roles, oldest first.
func rolesInOrder(t *testing.T, te *TaskExecutor, taskID, threadID string) []string {
	t.Helper()
	msgs, err := te.store.GetTaskMessages(taskID, threadID)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	roles := make([]string, 0, len(msgs))
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	return roles
}

// TestLLMCrashMidTurnWithToolCallLeavesNoPartialTurn pins the stored conversation when the provider
// dies mid-answer on a turn that already ran tools. The dead call must leave no assistant row: that
// row is what marks its prompt as answered.
func TestLLMCrashMidTurnWithToolCallLeavesNoPartialTurn(t *testing.T) {
	srv, calls := crashLLM(t,
		func(w http.ResponseWriter) { sseToolCall(w, "call_1", "no-such-file.txt") },
		func(w http.ResponseWriter) { sseStreamedThenAbort(w, "Let me explain what I found so") },
	)
	te, _, projectID, initialSHA := setupExecutorWithGit(t)
	const taskID = "crash-mid-turn"
	worktreePath := crashTask(t, te, projectID, initialSHA, taskID, srv.URL)

	startExecRun(t, te, projectID, taskID, srv.URL, worktreePath)

	// Every retry dies the same way, so wait for the run to settle on its own.
	waitForFailureRow(t, te, taskID, 60*time.Second)

	row := mustGetTask(t, te, taskID)
	if row.ExecutionState != types.ExecStateFailed {
		t.Errorf("execution state = %q, want %q: a dead provider must end the run, not leave it running",
			row.ExecutionState, types.ExecStateFailed)
	}
	if row.Status != types.StatusReview {
		t.Errorf("status = %q, want %q: the user picks a dead provider's task up from Review", row.Status, types.StatusReview)
	}
	if calls.Load() < 2 {
		t.Errorf("streaming calls = %d, want the aborted call retried", calls.Load())
	}

	msgs, err := te.store.GetTaskMessages(taskID, "thread-"+taskID)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	// The tool turn really ran, or this asserts nothing about a mid-turn crash.
	var sawAssistant, sawTool bool
	for _, m := range msgs {
		switch m.Role {
		case "assistant":
			sawAssistant = true
		case "tool_call":
			sawTool = true
		}
	}
	if !sawAssistant || !sawTool {
		t.Fatalf("the run never got as far as a tool turn (assistant=%v tool_call=%v, roles = %v)", sawAssistant, sawTool, rolesInOrder(t, te, taskID, "thread-"+taskID))
	}
	for _, m := range msgs {
		if m.Role == "assistant" && strings.Contains(m.Content, "Let me explain") {
			t.Errorf("a call that died mid-stream left its partial text stored as an assistant answer: %q", m.Content)
		}
	}
	if lastError := lastErrorRow(msgs); lastError == "" {
		t.Errorf("no error row stored; roles = %v", rolesInOrder(t, te, taskID, "thread-"+taskID))
	}
}

// lastErrorRow returns the content of the thread's last error row, or "".
// waitForFailureRow waits until the run is failed and its error row is stored. `failTask` writes the
// failed task row before the error row, so the state alone does not mean the thread is complete —
// reading it between those two writes is a red under load.
func waitForFailureRow(t *testing.T, te *TaskExecutor, taskID string, wait time.Duration) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if mustGetTask(t, te, taskID).ExecutionState == types.ExecStateFailed &&
			lastErrorRow(mustMessages(t, te, taskID, "thread-"+taskID)) != "" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func lastErrorRow(msgs []types.TaskMessage) string {
	last := ""
	for _, m := range msgs {
		if m.Role == "error" {
			last = m.Content
		}
	}
	return last
}

// TestLLMProviderDiesBeforeFirstFrameRetriesThenFailsIntoReview covers the provider that dies after
// the headers and before its first frame: the body ends cleanly, so the read reports no error.
func TestLLMProviderDiesBeforeFirstFrameRetriesThenFailsIntoReview(t *testing.T) {
	srv, calls := crashLLM(t, sseAbortedBeforeFirstFrame)
	te, _, projectID, initialSHA := setupExecutorWithGit(t)
	const taskID = "crash-no-frames"
	worktreePath := crashTask(t, te, projectID, initialSHA, taskID, srv.URL)

	startExecRun(t, te, projectID, taskID, srv.URL, worktreePath)

	waitForFailureRow(t, te, taskID, 60*time.Second)

	row := mustGetTask(t, te, taskID)
	if row.ExecutionState != types.ExecStateFailed || row.Status != types.StatusReview {
		t.Fatalf("after a provider that returned nothing: execution=%q status=%q, want failed/review (a task left "+
			"running is what leaves the client waiting on an answer that already arrived as nothing)",
			row.ExecutionState, row.Status)
	}
	if calls.Load() < 2 {
		t.Errorf("streaming calls = %d, want the aborted call retried", calls.Load())
	}
	msgs := mustMessages(t, te, taskID, "thread-"+taskID)
	if err := lastErrorRow(msgs); !strings.Contains(err, "[llm_connection_failed]") {
		t.Errorf("error row = %q, want the llm_connection_failed one; roles = %v", err, rolesInOrder(t, te, taskID, "thread-"+taskID))
	}
	assertPromptStillUnanswered(t, msgs)
}

// TestLLMCompletionWithNoContentFailsIntoReview covers the other nothing: a full completion -
// terminal frames and all - with no text, reasoning or tool call. Nothing to retry, nothing to
// settle on, and no reason for the task to keep waiting.
func TestLLMCompletionWithNoContentFailsIntoReview(t *testing.T) {
	srv, calls := crashLLM(t, sseEmptyCompletion)
	te, _, projectID, initialSHA := setupExecutorWithGit(t)
	const taskID = "answered-with-nothing"
	worktreePath := crashTask(t, te, projectID, initialSHA, taskID, srv.URL)

	startExecRun(t, te, projectID, taskID, srv.URL, worktreePath)

	waitForFailureRow(t, te, taskID, 30*time.Second)

	row := mustGetTask(t, te, taskID)
	if row.ExecutionState != types.ExecStateFailed || row.Status != types.StatusReview {
		t.Fatalf("after a completion with no content: execution=%q status=%q, want failed/review",
			row.ExecutionState, row.Status)
	}
	if calls.Load() != 1 {
		t.Errorf("streaming calls = %d, want 1: a completion that answered fully is not retried", calls.Load())
	}
	msgs := mustMessages(t, te, taskID, "thread-"+taskID)
	if err := lastErrorRow(msgs); !strings.Contains(err, "[llm_empty_response]") {
		t.Errorf("error row = %q, want the llm_empty_response one; roles = %v", err, rolesInOrder(t, te, taskID, "thread-"+taskID))
	}
	assertPromptStillUnanswered(t, msgs)
}

// assertPromptStillUnanswered checks the thread tail a retry has to pick up: the user's message is
// the last turn stored. Retry and error rows say what happened to the run, not what was answered.
func assertPromptStillUnanswered(t *testing.T, msgs []types.TaskMessage) {
	t.Helper()
	last := ""
	for _, m := range msgs {
		switch m.Role {
		case "user", "assistant", "tool_call":
			last = m.Role
		}
	}
	if last != "user" {
		t.Errorf("last answered turn = %q, want %q: the prompt is still the one waiting for an answer", last, "user")
	}
}

// mustMessages reads a thread's rows or fails the test.
func mustMessages(t *testing.T, te *TaskExecutor, taskID, threadID string) []types.TaskMessage {
	t.Helper()
	msgs, err := te.store.GetTaskMessages(taskID, threadID)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	return msgs
}

// TestAbortedStreamIsRetriedAndTheTurnCompletes: a provider that dies once is asked again by itself,
// and the retry's answer is the turn's answer.
func TestAbortedStreamIsRetriedAndTheTurnCompletes(t *testing.T) {
	srv, calls := crashLLM(t,
		func(w http.ResponseWriter) { sseStreamedThenAbort(w, "Let me explain what I found so") },
		func(w http.ResponseWriter) { sseText(w, "The file is not there. Should I create it?") },
	)
	te, _, projectID, initialSHA := setupExecutorWithGit(t)
	const taskID = "crash-then-answered"
	worktreePath := crashTask(t, te, projectID, initialSHA, taskID, srv.URL)

	startExecRun(t, te, projectID, taskID, srv.URL, worktreePath)

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && calls.Load() < 2 {
		time.Sleep(20 * time.Millisecond)
	}
	if got := calls.Load(); got < 2 {
		t.Fatalf("streaming calls = %d, want 2: an aborted stream must be retried without anyone asking", got)
	}
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if len(rolesInOrder(t, te, taskID, "thread-"+taskID)) > 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	roles := rolesInOrder(t, te, taskID, "thread-"+taskID)
	var answered, interrupted, partial int
	msgs, err := te.store.GetTaskMessages(taskID, "thread-"+taskID)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	for _, m := range msgs {
		switch {
		case m.Role == "assistant" && strings.Contains(m.Content, "The file is not there"):
			answered++
		case m.Role == "assistant" && strings.Contains(m.Content, "Let me explain"):
			partial++
		case m.Role == "system" && strings.Contains(m.Content, "Connection interrupted"):
			interrupted++
		}
	}
	if answered != 1 {
		t.Errorf("assistant answer stored %d times, want 1; roles = %v", answered, roles)
	}
	if partial != 0 {
		t.Errorf("the aborted call's half sentence was stored %d times, want 0", partial)
	}
	if interrupted != 1 {
		t.Errorf("retry notification stored %d times, want exactly 1 for the whole retry sequence; roles = %v", interrupted, roles)
	}
}
