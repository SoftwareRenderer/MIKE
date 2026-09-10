// Package errs holds the sentinel errors that cross package boundaries. A producer reports an
// outcome by wrapping one of these; the transport maps it to a status in one place
// (httputil.Fail) instead of guessing from the call site, so producers never mention HTTP and
// handlers never compare error strings.
//
// Only classifications a caller actually acts on belong here.
package errs

import (
	"context"
	"errors"
)

// ErrNotFound marks "the thing named by this request does not exist". It is the
// outcome a caller can act on: answer 404, do not retry, do not report a fault.
//
// Producers wrap it with the entity name so the message stays human without
// losing the classification, e.g. store.ErrTaskNotFound is
//
//	fmt.Errorf("task %w", errs.ErrNotFound)   // message: "task not found"
var ErrNotFound = errors.New("not found")

// IsNotFound reports whether err (at any wrap depth) declares a missing thing.
// A nil err is never "not found" — a caller that checked err != nil and still
// lands here has a bug, and answering 500 surfaces it instead of hiding it.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

// ErrConflict is the outcome "the thing exists and is in a state that forbids this action". It is a
// caller's own doing, not a server fault: the API answers 409 with the producer's sentence, and a
// handler must not be left to infer it from `err != nil` — which answers 500 for something the user
// caused and can act on.
var ErrConflict = errors.New("conflict")

// IsConflict reports whether err declares a state clash.
func IsConflict(err error) bool {
	return errors.Is(err, ErrConflict)
}

// Conflict states a conflict in the words a caller should see ("task already running"). The result
// classifies as ErrConflict at any wrap depth while keeping that sentence as its message, which is what
// lets a producer's sentinel be both a classification and the browser's text:
//
//	var ErrTaskAlreadyRunning = errs.Conflict("task already running")
func Conflict(message string) error {
	return &classified{message: message, class: ErrConflict}
}

// ErrCanceled is spelled context.Canceled so a producer that already returns that (a client that went
// away, a shutdown that cancelled the work) classifies without changing what it returns. Cancellation is
// not a server fault: it is logged at debug and answered out of the 5xx class, so a closed browser tab
// never counts as, or reads as, a failure anyone must chase.
var ErrCanceled = context.Canceled

// IsCanceled reports whether the work stopped because it was cancelled.
func IsCanceled(err error) bool {
	return errors.Is(err, ErrCanceled)
}

// ErrOperation is the outcome "MIKE could not do the thing the request asked for" — a git subprocess
// failed, the store refused a read, a registry did not answer. It is a server fault, so it answers 500,
// and it is the one class whose browser text MIKE writes itself: the cause carries git's stderr, a
// driver message and absolute host paths, which belong in the log.
//
// Producers reach for this instead of writing a status code, which is what leaves a handler free to
// answer through httputil.Fail without guessing whether the failure was the caller's fault.
var ErrOperation = errors.New("operation failed")

// IsOperation reports whether err declares a server fault.
func IsOperation(err error) bool {
	return errors.Is(err, ErrOperation)
}

// Operation states a server fault in the words the caller may see: "<operation> failed". The cause is
// kept for the log and is never the response body:
//
//	return errs.Operation("git log", err)   // browser: "git log failed" — log: git's stderr
//
// The cause is deliberately not unwrapped: an operation that failed because a lookup found nothing
// still failed, and answering that with 404 would turn a broken repository into "no such task".
func Operation(operation string, cause error) error {
	return &classified{message: operation + " failed", class: ErrOperation, cause: cause}
}

// Cause returns what Operation wrapped, or nil when err carries no cause. A transport logs this; a
// handler must not answer with it.
func Cause(err error) error {
	var c *classified
	if errors.As(err, &c) {
		return c.cause
	}
	return nil
}

// classified carries a classification and the sentence the caller sees, without letting the sentence
// become the classification: Is matches the class, Error returns the message.
type classified struct {
	message string
	class   error
	cause   error
}

func (c *classified) Error() string { return c.message }
func (c *classified) Unwrap() error { return c.class }

// Message returns the sentence a classified error carries for whoever caused it, or "" if err is not one
// of this package's classified errors. A transport answers with this rather than err.Error(): a wrapped
// chain ("start task: open /home/x/.git/index.lock: file exists") carries paths and driver text that
// belong in the log, not the browser.
func Message(err error) string {
	var c *classified
	if errors.As(err, &c) {
		return c.message
	}
	return ""
}
