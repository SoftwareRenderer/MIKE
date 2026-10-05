package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mike/backend/stream"
	"mike/backend/testutil/testgit"
	"mike/backend/types"

	"github.com/google/uuid"
)

// A wake that was already buffered, or whose message was consumed at the top of the loop, must not
// stop a call that has a right to finish: only a channel that grew since the watch began counts.
func TestToolCallContextIgnoresAStaleHint(t *testing.T) {
	t.Parallel()
	setup := &taskExecSetup{ctx: context.Background(), userMsgChan: make(chan stream.UserMessage, 16), msgWake: make(chan struct{}, 1)}
	setup.msgWake <- struct{}{}

	toolCtx, cancel := toolCallContext(setup)
	defer cancel()

	select {
	case <-toolCtx.Done():
		t.Fatal("the buffered hint alone cancelled the tool calls")
	case <-time.After(200 * time.Millisecond):
	}

	setup.userMsgChan <- stream.UserMessage{Content: "new"}
	wake(setup.msgWake)
	select {
	case <-toolCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a message that arrived after the watch began did not cancel the tool calls")
	}
}

// scriptedStreamLLM answers each streaming call with the next script (the last one repeats) and every
// non-streaming call with plain text, so a utility call never stalls a run. It records the request
// bodies so a test can read what the provider was told.
type scriptedStreamLLM struct {
	server  *httptest.Server
	mu      sync.Mutex
	scripts []string
	bodies  []string
}

func newScriptedStreamLLM(t *testing.T, scripts ...string) *scriptedStreamLLM {
	t.Helper()
	l := &scriptedStreamLLM{scripts: scripts}
	l.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var head struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(raw, &head)
		if !head.Stream {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"summary"},"finish_reason":"stop"}]}`))
			return
		}
		l.mu.Lock()
		idx := len(l.bodies)
		l.bodies = append(l.bodies, string(raw))
		script := l.scripts[len(l.scripts)-1]
		if idx < len(l.scripts) {
			script = l.scripts[idx]
		}
		l.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, script)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(l.server.Close)
	return l
}

func (l *scriptedStreamLLM) streamCalls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.bodies)
}

// body returns the n-th streaming request (1-indexed).
func (l *scriptedStreamLLM) body(n int) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n > len(l.bodies) {
		return ""
	}
	return l.bodies[n-1]
}

// scriptToolCall is one tool call the scripted answer asks for.
type scriptToolCall struct {
	id   string
	name string
	args string // JSON object text, as the provider sends it
}

// toolCallsScript renders the calls as the delta chunk the client assembles, escaping the args text
// into a JSON string so a test writes it the way the model does.
func toolCallsScript(calls ...scriptToolCall) string {
	parts := make([]string, 0, len(calls))
	for i, c := range calls {
		args, err := json.Marshal(c.args)
		if err != nil {
			panic(err)
		}
		parts = append(parts, fmt.Sprintf(`{"index":%d,"id":%q,"type":"function","function":{"name":%q,"arguments":%s}}`,
			i, c.id, c.name, string(args)))
	}
	return `data: {"choices":[{"delta":{"tool_calls":[` + strings.Join(parts, ",") +
		`]},"finish_reason":"tool_calls"}]}` + "\n\n"
}

// textScript ends a turn with a question, so the loop detector does not end the run for an
// unrelated reason (see mockToolThenTextLLM).
func textScript(text string) string {
	return fmt.Sprintf(`data: {"choices":[{"delta":{"content":%q},"finish_reason":null}]}`+"\n\n"+
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":6,"total_tokens":16}}`+"\n\n", text)
}

// interruptRun is one LLM turn running against a real worktree, driven only by the scripted
// provider and the user messages the test forwards.
type interruptRun struct {
	te           *TaskExecutor
	projectID    string
	taskID       string
	repoDir      string
	worktreePath string
	setup        *taskExecSetup
	cancel       context.CancelFunc
	done         chan struct{}
	llm          *scriptedStreamLLM
}

// bashSleepThen announces it started and then runs far longer than the test waits, so only an
// interrupt ends it early and the run's duration measures whether one happened.
const bashSleepThen = `echo started > started.txt; sleep 120`

