package store

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mike/backend/types"
)

func TestCreateAndGetTask(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	got, err := s.GetTask("proj-1", "task-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.ID != "task-1" || got.ProjectID != "proj-1" || got.Status != types.StatusBacklog {
		t.Fatalf("unexpected task: %+v", got)
	}
}

func TestTaskAutostartedPersistence(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-autostart", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Default is false (not auto-started)
	task := newTask("task-default", "proj-autostart")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	got, err := s.GetTask("proj-autostart", "task-default")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Autostarted {
		t.Errorf("default Autostarted should be false")
	}

	// Explicitly set true and persist via UpdateTask
	got.Autostarted = true
	if err := s.UpdateTask(got); err != nil {
		t.Fatalf("update task: %v", err)
	}
	got2, err := s.GetTaskByID("task-default")
	if err != nil {
		t.Fatalf("get task by ID: %v", err)
	}
	if !got2.Autostarted {
		t.Errorf("Autostarted should be true after update")
	}

	// Create a task with Autostarted=true directly
	autoTask := newTask("task-auto", "proj-autostart")
	autoTask.Autostarted = true
	if err := s.CreateTask(autoTask); err != nil {
		t.Fatalf("create auto task: %v", err)
	}
	autoGot, err := s.GetTask("proj-autostart", "task-auto")
	if err != nil {
		t.Fatalf("get auto task: %v", err)
	}
	if !autoGot.Autostarted {
		t.Errorf("Autostarted should be true for created task")
	}
}

func TestGetTaskNotFound(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	_, err := s.GetTask("proj-1", "task-1")
	if err == nil {
		t.Fatal("expected error for missing task")
	}
}

func TestGetTaskByID(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	got, err := s.GetTaskByID("task-1")
	if err != nil {
		t.Fatalf("get task by ID: %v", err)
	}
	if got.ID != "task-1" {
		t.Fatalf("unexpected task: %+v", got)
	}
}

func TestGetTasksByProject(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Created newest-last in the loop; the board query returns newest created first.
	base := time.Now().UTC().Truncate(time.Second)
	for i, id := range []string{"task-1", "task-2", "task-3"} {
		task := newTask(id, "proj-1")
		task.CreatedAt = base.Add(time.Duration(i) * time.Second)
		task.UpdatedAt = task.CreatedAt
		if err := s.CreateTask(task); err != nil {
			t.Fatalf("create task %s: %v", id, err)
		}
	}

	tasks, err := s.GetTasksByProject("proj-1")
	if err != nil {
		t.Fatalf("get tasks: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}
	// Should be ordered newest created first (rowid DESC keeps equal timestamps stable).
	if tasks[0].ID != "task-3" || tasks[1].ID != "task-2" || tasks[2].ID != "task-1" {
		t.Fatalf("unexpected order: %v, %v, %v", tasks[0].ID, tasks[1].ID, tasks[2].ID)
	}
}

func TestUpdateTask(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Update status; starting a run claims the in-flight state.
	task.Status = types.StatusInProgress
	task.ExecutionState = types.ExecStateRunning
	if err := s.UpdateTaskClaimingExecution(task); err != nil {
		t.Fatalf("update task: %v", err)
	}

	got, err := s.GetTask("proj-1", "task-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Status != types.StatusInProgress || got.ExecutionState != types.ExecStateRunning {
		t.Fatalf("unexpected status: %s, %s", got.Status, got.ExecutionState)
	}
}

func TestUpdateTaskExecutionState(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	if err := s.UpdateTaskExecutionState("task-1", "proj-1", "running"); err != nil {
		t.Fatalf("update status state: %v", err)
	}

	got, err := s.GetTask("proj-1", "task-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.ExecutionState != types.ExecStateRunning {
		t.Fatalf("unexpected status state: %s", got.ExecutionState)
	}
	if got.Status != types.StatusBacklog {
		t.Fatalf("status should be unchanged: %s", got.Status)
	}
}

func TestDeleteTask(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	if err := s.DeleteTask("proj-1", "task-1"); err != nil {
		t.Fatalf("delete task: %v", err)
	}

	_, err := s.GetTask("proj-1", "task-1")
	if err == nil {
		t.Fatal("expected error for deleted task")
	}
}

func TestGetAllTaskIDs(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	for _, id := range []string{"task-1", "task-2"} {
		if err := s.CreateTask(newTask(id, "proj-1")); err != nil {
			t.Fatalf("create task: %v", err)
		}
	}

	ids, err := s.GetAllTaskIDs()
	if err != nil {
		t.Fatalf("get all task IDs: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 task IDs, got %d", len(ids))
	}
}

func TestTaskBoolFields(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := newTask("task-1", "proj-1")
	task.AutoCommit = true

	task.OperatingMode = "plan"
	task.TitleGenerating = true
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	got, err := s.GetTask("proj-1", "task-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if !got.AutoCommit || !got.IsPlanMode() || !got.TitleGenerating {
		t.Fatalf("bool fields not preserved: %+v", got)
	}
}

func TestTaskParentTaskID(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	parent := newTask("parent-1", "proj-1")
	if err := s.CreateTask(parent); err != nil {
		t.Fatalf("create parent task: %v", err)
	}

	child := newTask("child-1", "proj-1")
	child.ParentTaskID = "parent-1"
	if err := s.CreateTask(child); err != nil {
		t.Fatalf("create child task: %v", err)
	}

	got, err := s.GetTask("proj-1", "child-1")
	if err != nil {
		t.Fatalf("get child task: %v", err)
	}
	if got.ParentTaskID != "parent-1" {
		t.Fatalf("unexpected parent_task_id: %s", got.ParentTaskID)
	}
}

func TestTaskCascadeDelete(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	msg := newMessage("msg-1", "task-1", "user", "hello")
	if err := s.CreateTaskMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}

	// Delete task — messages should be CASCADE deleted
	if err := s.DeleteTask("proj-1", "task-1"); err != nil {
		t.Fatalf("delete task: %v", err)
	}

	msgs, err := s.GetTaskMessages("task-1", "")
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages after cascade delete, got %d", len(msgs))
	}
}

