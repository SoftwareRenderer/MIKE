package keylock

// LockWatchedCtx: a notice while waiting, one final notice at the end, nothing when there was no wait —
// and a waiter that is called off says that instead, having taken nothing.

import (
	"context"
	"sync"
	"testing"
	"time"
)

type noticeLog struct {
	mu    sync.Mutex
	calls []time.Duration
	last  WaitOutcome // outcome of the most recent call
	seen  []WaitOutcome
}

func (n *noticeLog) notice(waited time.Duration, outcome WaitOutcome) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, waited)
	n.seen = append(n.seen, outcome)
	n.last = outcome
}

func (n *noticeLog) snapshot() ([]time.Duration, []WaitOutcome) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]time.Duration(nil), n.calls...), append([]WaitOutcome(nil), n.seen...)
}

func TestLockWatchedReportsAWaitThatIsStillGoing(t *testing.T) {
	var tbl Table
	const key = "proj"

	holder := tbl.Lock(key)
	var log noticeLog
	acquired := make(chan struct{})
	go func() {
		release, _ := tbl.LockWatchedCtx(context.Background(), key, 10*time.Millisecond, log.notice)
		close(acquired)
		release()
	}()

	select {
	case <-acquired:
		t.Fatal("the watched waiter got the lock while the holder held it")
	case <-time.After(200 * time.Millisecond):
	}

	calls, outcomes := log.snapshot()
	if len(calls) < 2 {
		t.Fatalf("a wait of 200ms with a 10ms interval produced %d notices, want the wait reported repeatedly", len(calls))
	}
	for i, o := range outcomes {
		if o != WaitWaiting {
			t.Fatalf("notice %d claimed %v while the wait was still going", i, o)
		}
	}

	holder()
	<-acquired

	_, outcomes = log.snapshot()
	acquisitions := 0
	for _, o := range outcomes {
		if o == WaitAcquired {
			acquisitions++
		}
	}
	if acquisitions != 1 {
		t.Errorf("the acquired notice fired %d times, want exactly 1", acquisitions)
	}
	if outcomes[len(outcomes)-1] != WaitAcquired {
		t.Errorf("a notice arrived after the acquisition; the ending must be last, got %v", outcomes)
	}
}

// An uncontended lock is not a wait. A line for every acquisition is a line nobody reads — the same reason
// the slow-wait report stays silent below its threshold.
func TestLockWatchedStaysSilentWhenNothingWasHeld(t *testing.T) {
	var tbl Table
	var log noticeLog

	release, _ := tbl.LockWatchedCtx(context.Background(), "free-key", time.Millisecond, log.notice)
	release()
	release2, _ := tbl.LockWatchedCtx(context.Background(), "free-key", time.Millisecond, log.notice)
	release2()

	if calls, _ := log.snapshot(); len(calls) != 0 {
		t.Errorf("an uncontended acquisition reported %d notices, want none: %v", len(calls), calls)
	}
}

// Two watched waiters on one key still each hear about their own wait, and the key is reusable afterwards:
// a notice path that wedges the entry would make the project uncreatable for the life of the process.
func TestLockWatchedTwoWaitersBothProceedAndTheKeySurvives(t *testing.T) {
	var tbl Table
	const key = "shared"

	holder := tbl.Lock(key)
	var wg sync.WaitGroup
	notes := make([]*noticeLog, 2)
	for i := range notes {
		notes[i] = &noticeLog{}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			release, _ := tbl.LockWatchedCtx(context.Background(), key, 5*time.Millisecond, notes[i].notice)
			release()
		}(i)
	}

	time.Sleep(80 * time.Millisecond)
	holder()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a watched waiter never got the lock after the holder released")
	}

	for i, n := range notes {
		calls, outcomes := n.snapshot()
		if len(calls) == 0 || outcomes[len(outcomes)-1] != WaitAcquired {
			t.Errorf("waiter %d saw %d notices ending in %v, want a report ending in the acquisition", i, len(calls), len(outcomes) > 0 && outcomes[len(outcomes)-1] == WaitAcquired)
		}
	}

	// The key is reusable: refs reached zero.
	again := tbl.Lock(key)
	again()
}

// The zero values must not crash: a nil notice means nobody wants to hear about the wait, and a
// non-positive interval would panic inside NewTicker — including for the waiter that does have a
// notifier to hear from.
func TestLockWatchedSurvivesItsZeroValues(t *testing.T) {
	cases := []struct {
		name   string
		notice WaitNotice
	}{
		{"no notifier at all", nil},
		{"a notifier and no interval", func(time.Duration, WaitOutcome) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var tbl Table
			holder := tbl.Lock("key")
			done := make(chan struct{})
			go func() {
				release, _ := tbl.LockWatchedCtx(context.Background(), "key", 0, tc.notice)
				release()
				close(done)
			}()

			time.Sleep(30 * time.Millisecond)
			holder()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("a watched wait on zero values did not complete after the release")
			}
		})
	}
}

