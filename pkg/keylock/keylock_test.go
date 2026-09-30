package keylock

// Tests for the mechanism every keyed lock in the server sits on. The properties that matter are the
// ones a keyed lock can lose silently: two keys that block each other, a timed-out wait that still
// acquires later, two tables that share a key, and a table that only ever grows.

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestKeysDoNotBlockEachOther is the reason this is a keyed lock and not one mutex: a project that is
// merging must not stop a different project from doing anything.
func TestKeysDoNotBlockEachOther(t *testing.T) {
	tbl := newTable(0, nil)

	releaseA := tbl.Lock("project-a")
	entered := make(chan struct{})
	go func() {
		releaseB := tbl.Lock("project-b")
		close(entered)
		releaseB()
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("an unrelated key blocked: the table is behaving like one global lock")
	}
	releaseA()
}

// TestSameKeyIsExclusive is the other direction: 50 workers on one key must never overlap.
func TestSameKeyIsExclusive(t *testing.T) {
	tbl := newTable(0, nil)

	// Counted per key: two different keys holding at once is the point of a keyed lock, so one
	// shared counter would report every legitimate overlap.
	var inside, overlaps int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := tbl.Lock("shared")
			if n := atomic.AddInt32(&inside, 1); n > 1 {
				atomic.AddInt32(&overlaps, 1)
			}
			time.Sleep(time.Millisecond)
			atomic.AddInt32(&inside, -1)
			release()
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&overlaps); got != 0 {
		t.Fatalf("%d concurrent holders of one key", got)
	}
	if n := len(tbl.locks); n != 0 {
		t.Errorf("table still holds %d key(s) after every holder released", n)
	}
}

// TestTryLockTimesOutWhileHeld — a waiter gives up on its own, without touching what the holder owns.
func TestTryLockTimesOutWhileHeld(t *testing.T) {
	tbl := newTable(0, nil)
	release := tbl.Lock("busy")
	defer release()

	start := time.Now()
	got, ok := tbl.TryLock("busy", 40*time.Millisecond)
	waited := time.Since(start)

	if ok {
		if got != nil {
			got()
		}
		t.Fatal("TryLock acquired a lock that was held")
	}
	if got != nil {
		t.Fatal("a failed TryLock returned a release function")
	}
	if waited < 40*time.Millisecond {
		t.Errorf("gave up after %v, before the timeout it was given", waited)
	}
	if waited > time.Second {
		t.Errorf("waited %v past a 40ms timeout", waited)
	}
}

// TestTimedOutWaitAcquiresNothingLater pins the difference from the implementation this replaced. That
// one let a goroutine keep waiting on the mutex after the caller had given up, and unlock when it
// eventually won — so a timed-out wait could later seize the key for an instant and make an unrelated
// caller wait for work nobody asked for.
func TestTimedOutWaitAcquiresNothingLater(t *testing.T) {
	tbl := newTable(0, nil)
	holder, _ := tbl.TryLock("k", time.Second)

	// Five callers give up while the key is held.
	for i := 0; i < 5; i++ {
		if _, ok := tbl.TryLock("k", 10*time.Millisecond); ok {
			t.Fatal("TryLock succeeded while the key was held")
		}
	}

	// The holder is still the only owner: it can release, and the key is then free for a fresh caller.
	// If a timed-out waiter had latched onto the key, one of them would own it now.
	holder()
	got, ok := tbl.TryLock("k", 10*time.Millisecond)
	if !ok {
		t.Fatal("the key was not free after its holder released: a timed-out waiter is holding it")
	}
	got()

	if n := len(tbl.locks); n != 0 {
		t.Errorf("abandoned waiters left %d key(s) in the table", n)
	}
}

