package server

import (
	"net/http"
	"testing"
)

func TestDependencyCreate(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "Parent task")
	childID := createTestTask(t, s, projID, "Child task")

	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+parentID+"/dependencies", map[string]any{
		"child_task_id": childID,
		"auto_start":    true,
	})
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	result := mustJSON[map[string]any](t, rr.Body)
	if result["parent_task_id"] != parentID {
		t.Fatalf("wrong parent_task_id: %s", result["parent_task_id"])
	}
	if result["child_task_id"] != childID {
		t.Fatalf("wrong child_task_id: %s", result["child_task_id"])
	}
}

func TestDependencyList(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "Parent task")
	child1ID := createTestTask(t, s, projID, "Child 1")
	child2ID := createTestTask(t, s, projID, "Child 2")

	// Create dependencies
	for _, childID := range []string{child1ID, child2ID} {
		req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+parentID+"/dependencies", map[string]any{
			"child_task_id": childID,
		})
		testResponseAuth(t, s, req)
	}

	// List dependents of parent
	req := testRequest("GET", "/api/projects/"+projID+"/tasks/"+parentID+"/dependencies", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	deps := mustJSON[[]map[string]any](t, rr.Body)
	if len(deps) != 2 {
		t.Fatalf("expected 2 dependents, got %d", len(deps))
	}
}

func TestDependencyListDependents(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "Parent task")
	childID := createTestTask(t, s, projID, "Child task")

	// Create dependency
	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+parentID+"/dependencies", map[string]any{
		"child_task_id": childID,
	})
	testResponseAuth(t, s, req)

	// List dependencies of child (tasks this child depends on)
	req = testRequest("GET", "/api/projects/"+projID+"/tasks/"+childID+"/dependents", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	deps := mustJSON[[]map[string]any](t, rr.Body)
	if len(deps) != 1 {
		t.Fatalf("expected 1 dependency, got %d", len(deps))
	}
	if deps[0]["parent_task_id"] != parentID {
		t.Fatalf("wrong parent_task_id: %s", deps[0]["parent_task_id"])
	}
}

func TestDependencyDelete(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "Parent task")
	childID := createTestTask(t, s, projID, "Child task")

	// Create dependency
	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+parentID+"/dependencies", map[string]any{
		"child_task_id": childID,
	})
	testResponseAuth(t, s, req)

	// Delete dependency
	req = testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+parentID+"/dependencies/"+childID, nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify dependency is gone
	req = testRequest("GET", "/api/projects/"+projID+"/tasks/"+parentID+"/dependencies", nil)
	rr = testResponseAuth(t, s, req)
	deps := mustJSON[[]map[string]any](t, rr.Body)
	if len(deps) != 0 {
		t.Fatalf("expected 0 dependents after delete, got %d", len(deps))
	}
}

func TestDependencyCreateInvalidJSON(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "Parent task")

	req := testRequest("POST", "/api/projects/"+projID+"/tasks/"+parentID+"/dependencies", "not json")
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}