// A called-off wait stops announcing itself, so the screen watching the task does not keep a spinner for a
// wait nobody is making.
func TestACalledOffWaitEndsItsOwnNotice(t *testing.T) {
	var tbl Table
	const key = "abandoned"

	holder := tbl.Lock(key)
	ctx, cancel := context.WithCancel(context.Background())
	givenUp := make(chan struct{})
	var log noticeLog
	go func() {
		release, ok := tbl.LockWatchedCtx(ctx, key, 5*time.Millisecond, log.notice)
		if ok || release != nil {
			t.Errorf("a cancelled wait returned ok=%v release=%v, want it refused as abandoned", ok, release != nil)
		}
		close(givenUp)
	}()

	waitForNotices(t, &log, 2)
	cancel()
	select {
	case <-givenUp:
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled wait kept standing rather than giving up")
	}

	_, outcomes := log.snapshot()
	if outcomes[len(outcomes)-1] != WaitAbandoned {
		t.Errorf("the last notice was %v, want the wait to end by saying it was abandoned", outcomes[len(outcomes)-1])
	}

	// Nothing more is said about a wait that is over, however long the holder keeps the lock.
	before := len(outcomes)
	time.Sleep(60 * time.Millisecond)
	if after, _ := log.snapshot(); len(after) != before {
		t.Errorf("a called-off wait announced itself %d more times, want silence", len(after)-before)
	}
	holder()
}

// A waiter that gave up leaves the entry it found: the next asker must not inherit a reference nobody holds.
func TestACalledOffWaitTakesNothingAndHoldsNothing(t *testing.T) {
	var tbl Table
	const key = "abandoned-reusable"

	holder := tbl.Lock(key)
	ctx, cancel := context.WithCancel(context.Background())
	givenUp := make(chan struct{})
	var log noticeLog
	go func() {
		release, ok := tbl.LockWatchedCtx(ctx, key, 5*time.Millisecond, log.notice)
		if ok || release != nil {
			t.Errorf("a cancelled wait returned ok=%v with a release function, want it refused as abandoned", ok)
		}
		close(givenUp)
	}()

	waitForNotices(t, &log, 1)
	cancel()
	<-givenUp
	holder()

	acquired := make(chan struct{})
	go func() { tbl.Lock(key)(); close(acquired) }()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("the key stayed held by a waiter that never took it")
	}

	tbl.mu.Lock()
	ghost := tbl.locks[key]
	tbl.mu.Unlock()
	if ghost != nil {
		t.Errorf("the abandoned waiter left an entry with %d reference(s) on the key, want the key forgotten once nobody holds or waits", ghost.refs)
	}
}

// A wait nobody calls off is reported while it lasts and ends in the acquisition, and it queues behind the
// same holder as a plain Lock.
func TestAWaitWithNoDeadlineEndsInAcquisition(t *testing.T) {
	var tbl Table
	const key = "patient"

	holder := tbl.Lock(key)
	watched := &noticeLog{}
	acquired := make(chan struct{}, 2)
	go func() {
		release, ok := tbl.LockWatchedCtx(context.Background(), key, 10*time.Millisecond, watched.notice)
		if !ok {
			t.Errorf("a wait nobody cancelled reported itself abandoned")
		}
		release()
		acquired <- struct{}{}
	}()
	go func() {
		tbl.Lock(key)()
		acquired <- struct{}{}
	}()

	waitForNotices(t, watched, 2)
	holder()
	for i := 0; i < 2; i++ {
		select {
		case <-acquired:
		case <-time.After(5 * time.Second):
			t.Fatal("a waiter never got the lock after the holder released")
		}
	}

	calls, outcomes := watched.snapshot()
	if outcomes[len(outcomes)-1] != WaitAcquired {
		t.Errorf("the wait ended in %v, want the acquisition", outcomes[len(outcomes)-1])
	}
	if len(calls) < 2 {
		t.Errorf("a wait nobody cancelled reported itself %d times, want the wait reported repeatedly", len(calls))
	}
}

func waitForNotices(t *testing.T, log *noticeLog, atLeast int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		calls, _ := log.snapshot()
		if len(calls) >= atLeast {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the wait reported itself %d times within 5s, want at least %d", len(calls), atLeast)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
