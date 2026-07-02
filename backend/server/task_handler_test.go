package server

import (
	"net/http"
	"strings"
	"testing"

	"kanban/backend/types"
)

func TestTaskCreate(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	req := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]any{
		"prompt": "Add login feature",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)
	taskID := result["id"].(string)
	if taskID == "" {
		t.Fatal("task ID should not be empty")
	}
	if result["status"] != "backlog" {
		t.Fatalf("expected status=backlog, got %s", result["status"])
	}
	if result["project_id"] != projID {
		t.Fatalf("wrong project_id: %s", result["project_id"])
	}
	// Title should be auto-generated from first line of prompt
	if result["title"] != "Add login feature" {
		t.Fatalf("unexpected auto-generated title: %s", result["title"])
	}
}

func TestTaskCreateWithTitle(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	req := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]any{
		"prompt": "Add login feature",
		"title":  "Custom Title",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)
	if result["title"] != "Custom Title" {
		t.Fatalf("expected custom title, got %s", result["title"])
	}
}

func TestTaskCreateInvalidJSON(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	req := testRequest("POST", "/api/projects/"+projID+"/tasks", "not json")
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestTaskList(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	// Create 2 tasks
	for _, prompt := range []string{"Task A", "Task B"} {
		createTestTask(t, s, projID, prompt)
	}

	req := testRequest("GET", "/api/projects/"+projID+"/tasks", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	tasks := mustJSON[[]map[string]any](t, rr.Body)
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
}

func TestTaskGetByID(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Get me")

	req := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID, nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)
	if result["id"] != taskID {
		t.Fatalf("wrong task ID: %s", result["id"])
	}
}

func TestTaskGetByIDNotFound(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	req := testRequest("GET", "/api/projects/"+projID+"/tasks/nonexistent", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestTaskUpdate(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Update me")

	// Update the task title
	req := testRequest("PUT", "/api/projects/"+projID+"/tasks/"+taskID, map[string]any{
		"title": "Updated Title",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)
	if result["title"] != "Updated Title" {
		t.Fatalf("expected updated title, got %s", result["title"])
	}
}

func TestTaskDelete(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Delete me")

	// Delete the task
	req := testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+taskID, nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify it's gone
	req = testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID, nil)
	rr = testResponseAuth(t, s, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", rr.Code)
	}
}

func TestTaskMove(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Move me")

	// Move to in_progress
	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/move", map[string]any{
		"status": "in_progress",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)
	if result["status"] != "in_progress" {
		t.Fatalf("expected status=in_progress, got %s", result["status"])
	}
}

func TestTaskReorder(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	// Create 3 tasks
	taskIDs := make([]string, 3)
	for i := range 3 {
		taskIDs[i] = createTestTask(t, s, projID, "Task "+string(rune('A'+i)))
	}

	// Reorder: set explicit column_order values
	req := testRequest("PUT", "/api/projects/"+projID+"/tasks/reorder", []map[string]any{
		{"id": taskIDs[0], "column_order": 10},
		{"id": taskIDs[1], "column_order": 20},
		{"id": taskIDs[2], "column_order": 0},
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify new order: taskIDs[2] (order=0) first, taskIDs[0] (order=10) last
	req = testRequest("GET", "/api/projects/"+projID+"/tasks", nil)
	rr = testResponseAuth(t, s, req)
	tasks := mustJSON[[]map[string]any](t, rr.Body)
	if tasks[0]["id"] != taskIDs[2] || tasks[1]["id"] != taskIDs[0] || tasks[2]["id"] != taskIDs[1] {
		t.Fatalf("reorder failed: expected [%s, %s, %s], got [%s, %s, %s]",
			taskIDs[2], taskIDs[0], taskIDs[1],
			tasks[0]["id"], tasks[1]["id"], tasks[2]["id"])
	}
}

func TestTaskParentValidation(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	// Create a parent in backlog
	parentID := createTestTask(t, s, projID, "Parent task")

	// Create a child with valid parent
	req := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]any{
		"prompt":         "Child task",
		"parent_task_id": parentID,
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	// Move parent to in_progress (no longer valid for subtasks)
	req = testRequest("POST", "/api/projects/"+projID+"/tasks/"+parentID+"/move", map[string]any{
		"status": "in_progress",
	})
	testResponseAuth(t, s, req)

	// Try to create another child — should fail
	req = testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]any{
		"prompt":         "Another child",
		"parent_task_id": parentID,
	})
	rr = testResponseAuth(t, s, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-backlog parent, got %d", rr.Code)
	}
}

func TestTaskParentNotFound(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	req := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]any{
		"prompt":         "Child with missing parent",
		"parent_task_id": "nonexistent-parent",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

// Helper: create a task with LLM config so the executor can start
func createTestTaskWithLLM(t *testing.T, s *Server, projectID string, prompt string) string {
	t.Helper()
	task := map[string]any{
		"prompt":           prompt,
		"llama_server_url": "http://localhost:9999/v1",
		"model_name":       "mock-model",
	}
	req := testRequest("POST", "/api/projects/"+projectID+"/tasks", task)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", rr.Code, rr.Body.String())
	}
	result := mustJSON[map[string]any](t, rr.Body)
	return result["id"].(string)
}

// Helper: create a subtask via API
func createTestSubtask(t *testing.T, s *Server, projectID string, parentTaskID string, prompt string) string {
	t.Helper()
	task := map[string]any{
		"prompt":         prompt,
		"parent_task_id": parentTaskID,
	}
	req := testRequest("POST", "/api/projects/"+projectID+"/tasks", task)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create subtask: %d %s", rr.Code, rr.Body.String())
	}
	result := mustJSON[map[string]any](t, rr.Body)
	return result["id"].(string)
}

// Helper: get a task by ID and return its status
func getTaskStatus(t *testing.T, s *Server, projectID string, taskID string) string {
	t.Helper()
	req := testRequest("GET", "/api/projects/"+projectID+"/tasks/"+taskID, nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get task: %d %s", rr.Code, rr.Body.String())
	}
	result := mustJSON[map[string]any](t, rr.Body)
	return result["status"].(string)
}

// Helper: get a task by ID and return its execution_state
func getTaskExecutionState(t *testing.T, s *Server, projectID string, taskID string) string {
	t.Helper()
	req := testRequest("GET", "/api/projects/"+projectID+"/tasks/"+taskID, nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get task: %d %s", rr.Code, rr.Body.String())
	}
	result := mustJSON[map[string]any](t, rr.Body)
	if ss, ok := result["execution_state"].(string); ok {
		return ss
	}
	return ""
}

// Helper: move a task to a status via API
func moveTask(t *testing.T, s *Server, projectID string, taskID string, status string) {
	t.Helper()
	req := testRequest("POST", "/api/projects/"+projectID+"/tasks/"+taskID+"/move", map[string]any{
		"status": status,
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("move task to %s: %d %s", status, rr.Code, rr.Body.String())
	}
}

// Helper: update a task directly via PUT
func updateTask(t *testing.T, s *Server, projectID string, taskID string, updates map[string]any) {
	t.Helper()
	req := testRequest("PUT", "/api/projects/"+projectID+"/tasks/"+taskID, updates)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("update task: %d %s", rr.Code, rr.Body.String())
	}
}

// Test: subtask move from review to done
func TestSubtaskMoveReviewToDone(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "parent-task")
	subID := createTestSubtask(t, s, projID, parentID, "subtask")

	// Move subtask to review
	moveTask(t, s, projID, subID, "review")
	status := getTaskStatus(t, s, projID, subID)
	if status != "review" {
		t.Fatalf("expected review, got %s", status)
	}

	// Move subtask from review to done
	moveTask(t, s, projID, subID, "done")
	status = getTaskStatus(t, s, projID, subID)
	if status != "done" {
		t.Fatalf("expected done, got %s", status)
	}
}

