package errs

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsNotFoundClassifiesAtAnyWrapDepth(t *testing.T) {
	// A producer's real shape: entity sentinel wrapping the taxonomy sentinel.
	entity := fmt.Errorf("task %w", ErrNotFound)
	reworked := fmt.Errorf("resolve worktree: %w", fmt.Errorf("load task: %w", entity))

	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "bare sentinel", err: ErrNotFound, want: true},
		{name: "entity sentinel wrapping it", err: entity, want: true},
		{name: "two layers of context", err: reworked, want: true},
		{name: "unrelated error", err: errors.New("database is locked"), want: false},
		{name: "unrelated wrapped error", err: fmt.Errorf("exec: %w", errors.New("no such table")), want: false},
		{name: "nil is never not-found", err: nil, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNotFound(tc.err); got != tc.want {
				t.Errorf("IsNotFound(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestNotFoundMessageComposes(t *testing.T) {
	// The wrapping pattern documented on ErrNotFound must produce a message that
	// reads the same as the plain string handlers used to send.
	got := fmt.Errorf("task %w", ErrNotFound).Error()
	if got != "task not found" {
		t.Errorf("composed message = %q, want %q", got, "task not found")
	}
}
