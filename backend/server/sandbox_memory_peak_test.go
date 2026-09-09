package server

import (
	"strings"
	"testing"

	"kanban/backend/store"
	"kanban/backend/types"
	"kanban/pkg/logging"
)

func peakFixture(t *testing.T) (*store.Store, *types.Task) {
	t.Helper()
	st, err := store.NewStore(t.TempDir() + "/peak.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	proj := &types.Project{ID: "proj-1", Name: "proj-1", Path: t.TempDir()}
	if err := st.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &types.Task{ID: "task-1", ProjectID: "proj-1", Title: "t", Prompt: "p", Status: types.StatusReview}
	if err := st.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return st, task
}

func TestSandboxMemoryPeakSinkRecordsTheLastRun(t *testing.T) {
	st, task := peakFixture(t)
	sink := sandboxMemoryPeakSink(st)

	sink(task.ID, 742)
	got, err := st.GetTaskByID(task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.SandboxMemoryPeakMB != 742 {
		t.Fatalf("recorded peak = %d, want 742", got.SandboxMemoryPeakMB)
	}

	sink(task.ID, 610)
	got, err = st.GetTaskByID(task.ID)
	if err != nil {
		t.Fatalf("get task after second run: %v", err)
	}
	if got.SandboxMemoryPeakMB != 610 {
		t.Errorf("recorded peak = %d, want 610 — the row shows the most recent run", got.SandboxMemoryPeakMB)
	}
}

func TestSandboxMemoryPeakSinkIgnoresContainersThatNameNoTask(t *testing.T) {
	st, task := peakFixture(t)
	sink := sandboxMemoryPeakSink(st)

	sink("proj:"+task.ProjectID, 999)

	got, err := st.GetTaskByID(task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.SandboxMemoryPeakMB != 0 {
		t.Errorf("project container wrote peak %d to a task row, want 0", got.SandboxMemoryPeakMB)
	}
}

func TestTaskPayloadCarriesTheRecordedPeak(t *testing.T) {
	s := testServer(t)
	st := s.store

	proj := &types.Project{ID: "proj-api", Name: "proj-api", Path: t.TempDir()}
	if err := st.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &types.Task{ID: "task-api", ProjectID: proj.ID, Title: "t", Prompt: "p", Status: types.StatusDone}
	if err := st.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	sandboxMemoryPeakSink(st)(task.ID, 742)

	body := mustJSON[[]map[string]any](t, testResponseAuth(t, s, testRequest("GET", "/api/projects/"+proj.ID+"/tasks", nil)).Body)
	if len(body) != 1 {
		t.Fatalf("got %d tasks, want 1", len(body))
	}
	if peak, ok := body[0]["sandbox_memory_peak_mb"].(float64); !ok || peak != 742 {
		t.Errorf("task payload peak = %v (present: %v), want 742", body[0]["sandbox_memory_peak_mb"], ok)
	}

	// A task that never recorded a peak sends no key, so the panel never has to tell "absent"
	// apart from a run that used no memory.
	silent := &types.Task{ID: "task-unsandboxed", ProjectID: proj.ID, Title: "t", Prompt: "p", Status: types.StatusBacklog}
	if err := st.CreateTask(silent); err != nil {
		t.Fatalf("create second task: %v", err)
	}
	body = mustJSON[[]map[string]any](t, testResponseAuth(t, s, testRequest("GET", "/api/projects/"+proj.ID+"/tasks", nil)).Body)
	for _, row := range body {
		if row["id"] == silent.ID {
			if _, present := row["sandbox_memory_peak_mb"]; present {
				t.Errorf("a task that never recorded a peak carries %v, want the key omitted", row["sandbox_memory_peak_mb"])
			}
		}
	}
}

func TestSandboxMemoryPeakSinkLogsAFailedWrite(t *testing.T) {
	logs, restore := logging.TestSink()
	defer restore()

	st, task := peakFixture(t)
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	sandboxMemoryPeakSink(st)(task.ID, 512)

	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "task_id="+task.ID) {
		t.Errorf("a failed peak write must be a warning naming the task, got:\n%s", out)
	}
}
