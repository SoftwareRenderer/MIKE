package stream

import (
	"sync/atomic"
	"testing"
	"time"

	"mike/backend/fsutil"
	"mike/backend/git"
	"mike/backend/types"
)

// These drive the real socket, so the frame's bytes are the claim.

// fixedStorage answers one reading forever, so the test does not read this host's disk.
func fixedStorage(total, avail uint64) func() (fsutil.DiskUsage, bool) {
	return func() (fsutil.DiskUsage, bool) {
		return fsutil.DiskUsage{TotalBytes: total, AvailBytes: avail}, true
	}
}

func isStorageFrame(frame map[string]any) bool {
	return frame["type"] == string(types.WSTypeStorageUsage)
}

// A connection opened after the last task landed in Done has missed every push.
func TestANewConnectionIsGreetedWithTheFilesystemReading(t *testing.T) {
	asm := NewAppStreamManager(writeTestStore(t), nil)
	asm.SetStorageProbe(fixedStorage(1000, 150))
	fs := dialStream(t, asm)

	frame := fs.awaitFrame(t, 5*time.Second, isStorageFrame, "the greeting reading")
	if frame["total_bytes"] != float64(1000) || frame["avail_bytes"] != float64(150) {
		t.Fatalf("greeting reading = %v, want total 1000 and avail 150", frame)
	}
}

// A task moving anywhere but Done changes nothing on disk.
func TestTheFilesystemReadingFollowsATaskLandingInDone(t *testing.T) {
	asm := NewAppStreamManager(writeTestStore(t), nil)
	asm.SetStorageProbe(fixedStorage(1000, 150))
	fs := dialStream(t, asm)
	fs.await(t, 5*time.Second, isStorageFrame, 1, "the greeting reading")

	asm.BroadcastTaskUpdated("p1", &types.Task{ID: "t1", ProjectID: "p1", Status: types.StatusReview})
	fs.quietWindow(t, 500*time.Millisecond, isStorageFrame, "a reading after an update to Review")

	asm.BroadcastTaskUpdated("p1", &types.Task{ID: "t1", ProjectID: "p1", Status: types.StatusDone})
	fs.await(t, 5*time.Second, isStorageFrame, 1, "a reading after the task landed in Done")
}

// Clearing Done ends with the board refresh. Without it the count stays red over freed space.
func TestTheBoardRefreshATeardownEndsWithPushesAFilesystemReading(t *testing.T) {
	asm := NewAppStreamManager(writeTestStore(t), nil)
	asm.SetStorageProbe(fixedStorage(1000, 900))
	fs := dialStream(t, asm)
	fs.await(t, 5*time.Second, isStorageFrame, 1, "the greeting reading")

	asm.Broadcast("p1")
	frame := fs.awaitFrame(t, 5*time.Second, isStorageFrame, "the reading after a teardown settled")
	if frame["avail_bytes"] != float64(900) {
		t.Fatalf("reading after the board refresh = %v, avail 900", frame)
	}
}

// No probe, an unreadable root and a zero-size root all answer no frame.
func TestNothingToSaySendsNoFilesystemFrame(t *testing.T) {
	wirings := map[string]func(*AppStreamManager){
		"no probe is wired": func(*AppStreamManager) {},
		"the probe cannot read the root": func(asm *AppStreamManager) {
			asm.SetStorageProbe(func() (fsutil.DiskUsage, bool) { return fsutil.DiskUsage{}, false })
		},
		"the root reports no size": func(asm *AppStreamManager) {
			asm.SetStorageProbe(func() (fsutil.DiskUsage, bool) { return fsutil.DiskUsage{TotalBytes: 0, AvailBytes: 0}, true })
		},
	}
	for name, wire := range wirings {
		t.Run(name, func(t *testing.T) {
			asm := NewAppStreamManager(writeTestStore(t), nil)
			wire(asm)
			fs := dialStream(t, asm)
			// project_counts is broadcast at connect, so seeing it proves the socket carries frames.
			fs.await(t, 5*time.Second, func(frame map[string]any) bool {
				return frame["type"] == string(types.WSTypeProjectCounts)
			}, 1, "the connection's first frame")

			asm.BroadcastTaskUpdated("p1", &types.Task{ID: "t1", ProjectID: "p1", Status: types.StatusDone})
			asm.Broadcast("p1")
			fs.quietWindow(t, 500*time.Millisecond, isStorageFrame, "a reading there was none to give")
		})
	}
}

