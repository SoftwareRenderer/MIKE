package git

import (
	"context"
	"time"

	"mike/pkg/keylock"
)

// tryLockProjectWorktree waits up to timeout for the project's worktree lock.
// Returns (nil, false) on timeout.
func (wm *WorktreeManager) tryLockProjectWorktree(projectID string, timeout time.Duration) (func(), bool) {
	return wm.worktree.TryLock(projectID, timeout)
}

// LockProjectMerge acquires the per-project merge lock. Interleaved merges would
// clobber the index and checkout. Lock order: always acquired before the project stash lock.
// Wait is unbounded.
func (wm *WorktreeManager) LockProjectMerge(projectID string) func() {
	return wm.merge.Lock(projectID)
}

// LockProjectStash acquires the per-project stash lock. Stash is repo-wide, so
// operations must not interleave. Wait is unbounded.
func (wm *WorktreeManager) LockProjectStash(projectID string) func() {
	return wm.stash.Lock(projectID)
}

// LockProjectStashWatched is LockProjectStash with the wait reported for user-facing operations and the
// wait ended by the caller's context; see keylock.Table.LockWatchedCtx.
func (wm *WorktreeManager) LockProjectStashWatched(ctx context.Context, projectID string, every time.Duration, notice keylock.WaitNotice) (func(), bool) {
	return wm.stash.LockWatchedCtx(ctx, projectID, every, notice)
}
