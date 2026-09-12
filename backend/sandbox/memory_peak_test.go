package sandbox

import (
	"sync"
	"testing"

	"kanban/pkg/sandboxruntime"
)

// fakeMem answers a container's usage and records the Forget calls, so the teardown reading is
// testable without podman.
type fakeMem struct {
	mu     sync.Mutex
	usage  sandboxruntime.MemoryUsageMB
	reads  int
	forgot []string
}

func (f *fakeMem) Usage(string) sandboxruntime.MemoryUsageMB {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.usage
}

func (f *fakeMem) Forget(containerName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgot = append(f.forgot, containerName)
}

func (f *fakeMem) counts() (reads int, forgot []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads, append([]string(nil), f.forgot...)
}

type peakRecord struct {
	taskID   string
	peakMB   int64
	oomKills int64
}

func peakRecorder() (func() []peakRecord, func(taskID string, peakMB int64, oomKills int64)) {
	var mu sync.Mutex
	var got []peakRecord
	return func() []peakRecord {
			mu.Lock()
			defer mu.Unlock()
			return append([]peakRecord(nil), got...)
		}, func(taskID string, peakMB int64, oomKills int64) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, peakRecord{taskID, peakMB, oomKills})
		}
}

// newPeakManager builds a manager tracking one container that exists nowhere: what is under test
// is what Stop reads and reports before the removal, not podman's answer.
func newPeakManager(usage sandboxruntime.MemoryUsageMB, sink func(taskID string, peakMB int64, oomKills int64)) (*SandboxManager, *fakeMem) {
	fm := &fakeMem{usage: usage}
	sm := &SandboxManager{
		rt:     sandboxruntime.NewRuntimeWithBoot(sandboxruntime.Boot{}),
		active: map[string]string{"task-1": "kanban-sandbox-peak-test"},
		mem:    fm,
	}
	sm.SetMemoryPeakSink(sink)
	return sm, fm
}

func TestStopReportsFinalPeakToSink(t *testing.T) {
	seen, sink := peakRecorder()
	sm, fm := newPeakManager(sandboxruntime.MemoryUsageMB{Known: true, Current: 120, Limit: 1024, Peak: 700}, sink)

	_ = sm.Stop("task-1")

	got := seen()
	if len(got) != 1 {
		t.Fatalf("Stop reported %d peaks, want 1: %+v", len(got), got)
	}
	if got[0] != (peakRecord{taskID: "task-1", peakMB: 700}) {
		t.Errorf("reported %+v, want task-1 / 700 MB", got[0])
	}
	if _, forgot := fm.counts(); len(forgot) != 1 || forgot[0] != "kanban-sandbox-peak-test" {
		t.Errorf("Forget calls = %v, want the removed container — a cached cgroup path outlives its container", forgot)
	}
}

// A task that lost processes must still say how many once its container is gone.
func TestStopReportsFinalOOMKillsWithThePeak(t *testing.T) {
	seen, sink := peakRecorder()
	sm, _ := newPeakManager(sandboxruntime.MemoryUsageMB{Known: true, Current: 1000, Limit: 1024, Peak: 1024, OOMKills: 3}, sink)

	_ = sm.Stop("task-1")

	got := seen()
	if len(got) != 1 {
		t.Fatalf("Stop reported %d peaks, want 1: %+v", len(got), got)
	}
	if got[0] != (peakRecord{taskID: "task-1", peakMB: 1024, oomKills: 3}) {
		t.Errorf("reported %+v, want task-1 / 1024 MB / 3 kills", got[0])
	}
}

// A host with no memory.peak has only the kill counter, so a zero peak must not silence it.
func TestStopReportsOOMKillsWithoutAPeak(t *testing.T) {
	seen, sink := peakRecorder()
	sm, _ := newPeakManager(sandboxruntime.MemoryUsageMB{Known: true, Limit: 1024, OOMKills: 1}, sink)

	_ = sm.Stop("task-1")

	if got := seen(); len(got) != 1 || got[0].oomKills != 1 || got[0].peakMB != 0 {
		t.Errorf("reported %+v, want one record with the kill count and no peak", got)
	}
}

// No peak and no kill: neither may overwrite a previous run's numbers.
func TestStopReportsNothingWithoutAPeakOrAKill(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage sandboxruntime.MemoryUsageMB
	}{
		{"unreadable cgroup", sandboxruntime.MemoryUsageMB{}},
		{"zero peak", sandboxruntime.MemoryUsageMB{Known: true, Current: 40, Limit: 1024}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen, sink := peakRecorder()
			sm, fm := newPeakManager(tc.usage, sink)

			_ = sm.Stop("task-1")

			if got := seen(); len(got) != 0 {
				t.Errorf("reported %+v, want nothing", got)
			}
			if _, forgot := fm.counts(); len(forgot) != 1 {
				t.Errorf("Forget calls = %v, want 1 — the container is gone whatever the reading said", forgot)
			}
		})
	}
}

// An untracked task has nothing to read: no report, and no cgroup lookup either.
func TestStopOfUnknownTaskReportsNothing(t *testing.T) {
	seen, sink := peakRecorder()
	sm, fm := newPeakManager(sandboxruntime.MemoryUsageMB{Known: true, Peak: 700}, sink)

	_ = sm.Stop("task-unknown")

	if got := seen(); len(got) != 0 {
		t.Errorf("reported %+v for an untracked task, want nothing", got)
	}
	if reads, _ := fm.counts(); reads != 0 {
		t.Errorf("tracker reads = %d for an untracked task, want 0", reads)
	}
}

// With nobody listening, tearing a container down must not pay for a reading.
func TestStopWithoutSinkReadsNothing(t *testing.T) {
	sm, fm := newPeakManager(sandboxruntime.MemoryUsageMB{Known: true, Peak: 700}, nil)

	_ = sm.Stop("task-1")

	reads, forgot := fm.counts()
	if reads != 0 {
		t.Errorf("tracker reads = %d with no sink registered, want 0 — a reading costs a podman inspect", reads)
	}
	if len(forgot) != 1 {
		t.Errorf("Forget calls = %d, want 1", len(forgot))
	}
}
