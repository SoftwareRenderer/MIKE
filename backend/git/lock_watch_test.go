package git

// Same hazard as the executor's watched twin, on the stash table: two stashers on different tables trade
// entries through a stack they both think they own.

import (
	"testing"
	"time"
)

func TestLockProjectStashWatchedQueuesOnTheSameTableAsLockProjectStash(t *testing.T) {
	t.Parallel()
	wm := NewWorktreeManager(nil, nil)
	const projectID = "proj-watched-stash"

	holder := wm.LockProjectStash(projectID)
	got := make(chan struct{})
	go func() {
		release := wm.LockProjectStashWatched(projectID, 5*time.Millisecond, func(time.Duration, bool) {})
		close(got)
		release()
	}()

	select {
	case <-got:
		t.Fatal("the watched waiter took the stash lock while the plain holder held it")
	case <-time.After(120 * time.Millisecond):
	}

	holder()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the watched waiter never proceeded after the release")
	}
}
