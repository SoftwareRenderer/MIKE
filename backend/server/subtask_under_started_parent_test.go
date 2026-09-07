package server

import (
	"net/http"
	"testing"
)

// What happens after a started task accepts a subtask: which column the new card lands
// in, and what Clear Done must not delete with it.

// putStatus parks a task in a column without running it, so the assertions are about the
// create rules and not about an executor with no LLM to talk to.
func putStatus(t *testing.T, s *Server, projectID, taskID, status, executionState string) {
	t.Helper()
	updateTask(t, s, projectID, taskID, map[string]any{
		"status":          status,
		"execution_state": executionState,
	})
}

func getTaskField(t *testing.T, s *Server, projectID, taskID, field string) any {
	t.Helper()
	return getTask(t, s, projectID, taskID)[field]
}

// TestSubtaskUnderStartedParentLandsPending: a subtask of a started task is created
// in_progress/idle (Pending) beneath its parent; every other parent gets a backlog child.
func TestSubtaskUnderStartedParentLandsPending(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	cases := []struct {
		name         string
		parentStatus string
		wantStatus   string
		wantState    string
	}{
		{"backlog parent", "backlog", "backlog", ""},
		{"in progress parent", "in_progress", "in_progress", "idle"},
		{"review parent", "review", "backlog", ""},
		{"done parent", "done", "backlog", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parentID := createTestTask(t, s, projID, "parent-"+tc.name)
			putStatus(t, s, projID, parentID, tc.parentStatus, "idle")

			childID := createTestSubtask(t, s, projID, parentID, "child-"+tc.name)

			if got := getTaskField(t, s, projID, childID, "status"); got != tc.wantStatus {
				t.Errorf("child status = %v, want %q", got, tc.wantStatus)
			}
			if got := getTaskField(t, s, projID, childID, "execution_state"); got != tc.wantState {
				t.Errorf("child execution_state = %v, want %q", got, tc.wantState)
			}
			if got := getTaskField(t, s, projID, childID, "parent_task_id"); got != parentID {
				t.Errorf("child parent_task_id = %v, want %s", got, parentID)
			}
		})
	}
}

// TestClearDoneKeepsDoneParentWithUnfinishedSubtask: deleting a Done task deletes its
// subtree, so a Done parent holding unfinished work is left behind while the rest of the
// column clears.
func TestClearDoneKeepsDoneParentWithUnfinishedSubtask(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	parentID := createTestTask(t, s, projID, "parent-done-with-work")
	putStatus(t, s, projID, parentID, "done", "completed")
	childID := createTestSubtask(t, s, projID, parentID, "sub-still-to-do")

	plainID := createTestTask(t, s, projID, "plain-done")
	moveTask(t, s, projID, plainID, "done")

	req := testRequest("DELETE", "/api/projects/"+projID+"/tasks/done", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("clear done: %d %s", rr.Code, rr.Body.String())
	}
	body := mustJSON[map[string]any](t, rr.Body)
	if body["deleted"] != float64(1) {
		t.Errorf("deleted = %v, want 1 (the parent with unfinished work stays)", body["deleted"])
	}

	for _, id := range []string{parentID, childID} {
		got := testRequest("GET", "/api/projects/"+projID+"/tasks/"+id, nil)
		if rr := testResponseAuth(t, s, got); rr.Code != http.StatusOK {
			t.Errorf("task %s was cleared: %d %s", id, rr.Code, rr.Body.String())
		}
	}
	req = testRequest("GET", "/api/projects/"+projID+"/tasks/"+plainID, nil)
	if rr := testResponseAuth(t, s, req); rr.Code != http.StatusNotFound {
		t.Errorf("plain done task still present after clear done: %d", rr.Code)
	}
}