// TestTableDoesNotGrowForever: keys are task and project IDs, so a table that never forgets is a leak
// proportional to everything this process has ever touched.
func TestTableDoesNotGrowForever(t *testing.T) {
	tbl := newTable(0, nil)
	for i := 0; i < 5000; i++ {
		key := fmt.Sprintf("key-%d", i)
		release := tbl.Lock(key)
		// A zero timeout answers this: the key is held, so there is nothing to wait for.
		if _, ok := tbl.TryLock(key, 0); ok {
			t.Fatal("reentrant acquisition succeeded; the lock is not exclusive")
		}
		release()
	}
	if n := len(tbl.locks); n != 0 {
		t.Fatalf("%d of 5000 keys remain after release", n)
	}
}

// TestAbandonedWaitsLeakNoGoroutines: the timed-out path must not need a janitor goroutine per wait,
// which is what the previous implementation paid for its "never leave the mutex held" guarantee.
func TestAbandonedWaitsLeakNoGoroutines(t *testing.T) {
	tbl := newTable(0, nil)
	release := tbl.Lock("held")
	runtime.GC()
	before := runtime.NumGoroutine()

	for i := 0; i < 200; i++ {
		if _, ok := tbl.TryLock("held", 200*time.Microsecond); ok {
			t.Fatal("acquired a held key")
		}
	}
	release()
	runtime.GC()
	// The release path is synchronous, so nothing of ours should still exist.
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Fatalf("goroutines went %d -> %d across 200 timed-out waits", before, after)
	}
	if n := len(tbl.locks); n != 0 {
		t.Errorf("table holds %d key(s) after everything released", n)
	}
}

// TestDoubleReleaseIsRefused: a caller that releases what it does not hold is a bug about ownership,
// and the failure has to name it. sync.Mutex panicked; so does this.
func TestDoubleReleaseIsRefused(t *testing.T) {
	tbl := newTable(0, nil)
	release := tbl.Lock("k")
	release()

	defer func() {
		if recover() == nil {
			t.Fatal("double release returned quietly")
		}
	}()
	release()
}

// TestTablesAreIndependentDomains is the mechanism-level half of F16. The named-type wrappers are what
// stop a wrong-domain call at compile time; independence is what makes a runtime alias impossible: a
// task ID and a project ID can be the same string, and two domains must still not see each other.
func TestTablesAreIndependentDomains(t *testing.T) {
	stash := newTable(0, nil)
	merge := newTable(0, nil)
	const sameKey = "frost-amber-quartz" // a valid project slug and a valid task ID

	releaseStash := stash.Lock(sameKey)
	releaseMerge, ok := merge.TryLock(sameKey, 20*time.Millisecond)
	if !ok {
		releaseStash()
		t.Fatal("two tables blocked each other on the same key")
	}
	releaseMerge()
	releaseStash()
}

// TestWaitersAllRun keeps the channel semaphore from starving a queued waiter while new callers keep
// arriving — the property the runtime's FIFO send queue is relied on for.
func TestWaitersAllRun(t *testing.T) {
	tbl := newTable(0, nil)
	var ran int32
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := tbl.Lock("one")
			atomic.AddInt32(&ran, 1)
			release()
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("only %d of 60 waiters finished", atomic.LoadInt32(&ran))
	}
	if got := atomic.LoadInt32(&ran); got != 60 {
		t.Fatalf("%d of 60 waiters ran", got)
	}
}

