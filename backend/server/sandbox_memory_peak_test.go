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

// The sink is fed by every container the server started, task or project-level, so one decision covers
// both: the row it writes is the one the container's own id names, and it writes the pair together — the
// card shows the most recent run's peak beside that run's OOM kills.
func TestSandboxMemoryPeakSinkWritesOnlyTheRowItsContainerNames(t *testing.T) {
	cases := []struct {
		name      string
		scopedTo  string // "task" or "project": which family the container id names
		writes    [][3]int64
		wantPeak  int64
		wantKills int64
	}{
		{
			name:     "a task container records each run, last one standing",
			scopedTo: "task",
			writes:   [][3]int64{{742, 2}, {610, 0}},
			wantPeak: 610, wantKills: 0,
		},
		{
			name:     "the project's terminal container writes no task row",
			scopedTo: "project",
			writes:   [][3]int64{{999, 4}},
			wantPeak: 0, wantKills: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, task := peakFixture(t)
			container := task.ID
			if tc.scopedTo == "project" {
				container = "proj:" + task.ProjectID
			}
			sink := sandboxMemoryPeakSink(st)
			for _, w := range tc.writes {
				sink(container, w[0], w[1])
			}
			got, err := st.GetTaskByID(task.ID)
			if err != nil {
				t.Fatalf("get task: %v", err)
			}
			if got.SandboxMemoryPeakMB != tc.wantPeak || got.SandboxOOMKills != tc.wantKills {
				t.Errorf("recorded peak = %d with %d kills, want %d / %d",
					got.SandboxMemoryPeakMB, got.SandboxOOMKills, tc.wantPeak, tc.wantKills)
			}
		})
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