// Test: subtask move from review to backlog
func TestSubtaskMoveReviewToBacklog(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "parent-task")
	subID := createTestSubtask(t, s, projID, parentID, "subtask")

	// Move subtask to review
	moveTask(t, s, projID, subID, "review")

	// Move subtask from review back to backlog (parent is in backlog, allowed)
	moveTask(t, s, projID, subID, "backlog")
	status := getTaskStatus(t, s, projID, subID)
	if status != "backlog" {
		t.Fatalf("expected backlog, got %s", status)
	}
}

// Test: block subtask from moving to backlog when parent is in_progress
func TestSubtaskBlockBacklogWhenParentInProgress(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "parent-task")
	subID := createTestSubtask(t, s, projID, parentID, "subtask")

	// Move subtask to review
	moveTask(t, s, projID, subID, "review")

	// Move parent to in_progress — subtask follows (new behavior: all descendants move with parent)
	moveTask(t, s, projID, parentID, "in_progress")

	// Subtask should have been moved to in_progress with its parent
	if getTaskStatus(t, s, projID, subID) != "in_progress" {
		t.Fatalf("subtask should follow parent to in_progress, got %s", getTaskStatus(t, s, projID, subID))
	}

	// Try to move subtask to backlog — should be blocked (ancestor is in_progress)
	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+subID+"/move", map[string]any{
		"status": "backlog",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}

	// Subtask should still be in in_progress (blocked from moving to backlog)
	status := getTaskStatus(t, s, projID, subID)
	if status != "in_progress" {
		t.Fatalf("expected in_progress (blocked), got %s", status)
	}
}

