package stream

// One connection holds a subscription per key, so one unsubscribe leaves nothing behind, and
// the work a subscription starts — reading a project's initial data, replaying a task buffer —
// belongs to the key, not to the frame that arrived.

import (
	"testing"
	"time"
)

// TestSubscribingTwiceToAProjectIsUndoneByOneUnsubscribe: the browser keeps the set of projects
// it subscribed to and sends one unsubscribe per key, so a second subscribe_project for a held
// project must not take it a second time — one unsubscribe has to be enough to leave it.
func TestSubscribingTwiceToAProjectIsUndoneByOneUnsubscribe(t *testing.T) {
	const projectID = "proj-shared"
	asm := watcherTestManager(t, projectID)
	wsURL := serveStream(t, asm)

	warmProbe(t, asm, projectID, "probe")
	c := dialFrameStream(t, wsURL)
	subscribeProven(t, c, projectID, "probe")
	awaitWatched(t, asm, projectID, true)

	c.sendSubscription(t, "subscribe_project", projectID, "")
	c.flushSubscriptions(t, asm, projectID)

	c.sendSubscription(t, "unsubscribe_project", projectID, "")
	c.flushSubscriptions(t, asm, projectID)

	assertSubscriberCount(t, asm, projectID, false)
	awaitWatched(t, asm, projectID, false)
}

// TestSubscribingToASecondProjectOnOneConnectionWatchesIt: watchers follow the project a
// connection newly holds, not the first project it happened to ask for.
func TestSubscribingToASecondProjectOnOneConnectionWatchesIt(t *testing.T) {
	const first, second = "proj-first", "proj-second"
	asm := watcherTestManager(t, first, second)
	wsURL := serveStream(t, asm)
	warmProbe(t, asm, first, "probe-first")
	warmProbe(t, asm, second, "probe-second")

	c := dialFrameStream(t, wsURL)
	subscribeProven(t, c, first, "probe-first")
	awaitWatched(t, asm, first, true)

	c.sendSubscription(t, "subscribe_project", second, "")
	c.sendSubscription(t, "subscribe_task", second, "probe-second")
	c.await(t, 5*time.Second, isTaskEventFor("probe-second", "warm-probe-second"), 1,
		"second project subscription applied")

	awaitWatched(t, asm, second, true)
	assertSubscriberCount(t, asm, second, true)
}

// TestSubscribeProjectReadsItsDataOncePerConnection: one initial-data pass reads the project's
// tasks and their diffs once, and the frames it writes are the record of that read — one
// task_diff_stats per task with changes, then the pass's closing project_branch. Duplicate
// subscribe_project frames for a project this connection already holds must not multiply it.
func TestSubscribeProjectReadsItsDataOncePerConnection(t *testing.T) {
	repoDir, s, _, wm, task := setupDoneSnapshotTest(t)
	base := revParse(t, repoDir, "HEAD")
	tip := commitInWorktree(t, wm, "task-1", "a.txt", "alpha\n")
	markDone(t, s, task, base, tip)

	asm := NewAppStreamManager(s, wm)
	t.Cleanup(asm.Close)

	const projectID = "proj-1"
	c := dialStream(t, asm)
	const duplicates = 5
	for i := 0; i < duplicates; i++ {
		c.sendSubscription(t, "subscribe_project", projectID, "")
	}

	c.await(t, 15*time.Second, isType("project_branch"), 1, "initial data for the first subscribe_project")
	c.flushSubscriptions(t, asm, projectID)

	// The frames are the record of the work: a pass writes its task_diff_stats before its
	// closing project_branch, so a window with no second branch has seen no second pass, and
	// the stats count is what a second pass would have doubled.
	c.quietWindow(t, time.Second, isType("project_branch"),
		"a second initial-data pass for a project this connection already holds")
	if extra := c.collect(300*time.Millisecond, isType("task_diff_stats"), duplicates); extra != 0 {
		t.Fatalf("%d task_diff_stats frames beyond the one this project's single pass writes", 1+extra)
	}
}

// TestSubscribeTaskReplaysItsBufferOncePerConnection: the replay is what the key's one
// subscription is worth; a frame that changes nothing must not re-send it.
func TestSubscribeTaskReplaysItsBufferOncePerConnection(t *testing.T) {
	asm := NewAppStreamManager(nil, nil)
	t.Cleanup(asm.Close)

	const projectID, taskID, appliedProbe = "proj-1", "task-1", "applied-probe"
	warmProbe(t, asm, projectID, taskID)
	warmProbe(t, asm, projectID, appliedProbe)

	c := dialStream(t, asm)
	const duplicates = 4
	for i := 0; i < duplicates; i++ {
		c.sendSubscription(t, "subscribe_task", projectID, taskID)
	}
	// Proven applied by a probe that follows the duplicates in the same read loop, so its
	// replay answers for all of them. Both counts come out of one drain, which discards
	// nothing.
	c.sendSubscription(t, "subscribe_task", projectID, appliedProbe)
	counts := c.tally(3*time.Second,
		isTaskEventFor(taskID, "warm-"+taskID),
		isTaskEventFor(appliedProbe, "warm-"+appliedProbe))

	if counts[1] == 0 {
		t.Fatal("the probe replay never arrived, so the subscribe_task frames were not applied")
	}
	if counts[0] != 1 {
		t.Fatalf("%d replays of one task buffer for %d subscribe_task frames, want 1", counts[0], duplicates)
	}
}
