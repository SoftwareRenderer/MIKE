package server

import (
	"net/http"
	"testing"
)

func TestProjectCreate(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	initGitRepo(t, dir)

	req := testRequest("POST", "/api/projects", map[string]any{
		"name": "My Project",
		"path": dir,
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)
	if result["name"] != "My Project" {
		t.Fatalf("unexpected name: %s", result["name"])
	}
	if result["id"] == "" || result["id"] == nil {
		t.Fatal("project ID should not be empty")
	}
}

func TestProjectCreateSandboxEnabled(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	initGitRepo(t, dir)

	req := testRequest("POST", "/api/projects", map[string]any{
		"name":                      "Sandboxed Project",
		"path":                      dir,
		"sandbox_enabled":           true,
		"sandbox_network_isolation": false,
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)
	if result["sandbox_enabled"] != true {
		t.Fatalf("expected sandbox_enabled to be true, got %v", result["sandbox_enabled"])
	}
	if result["sandbox_network_isolation"] == true {
		t.Fatal("expected sandbox_network_isolation to be false")
	}

	// Verify via GET
	projID := result["id"].(string)
	req = testRequest("GET", "/api/projects/"+projID, nil)
	rr = testResponseAuth(t, s, req)
	result = mustJSON[map[string]any](t, rr.Body)
	if result["sandbox_enabled"] != true {
		t.Fatalf("GET: expected sandbox_enabled to be true, got %v", result["sandbox_enabled"])
	}
}

func TestProjectCreateSandboxEnabledMissingNetworkIsolation(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	initGitRepo(t, dir)

	// sandbox_enabled=true without sandbox_network_isolation should return 400
	req := testRequest("POST", "/api/projects", map[string]any{
		"name":            "Sandboxed Project",
		"path":            dir,
		"sandbox_enabled": true,
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)
	msg, ok := result["error"].(string)
	if !ok || msg == "" {
		t.Fatal("expected error message in response")
	}

	// sandbox_enabled=false without sandbox_network_isolation should succeed
	req = testRequest("POST", "/api/projects", map[string]any{
		"name":            "Non-Sandboxed Project",
		"path":            dir,
		"sandbox_enabled": false,
	})
	rr = testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201 for non-sandboxed project, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestProjectCreateDuplicatePath(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	initGitRepo(t, dir)

	// Create first project
	req := testRequest("POST", "/api/projects", map[string]any{
		"name": "Project A",
		"path": dir,
	})
	testResponseAuth(t, s, req)

	// Create second project with same path — should fail (500 for UNIQUE constraint violation)
	req = testRequest("POST", "/api/projects", map[string]any{
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
		req := testRequest("POST", "/api/projects", map[string]any{
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

	projects := mustJSON[[]map[string]any](t, rr.Body)
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

	result := mustJSON[map[string]any](t, rr.Body)
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

	req := testRequest("PUT", "/api/projects/"+projID, map[string]any{
		"name": "Renamed Project",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)
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
	result := mustJSON[map[string]any](t, rr.Body)
	if result["sandbox_enabled"] == true {
		t.Fatal("expected sandbox_enabled to be false by default")
	}
	if result["sandbox_network_isolation"] == true {
		t.Fatal("expected sandbox_network_isolation to be false by default")
	}

	// Enable sandbox and network isolation
	req = testRequest("PUT", "/api/projects/"+projID, map[string]any{
		"sandbox_enabled":           true,
		"sandbox_network_isolation": true,
	})
	rr = testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	result = mustJSON[map[string]any](t, rr.Body)
	if result["sandbox_enabled"] != true {
		t.Fatal("expected sandbox_enabled to be true")
	}
	if result["sandbox_network_isolation"] != true {
		t.Fatal("expected sandbox_network_isolation to be true")
	}

	// Disable only network isolation (sandbox stays on)
	req = testRequest("PUT", "/api/projects/"+projID, map[string]any{
		"sandbox_network_isolation": false,
	})
	rr = testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	result = mustJSON[map[string]any](t, rr.Body)
	if result["sandbox_enabled"] != true {
		t.Fatal("expected sandbox_enabled to still be true")
	}
	if result["sandbox_network_isolation"] != false {
		t.Fatal("expected sandbox_network_isolation to be false")
	}
}

func TestProjectToggleSandboxWithTasks(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	// Create tasks (backlog, not running)
	createTestTask(t, s, projID, "Task 1")
	createTestTask(t, s, projID, "Task 2")

	// Enable sandbox
	req := testRequest("PUT", "/api/projects/"+projID, map[string]any{
		"sandbox_enabled":           true,
		"sandbox_network_isolation": true,
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("enable sandbox: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	result := mustJSON[map[string]any](t, rr.Body)
	if result["sandbox_enabled"] != true {
		t.Fatal("expected sandbox_enabled to be true")
	}

	// Disable sandbox (should succeed — no tasks running)
	req = testRequest("PUT", "/api/projects/"+projID, map[string]any{
		"sandbox_enabled": false,
	})
	rr = testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("disable sandbox: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	result = mustJSON[map[string]any](t, rr.Body)
	if result["sandbox_enabled"] != false {
		t.Fatal("expected sandbox_enabled to be false")
	}

	// Re-enable sandbox (toggle back)
	req = testRequest("PUT", "/api/projects/"+projID, map[string]any{
		"sandbox_enabled":           true,
		"sandbox_network_isolation": true,
	})
	rr = testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("re-enable sandbox: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	result = mustJSON[map[string]any](t, rr.Body)
	if result["sandbox_enabled"] != true {
		t.Fatal("expected sandbox_enabled to be true after re-enable")
	}
}

// Test: sandbox setting change is blocked when tasks are actively running
func TestProjectSandboxChangeBlockedWhenTasksRunning(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	// Create a task with LLM config so the executor can start
	taskID := createTestTaskWithLLM(t, s, projID, "Running task")

	// Start the task via the move endpoint (triggers executor)
	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/move", map[string]any{
		"status": "in_progress",
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("move to in_progress: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Try to change sandbox setting while task is running — should be blocked
	req = testRequest("PUT", "/api/projects/"+projID, map[string]any{
		"sandbox_enabled": true,
	})
	rr = testResponseAuth(t, s, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict when tasks running, got %d: %s", rr.Code, rr.Body.String())
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

	// Move one to in_progress (needs LLM config so auto-start succeeds)
	taskID := createTestTaskWithLLM(t, s, projID, "In progress task")
	moveReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/move", map[string]any{
		"status": "in_progress",
	})
	moveRR := testResponseAuth(t, s, moveReq)
	if moveRR.Code != http.StatusOK {
		t.Fatalf("move to in_progress failed: %d %s", moveRR.Code, moveRR.Body.String())
	}

	// Move one to review
	reviewTaskID := createTestTask(t, s, projID, "Review task")
	moveReq2 := testRequest("POST", "/api/projects/"+projID+"/tasks/"+reviewTaskID+"/move", map[string]any{
		"status": "review",
	})
	moveRR2 := testResponseAuth(t, s, moveReq2)
	if moveRR2.Code != http.StatusOK {
		t.Fatalf("move to review failed: %d %s", moveRR2.Code, moveRR2.Body.String())
	}

	// Debug: list tasks and their statuses
	listReq := testRequest("GET", "/api/projects/"+projID+"/tasks", nil)
	listRR := testResponseAuth(t, s, listReq)
	tasks := mustJSON[[]map[string]any](t, listRR.Body)
	for _, task := range tasks {
		t.Logf("task %s: status=%s", task["id"], task["status"])
	}

	req := testRequest("GET", "/api/projects/counts", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	counts := mustJSON[map[string]any](t, rr.Body)
	projCounts, ok := counts[projID].(map[string]any)
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

	board := mustJSON[map[string]any](t, rr.Body)
	tasks := board["tasks"].([]any)
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
	moveReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+doneTask+"/move", map[string]any{
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
	tasks := mustJSON[[]map[string]any](t, listRR.Body)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task after clear done, got %d", len(tasks))
	}
	if tasks[0]["id"] != backlogTask {
		t.Fatalf("wrong task remaining: %s", tasks[0]["id"])
	}
}

func TestProjectCreateWithMemoryLimit(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	initGitRepo(t, dir)

	req := testRequest("POST", "/api/projects", map[string]any{
		"name":                      "Memory Limit Project",
		"path":                      dir,
		"sandbox_enabled":           true,
		"sandbox_network_isolation": false,
		"sandbox_memory_limit":      2048,
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)
	if result["sandbox_memory_limit"].(float64) != 2048 {
		t.Fatalf("expected sandbox_memory_limit 2048, got %v", result["sandbox_memory_limit"])
	}

	// Verify via GET
	projID := result["id"].(string)
	req = testRequest("GET", "/api/projects/"+projID, nil)
	rr = testResponseAuth(t, s, req)
	result = mustJSON[map[string]any](t, rr.Body)
	if result["sandbox_memory_limit"].(float64) != 2048 {
		t.Fatalf("GET: expected sandbox_memory_limit 2048, got %v", result["sandbox_memory_limit"])
	}
}

func TestProjectUpdateSandboxMemoryLimit(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	initGitRepo(t, dir)

	// Create project with default memory limit
	req := testRequest("POST", "/api/projects", map[string]any{
		"name":                      "Memory Update Project",
		"path":                      dir,
		"sandbox_enabled":           true,
		"sandbox_network_isolation": false,
	})
	rr := testResponseAuth(t, s, req)
	result := mustJSON[map[string]any](t, rr.Body)
	projID := result["id"].(string)

	// Default should be 1024
	if result["sandbox_memory_limit"].(float64) != 1024 {
		t.Fatalf("expected default sandbox_memory_limit 1024, got %v", result["sandbox_memory_limit"])
	}

	// Update memory limit
	req = testRequest("PUT", "/api/projects/"+projID, map[string]any{
		"sandbox_memory_limit": 4096,
	})
	rr = testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	result = mustJSON[map[string]any](t, rr.Body)
	if result["sandbox_memory_limit"].(float64) != 4096 {
		t.Fatalf("expected sandbox_memory_limit 4096, got %v", result["sandbox_memory_limit"])
	}

	// Update to 0 (no limit)
	req = testRequest("PUT", "/api/projects/"+projID, map[string]any{
		"sandbox_memory_limit": 0,
	})
	rr = testResponseAuth(t, s, req)
	result = mustJSON[map[string]any](t, rr.Body)
	if result["sandbox_memory_limit"].(float64) != 0 {
		t.Fatalf("expected sandbox_memory_limit 0, got %v", result["sandbox_memory_limit"])
	}
}

func TestProjectUpdateSandboxForwardPorts(t *testing.T) {
	s := testServer(t)

	// Create project
	projID := createTestProject(t, s)

	// Update forward ports (container ports only, host ports are auto-assigned)
	req := testRequest("PUT", "/api/projects/"+projID, map[string]any{
		"sandbox_forward_ports": []map[string]any{
			{"container_port": 80},
			{"container_port": 3000},
		},
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	result := mustJSON[map[string]any](t, rr.Body)
	ports, ok := result["forward_ports"].([]any)
	if !ok {
		t.Fatalf("expected forward_ports to be array, got %T", result["forward_ports"])
	}
	if len(ports) != 2 {
		t.Fatalf("expected 2 forward ports, got %d", len(ports))
	}

	// Verify first port (only container_port)
	p0 := ports[0].(map[string]any)
	if p0["container_port"].(float64) != 80 {
		t.Fatalf("expected container port 80, got %v", p0["container_port"])
	}

	// Update to single port
	req = testRequest("PUT", "/api/projects/"+projID, map[string]any{
		"sandbox_forward_ports": []map[string]any{
			{"container_port": 8080},
		},
	})
	rr = testResponseAuth(t, s, req)
	result = mustJSON[map[string]any](t, rr.Body)
	ports = result["forward_ports"].([]any)
	if len(ports) != 1 {
		t.Fatalf("expected 1 forward port, got %d", len(ports))
	}

	// Clear forward ports
	req = testRequest("PUT", "/api/projects/"+projID, map[string]any{
		"sandbox_forward_ports": []map[string]any{},
	})
	rr = testResponseAuth(t, s, req)
	result = mustJSON[map[string]any](t, rr.Body)
	// forward_ports is omitted when empty (omitempty)
	if result["forward_ports"] != nil {
		ports := result["forward_ports"].([]any)
		if len(ports) != 0 {
			t.Fatalf("expected 0 forward ports, got %d", len(ports))
		}
	}
}

func TestProjectUpdateSandboxMounts(t *testing.T) {
	s := testServer(t)

	// Create project
	projID := createTestProject(t, s)

	// Update mounts
	req := testRequest("PUT", "/api/projects/"+projID, map[string]any{
		"sandbox_mounts": []map[string]any{
			{"host_path": "/data/cache", "container_path": "/mnt/cache", "mode": "rw"},
			{"host_path": "/data/config", "container_path": "/mnt/config", "mode": "ro"},
		},
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	result := mustJSON[map[string]any](t, rr.Body)
	mounts, ok := result["sandbox_mounts"].([]any)
	if !ok {
		t.Fatalf("expected sandbox_mounts to be array, got %T", result["sandbox_mounts"])
	}
	if len(mounts) != 2 {
		t.Fatalf("expected 2 mounts, got %d", len(mounts))
	}

	// Verify first mount
	m0 := mounts[0].(map[string]any)
	if m0["host_path"] != "/data/cache" {
		t.Fatalf("expected host_path /data/cache, got %v", m0["host_path"])
	}
	if m0["container_path"] != "/mnt/cache" {
		t.Fatalf("expected container_path /mnt/cache, got %v", m0["container_path"])
	}
	if m0["mode"] != "rw" {
		t.Fatalf("expected mode rw, got %v", m0["mode"])
	}

	// Update to single mount
	req = testRequest("PUT", "/api/projects/"+projID, map[string]any{
		"sandbox_mounts": []map[string]any{
			{"host_path": "/logs", "container_path": "/var/log/app", "mode": "rw"},
		},
	})
	rr = testResponseAuth(t, s, req)
	result = mustJSON[map[string]any](t, rr.Body)
	mounts = result["sandbox_mounts"].([]any)
	if len(mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(mounts))
	}

	// Clear mounts
	req = testRequest("PUT", "/api/projects/"+projID, map[string]any{
		"sandbox_mounts": []map[string]any{},
	})
	rr = testResponseAuth(t, s, req)
	result = mustJSON[map[string]any](t, rr.Body)
	// sandbox_mounts is omitted when empty (omitempty)
	if result["sandbox_mounts"] != nil {
		mounts := result["sandbox_mounts"].([]any)
		if len(mounts) != 0 {
			t.Fatalf("expected 0 mounts, got %d", len(mounts))
		}
	}
}

func TestProjectUpdateSandboxEnvs(t *testing.T) {
	s := testServer(t)

	// Create project
	projID := createTestProject(t, s)

	// Update envs
	req := testRequest("PUT", "/api/projects/"+projID, map[string]any{
		"sandbox_envs": []map[string]any{
			{"key": "DATABASE_URL", "value": "postgres://localhost/db"},
			{"key": "API_KEY", "value": "secret123"},
			{"key": "EMPTY_VAR", "value": ""},
		},
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	result := mustJSON[map[string]any](t, rr.Body)
	envs, ok := result["sandbox_envs"].([]any)
	if !ok {
		t.Fatalf("expected sandbox_envs to be array, got %T", result["sandbox_envs"])
	}
	if len(envs) != 3 {
		t.Fatalf("expected 3 envs, got %d", len(envs))
	}

	// Verify first env
	e0 := envs[0].(map[string]any)
	if e0["key"] != "DATABASE_URL" {
		t.Fatalf("expected key DATABASE_URL, got %v", e0["key"])
	}
	if e0["value"] != "postgres://localhost/db" {
		t.Fatalf("expected value postgres://localhost/db, got %v", e0["value"])
	}

	// Update to single env
	req = testRequest("PUT", "/api/projects/"+projID, map[string]any{
		"sandbox_envs": []map[string]any{
			{"key": "NEW_VAR", "value": "new_value"},
		},
	})
	rr = testResponseAuth(t, s, req)
	result = mustJSON[map[string]any](t, rr.Body)
	envs = result["sandbox_envs"].([]any)
	if len(envs) != 1 {
		t.Fatalf("expected 1 env, got %d", len(envs))
	}

	// Clear envs
	req = testRequest("PUT", "/api/projects/"+projID, map[string]any{
		"sandbox_envs": []map[string]any{},
	})
	rr = testResponseAuth(t, s, req)
	result = mustJSON[map[string]any](t, rr.Body)
	// sandbox_envs is omitted when empty (omitempty)
	if result["sandbox_envs"] != nil {
		envs := result["sandbox_envs"].([]any)
		if len(envs) != 0 {
			t.Fatalf("expected 0 envs, got %d", len(envs))
		}
	}
}
