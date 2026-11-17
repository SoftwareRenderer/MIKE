package sandbox

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"mike/backend/testutil/sandboxcheck"
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

// TestStopReportsWhatTheCgroupSaid: one row per answer the tracker can give, and what Stop owes the
// sink and the tracker for it. A peak is reported with its kill count — a task that lost processes must
// still say how many once its container is gone — and a zero peak never silences the counter, because a
// host with no memory.peak has only that. Nothing to report is reported as nothing, so no run overwrites
// the previous one's numbers. Both no-report rows that still remove the container must Forget it: a
// cached cgroup path outlives its container. And no reading is paid for when nobody is listening, or when
// the task is not tracked — a reading costs a podman inspect.
func TestStopReportsWhatTheCgroupSaid(t *testing.T) {
	const tracked = "task-1"
	for _, tc := range []struct {
		name       string
		task       string // "" is the tracked task
		sink       bool
		usage      sandboxruntime.MemoryUsageMB
		wantReport []peakRecord
		wantReads  int
		wantForget int
	}{
		{
			name:       "a peak",
			sink:       true,
			usage:      sandboxruntime.MemoryUsageMB{Known: true, Current: 120, Limit: 1024, Peak: 700},
			wantReport: []peakRecord{{taskID: tracked, peakMB: 700}},
			wantReads:  1, wantForget: 1,
		},
		{
			name:       "a peak and the kills that came with it",
			sink:       true,
			usage:      sandboxruntime.MemoryUsageMB{Known: true, Current: 1000, Limit: 1024, Peak: 1024, OOMKills: 3},
			wantReport: []peakRecord{{taskID: tracked, peakMB: 1024, oomKills: 3}},
			wantReads:  1, wantForget: 1,
		},
		{
			name:       "kills with no peak to carry them",
			sink:       true,
			usage:      sandboxruntime.MemoryUsageMB{Known: true, Limit: 1024, OOMKills: 1},
			wantReport: []peakRecord{{taskID: tracked, oomKills: 1}},
			wantReads:  1, wantForget: 1,
		},
		{
			name:       "an unreadable cgroup",
			sink:       true,
			usage:      sandboxruntime.MemoryUsageMB{},
			wantReport: nil,
			wantReads:  1, wantForget: 1,
		},
		{
			name:       "a reading that peaked at nothing",
			sink:       true,
			usage:      sandboxruntime.MemoryUsageMB{Known: true, Current: 40, Limit: 1024},
			wantReport: nil,
			wantReads:  1, wantForget: 1,
		},
		{
			name:       "a task nothing tracks",
			task:       "task-unknown",
			sink:       true,
			usage:      sandboxruntime.MemoryUsageMB{Known: true, Peak: 700},
			wantReport: nil,
			wantReads:  0, wantForget: 0,
		},
		{
			name:       "nobody listening",
			sink:       false,
			usage:      sandboxruntime.MemoryUsageMB{Known: true, Peak: 700},
			wantReport: nil,
			wantReads:  0, wantForget: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen, sink := peakRecorder()
			var registered func(taskID string, peakMB int64, oomKills int64)
			if tc.sink {
				registered = sink
			}
			sm, fm := newPeakManager(tc.usage, registered)

			task := tc.task
			if task == "" {
				task = tracked
			}
			_ = sm.Stop(task)

			if got := seen(); !reflect.DeepEqual(got, tc.wantReport) {
				t.Errorf("reported %+v, want %+v", got, tc.wantReport)
			}
			reads, forgot := fm.counts()
			if reads != tc.wantReads {
				t.Errorf("tracker reads = %d, want %d", reads, tc.wantReads)
			}
			if len(forgot) != tc.wantForget {
				t.Errorf("Forget calls = %v, want %d — the container is gone whatever the reading said", forgot, tc.wantForget)
			}
		})
	}
}

// TestStopRecordsHighWaterNotLastSample: a run records the most its sandbox ever held, not what it
// held when the container stopped. Held RAM falls back between samples, so these differ.
func TestStopRecordsHighWaterNotLastSample(t *testing.T) {
	sm := NewSandboxManager(bootWithBridge(t))
	sandboxcheck.Require(t, BaseImage)

	taskID := "test-peak-highwater"
	// The smallest cap that leaves room for the 64 MB below plus the sandbox's own ~23 MB and the copy
	// the shell makes while building the string. A container is charged its cap by the memory budget, so
	// asking for 2 GB here would cost the budget 2 GB that is never used.
	if _, err := sm.Create(taskID, taskWorktreeDir(t), &types.Project{Name: "test-proj"}, SandboxOpts{MemoryLimit: 192}); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	t.Cleanup(func() { _ = sm.Stop(taskID) })

	// Hold a 64 MB string in a background shell, built by one command substitution. Doubling a string
	// reaches twice its target because the old copy and the new one are alive while copying: a 200 MB
	// doubling loop was measured peaking at 525 MB for no extra signal. Ending the exec session is what
	// ends the allocation.
	const hog = `( x=$(head -c 67108864 /dev/zero | tr "\0" a); sleep 30 ) & echo hog-started`
	if out, err := sm.Exec(taskID, hog); err != nil {
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