func newInterruptRun(t *testing.T, task *types.Task, scripts ...string) *interruptRun {
	t.Helper()
	te, repoDir, projectID, initialSHA := setupExecutorWithGit(t)
	task.ID = "interrupt-task"
	task.ProjectID = projectID
	task.Status = types.StatusInProgress
	task.ExecutionState = types.ExecStateRunning
	task.OperatingMode = "act"
	task.Branch = "task/" + task.ID
	task.BaseCommit = initialSHA
	task.CurrentThreadID = "thread-1"
	task.CreatedAt = time.Now().UTC()
	task.UpdatedAt = time.Now().UTC()
	if err := te.store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := te.store.CreateTaskMessage(&types.TaskMessage{
		ID: uuid.New().String(), TaskID: task.ID, ThreadID: task.CurrentThreadID,
		Role: "user", Content: "Get to work", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create message: %v", err)
	}

	run := &interruptRun{te: te, projectID: projectID, taskID: task.ID, repoDir: repoDir}
	worktreePath, _, err := te.worktreeMgr.CreateWorktree(projectID, task.ID, task.Branch, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	run.worktreePath = worktreePath

	run.llm = newScriptedStreamLLM(t, scripts...)
	ctx, cancel := context.WithCancel(context.Background())
	run.cancel = cancel
	run.done = make(chan struct{})
	run.setup = &taskExecSetup{
		task: mustGetTask(t, te, task.ID), project: mustGetProject(t, te, projectID),
		projectID: projectID, worktreePath: worktreePath,
		serverURL: run.llm.server.URL, modelName: "test-model",
		ctx: ctx, cancel: cancel,
		userMsgChan: make(chan stream.UserMessage, 16),
		msgWake:     make(chan struct{}, 1),
		summarizeCh: make(chan *summarizeRequest, 16),
		done:        run.done,
	}
	te.taskMu.Lock()
	te.tasks[task.ID] = &executionState{
		ctx: ctx, cancel: cancel,
		userMsgChan:  run.setup.userMsgChan,
		msgWake:      run.setup.msgWake,
		summarizeCh:  run.setup.summarizeCh,
		executorType: "llm",
		done:         run.done,
	}
	te.taskMu.Unlock()

	go te.executeTask(run.setup)
	t.Cleanup(func() {
		cancel()
		select {
		case <-run.done:
		case <-time.After(20 * time.Second):
			t.Error("executor did not exit after cancel")
		}
	})
	return run
}

// send is what HandleSend does to a running executor: the row first, then the message, because the
// turn rebuilds its context from the database.
func (r *interruptRun) send(t *testing.T, content string) {
	if err := r.te.store.CreateTaskMessage(&types.TaskMessage{
		ID: uuid.New().String(), TaskID: r.taskID, ThreadID: "thread-1",
		Role: "user", Content: content, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create message: %v", err)
	}
	r.te.ForwardUserMessage(r.projectID, r.taskID, stream.UserMessage{Content: content})
}

// awaitFile waits for a command in the worktree to create path, which is how a test knows a tool call
// is actually running rather than merely scheduled.
func (r *interruptRun) awaitFile(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(r.worktreePath, name)); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never appeared in the worktree; the tool call never started", name)
}

func (r *interruptRun) awaitStreamCalls(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && r.llm.streamCalls() < n {
		time.Sleep(20 * time.Millisecond)
	}
	if got := r.llm.streamCalls(); got < n {
		t.Fatalf("streaming LLM calls = %d, want >= %d", got, n)
	}
}

// toolRows returns every tool_call row's stored result, in creation order.
func (r *interruptRun) toolRows(t *testing.T) []types.TaskMessage {
	t.Helper()
	msgs, err := r.te.store.GetTaskMessages(r.taskID, "thread-1")
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}
	var rows []types.TaskMessage
	for _, m := range msgs {
		if m.Role == "tool_call" {
			rows = append(rows, m)
		}
	}
	return rows
}

// storedResult decodes what a page reload reads back for one tool card.
func storedResult(t *testing.T, row types.TaskMessage) map[string]any {
	t.Helper()
	if row.ToolResult == "" {
		t.Fatalf("tool call %s has no stored result; the card renders as still running", row.ID)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(row.ToolResult), &got); err != nil {
		t.Fatalf("decode stored result %q: %v", row.ToolResult, err)
	}
	return got
}

// A user message must not queue behind a command that runs for minutes: it kills the call that is
// running and the calls that have not started, and the provider is told both.
func TestUserMessageInterruptsRunningToolCall(t *testing.T) {
	t.Parallel()
	run := newInterruptRun(t, &types.Task{Title: "Interrupt", Prompt: "Work"},
		toolCallsScript(
			scriptToolCall{id: "call_1", name: "bash", args: `{"command":"` + bashSleepThen + `","timeout":90}`},
			scriptToolCall{id: "call_2", name: "bash", args: `{"command":"echo second > second.txt"}`},
		),
		textScript("Stopping. What should I do instead?"),
	)

	run.awaitStreamCalls(t, 1)
	run.awaitFile(t, "started.txt")
	started := time.Now()

	run.send(t, "Stop, do something else")

	run.awaitStreamCalls(t, 2)
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Errorf("the second call came %v after the message; the running command was not killed", elapsed)
	}

	rows := run.toolRows(t)
	if len(rows) != 2 {
		t.Fatalf("tool_call rows = %d, want 2 (both calls of the turn are asked for and answered)", len(rows))
	}
	for _, row := range rows {
		got := storedResult(t, row)
		if got["output"] != interruptedToolResult {
			t.Errorf("tool %v stored output %v, want %q", row.ID, got["output"], interruptedToolResult)
		}
		if got["success"] != false {
			t.Errorf("tool %v stored success %v, want false", row.ID, got["success"])
		}
		if got["interrupted"] != true {
			t.Errorf("tool %v stored interrupted %v, want true", row.ID, got["interrupted"])
		}
	}

	// The call that never started left no trace in the worktree.
	if _, err := os.Stat(filepath.Join(run.worktreePath, "second.txt")); err == nil {
		t.Error("second.txt exists: the call after the interrupt ran anyway")
	}

	// And the killed call let go of the worktree: the next turn must not queue behind a command
	// that is no longer running.
	lockFreed := make(chan struct{})
	go func() { run.te.LockWorktree(run.taskID)(); close(lockFreed) }()
	select {
	case <-lockFreed:
	case <-time.After(5 * time.Second):
		t.Error("the worktree lock is still held after the interrupt")
	}

	// The provider was told the calls were interrupted and given the message that did it.
	second := run.llm.body(2)
	if !strings.Contains(second, interruptedToolResult) {
		t.Error("the second call's context does not carry the interrupted results")
	}
	if !strings.Contains(second, "Stop, do something else") {
		t.Error("the second call's context does not carry the user's message")
	}
}

