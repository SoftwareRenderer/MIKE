package store

import (
	"testing"
	"time"

	"kanban/backend/types"
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

func TestGetTaskNotFound(t *testing.T) {
	s := testStore(t)
	_, err := s.GetTask("proj-1", "task-1")
	if err == nil {
		t.Fatal("expected error for missing task")
	}
}

func TestGetTaskByTaskID(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	got, err := s.GetTaskByTaskID("task-1")
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

	for i := range 3 {
		task := newTask("task-1", "proj-1")
		task.ID = "task-1"
		task.ColumnOrder = 0
		if i == 1 {
			task.ID = "task-2"
			task.ColumnOrder = 2
		}
		if i == 2 {
			task.ID = "task-3"
			task.ColumnOrder = 1
		}
		if err := s.CreateTask(task); err != nil {
			t.Fatalf("create task %d: %v", i, err)
		}
	}

	tasks, err := s.GetTasksByProject("proj-1")
	if err != nil {
		t.Fatalf("get tasks: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}
	// Should be ordered by column_order
	if tasks[0].ID != "task-1" || tasks[1].ID != "task-3" || tasks[2].ID != "task-2" {
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

func TestUpdateTaskStatusAndState(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := newTask("task-1", "proj-1")
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	if err := s.UpdateTaskStatusAndState("task-1", "proj-1", "in_progress", "running"); err != nil {
		t.Fatalf("update status and state: %v", err)
	}

	got, err := s.GetTask("proj-1", "task-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Status != "in_progress" || got.ExecutionState != "running" {
		t.Fatalf("unexpected status/state: %s/%s", got.Status, got.ExecutionState)
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

func TestReorderTasks(t *testing.T) {
	s := testStore(t)
	proj := newProject("proj-1", "test", "/tmp/test")
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	for i, id := range []string{"task-1", "task-2", "task-3"} {
		task := newTask(id, "proj-1")
		task.ColumnOrder = i
		if err := s.CreateTask(task); err != nil {
			t.Fatalf("create task: %v", err)
		}
	}

	reorders := []types.ReorderRequest{
		{ID: "task-1", ColumnOrder: 2},
		{ID: "task-2", ColumnOrder: 0},
		{ID: "task-3", ColumnOrder: 1},
	}
	if err := s.ReorderTasks("proj-1", reorders); err != nil {
		t.Fatalf("reorder tasks: %v", err)
	}

	tasks, err := s.GetTasksByProject("proj-1")
	if err != nil {
		t.Fatalf("get tasks: %v", err)
	}
	if tasks[0].ID != "task-2" || tasks[1].ID != "task-3" || tasks[2].ID != "task-1" {
		t.Fatalf("unexpected order after reorder: %v, %v, %v", tasks[0].ID, tasks[1].ID, tasks[2].ID)
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

	task.PlanMode = true
	task.TitleGenerating = true
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	got, err := s.GetTask("proj-1", "task-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if !got.AutoCommit || !got.PlanMode || !got.TitleGenerating {
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
