package exec

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mike/backend/store"
	"mike/backend/types"
)

// recordingSupervisorActions records every stopTask/startTask/startSubtask call.
type recordingSupervisorActions struct {
	mu       sync.Mutex
	stopped  []string
	started  []string // "P/T" for root, "P/T:parent" for subtask
	noted    []string // "pause:T" / "resume:T" for the history notes
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

func (a *recordingSupervisorActions) noteMemoryPause(taskID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.noted = append(a.noted, "pause:"+taskID)
}

func (a *recordingSupervisorActions) noteMemoryResume(taskID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.noted = append(a.noted, "resume:"+taskID)
}

func (a *recordingSupervisorActions) noteKinds() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.noted...)
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

// fixedCharges answers the current cost of each container, keyed by task ID.
type fixedCharges map[string]int64

func (f fixedCharges) Charges() map[string]int64 { return f }

// TestSupervisorPausesSmallestLiveChargeFirst gives the two tasks a live cost the opposite way round
// to their stored peaks, so a decision that read the task rows would pause the wrong one.
func TestSupervisorPausesSmallestLiveChargeFirst(t *testing.T) {
	s := newMemSupTestStore(t)
	seedRunningTask(t, s, "task-heavy", 120) // last run's peak is the small one
	seedRunningTask(t, s, "task-light", 900) // …and this one's is the big one

	charges := fixedCharges{"task-heavy": 950, "task-light": 120}

	gate := &MemoryGate{}
	gate.SetCapacity(900)
	if err := gate.Acquire(context.Background(), 800, nil); err != nil {
		t.Fatal(err)
	}
	// Queue a waiter so QueueLen > 0.
	go func() { _ = gate.Acquire(context.Background(), 300, nil) }()
	time.Sleep(50 * time.Millisecond)

	actions := &recordingSupervisorActions{}
	NewMemorySupervisor(s, gate, charges, actions, time.Second).TickOnce()

	stopped := actions.stoppedIDs()
	if len(stopped) != 1 || stopped[0] != "task-light" {
		t.Fatalf("expected task-light paused (120 MB live, smallest), got %v", stopped)
	}

	got, err := s.GetTaskByID("task-light")
	if err != nil {
		t.Fatal(err)
	}
	if !got.MemorySupervisorPaused {
		t.Error("MemorySupervisorPaused must be true on the paused task")
	}
	gotHeavy, _ := s.GetTaskByID("task-heavy")
	if gotHeavy.MemorySupervisorPaused {
		t.Error("MemorySupervisorPaused must be false on the non-paused task")
	}
	if notes := actions.noteKinds(); len(notes) != 1 || notes[0] != "pause:task-light" {
		t.Errorf("the paused task alone must get the pause note, got %v", notes)
	}
}

// With no container reading, a task is costed at its reserve, never at zero.
func TestSupervisorFallsBackToReserveWithoutContainerCharges(t *testing.T) {
	s := newMemSupTestStore(t)
	narrow := newNarrowMemProject(t, s, "proj-narrow", 256)
	_ = narrow
	seedRunningTask(t, s, "task-wide", 10) // same 1024 MB project as the store's default
	seedRunningTaskIn(t, s, "task-narrow", "proj-narrow", 999)

	gate := &MemoryGate{}
	gate.SetCapacity(900)
	if err := gate.Acquire(context.Background(), 800, nil); err != nil {
		t.Fatal(err)
	}
	go func() { _ = gate.Acquire(context.Background(), 300, nil) }()
	time.Sleep(50 * time.Millisecond)

	actions := &recordingSupervisorActions{}
	NewMemorySupervisor(s, gate, nil, actions, time.Second).TickOnce()

	stopped := actions.stoppedIDs()
	if len(stopped) != 1 || stopped[0] != "task-narrow" {
		t.Fatalf("expected the 256 MB reserve paused, got %v", stopped)
	}
}

