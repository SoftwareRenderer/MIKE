package exec

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"mike/backend/store"
	"mike/backend/stream"
	"mike/backend/types"

	"github.com/google/uuid"
)

func mustTask(t *testing.T, s *store.Store, id string) *types.Task {
	t.Helper()
	task, err := s.GetTaskByID(id)
	if err != nil {
		t.Fatalf("get task %s: %v", id, err)
	}
	return task
}

// makeTaskRow creates a task in the state the pause and the scheduler are judged on.
func makeTaskRow(t *testing.T, s *store.Store, projectID, id, parentID string, status types.Status, execState types.ExecutionState) {
	t.Helper()
	now := time.Now().UTC()
	if err := s.CreateTask(&types.Task{
		ID: id, ProjectID: projectID, Title: id, Prompt: id + " prompt",
		Status: status, ExecutionState: execState, ParentTaskID: parentID,
		CurrentThreadID: "thread-" + id, LlamaServerURL: "http://localhost:8080", ModelName: "test-model",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create task %s: %v", id, err)
	}
}

// TestPauseTaskAndDescendants_StopsEveryRowThatCouldRun: a pause on a parent writes paused to every row of
// its branch that could still run, and leaves Backlog, Review and Done rows where they are.
func TestPauseTaskAndDescendants_StopsEveryRowThatCouldRun(t *testing.T) {
	t.Parallel()
	te, _, projectID := setupExecutorWithGitForSubtask(t)
	s := te.store

	makeTaskRow(t, s, projectID, "pd-parent", "", "in_progress", "idle")
	makeTaskRow(t, s, projectID, "pd-running", "pd-parent", "in_progress", "running")
	makeTaskRow(t, s, projectID, "pd-pending", "pd-parent", "in_progress", "idle")
	makeTaskRow(t, s, projectID, "pd-grand", "pd-pending", "in_progress", "idle")
	makeTaskRow(t, s, projectID, "pd-backlog", "pd-parent", "backlog", "idle")
	makeTaskRow(t, s, projectID, "pd-review", "pd-parent", "review", "idle")
	makeTaskRow(t, s, projectID, "pd-done", "pd-parent", "done", "completed")

	if err := te.PauseTaskAndDescendants("pd-parent"); err != nil {
		t.Fatalf("pause: %v", err)
	}

	for _, id := range []string{"pd-parent", "pd-running", "pd-pending", "pd-grand"} {
		if got := mustTask(t, s, id); got.ExecutionState != types.ExecStatePaused {
			t.Errorf("%s: execution_state = %s, want paused", id, got.ExecutionState)
		}
	}
	// The row that held the run keeps the output text a stopped run reports.
	if got := mustTask(t, s, "pd-running"); got.Output != "Paused by user" {
		t.Errorf("the stopped row's output = %q, want the pause sentence", got.Output)
	}

	backlog := mustTask(t, s, "pd-backlog")
	if backlog.Status != types.StatusBacklog || backlog.ExecutionState != types.ExecStateIdle {
		t.Errorf("a backlog row is not this pause's to stop: %s/%s", backlog.Status, backlog.ExecutionState)
	}
	review := mustTask(t, s, "pd-review")
	if review.Status != types.StatusReview || review.ExecutionState != types.ExecStateIdle {
		t.Errorf("a row waiting in Review must stay there: %s/%s", review.Status, review.ExecutionState)
	}
	done := mustTask(t, s, "pd-done")
	if done.Status != types.StatusDone || done.ExecutionState != types.ExecStateCompleted {
		t.Errorf("a done row must stay done: %s/%s", done.Status, done.ExecutionState)
	}
}

// TestAutoStartNextSibling_LeavesPausedSiblingPaused: another subtask finishing does not restart a paused
// sibling.
func TestAutoStartNextSibling_LeavesPausedSiblingPaused(t *testing.T) {
	t.Parallel()
	te, _, projectID := setupExecutorWithGitForSubtask(t)
	s := te.store

	makeTaskRow(t, s, projectID, "ps-parent", "", "in_progress", "idle")
	makeTaskRow(t, s, projectID, "ps-finished", "ps-parent", "review", "idle")
	makeTaskRow(t, s, projectID, "ps-paused", "ps-parent", "in_progress", "paused")

	if err := te.AutoStartNextSibling(projectID, "ps-finished", "ps-parent"); err != nil {
		t.Fatalf("AutoStartNextSibling: %v", err)
	}

	if liveExecutor(te, "ps-paused") != nil {
		t.Error("a paused subtask must not be started by the scheduler")
	}
	if got := mustTask(t, s, "ps-paused"); got.ExecutionState != types.ExecStatePaused {
		t.Errorf("paused subtask left the pause without a person: %s", got.ExecutionState)
	}
}

// TestAutoStartNextSibling_PausedParentLetsAncestorsOtherBranchRun: pausing one branch stops that branch,
// not the board, so the chain bubbles past a paused parent to the ancestor's other branches.
func TestAutoStartNextSibling_PausedParentLetsAncestorsOtherBranchRun(t *testing.T) {
	t.Parallel()
	te, _, projectID := setupExecutorWithGitForSubtask(t)
	s := te.store

	makeTaskRow(t, s, projectID, "pp-grand", "", "in_progress", "idle")
	makeTaskRow(t, s, projectID, "pp-paused", "pp-grand", "in_progress", "paused")
	makeTaskRow(t, s, projectID, "pp-child", "pp-paused", "in_progress", "paused")
	makeTaskRow(t, s, projectID, "pp-other", "pp-grand", "in_progress", "idle")
	t.Cleanup(func() { _ = te.StopTask("pp-other") })

	// pp-child reports its exit: nothing starts inside its paused branch.
	if err := te.AutoStartNextSibling(projectID, "pp-child", "pp-paused"); err != nil {
		t.Fatalf("AutoStartNextSibling: %v", err)
	}
	// and the ancestor's other branch is the one that goes on.
	if liveExecutor(te, "pp-other") == nil {
		t.Error("the ancestor's other branch should progress while a sibling branch is paused")
	}
	if liveExecutor(te, "pp-child") != nil {
		t.Error("nothing starts inside a paused branch")
	}
}

// TestPauseParent_StoppedSubtaskExitStartsNoSibling: pausing a parent must not start its pending subtasks.
// The stopped run hands its exit to the scheduler (loop.go, loop_agent.go), which reads this branch's rows.
func TestPauseParent_StoppedSubtaskExitStartsNoSibling(t *testing.T) {
	te, _, projectID := setupExecutorWithGitForSubtask(t)
	s := te.store

	mock := newHeldLLMMock()
	server := httptest.NewServer(mock.Handler())
	defer server.Close()

	makeTaskRowWithURL(t, s, projectID, "psp-parent", "", server.URL, "in_progress", "idle")
	makeTaskRowWithURL(t, s, projectID, "psp-pending2", "psp-parent", server.URL, "in_progress", "idle")
	makeTaskRowWithURL(t, s, projectID, "psp-pending3", "psp-parent", server.URL, "in_progress", "idle")
	runHeldSubtask(t, te, projectID, "psp-running", "psp-parent", server.URL)
	waitTaskState(t, s, "psp-running", isRunning)
	mock.waitForArrival(t, 1)

	if err := te.PauseTaskAndDescendants("psp-parent"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	for _, id := range []string{"psp-parent", "psp-running", "psp-pending2", "psp-pending3"} {
		if got := mustTask(t, s, id); got.ExecutionState != types.ExecStatePaused {
			t.Errorf("%s after the pause: execution_state = %s, want paused", id, got.ExecutionState)
		}
	}

	// What a run does as it ends: loop.go and loop_agent.go both hand this call to the scheduler with the
	// stopped task and its parent.
	if err := te.AutoStartNextSibling(projectID, "psp-running", "psp-parent"); err != nil {
		t.Fatalf("AutoStartNextSibling: %v", err)
	}
	for _, id := range []string{"psp-pending2", "psp-pending3"} {
		if liveExecutor(te, id) != nil {
			t.Errorf("%s was started in a branch a person paused", id)
		}
		if got := mustTask(t, s, id); got.ExecutionState != types.ExecStatePaused {
			t.Errorf("%s left the pause without a person: %s", id, got.ExecutionState)
		}
	}
}

// TestRetryTask_WithUnfinishedChildrenResumesTheBranch: resuming a parent resumes its children and leaves
// the order to the schedule, so the deepest ready row runs and the parent never runs its own turn.
func TestRetryTask_WithUnfinishedChildrenResumesTheBranch(t *testing.T) {
	t.Parallel()
	te, _, projectID := setupExecutorWithGitForSubtask(t)
	s := te.store

	fx := pausedChainFixture(t, te, projectID)

	if err := te.PauseTaskAndDescendants(fx.parentID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	// While it is paused, not even a sibling's finish starts anything under the parent.
	if err := te.AutoStartNextSibling(projectID, fx.finishedID, fx.parentID); err != nil {
		t.Fatalf("AutoStartNextSibling: %v", err)
	}
	for _, id := range []string{fx.leafID, fx.deepID} {
		if liveExecutor(te, id) != nil {
			t.Fatalf("%s started while the parent was paused", id)
		}
	}

	if err := te.RetryTask(projectID, fx.parentID); err != nil {
		t.Fatalf("resume: %v", err)
	}

	for _, id := range []string{fx.parentID, fx.leafID, fx.waitID, fx.deepID} {
		if got := mustTask(t, s, id); got.ExecutionState == types.ExecStatePaused {
			t.Errorf("%s is still paused after a person resumed the branch", id)
		}
	}
	// The parent waits on its children: a resume of a parent runs a subtask, never its own turn.
	if liveExecutor(te, fx.parentID) != nil {
		t.Error("a parent with unfinished children must not run its own turn")
	}
	// Sequentially, one branch goes: the deepest ready row, not the ready leaf beside it.
	if liveExecutor(te, fx.deepID) == nil {
		t.Error("the deepest ready subtask should have started")
	}
	if liveExecutor(te, fx.leafID) != nil {
		t.Error("sequentially, the ready leaf waits for the next finish")
	}
}

// TestStartReadySubtasks_ParallelResumeStartsEveryReadyBranch: the same resume with "run subtasks in
// parallel" starts every ready branch, and a parent whose own child is unfinished stays started-but-pending.
func TestStartReadySubtasks_ParallelResumeStartsEveryReadyBranch(t *testing.T) {
	t.Parallel()
	te, _, projectID := setupExecutorWithGitForSubtask(t)
	s := te.store
	if err := te.store.SetConfigValue("run_siblings_parallel", "true"); err != nil {
		t.Fatal(err)
	}

	fx := pausedChainFixture(t, te, projectID)
	if err := te.PauseTaskAndDescendants(fx.parentID); err != nil {
		t.Fatalf("pause: %v", err)
	}

	if _, err := te.StartReadySubtasks(projectID, fx.parentID); err != nil {
		t.Fatalf("StartReadySubtasks: %v", err)
	}

	for _, id := range []string{fx.leafID, fx.deepID} {
		if liveExecutor(te, id) == nil {
			t.Errorf("%s should have started under the parallel preference", id)
		}
	}
	waiter := mustTask(t, s, fx.waitID)
	if waiter.ExecutionState == types.ExecStatePaused {
		t.Errorf("%s is still paused after a person resumed the branch", fx.waitID)
	}
	if liveExecutor(te, fx.waitID) != nil {
		t.Errorf("%s must stay unstarted while its own child is unfinished", fx.waitID)
	}
}

// pausedChainFixture is newParallelChainFixture's tree with the waiting rows moved to In Progress, where a
// started parent keeps them: a finished child, a ready leaf, and a sibling waiting on its own ready child.
func pausedChainFixture(t *testing.T, te *TaskExecutor, projectID string) parallelChainFixture {
	t.Helper()
	fx := newParallelChainFixture(t, te, projectID)
	for _, id := range []string{fx.deepID} {
		deep := mustTask(t, te.store, id)
		deep.Status = types.StatusInProgress
		deep.ExecutionState = types.ExecStateIdle
		if err := te.store.UpdateTask(deep); err != nil {
			t.Fatal(err)
		}
	}
	return fx
}

// makeTaskRowWithURL is makeTaskRow pointed at a mock provider, so a started subtask reaches the mock
// rather than an address nobody serves.
func makeTaskRowWithURL(t *testing.T, s *store.Store, projectID, id, parentID, serverURL string, status types.Status, execState types.ExecutionState) {
	t.Helper()
	makeTaskRow(t, s, projectID, id, parentID, status, execState)
	task := mustTask(t, s, id)
	task.LlamaServerURL = serverURL
	if err := s.UpdateTask(task); err != nil {
		t.Fatal(err)
	}
}

// runHeldSubtask starts a real executeTask goroutine whose LLM stream stays open until the mock releases
// it, which is what a subtask mid-turn looks like to a pause.
func runHeldSubtask(t *testing.T, te *TaskExecutor, projectID, taskID, parentID, serverURL string) {
	t.Helper()
	s := te.store
	makeTaskRowWithURL(t, s, projectID, taskID, parentID, serverURL, "in_progress", "idle")
	task := mustTask(t, s, taskID)
	task.OperatingMode = "act"
	if err := s.UpdateTask(task); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTaskMessage(&types.TaskMessage{
		ID: uuid.New().String(), TaskID: taskID, ThreadID: task.CurrentThreadID,
		Role: "user", Content: task.Prompt, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	finished := make(chan struct{})
	setup := &taskExecSetup{
		task: task, project: &types.Project{ID: projectID}, projectID: projectID,
		worktreePath: t.TempDir(), serverURL: serverURL, modelName: "test-model",
		ctx: ctx, cancel: cancel,
		userMsgChan: make(chan stream.UserMessage, 16),
		summarizeCh: make(chan *summarizeRequest, 16),
		done:        done, finished: finished,
	}
	te.taskMu.Lock()
	te.tasks[taskID] = &executionState{ctx: ctx, cancel: cancel, userMsgChan: setup.userMsgChan,
		summarizeCh: setup.summarizeCh, executorType: "llm", done: done, finished: finished}
	te.taskMu.Unlock()
	go te.executeTask(setup)
}