// The row exists before the call runs, so the frame naming it cannot wait for the answer.
func TestRunningToolCallNamesItsRowWhileItRuns(t *testing.T) {
	t.Parallel()
	const marker = "started.txt"
	running := toolCallsScript(scriptToolCall{id: "call_1", name: "bash", args: `{"command":"` + bashSleepThen + `","timeout":90}`})
	run := newInterruptRun(t, &types.Task{Title: "Row id", Prompt: "Work"},
		running,
		toolCallsScript(scriptToolCall{id: "call_2", name: "bash", args: `{"command":"` + bashSleepThen + `","timeout":90}`}),
	)

	run.awaitStreamCalls(t, 1)
	run.awaitFile(t, marker)

	// Everything the second turn emits lands inside the capture, which ends while its command runs.
	frames := taskStreamFramesDuring(t, run.te.appStreamMgr, run.projectID, run.taskID, func() {
		if err := os.Remove(filepath.Join(run.worktreePath, marker)); err != nil {
			t.Errorf("clear the first turn's marker: %v", err)
			return
		}
		run.send(t, "Stop")
		run.awaitStreamCalls(t, 2)
		run.awaitFile(t, marker)
	})

	rows := run.toolRows(t)
	if len(rows) < 2 {
		t.Fatalf("tool_call rows = %d, want one per turn", len(rows))
	}
	secondRow := rows[len(rows)-1]
	var called struct {
		ID string `json:"tool_id"`
	}
	if err := json.Unmarshal([]byte(secondRow.ToolCalls), &called); err != nil {
		t.Fatalf("decode the second turn's tool_call row: %v", err)
	}
	if called.ID != "call_2" {
		t.Fatalf("the newest tool_call row names %q, want call_2 — the capture would weigh the wrong call", called.ID)
	}

	var namedRow, answered bool
	for _, f := range frames {
		switch f.Event {
		case string(types.WSTypeToolProgress):
			if f.Data["tool_id"] == called.ID && f.Data["message_id"] == secondRow.ID {
				namedRow = true
			}
		case string(types.WSTypeToolResult):
			if f.Data["tool_id"] == called.ID {
				answered = true
			}
		}
	}
	if !namedRow {
		t.Errorf("no tool_progress frame named row %s of the running %s; captured %d frames: %s",
			secondRow.ID, called.ID, len(frames), frameEvents(frames))
	}
	if answered {
		t.Error("the captured window holds the call's answer, so it says nothing about a running call")
	}
}