// Test: allow subtask to move to review/done when parent is in_progress
func TestSubtaskAllowReviewDoneWhenParentInProgress(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "parent-task")
	subID := createTestSubtask(t, s, projID, parentID, "subtask")

	// Move parent to in_progress
	moveTask(t, s, projID, parentID, "in_progress")

	// Move subtask to review — should be allowed
	moveTask(t, s, projID, subID, "review")
	status := getTaskStatus(t, s, projID, subID)
	if status != "review" {
		t.Fatalf("expected review, got %s", status)
	}

	// Move subtask to done — should be allowed
	moveTask(t, s, projID, subID, "done")
	status = getTaskStatus(t, s, projID, subID)
	if status != "done" {
		t.Fatalf("expected done, got %s", status)
	}
}

// Test: moving a subtask to in_progress does not move siblings in review/done
func TestSubtaskSiblingIsolation(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "parent-sibling")
	subA := createTestSubtask(t, s, projID, parentID, "sub-a")
	subB := createTestSubtask(t, s, projID, parentID, "sub-b")

	// Set all three to in_progress/idle via direct PUT (bypasses move endpoint's executor)
	updateTask(t, s, projID, parentID, map[string]any{"status": "in_progress", "execution_state": "idle"})
	updateTask(t, s, projID, subA, map[string]any{"status": "in_progress", "execution_state": "idle"})
	updateTask(t, s, projID, subB, map[string]any{"status": "in_progress", "execution_state": "idle"})

	// Move subA to review
	moveTask(t, s, projID, subA, "review")
	if getTaskStatus(t, s, projID, subA) != "review" {
		t.Fatal("subA should be review")
	}

	// Move subB to done
	moveTask(t, s, projID, subB, "done")
	if getTaskStatus(t, s, projID, subB) != "done" {
		t.Fatal("subB should be done")
	}

	// Move subA back to in_progress — subB should STAY in done
	moveTask(t, s, projID, subA, "in_progress")

	statusA := getTaskStatus(t, s, projID, subA)
	statusB := getTaskStatus(t, s, projID, subB)
	if statusA != "in_progress" {
		t.Fatalf("subA expected in_progress, got %s", statusA)
	}
	if statusB != "done" {
		t.Fatalf("subB expected done (sibling isolation), got %s", statusB)
	}
}