func TestTaskCreatedAtUpdatedAt(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	createdAt := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	task := newTask("task-1", "proj-1")
	task.CreatedAt = createdAt
	task.UpdatedAt = createdAt
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	got, err := s.GetTask("proj-1", "task-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if !got.CreatedAt.Equal(createdAt) {
		t.Fatalf("created_at mismatch: %v vs %v", got.CreatedAt, createdAt)
	}

	// Update should change updated_at
	time.Sleep(10 * time.Millisecond)
	if err := s.UpdateTask(got); err != nil {
		t.Fatalf("update task: %v", err)
	}

	got2, err := s.GetTask("proj-1", "task-1")
	if err != nil {
		t.Fatalf("get task after update: %v", err)
	}
	if got2.UpdatedAt.Before(got.UpdatedAt) {
		t.Fatalf("updated_at should have increased")
	}
}

func TestClearStaleTitleGenerating(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create tasks with title_generating = true
	for _, id := range []string{"task-1", "task-2"} {
		task := newTask(id, "proj-1")
		task.TitleGenerating = true
		if err := s.CreateTask(task); err != nil {
			t.Fatalf("create task: %v", err)
		}
	}

	// Create a task with title_generating = false
	task := newTask("task-3", "proj-1")
	task.TitleGenerating = false
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	if err := s.ClearStaleTitleGenerating(); err != nil {
		t.Fatalf("clear stale: %v", err)
	}

	for _, id := range []string{"task-1", "task-2", "task-3"} {
		got, err := s.GetTask("proj-1", id)
		if err != nil {
			t.Fatalf("get task %s: %v", id, err)
		}
		if got.TitleGenerating {
			t.Fatalf("task %s should have title_generating=false after clear", id)
		}
	}
}

func TestSetTaskBranchIfEmpty(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Task starts with empty branch
	got, _ := s.GetTask("proj-1", "task-1")
	if got.Branch != "" {
		t.Fatalf("expected empty branch, got %q", got.Branch)
	}

	// First call should succeed
	set, err := s.SetTaskBranchIfEmpty("task-1", "proj-1", "task/branch-1")
	if err != nil {
		t.Fatalf("set branch: %v", err)
	}
	if !set {
		t.Fatal("expected branch to be set")
	}

	// Verify branch was set
	got, _ = s.GetTask("proj-1", "task-1")
	if got.Branch != "task/branch-1" {
		t.Fatalf("expected branch 'task/branch-1', got %q", got.Branch)
	}

	// Second call should fail (branch already set)
	set, err = s.SetTaskBranchIfEmpty("task-1", "proj-1", "task/branch-2")
	if err != nil {
		t.Fatalf("set branch again: %v", err)
	}
	if set {
		t.Fatal("expected branch NOT to be set (already non-empty)")
	}

	// Branch should still be the original
	got, _ = s.GetTask("proj-1", "task-1")
	if got.Branch != "task/branch-1" {
		t.Fatalf("expected branch unchanged 'task/branch-1', got %q", got.Branch)
	}
}

func TestSetTaskBranchIfEmpty_WrongProject(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Call with wrong project ID should not set branch
	set, err := s.SetTaskBranchIfEmpty("task-1", "wrong-proj", "task/branch-1")
	if err != nil {
		t.Fatalf("set branch: %v", err)
	}
	if set {
		t.Fatal("expected branch NOT to be set (wrong project)")
	}

	// Branch should still be empty
	got, _ := s.GetTask("proj-1", "task-1")
	if got.Branch != "" {
		t.Fatalf("expected empty branch, got %q", got.Branch)
	}
}

