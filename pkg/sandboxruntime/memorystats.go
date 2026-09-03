package sandboxruntime

import "sync"

// MemoryUsageMB is one container's memory usage in whole megabytes — the unit every
// caller shows a human. The byte-to-MB conversion happens here and nowhere else.
type MemoryUsageMB struct {
	Known    bool   // false when nothing could be read: no container, no cgroup, or it stopped mid-poll
	Current  int64  // memory.current
	Limit    int64  // memory.max, 0 when unlimited or unknown
	Peak     int64  // memory.peak high-water mark
	OOMKills uint64 // oom_kill counter
}

// MemoryTracker answers "how much memory is this container using" for the two sandbox managers'
// status displays. Three rules: the cgroup directory costs a podman inspect plus a /proc read, so
// it is resolved once per container and reused; that path goes stale silently, because the cgroup
// disappears when the container stops, so a failed read must drop the cache rather than keep
// reading a dead path; and a container that stopped between polls is no data, not an error.
// Safe for concurrent use.
type MemoryTracker struct {
	// resolve finds a container's cgroup v2 directory; a field so the caching rules are
	// testable without podman.
	resolve func(name string) (string, error)

	mu   sync.Mutex
	dirs map[string]string // container name -> resolved cgroup directory
}

// NewMemoryTracker returns a tracker backed by r.
func NewMemoryTracker(r *Runtime) *MemoryTracker {
	return &MemoryTracker{resolve: r.ContainerCgroupDir, dirs: make(map[string]string)}
}

// Usage returns the container's memory usage. An empty containerName, a nil tracker, or
// an unreadable cgroup all answer "no data" rather than an error.
func (t *MemoryTracker) Usage(containerName string) MemoryUsageMB {
	if t == nil || containerName == "" {
		return MemoryUsageMB{}
	}

	t.mu.Lock()
	dir := t.dirs[containerName]
	t.mu.Unlock()

	if dir == "" {
		resolved, err := t.resolve(containerName)
		if err != nil {
			return MemoryUsageMB{}
		}
		// First resolver wins: caching a slower second resolve could pin a cgroup that
		// already belonged to a replaced container.
		t.mu.Lock()
		if existing := t.dirs[containerName]; existing != "" {
			dir = existing
		} else {
			t.dirs[containerName] = resolved
			dir = resolved
		}
		t.mu.Unlock()
	}

	mem, err := ReadCgroupMemory(dir)
	if err != nil {
		// The container stopped or was removed: drop the path so the next call resolves
		// again instead of reading a dead cgroup.
		t.Forget(containerName)
		return MemoryUsageMB{}
	}

	return MemoryUsageMB{
		Known:    true,
		Current:  mem.CurrentBytes >> 20,
		Limit:    mem.MaxBytes >> 20,
		Peak:     mem.PeakBytes >> 20,
		OOMKills: mem.OOMKills,
	}
}

// Forget drops a stopped or removed container's cached directory, so a name that comes
// back later resolves afresh. Safe on a nil tracker.
func (t *MemoryTracker) Forget(containerName string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	delete(t.dirs, containerName)
	t.mu.Unlock()
}
