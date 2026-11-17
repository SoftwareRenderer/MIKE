package server

import (
	"bufio"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"mike/backend/git"
	"mike/backend/sandbox"
	"mike/backend/testutil/sandboxcheck"
	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

// The title a created task carries is the one the person typed or, when they typed none, the prompt's
// first line — it is the only name the board has to show.
func TestTaskCreateTitlesTheTask(t *testing.T) {
	rows := []struct {
		name string
		body map[string]any
		want string
	}{
		{"a prompt names it", map[string]any{"prompt": "Add login feature"}, "Add login feature"},
		{"its first line, when the prompt has more", map[string]any{
			"prompt": "Add login feature\nwith oauth and a session store"}, "Add login feature"},
		{"a typed title wins", map[string]any{
			"prompt": "Add login feature", "title": "Custom Title"}, "Custom Title"},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			projID := createTestProject(t, s)

			rr := testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks", tc.body))
			if rr.Code != http.StatusCreated {
				t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
			}
			result := mustJSON[map[string]any](t, rr.Body)
			if result["title"] != tc.want {
				t.Fatalf("title = %v, want %q", result["title"], tc.want)
			}
			if result["id"] == "" || result["id"] == nil {
				t.Fatal("task ID should not be empty")
			}
			if result["status"] != "backlog" {
				t.Fatalf("expected status=backlog, got %s", result["status"])
			}
			if result["project_id"] != projID {
				t.Fatalf("wrong project_id: %s", result["project_id"])
			}
		})
	}
}

