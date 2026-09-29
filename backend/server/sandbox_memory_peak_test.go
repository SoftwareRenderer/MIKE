package server

import (
	"strings"
	"testing"

	"mike/backend/store"
	"mike/backend/types"
	"mike/pkg/logging"
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
	t.Parallel()
	st, task := peakFixture(t)
	sink := sandboxMemoryPeakSink(st)

	sink(task.ID, 742, 2)
	got, err := st.GetTaskByID(task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.SandboxMemoryPeakMB != 742 || got.SandboxOOMKills != 2 {
		t.Fatalf("recorded peak = %d with %d kills, want 742 / 2", got.SandboxMemoryPeakMB, got.SandboxOOMKills)
	}

	sink(task.ID, 610, 0)
	got, err = st.GetTaskByID(task.ID)
	if err != nil {
		t.Fatalf("get task after second run: %v", err)
	}
	if got.SandboxMemoryPeakMB != 610 || got.SandboxOOMKills != 0 {
		t.Errorf("recorded peak = %d with %d kills, want 610 / 0 — the row shows the most recent run, both numbers together",
			got.SandboxMemoryPeakMB, got.SandboxOOMKills)
	}
}

func TestSandboxMemoryPeakSinkIgnoresContainersThatNameNoTask(t *testing.T) {
	t.Parallel()
	st, task := peakFixture(t)
	sink := sandboxMemoryPeakSink(st)

	sink("proj:"+task.ProjectID, 999, 4)

	got, err := st.GetTaskByID(task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.SandboxMemoryPeakMB != 0 {
		t.Errorf("project container wrote peak %d to a task row, want 0", got.SandboxMemoryPeakMB)
	}
}

func TestTaskPayloadCarriesTheRecordedPeak(t *testing.T) {
	t.Parallel()
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

	sandboxMemoryPeakSink(st)(task.ID, 742, 3)

	body := mustJSON[[]map[string]any](t, testResponseAuth(t, s, testRequest("GET", "/api/projects/"+proj.ID+"/tasks", nil)).Body)
	if len(body) != 1 {
		t.Fatalf("got %d tasks, want 1", len(body))
	}
	if peak, ok := body[0]["sandbox_memory_peak_mb"].(float64); !ok || peak != 742 {
		t.Errorf("task payload peak = %v (present: %v), want 742", body[0]["sandbox_memory_peak_mb"], ok)
	}
	if kills, ok := body[0]["sandbox_oom_kills"].(float64); !ok || kills != 3 {
		t.Errorf("task payload OOM kills = %v (present: %v), want 3", body[0]["sandbox_oom_kills"], ok)
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
			if _, present := row["sandbox_oom_kills"]; present {
				t.Errorf("a task that never lost a process carries %v, want the key omitted", row["sandbox_oom_kills"])
			}
		}
	}
}

func TestSandboxMemoryPeakSinkLogsAFailedWrite(t *testing.T) {
	// Serial: logging.TestSink re-points the process-wide logger, so a concurrent test’s lines would
	// land in this buffer.
	logs, restore := logging.TestSink()
	defer restore()

	st, task := peakFixture(t)
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	sandboxMemoryPeakSink(st)(task.ID, 512, 1)

	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "task_id="+task.ID) {
		t.Errorf("a failed peak write must be a warning naming the task, got:\n%s", out)
	}
}
