package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"mike/backend/types"
)

// TestHandleStop_PausesWholeBranch: pausing a branch through the route leaves the parent, the subtask
// mid-turn and the subtasks waiting to run all reading paused, and answers the paused parent.
func TestHandleStop_PausesWholeBranch(t *testing.T) {
	h, s, projID := newTaskHandlerWithExecutor(t)
	createChainTask(t, s, projID, "pause-par", "Parent", "in_progress", "idle", "", "thread-pause-par", false)
	createChainTask(t, s, projID, "pause-child", "Running child", "in_progress", "running", "pause-par", "thread-pause-child", false)
	createChainTask(t, s, projID, "pause-wait1", "Waiting one", "in_progress", "idle", "pause-par", "thread-pause-wait1", false)
	createChainTask(t, s, projID, "pause-wait2", "Waiting two", "in_progress", "idle", "pause-par", "thread-pause-wait2", false)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/stop", h.HandleStop)
	rr := postToMux(t, mux, "/api/projects/"+projID+"/tasks/pause-par/stop", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("stop status = %d, body=%s", rr.Code, rr.Body.String())
	}

	var answered map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &answered); err != nil {
		t.Fatalf("decode answer: %v (%s)", err, rr.Body.String())
	}
	if answered["execution_state"] != string(types.ExecStatePaused) {
		t.Errorf("the route answers %v, want paused", answered["execution_state"])
	}

	for _, id := range []string{"pause-par", "pause-child", "pause-wait1", "pause-wait2"} {
		task, err := s.GetTaskByID(id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if task.ExecutionState != types.ExecStatePaused {
			t.Errorf("%s after the pause: execution_state = %s, want paused", id, task.ExecutionState)
		}
		if task.Status != types.StatusInProgress {
			t.Errorf("%s stays in its column: status = %s", id, task.Status)
		}
	}
}

// TestHandleStop_LeafTask: the same route on a task with no descendants pauses that one row.
func TestHandleStop_LeafTask(t *testing.T) {
	h, s, projID := newTaskHandlerWithExecutor(t)
	createChainTask(t, s, projID, "pause-leaf", "Leaf", "in_progress", "running", "", "thread-pause-leaf", false)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/stop", h.HandleStop)
	if rr := postToMux(t, mux, "/api/projects/"+projID+"/tasks/pause-leaf/stop", nil); rr.Code != http.StatusOK {
		t.Fatalf("stop status = %d, body=%s", rr.Code, rr.Body.String())
	}
	task, err := s.GetTaskByID("pause-leaf")
	if err != nil {
		t.Fatal(err)
	}
	if task.ExecutionState != types.ExecStatePaused {
		t.Errorf("leaf after the stop: execution_state = %s, want paused", task.ExecutionState)
	}
}

// TestHandleStop_MissingTask: a row that is not there is the store's answer, not a server fault.
func TestHandleStop_MissingTask(t *testing.T) {
	h, _, projID := newTaskHandlerWithExecutor(t)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/stop", h.HandleStop)
	rr := postToMux(t, mux, "/api/projects/"+projID+"/tasks/no-such-task/stop", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("stop of a missing task = %d, want 404 (body=%s)", rr.Code, rr.Body.String())
	}
}
