package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mike/backend/exec"
	"mike/backend/types"
)

// newPrioritizeMux wires the one route this file is about.
func newPrioritizeMux(h *TaskHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/prioritize", h.HandlePrioritize)
	return mux
}

// postPrioritize clicks the button: a POST with no body, because the route takes no argument.
func postPrioritize(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// The route answers the queue's question rather than a fault: a task that holds its slot, or never
// waited for one, is answered promoted=false. That a queued task answers true is exec's test to prove —
// the answer comes from the gate's own queue, so no stand-in here could show it.
func TestPrioritizeAnswersWhetherTheTaskIsQueued(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	h.executor = exec.NewTaskExecutor(exec.Deps{
		Store:        s,
		ConfigStore:  h.configStore,
		AppStreamMgr: h.boardStreamMgr,
		WorktreeMgr:  h.worktreeMgr,
	})

	now := time.Now().UTC()
	if err := s.CreateProject(&types.Project{ID: "proj-prio", Name: "prio", Path: t.TempDir()}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(&types.Task{
		ID:             "task-prio",
		ProjectID:      "proj-prio",
		Title:          "Prioritize me",
		Status:         types.StatusInProgress,
		ExecutionState: types.ExecStateIdle,
		CreatedAt:      now,
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}

	mux := newPrioritizeMux(h)
	rr := postPrioritize(t, mux, "/api/projects/proj-prio/tasks/task-prio/prioritize")
	if rr.Code != http.StatusOK {
		t.Fatalf("a real task answered %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Promoted bool `json:"promoted"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rr.Body.String(), err)
	}
	if body.Promoted {
		t.Error("a task that is not waiting for a slot answered as promoted")
	}

	// The path's project and task have to be the same task's — a task id from another project is not
	// this project's business, whatever the queue holds under it.
	if rr := postPrioritize(t, mux, "/api/projects/proj-other/tasks/task-prio/prioritize"); rr.Code != http.StatusNotFound {
		t.Errorf("a task id under a project that does not own it answered %d, want 404", rr.Code)
	}
}
