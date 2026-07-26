package server

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"kanban/backend/prompts"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Printf("fs home dir failed: %v", err)
		JSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	JSONResponse(w, http.StatusOK, map[string]string{"home": homeDir})
}

func (s *Server) handleReloadPrompts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	names, err := prompts.ValidateTemplates()
	if err != nil {
		log.Printf("prompts reload failed: %v", err)
		JSONError(w, http.StatusBadRequest, fmt.Sprintf("template error: %v", err))
		return
	}
	log.Printf("prompts templates validated: %v", names)
	JSONResponse(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"message": "Prompt templates validated successfully",
		"files":   names,
	})
}

func (s *Server) handleGC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	removed, err := s.executor.WorktreeMgr().GarbageCollect(false)
	if err != nil {
		log.Printf("gc manual trigger error: %v", err)
		JSONError(w, http.StatusInternalServerError, "internal error")
		return
	}
	JSONResponse(w, http.StatusOK, map[string]any{"removed": removed})
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