func TestSetTaskBranchIfEmpty_Concurrent(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Concurrent calls — only one should succeed
	const goroutines = 10
	results := make(chan bool, goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			set, _ := s.SetTaskBranchIfEmpty("task-1", "proj-1", "task/branch-"+string(rune('0'+i)))
			results <- set
		}(i)
	}

	successCount := 0
	for i := 0; i < goroutines; i++ {
		if <-results {
			successCount++
		}
	}
	if successCount != 1 {
		t.Fatalf("expected exactly 1 success from %d concurrent calls, got %d", goroutines, successCount)
	}

	// Branch should be set to one of the values
	got, _ := s.GetTask("proj-1", "task-1")
	if got.Branch == "" {
		t.Fatal("expected branch to be set")
	}
	if len(got.Branch) != len("task/branch-0") {
		t.Fatalf("unexpected branch format: %q", got.Branch)
	}
}

func TestClearTaskBranch(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	set, err := s.SetTaskBranchIfEmpty("task-1", "proj-1", "task/branch-1")
	if err != nil || !set {
		t.Fatalf("set branch: set=%v, err=%v", set, err)
	}

	// Clear it
	err = s.ClearTaskBranch("task-1", "proj-1")
	if err != nil {
		t.Fatalf("clear branch: %v", err)
	}

	// Verify branch is empty
	got, _ := s.GetTask("proj-1", "task-1")
	if got.Branch != "" {
		t.Fatalf("expected empty branch after clear, got %q", got.Branch)
	}

	// SetTaskBranchIfEmpty should work again (branch is empty)
	set, err = s.SetTaskBranchIfEmpty("task-1", "proj-1", "task/branch-2")
	if err != nil || !set {
		t.Fatalf("set branch after clear: set=%v, err=%v", set, err)
	}
	got, _ = s.GetTask("proj-1", "task-1")
	if got.Branch != "task/branch-2" {
		t.Fatalf("expected branch 'task/branch-2', got %q", got.Branch)
	}
}

func TestClearTaskBranch_WrongProject(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	set, err := s.SetTaskBranchIfEmpty("task-1", "proj-1", "task/branch-1")
	if err != nil || !set {
		t.Fatalf("set branch: set=%v, err=%v", set, err)
	}

	// Clear with wrong project — should not affect the branch
	err = s.ClearTaskBranch("task-1", "wrong-proj")
	if err != nil {
		t.Fatalf("clear branch with wrong project: %v", err)
	}
	got, _ := s.GetTask("proj-1", "task-1")
	if got.Branch != "task/branch-1" {
		t.Fatalf("expected branch unchanged, got %q", got.Branch)
	}
}

func TestTaskSessionCost(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create task with session cost
	task := newTask("task-1", "proj-1")
	task.SessionCost = 0.42
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Verify session cost is persisted
	got, err := s.GetTask("proj-1", "task-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.SessionCost != 0.42 {
		t.Fatalf("expected session_cost 0.42, got %f", got.SessionCost)
	}

	// Update session cost
	got.SessionCost = 1.23
	if err := s.UpdateTask(got); err != nil {
		t.Fatalf("update task: %v", err)
	}

	got2, err := s.GetTask("proj-1", "task-1")
	if err != nil {
		t.Fatalf("get task after update: %v", err)
	}
	if got2.SessionCost != 1.23 {
		t.Fatalf("expected session_cost 1.23, got %f", got2.SessionCost)
	}

	// Default session cost should be 0
	task2 := newTask("task-2", "proj-1")
	if err := s.CreateTask(task2); err != nil {
		t.Fatalf("create task2: %v", err)
	}
	got3, err := s.GetTask("proj-1", "task-2")
	if err != nil {
		t.Fatalf("get task2: %v", err)
	}
	if got3.SessionCost != 0 {
		t.Fatalf("expected default session_cost 0, got %f", got3.SessionCost)
	}
}

// TestSetTaskSummarizing verifies the dedicated flag transition: the flag
// round-trips and updated_at is bumped (used as the summary anchor).
func TestSetTaskSummarizing(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-sum", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := newTask("task-sum", "proj-sum")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.SetTaskSummarizing("task-sum", true); err != nil {
		t.Fatalf("set summarizing: %v", err)
	}
	got, err := s.GetTaskByID("task-sum")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if !got.Summarizing {
		t.Error("Summarizing should be true after SetTaskSummarizing(true)")
	}
	before := got.UpdatedAt
	time.Sleep(1100 * time.Millisecond) // stored timestamps have second precision
	if err := s.SetTaskSummarizing("task-sum", false); err != nil {
		t.Fatalf("clear summarizing: %v", err)
	}
	got, err = s.GetTaskByID("task-sum")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Summarizing {
		t.Error("Summarizing should be false after SetTaskSummarizing(false)")
	}
	if !got.UpdatedAt.After(before) {
		t.Errorf("updated_at should be bumped by SetTaskSummarizing (was %s, now %s)", before, got.UpdatedAt)
	}
}

