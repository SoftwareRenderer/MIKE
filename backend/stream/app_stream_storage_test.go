package stream

import (
	"sync"
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

// -- The task figure a project's board is sent --

// taskSpaceProbe is the measurement a test hands the stream: it counts how often each project was
// walked and answers the figure that project was priced at.
type taskSpaceProbe struct {
	mu      sync.Mutex
	calls   map[string]int
	figures map[string]git.TaskWorktreeUsage
	refused map[string]bool
	parked  map[string]*parkedWalk
}

// parkedWalk keeps one measurement running until the test lets it finish. entered closes when the walk
// arrives, and the walk waits for hold to close before it answers.
type parkedWalk struct {
	once    sync.Once
	entered chan struct{}
	hold    chan struct{}
}

func newTaskSpaceProbe() *taskSpaceProbe {
	return &taskSpaceProbe{
		calls:   map[string]int{},
		figures: map[string]git.TaskWorktreeUsage{},
		refused: map[string]bool{},
		parked:  map[string]*parkedWalk{},
	}
}

func (p *taskSpaceProbe) price(projectID string, bytes int64, worktrees int) *taskSpaceProbe {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.figures[projectID] = git.TaskWorktreeUsage{Bytes: bytes, Worktrees: worktrees}
	return p
}

func (p *taskSpaceProbe) refuse(projectID string) *taskSpaceProbe {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refused[projectID] = true
	return p
}

// park holds the next walk of one project until the returned release function runs, and returns a
// channel that closes when that walk reaches the measurement.
func (p *taskSpaceProbe) park(projectID string) (entered <-chan struct{}, release func()) {
	parked := &parkedWalk{entered: make(chan struct{}), hold: make(chan struct{})}
	p.mu.Lock()
	p.parked[projectID] = parked
	p.mu.Unlock()
	return parked.entered, func() { close(parked.hold) }
}

func (p *taskSpaceProbe) probe(projectID string) (git.TaskWorktreeUsage, bool) {
	p.mu.Lock()
	p.calls[projectID]++
	parked := p.parked[projectID]
	delete(p.parked, projectID)
	refused := p.refused[projectID]
	figure := p.figures[projectID]
	p.mu.Unlock()

	if parked != nil {
		parked.once.Do(func() { close(parked.entered) })
		<-parked.hold
	}
	if refused {
		return git.TaskWorktreeUsage{}, false
	}
	return figure, true
}

func (p *taskSpaceProbe) callsFor(projectID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[projectID]
}

// isTaskFrameFor matches the reading that names this project. The filesystem reading names no project.
func isTaskFrameFor(projectID string) func(map[string]any) bool {
	return func(frame map[string]any) bool {
		return isStorageFrame(frame) && frame["project_id"] == projectID
	}
}

// wantTaskFigure answers the figure a reading carries, or fails when it carries none. The caller's
// predicate picked the project, so this checks only what the frame priced.
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
	if _, sent := frame["project_id"]; sent {
		t.Fatalf("a filesystem reading names a project: %v", frame)
	}
}

// subscribeBoard puts a connection on a board, which is the only thing that asks for a task figure.
func subscribeBoard(t *testing.T, fs *frameStream, projectID string) {
	t.Helper()
	fs.sendSubscription(t, "subscribe_project", projectID, "")
}

// awaitBoardClosed waits until no connection holds the project and nothing is cached for it. A reopened
// board must be measured from that state again.
func awaitBoardClosed(t *testing.T, asm *AppStreamManager, projectID string) {
	t.Helper()
	awaitUntil(t, "the board for "+projectID+" never closed", func() bool {
		asm.mu.RLock()
		defer asm.mu.RUnlock()
		_, cached := asm.taskSpace[projectID]
		return len(asm.subscribers(projectSubs, projectID)) == 0 && !cached
	})
}

// awaitWalkSettled waits until no walk of the project is in flight, so an assertion about what the next
// reading does cannot race the one already running.
func awaitWalkSettled(t *testing.T, asm *AppStreamManager, projectID string) {
	t.Helper()
	awaitUntil(t, "the walk of "+projectID+" never settled", func() bool {
		asm.mu.RLock()
		defer asm.mu.RUnlock()
		return !asm.taskSpaceBusy[projectID]
	})
}

