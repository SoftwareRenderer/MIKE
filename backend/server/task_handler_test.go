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
	rr := testResponse(t, s, req)
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
	rr := testResponse(t, s, req)
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
	rr := testResponse(t, s, req)
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
	rr := testResponse(t, s, req)
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
	rr := testResponse(t, s, req)
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
	rr := testResponse(t, s, req)
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
	rr := testResponse(t, s, req)
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
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify it's gone
	req = testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID, nil)
	rr = testResponse(t, s, req)
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
	rr := testResponse(t, s, req)
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
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify new order: taskIDs[2] (order=0) first, taskIDs[0] (order=10) last
	req = testRequest("GET", "/api/projects/"+projID+"/tasks", nil)
	rr = testResponse(t, s, req)
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
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	// Move parent to in_progress (no longer valid for subtasks)
	req = testRequest("POST", "/api/projects/"+projID+"/tasks/"+parentID+"/move", map[string]interface{}{
		"status": "in_progress",
	})
	testResponse(t, s, req)

	// Try to create another child — should fail
	req = testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]interface{}{
		"prompt":         "Another child",
		"parent_task_id": parentID,
	})
	rr = testResponse(t, s, req)
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
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}