// A host under its free-memory floor pauses tasks even with nothing queued.
func TestSupervisorPausesOnHostMemoryFloorWithEmptyQueue(t *testing.T) {
	s := newMemSupTestStore(t)
	seedRunningTask(t, s, "task-a", 500)

	gate := &MemoryGate{}
	gate.SetCapacity(900)
	// 90 MB free of 2000 total is under the 5 % floor; nothing is queued. MIKE's own charge is well
	// inside its ceiling, which is the point: the floor pauses on the host, not on the budget.
	gate.SetMeasurements(500, 500, 90, 2000)

	actions := &recordingSupervisorActions{}
	NewMemorySupervisor(s, gate, fixedCharges{"task-a": 500}, actions, time.Second).TickOnce()

	stopped := actions.stoppedIDs()
	if len(stopped) != 1 || stopped[0] != "task-a" {
		t.Fatalf("the free-memory floor must pause a task with an empty queue, got %v", stopped)
	}
	got, _ := s.GetTaskByID("task-a")
	if !got.MemorySupervisorPaused {
		t.Error("MemorySupervisorPaused must be true on the paused task")
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
	NewMemorySupervisor(s, gate, nil, actions, time.Second).TickOnce()

	if len(actions.stoppedIDs()) > 0 {
		t.Errorf("empty queue must not cause a pause, got %v", actions.stoppedIDs())
	}
}

func TestSupervisorDisablesWhenZeroCapacity(t *testing.T) {
	s := newMemSupTestStore(t)
	seedRunningTask(t, s, "task-x", 9999)

	actions := &recordingSupervisorActions{}
	NewMemorySupervisor(s, &MemoryGate{}, nil, actions, time.Second).TickOnce()

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
	// Above the project's 1024 MB reserve, otherwise no task could ever resume.
	gate.SetCapacity(2000)

	actions := &recordingSupervisorActions{}
	NewMemorySupervisor(s, gate, nil, actions, time.Second).TickOnce()

	started := actions.startedIDs()
	if len(started) != 1 || started[0] != "proj-mem/task-b" {
		t.Fatalf("expected task-b resumed first (equal reserves, recorded peak breaks the tie), got %v", started)
	}
	if notes := actions.noteKinds(); len(notes) != 1 || notes[0] != "resume:task-b" {
		t.Errorf("the resumed task alone must get the resume note, got %v", notes)
	}
}

// A task whose own cap exceeds the ceiling is never resumed.
func TestSupervisorNeverResumesTaskWhoseCapExceedsCapacity(t *testing.T) {
	s := newMemSupTestStore(t) // proj-mem caps a sandbox at 1024 MB
	now := time.Now().UTC()
	if err := s.CreateTask(&types.Task{
		ID: "task-wide", ProjectID: "proj-mem", Title: "wide",
		Status: types.StatusInProgress, ExecutionState: types.ExecStatePaused,
		MemorySupervisorPaused: true, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	gate := &MemoryGate{}
	gate.SetCapacity(1000)

	actions := &recordingSupervisorActions{}
	NewMemorySupervisor(s, gate, nil, actions, time.Second).TickOnce()

	if started := actions.startedIDs(); len(started) > 0 {
		t.Errorf("must not resume a task that cannot fit its own cap, got %v", started)
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
	gate.SetCapacity(2000)

	actions := &recordingSupervisorActions{}
	NewMemorySupervisor(s, gate, nil, actions, time.Second).TickOnce()

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
	NewMemorySupervisor(s, gate, nil, actions, time.Second).TickOnce()

	if len(actions.startedIDs()) > 0 {
		t.Errorf("must not resume a task that immediately re-queues, got %v", actions.startedIDs())
	}
}

func TestSupervisorStartAndStop(t *testing.T) {
	s := newMemSupTestStore(t)
	gate := &MemoryGate{}
	gate.SetCapacity(1000)
	// Short interval so Start exercises the ticker.
	sup := NewMemorySupervisor(s, gate, nil, &recordingSupervisorActions{}, 50*time.Millisecond)
	sup.Start()
	time.Sleep(150 * time.Millisecond)
	sup.Stop()
	sup.Stop() // idempotent
}

// newNarrowMemProject adds a second project with its own memory limit.
func newNarrowMemProject(t *testing.T, s *store.Store, id string, limitMB int) string {
	t.Helper()
	if err := s.CreateProject(&types.Project{
		ID: id, Name: id, Path: t.TempDir(),
		SandboxEnabled: true, SandboxMemoryLimit: limitMB,
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedRunningTaskIn(t *testing.T, s *store.Store, id, projectID string, peakMB int64) {
	t.Helper()
	now := time.Now().UTC()
	if err := s.CreateTask(&types.Task{
		ID: id, ProjectID: projectID, Title: id,
		Status: types.StatusInProgress, ExecutionState: types.ExecStateRunning,
		SandboxMemoryPeakMB: peakMB, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

// TestSupervisorResumeKeepsRunProvenance: the supervisor picks its victim by memory cost, so
// resuming one answers nothing about who is waiting on it. A subtask its user started by name must
// stay a run somebody is watching after a pause — that flag is what decides whether the parent's
// context answers for the user.
func TestSupervisorResumeKeepsRunProvenance(t *testing.T) {
	for _, tc := range []struct {
		name        string
		autostarted bool
		asSubtask   bool
	}{
		{"user_started_root", false, false},
		{"chain_started_root", true, false},
		{"user_started_subtask", false, true},
		{"chain_started_subtask", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			te, _, projectID := setupExecutorWithGitForSubtask(t)
			now := time.Now().UTC()
			const parentID = "parent-of-resumed"
			if err := te.store.CreateTask(&types.Task{
				ID: parentID, ProjectID: projectID, Title: "Parent", Prompt: "Parent prompt",
				Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle,
				CurrentThreadID: "thread-parent", LlamaServerURL: "http://localhost:8080",
				ModelName: "test-model", CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				t.Fatalf("create parent: %v", err)
			}

			taskID := "resumed-" + tc.name
			task := &types.Task{
				ID: taskID, ProjectID: projectID, Title: taskID, Prompt: "Task prompt",
				Status: types.StatusInProgress, ExecutionState: types.ExecStateIdle,
				CurrentThreadID: "thread-" + taskID, LlamaServerURL: "http://localhost:8080",
				ModelName: "test-model", Autostarted: tc.autostarted, CreatedAt: now, UpdatedAt: now,
			}
			if tc.asSubtask {
				task.ParentTaskID = parentID
			}
			if err := te.store.CreateTask(task); err != nil {
				t.Fatalf("create task: %v", err)
			}

			var err error
			if tc.asSubtask {
				err = te.startSubtask(projectID, taskID)
			} else {
				err = te.startTask(projectID, taskID)
			}
			if err != nil {
				t.Fatalf("resume: %v", err)
			}
			defer te.StopTask(taskID)

			got, err := te.store.GetTaskByID(taskID)
			if err != nil {
				t.Fatalf("read task row: %v", err)
			}
			if got.Autostarted != tc.autostarted {
				t.Errorf("Autostarted after resume = %v, want %v: the resume continues the paused run, "+
					"it is not a start of its own", got.Autostarted, tc.autostarted)
			}
		})
	}
}

func TestSupervisorNotesOnlyFollowASuccessfulTransition(t *testing.T) {
	t.Run("failed stop leaves no pause note", func(t *testing.T) {
		s := newMemSupTestStore(t)
		seedRunningTask(t, s, "task-a", 500)

		gate := &MemoryGate{}
		gate.SetCapacity(900)
		gate.SetMeasurements(500, 500, 90, 2000) // under the free-memory floor: pauses with an empty queue

		actions := &recordingSupervisorActions{stopErr: errors.New("executor gone")}
		NewMemorySupervisor(s, gate, fixedCharges{"task-a": 500}, actions, time.Second).TickOnce()

		if notes := actions.noteKinds(); len(notes) != 0 {
			t.Errorf("a stop that failed must record no note, got %v", notes)
		}
	})

	t.Run("failed resume leaves no note and keeps the flag", func(t *testing.T) {
		s := newMemSupTestStore(t)
		now := time.Now().UTC()
		if err := s.CreateTask(&types.Task{
			ID: "task-b", ProjectID: "proj-mem", Title: "task-b",
			Status: types.StatusInProgress, ExecutionState: types.ExecStatePaused,
			SandboxMemoryPeakMB: 200, MemorySupervisorPaused: true,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}

		gate := &MemoryGate{}
		gate.SetCapacity(2000)

		actions := &recordingSupervisorActions{startErr: errors.New("slot refused")}
		NewMemorySupervisor(s, gate, nil, actions, time.Second).TickOnce()

		if notes := actions.noteKinds(); len(notes) != 0 {
			t.Errorf("a resume that failed must record no note, got %v", notes)
		}
		got, err := s.GetTaskByID("task-b")
		if err != nil {
			t.Fatal(err)
		}
		if !got.MemorySupervisorPaused {
			t.Error("a resume that failed must leave the task supervisor-paused, not user-paused")
		}
	})
}

func TestMemoryTransitionNotesAreSystemRowsOnTheThread(t *testing.T) {
	te, s := newTestExecutorWithStore(t)
	now := time.Now().UTC()
	if err := s.CreateProject(&types.Project{ID: "proj-note", Name: "note", Path: t.TempDir(), SandboxEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(&types.Task{
		ID: "task-note", ProjectID: "proj-note", Title: "note",
		Status: types.StatusInProgress, ExecutionState: types.ExecStatePaused,
		MemorySupervisorPaused: true, CurrentThreadID: "thread-note",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateThread(&types.TaskThread{ID: "thread-note", TaskID: "task-note", Name: "Main", IsActive: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	te.noteMemoryPause("task-note")
	te.noteMemoryResume("task-note")

	rows, err := s.GetTaskMessages("task-note", "thread-note")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("want one row per transition, got %d: %+v", len(rows), rows)
	}
	pause, resume := rows[0], rows[1]
	if pause.Role != "system" || pause.Kind != memoryPauseNoteKind || pause.Severity != "warning" {
		t.Errorf("pause row = role %q kind %q severity %q, want system/%s/warning", pause.Role, pause.Kind, pause.Severity, memoryPauseNoteKind)
	}
	if !strings.Contains(pause.Content, "memory pressure") {
		t.Errorf("pause row must say why in its own text too (API clients read it, not the catalog): %q", pause.Content)
	}
	if resume.Role != "system" || resume.Kind != memoryResumeNoteKind || resume.Severity != "info" {
		t.Errorf("resume row = role %q kind %q severity %q, want system/%s/info", resume.Role, resume.Kind, resume.Severity, memoryResumeNoteKind)
	}

	got, err := s.GetTaskByID("task-note")
	if err != nil {
		t.Fatal(err)
	}
	if got.Output != memoryPauseOutput {
		t.Errorf("paused row's output = %q, want %q", got.Output, memoryPauseOutput)
	}
}

func TestMemoryPauseNoteSkipsARowTheSupervisorNoLongerOwns(t *testing.T) {
	te, s := newTestExecutorWithStore(t)
	now := time.Now().UTC()
	if err := s.CreateProject(&types.Project{ID: "proj-mine", Name: "mine", Path: t.TempDir(), SandboxEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(&types.Task{
		ID: "task-mine", ProjectID: "proj-mine", Title: "mine", Output: "Paused by user",
		Status: types.StatusInProgress, ExecutionState: types.ExecStatePaused,
		CurrentThreadID: "thread-mine", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateThread(&types.TaskThread{ID: "thread-mine", TaskID: "task-mine", Name: "Main", IsActive: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	te.noteMemoryPause("task-mine") // paused, but with no supervisor flag on the row

	rows, err := s.GetTaskMessages("task-mine", "thread-mine")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("a pause that is not the supervisor's must record no note, got %+v", rows)
	}
	got, err := s.GetTaskByID("task-mine")
	if err != nil {
		t.Fatal(err)
	}
	if got.Output != "Paused by user" {
		t.Errorf("output = %q, want the text whoever paused it wrote", got.Output)
	}
}

func TestMemoryTransitionNoteNeedsAThread(t *testing.T) {
	te, s := newTestExecutorWithStore(t)
	now := time.Now().UTC()
	if err := s.CreateProject(&types.Project{ID: "proj-nothread", Name: "nothread", Path: t.TempDir(), SandboxEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(&types.Task{
		ID: "task-nothread", ProjectID: "proj-nothread", Title: "nothread",
		Status: types.StatusInProgress, ExecutionState: types.ExecStatePaused,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	te.noteMemoryPause("task-nothread")

	rows, err := s.GetTaskMessages("task-nothread", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("a threadless task must record no note, got %+v", rows)
	}
}
