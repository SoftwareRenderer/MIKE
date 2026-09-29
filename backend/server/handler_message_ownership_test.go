package server

import (
	"net/http"
	"testing"
	"time"

	"mike/backend/types"
)

const crossTaskHash = "0000000000000000000000000000000000000001"

// twoProjectFixture makes two projects with a task each.
func twoProjectFixture(t *testing.T, s *Server) (projA, taskA, projB, taskB string) {
	t.Helper()
	projA = createTestProject(t, s)
	taskA = createTestTask(t, s, projA, "task of the first project")
	projB = createTestProject(t, s)
	taskB = createTestTask(t, s, projB, "task of the second project")
	return
}

func plantMessage(t *testing.T, s *Server, taskID, id string) {
	t.Helper()
	msg := &types.TaskMessage{ID: id, TaskID: taskID, Role: "assistant", Content: "checkpoint", CreatedAt: time.Now().UTC()}
	if err := s.store.CreateTaskMessage(msg); err != nil {
		t.Fatalf("create message: %v", err)
	}
}

func TestPatchMessageRefusesAnotherTasksMessage(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	projA, taskA, projB, taskB := twoProjectFixture(t, s)
	plantMessage(t, s, taskA, "msg-patch-cross-project")

	req := testRequest(http.MethodPatch,
		"/api/projects/"+projB+"/tasks/"+taskB+"/messages/msg-patch-cross-project",
		map[string]any{"git_commit_hash": crossTaskHash})
	if rr := testResponseAuth(t, s, req); rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rr.Code, rr.Body.String())
	}
	// A stored hash is later handed to a worktree reset.
	if got := storedCommitHash(t, s, "msg-patch-cross-project"); got != "" {
		t.Errorf("stored git_commit_hash %q on a message the request's task does not own", got)
	}

	// The owning task can still write its own checkpoint.
	req = testRequest(http.MethodPatch,
		"/api/projects/"+projA+"/tasks/"+taskA+"/messages/msg-patch-cross-project",
		map[string]any{"git_commit_hash": crossTaskHash})
	if rr := testResponseAuth(t, s, req); rr.Code != http.StatusOK {
		t.Fatalf("the owning task was refused: %d %s", rr.Code, rr.Body.String())
	}
	if got := storedCommitHash(t, s, "msg-patch-cross-project"); got != crossTaskHash {
		t.Errorf("stored git_commit_hash = %q, want %q", got, crossTaskHash)
	}
}

func TestGetMessageByIDRefusesAnotherTasksMessage(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	_, taskA, projB, taskB := twoProjectFixture(t, s)
	plantMessage(t, s, taskA, "msg-read-cross-project")

	req := testRequest(http.MethodGet,
		"/api/projects/"+projB+"/tasks/"+taskB+"/messages/msg-read-cross-project?full=1", nil)
	rr := testResponseAuth(t, s, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rr.Code, rr.Body.String())
	}
}

func TestDeleteSingleMessageRefusesAnotherTasksMessage(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	_, taskA, projB, taskB := twoProjectFixture(t, s)
	plantMessage(t, s, taskA, "msg-delete-cross-project")

	req := testRequest(http.MethodDelete, "/api/projects/"+projB+"/tasks/"+taskB+"/messages",
		map[string]any{"message_id": "msg-delete-cross-project", "delete_single": true})
	if rr := testResponseAuth(t, s, req); rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rr.Code, rr.Body.String())
	}
	if _, err := s.store.GetTaskMessageByID("msg-delete-cross-project"); err != nil {
		t.Errorf("the other task's message was deleted anyway: %v", err)
	}

	// The range delete shares the guard.
	req = testRequest(http.MethodDelete, "/api/projects/"+projB+"/tasks/"+taskB+"/messages",
		map[string]any{"message_id": "msg-delete-cross-project"})
	if rr := testResponseAuth(t, s, req); rr.Code != http.StatusNotFound {
		t.Fatalf("range delete: status = %d, want 404: %s", rr.Code, rr.Body.String())
	}
	if _, err := s.store.GetTaskMessageByID("msg-delete-cross-project"); err != nil {
		t.Errorf("the range delete removed the other task's anchor: %v", err)
	}
}

func TestListThreadsRefusesAnotherProjectsTask(t *testing.T) {
	t.Parallel()
	s := testServer(t)
	projA, taskA, projB, _ := twoProjectFixture(t, s)

	req := testRequest(http.MethodGet, "/api/projects/"+projB+"/tasks/"+taskA+"/threads", nil)
	if rr := testResponseAuth(t, s, req); rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rr.Code, rr.Body.String())
	}

	req = testRequest(http.MethodGet, "/api/projects/"+projA+"/tasks/"+taskA+"/threads", nil)
	if rr := testResponseAuth(t, s, req); rr.Code != http.StatusOK {
		t.Fatalf("the owning project was refused: %d %s", rr.Code, rr.Body.String())
	}
}