// Test: task start changes status to in_progress
func TestTaskStartChangesStatus(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTaskWithLLM(t, s, projID, "start-test-task")

	// Verify initial status
	status := getTaskStatus(t, s, projID, taskID)
	if status != "backlog" {
		t.Fatalf("expected backlog, got %s", status)
	}

	// Start the task via API
	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/start", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("start task: %d %s", rr.Code, rr.Body.String())
	}

	// Verify status changed to in_progress
	status = getTaskStatus(t, s, projID, taskID)
	if status != "in_progress" {
		t.Fatalf("expected in_progress, got %s", status)
	}
}

// Test: task delete removes the task (via API handler)
func TestTaskDeleteViaHandler(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "delete-me")

	// Verify task exists
	status := getTaskStatus(t, s, projID, taskID)
	if status != "backlog" {
		t.Fatalf("expected backlog, got %s", status)
	}

	// Delete the task (returns 204 NoContent)
	req := testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+taskID, nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete task: expected 204, got %d %s", rr.Code, rr.Body.String())
	}

	// Verify task is gone
	req = testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID, nil)
	rr = testResponseAuth(t, s, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", rr.Code)
	}
}

// Test: task delete cascades to subtasks
func TestTaskDeleteCascadesToSubtasks(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "parent-delete")
	subID := createTestSubtask(t, s, projID, parentID, "sub-delete")

	// Verify both exist
	if getTaskStatus(t, s, projID, parentID) != "backlog" {
		t.Fatal("parent should exist")
	}
	if getTaskStatus(t, s, projID, subID) != "backlog" {
		t.Fatal("subtask should exist")
	}

	// Delete the parent (returns 204 NoContent)
	req := testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+parentID, nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete parent: expected 204, got %d %s", rr.Code, rr.Body.String())
	}

	// Verify both are gone
	req = testRequest("GET", "/api/projects/"+projID+"/tasks/"+parentID, nil)
	rr = testResponseAuth(t, s, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for parent, got %d", rr.Code)
	}
	req = testRequest("GET", "/api/projects/"+projID+"/tasks/"+subID, nil)
	rr = testResponseAuth(t, s, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for subtask (cascade), got %d", rr.Code)
	}
}

// Test: deleting a parent with subtasks in different states cascades to all of them
func TestTaskDeleteCascadesToSubtasksInDifferentStates(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "parent-multi-state")

	// Create 3 subtasks and move them to different columns
	subBacklog := createTestSubtask(t, s, projID, parentID, "sub-backlog")
	subReview := createTestSubtask(t, s, projID, parentID, "sub-review")
	subDone := createTestSubtask(t, s, projID, parentID, "sub-done")

	// Move subtasks to different states
	moveTask(t, s, projID, subReview, "review")
	moveTask(t, s, projID, subDone, "done")

	// Verify states
	if getTaskStatus(t, s, projID, subBacklog) != "backlog" {
		t.Fatal("subBacklog should be backlog")
	}
	if getTaskStatus(t, s, projID, subReview) != "review" {
		t.Fatal("subReview should be review")
	}
	if getTaskStatus(t, s, projID, subDone) != "done" {
		t.Fatal("subDone should be done")
	}

	// Delete the parent
	req := testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+parentID, nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete parent: expected 204, got %d %s", rr.Code, rr.Body.String())
	}

	// Verify ALL tasks are gone (parent + all subtasks regardless of state)
	for _, id := range []string{parentID, subBacklog, subReview, subDone} {
		req = testRequest("GET", "/api/projects/"+projID+"/tasks/"+id, nil)
		rr = testResponseAuth(t, s, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for %s, got %d", id, rr.Code)
		}
	}
}

