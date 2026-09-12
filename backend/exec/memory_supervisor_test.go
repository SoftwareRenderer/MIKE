package exec

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"kanban/backend/store"
	"kanban/backend/types"
)

// recordingSupervisorActions records every stopTask/startTask/startSubtask call.
type recordingSupervisorActions struct {
	mu       sync.Mutex
	stopped  []string
	started  []string // "P/T" for root, "P/T:parent" for subtask
	stopErr  error
	startErr error
}

func (a *recordingSupervisorActions) stopTask(taskID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopped = append(a.stopped, taskID)
	return a.stopErr
}

func (a *recordingSupervisorActions) startTask(projectID, taskID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.started = append(a.started, projectID+"/"+taskID)
	return a.startErr
}

func (a *recordingSupervisorActions) startSubtask(projectID, taskID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.started = append(a.started, projectID+"/"+taskID+":parent")
	return a.startErr
}

func (a *recordingSupervisorActions) stoppedIDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.stopped...)
}

func (a *recordingSupervisorActions) startedIDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.started...)
}

func newMemSupTestStore(t *testing.T) *store.Store {
	dir := t.TempDir()
	s, err := store.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.CreateProject(&types.Project{
		ID: "proj-mem", Name: "mem-test", Path: dir,
		SandboxEnabled: true, SandboxMemoryLimit: 1024,
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func seedRunningTask(t *testing.T, s *store.Store, id string, peakMB int64) {
	t.Helper()
	now := time.Now().UTC()
	if err := s.CreateTask(&types.Task{
		ID: id, ProjectID: "proj-mem", Title: id,
		Status: types.StatusInProgress, ExecutionState: types.ExecStateRunning,
		SandboxMemoryPeakMB: peakMB, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorPausesLowestUsageTask(t *testing.T) {
	s := newMemSupTestStore(t)
	seedRunningTask(t, s, "task-low", 200)
	seedRunningTask(t, s, "task-high", 800)

	gate := &MemoryGate{}
	gate.SetCapacity(900)
	if err := gate.Acquire(context.Background(), 800, nil); err != nil {
		t.Fatal(err)
	}
	// Queue a waiter so QueueLen > 0.
	go func() { _ = gate.Acquire(context.Background(), 300, nil) }()
	time.Sleep(50 * time.Millisecond)

	actions := &recordingSupervisorActions{}
	NewMemorySupervisor(s, gate, actions, time.Second).TickOnce()

	stopped := actions.stoppedIDs()
	if len(stopped) != 1 || stopped[0] != "task-low" {
		t.Fatalf("expected task-low paused (lowest peak, first in ascending order), got %v", stopped)
	}

	got, err := s.GetTaskByID("task-low")
	if err != nil {
		t.Fatal(err)
	}
	if !got.MemorySupervisorPaused {
		t.Error("MemorySupervisorPaused must be true on the paused task")
	}
	gotHigh, _ := s.GetTaskByID("task-high")
	if gotHigh.MemorySupervisorPaused {
		t.Error("MemorySupervisorPaused must be false on the non-paused task")
	}
}

func TestSupervisorNoPauseWhenQueueEmpty(t *testing.T) {
	s := newMemSupTestStore(t)
	seedRunningTask(t, s, "task-a", 500)

	gate := &MemoryGate{}
	gate.SetCapacity(900)
	if err := gate.Acquire(context.Background(), 500, nil); err != nil {
		t.Fatal(err)
	}

	actions := &recordingSupervisorActions{}
	NewMemorySupervisor(s, gate, actions, time.Second).TickOnce()

	if len(actions.stoppedIDs()) > 0 {
		t.Errorf("empty queue must not cause a pause, got %v", actions.stoppedIDs())
	}
}

func TestSupervisorDisablesWhenZeroCapacity(t *testing.T) {
	s := newMemSupTestStore(t)
	seedRunningTask(t, s, "task-x", 9999)

	actions := &recordingSupervisorActions{}
	NewMemorySupervisor(s, &MemoryGate{}, actions, time.Second).TickOnce()

	if len(actions.stoppedIDs()) > 0 {
		t.Errorf("zero capacity must disable the supervisor, got %v", actions.stoppedIDs())
	}
}

func TestSupervisorResumesLowestPausedTask(t *testing.T) {
	s := newMemSupTestStore(t)
	now := time.Now().UTC()
	for _, spec := range []struct {
		id     string
		peakMB int64
	}{
		{"task-b", 200},
		{"task-a", 800},
	} {
		if err := s.CreateTask(&types.Task{
			ID: spec.id, ProjectID: "proj-mem", Title: spec.id,
			Status: types.StatusInProgress, ExecutionState: types.ExecStatePaused,
			SandboxMemoryPeakMB: spec.peakMB, MemorySupervisorPaused: true,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}

	gate := &MemoryGate{}
	gate.SetCapacity(1000)

	actions := &recordingSupervisorActions{}
	NewMemorySupervisor(s, gate, actions, time.Second).TickOnce()

	started := actions.startedIDs()
	if len(started) != 1 || started[0] != "proj-mem/task-b" {
		t.Fatalf("expected lowest-peak task-b resumed first, got %v", started)
	}
}

func TestSupervisorResumesSubtaskViaStartSubtask(t *testing.T) {
	s := newMemSupTestStore(t)
	now := time.Now().UTC()
	if err := s.CreateTask(&types.Task{
		ID: "parent-1", ProjectID: "proj-mem", Title: "parent",
		Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(&types.Task{
		ID: "sub-1", ProjectID: "proj-mem", Title: "sub", ParentTaskID: "parent-1",
		Status: types.StatusInProgress, ExecutionState: types.ExecStatePaused,
		SandboxMemoryPeakMB: 300, MemorySupervisorPaused: true,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	gate := &MemoryGate{}
	gate.SetCapacity(1000)

	actions := &recordingSupervisorActions{}
	NewMemorySupervisor(s, gate, actions, time.Second).TickOnce()

	started := actions.startedIDs()
	if len(started) != 1 || started[0] != "proj-mem/sub-1:parent" {
		t.Fatalf("subtask resume must go through startSubtask, got %v", started)
	}
}

func TestSupervisorDoesNotResumeWhenWouldRequeue(t *testing.T) {
	s := newMemSupTestStore(t)
	now := time.Now().UTC()
	if err := s.CreateTask(&types.Task{
		ID: "big-task", ProjectID: "proj-mem", Title: "big",
		Status: types.StatusInProgress, ExecutionState: types.ExecStatePaused,
		SandboxMemoryPeakMB: 950, MemorySupervisorPaused: true,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	gate := &MemoryGate{}
	gate.SetCapacity(1000)
	if err := gate.Acquire(context.Background(), 100, nil); err != nil {
		t.Fatal(err)
	}

	actions := &recordingSupervisorActions{}
	NewMemorySupervisor(s, gate, actions, time.Second).TickOnce()

	if len(actions.startedIDs()) > 0 {
		t.Errorf("must not resume a task that immediately re-queues, got %v", actions.startedIDs())
	}
}

func TestSupervisorStartAndStop(t *testing.T) {
	s := newMemSupTestStore(t)
	gate := &MemoryGate{}
	gate.SetCapacity(1000)
	// Short interval so Start exercises the ticker.
	sup := NewMemorySupervisor(s, gate, &recordingSupervisorActions{}, 50*time.Millisecond)
	sup.Start()
	time.Sleep(150 * time.Millisecond)
	sup.Stop()
	sup.Stop() // idempotent
}
