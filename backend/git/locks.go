package git

import (
	"time"

	"mike/pkg/keylock"
)

// tryLockProjectWorktree waits up to timeout for the project's worktree lock and returns its release
// function. On timeout it returns (nil, false) without leaving the lock held and without leaving a
// waiter behind: the goroutine this used to abandon kept waiting on the mutex and unlocked whenever it
// eventually won, which made every later CreateWorktree for that project hit the same timeout.
func (wm *WorktreeManager) tryLockProjectWorktree(projectID string, timeout time.Duration) (func(), bool) {
	return wm.worktree.TryLock(projectID, timeout)
}

// LockProjectMerge acquires the per-project merge lock and returns the function that releases it.
// The main checkout has a single HEAD and index, so interleaved merges clobber each other's checkout,
// stashes and restore. Lock order: always acquired BEFORE (never after) the project stash lock.
//
// The wait is unbounded on purpose; see gitSlowWait.
func (wm *WorktreeManager) LockProjectMerge(projectID string) func() {
	return wm.merge.Lock(projectID)
}

// LockProjectStash acquires the per-project stash lock and returns the function that releases it.
// Stash is repo-wide - one stack for every worktree, holding the user's own entries - so stash
// operations must not interleave. Call it as `defer wm.LockProjectStash(projectID)()` when the
// critical section is the enclosing block; capture the release function when it is not.
//
// The wait is unbounded on purpose; see gitSlowWait.
func (wm *WorktreeManager) LockProjectStash(projectID string) func() {
	return wm.stash.Lock(projectID)
}

// LockProjectStashWatched is LockProjectStash with the wait reported, for a caller a person is waiting on.
func (wm *WorktreeManager) LockProjectStashWatched(projectID string, every time.Duration, notice keylock.WaitNotice) func() {
	return wm.stash.LockWatched(projectID, every, notice)
}
