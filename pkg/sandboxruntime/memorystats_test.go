package sandboxruntime

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

const mbBytes = int64(1024 * 1024)

// trackerWith returns a tracker whose resolver hands out dir for every name, plus a
// counter of how often it was asked.
func trackerWith(t *testing.T, dir string) (*MemoryTracker, func() int) {
	t.Helper()
	calls := 0
	tr := &MemoryTracker{
		resolve: func(string) (string, error) {
			calls++
			return dir, nil
		},
		dirs: make(map[string]string),
		peak: make(map[string]int64),
	}
	return tr, func() int { return calls }
}

func cgroupDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// statFile renders a memory.stat file with the lines the reader uses.
func statFile(anonMB, shmemMB, hostCacheMB int64) string {
	return "anon " + mbString(anonMB*mbBytes) +
		"file " + mbString((shmemMB+hostCacheMB)*mbBytes) +
		"shmem " + mbString(shmemMB*mbBytes) +
		"kernel " + mbString(4*mbBytes) + "\nslab_reclaimable " + mbString(2*mbBytes) + "\n"
}

// TestMemoryTrackerUsage_ConvertsToMB: a caller sees whole MB, never bytes. Parsing is
// TestReadCgroupMemory's job, so the fixtures say MB and only the files hold bytes.
func TestMemoryTrackerUsage_ConvertsToMB(t *testing.T) {
	dir := cgroupDir(t, map[string]string{
		"memory.stat":   statFile(40, 60, 900),
		"memory.max":    mbString(512 * mbBytes),
		"memory.events": "high 0\nmax 1\nlow 0\noom 0\noom_kill 3\n",
	})

	tr, _ := trackerWith(t, dir)
	got := tr.Usage("kanban-ext-1")

	want := MemoryUsageMB{Known: true, Current: 100, Limit: 512, Peak: 100, OOMKills: 3}
	if got != want {
		t.Errorf("Usage() = %+v, want %+v", got, want)
	}
}

// mbString renders a byte count the way a cgroup file holds it: decimal, newline
// terminated.
func mbString(bytes int64) string {
	return strconv.FormatInt(bytes, 10) + "\n"
}

// TestMemoryTrackerPeak_CountsGuestRAMNotHostCache checks that memory allocated inside the guest
// raises the peak, and that the page cache left behind by a file the container wrote does not.
func TestMemoryTrackerPeak_CountsGuestRAMNotHostCache(t *testing.T) {
	dir := cgroupDir(t, map[string]string{"memory.stat": statFile(17, 0, 0)})
	tr, _ := trackerWith(t, dir)

	if got := tr.Usage("c"); got.Peak != 17 {
		t.Fatalf("first Peak = %d, want 17 (the sentry's own heap)", got.Peak)
	}

	// The task's processes allocate 200 MB inside the guest.
	writeFile(t, dir, "memory.stat", statFile(17, 200, 0))
	if got := tr.Usage("c"); got.Current != 217 || got.Peak != 217 {
		t.Errorf("after a 200 MB guest allocation Usage() = %+v, want Current and Peak 217", got)
	}

	// It then writes a 250 MB file: the host caches those pages against this cgroup.
	writeFile(t, dir, "memory.stat", statFile(17, 200, 250))
	if got := tr.Usage("c"); got.Current != 217 || got.Peak != 217 {
		t.Errorf("after the write Usage() = %+v, want 217 — page cache is not memory the task holds", got)
	}

	// The guest frees its memory, but the high-water mark stays where it was.
	writeFile(t, dir, "memory.stat", statFile(17, 0, 0))
	if got := tr.Usage("c"); got.Current != 17 {
		t.Errorf("Current = %d after the guest freed, want 17", got.Current)
	}
	if got := tr.Usage("c"); got.Peak != 217 {
		t.Errorf("Peak = %d, want 217: a high-water is not lowered by a smaller sample", got.Peak)
	}
}

// TestMemoryTrackerPeak_PerContainer checks that one container's high-water mark is not charged
// to another.
func TestMemoryTrackerPeak_PerContainer(t *testing.T) {
	dir := cgroupDir(t, map[string]string{"memory.stat": statFile(17, 283, 0)})
	tr, _ := trackerWith(t, dir)

	if got := tr.Usage("big-task"); got.Peak != 300 {
		t.Fatalf("big-task Peak = %d, want 300", got.Peak)
	}
	writeFile(t, dir, "memory.stat", statFile(17, 3, 0))
	if got := tr.Usage("small-task"); got.Peak != 20 {
		t.Errorf("small-task Peak = %d, want 20 (the peak is per container)", got.Peak)
	}
	if got := tr.Usage("big-task"); got.Peak != 300 {
		t.Errorf("big-task Peak = %d on re-read, want 300", got.Peak)
	}
}

// TestMemoryTrackerForget_DropsPeak checks that a container reusing a name starts from a zero peak.
func TestMemoryTrackerForget_DropsPeak(t *testing.T) {
	dir := cgroupDir(t, map[string]string{"memory.stat": statFile(17, 683, 0)})
	tr, _ := trackerWith(t, dir)

	if got := tr.Usage("c"); got.Peak != 700 {
		t.Fatalf("Peak = %d, want 700", got.Peak)
	}
	tr.Forget("c")
	writeFile(t, dir, "memory.stat", statFile(17, 13, 0))
	if got := tr.Usage("c"); got.Peak != 30 {
		t.Errorf("Peak = %d after Forget, want 30 (the old run's peak went with it)", got.Peak)
	}
}

