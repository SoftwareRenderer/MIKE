package keylock

// LockWatched: a notice while waiting, one final notice at the end, nothing when there was no wait.

import (
	"sync"
	"testing"
	"time"
)

type noticeLog struct {
	mu    sync.Mutex
	calls []time.Duration
	last  bool // acquired flag of the most recent call
	seen  []bool
}

func (n *noticeLog) notice(waited time.Duration, acquired bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, waited)
	n.seen = append(n.seen, acquired)
	n.last = acquired
}

func (n *noticeLog) snapshot() ([]time.Duration, []bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]time.Duration(nil), n.calls...), append([]bool(nil), n.seen...)
}

func TestLockWatchedReportsAWaitThatIsStillGoing(t *testing.T) {
	var tbl Table
	const key = "proj"

	holder := tbl.Lock(key)
	var log noticeLog
	acquired := make(chan struct{})
	go func() {
		release := tbl.LockWatched(key, 10*time.Millisecond, log.notice)
		close(acquired)
		release()
	}()

	select {
	case <-acquired:
		t.Fatal("the watched waiter got the lock while the holder held it")
	case <-time.After(200 * time.Millisecond):
	}

	calls, flags := log.snapshot()
	if len(calls) < 2 {
		t.Fatalf("a wait of 200ms with a 10ms interval produced %d notices, want the wait reported repeatedly", len(calls))
	}
	for i, f := range flags {
		if f {
			t.Fatalf("notice %d claimed the lock was acquired while it was still waiting", i)
		}
	}

	holder()
	<-acquired

	_, flags = log.snapshot()
	acquisitions := 0
	for _, f := range flags {
		if f {
			acquisitions++
		}
	}
	if acquisitions != 1 {
		t.Errorf("the acquired=true notice fired %d times, want exactly 1", acquisitions)
	}
	if !flags[len(flags)-1] {
		t.Errorf("a notice arrived after the acquisition; the acquired flag must be last, got %v", flags)
	}
}

// An uncontended lock is not a wait. A line for every acquisition is a line nobody reads — the same reason
// the slow-wait report stays silent below its threshold.
func TestLockWatchedStaysSilentWhenNothingWasHeld(t *testing.T) {
	var tbl Table
	var log noticeLog

	release := tbl.LockWatched("free-key", time.Millisecond, log.notice)
	release()
	release2 := tbl.LockWatched("free-key", time.Millisecond, log.notice)
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
			release := tbl.LockWatched(key, 5*time.Millisecond, notes[i].notice)
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
		calls, flags := n.snapshot()
		if len(calls) == 0 || !flags[len(flags)-1] {
			t.Errorf("waiter %d saw %d notices ending in acquired=%v, want a report ending in the acquisition", i, len(calls), len(flags) > 0 && flags[len(flags)-1])
		}
	}

	// The key is reusable: refs reached zero.
	again := tbl.Lock(key)
	again()
}

// The zero values must not crash: a nil notice means nobody wants to hear about the wait, and a
// non-positive interval would panic inside NewTicker.
func TestLockWatchedSurvivesANilNotice(t *testing.T) {
	var tbl Table
	holder := tbl.Lock("key")
	done := make(chan struct{})
	go func() { tbl.LockWatched("key", 0, nil)(); close(done) }()

	time.Sleep(30 * time.Millisecond)
	holder()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a watched wait with a nil notice did not complete after the release")
	}
}

func TestLockWatchedSurvivesAZeroInterval(t *testing.T) {
	var tbl Table
	holder := tbl.Lock("key")
	done := make(chan struct{})
	go func() { tbl.LockWatched("key", 0, func(time.Duration, bool) {})(); close(done) }()

	time.Sleep(30 * time.Millisecond)
	holder()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a watched wait with a zero interval did not complete after the release")
	}
}
