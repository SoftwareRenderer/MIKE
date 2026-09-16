// Package httputil holds the shared HTTP vocabulary: JSON responses, request decoding, and
// Fail, which maps a domain error to a status so handlers never guess whether an error
// means "not found".
package httputil

import (
	"encoding/json"
	"errors"
	"net/http"

	"mike/backend/errs"
	"mike/pkg/logging"
)

// JSONResponse sends a JSON response with the given status code.
func JSONResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// JSONError sends a JSON error response.
func JSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// MethodNotAllowed answers 405 for a handler registered without a method in its
// route. The body is plain text; a handler whose client parses error bodies
// answers with JSONError instead.
func MethodNotAllowed(w http.ResponseWriter) {
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// MaxRequestBodyBytes bounds one request body. The largest thing the API legitimately
// sends is a user message with four inline images (~21 MB base64), so this leaves room
// and nothing more: without a bound any client with a session can make the server read
// JSON into memory until it dies.
const MaxRequestBodyBytes int64 = 32 << 20

// DecodeJSON decodes the JSON request body into v, bounded by MaxRequestBodyBytes. On failure
// it writes 400, or 413 when the body was simply too big, and returns false so handlers read as
// a linear happy path:
//
//	var req FooRequest
//	if !httputil.DecodeJSON(w, r, &req) {
//	        return
//	}
//
// It checks nothing else: method and required fields stay with the handler.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeJSON(w, r, v, MaxRequestBodyBytes)
}

// decodeJSON is DecodeJSON with the limit injected, so the 413 path is testable
// without building a 32 MB body.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if err := dec.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			JSONError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		JSONError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// statusClientClosedRequest is nginx's convention for "the peer hung up before we answered". Go has no
// constant for it, and it belongs in that class rather than 500: a closed tab is not a server fault, and
// a 5xx from an ordinary navigation is what makes an error rate unreadable.
const statusClientClosedRequest = 499

// Fail maps an error from a domain call to a status, so a handler never guesses:
//
//   - errs.ErrNotFound  → 404 with notFoundMessage
//   - errs.ErrConflict  → 409 with the producer's own sentence (it was written for the person who
//     caused it: "task already running")
//   - context.Canceled  → 499, logged at debug
//   - anything else, nil included → 500 "internal error", real error logged at error
//
// Inferring 404 from `err != nil` reports a closed database or a SQLITE_BUSY as a missing row; only the
// error knows which it was. Inferring 500 from it reports a user's double-click as a server fault, which
// is the same mistake pointed at the other end. `operation` ("get task") names the log line and never reaches
// the client.
//
// The two ways a request ends get different answers, and the pair is the rule: **the user left**
// (`context.Canceled` → 499 at debug — there is no fault to report, and an operator's log is not a transcript
// of every closed tab) or **the server could not answer** (everything else, at error). Neither is bounded
// here: a deadline on a request path fails the browser while the server finishes the work behind it, and the
// client then believes something failed. A subprocess or a protocol round-trip may carry its own timeout; a
// wait on this server does not. What a long wait gets instead is a report naming what it is still waiting on
// — `subproc.Watch` for a subprocess, the keylock slow-wait for a lock.
func Fail(w http.ResponseWriter, err error, operation, notFoundMessage string) {
	switch {
	case errs.IsNotFound(err):
		JSONError(w, http.StatusNotFound, notFoundMessage)
	case errs.IsConflict(err):
		// The producer's own sentence, not err.Error(): by the time the error reaches here it is often
		// wrapped, and a chain carries paths and driver text.
		message := errs.Message(err)
		if message == "" {
			message = errs.ErrConflict.Error()
		}
		JSONError(w, http.StatusConflict, message)
	case errs.IsOperation(err):
		// The browser gets MIKE's own sentence and the log gets the cause, which is the opposite of
		// what a hand-written 500 does: git's stderr reaches the panel and leaves no trace on the
		// server, so the one person who can act on it sees nothing.
		logging.For(logging.HTTP).Errorf("%s failed: %v", operation, errs.Cause(err))
		JSONError(w, http.StatusInternalServerError, errs.Message(err))
	case errs.IsCanceled(err):
		logging.For(logging.HTTP).Debugf("%s cancelled: %v", operation, err)
		JSONError(w, statusClientClosedRequest, "request cancelled")
	default:
		logging.For(logging.HTTP).Errorf("%s failed: %v", operation, err)
		JSONError(w, http.StatusInternalServerError, "internal error")
	}
}

// Fault answers the one outcome a handler has no richer classification for: the operation did not
// complete and the cause is MIKE's problem, not the caller's. It is Fail with an errs.Operation, so
// the handler states what it was doing and the cause goes to the log rather than the browser.
//
// Use Fail when the error may classify as a missing thing or a conflict; Fault when it cannot.
func Fault(w http.ResponseWriter, operation string, err error) {
	Fail(w, errs.Operation(operation, err), operation, operation+" failed")
}