// TestTaskRequestErrors is the task API's rejection set: a body that is not JSON, a task
// that does not exist, and a subtask pointing at a parent that does not exist. Each must be
// refused with a status the SPA can branch on — in particular, a create that silently
// accepts a dangling parent_task_id would produce a subtask no one can reach.
func TestTaskRequestErrors(t *testing.T) {
	cases := []struct {
		name   string
		method string
		// path builds the route once the project id is known.
		path func(projID string) string
		body any
		want int
	}{
		{
			name: "malformed JSON body", method: "POST",
			path: func(p string) string { return "/api/projects/" + p + "/tasks" },
			body: "not json", want: http.StatusBadRequest,
		},
		{
			name: "unknown task", method: "GET",
			path: func(p string) string { return "/api/projects/" + p + "/tasks/nonexistent" },
			want: http.StatusNotFound,
		},
		{
			name: "subtask with a missing parent", method: "POST",
			path: func(p string) string { return "/api/projects/" + p + "/tasks" },
			body: map[string]any{"prompt": "Child with missing parent", "parent_task_id": "nonexistent-parent"},
			want: http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			projID := createTestProject(t, s)

			rr := testResponseAuth(t, s, testRequest(tc.method, tc.path(projID), tc.body))
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d; body: %s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

// TestTaskUpdateRefValidation covers the HTTP contract for the git-facing task fields:
// PUT must reject an option-looking ref, accept a legitimate one, and ECHO the stored value
// back in the response body (which is what the SPA re-renders). The exhaustive rejection
// matrix for these fields lives with the validator itself, in
// api.TestTaskHandler_PUT_RefValidation — the row set here is deliberately small and
// adds the response-shape check that layer cannot make.
func TestTaskUpdateRefValidation(t *testing.T) {
	cases := []struct {
		field string
		value string
		want  int
	}{
		{field: "base_ref", value: "-f", want: http.StatusBadRequest},
		{field: "base_ref", value: "--dry-run", want: http.StatusBadRequest},
		{field: "base_ref", value: "main", want: http.StatusOK},
		{field: "base_ref", value: "origin/feature-branch", want: http.StatusOK},
		{field: "branch_tip", value: "-f", want: http.StatusBadRequest},
		{field: "branch_tip", value: "-C/tmp/evil", want: http.StatusBadRequest},
		{field: "branch_tip", value: "feature-branch", want: http.StatusOK},
		{field: "branch_tip", value: "task/frost-amber-quartz", want: http.StatusOK},
	}

	// One server per field: the accepted rows mutate the task they target.
	for _, field := range []string{"base_ref", "branch_tip"} {
		s := testServer(t)
		projID := createTestProject(t, s)
		taskID := createTestTask(t, s, projID, field+" validation task")

		for _, tc := range cases {
			if tc.field != field {
				continue
			}
			t.Run(tc.field+"/"+strings.TrimLeft(tc.value, "-"), func(t *testing.T) {
				rr := testResponseAuth(t, s, testRequest("PUT",
					"/api/projects/"+projID+"/tasks/"+taskID,
					map[string]any{tc.field: tc.value}))
				if rr.Code != tc.want {
					t.Fatalf("%s=%q: status = %d, want %d; body: %s",
						tc.field, tc.value, rr.Code, tc.want, rr.Body.String())
				}
				if tc.want != http.StatusOK {
					return
				}
				result := mustJSON[map[string]any](t, rr.Body)
				if result[tc.field] != tc.value {
					t.Errorf("response echoes %s=%v, want %q", tc.field, result[tc.field], tc.value)
				}
			})
		}
	}
}

// A start lands the card in its column whichever door the click came through: the move endpoint
// auto-starts, the start endpoint is the board's play button. Both leave the row in_progress.
func TestStartedTaskLandsInProgress(t *testing.T) {
	rows := []struct {
		name  string
		start func(t *testing.T, s *Server, projID, taskID string) map[string]any
	}{
		{"moved to in_progress", func(t *testing.T, s *Server, projID, taskID string) map[string]any {
			rr := testResponseAuth(t, s, testRequest("POST",
				"/api/projects/"+projID+"/tasks/"+taskID+"/move", map[string]any{"status": "in_progress"}))
			if rr.Code != http.StatusOK {
				t.Fatalf("move: %d %s", rr.Code, rr.Body.String())
			}
			return mustJSON[map[string]any](t, rr.Body)
		}},
		{"started", func(t *testing.T, s *Server, projID, taskID string) map[string]any {
			rr := testResponseAuth(t, s, testRequest("POST",
				"/api/projects/"+projID+"/tasks/"+taskID+"/start", nil))
			if rr.Code != http.StatusOK {
				t.Fatalf("start: %d %s", rr.Code, rr.Body.String())
			}
			return mustJSON[map[string]any](t, rr.Body)
		}},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			projID := createTestProject(t, s)
			taskID := createTestTaskWithLLM(t, s, projID, "start-me")

			// A task is born in the backlog, or the start answered before it moved.
			if got := getTaskStatus(t, s, projID, taskID); got != "backlog" {
				t.Fatalf("expected backlog, got %s", got)
			}
			result := tc.start(t, s, projID, taskID)
			if result["status"] != "in_progress" {
				t.Fatalf("the answer says status=%s, want in_progress", result["status"])
			}
			if got := getTaskStatus(t, s, projID, taskID); got != "in_progress" {
				t.Fatalf("the row says %s, want in_progress", got)
			}
		})
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

// A parent that has started keeps accepting children: its column decides where the new card lands,
// never whether it may exist. The refusal set (a parent that does not exist, a body that is not JSON)
// is TestTaskRequestErrors.
func TestTaskParentValidation(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)

	parentID := createTestTask(t, s, projID, "Parent task")

	// Create a child with a valid parent
	if id := createTestSubtask(t, s, projID, parentID, "Child task"); id == "" {
		t.Fatal("expected a child id")
	}

	for _, status := range []string{"in_progress", "review", "done"} {
		updateTask(t, s, projID, parentID, map[string]any{"status": status, "execution_state": "idle"})
		child := map[string]any{"prompt": "Child of a " + status + " parent", "parent_task_id": parentID}
		req := testRequest("POST", "/api/projects/"+projID+"/tasks", child)
		rr := testResponseAuth(t, s, req)
		if rr.Code != http.StatusCreated {
			t.Fatalf("parent in %s: expected 201, got %d: %s", status, rr.Code, rr.Body.String())
		}
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

// The two readers below are one GET (getTask) plus the column the caller is asking about.

// Helper: get a task by ID and return its status
func getTaskStatus(t *testing.T, s *Server, projectID string, taskID string) string {
	t.Helper()
	return getTask(t, s, projectID, taskID)["status"].(string)
}

// Helper: get a task by ID and return its execution_state
func getTaskExecutionState(t *testing.T, s *Server, projectID string, taskID string) string {
	t.Helper()
	if ss, ok := getTask(t, s, projectID, taskID)["execution_state"].(string); ok {
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

// What a subtask of a started parent may and may not do with its own column: forward moves are
// always open, a move back to the backlog is refused while an ancestor is working, and the parent's
// own move never drags a card that is waiting for a person.
func TestSubtaskMovesUnderAStartedParent(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "parent-task")
	// The child exists first, so the parent has work to wait for instead of starting a turn.
	subID := createTestSubtask(t, s, projID, parentID, "subtask")

	moveTask(t, s, projID, subID, "review")

	// The parent starts but the review subtask stays put (human gate); the parent waits
	// started-but-pending.
	moveTask(t, s, projID, parentID, "in_progress")
	if got := getTaskStatus(t, s, projID, subID); got != "review" {
		t.Fatalf("review subtask must stay in review, got %s", got)
	}
	if got := getTaskStatus(t, s, projID, parentID); got != "in_progress" {
		t.Fatalf("the parent did not start: %s", got)
	}

	// Backlog is the one refusal: it would pull work out from under the ancestor that owns it.
	rr := testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks/"+subID+"/move",
		map[string]any{"status": "backlog"}))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if got := getTaskStatus(t, s, projID, subID); got != "review" {
		t.Fatalf("expected review (blocked), got %s", got)
	}

	// Forward, both of them, with the ancestor still in progress.
	moveTask(t, s, projID, subID, "done")
	if got := getTaskStatus(t, s, projID, subID); got != "done" {
		t.Fatalf("expected done, got %s", got)
	}
	moved := createTestSubtask(t, s, projID, parentID, "subtask-two")
	moveTask(t, s, projID, moved, "review")
	if got := getTaskStatus(t, s, projID, moved); got != "review" {
		t.Fatalf("expected review, got %s", got)
	}
}

// A move belongs to the row it names: neither the parent above it nor the siblings beside it follow,
// in either direction and whatever column they hold.
func TestMoveSubtaskMovesOnlyItself(t *testing.T) {
	t.Run("a first move leaves the backlog neighbours", func(t *testing.T) {
		s := testServer(t)
		projID := createTestProject(t, s)
		parentID := createTestTask(t, s, projID, "parent-isolated")
		subA := createTestSubtask(t, s, projID, parentID, "sub-a")
		subB := createTestSubtask(t, s, projID, parentID, "sub-b")

		moveTask(t, s, projID, subA, "review")

		if getTaskStatus(t, s, projID, subA) != "review" {
			t.Fatal("subA should be review")
		}
		if got := getTaskStatus(t, s, projID, parentID); got != "backlog" {
			t.Fatalf("parent should stay in backlog, got %s", got)
		}
		if got := getTaskStatus(t, s, projID, subB); got != "backlog" {
			t.Fatalf("subB should stay in backlog (sibling isolation), got %s", got)
		}
	})

	t.Run("moving back to in_progress leaves a finished sibling", func(t *testing.T) {
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

		moveTask(t, s, projID, subA, "review")
		moveTask(t, s, projID, subB, "done")

		moveTask(t, s, projID, subA, "in_progress")

		if got := getTaskStatus(t, s, projID, subA); got != "in_progress" {
			t.Fatalf("subA expected in_progress, got %s", got)
		}
		if got := getTaskStatus(t, s, projID, subB); got != "done" {
			t.Fatalf("subB expected done (sibling isolation), got %s", got)
		}
	})
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

// Done is the column that carries an outcome: entering it completes the row, leaving it clears the
// completion so the card can run again.
func TestMoveToAndFromDoneSetsTheExecutionState(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "done-state")

	moveTask(t, s, projID, taskID, "done")
	if got := getTaskExecutionState(t, s, projID, taskID); got != "completed" {
		t.Fatalf("expected completed, got %s", got)
	}

	moveTask(t, s, projID, taskID, "review")
	if got := getTaskExecutionState(t, s, projID, taskID); got != "idle" {
		t.Fatalf("expected idle after moving away from done, got %s", got)
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

// Commit-and-done is the click that ends a run: the row leaves for Done and its execution state
// records the outcome, whoever wrote the message. No worktree exists here, so this pins the column
// and the state; the commit's contents are TestCommitAndDoneIncludesManualEditsAfterStaging.
func TestCommitAndDoneMovesTheTaskToDone(t *testing.T) {
	rows := []struct {
		name   string
		review bool
		body   map[string]any
	}{
		{"from review with the generated message", true, nil},
		{"from review with the message the person wrote", true, map[string]any{"commit_message": "My custom commit message"}},
		{"from backlog with no worktree", false, nil},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			projID := createTestProject(t, s)
			taskID := createTestTask(t, s, projID, "commit-done")

			if tc.review {
				moveTask(t, s, projID, taskID, "review")
			}
			rr := testResponseAuth(t, s, testRequest("POST",
				"/api/projects/"+projID+"/tasks/"+taskID+"/commit", tc.body))
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
		})
	}
}

// TestCommitPreviewStream is the commit-message preview's whole contract: one metadata frame naming
// the author and merge target, then the generated message. The Commit dialog opens on the metadata
// and fills the message as it streams, so a stream that never names them is an empty dialog.
func TestCommitPreviewStream(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "preview-test")

	// Move task to review (required for commit operations)
	moveReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/move", map[string]any{"status": "review"})
	if moveRR := testResponseAuth(t, s, moveReq); moveRR.Code != http.StatusOK {
		t.Fatalf("move to review: %d %s", moveRR.Code, moveRR.Body.String())
	}

	req := testRequest("GET", "/api/projects/"+projID+"/tasks/"+taskID+"/commit-preview/stream", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("commit-preview/stream: %d %s", rr.Code, rr.Body.String())
	}

	var metadata map[string]any
	var message string
	for _, ev := range readNamedSSE(t, rr.Body) {
		switch ev.event {
		case "metadata":
			if err := json.Unmarshal([]byte(ev.data), &metadata); err != nil {
				t.Fatalf("metadata frame %q: %v", ev.data, err)
			}
		case "done":
			var body map[string]any
			if err := json.Unmarshal([]byte(ev.data), &body); err != nil {
				t.Fatalf("done frame %q: %v", ev.data, err)
			}
			message, _ = body["content"].(string)
		case "error":
			t.Fatalf("commit-preview/stream reported an error: %s", ev.data)
		}
	}

	if metadata == nil {
		t.Fatal("no metadata event — the dialog opens on author_name, author_email and merge_target")
	}
	// The dialog reads all three keys before the first token. resolveGitAuthor always answers an
	// identity, so the author pair is non-empty; merge_target is empty until the task has a branch to
	// merge into, so it is held to presence.
	for _, key := range []string{"author_name", "author_email"} {
		if v, ok := metadata[key].(string); !ok || v == "" {
			t.Errorf("metadata[%s] is not a non-empty string: %v", key, metadata)
		}
	}
	if _, ok := metadata["merge_target"].(string); !ok {
		t.Errorf("metadata has no merge_target key: %v", metadata)
	}
	// The generated message should contain the task title
	if !strings.Contains(message, "preview-test") {
		t.Fatalf("commit message should contain task title, got: %q", message)
	}
}

// sseFrame is one SSE frame: the name after `event:` and the payload after `data:`.
type sseFrame struct{ event, data string }

// readNamedSSE splits an SSE body into frames keyed by event name. The clone stream's reader keys on
// a `type` field inside the payload instead; this stream names the frame with `event:`, which is what
// the client switches on.
func readNamedSSE(t *testing.T, body io.Reader) []sseFrame {
	t.Helper()
	var frames []sseFrame
	var frame sseFrame
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			frame.event = line[len("event: "):]
		case strings.HasPrefix(line, "data: "):
			frame.data = line[len("data: "):]
		case line == "":
			if frame.event != "" {
				frames = append(frames, frame)
			}
			frame = sseFrame{}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read SSE: %v", err)
	}
	return frames
}

// TestCommitAndDoneIncludesManualEditsAfterStaging verifies commit-and-done
// includes manual edits made after staging, while unstaged-only files stay
// out of the commit (staged/unstaged contract preserved).
// taskWorktreePath is where this server put a task's worktree. It asks the server's configuration
// instead of reading MIKE_WORKTREES_DIR: the worktrees directory is a decision resolved at boot and
// handed to NewServer, so a test that read it from the environment was reading a variable nothing
// sets.
func taskWorktreePath(t *testing.T, s *Server, projID, taskID string) string {
	t.Helper()
	cfg, err := s.configStore.GetConfig()
	if err != nil {
		t.Fatalf("workspace dir: %v", err)
	}
	return filepath.Join(cfg.WorkspaceDir, projID, taskID)
}

func TestCommitAndDoneIncludesManualEditsAfterStaging(t *testing.T) {
	s := testServer(t)

	// Create a project with a real git repo
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	cookie, csrfToken := loginTestCSRF(t, s)
	proj := map[string]any{"name": "manual-edit-commit", "path": tmpDir}
	req := testRequest("POST", "/api/projects", proj)
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	projID := mustJSON[map[string]any](t, rr.Body)["id"].(string)

	taskReq := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]any{
		"prompt": "test manual edits on commit",
	})
	withAuthCSRF(taskReq, cookie, csrfToken)
	taskRR := testResponse(t, s, taskReq)
	if taskRR.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", taskRR.Code, taskRR.Body.String())
	}
	taskID := mustJSON[map[string]any](t, taskRR.Body)["id"].(string)

	// Create a branch and worktree for the task
	branchName := "task/" + taskID
	testgit.Git(t, tmpDir, "branch", branchName)
	wtPath := taskWorktreePath(t, s, projID, taskID)
	testgit.Git(t, tmpDir, "worktree", "add", wtPath, branchName)

	taskDB, _ := s.store.GetTask(projID, taskID)
	taskDB.Branch = branchName
	taskDB.BaseRef = "master"
	if err := s.store.UpdateTask(taskDB); err != nil {
		t.Fatalf("update task: %v", err)
	}

	// Commit a base version of the file on the task branch
	os.WriteFile(filepath.Join(wtPath, "feature.txt"), []byte("line1\n"), 0o644)
	testgit.Git(t, wtPath, "add", "feature.txt")
	testgit.Git(t, wtPath, "commit", "-m", "base version")

	// "System staged" line2; user then adds line3 (unstaged) after staging.
	os.WriteFile(filepath.Join(wtPath, "feature.txt"), []byte("line1\nline2\n"), 0o644)
	testgit.Git(t, wtPath, "add", "feature.txt")
	os.WriteFile(filepath.Join(wtPath, "feature.txt"), []byte("line1\nline2\nline3\n"), 0o644)

	// Unstaged-only file — must stay out of the commit.
	os.WriteFile(filepath.Join(wtPath, "scratch.txt"), []byte("scratch\n"), 0o644)

	// Verify the staged-then-edited state before commit.
	status := testgit.GitOutput(t, wtPath, "status", "--porcelain")
	if !strings.Contains(status, "MM feature.txt") {
		t.Fatalf("expected feature.txt in MM state, got:\n%s", status)
	}

	// Move task to review (required for commit)
	moveTask(t, s, projID, taskID, "review")

	// Commit-and-done
	commitReq := testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/commit", map[string]any{
		"commit_message": "commit with manual edits",
	})
	commitRR := testResponseAuth(t, s, commitReq)
	if commitRR.Code != http.StatusOK {
		t.Fatalf("commit-and-done: %d %s", commitRR.Code, commitRR.Body.String())
	}
	result := mustJSON[map[string]any](t, commitRR.Body)
	if result["status"] != "done" {
		t.Fatalf("expected status=done, got %s", result["status"])
	}

	// The task branch commit must include the manual edit (line3).
	commitContent := testgit.GitOutput(t, tmpDir, "show", branchName+":feature.txt")
	if !strings.Contains(commitContent, "line3") {
		t.Fatalf("expected committed feature.txt to include manual edit line3, got:\n%s", commitContent)
	}

	// scratch.txt (only unstaged) must NOT be in the commit.
	branchFiles := testgit.GitOutput(t, tmpDir, "ls-tree", "-r", "--name-only", branchName)
	if strings.Contains(branchFiles, "scratch.txt") {
		t.Fatalf("expected scratch.txt to NOT be committed, got:\n%s", branchFiles)
	}
}