// Test: clear done cascades to subtasks of done tasks
func TestClearDoneCascadesToSubtasks(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "parent-clear-done")
	subID := createTestSubtask(t, s, projID, parentID, "sub-clear-done")

	// Move subtask to review (different state from parent)
	moveTask(t, s, projID, subID, "review")

	// Move parent to done — subtask follows (new behavior: all descendants move with parent)
	moveTask(t, s, projID, parentID, "done")

	// Verify both are in done
	if getTaskStatus(t, s, projID, parentID) != "done" {
		t.Fatal("parent should be done")
	}
	if getTaskStatus(t, s, projID, subID) != "done" {
		t.Fatalf("subtask should follow parent to done, got %s", getTaskStatus(t, s, projID, subID))
	}

	// Clear done tasks — both should be deleted
	req := testRequest("DELETE", "/api/projects/"+projID+"/tasks/done", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("clear done: expected 200, got %d %s", rr.Code, rr.Body.String())
	}

	// Verify both parent and subtask are gone
	for _, id := range []string{parentID, subID} {
		req = testRequest("GET", "/api/projects/"+projID+"/tasks/"+id, nil)
		rr = testResponseAuth(t, s, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for %s after clear done, got %d", id, rr.Code)
		}
	}
}

// Test: subtask consolidation — move subtask to same column as parent
func TestSubtaskConsolidation(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "parent-consolidate")
	subID := createTestSubtask(t, s, projID, parentID, "sub-consolidate")

	// Move subtask to review
	moveTask(t, s, projID, subID, "review")

	// Move subtask back to backlog (same as parent)
	moveTask(t, s, projID, subID, "backlog")
	status := getTaskStatus(t, s, projID, subID)
	if status != "backlog" {
		t.Fatalf("expected backlog, got %s", status)
	}

	// Re-move to review, then to done
	moveTask(t, s, projID, subID, "review")
	moveTask(t, s, projID, subID, "done")
	status = getTaskStatus(t, s, projID, subID)
	if status != "done" {
		t.Fatalf("expected done, got %s", status)
	}
}

// Test: task move to done sets execution_state to completed
func TestTaskMoveDoneSetsCompleted(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "done-state")

	moveTask(t, s, projID, taskID, "done")
	statusState := getTaskExecutionState(t, s, projID, taskID)
	if statusState != "completed" {
		t.Fatalf("expected completed, got %s", statusState)
	}
}

// Test: task moved away from done resets execution_state
func TestTaskMoveAwayFromDoneResetsState(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "reset-state")

	// Move to done
	moveTask(t, s, projID, taskID, "done")
	if getTaskExecutionState(t, s, projID, taskID) != "completed" {
		t.Fatal("should be completed")
	}

	// Move away from done
	moveTask(t, s, projID, taskID, "review")
	statusState := getTaskExecutionState(t, s, projID, taskID)
	if statusState != "idle" {
		t.Fatalf("expected idle after moving away from done, got %s", statusState)
	}
}

// Test: project sandbox toggle
func TestProjectSandboxToggle(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	// Get project
	req := testRequest("GET", "/api/projects/"+projID, nil)
	rr := testResponseAuth(t, s, req)
	proj := mustJSON[map[string]any](t, rr.Body)

	initialSandbox := proj["sandbox_enabled"].(bool)

	// Toggle sandbox via PUT on project
	req = testRequest("PUT", "/api/projects/"+projID, map[string]any{
		"sandbox_enabled": !initialSandbox,
	})
	rr = testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("toggle sandbox: %d %s", rr.Code, rr.Body.String())
	}

	// Verify toggle
	req = testRequest("GET", "/api/projects/"+projID, nil)
	rr = testResponseAuth(t, s, req)
	proj = mustJSON[map[string]any](t, rr.Body)
	if proj["sandbox_enabled"].(bool) == initialSandbox {
		t.Fatal("sandbox should have toggled")
	}
}