// TestMemoryTrackerUsage_UnlimitedLimitHasNoNumber: "max" is not a number, so a
// caller must be able to tell "no enforced limit" from a limit of 0 MB.
func TestMemoryTrackerUsage_UnlimitedLimitHasNoNumber(t *testing.T) {
	dir := cgroupDir(t, map[string]string{
		"memory.stat": statFile(4, 0, 0),
		"memory.max":  "max\n",
	})

	tr, _ := trackerWith(t, dir)
	got := tr.Usage("c")
	if !got.Known || got.Current != 4 || got.Limit != 0 {
		t.Errorf("Usage() = %+v, want Known with Current=4 and no limit", got)
	}
}

// TestMemoryTrackerUsage_ResolvesDirectoryOnce: resolving costs a podman inspect plus a
// /proc read, and status displays poll.
func TestMemoryTrackerUsage_ResolvesDirectoryOnce(t *testing.T) {
	dir := cgroupDir(t, map[string]string{"memory.stat": statFile(1, 0, 0)})
	tr, calls := trackerWith(t, dir)

	for i := 0; i < 5; i++ {
		if got := tr.Usage("busy-container"); !got.Known {
			t.Fatalf("call %d: Usage() reported no data", i)
		}
	}
	if n := calls(); n != 1 {
		t.Errorf("resolver called %d times, want 1 (the directory is cached)", n)
	}
}

// TestMemoryTrackerUsage_DroppedWhenCgroupVanishes: keeping the path of a stopped
// container makes every later poll read a dead directory.
func TestMemoryTrackerUsage_DroppedWhenCgroupVanishes(t *testing.T) {
	dir := cgroupDir(t, map[string]string{"memory.stat": statFile(1, 0, 0)})
	tr, calls := trackerWith(t, dir)

	if got := tr.Usage("c"); !got.Known {
		t.Fatal("first Usage() reported no data for a live cgroup")
	}
	// The container stops: the directory loses its controller files.
	if err := os.Remove(filepath.Join(dir, "memory.stat")); err != nil {
		t.Fatalf("remove memory.stat: %v", err)
	}
	if got := tr.Usage("c"); got.Known {
		t.Error("Usage() reported data after the cgroup went away")
	}
	if n := calls(); n != 1 {
		t.Fatalf("resolver calls = %d after the failed read, want the cache already dropped", n)
	}

	// And the next call goes back to asking, rather than reusing the dead path.
	tr.Usage("c")
	if n := calls(); n != 2 {
		t.Errorf("resolver calls = %d, want 2 (a failed read must invalidate the cache)", n)
	}
}

// TestMemoryTrackerForget: a stopped container's name can come back as a different
// container, whose cgroup directory is elsewhere.
func TestMemoryTrackerForget(t *testing.T) {
	dir := cgroupDir(t, map[string]string{"memory.stat": statFile(1, 0, 0)})
	tr, calls := trackerWith(t, dir)

	tr.Usage("c")
	tr.Forget("c")
	tr.Usage("c")

	if n := calls(); n != 2 {
		t.Errorf("resolver calls = %d after Forget, want 2", n)
	}
}

// TestMemoryTrackerUsage_NoContainer: a key with no container behind it is no data, and
// must not resolve.
func TestMemoryTrackerUsage_NoContainer(t *testing.T) {
	tr, calls := trackerWith(t, t.TempDir())

	if got := tr.Usage(""); got.Known {
		t.Error("Usage(\"\") reported data")
	}
	if n := calls(); n != 0 {
		t.Errorf("resolver called %d times for an empty name, want 0", n)
	}
}

// TestMemoryTrackerNil: a manager built without its constructor — how this repo's tests
// avoid the gVisor probe — has no tracker, and cleanup must not panic over it.
func TestMemoryTrackerNil(t *testing.T) {
	var tr *MemoryTracker

	if got := tr.Usage("anything"); got.Known {
		t.Error("nil tracker reported data")
	}
	tr.Forget("anything") // must not panic
}

// TestMemoryTrackerUsage_Unresolvable: a container that is gone for good answers no data
// and caches nothing.
func TestMemoryTrackerUsage_Unresolvable(t *testing.T) {
	calls := 0
	tr := &MemoryTracker{
		resolve: func(string) (string, error) {
			calls++
			return "", os.ErrNotExist
		},
		dirs: make(map[string]string),
		peak: make(map[string]int64),
	}

	if got := tr.Usage("gone"); got.Known {
		t.Error("Usage() reported data for an unresolvable container")
	}
	if len(tr.dirs) != 0 {
		t.Errorf("failed resolve left %d cached dirs, want 0", len(tr.dirs))
	}
	if len(tr.peak) != 0 {
		t.Errorf("failed resolve left %d cached peaks, want 0", len(tr.peak))
	}
}

// writeFile replaces one file of a fixture cgroup directory, standing in for what the
// container did between two samples.
func writeFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
