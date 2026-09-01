package exec

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"kanban/backend/agent"
	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/types"

	"github.com/google/uuid"
	ext "kanban/backend/extensions"
)

// heldLLMMock is an httptest LLM server that HOLDS every chat request open
// until Release() has been called at least n times (n = request index),
// simulating an in-flight LLM stream. After its release, a request streams a
// short text-only response (turn-complete). This lets tests observe tasks
// stuck "running" (stream held) and provider slot occupancy.
type heldLLMMock struct {
	reqCount int32
	releases int32
	// arrivals receives the 1-based index of each chat request as it arrives.
	arrivals chan int32
}

func newHeldLLMMock() *heldLLMMock {
	return &heldLLMMock{arrivals: make(chan int32, 16)}
}

func (m *heldLLMMock) Release() { atomic.AddInt32(&m.releases, 1) }

// waitForArrival blocks until request index n arrives (or timeout).
func (m *heldLLMMock) waitForArrival(t *testing.T, n int32) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case got := <-m.arrivals:
			if got >= n {
				return
			}
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("heldLLMMock: request %d never arrived", n)
}

func (m *heldLLMMock) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		// Answer in the requested format: SSE for streaming calls, plain JSON
		// for one-shot ChatCompletion (utility) calls.
		var reqBody struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		n := atomic.AddInt32(&m.reqCount, 1)
		m.arrivals <- n
		// Hold the stream open until this request's index has been released
		// (or the client went away - cancellation on stop/switch).
		for atomic.LoadInt32(&m.releases) < n {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
		if reqBody.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n"))
			_, _ = w.Write([]byte("data: {\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":0,\"total_tokens\":0}}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`))
	}
}

// runExecTask creates a task + initial user message + execution state and
// starts executeTask in a goroutine (mirrors the StartTask/handleLazyStart
// wiring without going through the HTTP layer).
func runExecTask(t *testing.T, te *TaskExecutor, proj *types.Project, taskID, serverURL, prompt string) *taskExecSetup {
	t.Helper()
	s := te.rawStore()
	task := &types.Task{
		ID:             taskID,
		ProjectID:      proj.ID,
		Title:          taskID,
		Prompt:         prompt,
		Status:         types.StatusBacklog,
		ExecutionState: types.ExecStateIdle,
		LlamaServerURL: serverURL,
		ModelName:      "test-model",
		OperatingMode:  "act",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.CreateTaskMessage(&types.TaskMessage{
		ID:        uuid.New().String(),
		TaskID:    taskID,
		Role:      "user",
		Content:   prompt,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create message: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	finished := make(chan struct{})
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
		finished:       finished,
	}
	te.taskMu.Lock()
	te.tasks[taskID] = &executionState{
		ctx:          ctx,
		cancel:       cancel,
		userMsgChan:  setup.userMsgChan,
		summarizeCh:  setup.summarizeCh,
		executorType: "llm",
		done:         done,
		finished:     finished,
	}
	te.taskMu.Unlock()

	go te.executeTask(setup)
	return setup
}

// waitTaskState polls the store until the task's (status, execution_state)
// matches, or fails the test after 15s.
func waitTaskState(t *testing.T, s *store.Store, taskID string, match func(status types.Status, execState types.ExecutionState) bool) *types.Task {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var task *types.Task
	for time.Now().Before(deadline) {
		var err error
		task, err = s.GetTaskByID(taskID)
		if err == nil && task != nil && match(task.Status, task.ExecutionState) {
			return task
		}
		time.Sleep(50 * time.Millisecond)
	}
	if task != nil {
		t.Fatalf("task %s did not reach expected state (last: status=%s exec=%s)", taskID, task.Status, task.ExecutionState)
	}
	t.Fatalf("task %s not found while waiting for state", taskID)
	return nil
}

var (
	isRunning = func(st types.Status, es types.ExecutionState) bool { return st == "in_progress" && es == "running" }
	isPending = func(st types.Status, es types.ExecutionState) bool { return st == "in_progress" && es == "idle" }
	isIdle    = func(st types.Status, es types.ExecutionState) bool { return st == "review" && es == "idle" }
)

// TestTaskGate_SecondTaskQueues verifies that with concurrency 1 (the default
// for a provider URL not in config), a second task on the same provider is
// queued (in_progress + execution_state idle = "Pending") while the first
// task runs, and picks up the slot as soon as the first task goes idle.
func TestTaskGate_SecondTaskQueues(t *testing.T) {
	te := newSessionCostTestExecutor(t)
	s := te.rawStore()
	proj := createProject(t, s, "proj-tg-queue", "TG Queue")

	mock := newHeldLLMMock()
	server := httptest.NewServer(mock.Handler())
	defer server.Close()

	// Task 1 takes the only slot and holds its LLM stream open (running).
	runExecTask(t, te, proj, "tg-queue-1", server.URL, "task one")
	mock.waitForArrival(t, 1)
	waitTaskState(t, s, "tg-queue-1", isRunning)

	// Task 2 on the same provider must queue as pending.
	runExecTask(t, te, proj, "tg-queue-2", server.URL, "task two")
	waitTaskState(t, s, "tg-queue-2", isPending)

	// Task 2 must stay pending while task 1's stream is held (no LLM call yet).
	if n := atomic.LoadInt32(&mock.reqCount); n != 1 {
		t.Fatalf("queued task 2 must not issue an LLM call; provider saw %d requests", n)
	}

	// Complete task 1's turn: it goes idle (review) and frees the slot.
	mock.Release()
	waitTaskState(t, s, "tg-queue-1", isIdle)

	// Task 2 now acquires the slot and starts running (its stream is held).
	mock.waitForArrival(t, 2)
	waitTaskState(t, s, "tg-queue-2", isRunning)

	// Cleanup: release task 2's stream and stop both executors.
	mock.Release()
	te.StopTask("tg-queue-1")
	te.StopTask("tg-queue-2")
}

// TestTaskGate_PerProviderIsolation verifies that the running-task limit is
// per provider URL: a task on provider B starts immediately even while
// provider A's single slot is occupied.
func TestTaskGate_PerProviderIsolation(t *testing.T) {
	te := newSessionCostTestExecutor(t)
	s := te.rawStore()
	proj := createProject(t, s, "proj-tg-iso", "TG Isolation")

	mockA := newHeldLLMMock()
	serverA := httptest.NewServer(mockA.Handler())
	defer serverA.Close()
	mockB := newHeldLLMMock()
	serverB := httptest.NewServer(mockB.Handler())
	defer serverB.Close()

	runExecTask(t, te, proj, "tg-iso-1", serverA.URL, "task A")
	mockA.waitForArrival(t, 1)
	waitTaskState(t, s, "tg-iso-1", isRunning)

	// Task on a DIFFERENT provider starts immediately (no pending phase).
	runExecTask(t, te, proj, "tg-iso-2", serverB.URL, "task B")
	mockB.waitForArrival(t, 1)
	waitTaskState(t, s, "tg-iso-2", isRunning)

	// Both running at once, each holding its own provider's slot.
	waitTaskState(t, s, "tg-iso-1", isRunning)

	mockA.Release()
	mockB.Release()
	te.StopTask("tg-iso-1")
	te.StopTask("tg-iso-2")
}

// emptyExtStore is an agent.ExtensionStore with no extensions installed.
type emptyExtStore struct{}

func (emptyExtStore) GetByType(_ string) ([]ext.Extension, error) { return nil, nil }

// TestTaskGate_AgentProviderBypasses verifies that agent providers (agent://)
// do not go through the LLM running-task gate — the task is routed to the
// agent executor (which fails here: no agent extension is installed).
func TestTaskGate_AgentProviderBypasses(t *testing.T) {
	te := newSessionCostTestExecutor(t)
	te.agentRegistry = agent.NewAgentRegistry(emptyExtStore{}, nil)
	s := te.rawStore()
	proj := createProject(t, s, "proj-tg-agent", "TG Agent")

	runExecTask(t, te, proj, "tg-agent-1", "agent://pi", "task agent")

	// The agent path fails fast (no extension registered) instead of queuing
	// behind the LLM task gate.
	waitTaskState(t, s, "tg-agent-1", func(st types.Status, es types.ExecutionState) bool { return es == "failed" })
	te.StopTask("tg-agent-1")
}

// TestTaskGate_ProviderSwapFreesOldProvider verifies that switching a running
// task to another provider releases the old provider's task slot (freeing a
// queued task there) and moves the task's slot to the new provider.
func TestTaskGate_ProviderSwapFreesOldProvider(t *testing.T) {
	te := newSessionCostTestExecutor(t)
	s := te.rawStore()
	proj := createProject(t, s, "proj-tg-swap", "TG Swap")

	mockA := newHeldLLMMock()
	serverA := httptest.NewServer(mockA.Handler())
	defer serverA.Close()
	mockB := newHeldLLMMock()
	serverB := httptest.NewServer(mockB.Handler())
	defer serverB.Close()

	// Task 1 runs on A (stream held); task 2 queues on A.
	setup1 := runExecTask(t, te, proj, "tg-swap-1", serverA.URL, "task one")
	mockA.waitForArrival(t, 1)
	waitTaskState(t, s, "tg-swap-1", isRunning)
	runExecTask(t, te, proj, "tg-swap-2", serverA.URL, "task two")
	waitTaskState(t, s, "tg-swap-2", isPending)

	// Switch task 1 to provider B via a user message carrying the new URL.
	setup1.userMsgChan <- stream.UserMessage{
		Content:   "switch provider",
		ServerURL: serverB.URL,
		ModelName: "test-model",
	}

	// Task 1 calls B (held), finishes its turn and goes idle.
	mockB.waitForArrival(t, 1)
	mockB.Release()
	waitTaskState(t, s, "tg-swap-1", isIdle)

	// The old provider's slot was freed by the swap: task 2 is now running on A.
	mockA.waitForArrival(t, 2)
	waitTaskState(t, s, "tg-swap-2", isRunning)

	// Cleanup: release every held stream (the mock's held handlers only exit
	// on release or client disconnect, and keep-alive pooling means a
	// cancelled client connection can outlive the executor) so the httptest
	// servers close cleanly.
	mockA.Release()
	mockA.Release()
	mockB.Release()
	te.StopTask("tg-swap-1")
	te.StopTask("tg-swap-2")
}

// TestTaskSlotReleasedWhenIdle verifies that a task which finishes its turn
// (goes review/idle) releases its running-task slot, so a new task on the
// same provider can start without queueing.
func TestTaskSlotReleasedWhenIdle(t *testing.T) {
	te := newSessionCostTestExecutor(t)
	s := te.rawStore()
	proj := createProject(t, s, "proj-tg-release", "TG Release")

	mock := newHeldLLMMock()
	server := httptest.NewServer(mock.Handler())
	defer server.Close()

	setup1 := runExecTask(t, te, proj, "tg-rel-1", server.URL, "task one")
	mock.waitForArrival(t, 1)
	waitTaskState(t, s, "tg-rel-1", isRunning)

	// Complete the turn: the task goes idle and must release the slot.
	mock.Release()
	waitTaskState(t, s, "tg-rel-1", isIdle)
	// The release happens before the review/idle persist, so by now the slot
	// must be free.
	if setup1.taskSlotURL != "" {
		t.Fatalf("task slot must be released when the task goes idle, still holding %q", setup1.taskSlotURL)
	}

	// A new task on the same provider starts immediately (slot was freed).
	runExecTask(t, te, proj, "tg-rel-2", server.URL, "task two")
	mock.waitForArrival(t, 2)
	waitTaskState(t, s, "tg-rel-2", isRunning)

	mock.Release()
	te.StopTask("tg-rel-1")
	te.StopTask("tg-rel-2")
}

// TestUtilityCalls_RespectInFlightLimit pins the per-request gate: while a
// task holds the provider's single in-flight LLM slot, a utility call
// (ChatCompletion via RunWithGate) must queue until the slot is free.
func TestUtilityCalls_RespectInFlightLimit(t *testing.T) {
	te := newSessionCostTestExecutor(t)
	s := te.rawStore()
	proj := createProject(t, s, "proj-tg-util", "TG Utility")

	mock := newHeldLLMMock()
	server := httptest.NewServer(mock.Handler())
	defer server.Close()

	// Task 1 holds the only in-flight request slot (stream held open).
	runExecTask(t, te, proj, "tg-util-1", server.URL, "task one")
	mock.waitForArrival(t, 1)
	waitTaskState(t, s, "tg-util-1", isRunning)

	// Utility call must block while the task's stream is in flight.
	utilDone := make(chan error, 1)
	go func() {
		_, _, err := te.ChatCompletion(context.Background(), server.URL, "test-model",
			[]types.ChatMessage{{Role: "user", Content: "utility"}}, nil)
		utilDone <- err
	}()
	select {
	case err := <-utilDone:
		t.Fatalf("utility call must queue while the provider's in-flight slot is held, returned %v", err)
	case <-time.After(750 * time.Millisecond):
		// Still queued — expected.
	}

	// Free the slot (task stream completes) and the utility call proceeds.
	// Two releases: one for the task's held stream, one for the utility
	// request itself (kept held by the mock until released).
	mock.Release()
	mock.Release()
	select {
	case err := <-utilDone:
		if err != nil {
			t.Fatalf("utility call after slot freed: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("utility call did not proceed after the in-flight slot was freed")
	}

	te.StopTask("tg-util-1")
}

// TestPersistTaskPreservesTitle verifies that a full-row write from the
// executor's stale task copy does not clobber a title that changed
// concurrently (async title generation or a user edit). The provider
// task-concurrency queue makes this race likely: title calls queue behind
// in-flight streams, so they can complete while the executor already holds
// its start-time row copy.
func TestPersistTaskPreservesTitle(t *testing.T) {
	te := newSessionCostTestExecutor(t)
	s := te.rawStore()
	proj := createProject(t, s, "proj-tg-title", "TG Title")

	task := &types.Task{
		ID: "tg-title-1", ProjectID: proj.ID, Title: "first-line title",
		Prompt: "first-line title", Status: types.StatusInProgress, ExecutionState: types.ExecStateRunning,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// The executor's copy, loaded BEFORE the title changes.
	stale, err := s.GetTask(proj.ID, "tg-title-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	// Concurrent title update (title generation / user edit).
	if err := s.SetTaskTitle(proj.ID, "tg-title-1", "generated title"); err != nil {
		t.Fatalf("set title: %v", err)
	}

	// The executor's full-row write must preserve the new title...
	stale.Output = "some output"
	te.persistTask(&taskExecSetup{task: stale, projectID: proj.ID})
	got, err := s.GetTask(proj.ID, "tg-title-1")
	if err != nil {
		t.Fatalf("get task after persist: %v", err)
	}
	if got.Title != "generated title" {
		t.Fatalf("persistTask clobbered title: got %q, want %q", got.Title, "generated title")
	}
	// ...while still applying the executor's own field writes.
	if got.Output != "some output" {
		t.Fatalf("persistTask did not apply executor fields: output = %q", got.Output)
	}
}

// TestTaskGate_TitleSurvivesExecutorWrites runs the same race through the
// real executor loop: a title is generated while a task is running, and the
// executor's turn-complete writes must not revert it.
func TestTaskGate_TitleSurvivesExecutorWrites(t *testing.T) {
	te := newSessionCostTestExecutor(t)
	s := te.rawStore()
	proj := createProject(t, s, "proj-tg-title2", "TG Title Run")

	mock := newHeldLLMMock()
	server := httptest.NewServer(mock.Handler())
	defer server.Close()

	runExecTask(t, te, proj, "tg-title-run", server.URL, "task title")
	mock.waitForArrival(t, 1)
	waitTaskState(t, s, "tg-title-run", isRunning)

	// Title generation completes while the executor holds the in-flight copy.
	if err := s.SetTaskTitle(proj.ID, "tg-title-run", "generated title"); err != nil {
		t.Fatalf("set title: %v", err)
	}

	// Turn completes → the executor's full-row writes (status/idle) go out.
	mock.Release()
	got := waitTaskState(t, s, "tg-title-run", isIdle)
	if got.Title != "generated title" {
		t.Fatalf("executor writes clobbered title: got %q, want %q", got.Title, "generated title")
	}
	te.StopTask("tg-title-run")
}