// Network isolation is the sandbox's own boundary, and an agent provider is the one kind that cannot
// live behind it: the agent binary runs on the host and answers over the network. So the pair
// (provider kind, isolation) decides whether a start is refused — and a refusal is a 409 naming the
// thing the person has to change, not a server fault.
func TestStartAnswersNetworkIsolationByProviderKind(t *testing.T) {
	rows := []struct {
		name       string
		kind       string
		url        string
		isolation  bool
		wantCode   int
		wantStatus string
		denyStatus string
		wantBody   string
	}{
		{name: "an agent under isolation is refused", kind: "agent", url: "agent://test-agent",
			isolation: true, wantCode: http.StatusConflict, wantStatus: "backlog",
			wantBody: "agent requires network to be enabled"},
		{name: "an agent without isolation starts", kind: "agent", url: "agent://test-agent-2",
			isolation: false, wantCode: http.StatusOK, denyStatus: "backlog"},
		{name: "an LLM under isolation starts", kind: "llm", url: "http://localhost:9999/v1",
			isolation: true, wantCode: http.StatusOK, wantStatus: "in_progress"},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			projID := createTestProject(t, s)

			cfg, err := s.configStore.GetConfig()
			if err != nil {
				t.Fatalf("get config: %v", err)
			}
			cfg.Providers = append(cfg.Providers, types.Provider{
				Name: "test-" + tc.kind, Kind: tc.kind, URL: tc.url, Concurrency: 1,
			})
			if err := s.configStore.UpdateConfig(cfg); err != nil {
				t.Fatalf("update config: %v", err)
			}
			if err := s.store.UpdateProjectSandboxNetworkIsolation(projID, tc.isolation); err != nil {
				t.Fatalf("set network isolation: %v", err)
			}

			var taskID string
			if tc.kind == "llm" {
				taskID = createTestTaskWithLLM(t, s, projID, "llm-task")
			} else {
				taskID = createTestTaskWithAgent(t, s, projID, "agent-task", tc.url)
			}

			rr := testResponseAuth(t, s, testRequest("POST",
				"/api/projects/"+projID+"/tasks/"+taskID+"/start", nil))
			if rr.Code != tc.wantCode {
				t.Fatalf("expected %d, got %d: %s", tc.wantCode, rr.Code, rr.Body.String())
			}
			if tc.wantBody != "" && !strings.Contains(rr.Body.String(), tc.wantBody) {
				t.Fatalf("expected the sentence the person can act on (%q), got: %s", tc.wantBody, rr.Body.String())
			}
			status := getTaskStatus(t, s, projID, taskID)
			if tc.wantStatus != "" && status != tc.wantStatus {
				t.Fatalf("task status = %s, want %s", status, tc.wantStatus)
			}
			// The agent binary may be missing from this host, which lands the task in review; what
			// must never happen is the isolation check resetting it to the backlog.
			if tc.denyStatus != "" && status == tc.denyStatus {
				t.Fatalf("task was blocked by the network-isolation check, got status %s", status)
			}
		})
	}
}

