package stream

// A checkpoint restore runs `checkout -f HEAD` → `clean -fd` → `read-tree --reset -u`, and a compute sampled
// inside that window holds nothing — the Changes panel renders that as a task with no changes. So while someone
// holds the task's worktree lock, the watcher publishes nothing and asks again until it settles.

import (
	"sync/atomic"
	"testing"
	"time"

	"mike/backend/testutil/testgit"
)

func TestWorktreeWatcherDefersWhileTheWorktreeIsBeingRewritten(t *testing.T) {
	ww, broadcasts, _, _, worktreePath := newUntrackedWatcherEnv(t)

	var rewriting atomic.Bool
	rewriting.Store(true)
	ww.mu.Lock()
	ww.busy = func(string) bool { return rewriting.Load() }
	ww.mu.Unlock()

	const name = "written-during-the-rewrite.txt"
	testgit.WriteFile(t, worktreePath, name, "written while the lock was held\n")

	// The fsnotify event that asked for this compute is already spent, so a deferral that is never re-armed
	// is a diff that never arrives. Two throttle windows pass with the lock held, and the watcher says nothing.
	select {
	case ev := <-broadcasts:
		t.Fatalf("published a diff while the worktree was being rewritten: hash=%q untracked=%d",
			ev.DiffHash, ev.UntrackedN)
	case <-time.After(2500 * time.Millisecond):
	}

	rewriting.Store(false)
	frame := waitForBroadcast(t, broadcasts, "the deferred compute once the rewrite ended")
	if frame.UntrackedN != 1 {
		t.Fatalf("the deferred compute reported untracked=%d, want 1: the settled state is what must be published",
			frame.UntrackedN)
	}
}
