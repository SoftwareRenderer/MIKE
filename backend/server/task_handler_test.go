package server

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kanban/backend/git"
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
	taskID := createTestTaskWithLLM(t, s, projID, "Move me")

	// Move to in_progress (auto-start succeeds because LLM is configured)
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

// Test: move to in_progress reverts to backlog when auto-start fails
func TestTaskMoveAutoStartFailReverts(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	// Task WITHOUT LLM config — auto-start will fail
	taskID := createTestTask(t, s, projID, "No LLM task")

	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/move", map[string]any{
		"status": "in_progress",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)
	// Task reverts to backlog because auto-start failed (no LLM config)
	if result["status"] != "backlog" {
		t.Fatalf("expected status=backlog (reverted on auto-start failure), got %s", result["status"])
	}
	if result["execution_state"] != "idle" {
		t.Fatalf("expected execution_state=idle, got %s", result["execution_state"])
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

	// Create a parent in backlog (with LLM config so auto-start succeeds)
	parentID := createTestTaskWithLLM(t, s, projID, "Parent task")

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

	// Verify parent stayed in in_progress (auto-start succeeded with LLM config)
	if getTaskStatus(t, s, projID, parentID) != "in_progress" {
		t.Fatalf("expected parent in_progress, got %s", getTaskStatus(t, s, projID, parentID))
	}

	// Try to create another child — should fail (parent not in backlog)
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

// Helper: create a subtask with LLM config so the executor can start
func createTestSubtaskWithLLM(t *testing.T, s *Server, projectID string, parentTaskID string, prompt string) string {
	t.Helper()
	task := map[string]any{
		"prompt":           prompt,
		"parent_task_id":   parentTaskID,
		"llama_server_url": "http://localhost:9999/v1",
		"model_name":       "mock-model",
	}
	req := testRequest("POST", "/api/projects/"+projectID+"/tasks", task)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create subtask with LLM: %d %s", rr.Code, rr.Body.String())
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
	// subA needs LLM config so auto-start succeeds when moved back to in_progress
	subA := createTestSubtaskWithLLM(t, s, projID, parentID, "sub-a")
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

// Test: commit-preview endpoint returns a commit message
func TestCommitPreview(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "preview-test")

	// Move task to review (required for commit operations)
	moveReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/move", map[string]any{"status": "review"})
	moveRR := testResponseAuth(t, s, moveReq)
	if moveRR.Code != http.StatusOK {
		t.Fatalf("move to review: %d %s", moveRR.Code, moveRR.Body.String())
	}

	// Get commit preview
	req := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/commit-preview", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("commit-preview: %d %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)
	commitMsg, ok := result["commit_message"].(string)
	if !ok || commitMsg == "" {
		t.Fatalf("expected non-empty commit_message, got %v", result["commit_message"])
	}
	// Message should contain the task title
	if !strings.Contains(commitMsg, "preview-test") {
		t.Fatalf("commit message should contain task title, got: %s", commitMsg)
	}
}

// Test: commit-and-done accepts custom commit message from request body
func TestCommitAndDoneCustomMessage(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "custom-msg")

	// Move task to review
	moveReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/move", map[string]any{"status": "review"})
	moveRR := testResponseAuth(t, s, moveReq)
	if moveRR.Code != http.StatusOK {
		t.Fatalf("move to review: %d %s", moveRR.Code, moveRR.Body.String())
	}

	// Commit-and-done with custom message (no worktree, but should still work)
	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/commit", map[string]any{
		"commit_message": "My custom commit message",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("commit-and-done with custom message: %d %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)
	if result["status"] != "done" {
		t.Fatalf("expected status=done, got %s", result["status"])
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

// Test: task delete cleans up fork thread branches
func TestTaskDeleteCleansThreadBranches(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "thread-branch-cleanup")

	// Get project path to create git branch
	proj, err := s.store.GetProject(projID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}

	// Create a fork thread in the DB with a git_branch
	threadID := "test-thread-branch-cleanup"
	thread := &types.TaskThread{
		ID:        threadID,
		TaskID:    taskID,
		Name:      "Fork #1",
		IsActive:  false,
		GitBranch: "thread/" + threadID,
	}
	if err := s.store.CreateThread(thread); err != nil {
		t.Fatalf("create thread: %v", err)
	}

	// Create the corresponding git branch in the project repo
	cmd := exec.Command("git", "-C", proj.Path, "branch", "thread/"+threadID)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch: %s: %v", string(out), err)
	}

	// Verify branch exists
	cmd = exec.Command("git", "-C", proj.Path, "branch", "--list", "thread/"+threadID)
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), "thread/"+threadID) {
		t.Fatal("thread branch should exist before task delete")
	}

	// Delete the task
	req := testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+taskID, nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete task: expected 204, got %d %s", rr.Code, rr.Body.String())
	}

	// Verify thread branch is gone
	cmd = exec.Command("git", "-C", proj.Path, "branch", "--list", "thread/"+threadID)
	out, _ = cmd.Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("thread branch should be deleted after task delete, but still exists: %s", string(out))
	}
}

