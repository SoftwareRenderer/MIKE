package git

// The user-visible contract of the project merge lock, written because the alternative — bounding the wait
// and answering "project is busy" — was considered and rejected (F22/F23, 2b): a merge is a legitimate
// multi-second operation, and refusing someone who arrived second would turn a normal race into a click they
// have to repeat. What they get instead is the wait, and a log line naming what it is still waiting on.

import (
	"strings"
	"sync"
	"testing"
	"time"

	"mike/pkg/logging"
)

// testMergeWaitReport is short so the test proves the report exists without sleeping for the production
// threshold (gitSlowWait, 2 s).
const testMergeWaitReport = 20 * time.Millisecond

// Serial: logging.TestSink re-points the process-wide log, so another test's lines would land in this
// buffer and this test's restore would land on that test's sink.
func TestMergeWaiterWaitsThenProceedsAndSaysWhatItWaitsOn(t *testing.T) {
	logged, restore := logging.TestSink()
	defer restore()

	wm := NewWorktreeManager(nil, nil)
	wm.merge.Configure(testMergeWaitReport, slowWaitReporter("merge"))

	holder := wm.LockProjectMerge("proj")
	released := make(chan struct{})
	go func() {
		waiter := wm.LockProjectMerge("proj")
		close(released)
		waiter()
	}()

	select {
	case <-released:
		t.Fatal("the waiter got the merge lock while the holder still had it — the main checkout's HEAD " +
			"and index are shared, so an interleaved merge would clobber the first one's stash and restore")
	case <-time.After(150 * time.Millisecond):
		// Still waiting: the correct answer. It is not refused, because refusing is the thing this rejects.
	}

	holder()
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter never got the merge lock after the holder released — that is a project whose " +
			"second operation is dropped rather than queued")
	}

	// The report arrives when the wait ends (keylock reports a completed wait, not a live one), so it is
	// checked after the waiter is in: the operator sees "waited 152ms for the project's merge lock".
	if got := strings.Count(logged.String(), "merge lock"); got != 1 {
		t.Errorf("the long wait was reported %d times, want exactly 1; log was:\n%s", got, logged.String())
	}
	if !strings.Contains(logged.String(), "project_id=proj") {
		t.Errorf("the report must name the project so a grep by project finds it; log was:\n%s", logged.String())
	}

	// A wait that never happened stays silent. A line on every uncontended lock is a line nobody reads, and
	// the report's whole value is that its presence means something waited.
	wm.LockProjectMerge("quiet")()
	if strings.Count(logged.String(), "merge lock") != 1 {
		t.Errorf("an uncontended lock reported a wait it did not have; log was:\n%s", logged.String())
	}
}

// Parallel subtasks auto-merging is the ordinary case, not the pathological one: each merge serializes
// against the others and every one of them runs. Nothing in this path may answer "busy" — the direction the
// bounded-wait design would have gotten wrong.
func TestParallelMergeWaitersAllRunAndNoneIsRefused(t *testing.T) {
	t.Parallel()
	wm := NewWorktreeManager(nil, nil)
	const projectID = "siblings"

	var mu sync.Mutex
	var order []int
	const n = 4
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			release := wm.LockProjectMerge(projectID)
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
			time.Sleep(5 * time.Millisecond) // the merge itself: a checkout, a stash, a restore
			release()
		}(i)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(order) != n {
		t.Errorf("%d of %d merges ran, want all of them", len(order), n)
	}

	// The other project is untouched by any of this: one table, per-key waits (F16 keeps the domains apart).
	other := wm.LockProjectMerge("another-project")
	other()
}