// Test: clear done tasks removes all done tasks
func TestClearDoneTasks(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	// Create tasks and move some to done
	task1 := createTestTask(t, s, projID, "done-task-1")
	task2 := createTestTask(t, s, projID, "done-task-2")
	task3 := createTestTask(t, s, projID, "backlog-task")

	moveTask(t, s, projID, task1, "done")
	moveTask(t, s, projID, task2, "done")

	// Verify: 2 done, 1 backlog
	req := testRequest("GET", "/api/projects/"+projID+"/tasks", nil)
	rr := testResponseAuth(t, s, req)
	tasks := mustJSON[[]map[string]any](t, rr.Body)
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}

	// Clear done tasks (DELETE /api/projects/{id}/tasks/done)
	req = testRequest("DELETE", "/api/projects/"+projID+"/tasks/done", nil)
	rr = testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("clear done: %d %s", rr.Code, rr.Body.String())
	}

	// Verify: only backlog task remains
	req = testRequest("GET", "/api/projects/"+projID+"/tasks", nil)
	rr = testResponseAuth(t, s, req)
	tasks = mustJSON[[]map[string]any](t, rr.Body)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task after clear done, got %d", len(tasks))
	}
	if tasks[0]["id"] != task3 {
		t.Fatalf("expected backlog task to remain, got %s", tasks[0]["id"])
	}
}

// Test: create subtask with LLM config
func TestCreateSubtaskWithLLM(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "parent-llm")

	req := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]any{
		"prompt":           "subtask with llm",
		"parent_task_id":   parentID,
		"llama_server_url": "http://localhost:9999/v1",
		"model_name":       "mock-model",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create subtask with LLM: %d %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)
	if result["parent_task_id"] != parentID {
		t.Fatalf("expected parent_task_id=%s, got %s", parentID, result["parent_task_id"])
	}
	if result["llama_server_url"] != "http://localhost:9999/v1" {
		t.Fatalf("expected llama_server_url, got %s", result["llama_server_url"])
	}
}

// Test: board endpoint returns tasks grouped by status
func TestBoardEndpointSubtasks(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "board-parent")
	subID := createTestSubtask(t, s, projID, parentID, "board-sub")

	// Move subtask to review
	moveTask(t, s, projID, subID, "review")

	// Get board
	req := testRequest("GET", "/api/projects/"+projID+"/board", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get board: %d %s", rr.Code, rr.Body.String())
	}

	board := mustJSON[map[string]any](t, rr.Body)
	tasks := board["tasks"].([]any)
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks on board, got %d", len(tasks))
	}
}

// Test: commit-and-done endpoint moves task to done and stops sandbox
func TestCommitAndDone(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "commit-done")

	// Commit-and-done (no worktree exists, but handler should still move task to done)
	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/commit", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("commit-and-done: %d %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)
	if result["status"] != "done" {
		t.Fatalf("expected status=done, got %s", result["status"])
	}
	if result["execution_state"] != "completed" {
		t.Fatalf("expected execution_state=completed, got %s", result["execution_state"])
	}
}

// Test: agent provider blocked when network isolation is enabled
func TestAgentBlockedWithNetworkIsolation(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	// Set up an agent provider in config
	cfg, err := s.configStore.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.Providers = append(cfg.Providers, types.Provider{
		Name:         "test-agent",
		Kind:         "pi",
		URL:          "pi://agent",
		Concurrency:  1,
		DefaultModel: "agent-model",
	})
	if err := s.configStore.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	// Enable network isolation on the project
	if err := s.store.UpdateProjectSandboxNetworkIsolation(projID, true); err != nil {
		t.Fatalf("enable network isolation: %v", err)
	}

	// Create a task with the agent provider URL
	taskID := createTestTaskWithAgent(t, s, projID, "agent-blocked-task", "pi://agent")

	// Try to start the task — should be blocked
	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/start", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "agent requires network to be enabled") {
		t.Fatalf("expected error about agent requiring network, got: %s", body)
	}

	// Verify task is still in backlog (not started)
	status := getTaskStatus(t, s, projID, taskID)
	if status != "backlog" {
		t.Fatalf("expected task to remain in backlog, got %s", status)
	}
}