// Test: task delete prunes orphaned task/* branches that are not tracked by task.Branch
func TestTaskDeletePrunesOrphanedTaskBranches(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "orphan-branch-cleanup")

	// Get project path
	proj, err := s.store.GetProject(projID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}

	// Create orphaned task branches that don't match any task in the DB.
	// These simulate branches left behind by crashed/deleted tasks.
	orphanBranch := "task/orphan-abc-def-ghi"
	cmd := exec.Command("git", "-C", proj.Path, "branch", orphanBranch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch: %s: %v", string(out), err)
	}

	// Also create a branch that matches the task we'll delete
	taskBranch := "task/" + taskID
	cmd = exec.Command("git", "-C", proj.Path, "branch", taskBranch)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch: %s: %v", string(out), err)
	}

	// Verify branches exist
	cmd = exec.Command("git", "-C", proj.Path, "branch", "--list", orphanBranch)
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), orphanBranch) {
		t.Fatal("orphan branch should exist before task delete")
	}

	// Delete the task — this should prune ALL orphaned task/* branches
	req := testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+taskID, nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete task: expected 204, got %d %s", rr.Code, rr.Body.String())
	}

	// Verify the orphaned branch is gone (no task in DB matches it)
	cmd = exec.Command("git", "-C", proj.Path, "branch", "--list", orphanBranch)
	out, _ = cmd.Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("orphaned task branch should be pruned after task delete, but still exists: %s", string(out))
	}

	// Verify the task's own branch is also gone (task was deleted, so no match)
	cmd = exec.Command("git", "-C", proj.Path, "branch", "--list", taskBranch)
	out, _ = cmd.Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("task branch should be pruned after task delete, but still exists: %s", string(out))
	}
}

// Test: task delete keeps branches for other active tasks in the same project
func TestTaskDeleteKeepsActiveTaskBranches(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskToDelete := createTestTask(t, s, projID, "task-to-delete")
	taskToKeep := createTestTask(t, s, projID, "task-to-keep")

	proj, err := s.store.GetProject(projID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}

	// Create branches for both tasks
	branchToDelete := "task/" + taskToDelete
	branchToKeep := "task/" + taskToKeep

	cmd := exec.Command("git", "-C", proj.Path, "branch", branchToDelete)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch: %s: %v", string(out), err)
	}
	cmd = exec.Command("git", "-C", proj.Path, "branch", branchToKeep)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch: %s: %v", string(out), err)
	}

	// Delete only one task
	req := testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+taskToDelete, nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete task: expected 204, got %d %s", rr.Code, rr.Body.String())
	}

	// Verify the deleted task's branch is gone
	cmd = exec.Command("git", "-C", proj.Path, "branch", "--list", branchToDelete)
	out, _ := cmd.Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("deleted task branch should be pruned: %s", string(out))
	}

	// Verify the other task's branch is still there
	cmd = exec.Command("git", "-C", proj.Path, "branch", "--list", branchToKeep)
	out, _ = cmd.Output()
	if strings.TrimSpace(string(out)) == "" {
		t.Error("active task branch should be kept after other task is deleted")
	}
}

// TestTaskDeleteCleansStashes verifies that deleting a task removes all
// kanban-labeled stash entries from the project's git repo.
func TestTaskDeleteCleansStashes(t *testing.T) {
	s := testServer(t)

	// Create a project with a real git repo
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	initGitRepo(t, tmpDir)

	cookie, csrfToken := loginTestCSRF(t, s)
	proj := map[string]any{"name": "stash-cleanup-test", "path": tmpDir}
	req := testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	projID := mustJSON[map[string]any](t, rr.Body)["id"].(string)

	// Create a task
	taskReq := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]any{"prompt": "test task"})
	withAuthCSRF(taskReq, cookie, csrfToken)
	taskRR := testResponse(t, s, taskReq)
	if taskRR.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", taskRR.Code, taskRR.Body.String())
	}
	taskData := mustJSON[map[string]any](t, taskRR.Body)
	taskID := taskData["id"].(string)

	// Create kanban-labeled stash entries (simulating stale stashes from crashes)
	// Need working tree changes for git stash push to work
	os.WriteFile(filepath.Join(tmpDir, "change.txt"), []byte("a\n"), 0o644)
	mustRunGit(t, tmpDir, "add", "change.txt")
	mustRunGit(t, tmpDir, "stash", "push", "-u", "-m", git.StashMessageForSwitch(taskID))

	os.WriteFile(filepath.Join(tmpDir, "change.txt"), []byte("b\n"), 0o644)
	mustRunGit(t, tmpDir, "add", "change.txt")
	mustRunGit(t, tmpDir, "stash", "push", "-u", "-m", git.StashMessageForFork(taskID))

	// Also create a stash for a different task — should NOT be removed
	otherTaskID := "other-task-xyz"
	os.WriteFile(filepath.Join(tmpDir, "change.txt"), []byte("c\n"), 0o644)
	mustRunGit(t, tmpDir, "add", "change.txt")
	mustRunGit(t, tmpDir, "stash", "push", "-u", "-m", git.StashMessageForSwitch(otherTaskID))

	// Verify stashes exist
	stashList := gitListStashes(t, tmpDir)
	if len(stashList) != 3 {
		t.Fatalf("expected 3 stashes before delete, got %d: %v", len(stashList), stashList)
	}

	// Delete the task
	deleteReq := testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+taskID, nil)
	deleteRR := testResponseAuth(t, s, deleteReq)
	if deleteRR.Code != http.StatusNoContent {
		t.Fatalf("delete task: expected 204, got %d %s", deleteRR.Code, deleteRR.Body.String())
	}

	// Verify our task's stashes are gone but the other task's stash remains
	stashList = gitListStashes(t, tmpDir)
	for _, subject := range stashList {
		if strings.Contains(subject, taskID) {
			t.Errorf("stash for deleted task %s should be gone, found: %s", taskID, subject)
		}
	}
	foundOther := false
	for _, subject := range stashList {
		if strings.Contains(subject, otherTaskID) {
			foundOther = true
			break
		}
	}
	if !foundOther {
		t.Errorf("stash for other task %s should remain, got: %v", otherTaskID, stashList)
	}
}

