// Package keylock serializes work per key and lets a waiter give up.
//
// One Table is one lock domain. A domain is a named type in the package that owns it —
// `type stashLocks struct{ *keylock.Table }` — and both halves of that matter. The distinct type makes
// passing one domain's lock where another belongs a compile error; the distinct table makes it
// unrepresentable at runtime too, because a task ID and a project ID are drawn from the same alphabet
// (task IDs are three words from the same word list a project ID is a path slug over), so a
// shared table behind several aliases would fuse two domains in silence.
//
// Two acquisition forms, because they answer different questions:
//
//   - Lock waits as long as it takes. Correct when the waiting work has to happen anyway and nobody
//     is watching — the exec loop serializing a merge behind the task's own tool calls.
//   - TryLock gives up and says so. Correct when a person is waiting and "still working" would be a
//     lie: the caller answers "the project is busy with another git operation, try again".
package keylock

import (
	"sync"
	"time"
)

// Table holds one domain's locks. Use it through a pointer and never copy it: a copy would share the
// map while owning a separate guard, so two callers could insert the same key twice and serialize on
// different locks while both believe they hold one. The embedded sync.Mutex is what makes `go vet`
// refuse the copy.
type Table struct {
	mu        sync.Mutex
	locks     map[string]*entry
	slowAfter time.Duration // zero disables the slow-wait report
	onSlow    WaitReport
}

// WaitReport is called after a wait that took longer than the table's threshold. It exists because a
// bounded wait is the wrong way to find a wedged operation when a legitimate one can be long: a
// project's merge lock is held across a wait on a task's tool lock (`exec/merge.go:654` → `:672`), and
// that one is held across a whole tool call — a `bash` of up to five minutes. Bounding the merge wait
// would fail the merges it is meant to protect. Reporting the wait keeps the signal a bound was bought
// for, and cannot break anything.
type WaitReport func(key string, waited time.Duration)

// entry is one key's lock plus the goroutines holding a reference to it: whoever holds the lock, and
// every waiter that has not given up. The key becomes deletable only when that count reaches zero,
// which is what stops a waiter sleeping on a lock that nothing can release any more.
type entry struct {
	table *Table
	key   string
	sem   chan struct{} // capacity 1; the token is the lock
	taken bool          // the token is out; guarded by table.mu, and what Held answers from
	refs  int           // guarded by table.mu
}

// setTaken records who moved the token. A reader cannot probe the channel instead: draining the token to look
// at it would let a waiter take it in the gap.
func (e *entry) setTaken(taken bool) {
	t := e.table
	t.mu.Lock()
	e.taken = taken
	t.mu.Unlock()
}

// take moves the token here and records that it is out. The two select clauses name the channel, so they record
// the flag themselves; a token moved without the record is a lock that reads as free.
func (e *entry) take() {
	e.sem <- struct{}{}
	e.setTaken(true)
}

// Held reports whether some goroutine holds the key's lock right now. It reads a moment, not a promise — the
// holder may release as this returns — so it is for a reader that will look again, never for admission, and it
// never creates an entry.
func (t *Table) Held(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.locks[key]
	return ok && e.taken
}

// Configure sets the slow-wait report; pass 0 and nil for none. Call it once, while building the owner,
// and never while the table is in use: the two fields are read without the lock on the hot path.
func (t *Table) Configure(slowAfter time.Duration, onSlow WaitReport) {
	t.slowAfter, t.onSlow = slowAfter, onSlow
}

// Lock takes the key's lock and returns the function that releases it. It waits indefinitely: a caller
// that can tell someone to come back later should be calling TryLock.
func (t *Table) Lock(key string) func() {
	e := t.acquire(key)
	t.wait(e)
	return e.held
}

// TryLock takes the key's lock if it becomes free within timeout, and reports whether it did. On the
// false return nothing is held and no waiter is left behind, so a timed-out wait cannot later steal a
// turn and hand the key back to someone else.
//
// A negative timeout means wait indefinitely, which is what Lock asks for.
func (t *Table) TryLock(key string, timeout time.Duration) (func(), bool) {
	e := t.acquire(key)
	if timeout < 0 {
		t.wait(e)
		return e.held, true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	start := time.Now()
	select {
	case e.sem <- struct{}{}: // a select clause names the channel, so this site records the flag itself
		e.setTaken(true)
		t.report(e.key, time.Since(start))
		return e.held, true
	case <-timer.C:
		e.drop()
		return nil, false
	}
}

// wait blocks for the key's token, then reports the wait if it was slow enough to be interesting.
// The clock is started before the send and read after it: the point of the number is how long the
// caller stood here, which is what a wedge looks like from the inside.
func (t *Table) wait(e *entry) {
	if t.slowAfter <= 0 || t.onSlow == nil {
		e.take()
		return
	}
	start := time.Now()
	e.take()
	t.report(e.key, time.Since(start))
}

func (t *Table) report(key string, waited time.Duration) {
	if t.slowAfter > 0 && t.onSlow != nil && waited > t.slowAfter {
		t.onSlow(key, waited)
	}
}

// acquire returns the key's entry with a reference held on it. Waiting happens outside this critical
// section: a blocked sender must never be holding the table lock.
//
// The map is created here rather than only in New so that a zero Table is a usable Table, which is what
// sync.Map gave every struct literal in this repo: a lock that panics when its owner was built with
// `&T{}` is a crash waiting in production.
func (t *Table) acquire(key string) *entry {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.locks == nil {
		t.locks = map[string]*entry{}
	}
	e, ok := t.locks[key]
	if !ok {
		e = &entry{table: t, key: key, sem: make(chan struct{}, 1)}
		t.locks[key] = e
	}
	e.refs++
	return e
}

// held is the release function handed to the caller. A double release panics instead of becoming a
// no-op, so a caller that does not know who owns the lock is reported at the release rather than
// turning into a hang later.
func (e *entry) held() {
	select {
	case <-e.sem:
		e.setTaken(false)
		e.drop()
	default:
		panic("keylock: release called for a lock this holder does not hold (key " + e.key + ")")
	}
}

// drop releases a reference and removes the key when nobody is waiting or holding any more. Both
// halves happen under the table lock; see the note on entry about why they must.
func (e *entry) drop() {
	t := e.table
	t.mu.Lock()
	defer t.mu.Unlock()
	e.refs--
	if e.refs == 0 && t.locks[e.key] == e {
		delete(t.locks, e.key)
	}
}

// WaitNotice reports a wait that is still going. acquired is true on the final call, once the lock is held.
type WaitNotice func(waited time.Duration, acquired bool)

// LockWatched reports the wait every `every`, then once with acquired=true. A free lock never calls notice.
func (t *Table) LockWatched(key string, every time.Duration, notice WaitNotice) func() {
	if notice == nil {
		return t.Lock(key)
	}
	e := t.acquire(key)
	if every <= 0 {
		every = time.Second
	}
	start := time.Now()
	tick := time.NewTicker(every)
	defer tick.Stop()
	reported := false
	for {
		select {
		case <-tick.C:
			reported = true
			notice(time.Since(start), false)
		case e.sem <- struct{}{}:
			e.setTaken(true)
			waited := time.Since(start)
			if reported {
				notice(waited, true)
			}
			t.report(e.key, waited)
			return e.held
		}
	}
}