// frameEvents lists the events a capture held.
func frameEvents(frames []taskStreamFrame) string {
	events := make([]string, 0, len(frames))
	for _, f := range frames {
		events = append(events, f.Event)
	}
	return strings.Join(events, ",")
}

// An interrupted turn settles nothing: a request_review in it does not commit, merge or mark Done,
// because the work it was reporting on was stopped on the way.
func TestInterruptedTurnDoesNotCommitOrFinish(t *testing.T) {
	t.Parallel()
	run := newInterruptRun(t, &types.Task{Title: "Interrupt review", Prompt: "Work", AutoCommit: true, AutoMerge: true},
		toolCallsScript(
			scriptToolCall{id: "call_1", name: "bash", args: `{"command":"` + bashSleepThen + `","timeout":90}`},
			scriptToolCall{id: "call_2", name: "request_review", args: `{}`},
		),
		textScript("Stopping. Shall I try the other approach?"),
	)
	before := testgit.GitOutput(t, run.repoDir, "rev-list", "--count", "HEAD")

	run.awaitStreamCalls(t, 1)
	run.awaitFile(t, "started.txt")
	run.send(t, "Wait, not that")
	run.awaitStreamCalls(t, 2)

	if after := testgit.GitOutput(t, run.repoDir, "rev-list", "--count", "HEAD"); after != before {
		t.Errorf("the repository went from %s to %s commits: an interrupted turn committed", before, after)
	}
	row, err := run.te.store.GetTaskByID(run.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status == types.StatusDone {
		t.Error("the task is Done although its turn was interrupted")
	}
}

// The user's own keystroke is not a model failure: turns cut short by a message never reach the
// consecutive-failure count that would otherwise end the run.
func TestInterruptIsNotATurnFailure(t *testing.T) {
	t.Parallel()
	const maxTurns = 2
	run := newInterruptRun(t, &types.Task{
		Title: "Repeat", Prompt: "Work",
		MaxConsecutiveFailedTurns: maxTurns,
	}, toolCallsScript(
		scriptToolCall{id: "call_1", name: "bash", args: `{"command":"` + bashSleepThen + `","timeout":90}`},
	))

	for i := 1; i <= maxTurns+1; i++ {
		run.awaitStreamCalls(t, i)
		run.awaitFile(t, "started.txt")
		if err := os.Remove(filepath.Join(run.worktreePath, "started.txt")); err != nil {
			t.Fatalf("reset the marker for turn %d: %v", i, err)
		}
		run.send(t, fmt.Sprintf("Change of plan %d", i))
	}

	// A counted failure would have ended the run at maxTurns identical turns with a stored error.
	msgs, err := run.te.store.GetTaskMessages(run.taskID, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.Role == "error" {
			t.Fatalf("stored error after %d interrupted turns: %s", maxTurns+1, m.Content)
		}
	}
}

// A stop is a stop: cancelling the task mid-command is reported as the command failing, not as an
// interrupt, and the run makes no further provider call.
func TestStoppingATaskIsNotAnInterrupt(t *testing.T) {
	t.Parallel()
	run := newInterruptRun(t, &types.Task{Title: "Stop", Prompt: "Work"},
		toolCallsScript(
			scriptToolCall{id: "call_1", name: "bash", args: `{"command":"` + bashSleepThen + `","timeout":90}`},
		),
		textScript("Anything else?"),
	)

	run.awaitStreamCalls(t, 1)
	run.awaitFile(t, "started.txt")
	run.cancel()

	select {
	case <-run.done:
	case <-time.After(30 * time.Second):
		t.Fatal("the executor did not finish after the task was stopped")
	}

	for _, row := range run.toolRows(t) {
		got := storedResult(t, row)
		if got["interrupted"] == true {
			t.Errorf("tool %v was recorded as interrupted by a user message; a stop is not a message", row.ID)
		}
	}
	if got := run.llm.streamCalls(); got != 1 {
		t.Errorf("streaming LLM calls = %d after a stop, want 1", got)
	}
}