// TestTaskDeleteCleansStashes_NoBranch verifies stash cleanup runs even when
// the task has no branch (created but never started).
func TestTaskDeleteCleansStashes_NoBranch(t *testing.T) {
	s := testServer(t)

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	initGitRepo(t, tmpDir)

	cookie, csrfToken := loginTestCSRF(t, s)
	proj := map[string]any{"name": "stash-cleanup-nobranch", "path": tmpDir}
	req := testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	projID := mustJSON[map[string]any](t, rr.Body)["id"].(string)

	// Create a task (no branch — never started)
	taskReq := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]any{"prompt": "test task"})
	withAuthCSRF(taskReq, cookie, csrfToken)
	taskRR := testResponse(t, s, taskReq)
	if taskRR.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", taskRR.Code, taskRR.Body.String())
	}
	taskData := mustJSON[map[string]any](t, taskRR.Body)
	taskID := taskData["id"].(string)

	// Create kanban-labeled stash entries (simulating stale stashes)
	os.WriteFile(filepath.Join(tmpDir, "change.txt"), []byte("a\n"), 0o644)
	mustRunGit(t, tmpDir, "add", "change.txt")
	mustRunGit(t, tmpDir, "stash", "push", "-u", "-m", git.StashMessageForSwitch(taskID))

	// Verify stash exists
	stashList := gitListStashes(t, tmpDir)
	if len(stashList) != 1 {
		t.Fatalf("expected 1 stash before delete, got %d: %v", len(stashList), stashList)
	}

	// Delete the task (has no branch)
	deleteReq := testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+taskID, nil)
	deleteRR := testResponseAuth(t, s, deleteReq)
	if deleteRR.Code != http.StatusNoContent {
		t.Fatalf("delete task: expected 204, got %d %s", deleteRR.Code, deleteRR.Body.String())
	}

	// Verify stash is gone
	stashList = gitListStashes(t, tmpDir)
	if len(stashList) != 0 {
		t.Errorf("expected 0 stashes after delete, got %d: %v", len(stashList), stashList)
	}
}

// TestTaskDeleteCleansCheckpointStashes verifies that checkpoint-style stashes
// (created via git stash create + stash store) are also cleaned up on task deletion.
func TestTaskDeleteCleansCheckpointStashes(t *testing.T) {
	s := testServer(t)

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	initGitRepo(t, tmpDir)

	cookie, csrfToken := loginTestCSRF(t, s)
	proj := map[string]any{"name": "stash-cleanup-checkpoint", "path": tmpDir}
	req := testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	projID := mustJSON[map[string]any](t, rr.Body)["id"].(string)

	// Create a task and start it (create branch)
	taskReq := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]any{"prompt": "test task"})
	withAuthCSRF(taskReq, cookie, csrfToken)
	taskRR := testResponse(t, s, taskReq)
	if taskRR.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", taskRR.Code, taskRR.Body.String())
	}
	taskData := mustJSON[map[string]any](t, taskRR.Body)
	taskID := taskData["id"].(string)

	// Create a branch and update the task (simulating task start)
	branchName := "task/" + taskID
	mustRunGit(t, tmpDir, "branch", branchName)
	task, _ := s.store.GetTask(projID, taskID)
	task.Branch = branchName
	s.store.UpdateTask(task)

	// Create checkpoint-style stashes with kanban-labeled messages.
	// This is how the executor saves worktree state during execution.
	os.WriteFile(filepath.Join(tmpDir, "change.txt"), []byte("checkpoint1\n"), 0o644)
	mustRunGit(t, tmpDir, "add", "change.txt")
	hash1 := mustRunGitOutput(t, tmpDir, "stash", "create")
	mustRunGit(t, tmpDir, "stash", "store", "-m", git.StashMessageForCheckpoint(taskID, branchName), hash1)

	os.WriteFile(filepath.Join(tmpDir, "change.txt"), []byte("checkpoint2\n"), 0o644)
	mustRunGit(t, tmpDir, "add", "change.txt")
	hash2 := mustRunGitOutput(t, tmpDir, "stash", "create")
	mustRunGit(t, tmpDir, "stash", "store", "-m", git.StashMessageForCheckpoint(taskID, branchName), hash2)

	// Verify stashes exist
	stashList := gitListStashes(t, tmpDir)
	if len(stashList) < 2 {
		t.Fatalf("expected at least 2 stashes before delete, got %d: %v", len(stashList), stashList)
	}

	// Delete the task
	deleteReq := testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+taskID, nil)
	deleteRR := testResponseAuth(t, s, deleteReq)
	if deleteRR.Code != http.StatusNoContent {
		t.Fatalf("delete task: expected 204, got %d %s", deleteRR.Code, deleteRR.Body.String())
	}

	// Verify checkpoint stashes are gone (matched by task ID in kanban-checkpoint label)
	stashList = gitListStashes(t, tmpDir)
	for _, subject := range stashList {
		if strings.Contains(subject, taskID) {
			t.Errorf("checkpoint stash for task %s should be gone, found: %s", taskID, subject)
		}
	}
}

