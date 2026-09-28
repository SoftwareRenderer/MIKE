package stream

import (
	"errors"
	"sync"
	"testing"

	"mike/backend/types"
)

type fakeStatsSource struct {
	ids   []string
	stats map[string]*types.SandboxMemoryStats
}

func (f *fakeStatsSource) ActiveTaskIDs() []string { return f.ids }
func (f *fakeStatsSource) GetMemoryStats(taskID string) *types.SandboxMemoryStats {
	if s, ok := f.stats[taskID]; ok {
		return s
	}
	return &types.SandboxMemoryStats{} // tracked but container not running
}

type fakeTaskLookup map[string]*types.Task

func (f fakeTaskLookup) GetTaskByID(taskID string) (*types.Task, error) {
	if t, ok := f[taskID]; ok {
		return t, nil
	}
	return nil, errors.New("task not found")
}

type broadcastEvent struct {
	projectID string
	taskID    string
	event     types.StreamEvent
}

type fakeBroadcaster struct {
	mu     sync.Mutex
	events []broadcastEvent
}

func (f *fakeBroadcaster) SendTaskEvent(projectID, taskID string, event types.StreamEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, broadcastEvent{projectID: projectID, taskID: taskID, event: event})
}

func (f *fakeBroadcaster) eventsSnapshot() []broadcastEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]broadcastEvent, len(f.events))
	copy(out, f.events)
	return out
}

func newTestSampler(source *fakeStatsSource, tasks fakeTaskLookup, b *fakeBroadcaster) *SandboxStatsSampler {
	return NewSandboxStatsSampler(source, tasks, b, 0, 0)
}

