package exec

// These runs drive a real executor against a scripted provider, so the turn that trips the limit, the cut
// and the message that follows are the ones a person would see.

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"mike/backend/types"
)

// failingBash answers at once and always fails, so the counter reaches its limit quickly.
var failingBash = scriptToolCall{id: "call_1", name: "bash", args: `{"command":"exit 7"}`}

// errRewindRefused is a truncation that did not happen.
var errRewindRefused = errors.New("rewind refused")

// cutPoint is one row the run named, remembered with its role: the cut deletes the row, so afterwards
// nothing in the database says which row was named.
type cutPoint struct {
	id   string
	role string
}

// breakerCuts records which row each break named, and truncates it. The steps truncateMessagesFrom adds
// around the delete are its own tests' business; the contract here is which row gets named.
type breakerCuts struct {
	mu     sync.Mutex
	named  []cutPoint
	te     *TaskExecutor
	thread string
	err    error
}

func (c *breakerCuts) rewind(projectID, taskID, messageID string) error {
	if c.err != nil {
		return c.err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	role := "missing"
	if row, err := c.te.store.GetTaskMessageByID(messageID); err == nil {
		role = row.Role
	}
	c.named = append(c.named, cutPoint{id: messageID, role: role})
	_, err := c.te.store.DeleteTaskMessagesAfter(taskID, messageID, c.thread)
	return err
}

func (c *breakerCuts) names() []cutPoint {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]cutPoint{}, c.named...)
}

// newBreakerRun starts a run with the breaker on, the truncation recorded, and the failing command
// repeated as many times as the scripts ask for.
func newBreakerRun(t *testing.T, task *types.Task, cuts *breakerCuts, scripts ...string) *interruptRun {
	t.Helper()
	te, repoDir, projectID, initialSHA := setupExecutorWithGit(t)
	if err := te.store.SetConfigValue("loop_breaker", "true"); err != nil {
		t.Fatalf("enable the loop breaker: %v", err)
	}
	cuts.te = te
	cuts.thread = "thread-1"
	te.rewindConversation = cuts.rewind
	return newInterruptRunWith(t, te, repoDir, projectID, initialSHA, task, scripts...)
}

// rowsOf lists the conversation rows of one role, oldest first.
func rowsOf(t *testing.T, te *TaskExecutor, taskID, role, content string) []types.TaskMessage {
	t.Helper()
	msgs, err := te.store.GetTaskMessages(taskID, "thread-1")
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}
	var found []types.TaskMessage
	for _, m := range msgs {
		if m.Role == role && (content == "" || m.Content == content) {
			found = append(found, m)
		}
	}
	return found
}

// A run that repeats one failing call is cut at the first of them, asked to proceed, and gets to answer.
func TestLoopBreakerCutsTheFailedSeriesAndContinues(t *testing.T) {
	t.Parallel()
	const maxTurns = 3
	var cuts breakerCuts
	run := newBreakerRun(t, &types.Task{Title: "Break", Prompt: "Work", MaxConsecutiveFailedTurns: maxTurns},
		&cuts,
		toolCallsScript(failingBash), toolCallsScript(failingBash), toolCallsScript(failingBash),
		textScript("That approach is not working. What should I try instead?"))

	// The break's own call is the fourth.
	run.awaitStreamCalls(t, maxTurns+1)

	named := cuts.names()
	if len(named) != 1 {
		t.Fatalf("rewinds = %d (%v), want one for the series that tripped the limit", len(named), named)
	}
	if named[0].role != "assistant" {
		t.Errorf("the cut named a %s row; a turn that said nothing but the failed call goes with it", named[0].role)
	}

	// The whole series went with the cut.
	if rows := run.toolRows(t); len(rows) != 0 {
		t.Errorf("tool_call rows after the break = %d, want 0 — the cut must take the whole series", len(rows))
	}

	proceed := rowsOf(t, run.te, run.taskID, "user", proceedInstruction)
	if len(proceed) != 1 {
		t.Fatalf("%q rows = %d, want exactly 1", proceedInstruction, len(proceed))
	}
	notes := rowsOf(t, run.te, run.taskID, "system", "")
	if len(notes) != 1 || notes[0].Kind != loopBreakerNoteKind {
		t.Fatalf("system notes = %+v, want one kinded %q", notes, loopBreakerNoteKind)
	}

	// The next call is built from the cut conversation.
	body := run.llm.body(maxTurns + 1)
	if strings.Contains(body, "exit 7") {
		t.Error("the call after the break still carries the failed tool call")
	}
	if !strings.Contains(body, proceedInstruction) {
		t.Error("the call after the break does not carry the Proceed instruction")
	}

	if errs := rowsOf(t, run.te, run.taskID, "error", ""); len(errs) != 0 {
		t.Errorf("the run reported %v; a broken loop is not a failed task", errs[0].Content)
	}
}