// gitListStashes returns the list of stash subjects from a git repo.
// Uses %gs (reflog subject) which shows the actual stash message.
func gitListStashes(t *testing.T, dir string) []string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "stash", "list", "--format=%gs")
	out, err := cmd.Output()
	if err != nil {
		return nil // no stashes
	}
	var subjects []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			subjects = append(subjects, line)
		}
	}
	return subjects
}

// Helper: get the full task as a map
func getTask(t *testing.T, s *Server, projectID string, taskID string) map[string]any {
	t.Helper()
	req := testRequest("GET", "/api/projects/"+projectID+"/tasks/"+taskID, nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get task: %d %s", rr.Code, rr.Body.String())
	}
	return mustJSON[map[string]any](t, rr.Body)
}

// TestCommitAndDoneMergeConflictTriggersResolution verifies that when a merge
// conflict is detected during commit-and-done, the task is moved to in_progress
// with the merge conflict flag set, and the original auto_merge/auto_commit
// settings are preserved (the executor loop's HasMergeConflict path handles
// the post-resolution merge).
func TestCommitAndDoneMergeConflictTriggersResolution(t *testing.T) {
	s := testServer(t)

	// Create a project with a real git repo
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	cookie, csrfToken := loginTestCSRF(t, s)
	proj := map[string]any{"name": "conflict-auto-merge", "path": tmpDir}
	req := testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	projID := mustJSON[map[string]any](t, rr.Body)["id"].(string)

	// Create a task with auto_merge=false
	taskReq := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]any{
		"prompt":           "test merge conflict auto merge",
		"llama_server_url": "http://localhost:9999/v1",
		"model_name":       "mock-model",
		"auto_merge":       false,
	})
	withAuthCSRF(taskReq, cookie, csrfToken)
	taskRR := testResponse(t, s, taskReq)
	if taskRR.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", taskRR.Code, taskRR.Body.String())
	}
	taskData := mustJSON[map[string]any](t, taskRR.Body)
	taskID := taskData["id"].(string)

	// Verify auto_merge is false
	task := getTask(t, s, projID, taskID)
	if task["auto_merge"] == true {
		t.Fatal("auto_merge should be false initially")
	}

	// Create a branch for the task
	branchName := "task/" + taskID
	mustRunGit(t, tmpDir, "branch", branchName)

	// Create a file on the task branch
	mustRunGit(t, tmpDir, "checkout", branchName)
	os.WriteFile(filepath.Join(tmpDir, "shared.txt"), []byte("task version\n"), 0o644)
	mustRunGit(t, tmpDir, "add", "shared.txt")
	mustRunGit(t, tmpDir, "commit", "-m", "task edit shared")
	mustRunGit(t, tmpDir, "checkout", "master")

	// Create a conflicting change on master
	os.WriteFile(filepath.Join(tmpDir, "shared.txt"), []byte("master version\n"), 0o644)
	mustRunGit(t, tmpDir, "add", "shared.txt")
	mustRunGit(t, tmpDir, "commit", "-m", "master edit shared")

	// Create a worktree for the task at the path the worktree manager expects.
	// MIKE_WORKTREES_DIR env var is set by testServer and takes priority over config.
	wtPath := filepath.Join(os.Getenv("MIKE_WORKTREES_DIR"), projID, taskID)
	mustRunGit(t, tmpDir, "worktree", "add", wtPath, branchName)

	// Verify worktree exists at the expected path
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("worktree does not exist at %q: %v", wtPath, err)
	}

	// Update task with branch info
	taskDB, _ := s.store.GetTask(projID, taskID)
	taskDB.Branch = branchName
	taskDB.BaseRef = "master"
	s.store.UpdateTask(taskDB)

	// Move task to review (required for commit)
	moveTask(t, s, projID, taskID, "review")

	// Commit-and-done — this will trigger a merge conflict during sync-merge
	commitReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/commit", nil)
	commitRR := testResponseAuth(t, s, commitReq)
	if commitRR.Code != http.StatusOK {
		t.Fatalf("commit-and-done: %d %s", commitRR.Code, commitRR.Body.String())
	}

	commitResult := mustJSON[map[string]any](t, commitRR.Body)

	// Verify the task was moved to in_progress (conflict resolution path)
	if commitResult["status"] != "in_progress" {
		t.Fatalf("expected status=in_progress, got %s", commitResult["status"])
	}

	// Verify auto_merge/auto_commit are preserved (not modified)
	if commitResult["auto_merge"] != false {
		t.Fatalf("expected auto_merge=false (unchanged), got %v", commitResult["auto_merge"])
	}
	if commitResult["auto_commit"] != false {
		t.Fatalf("expected auto_commit=false (unchanged), got %v", commitResult["auto_commit"])
	}

	// Verify has_merge_conflict is set
	if commitResult["has_merge_conflict"] != true {
		t.Fatalf("expected has_merge_conflict=true, got %v", commitResult["has_merge_conflict"])
	}
}