// awaitUntil polls a condition answered from the manager's own state, for the tests that must not race a
// goroutine the stream runs on its own.
func awaitUntil(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for !done() {
		select {
		case <-deadline.C:
			t.Fatal(what)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// The walk covers a whole tree, so it cannot run inside the broadcast. The first reading a board is
// given arrives once the measurement of that project is done.
func TestTheTaskFigureFollowsTheReadingItWasMeasuredFor(t *testing.T) {
	asm := NewAppStreamManager(writeTestStore(t), nil)
	asm.SetStorageProbe(fixedStorage(1000, 150))
	asm.SetTaskSpaceProbe(newTaskSpaceProbe().price("p1", 9000, 4).probe)
	fs := dialStream(t, asm)

	// The greeting is the filesystem alone: no board has been opened, so no tree has been walked.
	refusesTaskFigure(t, fs.awaitFrame(t, 5*time.Second, isStorageFrame, "the greeting reading"))

	subscribeBoard(t, fs, "p1")
	frame := fs.awaitFrame(t, 5*time.Second, isTaskFrameFor("p1"), "the reading of p1's worktrees")
	if bytes, count := wantTaskFigure(t, frame); bytes != 9000 || count != 4 {
		t.Fatalf("task figure = %d bytes in %d worktrees, want 9000 in 4", bytes, count)
	}
}

// A board reads its own project alone. Another project's figure never reaches it, and that project's
// tree is never walked because of it.
func TestTheTaskFigureReachesOnlyTheBoardThatAsked(t *testing.T) {
	probe := newTaskSpaceProbe().price("p1", 1<<20, 3).price("p2", 2<<20, 7)
	asm := NewAppStreamManager(writeTestStore(t), nil)
	asm.SetStorageProbe(fixedStorage(1000, 150))
	asm.SetTaskSpaceProbe(probe.probe)
	wsURL := serveStream(t, asm)
	one := dialFrameStream(t, wsURL)
	two := dialFrameStream(t, wsURL)

	subscribeBoard(t, one, "p1")
	if bytes, count := wantTaskFigure(t,
		one.awaitFrame(t, 5*time.Second, isTaskFrameFor("p1"), "p1's figure")); bytes != 1<<20 || count != 3 {
		t.Fatalf("p1's figure = %d in %d, want %d in 3", bytes, count, 1<<20)
	}

	// The other board's reading is its own filesystem figure, with nothing of p1's bytes in it.
	subscribeBoard(t, two, "p2")
	if bytes, count := wantTaskFigure(t,
		two.awaitFrame(t, 5*time.Second, isTaskFrameFor("p2"), "p2's figure")); bytes != 2<<20 || count != 7 {
		t.Fatalf("p2's figure = %d in %d, want %d in 7", bytes, count, 2<<20)
	}
	two.quietWindow(t, 500*time.Millisecond, isTaskFrameFor("p1"), "p1's figure on p2's board")
	if got := probe.callsFor("p1"); got != 1 {
		t.Fatalf("p1's tree was walked %d times, want once, and none of them for p2's board", got)
	}

	// A teardown of a project nobody has open asks the walk for nothing at all.
	asm.Broadcast("p3")
	one.await(t, 5*time.Second, isStorageFrame, 1, "the filesystem reading a teardown pushes")
	if got := probe.callsFor("p3"); got != 0 {
		t.Fatalf("p3's tree was walked %d times with no board open, want never", got)
	}
}

// A board opened after the measurement is greeted with the figure, the same way the filesystem reading
// is: the walk is not repeated for every connection.
func TestAConnectionOpenedAfterTheMeasurementKeepsTheTaskFigure(t *testing.T) {
	probe := newTaskSpaceProbe().price("p1", 2048, 2)
	asm := NewAppStreamManager(writeTestStore(t), nil)
	asm.SetStorageProbe(fixedStorage(1000, 150))
	asm.SetTaskSpaceProbe(probe.probe)
	wsURL := serveStream(t, asm)
	first := dialFrameStream(t, wsURL)
	subscribeBoard(t, first, "p1")
	wantTaskFigure(t, first.awaitFrame(t, 5*time.Second, isTaskFrameFor("p1"), "the first board's figure"))

	second := dialFrameStream(t, wsURL)
	secondFrame := second.awaitFrame(t, 5*time.Second, isStorageFrame, "the second connection's greeting")
	refusesTaskFigure(t, secondFrame)
	subscribeBoard(t, second, "p1")
	if bytes, count := wantTaskFigure(t,
		second.awaitFrame(t, 5*time.Second, isTaskFrameFor("p1"), "the cached figure")); bytes != 2048 || count != 2 {
		t.Fatalf("greeting task figure = %d in %d, want the cached 2048 in 2", bytes, count)
	}
	// The greeting reads the cache; the only walk is the one the first board started.
	if got := probe.callsFor("p1"); got != 1 {
		t.Fatalf("the tree was walked %d times, want once", got)
	}
}

// A checkout is not a statfs: however many readings go out for one project inside one budget, its trees
// are walked once. Past the budget they are walked again.
func TestTheTaskFigureIsMeasuredOncePerBudget(t *testing.T) {
	probe := newTaskSpaceProbe().price("p1", 512, 1)
	asm := NewAppStreamManager(writeTestStore(t), nil)
	asm.SetStorageProbe(fixedStorage(1000, 150))
	asm.SetTaskSpaceProbe(probe.probe)
	fs := dialStream(t, asm)
	subscribeBoard(t, fs, "p1")
	wantTaskFigure(t, fs.awaitFrame(t, 5*time.Second, isTaskFrameFor("p1"), "the first figure"))

	for range 3 {
		asm.Broadcast("p1")
		reading := fs.awaitFrame(t, 5*time.Second, isTaskFrameFor("p1"), "a reading inside the budget")
		wantTaskFigure(t, reading)
	}
	if got := probe.callsFor("p1"); got != 1 {
		t.Fatalf("the tree was walked %d times over four readings, want once", got)
	}

	// Age the figure past its budget rather than shrinking the budget: a budget shorter than a walk
	// would let the refresh's own reading look stale, and every reading would start another walk.
	asm.mu.Lock()
	asm.taskSpaceAt["p1"] = time.Now().Add(-2 * defaultTaskSpaceTTL)
	asm.mu.Unlock()
	asm.Broadcast("p1")
	// The refresh's frame arrives only after the second walk, so this wait proves the budget expired.
	wantTaskFigure(t, fs.awaitFrame(t, 5*time.Second, isTaskFrameFor("p1"), "the reading built from the stale figure"))
	wantTaskFigure(t, fs.awaitFrame(t, 5*time.Second, isTaskFrameFor("p1"), "the reading the second walk pushed"))
	// Exactly twice: the reading the refresh itself pushed found a fresh figure, so it started nothing.
	if got := probe.callsFor("p1"); got != 2 {
		t.Fatalf("the tree was walked %d times across two budgets, want twice", got)
	}
}

// Once no board holds the project its figure is dropped, so a reopened board is measured again rather
// than greeted by a figure of unknown age.
func TestAProjectClosedAndReopenedIsWalkedAgain(t *testing.T) {
	probe := newTaskSpaceProbe().price("p1", 64, 1)
	asm := NewAppStreamManager(writeTestStore(t), nil)
	asm.SetStorageProbe(fixedStorage(1000, 150))
	asm.SetTaskSpaceProbe(probe.probe)
	fs := dialStream(t, asm)
	subscribeBoard(t, fs, "p1")
	wantTaskFigure(t, fs.awaitFrame(t, 5*time.Second, isTaskFrameFor("p1"), "the first figure"))

	fs.sendSubscription(t, "unsubscribe_project", "p1", "")
	awaitBoardClosed(t, asm, "p1")

	subscribeBoard(t, fs, "p1")
	wantTaskFigure(t, fs.awaitFrame(t, 5*time.Second, isTaskFrameFor("p1"), "the figure after reopening"))
	if got := probe.callsFor("p1"); got != 2 {
		t.Fatalf("the tree was walked %d times across a close and a reopen, want twice", got)
	}
}

// A walk that finishes with nobody watching leaves nothing behind. A stamp of now with no figure would
// tell a reopened board its figure is fresh, and no walk would ever start.
func TestABoardClosedWhileItsWalkRanIsGreetedWithAFigureOnReopen(t *testing.T) {
	probe := newTaskSpaceProbe().price("p1", 4096, 2)
	entered, release := probe.park("p1")
	asm := NewAppStreamManager(writeTestStore(t), nil)
	asm.SetStorageProbe(fixedStorage(1000, 150))
	asm.SetTaskSpaceProbe(probe.probe)
	fs := dialStream(t, asm)

	subscribeBoard(t, fs, "p1")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("opening the board never asked for the measurement")
	}

	fs.sendSubscription(t, "unsubscribe_project", "p1", "")
	awaitBoardClosed(t, asm, "p1")
	release()
	awaitWalkSettled(t, asm, "p1")

	subscribeBoard(t, fs, "p1")
	if bytes, count := wantTaskFigure(t,
		fs.awaitFrame(t, 5*time.Second, isTaskFrameFor("p1"), "the figure after reopening mid-walk")); bytes != 4096 || count != 2 {
		t.Fatalf("figure after reopening = %d in %d, want 4096 in 2", bytes, count)
	}
}

// A tree that cannot be priced leaves the filesystem reading arriving and the tasks line absent. It is
// never a zero the browser would draw as "0 B in 0 worktrees".
func TestAnUnpricedProjectSendsNoTaskFigure(t *testing.T) {
	probe := newTaskSpaceProbe().refuse("p1")
	asm := NewAppStreamManager(writeTestStore(t), nil)
	asm.SetStorageProbe(fixedStorage(1000, 150))
	asm.SetTaskSpaceProbe(probe.probe)
	fs := dialStream(t, asm)

	greeting := fs.awaitFrame(t, 5*time.Second, isStorageFrame, "the greeting reading")
	if greeting["total_bytes"] != float64(1000) {
		t.Fatalf("greeting reading = %v, want the filesystem figure to flow", greeting)
	}
	refusesTaskFigure(t, greeting)

	subscribeBoard(t, fs, "p1")
	fs.quietWindow(t, 500*time.Millisecond, isStorageFrame, "a reading carrying a figure that was never measured")

	// Opening the board asked once, and the answer came back inside one budget: one walk, not two.
	if got := probe.callsFor("p1"); got != 1 {
		t.Fatalf("the unreadable tree was walked %d times, want once per budget", got)
	}
}
