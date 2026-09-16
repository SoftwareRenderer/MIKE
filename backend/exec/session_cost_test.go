package exec

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mike/backend/stream"
	"mike/backend/types"

	"github.com/google/uuid"
)

// newSessionCostTestExecutor builds a TaskExecutor ready for executeTask (LLM path)
// against a mock LLM server. It extends newTestExecutor with the concurrency gate
// that the executor loop requires.
func newSessionCostTestExecutor(t *testing.T) *TaskExecutor {
	t.Helper()
	te := newTestExecutor(t)
	te.gate = NewConcurrencyGate()
	return te
}

// mockSessionCostLLM returns an httptest server that answers /models discovery
// and streams a zero-usage chat completion. firstChat is signaled once when a
// chat request arrives (guaranteeing the executor has initialized its loop).
func mockSessionCostLLM(firstChat chan<- struct{}) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		// Signal once on the first chat request (channel is buffered).
		select {
		case firstChat <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		// Zero usage so the run itself adds no cost — the test isolates carry-over.
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":0,\"total_tokens\":0}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
}

// A restarted run accumulates on top of the stored SessionCost; it never resets it.
func TestExecuteTask_PreservesSessionCostAcrossRun(t *testing.T) {
	te := newSessionCostTestExecutor(t)
	s := te.store

	proj := createProject(t, s, "proj-session-cost", "Session Cost Proj")
	taskID := "session-cost-task-1"

	firstChat := make(chan struct{}, 1)
	server := mockSessionCostLLM(firstChat)
	defer server.Close()

	task := &types.Task{
		ID:             taskID,
		ProjectID:      proj.ID,
		Title:          "Session cost task",
		Prompt:         "Do something",
		Status:         types.StatusBacklog,
		ExecutionState: types.ExecStateIdle,
		LlamaServerURL: server.URL,
		ModelName:      "test-model",
		OperatingMode:  "act",
		SessionCost:    0.5, // pre-existing accumulated cost from a prior run/provider
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// A user message so the executor issues an LLM call.
	if err := s.CreateTaskMessage(&types.TaskMessage{
		ID:        uuid.New().String(),
		TaskID:    taskID,
		ThreadID:  "",
		Role:      "user",
		Content:   "Please do the thing",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create message: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	setup := &taskExecSetup{
		task:           task,
		project:        proj,
		projectID:      proj.ID,
		worktreePath:   t.TempDir(),
		serverURL:      server.URL,
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

	// Wait until the executor issues a chat request (guarantees the loop/lc is
	// initialized), then cancel so the run ends and the defer persists the cost.
	select {
	case <-firstChat:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for LLM request")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for executor to finish")
	}

	got, err := s.GetTaskByID(taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.SessionCost != 0.5 {
		t.Fatalf("expected session cost 0.5 to be carried over, got %f", got.SessionCost)
	}

	// The same task, started again (e.g. after a provider/model change restart),
	// must still accumulate on top of the preserved cost.
	task2, _ := s.GetTaskByID(taskID)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	done2 := make(chan struct{})
	firstChat2 := make(chan struct{}, 1)
	server2 := mockSessionCostLLM(firstChat2)
	defer server2.Close()
	task2.LlamaServerURL = server2.URL
	if err := s.UpdateTask(task2); err != nil {
		t.Fatalf("update task2: %v", err)
	}
	setup2 := &taskExecSetup{
		task:           task2,
		project:        proj,
		projectID:      proj.ID,
		worktreePath:   t.TempDir(),
		serverURL:      server2.URL,
		modelName:      "test-model",
		ctx:            ctx2,
		cancel:         cancel2,
		userMsgChan:    make(chan stream.UserMessage, 16),
		summarizeCh:    make(chan *summarizeRequest, 16),
		sandboxEnabled: false,
		done:           done2,
	}
	te.taskMu.Lock()
	te.tasks[taskID] = &executionState{
		ctx:          ctx2,
		cancel:       cancel2,
		userMsgChan:  setup2.userMsgChan,
		summarizeCh:  setup2.summarizeCh,
		executorType: "llm",
		done:         done2,
	}
	te.taskMu.Unlock()

	go te.executeTask(setup2)

	select {
	case <-firstChat2:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for LLM request (second run)")
	}
	cancel2()

	select {
	case <-done2:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for executor to finish (second run)")
	}

	got2, err := s.GetTaskByID(taskID)
	if err != nil {
		t.Fatalf("get task after second run: %v", err)
	}
	if got2.SessionCost != 0.5 {
		t.Fatalf("expected session cost to remain 0.5 after second run, got %f", got2.SessionCost)
	}
}

// TestAccumulateAgentSessionCost_InheritsCarriedOverCost is the agent path's half of the rule
// TestExecuteTask_PreservesSessionCostAcrossRun holds for the LLM path. The agent path reaches it
// through a different door: it re-reads the task from the store before adding, so the base it
// accumulates onto is the stored row, not the copy the executor started with.
func TestAccumulateAgentSessionCost_InheritsCarriedOverCost(t *testing.T) {
	te, s := newTestExecutorWithStore(t)
	proj := createProject(t, s, "proj-agent-cost", "Agent Cost Proj")

	task := &types.Task{
		ID:          "agent-cost-task",
		ProjectID:   proj.ID,
		Title:       "Agent cost task",
		Prompt:      "Do something",
		SessionCost: 1.25, // spent by an earlier run, on another provider
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	te.accumulateAgentSessionCost(proj.ID, task, 0.5, "model-x", "")

	if task.SessionCost != 1.75 {
		t.Fatalf("task session cost = %f, want 1.75 (carried 1.25 + run 0.5)", task.SessionCost)
	}
	stored, err := s.GetTaskByID(task.ID)
	if err != nil {
		t.Fatalf("read task back: %v", err)
	}
	if stored.SessionCost != 1.75 {
		t.Fatalf("stored session cost = %f, want 1.75 — the badge reads the row, not the caller's copy", stored.SessionCost)
	}

	stats, err := s.GetProjectStats(proj.ID)
	if err != nil {
		t.Fatalf("project stats: %v", err)
	}
	// The project sees this run's increment only; the carried cost was already counted there.
	if stats.TotalCost != 0.5 {
		t.Fatalf("project total cost = %f, want 0.5 — the increment, not the task total", stats.TotalCost)
	}
	// An empty provider name is answered by the default, not stored blank.
	if len(stats.ByModel) != 1 || stats.ByModel[0].ProviderName == "" {
		t.Fatalf("by-model stats = %+v, want one row with a provider name", stats.ByModel)
	}
}

// TestAccumulateAgentSessionCost_ZeroCostRunKeepsTheCarriedValue pins the other half: a run that
// spent nothing must neither touch the project stats nor drop the cost the task already carried.
func TestAccumulateAgentSessionCost_ZeroCostRunKeepsTheCarriedValue(t *testing.T) {
	te, s := newTestExecutorWithStore(t)
	proj := createProject(t, s, "proj-agent-cost-zero", "Agent Cost Zero Proj")

	task := &types.Task{
		ID:          "agent-cost-zero",
		ProjectID:   proj.ID,
		Title:       "Agent cost zero",
		Prompt:      "Do something",
		SessionCost: 0.75,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	te.accumulateAgentSessionCost(proj.ID, task, 0, "model-x", "provider-x")

	if task.SessionCost != 0.75 {
		t.Fatalf("task session cost = %f, want the carried 0.75 untouched", task.SessionCost)
	}
	stats, err := s.GetProjectStats(proj.ID)
	if err != nil {
		t.Fatalf("project stats: %v", err)
	}
	if stats.TotalCost != 0 {
		t.Fatalf("project total cost = %f, want 0 — a run that spent nothing reports nothing", stats.TotalCost)
	}
	// The guard is not about the number: a zero-cost call would still open a stats row for the
	// model, so a project would list a model it never spent anything on.
	if len(stats.ByModel) != 0 {
		t.Fatalf("by-model stats = %+v, want none — a run that spent nothing adds no model row", stats.ByModel)
	}
}