// TestCommitAndDoneMergeConflictPreservesAutoMerge verifies that when auto_merge
// is already enabled, commit-and-done with a merge conflict preserves the setting.
func TestCommitAndDoneMergeConflictPreservesAutoMerge(t *testing.T) {
	s := testServer(t)

	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	cookie, csrfToken := loginTestCSRF(t, s)
	proj := map[string]any{"name": "conflict-auto-merge-already", "path": tmpDir}
	req := testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	projID := mustJSON[map[string]any](t, rr.Body)["id"].(string)

	// Create a task with auto_merge=true
	taskReq := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]any{
		"prompt":           "test merge conflict auto merge already enabled",
		"llama_server_url": "http://localhost:9999/v1",
		"model_name":       "mock-model",
		"auto_merge":       true,
	})
	withAuthCSRF(taskReq, cookie, csrfToken)
	taskRR := testResponse(t, s, taskReq)
	if taskRR.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", taskRR.Code, taskRR.Body.String())
	}
	taskData := mustJSON[map[string]any](t, taskRR.Body)
	taskID := taskData["id"].(string)

	// Verify auto_merge is true
	task := getTask(t, s, projID, taskID)
	if task["auto_merge"] != true {
		t.Fatal("auto_merge should be true initially")
	}

	// Create a branch for the task
	branchName := "task/" + taskID
	mustRunGit(t, tmpDir, "branch", branchName)

	// Create a file on the task branch
	mustRunGit(t, tmpDir, "checkout", branchName)
	os.WriteFile(filepath.Join(tmpDir, "shared.txt"), []byte("task version\n"), 0o644)
	mustRunGit(t, tmpDir, "add", "shared.txt")
	mustRunGit(t, tmpDir, "commit", "-m", "task edit shared")
	mustRunGit(t, tmpDir, "checkout", "master")

	// Create a conflicting change on master
	os.WriteFile(filepath.Join(tmpDir, "shared.txt"), []byte("master version\n"), 0o644)
	mustRunGit(t, tmpDir, "add", "shared.txt")
	mustRunGit(t, tmpDir, "commit", "-m", "master edit shared")

	// Create a worktree for the task
	wtPath := filepath.Join(os.Getenv("MIKE_WORKTREES_DIR"), projID, taskID)
	mustRunGit(t, tmpDir, "worktree", "add", wtPath, branchName)

	// Update task with branch info
	taskDB, _ := s.store.GetTask(projID, taskID)
	taskDB.Branch = branchName
	taskDB.BaseRef = "master"
	s.store.UpdateTask(taskDB)

	// Move task to review
	moveTask(t, s, projID, taskID, "review")

	// Commit-and-done — merge conflict path
	commitReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/commit", nil)
	commitRR := testResponseAuth(t, s, commitReq)
	if commitRR.Code != http.StatusOK {
		t.Fatalf("commit-and-done: %d %s", commitRR.Code, commitRR.Body.String())
	}

	commitResult := mustJSON[map[string]any](t, commitRR.Body)

	// Verify auto_merge stays true (preserved, not modified)
	if commitResult["auto_merge"] != true {
		t.Fatalf("expected auto_merge=true (preserved), got %v", commitResult["auto_merge"])
	}

	// Verify task moved to in_progress
	if commitResult["status"] != "in_progress" {
		t.Fatalf("expected status=in_progress, got %s", commitResult["status"])
	}

	// Verify has_merge_conflict is set
	if commitResult["has_merge_conflict"] != true {
		t.Fatalf("expected has_merge_conflict=true, got %v", commitResult["has_merge_conflict"])
	}
}

