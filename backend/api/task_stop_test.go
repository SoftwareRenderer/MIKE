package api

import (
	"encoding/json/v2"
	"net/http"
	"testing"

	"mike/backend/types"
)

// TestHandleStop_PausesTheRowAndEverythingUnderIt: the stop route pauses through the executor, so a
// branch stops as one unit — the parent the person clicked, the subtask mid-turn and the subtasks
// waiting to run all read paused, and none of them leaves its column. A paused row is a stop, not a
// schedule decision, and the answer the route gives is the paused parent. A row with no descendants
// is the same call with nothing under it.
func TestHandleStop_PausesTheRowAndEverythingUnderIt(t *testing.T) {
	for _, tt := range []struct {
		name string
		// click is the row the person stops, and state the execution state it is in when they do.
		click string
		state types.ExecutionState
		// under are the descendants the click must reach.
		under []string
	}{
		{
			name:  "a branch stops the parent, the running child and the rows waiting to run",
			click: "pause-par",
			state: types.ExecStateIdle,
			under: []string{"pause-child", "pause-wait1", "pause-wait2"},
		},
		{
			name:  "a leaf stops just itself",
			click: "pause-leaf",
			state: types.ExecStateRunning,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, s, projID := newTaskHandlerWithExecutor(t)
			createChainTask(t, s, projID, tt.click, "Parent", types.StatusInProgress, tt.state, "", "thread-"+tt.click, false)
			for _, id := range tt.under {
				state := types.ExecStateIdle
				if id == "pause-child" {
					state = types.ExecStateRunning
				}
				createChainTask(t, s, projID, id, id, types.StatusInProgress, state, tt.click, "thread-"+id, false)
			}

			mux := http.NewServeMux()
			mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/stop", h.HandleStop)
			rr := postToMux(t, mux, "/api/projects/"+projID+"/tasks/"+tt.click+"/stop", nil)
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

			for _, id := range append([]string{tt.click}, tt.under...) {
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
		})
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
