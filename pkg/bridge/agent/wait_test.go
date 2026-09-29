package main

import "time"

// awaitProcess polls a process predicate until it holds or the deadline passes, and lets the caller
// write the failure sentence because only the caller knows which pid it was chasing. A killed child
// disappears asynchronously, so the only honest wait for one is bounded and re-checked.
func awaitProcess(timeout time.Duration, holds func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if holds() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}