// TestCommitAndDoneMergeConflictSystemMessage verifies that the system message
// in the chat notes the merge conflict and agent resolution.
func TestCommitAndDoneMergeConflictSystemMessage(t *testing.T) {
	s := testServer(t)

	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	cookie, csrfToken := loginTestCSRF(t, s)
	proj := map[string]any{"name": "conflict-system-msg", "path": tmpDir}
	req := testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	projID := mustJSON[map[string]any](t, rr.Body)["id"].(string)

	// Create a task with auto_merge=false
	taskReq := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]any{
		"prompt":           "test system message",
		"llama_server_url": "http://localhost:9999/v1",
		"model_name":       "mock-model",
		"auto_merge":       false,
	})
	withAuthCSRF(taskReq, cookie, csrfToken)
	taskRR := testResponse(t, s, taskReq)
	if taskRR.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", taskRR.Code, taskRR.Body.String())
	}
	taskData := mustJSON[map[string]any](t, taskRR.Body)
	taskID := taskData["id"].(string)

	// Create a branch for the task
	branchName := "task/" + taskID
	mustRunGit(t, tmpDir, "branch", branchName)

	// Create a file on the task branch
	mustRunGit(t, tmpDir, "checkout", branchName)
	os.WriteFile(filepath.Join(tmpDir, "shared.txt"), []byte("task version\n"), 0o644)
	mustRunGit(t, tmpDir, "add", "shared.txt")
	mustRunGit(t, tmpDir, "commit", "-m", "task edit shared")
	mustRunGit(t, tmpDir, "checkout", "master")

	// Create a conflicting change on master
	os.WriteFile(filepath.Join(tmpDir, "shared.txt"), []byte("master version\n"), 0o644)
	mustRunGit(t, tmpDir, "add", "shared.txt")
	mustRunGit(t, tmpDir, "commit", "-m", "master edit shared")

	// Create a worktree for the task
	wtPath := filepath.Join(os.Getenv("MIKE_WORKTREES_DIR"), projID, taskID)
	mustRunGit(t, tmpDir, "worktree", "add", wtPath, branchName)

	// Update task with branch info
	taskDB, _ := s.store.GetTask(projID, taskID)
	taskDB.Branch = branchName
	taskDB.BaseRef = "master"
	s.store.UpdateTask(taskDB)

	// Move task to review
	moveTask(t, s, projID, taskID, "review")

	// Commit-and-done — merge conflict path
	commitReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/commit", nil)
	commitRR := testResponseAuth(t, s, commitReq)
	if commitRR.Code != http.StatusOK {
		t.Fatalf("commit-and-done: %d %s", commitRR.Code, commitRR.Body.String())
	}

	// Get task messages to verify system message content
	msgs, err := s.store.GetTaskMessages(taskID, "")
	if err != nil {
		t.Fatalf("get task messages: %v", err)
	}

	// Find the system message about merge conflict
	var foundSystemMsg bool
	for _, msg := range msgs {
		if msg.Role == "system" && strings.Contains(msg.Content, "Merge conflict detected") {
			foundSystemMsg = true
			// Verify it mentions resolving with agent
			if !strings.Contains(msg.Content, "resolving with agent") {
				t.Fatalf("system message should mention resolving with agent, got: %s", msg.Content)
			}
			break
		}
	}

	if !foundSystemMsg {
		t.Fatal("expected system message about merge conflict resolution")
	}
}

// TestTaskMoveUpdatesUpdatedAt verifies that moving a task between columns
// updates the updated_at timestamp, enabling chronological sorting by activity.
func TestTaskMoveUpdatesUpdatedAt(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	// Create 3 tasks (taskB and taskC stay in backlog for ordering verification)
	taskA := createTestTask(t, s, projID, "task-a")
	_ = createTestTask(t, s, projID, "task-b")
	_ = createTestTask(t, s, projID, "task-c")

	// Get initial updated_at for taskA
	taskAMap := getTask(t, s, projID, taskA)
	initialUpdatedAt := taskAMap["updated_at"].(string)

	// Small delay to ensure distinct timestamps (updated_at uses second precision)
	time.Sleep(1 * time.Second)

	// Move taskA to review — updated_at should change
	moveTask(t, s, projID, taskA, "review")
	taskAMap = getTask(t, s, projID, taskA)
	reviewUpdatedAt := taskAMap["updated_at"].(string)
	if reviewUpdatedAt == initialUpdatedAt {
		t.Fatal("updated_at should change after move")
	}

	// Small delay to ensure distinct timestamps (updated_at uses second precision)
	time.Sleep(1 * time.Second)

	// Move taskA to done — updated_at should change again
	moveTask(t, s, projID, taskA, "done")
	taskAMap = getTask(t, s, projID, taskA)
	doneUpdatedAt := taskAMap["updated_at"].(string)
	if doneUpdatedAt == reviewUpdatedAt {
		t.Fatal("updated_at should change after second move")
	}

	// Verify chronological order: initial < review < done
	if initialUpdatedAt >= reviewUpdatedAt {
		t.Fatalf("expected initial < review, got %s >= %s", initialUpdatedAt, reviewUpdatedAt)
	}
	if reviewUpdatedAt >= doneUpdatedAt {
		t.Fatalf("expected review < done, got %s >= %s", reviewUpdatedAt, doneUpdatedAt)
	}

	// Verify all three tasks in backlog are sortable by updated_at
	// taskA was created first, taskB second, taskC third
	tasks := getAllTasks(t, s, projID)
	backlogTasks := []map[string]any{}
	for _, task := range tasks {
		if task["status"] == "backlog" {
			backlogTasks = append(backlogTasks, task)
		}
	}
	if len(backlogTasks) != 2 {
		t.Fatalf("expected 2 backlog tasks, got %d", len(backlogTasks))
	}
	// taskB created before taskC, so taskB.updated_at <= taskC.updated_at
	if backlogTasks[0]["updated_at"].(string) > backlogTasks[1]["updated_at"].(string) {
		t.Fatal("backlog tasks should be in chronological order by updated_at")
	}
}

