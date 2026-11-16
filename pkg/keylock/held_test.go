package keylock

// Held is the question the worktree watcher asks before it publishes: is someone rewriting this right now. It
// answers from a flag, because a probe that drained the token to look at it would let a waiter slip in and be
// handed a lock nobody owns.

import "testing"

func TestHeldFollowsTheHolderAndGrowsNothing(t *testing.T) {
	t.Parallel()
	var table Table

	if table.Held("absent") {
		t.Fatal("a key nobody locked reads as held")
	}
	if len(table.locks) != 0 {
		t.Fatalf("asking about an unknown key left %d entries: a question must not grow the table", len(table.locks))
	}

	release := table.Lock("a")
	if !table.Held("a") {
		t.Fatal("the holder does not read as holding")
	}

	// A waiter does not change the answer, and the token moving to it must not read as free in the gap.
	waiter := make(chan func(), 1)
	go func() { waiter <- table.Lock("a") }()
	release()
	next := <-waiter
	if !table.Held("a") {
		t.Fatal("the lock reads as free while the waiter holds it")
	}

	next()
	if table.Held("a") {
		t.Fatal("the lock still reads as held after its last holder released")
	}
	if len(table.locks) != 0 {
		t.Fatalf("the key outlived its last holder: %d entries left", len(table.locks))
	}
}
