package server

import (
	"mike/pkg/logging"
	"net/http"
	"os"
	"strings"

	"mike/backend/types"
)

// taskInPath reads the task the request's path names and answers it when there is none.
//
// ok is false after writing the response.
func (s *Server) taskInPath(w http.ResponseWriter, projectID, taskID string) (*types.Task, bool) {
	task, err := s.store.GetTask(projectID, taskID)
	if err != nil {
		Fail(w, err, "get task", "task not found")
		return nil, false
	}
	return task, true
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		logging.For(logging.HTTP).Errorf("fs home dir failed: %v", err)
		JSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	JSONResponse(w, http.StatusOK, map[string]any{
		"home": homeDir,
	})
}

// safeNotFoundError returns a generic not-found message without leaking the path.
func safeNotFoundError() string {
	return "file not found"
}

// shellQuote wraps a string in single quotes for safe use as a single argument
// in shell commands executed via `sh -c`. It escapes any embedded single quotes
// using the standard POSIX technique (close quote, escaped quote, reopen quote).
// This prevents command injection when interpolating paths or other user-derived
// data into shell command strings.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
