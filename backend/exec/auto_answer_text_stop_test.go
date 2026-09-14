package exec

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kanban/backend/stream"
	"kanban/backend/types"

	"github.com/google/uuid"
)

// textStopMock answers the executor's streaming calls and the parent-context answering call
// separately, so a test can tell "the run went back to the LLM" from "someone answered for the
// user". Streaming call 1 is a text-only answer; call 2 asks for review, which ends the run.
//
// Call 2 parks on release when reached is non-nil: the test then reads the task row while the run
// is provably past the automated answer and before the review turn has changed anything.
type textStopMock struct {
	srv         *httptest.Server
	streamCalls atomic.Int32
	answerCalls atomic.Int32
	reached     chan struct{}
	release     chan struct{}
}

func newTextStopMock(park bool) *textStopMock {
	m := &textStopMock{}
	if park {
		m.reached = make(chan struct{})
		m.release = make(chan struct{})
	}
	m.srv = httptest.NewServer(http.HandlerFunc(m.handle))
	return m
}

const textStopAnswer = "Yes — the docs are in scope, finish them and call request_review."

func (m *textStopMock) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/models" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
		return
	}
	var body struct {
		Stream   bool `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	if !body.Stream {
		// One endpoint serves every non-streaming call the turn makes. Only the answering call
		// is counted: answer_question.tmpl opens with a sentence no utility prompt uses, and the
		// rest (the commit message, say) get their own kind of answer.
		system := ""
		for _, msg := range body.Messages {
			if msg.Role == "system" {
				system += msg.Content
			}
		}
		content := "chore: accept both parser shapes"
		if strings.Contains(system, "coordinating task completion") {
			m.answerCalls.Add(1)
			content = textStopAnswer
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 8, "total_tokens": 18},
		})
		return
	}

	switch m.streamCalls.Add(1) {
	case 1:
		writeSSE(w, `{"choices":[{"delta":{"content":"The parser handles both shapes. Should I also update the docs?"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":9,"total_tokens":19}}`)
	default:
		if m.reached != nil {
			close(m.reached)
			<-m.release
		}
		writeSSE(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_review","type":"function","function":{"name":"request_review","arguments":"{\"summary\":\"Both shapes handled.\"}"}}]},"finish_reason":"tool_calls"}]}`)
	}
	writeSSE(w, `[DONE]`)
}

func writeSSE(w http.ResponseWriter, payload string) {
	_, _ = w.Write([]byte("data: " + payload + "\n\n"))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// textStopRun is one executor run over a subtask with a parent, driven entirely by the mock:
// nothing is ever sent on the user-message channel.
type textStopRun struct {
	te           *TaskExecutor
	setup        *taskExecSetup
	worktreePath string
	parentBranch string
	baseSHA      string
	cancel       context.CancelFunc
	done         chan struct{}
}

// startTextStopRun builds the same hand-built setup as the turn-outcome tests (a real store and
// worktree, no prepareTaskExecution) and starts the run. mutate is the test's only say in the
// subtask's row, so provenance and merge flags stay explicit at each call site.
func startTextStopRun(t *testing.T, m *textStopMock, mutate func(*types.Task)) *textStopRun {
	t.Helper()

	// automatedAnswers renders prompts/answer_question.tmpl from the CWD.
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("chdir to repo root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	te, repoDir, projectID, initialSHA := setupExecutorWithGit(t)
	srvURL := m.srv.URL
	const parentBranch = "refs/heads/task/parent-review-target"
	runGit(t, repoDir, "branch", strings.TrimPrefix(parentBranch, "refs/heads/"), initialSHA)

	parent := &types.Task{
		ID:              "parent-text-stop",
		ProjectID:       projectID,
		Title:           "Ship the parser",
		Prompt:          "The parser must accept both shapes, docs included.",
		Status:          types.StatusInProgress,
		ExecutionState:  types.ExecStateIdle,
		LlamaServerURL:  srvURL,
		ModelName:       "parent-model",
		Branch:          strings.TrimPrefix(parentBranch, "refs/heads/"),
		CurrentThreadID: "thread-parent",
	}
	if err := te.store.CreateTask(parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	const taskID = "subtask-text-stop"
	const branch = "task/" + taskID
	subtask := &types.Task{
		ID:              taskID,
		ProjectID:       projectID,
		Title:           "Accept both shapes",
		Prompt:          "Extend the parser.",
		Status:          types.StatusInProgress,
		ExecutionState:  types.ExecStateRunning,
		ParentTaskID:    parent.ID,
		Autostarted:     true,
		LlamaServerURL:  srvURL,
		ModelName:       "test-model",
		OperatingMode:   types.ModeAct,
		Branch:          branch,
		BaseCommit:      initialSHA,
		CurrentThreadID: "thread-1",
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}
	if mutate != nil {
		mutate(subtask)
	}
	if err := te.store.CreateTask(subtask); err != nil {
		t.Fatalf("create subtask: %v", err)
	}
	if err := te.store.CreateTaskMessage(&types.TaskMessage{
		ID: uuid.New().String(), TaskID: taskID, ThreadID: "thread-1",
		Role: "user", Content: "Extend the parser.", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create message: %v", err)
	}

	worktreePath, _, err := te.worktreeMgr.CreateWorktree(projectID, taskID, branch, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
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
	te.tasks[taskID] = &executionState{ctx: ctx, cancel: cancel, userMsgChan: setup.userMsgChan, summarizeCh: setup.summarizeCh, executorType: "llm", done: done}
	te.taskMu.Unlock()

	go te.executeTask(setup)
	t.Cleanup(func() {
		if m.release != nil {
			select {
			case <-m.release:
			default:
				close(m.release)
			}
		}
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("executor did not exit after cancel")
		}
	})

	return &textStopRun{te: te, setup: setup, worktreePath: worktreePath, parentBranch: parent.Branch, baseSHA: initialSHA, cancel: cancel, done: done}
}

func (r *textStopRun) task(t *testing.T) *types.Task {
	t.Helper()
	row, err := r.te.store.GetTaskByID(r.setup.task.ID)
	if err != nil {
		t.Fatalf("read task row: %v", err)
	}
	return row
}

// autoAnswerRows returns the content of every advisory row the run recorded with the
// auto-answer kind.
func (r *textStopRun) autoAnswerRows(t *testing.T) []string {
	t.Helper()
	msgs, err := r.te.store.GetTaskMessages(r.setup.task.ID, "thread-1")
	if err != nil {
		t.Fatalf("read messages: %v", err)
	}
	var out []string
	for _, m := range msgs {
		if m.Role == "advisory" && m.Kind == advisoryAutoAnswer {
			out = append(out, m.Content)
		}
	}
	return out
}

func gitRevParse(t *testing.T, dir, ref string) string {
	t.Helper()
	return gitOut(t, dir, "rev-parse", ref)
}

// TestAutoAnswer_TextStopContinuesFromParentContext is the feature itself: a subtask run that no
// one started answers its own text-only stop and goes straight back to the LLM. Nothing is ever
// sent on the user-message channel, so the second streaming call can only come from the answer.
func TestAutoAnswer_TextStopContinuesFromParentContext(t *testing.T) {
	m := newTextStopMock(true)
	defer m.srv.Close()
	run := startTextStopRun(t, m, nil)

	select {
	case <-m.reached:
	case <-time.After(30 * time.Second):
		t.Fatalf("streaming calls = %d, answering calls = %d: the text-only stop must be answered from "+
			"the parent context and the run must return to the LLM without a user message", m.streamCalls.Load(), m.answerCalls.Load())
	}

	if got := m.answerCalls.Load(); got != 1 {
		t.Errorf("answering calls = %d, want 1 before the run returns to the LLM", got)
	}
	rows := run.autoAnswerRows(t)
	if len(rows) != 1 {
		t.Fatalf("advisory rows with kind %q = %d, want 1 (the answer must be its own row, not a user one)", advisoryAutoAnswer, len(rows))
	}
	if rows[0] != textStopAnswer {
		t.Errorf("advisory content = %q, want the answering model's text", rows[0])
	}
	// Past the answer and back to running: the run did not stay in review.
	if row := run.task(t); row.Status != types.StatusInProgress || row.ExecutionState != types.ExecStateRunning {
		t.Errorf("task after the answer = %s/%s, want in_progress/running", row.Status, row.ExecutionState)
	}

	close(m.release)
	select {
	case <-run.done:
	case <-time.After(30 * time.Second):
		t.Fatal("executor did not finish after request_review")
	}

	if got := m.streamCalls.Load(); got != 2 {
		t.Errorf("streaming calls = %d, want 2", got)
	}
	select {
	case um := <-run.setup.userMsgChan:
		t.Fatalf("a user message reached the executor even though the test sent none: %+v", um)
	default:
	}
}

// TestAutoAnswer_UserStartedSubtaskStillWaitsForTheUser pins the gate: a run begun by a click on
// this very task is a run somebody is watching, so a text-only stop settles in review unanswered.
func TestAutoAnswer_UserStartedSubtaskStillWaitsForTheUser(t *testing.T) {
	m := newTextStopMock(false)
	defer m.srv.Close()
	run := startTextStopRun(t, m, func(task *types.Task) { task.Autostarted = false })

	if row := waitForStatus(t, run, types.StatusReview); row == nil {
		t.Fatalf("task never reached review (streaming calls = %d)", m.streamCalls.Load())
	}
	time.Sleep(500 * time.Millisecond)
	if got := m.streamCalls.Load(); got != 1 {
		t.Errorf("streaming calls = %d, want 1: a user-started run must wait for the user after a text answer", got)
	}
	if got := m.answerCalls.Load(); got != 0 {
		t.Errorf("answering calls = %d, want 0", got)
	}
	if rows := run.autoAnswerRows(t); len(rows) != 0 {
		t.Errorf("advisory rows with kind %q = %d, want 0", advisoryAutoAnswer, len(rows))
	}
}

// TestAutoAnswer_PlanModeStopIsTheDeliverable pins the other half of the gate: in plan mode a text
// answer is what the task was asked for, so answering it from the parent would talk over it.
func TestAutoAnswer_PlanModeStopIsTheDeliverable(t *testing.T) {
	m := newTextStopMock(false)
	defer m.srv.Close()
	run := startTextStopRun(t, m, func(task *types.Task) { task.OperatingMode = types.ModePlan })

	if row := waitForStatus(t, run, types.StatusReview); row == nil {
		t.Fatalf("task never reached review (streaming calls = %d)", m.streamCalls.Load())
	}
	time.Sleep(500 * time.Millisecond)
	if got := m.streamCalls.Load(); got != 1 {
		t.Errorf("streaming calls = %d, want 1", got)
	}
	if got := m.answerCalls.Load(); got != 0 {
		t.Errorf("answering calls = %d, want 0", got)
	}
}

// TestAutoAnswer_AnsweredStopDoesNotMerge pins that the automated answer does not reach the merge
// path: with auto_commit on and auto_merge off, the run goes on to request_review, commits its own
// branch, and waits in review with the parent's branch untouched.
func TestAutoAnswer_AnsweredStopDoesNotMerge(t *testing.T) {
	m := newTextStopMock(false)
	defer m.srv.Close()
	run := startTextStopRun(t, m, func(task *types.Task) {
		task.AutoCommit = true
		task.AutoMerge = false
	})

	select {
	case <-run.done:
	case <-time.After(30 * time.Second):
		t.Fatal("executor did not finish after request_review")
	}

	if got := m.answerCalls.Load(); got != 1 {
		t.Fatalf("answering calls = %d, want 1: the run must have been answered to reach request_review", got)
	}
	row := run.task(t)
	if row.Status != types.StatusReview {
		t.Errorf("task status = %s, want review: nothing merged means nothing is done", row.Status)
	}
	if len(run.autoAnswerRows(t)) != 1 {
		t.Errorf("advisory rows with kind %q != 1", advisoryAutoAnswer)
	}

	tip := gitRevParse(t, run.worktreePath, "HEAD")
	if tip == run.baseSHA {
		t.Error("the task branch has no commit: auto_commit is honored, so its own branch should carry the work")
	}
	parentTip := gitRevParse(t, run.setup.project.Path, run.parentBranch)
	if parentTip != run.baseSHA {
		t.Errorf("parent branch tip = %s, want %s: an automated answer authorizes no merge", parentTip, run.baseSHA)
	}
}

func waitForStatus(t *testing.T, run *textStopRun, want types.Status) *types.Task {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if row := run.task(t); row.Status == want {
			return row
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}