// A turn that said something keeps its row; only its call and what followed it are cut.
func TestLoopBreakerKeepsTheTurnThatSpoke(t *testing.T) {
	t.Parallel()
	spoken := toolCallsScriptWith("Let me check the build.", failingBash)
	var cuts breakerCuts
	run := newBreakerRun(t, &types.Task{Title: "Spoken", Prompt: "Work", MaxConsecutiveFailedTurns: 1},
		&cuts, spoken, textScript("The build output is missing. Shall I run it again?"))

	run.awaitStreamCalls(t, 2)

	named := cuts.names()
	if len(named) != 1 {
		t.Fatalf("rewinds = %d (%v), want one", len(named), named)
	}
	if named[0].role != "tool_call" {
		t.Errorf("the cut named a %s row; the turn that spoke survives, so the call itself is the cut", named[0].role)
	}
	var kept bool
	for _, m := range rowsOf(t, run.te, run.taskID, "assistant", "") {
		if strings.Contains(m.Content, "Let me check the build.") {
			kept = true
		}
	}
	if !kept {
		t.Error("the assistant turn that spoke was cut along with its failed call")
	}
	if len(rowsOf(t, run.te, run.taskID, "user", proceedInstruction)) != 1 {
		t.Errorf("no %q instruction followed the cut", proceedInstruction)
	}
}

// With the preference off the limit ends the run and stores the failure.
func TestLoopBreakerOffStillEndsTheRun(t *testing.T) {
	t.Parallel()
	te, repoDir, projectID, initialSHA := setupExecutorWithGit(t)
	var cuts breakerCuts
	cuts.te = te
	te.rewindConversation = cuts.rewind // wired but unused: the preference is what declines
	run := newInterruptRunWith(t, te, repoDir, projectID, initialSHA,
		&types.Task{Title: "No breaker", Prompt: "Work", MaxConsecutiveFailedTurns: 2},
		toolCallsScript(failingBash), toolCallsScript(failingBash), textScript("Anything else?"))

	select {
	case <-run.done:
	case <-time.After(30 * time.Second):
		t.Fatal("the run did not end at its failed-turn limit")
	}

	if names := cuts.names(); len(names) != 0 {
		t.Errorf("the conversation was truncated (%v) while the breaker is off", names)
	}
	errs := rowsOf(t, te, run.taskID, "error", "")
	if len(errs) != 1 || !strings.Contains(errs[0].Content, "max_failures") {
		t.Errorf("error rows = %+v, want the stored max_failures report", errs)
	}
	if got := len(rowsOf(t, te, run.taskID, "user", proceedInstruction)); got != 0 {
		t.Errorf("%q rows = %d, want none while the breaker is off", proceedInstruction, got)
	}
	if got := run.llm.streamCalls(); got != 2 {
		t.Errorf("provider calls = %d, want 2 — the run stopped at its limit", got)
	}
}

// rewindPoint reads the stored rows the way the breaker does; the run tests above show the two agree.
func TestRewindPointNamesTheFirstFailedCall(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	const args = `{"command":"exit 7"}`
	taskID := "cut-task"
	thread := "thread-1"
	now := time.Now().UTC()
	if err := s.CreateProject(&types.Project{ID: "cut-project", Name: "cut", Path: "/tmp/cut", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(&types.Task{
		ID: taskID, ProjectID: "cut-project", Title: "cut", Prompt: "p", Status: types.StatusInProgress,
		ExecutionState: types.ExecStateIdle, CurrentThreadID: thread, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	row := func(id, role, content, toolCalls, toolResult, owner string) {
		if err := s.CreateTaskMessage(&types.TaskMessage{
			ID: id, TaskID: taskID, ThreadID: thread, Role: role, Content: content,
			ToolCalls: toolCalls, ToolResult: toolResult, AssistantMsgID: owner,
			CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	call := func(id, owner string) string {
		return `{"tool_id":"` + id + `","tool_name":"bash","raw_args":` + jsonQuoted(args) + `}`
	}

	row("a1", "assistant", "", "", "", "")
	row("t1", "tool_call", "", call("call_1", "a1"), `{"output":"exit status 7","success":false}`, "a1")
	row("a2", "assistant", "Let me check the build.", "", "", "")
	row("t2", "tool_call", "", call("call_2", "a2"), `{"output":"exit status 7","success":false}`, "a2")
	row("a3", "assistant", "", "", "", "")
	row("t3", "tool_call", "", call("call_3", "a3"), `{"output":"fine","success":true}`, "a3")

	msgs, err := s.GetTaskMessages(taskID, thread)
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}

	// The turn that said nothing goes with its call, and the earliest failure is the one named.
	if cut, ok := rewindPoint(msgs, "bash", args); !ok || cut != "a1" {
		t.Errorf("rewindPoint = %q,%v, want the first failed turn a1", cut, ok)
	}

	// A call that succeeded is not the failure to cut.
	if cut, ok := rewindPoint(msgs, "bash", `{"command":"echo fine"}`); ok {
		t.Errorf("rewindPoint cut %q for a call that succeeded", cut)
	}
}

// When the cut cannot be made, the failure is reported as it always was.
func TestLoopBreakerDeclinesWhenNothingCanBeCut(t *testing.T) {
	t.Parallel()
	const maxTurns = 2
	var cuts breakerCuts
	cuts.err = errRewindRefused
	run := newBreakerRun(t, &types.Task{Title: "Refused", Prompt: "Work", MaxConsecutiveFailedTurns: maxTurns},
		&cuts,
		toolCallsScript(failingBash), toolCallsScript(failingBash), textScript("Anything else?"))

	select {
	case <-run.done:
	case <-time.After(30 * time.Second):
		t.Fatal("the run did not end after the cut was refused")
	}

	if got := len(rowsOf(t, run.te, run.taskID, "user", proceedInstruction)); got != 0 {
		t.Errorf("%q rows = %d, want none after a refused cut", proceedInstruction, got)
	}
	if errs := rowsOf(t, run.te, run.taskID, "error", ""); len(errs) != 1 {
		t.Errorf("error rows = %+v, want the failure the run fell back to reporting", errs)
	}
}