// A parent's column is its subtree's column: every descendant follows, however deep, and whatever
// column it was sitting in — and a descendant left in a finished column follows it back out.
func TestMoveParentMovesEveryDescendant(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	top := createTestTask(t, s, projID, "top")
	subA := createTestSubtask(t, s, projID, top, "sub-a")
	subB := createTestSubtask(t, s, projID, top, "sub-b")
	grandchildA := createTestSubtask(t, s, projID, subA, "grandchild-a")
	grandchildB := createTestSubtask(t, s, projID, subB, "grandchild-b")

	// Two of them start somewhere else, so a cascade that only reaches the rows beside the
	// parent, or only reaches a backlog row, cannot pass.
	moveTask(t, s, projID, subB, "review")
	moveTask(t, s, projID, grandchildB, "done")

	all := []string{top, subA, subB, grandchildA, grandchildB}

	moveTask(t, s, projID, top, "review")
	for _, id := range all {
		if got := getTaskStatus(t, s, projID, id); got != "review" {
			t.Fatalf("after moving the top row to review, %s is %s", id, got)
		}
	}

	moveTask(t, s, projID, top, "done")
	for _, id := range all {
		if got := getTaskStatus(t, s, projID, id); got != "done" {
			t.Fatalf("after moving the top row to done, %s is %s", id, got)
		}
		if got := getTaskExecutionState(t, s, projID, id); got != "completed" {
			t.Fatalf("%s followed its ancestor without an outcome: execution_state=%s", id, got)
		}
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

// stashWork puts one labelled entry on the project's repo the way the executor does: a change, staged,
// then stashed under MIKE's own label.
func stashWork(t *testing.T, dir, label string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, "change.txt"), []byte(label+"\n"), 0o644)
	testgit.Git(t, dir, "add", "change.txt")
	testgit.Git(t, dir, "stash", "push", "-u", "-m", label)
}