// TestUpdateTask_DoesNotTouchSummarizingFlag is the regression test for the
// stuck-summarization-spinner bug: a full-row write from a task copy loaded
// while the flag was set must not resurrect the flag after it was cleared.
func TestUpdateTask_DoesNotTouchSummarizingFlag(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-sum-stale", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := newTask("task-stale", "proj-sum-stale")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// In-progress summarization: flag set in the DB, and a concurrent writer
	// (e.g. HandleSend / HandleByID / an executor persist) loads the row.
	if err := s.SetTaskSummarizing("task-stale", true); err != nil {
		t.Fatalf("set summarizing: %v", err)
	}
	stale, err := s.GetTaskByID("task-stale")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if !stale.Summarizing {
		t.Fatal("precondition: loaded copy should have Summarizing=true")
	}

	// Summarization completes: the flag is cleared via the dedicated update.
	if err := s.SetTaskSummarizing("task-stale", false); err != nil {
		t.Fatalf("clear summarizing: %v", err)
	}

	// The stale full-row write lands afterwards — it must not resurrect the
	// flag even though the in-memory copy still says true.
	if err := s.UpdateTask(stale); err != nil {
		t.Fatalf("update task: %v", err)
	}
	got, err := s.GetTaskByID("task-stale")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Summarizing {
		t.Error("full-row UpdateTask must not resurrect a cleared summarizing flag")
	}

	// Other fields on the same write must still be applied.
	stale.Title = "renamed"
	if err := s.UpdateTask(stale); err != nil {
		t.Fatalf("update task: %v", err)
	}
	got, err = s.GetTaskByID("task-stale")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Title != "renamed" {
		t.Errorf("expected title rename to be applied, got %q", got.Title)
	}
	if got.Summarizing {
		t.Error("full-row UpdateTask must not resurrect a cleared summarizing flag (after field change)")
	}
}

// TestCreateTaskSetsMovedAtFromCreated: a new task's position is its creation time.
func TestCreateTaskSetsMovedAtFromCreated(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	created := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	task := newTask("task-1", "proj-1")
	task.CreatedAt = created
	task.UpdatedAt = created
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	got, err := s.GetTask("proj-1", "task-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if !got.MovedAt.Equal(created) {
		t.Fatalf("expected moved_at = created_at (%v), got %v", created, got.MovedAt)
	}
}

