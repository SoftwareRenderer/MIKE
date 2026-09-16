package exec

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mike/backend/store"
	"mike/backend/stream"
	"mike/backend/types"

	"github.com/google/uuid"
)

// isPaused matches the new paused state: in the In Progress column, not running.
var isPaused = func(st types.Status, es types.ExecutionState) bool { return st == "in_progress" && es == "paused" }

// --- applyStopTransition (pure helper) ---

func TestApplyStopTransition(t *testing.T) {
	cases := []struct {
		name        string
		status      types.Status
		execState   types.ExecutionState
		wantChanged bool
		wantExec    types.ExecutionState
		wantStatus  types.Status
	}{
		{"in_progress running -> paused", "in_progress", "running", true, "paused", "in_progress"},
		{"in_progress pulling -> paused", "in_progress", "pulling_image", true, "paused", "in_progress"},
		{"done running -> completed", "done", "running", true, "completed", "done"},
		{"review running -> idle", "review", "running", true, "idle", "review"},
		{"backlog pulling -> idle", "backlog", "pulling_image", true, "idle", "backlog"},
		{"in_progress idle untouched", "in_progress", "idle", false, "idle", "in_progress"},
		{"done completed untouched", "done", "completed", false, "completed", "done"},
		{"in_progress paused untouched (idempotent)", "in_progress", "paused", false, "paused", "in_progress"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := &types.Task{Status: tc.status, ExecutionState: tc.execState}
			changed := applyStopTransition(task)
			if changed != tc.wantChanged {
				t.Fatalf("changed = %v, want %v", changed, tc.wantChanged)
			}
			if tc.wantChanged {
				if task.ExecutionState != tc.wantExec || task.Status != tc.wantStatus {
					t.Fatalf("got %s/%s, want %s/%s", task.Status, task.ExecutionState, tc.wantStatus, tc.wantExec)
				}
			}
		})
	}
}

// --- StopTask with no live executor (stale state after restart/crash) ---