// stashProject is a project with one task and a repo to hold stash entries for it.
func stashProject(t *testing.T) (*Server, string, string, string) {
	t.Helper()
	s := testServer(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	initGitRepo(t, dir)

	cookie, csrfToken := loginTestCSRF(t, s)
	req := testRequest("POST", "/api/projects", map[string]any{"name": "stash-cleanup-test", "path": dir})
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	projID := mustJSON[map[string]any](t, rr.Body)["id"].(string)

	taskReq := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]any{"prompt": "test task"})
	withAuthCSRF(taskReq, cookie, csrfToken)
	taskRR := testResponse(t, s, taskReq)
	if taskRR.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", taskRR.Code, taskRR.Body.String())
	}
	return s, projID, mustJSON[map[string]any](t, taskRR.Body)["id"].(string), dir
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

// Deleting a task drops every stash entry labelled for it — the switch and fork labels, and the
// checkpoint labels a repo written before snapshots were pinned refs still holds — and keeps every
// other task's. The sweep is keyed by the task ID in the label, so it runs for a task that never got a
// branch too, and a branch-scoped sweep that leaned on task.branch would leave these entries behind.
func TestTaskDeleteDropsItsStashEntries(t *testing.T) {
	t.Run("switch_and_fork_labels", func(t *testing.T) {
		s, projID, taskID, dir := stashProject(t)

		stashWork(t, dir, git.StashMessageForSwitch(taskID))
		stashWork(t, dir, git.StashMessageForFork(taskID))
		const otherTaskID = "other-task-xyz"
		stashWork(t, dir, git.StashMessageForSwitch(otherTaskID))

		if got := gitListStashes(t, dir); len(got) != 3 {
			t.Fatalf("expected 3 stashes before delete, got %d: %v", len(got), got)
		}

		rr := testResponseAuth(t, s, testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+taskID, nil))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("delete task: expected 204, got %d %s", rr.Code, rr.Body.String())
		}

		var foundOther bool
		for _, subject := range gitListStashes(t, dir) {
			if strings.Contains(subject, taskID) {
				t.Errorf("stash for deleted task %s should be gone, found: %s", taskID, subject)
			}
			if strings.Contains(subject, otherTaskID) {
				foundOther = true
			}
		}
		if !foundOther {
			t.Errorf("the other task's stash went with this deletion")
		}
	})

	t.Run("legacy_checkpoint_labels", func(t *testing.T) {
		s, projID, taskID, dir := stashProject(t)

		// The task has started, so it has a branch — the label a checkpoint left carries it.
		branchName := "task/" + taskID
		testgit.Git(t, dir, "branch", branchName)
		task, _ := s.store.GetTask(projID, taskID)
		task.Branch = branchName
		if err := s.store.UpdateTask(task); err != nil {
			t.Fatalf("record the branch: %v", err)
		}

		// Labelled the way the executor labelled them before snapshots became pinned refs (see
		// backend/git/snapshots.go). Spelled out rather than built by a helper on purpose: this data
		// exists in real repos and the sweep must keep matching it no matter what the current code
		// calls its labels.
		for _, body := range []string{"checkpoint1", "checkpoint2"} {
			os.WriteFile(filepath.Join(dir, "change.txt"), []byte(body+"\n"), 0o644)
			testgit.Git(t, dir, "add", "change.txt")
			hash := testgit.GitOutput(t, dir, "stash", "create")
			testgit.Git(t, dir, "stash", "store", "-m",
				"On "+branchName+": mike-checkpoint-"+taskID, hash)
		}
		if got := gitListStashes(t, dir); len(got) < 2 {
			t.Fatalf("expected at least 2 stashes before delete, got %d: %v", len(got), got)
		}

		rr := testResponseAuth(t, s, testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+taskID, nil))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("delete task: expected 204, got %d %s", rr.Code, rr.Body.String())
		}
		for _, subject := range gitListStashes(t, dir) {
			if strings.Contains(subject, taskID) {
				t.Errorf("checkpoint stash for task %s should be gone, found: %s", taskID, subject)
			}
		}
	})

	t.Run("a_task_that_never_started", func(t *testing.T) {
		s, projID, taskID, dir := stashProject(t)

		stashWork(t, dir, git.StashMessageForSwitch(taskID))
		if got := gitListStashes(t, dir); len(got) != 1 {
			t.Fatalf("expected 1 stash before delete, got %d: %v", len(got), got)
		}

		rr := testResponseAuth(t, s, testRequest("DELETE", "/api/projects/"+projID+"/tasks/"+taskID, nil))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("delete task: expected 204, got %d %s", rr.Code, rr.Body.String())
		}
		if got := gitListStashes(t, dir); len(got) != 0 {
			t.Errorf("expected 0 stashes after delete, got %d: %v", len(got), got)
		}
	})
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

