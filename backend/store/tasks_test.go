package store

import (
	"testing"
	"time"
)

func TestCreateAndGetTask(t *testing.T) {
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
	if got.ID != "task-1" || got.ProjectID != "proj-1" || got.Status != "backlog" {
		t.Fatalf("unexpected task: %+v", got)
	}
}

func TestTaskAutostartedPersistence(t *testing.T) {
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
	s := testStore(t)
	_, err := s.GetTask("proj-1", "task-1")
	if err == nil {
		t.Fatal("expected error for missing task")
	}
}

func TestGetTaskByID(t *testing.T) {
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
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Update status
	task.Status = "in_progress"
	task.ExecutionState = "running"
	if err := s.UpdateTask(task); err != nil {
		t.Fatalf("update task: %v", err)
	}

	got, err := s.GetTask("proj-1", "task-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Status != "in_progress" || got.ExecutionState != "running" {
		t.Fatalf("unexpected status: %s, %s", got.Status, got.ExecutionState)
	}
}

func TestUpdateTaskExecutionState(t *testing.T) {
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
	if got.ExecutionState != "running" {
		t.Fatalf("unexpected status state: %s", got.ExecutionState)
	}
	// Status should be unchanged
	if got.Status != "backlog" {
		t.Fatalf("status should be unchanged: %s", got.Status)
	}
}

func TestDeleteTask(t *testing.T) {
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
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Set a branch
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
	got.ExecutionState = "running"
	if err := s.UpdateTask(got); err != nil {
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
	got.Status = "review"
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
	after.Status = "done"
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
