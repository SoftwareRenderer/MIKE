package server

import (
	"net/http"
	"net/http/httptest"
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

// A route under a task path names a task, but a message is keyed by its own id alone: the lookup that fetches
// a row never learns which task asked. So each message route resolves the task in the path first and refuses
// another task's row, and a refusal must change nothing — a stored hash is later handed to a worktree reset,
// and a deleted row is not recoverable. server.messageInTask is the one guard every one of these goes
// through; the threads route has its own half of the same rule in TestListThreadsRefusesAnotherProjectsTask.
func TestMessageRoutesRefuseAnotherTasksRow(t *testing.T) {
	const foreignID = "msg-cross-project"

	path := func(projID, taskID string) string {
		return "/api/projects/" + projID + "/tasks/" + taskID + "/messages"
	}
	cases := []struct {
		name      string
		call      func(t *testing.T, s *Server, projID, taskID string) *httptest.ResponseRecorder
		unchanged func(t *testing.T, s *Server)
	}{
		{
			name: "the patch that names a checkpoint",
			call: func(t *testing.T, s *Server, projID, taskID string) *httptest.ResponseRecorder {
				return testResponseAuth(t, s, testRequest(http.MethodPatch, path(projID, taskID)+"/"+foreignID,
					map[string]any{"git_commit_hash": crossTaskHash}))
			},
			unchanged: func(t *testing.T, s *Server) {
				if got := storedCommitHash(t, s, foreignID); got != "" {
					t.Errorf("the refused patch stored git_commit_hash %q on a row its task does not own", got)
				}
			},
		},
		{
			name: "the read of one row",
			call: func(t *testing.T, s *Server, projID, taskID string) *httptest.ResponseRecorder {
				return testResponseAuth(t, s, testRequest(http.MethodGet,
					path(projID, taskID)+"/"+foreignID+"?full=1", nil))
			},
			unchanged: func(t *testing.T, s *Server) {
				if _, err := s.store.GetTaskMessageByID(foreignID); err != nil {
					t.Errorf("the refused read took the row away: %v", err)
				}
			},
		},
		{
			name: "the delete of one row",
			call: func(t *testing.T, s *Server, projID, taskID string) *httptest.ResponseRecorder {
				return testResponseAuth(t, s, testRequest(http.MethodDelete, path(projID, taskID),
					map[string]any{"message_id": foreignID, "delete_single": true}))
			},
			unchanged: func(t *testing.T, s *Server) {
				if _, err := s.store.GetTaskMessageByID(foreignID); err != nil {
					t.Errorf("the other task's message was deleted anyway: %v", err)
				}
			},
		},
		{
			name: "the delete of a range",
			call: func(t *testing.T, s *Server, projID, taskID string) *httptest.ResponseRecorder {
				return testResponseAuth(t, s, testRequest(http.MethodDelete, path(projID, taskID),
					map[string]any{"message_id": foreignID}))
			},
			unchanged: func(t *testing.T, s *Server) {
				if _, err := s.store.GetTaskMessageByID(foreignID); err != nil {
					t.Errorf("the range delete removed the other task's anchor: %v", err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			_, taskA, projB, taskB := twoProjectFixture(t, s)
			plantMessage(t, s, taskA, foreignID)

			if rr := tc.call(t, s, projB, taskB); rr.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404: %s", rr.Code, rr.Body.String())
			}
			tc.unchanged(t, s)
		})
	}

	// The guard refuses the stranger, not the owner.
	t.Run("the owning task still writes its own checkpoint", func(t *testing.T) {
		s := testServer(t)
		projA, taskA, _, _ := twoProjectFixture(t, s)
		plantMessage(t, s, taskA, foreignID)

		rr := testResponseAuth(t, s, testRequest(http.MethodPatch, path(projA, taskA)+"/"+foreignID,
			map[string]any{"git_commit_hash": crossTaskHash}))
		if rr.Code != http.StatusOK {
			t.Fatalf("the owning task was refused: %d %s", rr.Code, rr.Body.String())
		}
		if got := storedCommitHash(t, s, foreignID); got != crossTaskHash {
			t.Errorf("stored git_commit_hash = %q, want %q", got, crossTaskHash)
		}
	})
}

func TestListThreadsRefusesAnotherProjectsTask(t *testing.T) {
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
