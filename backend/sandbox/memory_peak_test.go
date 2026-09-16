package sandbox

import (
	"sync"
	"testing"
	"time"

	"mike/backend/types"
	"mike/pkg/sandboxruntime"
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

// TestStopRecordsHighWaterNotLastSample: a run records the most its sandbox ever held, not what it
// held when the container stopped. Held RAM falls back between samples, so these differ.
func TestStopRecordsHighWaterNotLastSample(t *testing.T) {
	sm := NewSandboxManager(bootWithBridge(t))
	if !sm.IsAvailable() {
		t.Skip("gVisor not available, skipping integration test")
	}

	taskID := "test-peak-highwater"
	// The smallest cap that leaves room for the 64 MB below plus the sandbox's own ~23 MB and the copy
	// the shell makes while building the string. A container is charged its cap by the memory budget, so
	// asking for 2 GB here would cost the budget 2 GB that is never used.
	if _, err := sm.Create(taskID, t.TempDir(), &types.Project{Name: "test-proj"}, SandboxOpts{MemoryLimit: 192}); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	t.Cleanup(func() { _ = sm.Stop(taskID) })

	// Hold a 64 MB string in a background shell, built by one command substitution. Doubling a string
	// reaches twice its target because the old copy and the new one are alive while copying: a 200 MB
	// doubling loop was measured peaking at 525 MB for no extra signal. Ending the exec session is what
	// ends the allocation.
	const hog = `( x=$(head -c 67108864 /dev/zero | tr "\0" a); sleep 30 ) & echo hog-started`
	if out, err := sm.Exec(taskID, hog, nil); err != nil {
		t.Fatalf("start memhog: %v (%s)", err, out)
	}

	waitFor := func(want func(*types.SandboxMemoryStats) bool, within time.Duration, why string) *types.SandboxMemoryStats {
		t.Helper()
		deadline := time.Now().Add(within)
		var stats *types.SandboxMemoryStats
		for time.Now().Before(deadline) {
			stats = sm.GetMemoryStats(taskID)
			if want(stats) {
				return stats
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s (last sample %+v)", why, stats)
		return nil
	}

	held := waitFor(func(s *types.SandboxMemoryStats) bool { return s.Running && s.CurrentMB >= 60 },
		30*time.Second, "the 64 MB allocation to be sampled")
	t.Logf("held: current=%d MB peak=%d MB", held.CurrentMB, held.PeakMB)

	// The RAM is returned. The comparison is against the peak this run reached rather than a fixed MB
	// count, because an idle sandbox holds memory for its own processes and that floor differs between
	// containers.
	t2 := waitFor(func(s *types.SandboxMemoryStats) bool { return s.PeakMB > 0 && s.CurrentMB*2 <= s.PeakMB },
		45*time.Second, "the guest to free the allocation")
	t.Logf("freed: running=%v current=%d MB peak=%d MB", t2.Running, t2.CurrentMB, t2.PeakMB)

	var reported []peakRecord
	record, sink := peakRecorder()
	sm.SetMemoryPeakSink(func(taskID string, peakMB int64, oomKills int64) {
		sink(taskID, peakMB, oomKills)
	})
	if err := sm.Stop(taskID); err != nil {
		t.Fatalf("stop sandbox: %v", err)
	}

	reported = record()
	if len(reported) != 1 {
		t.Fatalf("peak sink calls = %d, want 1: %+v", len(reported), reported)
	}
	t.Logf("sink reported: %+v", reported)
	got := reported[0]
	if got.taskID != taskID || got.peakMB < 60 {
		t.Errorf("recorded %s peak=%d MB, want this task's peak >= 60 MB (the 64 MB it held)", got.taskID, got.peakMB)
	}
	if got.oomKills != 0 {
		t.Errorf("recorded %d OOM kills, want 0 — the peak must come from the sandbox growing, "+
			"not from the cgroup killing it", got.oomKills)
	}
}