// conflictTask builds a task whose branch and whose project both changed shared.txt, so the sync-merge
// commit-and-done runs has to stop on a conflict. autoMerge is the setting the interruption must leave
// alone; the task is parked in review, which is where the click is available.
func conflictTask(t *testing.T, name string, autoMerge bool) (*Server, string, string) {
	t.Helper()
	s := testServer(t)
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	cookie, csrfToken := loginTestCSRF(t, s)
	req := testRequest("POST", "/api/projects", map[string]any{"name": name, "path": tmpDir})
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rr.Code, rr.Body.String())
	}
	projID := mustJSON[map[string]any](t, rr.Body)["id"].(string)

	taskReq := testRequest("POST", "/api/projects/"+projID+"/tasks", map[string]any{
		"prompt":           "test merge conflict " + name,
		"llama_server_url": "http://localhost:9999/v1",
		"model_name":       "mock-model",
		"auto_merge":       autoMerge,
	})
	withAuthCSRF(taskReq, cookie, csrfToken)
	taskRR := testResponse(t, s, taskReq)
	if taskRR.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", taskRR.Code, taskRR.Body.String())
	}
	taskID := mustJSON[map[string]any](t, taskRR.Body)["id"].(string)
	// The conflict path reports these settings back, so the fixture has to have set them.
	if got := getTask(t, s, projID, taskID)["auto_merge"]; got != autoMerge {
		t.Fatalf("the fixture stored auto_merge=%v, want %v", got, autoMerge)
	}

	branchName := "task/" + taskID
	testgit.Git(t, tmpDir, "branch", branchName)
	testgit.Git(t, tmpDir, "checkout", branchName)
	os.WriteFile(filepath.Join(tmpDir, "shared.txt"), []byte("task version\n"), 0o644)
	testgit.Git(t, tmpDir, "add", "shared.txt")
	testgit.Git(t, tmpDir, "commit", "-m", "task edit shared")
	testgit.Git(t, tmpDir, "checkout", "master")

	os.WriteFile(filepath.Join(tmpDir, "shared.txt"), []byte("master version\n"), 0o644)
	testgit.Git(t, tmpDir, "add", "shared.txt")
	testgit.Git(t, tmpDir, "commit", "-m", "master edit shared")

	wtPath := taskWorktreePath(t, s, projID, taskID)
	testgit.Git(t, tmpDir, "worktree", "add", wtPath, branchName)
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("worktree does not exist at %q: %v", wtPath, err)
	}

	taskDB, _ := s.store.GetTask(projID, taskID)
	taskDB.Branch = branchName
	taskDB.BaseRef = "master"
	if err := s.store.UpdateTask(taskDB); err != nil {
		t.Fatalf("record the branch: %v", err)
	}

	moveTask(t, s, projID, taskID, "review")
	return s, projID, taskID
}

// A merge conflict interrupts the click, it does not cancel it: the row comes back to in_progress
// carrying the conflict flag, and the merge settings the person chose survive the interruption
// unchanged, because the resolver that finishes the merge is the one that reads them.
func TestCommitAndDoneLeavesAConflictToBeResolved(t *testing.T) {
	rows := []struct {
		name      string
		autoMerge bool
	}{
		{"a task that was not going to auto merge", false},
		{"a task that was", true},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			s, projID, taskID := conflictTask(t, "conflict-"+strconv.FormatBool(tc.autoMerge), tc.autoMerge)
			before := getTask(t, s, projID, taskID)

			rr := testResponseAuth(t, s, testRequest("POST",
				"/api/projects/"+projID+"/tasks/"+taskID+"/commit", nil))
			if rr.Code != http.StatusOK {
				t.Fatalf("commit-and-done: %d %s", rr.Code, rr.Body.String())
			}
			result := mustJSON[map[string]any](t, rr.Body)

			if result["status"] != "in_progress" {
				t.Fatalf("expected status=in_progress, got %s", result["status"])
			}
			if result["has_merge_conflict"] != true {
				t.Fatalf("expected has_merge_conflict=true, got %v", result["has_merge_conflict"])
			}
			for _, setting := range []string{"auto_merge", "auto_commit"} {
				if result[setting] != before[setting] {
					t.Errorf("%s changed from %v to %v during the interruption", setting, before[setting], result[setting])
				}
			}
		})
	}
}

