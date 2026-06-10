package server

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime/debug"

	"kanban/backend/httputil"
)

// Re-export JSON helpers from httputil.
var (
	JSONResponse = httputil.JSONResponse
	JSONError    = httputil.JSONError
	JSONErrorf   = httputil.JSONErrorf
)

// GetEnv returns the environment variable value or a default.
func GetEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

// CORS middleware.
func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// PanicRecoveryMiddleware recovers from panics and returns a JSON error.
func PanicRecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("[PANIC] %s %s: %v\n%s", r.Method, r.URL.Path, err, debug.Stack())
				JSONError(w, http.StatusInternalServerError, fmt.Sprintf("internal server error: %v", err))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
