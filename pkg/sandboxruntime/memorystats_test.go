package sandboxruntime

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

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

// TestMemoryTrackerUsage_ConvertsToMB: a caller sees whole MB, never bytes. Parsing is
// TestReadCgroupMemory's job, so the fixtures say MB and only the files hold bytes.
func TestMemoryTrackerUsage_ConvertsToMB(t *testing.T) {
	const mb = int64(1024 * 1024)
	dir := cgroupDir(t, map[string]string{
		"memory.current": mbString(100 * mb),
		"memory.peak":    mbString(200 * mb),
		"memory.max":     mbString(512 * mb),
		"memory.events":  "high 0\nmax 1\nlow 0\noom 0\noom_kill 3\n",
	})

	tr, _ := trackerWith(t, dir)
	got := tr.Usage("kanban-ext-1")

	want := MemoryUsageMB{Known: true, Current: 100, Limit: 512, Peak: 200, OOMKills: 3}
	if got != want {
		t.Errorf("Usage() = %+v, want %+v", got, want)
	}
}

// mbString renders a byte count the way a cgroup file holds it: decimal, newline
// terminated.
func mbString(bytes int64) string {
	return strconv.FormatInt(bytes, 10) + "\n"
}

// TestMemoryTrackerUsage_UnlimitedLimitHasNoNumber: "max" is not a number, so a
// caller must be able to tell "no enforced limit" from a limit of 0 MB.
func TestMemoryTrackerUsage_UnlimitedLimitHasNoNumber(t *testing.T) {
	dir := cgroupDir(t, map[string]string{
		"memory.current": "4194304\n", // 4 MB
		"memory.max":     "max\n",
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
	dir := cgroupDir(t, map[string]string{"memory.current": "1048576\n"})
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
	dir := cgroupDir(t, map[string]string{"memory.current": "1048576\n"})
	tr, calls := trackerWith(t, dir)

	if got := tr.Usage("c"); !got.Known {
		t.Fatal("first Usage() reported no data for a live cgroup")
	}
	// The container stops: the directory loses its controller files.
	if err := os.Remove(filepath.Join(dir, "memory.current")); err != nil {
		t.Fatalf("remove memory.current: %v", err)
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
	dir := cgroupDir(t, map[string]string{"memory.current": "1048576\n"})
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
	}

	if got := tr.Usage("gone"); got.Known {
		t.Error("Usage() reported data for an unresolvable container")
	}
	if len(tr.dirs) != 0 {
		t.Errorf("failed resolve left %d cached dirs, want 0", len(tr.dirs))
	}
}