// The person reading the card has to learn why it started again: the conflict is written into the
// conversation as a system row, naming both the conflict and the agent that is being asked to resolve
// it. It is a system row, never dressed up as something the person said.
func TestCommitAndDoneReportsTheConflictInTheConversation(t *testing.T) {
	s, projID, taskID := conflictTask(t, "conflict-system-msg", false)

	rr := testResponseAuth(t, s, testRequest("POST",
		"/api/projects/"+projID+"/tasks/"+taskID+"/commit", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("commit-and-done: %d %s", rr.Code, rr.Body.String())
	}

	msgs, err := s.store.GetTaskMessages(taskID, "")
	if err != nil {
		t.Fatalf("get task messages: %v", err)
	}
	var found bool
	for _, msg := range msgs {
		if msg.Role != "system" || !strings.Contains(msg.Content, "Merge conflict detected") {
			continue
		}
		found = true
		if !strings.Contains(msg.Content, "resolving with agent") {
			t.Fatalf("system message should mention resolving with agent, got: %s", msg.Content)
		}
	}
	if !found {
		t.Fatal("expected system message about merge conflict resolution")
	}
}

// Helper: get a task's auto_merge value
func getTaskAutoMerge(t *testing.T, s *Server, projectID string, taskID string) bool {
	t.Helper()
	if v, ok := getTask(t, s, projectID, taskID)["auto_merge"]; ok {
		return v.(bool)
	}
	return false
}

// "Allow subtask to auto merge" is the parent's answer for its whole subtree: switching it on hands
// every descendant a merge — and the commit a merge presupposes — and switching it off takes the merge
// back down the same depth. A descendant two levels down that kept its own copy of the setting would
// merge into a branch its ancestor no longer allows.
func TestSubtasksAutoMergePropagatesToTheSubtree(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	parentID := createTestTask(t, s, projID, "parent-auto-merge")
	subA := createTestSubtask(t, s, projID, parentID, "sub-a")
	subB := createTestSubtask(t, s, projID, parentID, "sub-b")
	grandchildA := createTestSubtask(t, s, projID, subA, "grandchild-a")
	all := []string{subA, subB, grandchildA}

	// Nothing asked for a merge, so nothing has one — including the commit that only a merge brings.
	for _, id := range all {
		row := getTask(t, s, projID, id)
		if row["auto_merge"] == true || row["auto_commit"] == true {
			t.Fatalf("%s starts with auto_merge=%v auto_commit=%v", id, row["auto_merge"], row["auto_commit"])
		}
	}

	updateTask(t, s, projID, parentID, map[string]any{"subtasks_auto_merge": true})
	if getTask(t, s, projID, parentID)["subtasks_auto_merge"] != true {
		t.Fatal("parent subtasks_auto_merge should be true")
	}
	for _, id := range all {
		row := getTask(t, s, projID, id)
		if row["auto_merge"] != true {
			t.Fatalf("%s did not inherit its ancestor's answer", id)
		}
		if row["auto_commit"] != true {
			t.Fatalf("%s was handed a merge without the commit it presupposes", id)
		}
	}

	updateTask(t, s, projID, parentID, map[string]any{"subtasks_auto_merge": false})
	for _, id := range all {
		if getTaskAutoMerge(t, s, projID, id) {
			t.Fatalf("%s kept a merge its ancestor revoked", id)
		}
	}
}

func TestTaskOperatingModeRemount(t *testing.T) {
	s := testServer(t)
	sandboxcheck.Require(t, sandbox.BaseImage)
	projID := createTestProject(t, s)

	// Enable sandboxing on the project
	cookie, csrfToken := loginTestCSRF(t, s)
	reqEnable := testRequest(http.MethodPut, "/api/projects/"+projID, map[string]any{
		"sandbox_enabled": true,
	})
	withAuthCSRF(reqEnable, cookie, csrfToken)
	rrEnable := testResponse(t, s, reqEnable)
	if rrEnable.Code != http.StatusOK {
		t.Fatalf("enable sandbox: %d %s", rrEnable.Code, rrEnable.Body.String())
	}

	// Use agent provider to ensure llama_server_url and model_name are set
	taskID := createTestTaskWithAgent(t, s, projID, "Test mode remount", "http://localhost:9999/v1")

	t.Run("remounts when not running", func(t *testing.T) {
		// 1. Start task to create sandbox
		reqStart := testRequest(http.MethodPost, "/api/projects/"+projID+"/tasks/"+taskID+"/start", nil)
		rrStart := testResponseAuth(t, s, reqStart)
		if rrStart.Code != http.StatusOK {
			t.Fatalf("start task failed: %d %s", rrStart.Code, rrStart.Body.String())
		}

		// Wait for sandbox to be created (happens asynchronously in executeTask goroutine)
		// Use longer timeout for parallel test runs (podman contention)
		for i := 0; i < 100; i++ {
			if s.sandboxMgr.ContainerIsRunning(taskID) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !s.sandboxMgr.ContainerIsRunning(taskID) {
			t.Fatal("expected sandbox to be created after start")
		}

		// 2. Stop task (execution state becomes idle, but sandbox remains)
		reqStop := testRequest(http.MethodPost, "/api/projects/"+projID+"/tasks/"+taskID+"/stop", nil)
		rrStop := testResponseAuth(t, s, reqStop)
		if rrStop.Code != http.StatusOK {
			t.Fatalf("stop task failed: %d %s", rrStop.Code, rrStop.Body.String())
		}

		// Wait for the executor goroutine to exit and clean up task state
		// (context cancellation → goroutine exit → defer cleanup → DB update)
		for i := 0; i < 100; i++ {
			task, _ := s.store.GetTask(projID, taskID)
			if task != nil && task.ExecutionState != types.ExecStateRunning {
				break
			}
			time.Sleep(50 * time.Millisecond)
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
		if s.sandboxMgr.ContainerIsRunning(taskID) {
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

		// Wait for sandbox to be created (longer timeout for parallel runs)
		for i := 0; i < 100; i++ {
			if s.sandboxMgr.ContainerIsRunning(taskID) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !s.sandboxMgr.ContainerIsRunning(taskID) {
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

		// 3. Wait for sandbox recreation (mode switch triggers stop + create in executor loop)
		for i := 0; i < 100; i++ {
			if s.sandboxMgr.ContainerIsRunning(taskID) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !s.sandboxMgr.ContainerIsRunning(taskID) {
			t.Fatal("expected sandbox to remain active while task is running")
		}
	})
}

// createTaskDirect creates a task directly in the store with explicit
// auto-merge fields (bypassing API defaults) for PUT reparent tests.
func createTaskDirect(t *testing.T, s *Server, projID, taskID string, autoCommit, autoMerge, subtasksAutoMerge bool, parentTaskID string) {
	t.Helper()
	now := time.Now().UTC()
	task := &types.Task{
		ID:                taskID,
		ProjectID:         projID,
		Title:             "Direct Task",
		Prompt:            "Direct prompt",
		Status:            types.StatusBacklog,
		ExecutionState:    types.ExecStateIdle,
		AutoCommit:        autoCommit,
		AutoMerge:         autoMerge,
		SubtasksAutoMerge: subtasksAutoMerge,
		ParentTaskID:      parentTaskID,
		OperatingMode:     "act",
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := s.store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
}

// How PUT decides a task's auto_merge when its parent changes has four answers, and the fifth is the
// refusal: the parent's preference is inherited, an explicit value in the same request outranks it,
// un-nesting falls back to the server default, and re-sending the parent the task already has changes
// nothing. A task's own subtasks_auto_merge is never part of the deal.
func TestTaskUpdateParentDecidesAutoMerge(t *testing.T) {
	type spec struct {
		id                    string
		autoCommit, autoMerge bool
		subtasksAutoMerge     bool
		parent                string
	}
	rows := []struct {
		name             string
		defaultAutoMerge string
		parent, target   spec
		put              map[string]any
		wantCode         int
		want             map[string]any
	}{
		{
			name:     "a nested task inherits the parent's answer",
			parent:   spec{id: "parent-put-1", autoCommit: true, autoMerge: true, subtasksAutoMerge: false},
			target:   spec{id: "target-put-1", autoCommit: true, autoMerge: true, subtasksAutoMerge: true},
			put:      map[string]any{"parent_task_id": "parent-put-1"},
			wantCode: http.StatusOK,
			want: map[string]any{
				"parent_task_id": "parent-put-1",
				"auto_merge":     false,
				// the task's own answer for its own children survives
				"subtasks_auto_merge": true,
			},
		},
		{
			name:     "an explicit answer in the same request outranks the parent",
			parent:   spec{id: "parent-put-2", autoCommit: true, autoMerge: true, subtasksAutoMerge: false},
			target:   spec{id: "target-put-2", autoCommit: true, autoMerge: true, subtasksAutoMerge: true},
			put:      map[string]any{"parent_task_id": "parent-put-2", "auto_merge": true},
			wantCode: http.StatusOK,
			want:     map[string]any{"auto_merge": true},
		},
		{
			name:             "un-nesting falls back to the server default",
			defaultAutoMerge: "true",
			parent:           spec{id: "parent-put-3", autoCommit: true, autoMerge: true, subtasksAutoMerge: false},
			target:           spec{id: "target-put-3", autoCommit: true, autoMerge: false, subtasksAutoMerge: true, parent: "parent-put-3"},
			put:              map[string]any{"parent_task_id": ""},
			wantCode:         http.StatusOK,
			want: map[string]any{
				"auto_merge":          true,
				"auto_commit":         true,
				"subtasks_auto_merge": true,
			},
		},
		{
			name:     "the parent it already has changes nothing",
			parent:   spec{id: "parent-put-4", autoCommit: true, autoMerge: true, subtasksAutoMerge: false},
			target:   spec{id: "target-put-4", autoCommit: true, autoMerge: true, subtasksAutoMerge: true, parent: "parent-put-4"},
			put:      map[string]any{"parent_task_id": "parent-put-4"},
			wantCode: http.StatusOK,
			want:     map[string]any{"auto_merge": true},
		},
		{
			name:     "a parent that does not exist is refused",
			target:   spec{id: "target-put-5", autoCommit: true, autoMerge: true, subtasksAutoMerge: true},
			put:      map[string]any{"parent_task_id": "does-not-exist"},
			wantCode: http.StatusBadRequest,
		},
	}

	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			projID := createTestProject(t, s)
			if tc.defaultAutoMerge != "" {
				if err := s.store.SetConfigValue("default_auto_merge", tc.defaultAutoMerge); err != nil {
					t.Fatalf("set config: %v", err)
				}
			}
			for _, spec := range []spec{tc.parent, tc.target} {
				if spec.id == "" {
					continue
				}
				createTaskDirect(t, s, projID, spec.id, spec.autoCommit, spec.autoMerge, spec.subtasksAutoMerge, spec.parent)
			}

			rr := testResponseAuth(t, s, testRequest("PUT",
				"/api/projects/"+projID+"/tasks/"+tc.target.id, tc.put))
			if rr.Code != tc.wantCode {
				t.Fatalf("expected %d, got %d: %s", tc.wantCode, rr.Code, rr.Body.String())
			}
			if tc.wantCode != http.StatusOK {
				return
			}
			result := mustJSON[map[string]any](t, rr.Body)
			for key, want := range tc.want {
				got, present := result[key]
				if !present {
					t.Errorf("%s is missing from the response", key)
					continue
				}
				if fmt.Sprint(got) != fmt.Sprint(want) {
					t.Errorf("%s = %v, want %v", key, got, want)
				}
			}
		})
	}
}

// Which timestamp a click advances is what the board sorts on. moved_at belongs to the column and
// moves only when the column does; updated_at belongs to the row and moves on any write to it. Both
// are read back from the row, because the response echoing a value the store does not hold is the same
// bug wearing a different hat.
func TestMoveAndEditAdvanceTheirOwnTimestamp(t *testing.T) {
	s := testServer(t)
	projID := createTestProject(t, s)
	taskID := createTestTask(t, s, projID, "Move me later")

	// Pin the timestamps in the past so a change inside the same second is still observable.
	old := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
	if _, err := s.store.Exec(`UPDATE tasks SET created_at = ?, updated_at = ?, moved_at = ? WHERE id = ?`, old, old, old, taskID); err != nil {
		t.Fatalf("seed timestamps: %v", err)
	}

	// An ordinary edit is an update, not a move.
	rr := testResponseAuth(t, s, testRequest("PUT", "/api/projects/"+projID+"/tasks/"+taskID,
		map[string]any{"title": "Renamed"}))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	edited := mustJSON[map[string]any](t, rr.Body)
	if edited["moved_at"] != old {
		t.Fatalf("title edit must not change moved_at: expected %v, got %v", old, edited["moved_at"])
	}
	if edited["updated_at"] == old {
		t.Fatal("updated_at should still advance on an edit")
	}

	// A move is both: the column changed, and the row was written.
	rr = testResponseAuth(t, s, testRequest("POST", "/api/projects/"+projID+"/tasks/"+taskID+"/move",
		map[string]any{"status": "review"}))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	moved := mustJSON[map[string]any](t, rr.Body)
	movedAt, err := time.Parse(time.RFC3339, moved["moved_at"].(string))
	if err != nil {
		t.Fatalf("parse moved_at %v: %v", moved["moved_at"], err)
	}
	oldT, _ := time.Parse(time.RFC3339, old)
	if !movedAt.After(oldT) {
		t.Fatalf("expected moved_at to advance after a move, got %v (was %v)", moved["moved_at"], old)
	}
	if moved["updated_at"] == old {
		t.Fatal("a move that does not count as an update leaves the row sorted behind its own history")
	}

	// The stored row agrees with what the API returned.
	stored := getTask(t, s, projID, taskID)
	if stored["moved_at"] != moved["moved_at"] {
		t.Fatalf("API echoed moved_at %v but stored %v", moved["moved_at"], stored["moved_at"])
	}
}
