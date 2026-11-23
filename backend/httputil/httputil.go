// Package httputil holds the shared HTTP vocabulary: JSON responses, request decoding, and
// Fail, which maps a domain error to a status so handlers never guess whether an error
// means "not found".
package httputil

import (
	"bytes"
	"encoding/json/v2"
	"io"
	"net/http"
	"strconv"
	"sync"

	"mike/backend/errs"
	"mike/backend/types"
	"mike/pkg/logging"
)

// JSONResponse sends a JSON response with the given status code.
func JSONResponse(w http.ResponseWriter, status int, v any) {
	writeJSON(w, status, v)
}

// JSONError sends a JSON error response.
func JSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// internalErrorBody is Fail's default sentence, already encoded so the fallback cannot fail the way the
// value it replaces did.
var internalErrorBody = []byte(`{"error":"internal error"}`)

// jsonBuffers recycles the encode buffer. Encoding into a fresh bytes.Buffer costs 5.3x the body — v2
// keeps an encode buffer of its own beside the caller's, so a 900 KB file read asks for 4.8 MB — and a
// buffer that has been that large before asks for nothing. sync.Pool gives the memory back at the next
// GC, so an idle server does not hold the largest response it ever sent.
var jsonBuffers = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// writeJSON encodes the whole value before any of the answer is on the wire, because a status already sent
// cannot be retracted: marshalling straight to the ResponseWriter leaves a success code with a short body
// behind it whenever the encoder refuses the value. The one class it refuses here is a string holding bytes
// that are not valid UTF-8 — what a file's contents are when a route reads one as text — and such a value
// describes no body, so the answer is Fail's default arm with the cause in the log. Every body here comes
// from a read already capped, so the buffer costs nothing the route did not already allow.
// docs/api-contract.md → Reading one file: text, a picture, or bytes.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body := jsonBuffers.Get().(*bytes.Buffer)
	defer func() {
		body.Reset()
		jsonBuffers.Put(body)
	}()
	if err := json.MarshalWrite(body, v); err != nil {
		logging.For(logging.HTTP).Errorf("response body could not be encoded: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(internalErrorBody)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	// A HEAD request — which a GET pattern also answers — has nothing but this to state the length from.
	h.Set("Content-Length", strconv.Itoa(body.Len()))
	w.WriteHeader(status)
	_, _ = w.Write(body.Bytes())
}

// MethodNotAllowed answers 405 with a plain-text body. It stays live even though every route declares
// its verb: a handler several verbs share needs a default arm, and a `GET` pattern also matches HEAD, so
// that arm — not the mux — is what answers a HEAD request. A handler whose client parses error bodies
// answers with JSONError instead. docs/api-contract.md → Route verbs.
func MethodNotAllowed(w http.ResponseWriter) {
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// MaxRequestBodyBytes bounds one request body that carries no attachments: what every JSON route reads
// through DecodeJSON. The largest thing the UI legitimately sends without an image is a file write's
// 1 MB `content` field, so this leaves room and nothing more: without a bound any client with a session
// can make the server read JSON into memory until it dies.
const MaxRequestBodyBytes int64 = 32 << 20

// MaxMessageBodyBytes bounds the two bodies that carry a user message's attachments, the only place an
// image travels: four images at the frontend's intake cap (MAX_IMAGE_SIZE in
// frontend/src/lib/utils/attachments.ts, mirroring exec.MaxUploadBytes) are about 107 MB once base64 adds a
// third. MaxBodyMiddleware carries this number so the largest legitimate message arrives, and a route with
// no attachments stays at MaxRequestBodyBytes.
const MaxMessageBodyBytes int64 = 128 << 20

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
	return decodeJSON(w, r, v, MaxRequestBodyBytes, false)
}

// DecodeMessageJSON is DecodeJSON at MaxMessageBodyBytes, for the routes that carry a user message. A route
// may not pick its own bound.
func DecodeMessageJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeJSON(w, r, v, MaxMessageBodyBytes, false)
}

// DecodeJSONOrEmpty is DecodeJSON for a route whose fields are all optional: a request with no body
// carries no fields, so `v` is left as the handler built it. A body that is present and is not JSON is
// still the 400 DecodeJSON gives.
func DecodeJSONOrEmpty(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeJSON(w, r, v, MaxRequestBodyBytes, true)
}

// decodeJSON is DecodeJSON with the limit injected, so the 413 path is testable
// without building a 32 MB body.
//
// The body is read before it is decoded because the reader cannot tell the two failures apart: a body
// that never arrived and one that stopped half way through a value both arrive as io.ErrUnexpectedEOF.
// `emptyOK` means "no body", not "unreadable body", so the bytes decide.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any, limit int64, emptyOK bool) bool {
	raw, ok := readBody(w, r, limit)
	if !ok {
		return false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		if emptyOK {
			return true
		}
		JSONError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	if err := json.Unmarshal(raw, v); err != nil {
		JSONError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// MessageBody reads a whole body at MaxMessageBodyBytes for a handler that decodes the bytes more than once
// and so cannot hand the read to DecodeMessageJSON. It answers 413 over the cap and 400 for a body that
// failed to arrive.
func MessageBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	return readBody(w, r, MaxMessageBodyBytes)
}

// readBody is the bounded read behind both, and behind decodeJSON with the general cap.
func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		JSONError(w, http.StatusBadRequest, "invalid request body")
		return nil, false
	}
	if int64(len(raw)) > limit {
		JSONError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return nil, false
	}
	return raw, true
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

// ProjectGetter is the store call the project preamble needs.
type ProjectGetter interface {
	GetProject(id string) (*types.Project, error)
}

// ResolveProjectID answers the failure of a project lookup a handler cannot do without, returning
// false so the handler stops. Callers do the lookup: the id comes from a path segment, a query
// parameter, or a row already in hand.
func ResolveProjectID(w http.ResponseWriter, store ProjectGetter, id string) (*types.Project, bool) {
	project, err := store.GetProject(id)
	if err != nil {
		Fail(w, err, "get project", "project not found")
		return nil, false
	}
	return project, true
}