// TestUpdateTaskSameStatusKeepsMovedAt: writes that leave the task in its column must not
// touch moved_at, even though updated_at keeps advancing.
func TestUpdateTaskSameStatusKeepsMovedAt(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	past := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	task := newTask("task-1", "proj-1")
	task.CreatedAt = past
	task.UpdatedAt = past
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	got, err := s.GetTask("proj-1", "task-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	got.Title = "renamed"
	got.Output = "some run output"
	got.SessionCost = 1.25
	got.ExecutionState = types.ExecStateRunning
	if err := s.UpdateTaskClaimingExecution(got); err != nil {
		t.Fatalf("update task: %v", err)
	}

	after, err := s.GetTask("proj-1", "task-1")
	if err != nil {
		t.Fatalf("get task after update: %v", err)
	}
	if !after.MovedAt.Equal(past) {
		t.Errorf("moved_at must not advance without a column change: %v -> %v", past, after.MovedAt)
	}
	if !after.UpdatedAt.After(past) {
		t.Errorf("updated_at should still advance on a plain write, got %v", after.UpdatedAt)
	}
	// UpdateTask mirrors the stored value back into the struct (RETURNING).
	if !got.MovedAt.Equal(past) {
		t.Errorf("expected in-memory moved_at to mirror the stored value %v, got %v", past, got.MovedAt)
	}
}

// TestUpdateTaskColumnChangeBumpsMovedAt: a real move re-ranks the card.
func TestUpdateTaskColumnChangeBumpsMovedAt(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	past := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	task := newTask("task-1", "proj-1")
	task.CreatedAt = past
	task.UpdatedAt = past
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	got, err := s.GetTask("proj-1", "task-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	got.Status = types.StatusReview
	if err := s.UpdateTask(got); err != nil {
		t.Fatalf("update task: %v", err)
	}

	after, err := s.GetTask("proj-1", "task-1")
	if err != nil {
		t.Fatalf("get task after move: %v", err)
	}
	if !after.MovedAt.After(past) {
		t.Fatalf("expected moved_at to advance on a column change, got %v (was %v)", after.MovedAt, past)
	}
	if !got.MovedAt.Equal(after.MovedAt) {
		t.Errorf("expected in-memory moved_at %v to match stored %v", got.MovedAt, after.MovedAt)
	}

	// Moving again must advance it once more.
	after.Status = types.StatusDone
	if err := s.UpdateTask(after); err != nil {
		t.Fatalf("second update task: %v", err)
	}
	second, err := s.GetTask("proj-1", "task-1")
	if err != nil {
		t.Fatalf("get task after second move: %v", err)
	}
	if second.MovedAt.Before(after.MovedAt) {
		t.Fatalf("expected moved_at to stay monotonic across moves, got %v then %v", after.MovedAt, second.MovedAt)
	}
}

// --- execution_state ownership guard ---

// newTaskInState creates a task row already sitting in the given execution state.
func newTaskInState(t *testing.T, s *Store, id, projectID string, status types.Status, execState types.ExecutionState) {
	t.Helper()
	task := newTask(id, projectID)
	task.Status = status
	task.ExecutionState = execState
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
}

// TestUpdateTaskGuardBlocksInFlightPromotion: a full-row write must not put a
// stopped task back to "running", which is how a task ended up showing a run
// with no executor behind it.
func TestUpdateTaskGuardBlocksInFlightPromotion(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	if err := s.CreateProject(newProject("proj-guard", "guard", "/tmp/guard")); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Typed sets: the loop variables feed ExecutionState directly, so the values they
	// range over cannot drift into a state the store does not know.
	for _, stored := range []types.ExecutionState{"idle", "paused", "completed", "failed"} {
		for _, wanted := range []types.ExecutionState{"running", "pulling_image"} {
			id := "task-" + string(stored) + "-" + string(wanted)
			newTaskInState(t, s, id, "proj-guard", "in_progress", stored)

			got, err := s.GetTaskByID(id)
			if err != nil {
				t.Fatalf("get task: %v", err)
			}
			got.ExecutionState = wanted
			got.Title = "written by the stale writer"
			got.Output = "output written by the stale writer"
			if err := s.UpdateTask(got); err != nil {
				t.Fatalf("update task (%s -> %s): %v", stored, wanted, err)
			}

			after, err := s.GetTaskByID(id)
			if err != nil {
				t.Fatalf("get task after update: %v", err)
			}
			if after.ExecutionState != stored {
				t.Errorf("%s -> %s: execution_state = %s, want stored %s", stored, wanted, after.ExecutionState, stored)
			}
			// The struct must mirror what was stored, not what was asked for.
			if got.ExecutionState != stored {
				t.Errorf("%s -> %s: in-memory execution_state = %s, want stored %s", stored, wanted, got.ExecutionState, stored)
			}
			// Every other column is still written normally.
			if after.Title != "written by the stale writer" || after.Output != "output written by the stale writer" {
				t.Errorf("%s -> %s: other columns must still be written, got title=%q output=%q", stored, wanted, after.Title, after.Output)
			}
		}
	}
}

// TestUpdateTaskGuardAllowsOtherExecutionStateWrites: the guard must not get in
// the way of a live run keeping the row in-flight, or of settling it.
func TestUpdateTaskGuardAllowsOtherExecutionStateWrites(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	if err := s.CreateProject(newProject("proj-guard2", "guard2", "/tmp/guard2")); err != nil {
		t.Fatalf("create project: %v", err)
	}

	newTaskInState(t, s, "task-live", "proj-guard2", "in_progress", "running")

	got, err := s.GetTaskByID("task-live")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	// Writing "running" again while running keeps it running.
	if err := s.UpdateTask(got); err != nil {
		t.Fatalf("re-write running: %v", err)
	}
	if after, _ := s.GetTaskByID("task-live"); after.ExecutionState != types.ExecStateRunning {
		t.Fatalf("running -> running: got %s", after.ExecutionState)
	}

	// Settling to any resting state works.
	for _, want := range []types.ExecutionState{"idle", "paused", "completed", "failed"} {
		got.ExecutionState = want
		if err := s.UpdateTask(got); err != nil {
			t.Fatalf("settle to %s: %v", want, err)
		}
		if after, _ := s.GetTaskByID("task-live"); after.ExecutionState != want {
			t.Fatalf("running -> %s: got %s", want, after.ExecutionState)
		}
		got.ExecutionState = types.ExecStateRunning
		if err := s.UpdateTaskClaimingExecution(got); err != nil {
			t.Fatalf("re-claim running: %v", err)
		}
	}
}

// TestUpdateTaskClaimingExecutionPromotes: whoever owns a run can start or keep
// an in-flight state, including right after a stop.
func TestUpdateTaskClaimingExecutionPromotes(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	if err := s.CreateProject(newProject("proj-guard3", "guard3", "/tmp/guard3")); err != nil {
		t.Fatalf("create project: %v", err)
	}

	newTaskInState(t, s, "task-claim", "proj-guard3", "in_progress", "paused")

	got, err := s.GetTaskByID("task-claim")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	got.Status = types.StatusInProgress
	got.ExecutionState = types.ExecStateRunning
	if err := s.UpdateTaskClaimingExecution(got); err != nil {
		t.Fatalf("claim: %v", err)
	}
	after, err := s.GetTaskByID("task-claim")
	if err != nil {
		t.Fatalf("get task after claim: %v", err)
	}
	if after.ExecutionState != types.ExecStateRunning {
		t.Fatalf("claiming write must promote to running, got %s", after.ExecutionState)
	}
	if got.ExecutionState != types.ExecStateRunning {
		t.Fatalf("in-memory copy must mirror the promoted state, got %s", got.ExecutionState)
	}
}

// TestSetTaskAssignedPortsIsTargeted: ports are written while a run is live and
// cleared when it ends, so the write must leave status and execution_state alone.
func TestSetTaskAssignedPortsIsTargeted(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	if err := s.CreateProject(newProject("proj-ports", "ports", "/tmp/ports")); err != nil {
		t.Fatalf("create project: %v", err)
	}

	past := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	task := newTask("task-ports", "proj-ports")
	task.Status = types.StatusInProgress
	task.ExecutionState = types.ExecStateRunning
	task.CreatedAt = past
	task.UpdatedAt = past
	task.MovedAt = past
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	ports := []types.AssignedPort{{HostPort: 32100, ContainerPort: 8080}}
	if err := s.SetTaskAssignedPorts("task-ports", "proj-ports", ports); err != nil {
		t.Fatalf("set ports: %v", err)
	}
	after, err := s.GetTaskByID("task-ports")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if len(after.AssignedPorts) != 1 || after.AssignedPorts[0].HostPort != 32100 || after.AssignedPorts[0].ContainerPort != 8080 {
		t.Fatalf("assigned_ports not stored: %+v", after.AssignedPorts)
	}
	if after.ExecutionState != types.ExecStateRunning || after.Status != types.StatusInProgress {
		t.Fatalf("ports write changed run columns: %s/%s", after.Status, after.ExecutionState)
	}
	if !after.MovedAt.Equal(past) {
		t.Fatalf("ports write must not move the card: moved_at %v, want %v", after.MovedAt, past)
	}
	if !after.UpdatedAt.After(past) {
		t.Fatalf("updated_at should advance, got %v", after.UpdatedAt)
	}

	// Clearing stores the same empty encoding UpdateTask writes.
	if err := s.SetTaskAssignedPorts("task-ports", "proj-ports", nil); err != nil {
		t.Fatalf("clear ports: %v", err)
	}
	cleared, err := s.GetTaskByID("task-ports")
	if err != nil {
		t.Fatalf("get task after clear: %v", err)
	}
	if len(cleared.AssignedPorts) != 0 {
		t.Fatalf("ports not cleared: %+v", cleared.AssignedPorts)
	}
	if cleared.ExecutionState != types.ExecStateRunning {
		t.Fatalf("clearing ports changed execution_state: %s", cleared.ExecutionState)
	}

	// Scoped to the owning project.
	if err := s.SetTaskAssignedPorts("task-ports", "other-project", ports); err != nil {
		t.Fatalf("set ports (wrong project): %v", err)
	}
	if after, _ := s.GetTaskByID("task-ports"); len(after.AssignedPorts) != 0 {
		t.Fatalf("wrong-project write must not apply, got %+v", after.AssignedPorts)
	}
}

func TestSandboxMemoryPeakSurvivesFullRowUpdate(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	if err := s.CreateProject(newProject("proj-peak", "peak", "/tmp/peak")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-peak", "proj-peak")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	stale, err := s.GetTaskByID("task-peak")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if stale.SandboxMemoryPeakMB != 0 {
		t.Fatalf("a new task must start with no recorded peak, got %d", stale.SandboxMemoryPeakMB)
	}

	if err := s.RecordTaskSandboxMemory("task-peak", 742, 2); err != nil {
		t.Fatalf("record peak: %v", err)
	}

	// Read before the peak was recorded, written back after it.
	stale.Title = "renamed after the run"
	stale.Output = "written by a stale copy"
	if err := s.UpdateTask(stale); err != nil {
		t.Fatalf("full-row update from a stale copy: %v", err)
	}

	got, err := s.GetTaskByID("task-peak")
	if err != nil {
		t.Fatalf("get task after update: %v", err)
	}
	if got.SandboxMemoryPeakMB != 742 {
		t.Errorf("peak = %d after a full-row UpdateTask, want 742 — UpdateTask must not own this column", got.SandboxMemoryPeakMB)
	}
	if got.SandboxOOMKills != 2 {
		t.Errorf("OOM kills = %d after a full-row UpdateTask, want 2 — UpdateTask must not own this column either", got.SandboxOOMKills)
	}
	// The caller broadcasts the struct it just wrote, so it must come back holding the peak the
	// sink recorded in the meantime.
	if stale.SandboxMemoryPeakMB != 742 {
		t.Errorf("the written-back copy carries peak %d, want 742", stale.SandboxMemoryPeakMB)
	}
	if stale.SandboxOOMKills != 2 {
		t.Errorf("the written-back copy carries %d OOM kills, want 2 — a broadcast of it would clear the alert", stale.SandboxOOMKills)
	}
	if got.Title != "renamed after the run" {
		t.Errorf("the peak column must not block the rest of UpdateTask, title = %q", got.Title)
	}
}

func TestSandboxMemoryPeakPersistsAcrossReopen(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "peak.db")
	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := s.CreateProject(newProject("proj-reopen", "reopen", "/tmp/reopen")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-reopen", "proj-reopen")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.RecordTaskSandboxMemory("task-reopen", 900, 1); err != nil {
		t.Fatalf("record peak: %v", err)
	}
	s.Close()

	s2, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	got, err := s2.GetTask("proj-reopen", "task-reopen")
	if err != nil {
		t.Fatalf("get task after reopen: %v", err)
	}
	if got.SandboxMemoryPeakMB != 900 {
		t.Errorf("peak after reopen = %d, want 900", got.SandboxMemoryPeakMB)
	}
	if got.SandboxOOMKills != 1 {
		t.Errorf("OOM kills after reopen = %d, want 1", got.SandboxOOMKills)
	}
}

// The teardown path also carries the project-level container's "proj:…" key and any task
// deleted mid-run. Neither is a teardown failure.
func TestRecordTaskSandboxMemoryIgnoresAnUnknownTask(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	if err := s.RecordTaskSandboxMemory("proj:gone", 512, 4); err != nil {
		t.Errorf("recording a peak for an unknown task: %v", err)
	}
}

// One dismissal must serve every device: it lands, it replaces, and a stale row write keeps it.
func TestDismissTaskMemoryAlertLandsOnTheRow(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	if err := s.CreateProject(newProject("proj-ack", "ack", "/tmp/ack")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-ack", "proj-ack")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	untouched, err := s.GetTaskByID("task-ack")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if untouched.MemoryAlertAck != nil {
		t.Fatalf("a task nobody dismissed must read as no acknowledgement, got %+v", untouched.MemoryAlertAck)
	}
	encoded, err := json.Marshal(untouched)
	if err != nil {
		t.Fatalf("marshal task: %v", err)
	}
	if !strings.Contains(string(encoded), `"memory_alert_ack":null`) {
		t.Errorf("an undismissed alert serialises as null so the client sees one shape: %s", encoded)
	}

	before := untouched.UpdatedAt
	if err := s.DismissTaskMemoryAlert("task-ack", 1024, 2, 1024); err != nil {
		t.Fatalf("dismiss alert: %v", err)
	}
	got, err := s.GetTaskByID("task-ack")
	if err != nil {
		t.Fatalf("get task after dismiss: %v", err)
	}
	if got.MemoryAlertAck == nil || *got.MemoryAlertAck != (types.MemoryAlertAck{PeakMB: 1024, OOMKills: 2, LimitMB: 1024}) {
		t.Fatalf("ack = %+v, want {1024 2 1024}", got.MemoryAlertAck)
	}
	// The board sorts on updated_at; dismissing is no reason to move a card.
	if !got.UpdatedAt.Equal(before) {
		t.Errorf("updated_at moved from %s to %s", before, got.UpdatedAt)
	}

	// A worse run re-armed the alert, so this replaces the numbers just stored.
	if err := s.DismissTaskMemoryAlert("task-ack", 2048, 5, 1024); err != nil {
		t.Fatalf("dismiss the re-armed alert: %v", err)
	}
	again, err := s.GetTaskByID("task-ack")
	if err != nil {
		t.Fatalf("get task after second dismiss: %v", err)
	}
	if again.MemoryAlertAck == nil || *again.MemoryAlertAck != (types.MemoryAlertAck{PeakMB: 2048, OOMKills: 5, LimitMB: 1024}) {
		t.Fatalf("ack = %+v, want the second dismissal to replace the first", again.MemoryAlertAck)
	}

	// A stale copy that never saw the click must not erase it.
	stale := untouched
	stale.Title = "renamed after the dismissal"
	if err := s.UpdateTask(stale); err != nil {
		t.Fatalf("full-row update from a stale copy: %v", err)
	}
	if stale.MemoryAlertAck == nil || stale.MemoryAlertAck.OOMKills != 5 {
		t.Errorf("the written-back copy carries ack %+v, want the stored one — its broadcast would clear an unseen alert", stale.MemoryAlertAck)
	}
	kept, err := s.GetTaskByID("task-ack")
	if err != nil {
		t.Fatalf("get task after the stale update: %v", err)
	}
	if kept.MemoryAlertAck == nil || *kept.MemoryAlertAck != (types.MemoryAlertAck{PeakMB: 2048, OOMKills: 5, LimitMB: 1024}) {
		t.Fatalf("ack after a stale full-row update = %+v", kept.MemoryAlertAck)
	}
}

// The flag says "someone dismissed", not the numbers: zeros can be a real dismissal.
func TestDismissTaskMemoryAlertRecordsADismissalOfNothingButKills(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	if err := s.CreateProject(newProject("proj-ack-zero", "ack zero", "/tmp/ack-zero")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-ack-zero", "proj-ack-zero")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.DismissTaskMemoryAlert("task-ack-zero", 0, 2, 0); err != nil {
		t.Fatalf("dismiss alert: %v", err)
	}
	got, err := s.GetTaskByID("task-ack-zero")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.MemoryAlertAck == nil {
		t.Fatal("a dismissal at {peak 0, 2 kills, no limit} is a real one and must not read as never dismissed")
	}
	if *got.MemoryAlertAck != (types.MemoryAlertAck{PeakMB: 0, OOMKills: 2, LimitMB: 0}) {
		t.Errorf("ack = %+v, want {0 2 0}", got.MemoryAlertAck)
	}
}

func TestDismissTaskMemoryAlertIgnoresAnUnknownTask(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	if err := s.DismissTaskMemoryAlert("proj:gone", 512, 1, 1024); err != nil {
		t.Errorf("dismissing an unknown task's alert: %v", err)
	}
}

func TestMemoryAlertAckPersistsAcrossReopen(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "ack.db")
	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := s.CreateProject(newProject("proj-ack-reopen", "ack reopen", "/tmp/ack-reopen")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(newTask("task-ack-reopen", "proj-ack-reopen")); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.DismissTaskMemoryAlert("task-ack-reopen", 700, 0, 700); err != nil {
		t.Fatalf("dismiss alert: %v", err)
	}
	s.Close()

	s2, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	got, err := s2.GetTask("proj-ack-reopen", "task-ack-reopen")
	if err != nil {
		t.Fatalf("get task after reopen: %v", err)
	}
	if got.MemoryAlertAck == nil || *got.MemoryAlertAck != (types.MemoryAlertAck{PeakMB: 700, OOMKills: 0, LimitMB: 700}) {
		t.Fatalf("ack after reopen = %+v, want {700 0 700}", got.MemoryAlertAck)
	}
}

func TestSetMemoryPressurePausedOutputGuard(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	if err := s.CreateProject(newProject("proj-out", "out", "/tmp/out")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	newTaskInState(t, s, "sup-paused", "proj-out", types.StatusInProgress, types.ExecStatePaused)
	newTaskInState(t, s, "user-paused", "proj-out", types.StatusInProgress, types.ExecStatePaused)
	newTaskInState(t, s, "still-running", "proj-out", types.StatusInProgress, types.ExecStateRunning)
	for _, id := range []string{"sup-paused", "still-running"} {
		if err := s.SetMemorySupervisorPaused(id, true); err != nil {
			t.Fatalf("set flag on %s: %v", id, err)
		}
	}

	for _, id := range []string{"user-paused", "still-running"} {
		changed, err := s.SetMemoryPressurePausedOutput(id, "memory pressure")
		if err != nil {
			t.Fatalf("write %s: %v", id, err)
		}
		if changed {
			t.Errorf("%s: must refuse a row that is not paused-by-supervisor", id)
		}
	}

	changed, err := s.SetMemoryPressurePausedOutput("sup-paused", "memory pressure")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("a paused row carrying the flag must accept the text")
	}
	got, err := s.GetTaskByID("sup-paused")
	if err != nil {
		t.Fatal(err)
	}
	if got.Output != "memory pressure" {
		t.Errorf("output = %q, want the reason the supervisor gave", got.Output)
	}

	if err := s.SetMemorySupervisorPaused("sup-paused", false); err != nil {
		t.Fatal(err)
	}
	changed, err = s.SetMemoryPressurePausedOutput("sup-paused", "still mine")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("a row whose flag a start cleared is no longer the supervisor's to annotate")
	}
}

// TestInFlightStatesSQLIsTheGoSet: the pool GetRunningSandboxedTasks hands the supervisor to pause must be
// what types.ExecutionState.InFlight says. Both sides are derived here.
func TestInFlightStatesSQLIsTheGoSet(t *testing.T) {
	t.Parallel()
	for _, state := range []types.ExecutionState{
		types.ExecStateIdle, types.ExecStateRunning, types.ExecStatePullingImage,
		types.ExecStatePaused, types.ExecStateCompleted, types.ExecStateFailed,
	} {
		inSQL := strings.Contains(inFlightStates, "'"+string(state)+"'")
		if inSQL != state.InFlight() {
			t.Errorf("%q: inFlightStates says %t, types.ExecutionState.InFlight says %t", state, inSQL, state.InFlight())
		}
	}
}
