package stream

// The two frames a waiting click gets: what it is queued behind, then that it got in.

import (
	"testing"
	"time"

	"mike/backend/types"
)

func isLockWait(frame map[string]any) bool {
	return frame["type"] == "task_event" && frame["event"] == string(types.WSTypeLockWait)
}

func TestLockWaitNotifierAnnouncesTheWaitThenTheAcquisition(t *testing.T) {
	_, s, _, wm, _ := setupDoneSnapshotTest(t)
	asm := NewAppStreamManager(s, wm)
	t.Cleanup(asm.Close)

	const projectID, taskID = "proj-lock", "task-lock"
	c := dialStream(t, asm)
	client := clientAlone(t, asm)
	c.sendSubscription(t, "subscribe_task", projectID, taskID)
	// Applied before the first notice, or the first frame is the one that races the server's own
	// read loop rather than the wait it is meant to announce.
	awaitTaskSub(t, asm, client, projectID, taskID)

	notice := asm.LockWaitNotifier(projectID, taskID, "worktree")
	notice(12*time.Second, false)

	frame := c.awaitFrame(t, 5*time.Second, isLockWait, "the wait announcement")
	data, _ := frame["data"].(map[string]any)
	if data["domain"] != "worktree" {
		t.Errorf("data.domain = %v, want worktree", data["domain"])
	}
	if data["waited_seconds"] != float64(12) {
		t.Errorf("data.waited_seconds = %v, want 12 (how long they have stood there is the useful number)", data["waited_seconds"])
	}
	if data["acquired"] != false {
		t.Errorf("data.acquired = %v, want false while still waiting", data["acquired"])
	}

	notice(13*time.Second, true)
	frame = c.awaitFrame(t, 5*time.Second, func(f map[string]any) bool {
		if !isLockWait(f) {
			return false
		}
		d, _ := f["data"].(map[string]any)
		return d["acquired"] == true
	}, "the clearing frame")
	data, _ = frame["data"].(map[string]any)
	if data["acquired"] != true {
		t.Fatal("the acquired frame did not arrive; a browser left without it shows a spinner until reload")
	}
}
