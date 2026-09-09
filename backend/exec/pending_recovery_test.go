package exec

import (
	"context"
	"fmt"
	"testing"
	"time"

	"kanban/backend/types"
)

// seedPendingTask creates an "In Progress + Pending" task whose thread tail is the given
// roles, written in order a second apart.
func seedPendingTask(t *testing.T, te *TaskExecutor, projectID, id string, roles ...string) *types.Task {
	t.Helper()
	now := time.Now().UTC()
	task := &types.Task{
		ID: id, ProjectID: projectID, Title: id, Prompt: "prompt",
		Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle,
		LlamaServerURL: "http://localhost:8080", ModelName: "test-model",
		CurrentThreadID: "thread-" + id,
		CreatedAt:       now, UpdatedAt: now,
	}
	if err := te.store.CreateTask(task); err != nil {
		t.Fatalf("create task %s: %v", id, err)
	}
	for i, role := range roles {
		m := &types.TaskMessage{
			ID: fmt.Sprintf("%s-%d-%s", id, i, role), TaskID: id, ThreadID: task.CurrentThreadID,
			Role: role, Content: "x", CreatedAt: now.Add(time.Duration(i) * time.Second),
		}
		if err := te.store.CreateTaskMessage(m); err != nil {
			t.Fatalf("create message for %s: %v", id, err)
		}
	}
	return task
}

// TestRecoverStaleTasksNormalizesPendingRows checks the startup cleanup of tasks stranded
// in "In Progress + Pending", and that every legitimately pending row is left alone.
func TestRecoverStaleTasksNormalizesPendingRows(t *testing.T) {
	te := newSessionCostTestExecutor(t)
	proj := createProject(t, te.store, "proj-pend", "Pending")

	// normalized: the agent has the last word, so the conversation waits on the user
	agentLast := seedPendingTask(t, te, proj.ID, "pend-agent-last", "user", "assistant")
	toolLast := seedPendingTask(t, te, proj.ID, "pend-tool-last", "user", "assistant", "tool_call")

	// left alone: unanswered user message (dropped send, or still queued at shutdown),
	// and a thread with no messages
	userLast := seedPendingTask(t, te, proj.ID, "pend-user-last", "assistant", "user")
	empty := seedPendingTask(t, te, proj.ID, "pend-empty")

	// left alone: the work is in the unfinished subtasks
	parent := seedPendingTask(t, te, proj.ID, "pend-parent", "user", "assistant")
	child := seedPendingTask(t, te, proj.ID, "pend-parent-child")
	child.ParentTaskID = parent.ID
	child.Status = types.StatusBacklog
	if err := te.store.UpdateTask(child); err != nil {
		t.Fatalf("make child: %v", err)
	}

	// left alone: a live executor (queued for a slot, or a message just accepted)
	live := seedPendingTask(t, te, proj.ID, "pend-live", "user", "assistant")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	te.taskMu.Lock()
	te.tasks[live.ID] = &executionState{ctx: ctx, cancel: cancel}
	te.taskMu.Unlock()
	t.Cleanup(func() {
		te.taskMu.Lock()
		delete(te.tasks, live.ID)
		te.taskMu.Unlock()
	})

	// in-flight rows are recovered by the same call; this one ends up paused
	running := createTaskRow(t, te.store, "pend-running", proj.ID, "in_progress", "running")

	te.RecoverStaleTasks()

	for _, id := range []string{agentLast.ID, toolLast.ID} {
		got := waitTaskState(t, te.store, id, func(st types.Status, es types.ExecutionState) bool { return st == "review" && es == "idle" })
		if got.Status != types.StatusReview || got.ExecutionState != types.ExecStateIdle {
			t.Errorf("%s: status=%q state=%q, want review/idle", id, got.Status, got.ExecutionState)
		}
	}
	for _, tc := range []struct {
		id       string
		wantStat types.Status
	}{
		{userLast.ID, "in_progress"}, // unanswered user message — not "agent is done"
		{empty.ID, "in_progress"},    // nothing to judge it by
		{parent.ID, "in_progress"},   // waiting on unfinished subtasks
		{live.ID, "in_progress"},     // live executor: queued or just accepted
	} {
		got, err := te.store.GetTaskByID(tc.id)
		if err != nil {
			t.Fatalf("get %s: %v", tc.id, err)
		}
		if got.Status != tc.wantStat || got.ExecutionState != types.ExecStateIdle {
			t.Errorf("%s: status=%q state=%q, want %s/idle (legitimate pending)", tc.id, got.Status, got.ExecutionState, tc.wantStat)
		}
	}

	// the in-flight recovery still ran
	if got, err := te.store.GetTaskByID(running.ID); err != nil || got.ExecutionState != types.ExecStatePaused {
		t.Errorf("in-flight row: state=%q err=%v, want paused", got.ExecutionState, err)
	}
}

// TestRecoverStaleTasksLeavesPendingCountReported pins that the return value stays the
// count of recovered in-flight rows, so the startup log keeps the two jobs apart.
func TestRecoverStaleTasksLeavesPendingCountReported(t *testing.T) {
	te := newSessionCostTestExecutor(t)
	proj := createProject(t, te.store, "proj-pend-cnt", "Pending Count")

	seedPendingTask(t, te, proj.ID, "cnt-pending", "user", "assistant")
	createTaskRow(t, te.store, "cnt-running", proj.ID, "in_progress", "running")

	if n := te.RecoverStaleTasks(); n != 1 {
		t.Fatalf("recovered = %d, want 1 (in-flight rows only; the pending normalization is logged separately)", n)
	}
	if got, err := te.store.GetTaskByID("cnt-pending"); err != nil || got.Status != types.StatusReview {
		t.Errorf("pending row not normalized: status=%q err=%v", got.Status, err)
	}
}