// -- The task figure the same frame carries --

// measuredTaskSpace answers one figure every time it is walked, and counts the walkings.
type measuredTaskSpace struct {
	calls atomic.Int64
	space git.TaskWorktreeUsage
}

func (m *measuredTaskSpace) probe() (git.TaskWorktreeUsage, bool) {
	m.calls.Add(1)
	return m.space, true
}

// unmeasurableTaskSpace is the worktrees root a walk cannot price.
type unmeasurableTaskSpace struct {
	calls atomic.Int64
}

func (u *unmeasurableTaskSpace) probe() (git.TaskWorktreeUsage, bool) {
	u.calls.Add(1)
	return git.TaskWorktreeUsage{}, false
}

// wantTaskFigure answers the figure a reading carries, or fails when it carries none.
func wantTaskFigure(t *testing.T, frame map[string]any) (int64, int) {
	t.Helper()
	raw, sent := frame["tasks_bytes"]
	if !sent {
		t.Fatalf("reading %v carries no task figure", frame)
	}
	bytes, bytesOK := raw.(float64)
	count, countOK := frame["tasks_count"].(float64)
	if !bytesOK || !countOK {
		t.Fatalf("task figure in %v is not two numbers", frame)
	}
	return int64(bytes), int(count)
}

// refusesTaskFigure fails when a reading carries a figure the client would draw.
func refusesTaskFigure(t *testing.T, frame map[string]any) {
	t.Helper()
	if _, sent := frame["tasks_bytes"]; sent {
		t.Fatalf("reading %v carries a task figure that was never measured", frame)
	}
}

// The walk covers every checkout, so it cannot run inside the broadcast: the client sees one reading
// without the line, then one with it.
func TestTheTaskFigureFollowsTheReadingItWasMeasuredFor(t *testing.T) {
	asm := NewAppStreamManager(writeTestStore(t), nil)
	asm.SetStorageProbe(fixedStorage(1000, 150))
	asm.SetTaskSpaceProbe((&measuredTaskSpace{space: git.TaskWorktreeUsage{Bytes: 9000, Worktrees: 4}}).probe)
	fs := dialStream(t, asm)

	greeting := fs.awaitFrame(t, 5*time.Second, isStorageFrame, "the greeting reading")
	refusesTaskFigure(t, greeting)

	second := fs.awaitFrame(t, 5*time.Second, isStorageFrame, "the reading the measurement pushed")
	if bytes, count := wantTaskFigure(t, second); bytes != 9000 || count != 4 {
		t.Fatalf("task figure = %d bytes in %d worktrees, want 9000 in 4", bytes, count)
	}
}

// A board opened after the measurement is greeted with the figure, the same way the filesystem
// reading is: the walk is not repeated for every connection.
func TestAConnectionOpenedAfterTheMeasurementKeepsTheTaskFigure(t *testing.T) {
	walked := &measuredTaskSpace{space: git.TaskWorktreeUsage{Bytes: 2048, Worktrees: 2}}
	asm := NewAppStreamManager(writeTestStore(t), nil)
	asm.SetStorageProbe(fixedStorage(1000, 150))
	asm.SetTaskSpaceProbe(walked.probe)
	first := dialStream(t, asm)
	first.awaitFrame(t, 5*time.Second, isStorageFrame, "the greeting reading")
	firstFrame := first.awaitFrame(t, 5*time.Second, isStorageFrame, "the reading with the figure")
	wantTaskFigure(t, firstFrame)

	second := dialStream(t, asm)
	greeting := second.awaitFrame(t, 5*time.Second, isStorageFrame, "the second connection's greeting")
	if bytes, count := wantTaskFigure(t, greeting); bytes != 2048 || count != 2 {
		t.Fatalf("greeting task figure = %d in %d, want the cached 2048 in 2", bytes, count)
	}
	// The greeting reads the cache; the only walk is the one the first connection started.
	if got := walked.calls.Load(); got != 1 {
		t.Fatalf("the tree was walked %d times, want once", got)
	}
}