// TestConcurrentAcquireAndDrop is the race the refcount can lose: an entry deleted while a waiter is
// about to use it would hand the next caller a second lock for the same key. Run under -race.
func TestConcurrentAcquireAndDrop(t *testing.T) {
	tbl := newTable(0, nil)
	// Counted per key: two different keys holding at once is the point of a keyed lock, so one
	// shared counter would report every legitimate overlap.
	var inside [3]int32
	var overlaps int32
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("k%d", i%3)
			// Mix both forms so a release and an acquire race on the same entry.
			acquire := func() (func(), bool) {
				if i%2 == 0 {
					return tbl.Lock(key), true
				}
				return tbl.TryLock(key, 5*time.Second)
			}
			release, ok := acquire()
			if !ok {
				return
			}
			// The test's own mutation check: revert keylock to a shared table behind two keys and
			// this reports an overlap, because two keys would then share one token.
			slot := i % 3
			if n := atomic.AddInt32(&inside[slot], 1); n > 1 {
				atomic.AddInt32(&overlaps, 1)
			}
			time.Sleep(50 * time.Microsecond)
			atomic.AddInt32(&inside[slot], -1)
			release()
		}(i)
	}

	wg.Wait()
	if got := atomic.LoadInt32(&overlaps); got != 0 {
		t.Errorf("%d overlapping holders across 3 keys", got)
	}
	if n := len(tbl.locks); n != 0 {
		t.Errorf("%d key(s) remain after all holders released", n)
	}
}

// --- the slow-wait report ---------------------------------------------------
//
// This is what replaced the plan's seconds-scale bound on the git-family locks. Those sections can be
// held across a wait on a task's tool lock, which outlives any small timeout by orders of magnitude,
// so a bound there would fail legitimate work. The report keeps the one thing the bound was for: a
// section that outlives the threshold becomes visible instead of being silently normal.

// newTable is the package's own constructor for tests, mirroring what an embedding owner does.
func newTable(slowAfter time.Duration, onSlow WaitReport) *Table {
	tbl := &Table{}
	tbl.Configure(slowAfter, onSlow)
	return tbl
}

func TestSlowWaitIsReported(t *testing.T) {
	type report struct {
		key    string
		waited time.Duration
	}
	reports := make(chan report, 4)
	tbl := newTable(20*time.Millisecond, func(key string, waited time.Duration) {
		reports <- report{key: key, waited: waited}
	})

	release := tbl.Lock("wedged")
	blocked := make(chan struct{})
	go func() {
		later := tbl.Lock("wedged")
		later()
		close(blocked)
	}()

	time.Sleep(80 * time.Millisecond)
	release()
	<-blocked

	select {
	case got := <-reports:
		if got.key != "wedged" {
			t.Errorf("report named key %q, want wedged", got.key)
		}
		if got.waited < 20*time.Millisecond {
			t.Errorf("reported %v for a wait past the 20ms threshold", got.waited)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a wait far past the threshold was not reported")
	}
}

// A report on every ordinary acquisition would be noise, and noise is how a signal stops being read.
func TestOrdinaryWaitsAreNotReported(t *testing.T) {
	var calls int32
	tbl := newTable(time.Hour, func(string, time.Duration) { atomic.AddInt32(&calls, 1) })

	for i := 0; i < 20; i++ {
		release := tbl.Lock("quick")
		release()
	}
	// A bounded wait that succeeds immediately is ordinary too.
	if release, ok := tbl.TryLock("quick", time.Second); ok {
		release()
	} else {
		t.Fatal("TryLock failed on an uncontended key")
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("%d report(s) for waits measured in microseconds", got)
	}
}

// Disabling the report must not cost a channel operation or a clock read per acquisition — that is the
// reason it is a threshold on the table rather than a timer per wait.
func TestReportDisabledTakesNoClock(t *testing.T) {
	tbl := newTable(0, nil)
	release := tbl.Lock("k")
	release()
	if tbl.slowAfter != 0 || tbl.onSlow != nil {
		t.Fatal("a table nobody configured carries report state")
	}
}

// A zero Table must work: every struct literal in this repo used to get a usable sync.Map for free, and
// a keyed lock that panics when its owner was built as `&T{}` trades a nil-map bug for a crash in
// production code paths that no test touched.
func TestZeroTableIsUsable(t *testing.T) {
	var tbl Table
	release := tbl.Lock("from-a-struct-literal")
	if release == nil {
		t.Fatal("Lock returned no release function")
	}
	if _, ok := tbl.TryLock("from-a-struct-literal", 10*time.Millisecond); ok {
		release()
		t.Fatal("the lock was not held")
	}
	release()
}
