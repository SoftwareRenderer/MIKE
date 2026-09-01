// Package httputil holds the shared HTTP vocabulary: JSON responses, request decoding, and
// Fail, which maps a domain error to a status so handlers never guess whether an error
// means "not found".
package httputil

import (
	"encoding/json"
	"log"
	"net/http"

	"kanban/backend/errs"
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

// DecodeJSON decodes the JSON request body into v. On failure it writes the
// standard 400 "invalid request body" response and returns false, so handlers
// read as a linear happy path:
//
//	var req FooRequest
//	if !httputil.DecodeJSON(w, r, &req) {
//		return
//	}
//
// It checks nothing else on purpose — method and required fields stay with the
// handler, which is where the domain rules for them live.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
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
	log.Printf("%s failed: %v", operation, err)
	JSONError(w, http.StatusInternalServerError, "internal error")
}
