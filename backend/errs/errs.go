// Package errs holds the sentinel errors that cross package boundaries. A producer reports an
// outcome by wrapping one of these; the transport maps it to a status in one place
// (httputil.Fail) instead of guessing from the call site, so producers never mention HTTP and
// handlers never compare error strings.
//
// Only classifications a caller actually acts on belong here.
package errs

import "errors"

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
