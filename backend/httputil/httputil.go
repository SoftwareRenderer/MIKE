// Package httputil holds the shared HTTP vocabulary: JSON responses, request decoding, and
// Fail, which maps a domain error to a status so handlers never guess whether an error
// means "not found".
package httputil

import (
	"encoding/json"
	"errors"
	"net/http"

	"kanban/backend/errs"
	"kanban/pkg/logging"
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

// Fail maps an error from a domain call to a status, so a handler never guesses:
//
//   - an error declaring errs.ErrNotFound → 404 with notFoundMessage
//   - anything else, nil included        → 500 "internal error", real error logged
//
// Inferring 404 from `err != nil` reports a closed database or a SQLITE_BUSY as a missing
// row; only the error knows which it was. operation ("get task") is for the log line only
// and never reaches the client.
func Fail(w http.ResponseWriter, err error, operation, notFoundMessage string) {
	if errs.IsNotFound(err) {
		JSONError(w, http.StatusNotFound, notFoundMessage)
		return
	}
	logging.For(logging.HTTP).Errorf("%s failed: %v", operation, err)
	JSONError(w, http.StatusInternalServerError, "internal error")
}