// Helper: get all tasks for a project as a slice of maps
func getAllTasks(t *testing.T, s *Server, projectID string) []map[string]any {
	t.Helper()
	req := testRequest("GET", "/api/projects/"+projectID+"/tasks", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get tasks: %d %s", rr.Code, rr.Body.String())
	}
	return mustJSON[[]map[string]any](t, rr.Body)
}

// Helper: get a task's auto_merge value
func getTaskAutoMerge(t *testing.T, s *Server, projectID string, taskID string) bool {
	t.Helper()
	req := testRequest("GET", "/api/projects/"+projectID+"/tasks/"+taskID, nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get task: %d %s", rr.Code, rr.Body.String())
	}
	result := mustJSON[map[string]any](t, rr.Body)
	if v, ok := result["auto_merge"]; ok {
		return v.(bool)
	}
	return false
}

// Test: enabling subtasks_auto_merge recursively enables auto_merge on all descendants
func TestSubtasksAutoMergePropagatesToDescendants(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	// Create a parent task with subtasks_auto_merge=false
	parentID := createTestTask(t, s, projID, "parent-auto-merge")

	// Create two direct children
	subA := createTestSubtask(t, s, projID, parentID, "sub-a")
	subB := createTestSubtask(t, s, projID, parentID, "sub-b")

	// Create a grandchild under subA
	grandchildA := createTestSubtask(t, s, projID, subA, "grandchild-a")

	// Verify initial state: auto_merge should be false (default)
	if getTaskAutoMerge(t, s, projID, subA) {
		t.Fatal("subA auto_merge should be false initially")
	}
	if getTaskAutoMerge(t, s, projID, subB) {
		t.Fatal("subB auto_merge should be false initially")
	}
	if getTaskAutoMerge(t, s, projID, grandchildA) {
		t.Fatal("grandchildA auto_merge should be false initially")
	}

	// Enable subtasks_auto_merge on parent via PUT
	updateTask(t, s, projID, parentID, map[string]any{
		"subtasks_auto_merge": true,
	})

	// Verify parent has subtasks_auto_merge=true
	req := testRequest("GET", "/api/projects/"+projID+"/tasks/"+parentID, nil)
	rr := testResponseAuth(t, s, req)
	parent := mustJSON[map[string]any](t, rr.Body)
	if parent["subtasks_auto_merge"] != true {
		t.Fatal("parent subtasks_auto_merge should be true")
	}

	// Verify ALL descendants have auto_merge=true (recursive)
	if !getTaskAutoMerge(t, s, projID, subA) {
		t.Fatal("subA auto_merge should be true after parent enables subtasks_auto_merge")
	}
	if !getTaskAutoMerge(t, s, projID, subB) {
		t.Fatal("subB auto_merge should be true after parent enables subtasks_auto_merge")
	}
	if !getTaskAutoMerge(t, s, projID, grandchildA) {
		t.Fatal("grandchildA auto_merge should be true (recursive propagation)")
	}
}

// Test: disabling subtasks_auto_merge cascades auto_merge=false to descendants
func TestSubtasksAutoMergeDisablePropagates(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	parentID := createTestTask(t, s, projID, "parent-disable-propagate")
	subA := createTestSubtask(t, s, projID, parentID, "sub-a")

	// First enable subtasks_auto_merge (which sets auto_merge on subA)
	updateTask(t, s, projID, parentID, map[string]any{
		"subtasks_auto_merge": true,
	})

	// Verify subA got auto_merge=true
	if !getTaskAutoMerge(t, s, projID, subA) {
		t.Fatal("subA auto_merge should be true after enable")
	}

	// Now disable subtasks_auto_merge on parent
	updateTask(t, s, projID, parentID, map[string]any{
		"subtasks_auto_merge": false,
	})

	// Verify subA's auto_merge is now false (disable propagates)
	if getTaskAutoMerge(t, s, projID, subA) {
		t.Fatal("subA auto_merge should be false after parent disables subtasks_auto_merge")
	}
}

