package sandboxruntime

import "sync"

// MemoryUsageMB is one container's memory usage in whole megabytes — the unit every
// caller shows a human. The byte-to-MB conversion happens here and nowhere else.
type MemoryUsageMB struct {
	Known    bool   // false when nothing could be read: no container, no cgroup, or it stopped mid-poll
	Current  int64  // what the sandbox holds: its host heaps plus the guest's RAM, no page cache
	Limit    int64  // memory.max, 0 when unlimited or unknown
	Peak     int64  // high-water of Current since this container was first sampled
	OOMKills uint64 // oom_kill counter
}

// ChargeMB is what one container costs the memory budget: the memory it holds now, or its limit if
// that is larger. A container whose usage cannot be read is still charged its limit.
func ChargeMB(usage MemoryUsageMB, capMB int64) (heldMB, chargeMB int64) {
	if !usage.Known {
		return 0, capMB
	}
	charge := capMB
	if usage.Current > charge {
		charge = usage.Current
	}
	return usage.Current, charge
}

// MemoryTracker answers "how much memory is this container using" for the sandbox managers and the
// memory budget. A resolved cgroup directory is cached, and a read that fails drops it, so a path
// left behind by a stopped container is not read forever. The peak is a high-water mark MIKE keeps
// itself, because the kernel's memory.peak counts page cache and cannot be reset. Safe for
// concurrent use.
type MemoryTracker struct {
	// resolve finds a container's cgroup v2 directory; a field so the caching rules are
	// testable without podman.
	resolve func(name string) (string, error)

	mu   sync.Mutex
	dirs map[string]string // container name -> resolved cgroup directory
	peak map[string]int64  // container name -> high-water anon in bytes
}

// NewMemoryTracker returns a tracker backed by r.
func NewMemoryTracker(r *Runtime) *MemoryTracker {
	return &MemoryTracker{
		resolve: r.ContainerCgroupDir,
		dirs:    make(map[string]string),
		peak:    make(map[string]int64),
	}
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
		Current:  mem.HeldBytes() >> 20,
		Limit:    mem.MaxBytes >> 20,
		Peak:     t.raisePeak(containerName, mem.HeldBytes()) >> 20,
		OOMKills: mem.OOMKills,
	}
}

// raisePeak records one sample. The high-water mark of a container is never lowered by a later
// sample.
func (t *MemoryTracker) raisePeak(containerName string, anonBytes int64) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.peak == nil {
		t.peak = make(map[string]int64)
	}
	if anonBytes > t.peak[containerName] {
		t.peak[containerName] = anonBytes
	}
	return t.peak[containerName]
}

// Forget drops a container's cached directory and high-water mark, so that a name reused by a new
// container is resolved afresh. Safe on a nil tracker.
func (t *MemoryTracker) Forget(containerName string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	delete(t.dirs, containerName)
	delete(t.peak, containerName)
	t.mu.Unlock()
}
