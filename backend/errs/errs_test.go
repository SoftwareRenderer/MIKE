package errs

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

func TestConflictClassifiesAndKeepsItsSentence(t *testing.T) {
	// The producer's sentinel shape: a named error that is a conflict and reads as its own sentence.
	errTaskRunning := Conflict("task already running")
	wrapped := fmt.Errorf("start task: %w", errTaskRunning)

	for _, tc := range []struct {
		name, want string
		err        error
	}{
		{name: "sentinel", err: errTaskRunning, want: "task already running"},
		{name: "wrapped", err: wrapped, want: "task already running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !IsConflict(tc.err) {
				t.Errorf("IsConflict(%v) = false, want true", tc.err)
			}
			if got := Message(tc.err); got != tc.want {
				t.Errorf("Message() = %q, want %q — the browser must not see the wrap chain", got, tc.want)
			}
			if IsNotFound(tc.err) {
				t.Error("a conflict classified as not-found; the transport would answer 404")
			}
		})
	}

	if IsConflict(errors.New("no such table: tasks")) {
		t.Error("an arbitrary error classified as a conflict")
	}
	if got := Message(errors.New("plain")); got != "" {
		t.Errorf("Message(plain error) = %q, want empty", got)
	}
}

func TestCancellationIsContextCanceled(t *testing.T) {
	// ErrCanceled must be context.Canceled itself, not a lookalike: producers already return the stdlib
	// value, and a distinct sentinel would leave the common case unclassified.
	if ErrCanceled != context.Canceled {
		t.Error("ErrCanceled is not context.Canceled, so existing producers do not classify")
	}
	if !IsCanceled(fmt.Errorf("clone: %w", context.Canceled)) {
		t.Error("a cancelled clone did not classify as cancelled")
	}
	if IsCanceled(errors.New("database is locked")) {
		t.Error("an unrelated error classified as cancelled")
	}
}

// An operation's cause is often a lookup that failed — and a failed lookup is not "the thing does not
// exist". If Operation unwrapped its cause, a broken repository would answer 404 and the UI would say
// "no such task" about a task that is there.
func TestOperationDoesNotInheritItsCausesClassification(t *testing.T) {
	err := Operation("git log", fmt.Errorf("read index: %w", ErrNotFound))

	if !IsOperation(err) {
		t.Fatal("an operation must classify as a server fault")
	}
	if IsNotFound(err) {
		t.Error("a failed operation inherited NotFound from its cause — a broken repository would answer 404")
	}
	if IsConflict(err) {
		t.Error("a failed operation inherited Conflict from its cause")
	}
}

// The sentence is the response body and the cause is the log line; getting either one backwards is the
// whole leak.
func TestOperationKeepsTheSentenceAndTheCauseApart(t *testing.T) {
	cause := errors.New("exit status 128\nfatal: bad object /home/operator/.git/objects/ab/cdef")
	err := Operation("git log", cause)

	if got := Message(err); got != "git log failed" {
		t.Errorf("Message = %q, want the operation's sentence", got)
	}
	if Cause(err) != cause {
		t.Errorf("Cause = %v, want the wrapped error", Cause(err))
	}
	if !strings.Contains(err.Error(), "git log failed") {
		t.Errorf("Error() = %q, want the sentence (the cause is reached through Cause, not through the message)", err.Error())
	}
	if Cause(ErrNotFound) != nil {
		t.Error("an error with no cause must answer nil, not a zero value")
	}
	// Classification survives the handler's own wrapping on the way to the transport.
	if !IsOperation(fmt.Errorf("branch view: %w", err)) {
		t.Error("IsOperation must see through an ordinary wrap")
	}
}
