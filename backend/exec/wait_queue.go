package exec

// waitQueue is the queue bookkeeping both gates share, always reached under the gate's own mutex.
// What makes a waiter live is the gates' business; find-and-drop is all that lives here.
type waitQueue[T comparable] struct {
	entries []T
}

func (q *waitQueue[T]) push(e T) {
	q.entries = append(q.entries, e)
}

// index returns e's position, or -1 when it is no longer queued.
func (q *waitQueue[T]) index(e T) int {
	for i, x := range q.entries {
		if x == e {
			return i
		}
	}
	return -1
}

// remove drops e. The waiter's cancellation and the granter's handoff both reach it, so finding
// nothing the second time is normal.
func (q *waitQueue[T]) remove(e T) {
	if i := q.index(e); i != -1 {
		q.entries = append(q.entries[:i], q.entries[i+1:]...)
	}
}

// Len returns the number of queued waiters.
func (q *waitQueue[T]) Len() int { return len(q.entries) }

// wake hints a waiter that something changed without waiting for it to listen. The waiter re-reads the
// gate under the lock, so a second hint adds nothing and done holds only one.
func wake(done chan struct{}) {
	select {
	case done <- struct{}{}:
	default:
	}
}