func createTaskRow(t *testing.T, s *store.Store, id, projectID string, status types.Status, execState types.ExecutionState) *types.Task {
	t.Helper()
	now := time.Now().UTC()
	task := &types.Task{
		ID:             id,
		ProjectID:      projectID,
		Title:          id,
		Prompt:         "prompt",
		Status:         status,
		ExecutionState: execState,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return task
}

// TestStopStaleRunningTask verifies the core reliability fix: stopping a task
// whose executor no longer exists (server restarted / crashed while the task
// was running) must succeed and clear the stale running state — before this
// fix StopTask returned "task not running" and the task was stuck "Running".
func TestStopStaleRunningTask(t *testing.T) {
	te := newSessionCostTestExecutor(t)
	s := te.store
	proj := createProject(t, s, "proj-stop-stale", "Stop Stale")

	createTaskRow(t, s, "stale-running", proj.ID, "in_progress", "running")

	// No executor in te.tasks for this task.
	if err := te.StopTask("stale-running"); err != nil {
		t.Fatalf("StopTask on stale running task must not error (was 'task not running'): %v", err)
	}
	task := waitTaskState(t, s, "stale-running", isPaused)
	if task.Status != types.StatusInProgress {
		t.Fatalf("status = %s, want in_progress", task.Status)
	}
}

// TestStopStaleNonInProgressTask verifies a stale in-flight row outside the
// In Progress column settles to idle (not paused) when stopped.
func TestStopStaleNonInProgressTask(t *testing.T) {
	te := newSessionCostTestExecutor(t)
	s := te.store
	proj := createProject(t, s, "proj-stop-stale-2", "Stop Stale 2")

	createTaskRow(t, s, "stale-review", proj.ID, "review", "running")

	if err := te.StopTask("stale-review"); err != nil {
		t.Fatalf("StopTask: %v", err)
	}
	waitTaskState(t, s, "stale-review", isIdle)
}

// TestStopStaleMissingTask verifies StopTask still errors for a task that does
// not exist at all.
func TestStopStaleMissingTask(t *testing.T) {
	te := newSessionCostTestExecutor(t)
	if err := te.StopTask("no-such-task"); err == nil {
		t.Fatal("StopTask on missing task must error")
	}
}

// --- StopTask with a live executor ---

// TestStopRunningTaskMidStream stops a task while its LLM stream is held open
// (the common "click stop mid-stream" case) and verifies: paused state, no
// "stopped" error entry persisted, executor exited, and the provider slot
// freed for the next task.
func TestStopRunningTaskMidStream(t *testing.T) {
	te := newSessionCostTestExecutor(t)
	s := te.store
	proj := createProject(t, s, "proj-stop-live", "Stop Live")

	mock := newHeldLLMMock()
	server := httptest.NewServer(mock.Handler())
	defer server.Close()

	setup := runExecTask(t, te, proj, "stop-live-1", server.URL, "task one")
	mock.waitForArrival(t, 1)
	waitTaskState(t, s, "stop-live-1", isRunning)

	if err := te.StopTask("stop-live-1"); err != nil {
		t.Fatalf("StopTask: %v", err)
	}
	waitTaskState(t, s, "stop-live-1", isPaused)

	// No "stopped" error entry may be persisted (paused is a normal state).
	msgs, _ := s.GetTaskMessages("stop-live-1", "")
	for _, m := range msgs {
		if m.Role == "error" && strings.Contains(m.Content, "stopped") {
			t.Fatalf("unexpected stopped error entry persisted: %q", m.Content)
		}
	}

	// The executor must have exited (its done channel is closed by the defer).
	select {
	case <-setup.done:
	case <-time.After(10 * time.Second):
		t.Fatal("executor did not exit after StopTask")
	}

	// The provider slot (concurrency 1 for unknown URLs) is released: a new
	// task on the same provider starts running immediately instead of queuing.
	runExecTask(t, te, proj, "stop-live-2", server.URL, "task two")
	mock.waitForArrival(t, 2)
	waitTaskState(t, s, "stop-live-2", isRunning)

	mock.Release()
	mock.Release()
	te.StopTask("stop-live-2")
}

// startExecTaskExisting starts a fresh executor for an EXISTING task — the
// resume path (what /resume → RetryTask → StartTask does), without the
// worktree/HTTP plumbing that the lightweight test executor lacks.
func startExecTaskExisting(t *testing.T, te *TaskExecutor, proj *types.Project, taskID, serverURL string) *taskExecSetup {
	t.Helper()
	task, err := te.store.GetTaskByID(taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
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
		modelName:      task.ModelName,
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

// TestPauseResumeCycle verifies the full pause -> resume cycle through the
// real execution loop: stop a running task (paused), then start a fresh
// executor for it (what /resume does) and confirm it runs again.
func TestPauseResumeCycle(t *testing.T) {
	te := newSessionCostTestExecutor(t)
	s := te.store
	proj := createProject(t, s, "proj-pause-resume", "Pause Resume")

	mock := newHeldLLMMock()
	server := httptest.NewServer(mock.Handler())
	defer server.Close()

	setup := runExecTask(t, te, proj, "pause-resume-1", server.URL, "task one")
	mock.waitForArrival(t, 1)
	waitTaskState(t, s, "pause-resume-1", isRunning)

	if err := te.StopTask("pause-resume-1"); err != nil {
		t.Fatalf("StopTask: %v", err)
	}
	waitTaskState(t, s, "pause-resume-1", isPaused)
	select {
	case <-setup.done:
	case <-time.After(10 * time.Second):
		t.Fatal("executor did not exit after pause")
	}

	// Resume: a fresh executor for the same task must run again.
	startExecTaskExisting(t, te, proj, "pause-resume-1", server.URL)
	mock.waitForArrival(t, 2)
	waitTaskState(t, s, "pause-resume-1", isRunning)

	mock.Release()
	te.StopTask("pause-resume-1")
}

// TestStopDoesNotClobberMovedTask verifies a delayed executor exit can no
// longer clobber a concurrent column move: a running task moved to Done stays
// done+completed after its executor is cancelled (previously the loop's
// handleStopped reverted it to review+idle).
func TestStopDoesNotClobberMovedTask(t *testing.T) {
	te := newSessionCostTestExecutor(t)
	s := te.store
	proj := createProject(t, s, "proj-stop-clobber", "Stop Clobber")

	mock := newHeldLLMMock()
	server := httptest.NewServer(mock.Handler())
	defer server.Close()

	runExecTask(t, te, proj, "stop-clobber-1", server.URL, "task one")
	mock.waitForArrival(t, 1)
	waitTaskState(t, s, "stop-clobber-1", isRunning)

	// User moves the task to Done (HandleMove does this: status done +
	// execution_state completed, then stops the executor).
	fresh, _ := s.GetTaskByID("stop-clobber-1")
	fresh.Status = types.StatusDone
	fresh.ExecutionState = types.ExecStateCompleted
	if err := s.UpdateTask(fresh); err != nil {
		t.Fatalf("move to done: %v", err)
	}

	if err := te.StopTask("stop-clobber-1"); err != nil {
		t.Fatalf("StopTask: %v", err)
	}

	// Wait for the executor to exit (its deferred cancel handling runs
	// handleCancelledTask, which must NOT rewrite the row).
	waitTaskState(t, s, "stop-clobber-1", func(st types.Status, es types.ExecutionState) bool {
		return st == "done" && es == "completed"
	})
	time.Sleep(500 * time.Millisecond) // give a (buggy) late write a chance to clobber
	final, err := s.GetTaskByID("stop-clobber-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if final.Status != types.StatusDone || final.ExecutionState != types.ExecStateCompleted {
		t.Fatalf("task clobbered by executor exit: got %s/%s, want done/completed", final.Status, final.ExecutionState)
	}
}

// --- RecoverStaleTasks (startup recovery) ---

// TestRecoverStaleTasks verifies startup recovery transitions every in-flight
// row: in_progress -> paused (resumable), other columns -> idle; rows that
// are not in-flight are untouched.
func TestRecoverStaleTasks(t *testing.T) {
	te := newSessionCostTestExecutor(t)
	s := te.store
	proj := createProject(t, s, "proj-recover", "Recover")

	createTaskRow(t, s, "rec-running", proj.ID, "in_progress", "running")
	createTaskRow(t, s, "rec-pulling", proj.ID, "in_progress", "pulling_image")
	createTaskRow(t, s, "rec-review", proj.ID, "review", "running")
	createTaskRow(t, s, "rec-backlog", proj.ID, "backlog", "pulling_image")
	createTaskRow(t, s, "rec-idle", proj.ID, "in_progress", "idle")

	if n := te.RecoverStaleTasks(); n != 4 {
		t.Fatalf("RecoverStaleTasks recovered %d, want 4", n)
	}

	waitTaskState(t, s, "rec-running", isPaused)
	waitTaskState(t, s, "rec-pulling", isPaused)
	waitTaskState(t, s, "rec-review", isIdle)
	waitTaskState(t, s, "rec-backlog", func(st types.Status, es types.ExecutionState) bool { return st == "backlog" && es == "idle" })
	if idle := waitTaskState(t, s, "rec-idle", isPending); idle.ExecutionState != types.ExecStateIdle {
		t.Fatalf("rec-idle modified: %s", idle.ExecutionState)
	}
}

// --- StartTask assistant-last early return ---

// TestStartTaskAssistantLastSettleIdle verifies that starting a task whose last message is
// already from the assistant settles into review/idle — no executor, no stale "running"
// row, and no "Pending" card for a conversation that is waiting on the user.
func TestStartTaskAssistantLastSettleIdle(t *testing.T) {
	te, _, projectID, _ := setupExecutorWithGit(t)
	s := te.store

	task := &types.Task{
		ID:             "start-assistant-last",
		ProjectID:      projectID,
		Title:          "assistant last",
		Prompt:         "prompt",
		Status:         types.StatusReview,
		ExecutionState: types.ExecStateIdle,
		LlamaServerURL: "http://localhost:1",
		ModelName:      "test-model",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	now := time.Now().UTC()
	for _, m := range []types.TaskMessage{
		{ID: uuid.New().String(), TaskID: task.ID, Role: "user", Content: "hi", CreatedAt: now},
		{ID: uuid.New().String(), TaskID: task.ID, Role: "assistant", Content: "hello", CreatedAt: now.Add(time.Second)},
	} {
		if err := s.CreateTaskMessage(&m); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	if err := te.StartTask(projectID, task.ID, false); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	task = waitTaskState(t, s, task.ID, func(st types.Status, es types.ExecutionState) bool { return es == "idle" })
	if task.Status != types.StatusReview {
		t.Fatalf("status = %s, want review (nothing to run — the agent waits for the user)", task.Status)
	}
	// No executor may remain registered.
	te.taskMu.Lock()
	_, active := te.tasks[task.ID]
	te.taskMu.Unlock()
	if active {
		t.Fatal("executor left registered for assistant-last task")
	}
}

// --- the stop must be the last write to the row ---

// TestStopWithAssignedPortsLeavesTaskPaused: stopping a task that holds
// forwarded ports must leave it paused with the ports cleared. Clearing them
// used to be a full-row write, which put "running" back after the stop and left
// the task stuck until a second Stop.
func TestStopWithAssignedPortsLeavesTaskPaused(t *testing.T) {
	te := newSessionCostTestExecutor(t)
	s := te.store
	proj := createProject(t, s, "proj-stop-ports", "Stop Ports")

	mock := newHeldLLMMock()
	server := httptest.NewServer(mock.Handler())
	defer server.Close()

	setup := runExecTask(t, te, proj, "stop-ports-1", server.URL, "task one")
	mock.waitForArrival(t, 1)
	waitTaskState(t, s, "stop-ports-1", isRunning)

	// A terminal session forwarded a port. Terminals write the column directly, which
	// is precisely the case the exit path must catch: the executor's own copy of the
	// task never learns about it. (It used to be staged onto setup.task as well —
	// that only worked because the test and the loop shared one struct.)
	ports := []types.AssignedPort{{HostPort: 32145, ContainerPort: 3000}}
	if err := s.SetTaskAssignedPorts("stop-ports-1", proj.ID, ports); err != nil {
		t.Fatalf("assign ports: %v", err)
	}

	if err := te.StopTask("stop-ports-1"); err != nil {
		t.Fatalf("StopTask: %v", err)
	}

	// The ordering itself: StopTask returns only after the executor's last write,
	// so its own transition is the final one.
	select {
	case <-setup.finished:
	default:
		t.Fatal("StopTask returned before the executor finished its exit writes")
	}

	// So the row is paused already, not "running" awaiting a stale write.
	got, err := s.GetTaskByID("stop-ports-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.ExecutionState != types.ExecStatePaused {
		t.Fatalf("row right after StopTask: exec=%s, want paused", got.ExecutionState)
	}

	select {
	case <-setup.done:
	case <-time.After(10 * time.Second):
		t.Fatal("executor did not exit after StopTask")
	}

	final := waitTaskState(t, s, "stop-ports-1", isPaused)
	if len(final.AssignedPorts) != 0 {
		t.Fatalf("assigned_ports must be cleared when the run ends: %+v", final.AssignedPorts)
	}

	// Give any late writer a chance to land, then re-check.
	time.Sleep(500 * time.Millisecond)
	after, err := s.GetTaskByID("stop-ports-1")
	if err != nil {
		t.Fatalf("get task after exit: %v", err)
	}
	if after.ExecutionState != types.ExecStatePaused {
		t.Fatalf("executor exit resurrected the run: exec=%s", after.ExecutionState)
	}
}

// TestStaleWriterCannotResurrectButRealClaimCan: after a stop a stale full-row
// write must not restart the run, but the resumed run's own claim must work.
func TestStaleWriterCannotResurrectButRealClaimCan(t *testing.T) {
	te := newSessionCostTestExecutor(t)
	s := te.store
	proj := createProject(t, s, "proj-claim", "Claim")

	mock := newHeldLLMMock()
	server := httptest.NewServer(mock.Handler())
	defer server.Close()

	setup := runExecTask(t, te, proj, "claim-1", server.URL, "task one")
	mock.waitForArrival(t, 1)
	waitTaskState(t, s, "claim-1", isRunning)

	if err := te.StopTask("claim-1"); err != nil {
		t.Fatalf("StopTask: %v", err)
	}
	select {
	case <-setup.done:
	case <-time.After(10 * time.Second):
		t.Fatal("executor did not exit after StopTask")
	}
	waitTaskState(t, s, "claim-1", isPaused)

	// A stale full-row write, the shape of a dying executor's copy.
	stale, err := s.GetTaskByID("claim-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	stale.ExecutionState = types.ExecStateRunning
	stale.Output = "written by a stale copy"
	if err := s.UpdateTask(stale); err != nil {
		t.Fatalf("stale write: %v", err)
	}
	// The struct mirrors what was stored instead of keeping its stale value.
	if stale.ExecutionState != types.ExecStatePaused {
		t.Fatalf("in-memory copy after guarded write: exec=%s, want paused", stale.ExecutionState)
	}
	after, err := s.GetTaskByID("claim-1")
	if err != nil {
		t.Fatalf("get task after stale write: %v", err)
	}
	if after.ExecutionState != types.ExecStatePaused {
		t.Fatalf("guarded UpdateTask resurrected the run: exec=%s", after.ExecutionState)
	}
	if after.Output != "written by a stale copy" {
		t.Fatalf("guard must protect execution_state only, output=%q", after.Output)
	}

	// The resumed run may claim the row again.
	startExecTaskExisting(t, te, proj, "claim-1", server.URL)
	mock.waitForArrival(t, 2)
	waitTaskState(t, s, "claim-1", isRunning)

	te.StopTask("claim-1")
}
