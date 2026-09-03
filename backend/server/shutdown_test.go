package server

import (
	"context"
	"errors"
	"testing"
	"time"
)

// SIGTERM must end the process. A wedged step would otherwise leave a zombie that still holds the
// data directory open.
func TestRunShutdownWithDeadlineReturnsAtDeadline(t *testing.T) {
	released := make(chan struct{})
	defer close(released) // unblock the wedged step when the test ends

	err := runShutdownWithDeadline(100*time.Millisecond, func(ctx context.Context) error {
		<-released
		return nil
	})

	start := time.Now()
	if !errors.Is(err, ErrShutdownDeadline) {
		t.Fatalf("got %v, want ErrShutdownDeadline", err)
	}
	if start.After(time.Now().Add(5 * time.Second)) {
		t.Fatal("deadline path did not return")
	}
}

func TestRunShutdownWithDeadlinePropagatesCleanResult(t *testing.T) {
	cleanupErr := errors.New("database close failed")
	calls := 0
	err := runShutdownWithDeadline(5*time.Second, func(ctx context.Context) error {
		calls++
		return cleanupErr
	})
	if calls != 1 {
		t.Fatalf("shutdown ran %d times, want 1", calls)
	}
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("got %v, want the cleanup's own error", err)
	}
}
