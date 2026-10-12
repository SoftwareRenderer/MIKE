package exec

import (
	"sync"
)

// CommitQueue serializes commit-and-done operations per project.
type CommitQueue struct {
	mu       sync.Mutex
	projects map[string]chan struct{}
}

func NewCommitQueue() *CommitQueue {
	return &CommitQueue{
		projects: make(map[string]chan struct{}),
	}
}

// Acquire blocks until the project's previous commit operation completes,
// then returns a release function.
func (cq *CommitQueue) Acquire(projectID string) (release func()) {
	cq.mu.Lock()
	done, ok := cq.projects[projectID]
	if !ok {
		done = make(chan struct{}, 1)
		done <- struct{}{}
		cq.projects[projectID] = done
	}
	cq.mu.Unlock()

	<-done

	return func() {
		cq.mu.Lock()
		done := cq.projects[projectID]
		cq.mu.Unlock()
		done <- struct{}{}
	}
}