// A checkout is not a statfs: however many readings go out inside one budget, the trees are walked
// once. Past the budget they are walked again.
func TestTheTaskFigureIsMeasuredOncePerBudget(t *testing.T) {
	walked := &measuredTaskSpace{space: git.TaskWorktreeUsage{Bytes: 512, Worktrees: 1}}
	asm := NewAppStreamManager(writeTestStore(t), nil)
	asm.SetStorageProbe(fixedStorage(1000, 150))
	asm.SetTaskSpaceProbe(walked.probe)
	fs := dialStream(t, asm)
	fs.awaitFrame(t, 5*time.Second, isStorageFrame, "the greeting reading")
	measured := fs.awaitFrame(t, 5*time.Second, isStorageFrame, "the reading with the figure")
	wantTaskFigure(t, measured)

	for range 3 {
		asm.Broadcast("p1")
		reading := fs.awaitFrame(t, 5*time.Second, isStorageFrame, "a reading inside the budget")
		wantTaskFigure(t, reading)
	}
	if got := walked.calls.Load(); got != 1 {
		t.Fatalf("the tree was walked %d times over four readings, want once", got)
	}

	// Age the figure past its budget rather than shrinking the budget: a budget shorter than a walk
	// would let the refresh's own reading look stale, and every reading would start another walk.
	asm.mu.Lock()
	asm.taskSpaceAt = time.Now().Add(-2 * defaultTaskSpaceTTL)
	asm.mu.Unlock()
	asm.Broadcast("p1")
	// The refresh's frame arrives only after the second walk, so this wait proves the budget expired.
	stale := fs.awaitFrame(t, 5*time.Second, isStorageFrame, "the reading built from the stale figure")
	wantTaskFigure(t, stale)
	refreshed := fs.awaitFrame(t, 5*time.Second, isStorageFrame, "the reading the second walk pushed")
	wantTaskFigure(t, refreshed)
	// Exactly twice: the reading the refresh itself pushed found a fresh figure, so it started nothing.
	if got := walked.calls.Load(); got != 2 {
		t.Fatalf("the tree was walked %d times across two budgets, want twice", got)
	}
}

// A root that cannot be priced leaves the filesystem reading flowing and the tasks line absent —
// never a zero the client would draw as "0 B in 0 worktrees".
func TestAnUnpricedRootSendsNoTaskFigure(t *testing.T) {
	refused := &unmeasurableTaskSpace{}
	asm := NewAppStreamManager(writeTestStore(t), nil)
	asm.SetStorageProbe(fixedStorage(1000, 150))
	asm.SetTaskSpaceProbe(refused.probe)
	fs := dialStream(t, asm)

	greeting := fs.awaitFrame(t, 5*time.Second, isStorageFrame, "the greeting reading")
	if greeting["total_bytes"] != float64(1000) {
		t.Fatalf("greeting reading = %v, want the filesystem figure to flow", greeting)
	}
	refusesTaskFigure(t, greeting)

	asm.Broadcast("p1")
	second := fs.awaitFrame(t, 5*time.Second, isStorageFrame, "the reading after a teardown")
	refusesTaskFigure(t, second)

	// Both readings asked, and both were answered inside one budget: one walk, not two.
	if got := refused.calls.Load(); got != 1 {
		t.Fatalf("the unreadable root was walked %d times, want once per budget", got)
	}
}
