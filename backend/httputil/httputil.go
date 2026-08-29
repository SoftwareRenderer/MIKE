package httputil

import (
	"encoding/json"
	"net/http"
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