// Test: agent provider allowed when network isolation is disabled
func TestAgentAllowedWithoutNetworkIsolation(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	// Set up an agent provider in config
	cfg, err := s.configStore.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.Providers = append(cfg.Providers, types.Provider{
		Name:         "test-agent-2",
		Kind:         "pi",
		URL:          "pi://agent",
		Concurrency:  1,
		DefaultModel: "agent-model",
	})
	if err := s.configStore.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	// Network isolation is disabled by default

	// Create a task with the agent provider URL
	taskID := createTestTaskWithAgent(t, s, projID, "agent-allowed-task", "pi://agent")

	// Start the task — should succeed (no network isolation)
	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/start", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify task is in_progress
	status := getTaskStatus(t, s, projID, taskID)
	if status != "in_progress" {
		t.Fatalf("expected in_progress, got %s", status)
	}
}

// Test: LLM provider allowed even with network isolation
func TestLLMAllowedWithNetworkIsolation(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	// Set up an LLM provider in config
	cfg, err := s.configStore.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.Providers = append(cfg.Providers, types.Provider{
		Name:         "test-llm",
		Kind:         "llm",
		URL:          "http://localhost:9999/v1",
		Concurrency:  1,
		DefaultModel: "mock-model",
	})
	if err := s.configStore.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	// Enable network isolation
	if err := s.store.UpdateProjectSandboxNetworkIsolation(projID, true); err != nil {
		t.Fatalf("enable network isolation: %v", err)
	}

	// Create a task with the LLM provider URL
	taskID := createTestTaskWithLLM(t, s, projID, "llm-allowed-task")

	// Start the task — should succeed (LLM provider, not an agent)
	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/start", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify task is in_progress
	status := getTaskStatus(t, s, projID, taskID)
	if status != "in_progress" {
		t.Fatalf("expected in_progress, got %s", status)
	}
}

// Test: moving a parent task moves all subtasks recursively to the same column
func TestMoveParentMovesAllSubtasks(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "parent-cascade")
	subA := createTestSubtask(t, s, projID, parentID, "sub-a")
	subB := createTestSubtask(t, s, projID, parentID, "sub-b")

	// All start in backlog
	if getTaskStatus(t, s, projID, parentID) != "backlog" {
		t.Fatal("parent should be backlog")
	}
	if getTaskStatus(t, s, projID, subA) != "backlog" {
		t.Fatal("subA should be backlog")
	}
	if getTaskStatus(t, s, projID, subB) != "backlog" {
		t.Fatal("subB should be backlog")
	}

	// Move parent to review — all subtasks should follow
	moveTask(t, s, projID, parentID, "review")

	if getTaskStatus(t, s, projID, parentID) != "review" {
		t.Fatal("parent should be review")
	}
	if getTaskStatus(t, s, projID, subA) != "review" {
		t.Fatalf("subA should follow parent to review, got %s", getTaskStatus(t, s, projID, subA))
	}
	if getTaskStatus(t, s, projID, subB) != "review" {
		t.Fatalf("subB should follow parent to review, got %s", getTaskStatus(t, s, projID, subB))
	}
}

// Test: moving a parent task moves nested subtasks (grandchildren) recursively
func TestMoveParentMovesNestedSubtasks(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	grandparentID := createTestTask(t, s, projID, "grandparent")
	parentID := createTestSubtask(t, s, projID, grandparentID, "parent")
	childID := createTestSubtask(t, s, projID, parentID, "child")

	// Move grandparent to done — all descendants should follow
	moveTask(t, s, projID, grandparentID, "done")

	if getTaskStatus(t, s, projID, grandparentID) != "done" {
		t.Fatal("grandparent should be done")
	}
	if getTaskStatus(t, s, projID, parentID) != "done" {
		t.Fatalf("parent should follow to done, got %s", getTaskStatus(t, s, projID, parentID))
	}
	if getTaskStatus(t, s, projID, childID) != "done" {
		t.Fatalf("child should follow to done, got %s", getTaskStatus(t, s, projID, childID))
	}
	// Check execution_state is "completed" for done tasks
	if getTaskExecutionState(t, s, projID, grandparentID) != "completed" {
		t.Fatalf("grandparent execution_state should be completed, got %s", getTaskExecutionState(t, s, projID, grandparentID))
	}
	if getTaskExecutionState(t, s, projID, parentID) != "completed" {
		t.Fatalf("parent execution_state should be completed, got %s", getTaskExecutionState(t, s, projID, parentID))
	}
	if getTaskExecutionState(t, s, projID, childID) != "completed" {
		t.Fatalf("child execution_state should be completed, got %s", getTaskExecutionState(t, s, projID, childID))
	}
}

