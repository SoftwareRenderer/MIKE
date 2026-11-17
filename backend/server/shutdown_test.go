package server

import (
	"errors"
	"testing"
	"time"
)

// SIGTERM must end the process whatever a step does with the chance: a wedged step ends at the
// deadline rather than leaving a zombie that still holds the data directory open, and a step that
// answers is run once and decides the result.
func TestRunShutdownWithDeadline(t *testing.T) {
	t.Run("a wedged step ends at the deadline", func(t *testing.T) {
		released := make(chan struct{})
		defer close(released) // unblock the wedged step when the test ends

		const deadline = 100 * time.Millisecond
		start := time.Now()
		err := runShutdownWithDeadline(deadline, func() error {
			<-released
			return nil
		})
		elapsed := time.Since(start)

		if !errors.Is(err, ErrShutdownDeadline) {
			t.Fatalf("got %v, want ErrShutdownDeadline", err)
		}
		if elapsed < deadline {
			t.Errorf("returned after %v, before the %v deadline", elapsed, deadline)
		}
		if elapsed > 5*time.Second {
			t.Errorf("returned after %v: the deadline did not end the wedged step", elapsed)
		}
	})

	t.Run("the step's own error is the result", func(t *testing.T) {
		cleanupErr := errors.New("database close failed")
		calls := 0
		err := runShutdownWithDeadline(5*time.Second, func() error {
			calls++
			return cleanupErr
		})
		if calls != 1 {
			t.Fatalf("shutdown ran %d times, want 1", calls)
		}
		if !errors.Is(err, cleanupErr) {
			t.Fatalf("got %v, want the cleanup's own error", err)
		}
	})
}
