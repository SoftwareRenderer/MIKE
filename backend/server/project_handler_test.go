package server

import (
	"net/http"
	"testing"
)

func TestProjectCreate(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	initGitRepo(t, dir)

	req := testRequest("POST", "/api/projects", map[string]interface{}{
		"name": "My Project",
		"path": dir,
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]interface{}](t, rr.Body)
	if result["name"] != "My Project" {
		t.Fatalf("unexpected name: %s", result["name"])
	}
	if result["id"] == "" || result["id"] == nil {
		t.Fatal("project ID should not be empty")
	}
}

func TestProjectCreateDuplicatePath(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	initGitRepo(t, dir)

	// Create first project
	req := testRequest("POST", "/api/projects", map[string]interface{}{
		"name": "Project A",
		"path": dir,
	})
	testResponseAuth(t, s, req)

	// Create second project with same path — should fail (500 for UNIQUE constraint violation)
	req = testRequest("POST", "/api/projects", map[string]interface{}{
		"name": "Project B",
		"path": dir,
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code < http.StatusBadRequest {
		t.Fatalf("expected error for duplicate path, got %d", rr.Code)
	}
}

func TestProjectList(t *testing.T) {
	s := testServer(t)

	// Create 2 projects
	for _, name := range []string{"Project A", "Project B"} {
		dir := t.TempDir()
		initGitRepo(t, dir)
		req := testRequest("POST", "/api/projects", map[string]interface{}{
			"name": name,
			"path": dir,
		})
		testResponseAuth(t, s, req)
	}

	req := testRequest("GET", "/api/projects", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	projects := mustJSON[[]map[string]interface{}](t, rr.Body)
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}
	// Should be sorted by name
	if projects[0]["name"] != "Project A" || projects[1]["name"] != "Project B" {
		t.Fatalf("unexpected order: %v, %v", projects[0]["name"], projects[1]["name"])
	}
}

func TestProjectGetByID(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	req := testRequest("GET", "/api/projects/"+projID, nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]interface{}](t, rr.Body)
	if result["id"] != projID {
		t.Fatalf("wrong ID: %s", result["id"])
	}
}

func TestProjectGetByIDNotFound(t *testing.T) {
	s := testServer(t)

	req := testRequest("GET", "/api/projects/nonexistent", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestProjectUpdate(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	req := testRequest("PUT", "/api/projects/"+projID, map[string]interface{}{
		"name": "Renamed Project",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]interface{}](t, rr.Body)
	if result["name"] != "Renamed Project" {
		t.Fatalf("unexpected name: %s", result["name"])
	}
}

func TestProjectUpdateSandboxNetworkIsolation(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	// Verify default: sandbox disabled, network isolation disabled
	req := testRequest("GET", "/api/projects/"+projID, nil)
	rr := testResponseAuth(t, s, req)
	result := mustJSON[map[string]interface{}](t, rr.Body)
	if result["sandbox_enabled"] == true {
		t.Fatal("expected sandbox_enabled to be false by default")
	}
	if result["sandbox_network_isolation"] == true {
		t.Fatal("expected sandbox_network_isolation to be false by default")
	}

	// Enable sandbox and network isolation
	req = testRequest("PUT", "/api/projects/"+projID, map[string]interface{}{
		"sandbox_enabled":            true,
		"sandbox_network_isolation": true,
	})
	rr = testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	result = mustJSON[map[string]interface{}](t, rr.Body)
	if result["sandbox_enabled"] != true {
		t.Fatal("expected sandbox_enabled to be true")
	}
	if result["sandbox_network_isolation"] != true {
		t.Fatal("expected sandbox_network_isolation to be true")
	}

	// Disable only network isolation (sandbox stays on)
	req = testRequest("PUT", "/api/projects/"+projID, map[string]interface{}{
		"sandbox_network_isolation": false,
	})
	rr = testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	result = mustJSON[map[string]interface{}](t, rr.Body)
	if result["sandbox_enabled"] != true {
		t.Fatal("expected sandbox_enabled to still be true")
	}
	if result["sandbox_network_isolation"] != false {
		t.Fatal("expected sandbox_network_isolation to be false")
	}
}

func TestProjectDelete(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	req := testRequest("DELETE", "/api/projects/"+projID, nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify it's gone
	req = testRequest("GET", "/api/projects/"+projID, nil)
	rr = testResponseAuth(t, s, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", rr.Code)
	}
}

func TestProjectCounts(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	// Create tasks in different statuses
	createTestTask(t, s, projID, "Backlog task 1")
	createTestTask(t, s, projID, "Backlog task 2")

	// Move one to in_progress
	taskID := createTestTask(t, s, projID, "In progress task")
	moveReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/move", map[string]interface{}{
		"status": "in_progress",
	})
	moveRR := testResponseAuth(t, s, moveReq)
	if moveRR.Code != http.StatusOK {
		t.Fatalf("move to in_progress failed: %d %s", moveRR.Code, moveRR.Body.String())
	}

	// Move one to review
	reviewTaskID := createTestTask(t, s, projID, "Review task")
	moveReq2 := testRequest("POST", "/api/projects/"+projID+"/tasks/"+reviewTaskID+"/move", map[string]interface{}{
		"status": "review",
	})
	moveRR2 := testResponseAuth(t, s, moveReq2)
	if moveRR2.Code != http.StatusOK {
		t.Fatalf("move to review failed: %d %s", moveRR2.Code, moveRR2.Body.String())
	}

	// Debug: list tasks and their statuses
	listReq := testRequest("GET", "/api/projects/"+projID+"/tasks", nil)
	listRR := testResponseAuth(t, s, listReq)
	tasks := mustJSON[[]map[string]interface{}](t, listRR.Body)
	for _, task := range tasks {
		t.Logf("task %s: status=%s", task["id"], task["status"])
	}

	req := testRequest("GET", "/api/projects/counts", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	counts := mustJSON[map[string]interface{}](t, rr.Body)
	projCounts, ok := counts[projID].(map[string]interface{})
	if !ok {
		t.Fatalf("project %s not found in counts: %+v", projID, counts)
	}
	if projCounts["backlog"].(float64) != 2 {
		t.Fatalf("expected 2 backlog, got %v", projCounts["backlog"])
	}
	if projCounts["in_progress"].(float64) != 1 {
		t.Fatalf("expected 1 in_progress, got %v", projCounts["in_progress"])
	}
	if projCounts["review"].(float64) != 1 {
		t.Fatalf("expected 1 review, got %v", projCounts["review"])
	}
}

func TestBoardEndpoint(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	// Create 2 tasks
	createTestTask(t, s, projID, "Task A")
	createTestTask(t, s, projID, "Task B")

	req := testRequest("GET", "/api/projects/"+projID+"/board", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	board := mustJSON[map[string]interface{}](t, rr.Body)
	tasks := board["tasks"].([]interface{})
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks on board, got %d", len(tasks))
	}
}

func TestClearDone(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	// Create tasks in different statuses
	backlogTask := createTestTask(t, s, projID, "Backlog task")
	doneTask := createTestTask(t, s, projID, "Done task")

	// Move one to done
	moveReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+doneTask+"/move", map[string]interface{}{
		"status": "done",
	})
	moveRR := testResponseAuth(t, s, moveReq)
	if moveRR.Code != http.StatusOK {
		t.Fatalf("move to done failed: %d %s", moveRR.Code, moveRR.Body.String())
	}

	// Clear done tasks
	req := testRequest("DELETE", "/api/projects/"+projID+"/tasks/done", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify done task is gone but backlog task remains
	listReq := testRequest("GET", "/api/projects/"+projID+"/tasks", nil)
	listRR := testResponseAuth(t, s, listReq)
	tasks := mustJSON[[]map[string]interface{}](t, listRR.Body)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task after clear done, got %d", len(tasks))
	}
	if tasks[0]["id"] != backlogTask {
		t.Fatalf("wrong task remaining: %s", tasks[0]["id"])
	}
}