// Test: moving a parent task moves subtasks that are in different columns
func TestMoveParentMovesSubtasksInDifferentColumns(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "parent-mixed")
	subBacklog := createTestSubtask(t, s, projID, parentID, "sub-backlog")
	subReview := createTestSubtask(t, s, projID, parentID, "sub-review")
	subDone := createTestSubtask(t, s, projID, parentID, "sub-done")

	// Move subtasks to different columns
	moveTask(t, s, projID, subReview, "review")
	moveTask(t, s, projID, subDone, "done")

	// Verify different columns
	if getTaskStatus(t, s, projID, subBacklog) != "backlog" {
		t.Fatal("subBacklog should be backlog")
	}
	if getTaskStatus(t, s, projID, subReview) != "review" {
		t.Fatal("subReview should be review")
	}
	if getTaskStatus(t, s, projID, subDone) != "done" {
		t.Fatal("subDone should be done")
	}

	// Move parent to review — all subtasks should follow
	moveTask(t, s, projID, parentID, "review")

	if getTaskStatus(t, s, projID, parentID) != "review" {
		t.Fatal("parent should be review")
	}
	if getTaskStatus(t, s, projID, subBacklog) != "review" {
		t.Fatalf("subBacklog should follow to review, got %s", getTaskStatus(t, s, projID, subBacklog))
	}
	if getTaskStatus(t, s, projID, subReview) != "review" {
		t.Fatalf("subReview should stay in review, got %s", getTaskStatus(t, s, projID, subReview))
	}
	if getTaskStatus(t, s, projID, subDone) != "review" {
		t.Fatalf("subDone should follow to review, got %s", getTaskStatus(t, s, projID, subDone))
	}
}

// Test: moving a subtask does NOT move its siblings or parent
func TestMoveSubtaskDoesNotMoveSiblingsOrParent(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "parent-isolated")
	subA := createTestSubtask(t, s, projID, parentID, "sub-a")
	subB := createTestSubtask(t, s, projID, parentID, "sub-b")

	// Move subA to review — parent and subB should stay in backlog
	moveTask(t, s, projID, subA, "review")

	if getTaskStatus(t, s, projID, subA) != "review" {
		t.Fatal("subA should be review")
	}
	if getTaskStatus(t, s, projID, parentID) != "backlog" {
		t.Fatalf("parent should stay in backlog, got %s", getTaskStatus(t, s, projID, parentID))
	}
	if getTaskStatus(t, s, projID, subB) != "backlog" {
		t.Fatalf("subB should stay in backlog (sibling isolation), got %s", getTaskStatus(t, s, projID, subB))
	}
}

// Helper: create a task with an agent provider URL
func createTestTaskWithAgent(t *testing.T, s *Server, projectID string, prompt string, serverURL string) string {
	t.Helper()
	task := map[string]any{
		"prompt":           prompt,
		"llama_server_url": serverURL,
		"model_name":       "agent-model",
	}
	req := testRequest("POST", "/api/projects/"+projectID+"/tasks", task)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create task with agent: %d %s", rr.Code, rr.Body.String())
	}
	result := mustJSON[map[string]any](t, rr.Body)
	return result["id"].(string)
}