func TestSandboxStatsSampler_PushOnChangeOnly(t *testing.T) {
	source := &fakeStatsSource{
		ids: []string{"task-1"},
		stats: map[string]*types.SandboxMemoryStats{
			"task-1": {Running: true, CurrentMB: 100, LimitMB: 512},
		},
	}
	tasks := fakeTaskLookup{
		"task-1": {ID: "task-1", ProjectID: "proj-1", Status: types.StatusInProgress},
	}
	b := &fakeBroadcaster{}
	s := newTestSampler(source, tasks, b)

	s.SampleOnce()
	s.publish()
	events := b.eventsSnapshot()
	if len(events) != 1 {
		t.Fatalf("first tick: got %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.projectID != "proj-1" || ev.taskID != "task-1" || ev.event.Type != types.WSTypeSandboxStats {
		t.Fatalf("unexpected event: %+v", ev)
	}
	if got := ev.event.Data.(*types.SandboxMemoryStats); got.CurrentMB != 100 || got.LimitMB != 512 {
		t.Fatalf("unexpected stats: %+v", got)
	}

	// Unchanged value: no re-push.
	s.SampleOnce()
	s.publish()
	if got := len(b.eventsSnapshot()); got != 1 {
		t.Fatalf("unchanged tick: got %d events, want 1 (no re-push)", got)
	}

	// Changed value: one more push with the new value.
	source.stats["task-1"] = &types.SandboxMemoryStats{Running: true, CurrentMB: 200, LimitMB: 512}
	s.SampleOnce()
	s.publish()
	events = b.eventsSnapshot()
	if len(events) != 2 {
		t.Fatalf("changed tick: got %d events, want 2", len(events))
	}
	if got := events[1].event.Data.(*types.SandboxMemoryStats); got.CurrentMB != 200 {
		t.Fatalf("second push stats = %+v, want CurrentMB 200", got)
	}

	latest := s.Latest("task-1")
	if latest == nil || latest.CurrentMB != 200 {
		t.Fatalf("Latest = %+v, want CurrentMB 200", latest)
	}
}

// TestSandboxStatsSampler_SkipsContainersWithNoTaskRow: the project's terminal container shares the
// container map with task containers, and nothing can be said about which task its memory belongs to.
func TestSandboxStatsSampler_SkipsContainersWithNoTaskRow(t *testing.T) {
	source := &fakeStatsSource{
		ids: []string{"ghost-task", "proj:proj-1"},
		stats: map[string]*types.SandboxMemoryStats{
			"ghost-task":  {Running: true, CurrentMB: 100, LimitMB: 512},
			"proj:proj-1": {Running: true, CurrentMB: 100, LimitMB: 512},
		},
	}
	tasks := fakeTaskLookup{} // neither ID has a row
	b := &fakeBroadcaster{}
	s := newTestSampler(source, tasks, b)

	s.SampleOnce()
	s.publish()
	if got := len(b.eventsSnapshot()); got != 0 {
		t.Fatalf("got %d events for containers with no task row, want 0", got)
	}
	if latest := s.Latest("proj:proj-1"); latest != nil {
		t.Fatalf("Latest(proj:…) = %+v, want nil (never sampled)", latest)
	}
}

// TestSandboxStatsSampler_IdleTaskWithContainerIsTracked: a task that went idle with its container
// alive still holds RAM, and the budget decides admissions from these samples. Skipping it would let
// the container grow to its cap unmeasured.
func TestSandboxStatsSampler_IdleTaskWithContainerIsTracked(t *testing.T) {
	source := &fakeStatsSource{
		ids: []string{"idle-task"},
		stats: map[string]*types.SandboxMemoryStats{
			"idle-task": {Running: true, CurrentMB: 100, LimitMB: 512},
		},
	}
	tasks := fakeTaskLookup{
		"idle-task": {ID: "idle-task", ProjectID: "proj-1", Status: types.StatusReview},
	}
	b := &fakeBroadcaster{}
	s := newTestSampler(source, tasks, b)

	s.SampleOnce()
	s.publish()
	events := b.eventsSnapshot()
	if len(events) != 1 || events[0].projectID != "proj-1" {
		t.Fatalf("got %d events for an idle task with a live container, want 1: %+v", len(events), events)
	}

	// It grows while idle: the new number is sampled and pushed.
	source.stats["idle-task"] = &types.SandboxMemoryStats{Running: true, CurrentMB: 400, LimitMB: 512}
	s.SampleOnce()
	s.publish()
	events = b.eventsSnapshot()
	if len(events) != 2 {
		t.Fatalf("got %d events after idle growth, want 2", len(events))
	}
	if latest := s.Latest("idle-task"); latest == nil || latest.CurrentMB != 400 {
		t.Fatalf("Latest = %+v, want CurrentMB 400", latest)
	}
}

func TestSandboxStatsSampler_LatestRetainedAfterIdle(t *testing.T) {
	source := &fakeStatsSource{
		ids: []string{"task-1"},
		stats: map[string]*types.SandboxMemoryStats{
			"task-1": {Running: true, CurrentMB: 300, LimitMB: 512, OOMKills: 1},
		},
	}
	tasks := fakeTaskLookup{
		"task-1": {ID: "task-1", ProjectID: "proj-1", Status: types.StatusInProgress},
	}
	b := &fakeBroadcaster{}
	s := newTestSampler(source, tasks, b)

	s.SampleOnce()
	s.publish()
	if got := len(b.eventsSnapshot()); got != 1 {
		t.Fatalf("first tick: got %d events, want 1", got)
	}

	// Task goes idle with the container's value unchanged: nothing new is pushed, and the most
	// recent value is retained for a client that subscribes now.
	tasks["task-1"] = &types.Task{ID: "task-1", ProjectID: "proj-1", Status: types.StatusReview}
	s.SampleOnce()
	s.publish()
	if got := len(b.eventsSnapshot()); got != 1 {
		t.Fatalf("idle tick: got %d events, want 1 (no new pushes)", got)
	}
	latest := s.Latest("task-1")
	if latest == nil || latest.CurrentMB != 300 || latest.OOMKills != 1 {
		t.Fatalf("Latest after idle = %+v, want CurrentMB 300, OOMKills 1", latest)
	}

	// The provider returns the retained value (and only for known tasks).
	provider := s.LatestStatsProvider()
	if got := provider("proj-1", "task-1"); got == nil || got.CurrentMB != 300 {
		t.Fatalf("provider = %+v, want retained stats", got)
	}
	if got := provider("proj-1", "unknown"); got != nil {
		t.Fatalf("provider(unknown) = %+v, want nil", got)
	}
}

func TestSandboxStatsSampler_NotRunningSampledOnce(t *testing.T) {
	// In-progress task whose container is not running: the not-running value
	// is pushed once, then not repeated.
	source := &fakeStatsSource{ids: []string{"task-1"}} // no stats entry
	tasks := fakeTaskLookup{
		"task-1": {ID: "task-1", ProjectID: "proj-1", Status: types.StatusInProgress},
	}
	b := &fakeBroadcaster{}
	s := newTestSampler(source, tasks, b)

	s.SampleOnce()
	s.publish()
	events := b.eventsSnapshot()
	if len(events) != 1 {
		t.Fatalf("first tick: got %d events, want 1", len(events))
	}
	if got := events[0].event.Data.(*types.SandboxMemoryStats); got.Running {
		t.Fatalf("expected not-running stats, got %+v", got)
	}

	s.SampleOnce()
	s.publish()
	if got := len(b.eventsSnapshot()); got != 1 {
		t.Fatalf("second tick: got %d events, want 1 (unchanged)", got)
	}
}

// countingMeasure answers the budget with fixed numbers and counts how often it was asked.
type countingMeasure struct{ calls int }

func (m *countingMeasure) Measure() (int64, int64, int64, int64, bool) {
	m.calls++
	return 512, 300, 4096, 15000, true
}

// TestSamplerMeasuresTheBudgetOncePerSample checks that the budget is measured once per sample
// tick.
func TestSamplerMeasuresTheBudgetOncePerSample(t *testing.T) {
	s := newTestSampler(&fakeStatsSource{ids: []string{"task-1"}}, fakeTaskLookup{}, &fakeBroadcaster{})
	m := &countingMeasure{}
	var got [4]int64
	s.SetBudget(m, func(mikeMB, heldMB, availableMB, totalMB int64) {
		got = [4]int64{mikeMB, heldMB, availableMB, totalMB}
	})

	s.SampleOnce()
	s.SampleOnce()

	if m.calls != 2 {
		t.Errorf("budget measured %d times, want one measure per sample tick", m.calls)
	}
	if got != [4]int64{512, 300, 4096, 15000} {
		t.Errorf("sink received %v, want the measured numbers unchanged", got)
	}
}

// A host that cannot be read is passed on as unmeasured rather than as a host holding nothing.
func TestSamplerSkipsUnmeasuredBudget(t *testing.T) {
	s := newTestSampler(&fakeStatsSource{}, fakeTaskLookup{}, &fakeBroadcaster{})
	s.SetBudget(unmeasuredBudget{}, func(_, _, _, _ int64) {
		t.Error("an unmeasured host must not push numbers into the budget")
	})
	s.SampleOnce()
}

type unmeasuredBudget struct{}

func (unmeasuredBudget) Measure() (int64, int64, int64, int64, bool) {
	return 0, 0, 0, 0, false
}