// Test: enabling subtasks_auto_merge on a task with no subtasks is a no-op
func TestSubtasksAutoMergeNoSubtasks(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	taskID := createTestTask(t, s, projID, "no-subtasks")

	// Enable subtasks_auto_merge on a task with no children
	updateTask(t, s, projID, taskID, map[string]any{
		"subtasks_auto_merge": true,
	})

	// Should succeed without error
	req := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID, nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	result := mustJSON[map[string]any](t, rr.Body)
	if result["subtasks_auto_merge"] != true {
		t.Fatal("subtasks_auto_merge should be true")
	}
}

// Test: enabling subtasks_auto_merge also sets auto_commit=true on descendants
// (since auto_merge requires auto_commit)
func TestSubtasksAutoMergeSetsAutoCommitOnDescendants(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	parentID := createTestTask(t, s, projID, "parent-auto-commit")
	subA := createTestSubtask(t, s, projID, parentID, "sub-a-commit")

	// Verify initial state
	req := testRequest("GET", "/api/projects/"+projID+"/tasks/"+subA, nil)
	rr := testResponseAuth(t, s, req)
	subTask := mustJSON[map[string]any](t, rr.Body)
	if subTask["auto_commit"] == true {
		t.Fatal("subA auto_commit should be false initially")
	}

	// Enable subtasks_auto_merge on parent
	updateTask(t, s, projID, parentID, map[string]any{
		"subtasks_auto_merge": true,
	})

	// Verify subA has both auto_merge=true AND auto_commit=true
	req = testRequest("GET", "/api/projects/"+projID+"/tasks/"+subA, nil)
	rr = testResponseAuth(t, s, req)
	subTask = mustJSON[map[string]any](t, rr.Body)
	if subTask["auto_merge"] != true {
		t.Fatal("subA auto_merge should be true")
	}
	if subTask["auto_commit"] != true {
		t.Fatal("subA auto_commit should be true (prerequisite for auto_merge)")
	}
}

func TestTaskOperatingModeRemount(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	// Use agent provider to ensure llama_server_url and model_name are set
	taskID := createTestTaskWithAgent(t, s, projID, "Test mode remount", "http://localhost:9999/v1")

	t.Run("remounts when not running", func(t *testing.T) {
		// 1. Start task to create sandbox
		reqStart := testRequest(http.MethodPost, "/api/projects/"+projID+"/tasks/"+taskID+"/start", nil)
		rrStart := testResponseAuth(t, s, reqStart)
		if rrStart.Code != http.StatusOK {
			t.Fatalf("start task failed: %d %s", rrStart.Code, rrStart.Body.String())
		}

		// Sandbox should be created synchronously in StartTask (via prepareTaskExecution)
		if !s.sandboxMgr.HasActiveContainer(taskID) {
			t.Fatal("expected sandbox to be created after start")
		}

		// 2. Stop task (execution state becomes idle, but sandbox remains)
		reqStop := testRequest(http.MethodPost, "/api/projects/"+projID+"/tasks/"+taskID+"/stop", nil)
		rrStop := testResponseAuth(t, s, reqStop)
		if rrStop.Code != http.StatusOK {
			t.Fatalf("stop task failed: %d %s", rrStop.Code, rrStop.Body.String())
		}

		// 3. Change operating mode
		reqMode := testRequest(http.MethodPut, "/api/projects/"+projID+"/tasks/"+taskID, map[string]any{
			"operating_mode": "plan",
		})
		rrMode := testResponseAuth(t, s, reqMode)
		if rrMode.Code != http.StatusOK {
			t.Fatalf("update mode failed: %d %s", rrMode.Code, rrMode.Body.String())
		}

		// 4. Verify sandbox was stopped (remounted)
		if s.sandboxMgr.HasActiveContainer(taskID) {
			t.Fatal("expected sandbox to be stopped after mode change")
		}
	})

	t.Run("does not remount when running", func(t *testing.T) {
		// 1. Start task
		reqStart := testRequest(http.MethodPost, "/api/projects/"+projID+"/tasks/"+taskID+"/start", nil)
		rrStart := testResponseAuth(t, s, reqStart)
		if rrStart.Code != http.StatusOK {
			t.Fatalf("start task failed: %d %s", rrStart.Code, rrStart.Body.String())
		}

		if !s.sandboxMgr.HasActiveContainer(taskID) {
			t.Fatal("expected sandbox to be created after start")
		}

		// 2. Change operating mode while running
		reqMode := testRequest(http.MethodPut, "/api/projects/"+projID+"/tasks/"+taskID, map[string]any{
			"operating_mode": "act", // switch back to act
		})
		rrMode := testResponseAuth(t, s, reqMode)
		if rrMode.Code != http.StatusOK {
			t.Fatalf("update mode failed: %d %s", rrMode.Code, rrMode.Body.String())
		}

		// 3. Verify sandbox was NOT stopped
		if !s.sandboxMgr.HasActiveContainer(taskID) {
			t.Fatal("expected sandbox to remain active while task is running")
		}
	})
}
