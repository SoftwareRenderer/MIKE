package managers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"kanban/backend/types"
)

// The route as backend/server/routes.go registers it.
func memoryAlertMux(h *TaskHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects/{projectID}/tasks/{taskID}/memory-alert-ack", h.HandleDismissMemoryAlert)
	return mux
}

const memoryAlertPath = "/api/projects/task-val-proj/tasks/task-val-task/memory-alert-ack"

func TestDismissMemoryAlertWritesTheAckTheClientDismissed(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	if err := s.RecordTaskSandboxMemory("task-val-task", 1024, 2); err != nil {
		t.Fatalf("record the run's memory: %v", err)
	}
	before, err := s.GetTask("task-val-proj", "task-val-task")
	if err != nil {
		t.Fatalf("read stored task: %v", err)
	}

	rr := doTaskRequest(t, memoryAlertMux(h), http.MethodPost, memoryAlertPath,
		map[string]any{"peak_mb": 1024, "oom_kills": 2, "limit_mb": 1024})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}

	stored, err := s.GetTask("task-val-proj", "task-val-task")
	if err != nil {
		t.Fatalf("read stored task after dismiss: %v", err)
	}
	if stored.MemoryAlertAck == nil || *stored.MemoryAlertAck != (types.MemoryAlertAck{PeakMB: 1024, OOMKills: 2, LimitMB: 1024}) {
		t.Fatalf("stored ack = %+v, want {1024 2 1024}", stored.MemoryAlertAck)
	}
	// The ack write must not disturb the run's own numbers.
	if stored.SandboxMemoryPeakMB != before.SandboxMemoryPeakMB || stored.SandboxOOMKills != before.SandboxOOMKills {
		t.Errorf("the ack write moved the run's memory record: peak %d→%d, kills %d→%d",
			before.SandboxMemoryPeakMB, stored.SandboxMemoryPeakMB, before.SandboxOOMKills, stored.SandboxOOMKills)
	}
	if !stored.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("updated_at moved from %s to %s — a dismissal must not reorder the board", before.UpdatedAt, stored.UpdatedAt)
	}

	var answered struct {
		ID             string                `json:"id"`
		MemoryAlertAck *types.MemoryAlertAck `json:"memory_alert_ack"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &answered); err != nil {
		t.Fatalf("decode answer: %v", err)
	}
	if answered.ID != "task-val-task" {
		t.Errorf("answered task = %q, want task-val-task", answered.ID)
	}
	if answered.MemoryAlertAck == nil || *answered.MemoryAlertAck != (types.MemoryAlertAck{PeakMB: 1024, OOMKills: 2, LimitMB: 1024}) {
		t.Errorf("answered ack = %+v, want the one just written — every device reads it off this frame", answered.MemoryAlertAck)
	}
}

// The path's project and task are honoured as a pair.
func TestDismissMemoryAlertRefusesATaskOutsideTheProject(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	if err := s.CreateProject(&types.Project{ID: "other-proj", Name: "other", Path: t.TempDir()}); err != nil {
		t.Fatalf("create other project: %v", err)
	}
	if err := s.CreateTask(&types.Task{ID: "task-val-task-2", ProjectID: "other-proj", Title: "the other one", Status: types.StatusBacklog}); err != nil {
		t.Fatalf("create other task: %v", err)
	}
	mux := memoryAlertMux(h)

	// The task exists, but not under this project.
	rr := doTaskRequest(t, mux, http.MethodPost, "/api/projects/task-val-proj/tasks/task-val-task-2/memory-alert-ack",
		map[string]any{"peak_mb": 1, "oom_kills": 0, "limit_mb": 1})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	other, err := s.GetTask("other-proj", "task-val-task-2")
	if err != nil {
		t.Fatalf("read the other task: %v", err)
	}
	if other.MemoryAlertAck != nil {
		t.Errorf("a cross-project write landed: %+v", other.MemoryAlertAck)
	}

	rr = doTaskRequest(t, mux, http.MethodPost, "/api/projects/task-val-proj/tasks/no-such-task/memory-alert-ack",
		map[string]any{"peak_mb": 1, "oom_kills": 0, "limit_mb": 1})
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d for a task that does not exist, want 404", rr.Code)
	}
}

// A missing or bad number would make the ack cover the wrong alert. Each rejection writes nothing.
func TestDismissMemoryAlertRejectsAnIncompleteBody(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	mux := memoryAlertMux(h)

	cases := []struct {
		body string
		want string // the field the 400 names
	}{
		{`{"oom_kills": 2, "limit_mb": 1024}`, "peak_mb"},
		{`{"peak_mb": 1024, "limit_mb": 1024}`, "oom_kills"},
		{`{"peak_mb": 1024, "oom_kills": 2}`, "limit_mb"},
		{`{"peak_mb": null, "oom_kills": 2, "limit_mb": 1024}`, "peak_mb"},
		{`{"peak_mb": "1024", "oom_kills": 2, "limit_mb": 1024}`, "peak_mb"},
		{`{"peak_mb": 1024, "oom_kills": 2, "limit_mb": -1}`, "limit_mb"},
		{`{}`, "peak_mb"},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			rr := doTaskRequest(t, mux, http.MethodPost, memoryAlertPath, json.RawMessage(tc.body))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rr.Code, rr.Body.String())
			}
			if msg := rr.Body.String(); !strings.Contains(msg, tc.want) {
				t.Errorf("the 400 does not name %q: %s", tc.want, msg)
			}
			task, err := s.GetTask("task-val-proj", "task-val-task")
			if err != nil {
				t.Fatalf("read stored task: %v", err)
			}
			if task.MemoryAlertAck != nil {
				t.Errorf("a rejected body wrote an acknowledgement: %+v", task.MemoryAlertAck)
			}
		})
	}
}

// Whether an alert exists is the client's rule over the numbers, not this write's.
func TestDismissMemoryAlertOnATaskWithNoRun(t *testing.T) {
	h, s, _ := newTestTaskHandler(t)
	setupTaskValidationProject(t, h, s)
	rr := doTaskRequest(t, memoryAlertMux(h), http.MethodPost, memoryAlertPath,
		map[string]any{"peak_mb": 0, "oom_kills": 0, "limit_mb": 0})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	task, err := s.GetTask("task-val-proj", "task-val-task")
	if err != nil {
		t.Fatalf("read stored task: %v", err)
	}
	if task.MemoryAlertAck == nil {
		t.Error("a dismissal of zeros is a real one and must be stored")
	}
}
