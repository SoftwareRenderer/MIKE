package backend

import (
	"sync"
)

// CommitQueue serializes commit-and-done operations per project.
// Each project has its own lock; different projects can proceed in parallel.
type CommitQueue struct {
	mu       sync.Mutex
	projects map[string]chan struct{}
}

// NewCommitQueue creates a new CommitQueue.
func NewCommitQueue() *CommitQueue {
	return &CommitQueue{
		projects: make(map[string]chan struct{}),
	}
}

// Acquire blocks until the project's previous commit operation completes,
// then returns a release function that must be called when done.
// The first acquire for a project always succeeds immediately.
func (cq *CommitQueue) Acquire(projectID string) (release func()) {
	cq.mu.Lock()
	done, ok := cq.projects[projectID]
	if !ok {
		// Buffered channel: first acquire fills it and proceeds immediately.
		// Subsequent acquires block on the empty buffer until release fills it.
		done = make(chan struct{}, 1)
		done <- struct{}{}
		cq.projects[projectID] = done
	}
	cq.mu.Unlock()

	<-done // block if buffer is empty (previous op hasn't released yet)

	return func() {
		cq.mu.Lock()
		done := cq.projects[projectID]
		cq.mu.Unlock()
		done <- struct{}{} // fill buffer, unblock next waiter
	}
}
