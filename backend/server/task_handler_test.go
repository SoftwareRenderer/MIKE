package server

import (
	"net/http"
	"testing"
)

func TestTaskCreate(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	req := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]interface{}{
		"prompt": "Add login feature",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]interface{}](t, rr.Body)
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

	req := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]interface{}{
		"prompt": "Add login feature",
		"title":  "Custom Title",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]interface{}](t, rr.Body)
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

	tasks := mustJSON[[]map[string]interface{}](t, rr.Body)
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

	result := mustJSON[map[string]interface{}](t, rr.Body)
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
	req := testRequest("PUT", "/api/projects/"+projID+"/tasks/"+taskID, map[string]interface{}{
		"title": "Updated Title",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]interface{}](t, rr.Body)
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
	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/move", map[string]interface{}{
		"status": "in_progress",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]interface{}](t, rr.Body)
	if result["status"] != "in_progress" {
		t.Fatalf("expected status=in_progress, got %s", result["status"])
	}
}

func TestTaskReorder(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	// Create 3 tasks
	taskIDs := make([]string, 3)
	for i := 0; i < 3; i++ {
		taskIDs[i] = createTestTask(t, s, projID, "Task "+string(rune('A'+i)))
	}

	// Reorder: set explicit column_order values
	req := testRequest("PUT", "/api/projects/"+projID+"/tasks/reorder", []map[string]interface{}{
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
	tasks := mustJSON[[]map[string]interface{}](t, rr.Body)
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
	req := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]interface{}{
		"prompt":         "Child task",
		"parent_task_id": parentID,
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	// Move parent to in_progress (no longer valid for subtasks)
	req = testRequest("POST", "/api/projects/"+projID+"/tasks/"+parentID+"/move", map[string]interface{}{
		"status": "in_progress",
	})
	testResponseAuth(t, s, req)

	// Try to create another child — should fail
	req = testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]interface{}{
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

	req := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]interface{}{
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
	task := map[string]interface{}{
		"prompt":           prompt,
		"llama_server_url": "http://localhost:9999/v1",
		"model_name":       "mock-model",
	}
	req := testRequest("POST", "/api/projects/"+projectID+"/tasks", task)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", rr.Code, rr.Body.String())
	}
	result := mustJSON[map[string]interface{}](t, rr.Body)
	return result["id"].(string)
}

// Helper: create a subtask via API
func createTestSubtask(t *testing.T, s *Server, projectID string, parentTaskID string, prompt string) string {
	t.Helper()
	task := map[string]interface{}{
		"prompt":         prompt,
		"parent_task_id": parentTaskID,
	}
	req := testRequest("POST", "/api/projects/"+projectID+"/tasks", task)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create subtask: %d %s", rr.Code, rr.Body.String())
	}
	result := mustJSON[map[string]interface{}](t, rr.Body)
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
	result := mustJSON[map[string]interface{}](t, rr.Body)
	return result["status"].(string)
}

// Helper: get a task by ID and return its status_state
func getTaskStatusState(t *testing.T, s *Server, projectID string, taskID string) string {
	t.Helper()
	req := testRequest("GET", "/api/projects/"+projectID+"/tasks/"+taskID, nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get task: %d %s", rr.Code, rr.Body.String())
	}
	result := mustJSON[map[string]interface{}](t, rr.Body)
	if ss, ok := result["status_state"].(string); ok {
		return ss
	}
	return ""
}

// Helper: move a task to a status via API
func moveTask(t *testing.T, s *Server, projectID string, taskID string, status string) {
	t.Helper()
	req := testRequest("POST", "/api/projects/"+projectID+"/tasks/"+taskID+"/move", map[string]interface{}{
		"status": status,
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("move task to %s: %d %s", status, rr.Code, rr.Body.String())
	}
}

// Helper: update a task directly via PUT
func updateTask(t *testing.T, s *Server, projectID string, taskID string, updates map[string]interface{}) {
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

	// Move parent to in_progress
	moveTask(t, s, projID, parentID, "in_progress")

	// Try to move subtask to backlog — should be blocked
	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+subID+"/move", map[string]interface{}{
		"status": "backlog",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}

	// Subtask should still be in review
	status := getTaskStatus(t, s, projID, subID)
	if status != "review" {
		t.Fatalf("expected review (blocked), got %s", status)
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
	updateTask(t, s, projID, parentID, map[string]interface{}{"status": "in_progress", "status_state": "idle"})
	updateTask(t, s, projID, subA, map[string]interface{}{"status": "in_progress", "status_state": "idle"})
	updateTask(t, s, projID, subB, map[string]interface{}{"status": "in_progress", "status_state": "idle"})

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

// Test: task move to done sets status_state to completed
func TestTaskMoveDoneSetsCompleted(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "done-state")

	moveTask(t, s, projID, taskID, "done")
	statusState := getTaskStatusState(t, s, projID, taskID)
	if statusState != "completed" {
		t.Fatalf("expected completed, got %s", statusState)
	}
}

// Test: task moved away from done resets status_state
func TestTaskMoveAwayFromDoneResetsState(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "reset-state")

	// Move to done
	moveTask(t, s, projID, taskID, "done")
	if getTaskStatusState(t, s, projID, taskID) != "completed" {
		t.Fatal("should be completed")
	}

	// Move away from done
	moveTask(t, s, projID, taskID, "review")
	statusState := getTaskStatusState(t, s, projID, taskID)
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
	proj := mustJSON[map[string]interface{}](t, rr.Body)

	initialSandbox := proj["sandbox_enabled"].(bool)

	// Toggle sandbox via PUT on project
	req = testRequest("PUT", "/api/projects/"+projID, map[string]interface{}{
		"sandbox_enabled": !initialSandbox,
	})
	rr = testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("toggle sandbox: %d %s", rr.Code, rr.Body.String())
	}

	// Verify toggle
	req = testRequest("GET", "/api/projects/"+projID, nil)
	rr = testResponseAuth(t, s, req)
	proj = mustJSON[map[string]interface{}](t, rr.Body)
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
	tasks := mustJSON[[]map[string]interface{}](t, rr.Body)
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
	tasks = mustJSON[[]map[string]interface{}](t, rr.Body)
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

	req := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]interface{}{
		"prompt":           "subtask with llm",
		"parent_task_id":   parentID,
		"llama_server_url": "http://localhost:9999/v1",
		"model_name":       "mock-model",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create subtask with LLM: %d %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]interface{}](t, rr.Body)
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

	board := mustJSON[map[string]interface{}](t, rr.Body)
	tasks := board["tasks"].([]interface{})
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

	result := mustJSON[map[string]interface{}](t, rr.Body)
	if result["status"] != "done" {
		t.Fatalf("expected status=done, got %s", result["status"])
	}
	if result["status_state"] != "completed" {
		t.Fatalf("expected status_state=completed, got %s", result["status_state"])
	}
}
