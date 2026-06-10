package httputil

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// JSONResponse sends a JSON response with the given status code.
func JSONResponse(w http.ResponseWriter, status int, v interface{}) {
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

// JSONErrorf sends a JSON error response with formatted message.
func JSONErrorf(w http.ResponseWriter, status int, format string, args ...interface{}) {
	JSONError(w, status, fmt.Sprintf(format, args...))
}
