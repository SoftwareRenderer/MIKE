package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kanban/backend/prompts"
	"kanban/backend/types"

	"github.com/google/uuid"
)

func (s *Server) handleTaskMessages(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method == http.MethodDelete {
		var req struct {
			MessageID string `json:"message_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.MessageID == "" {
			JSONError(w, http.StatusBadRequest, "message_id required")
			return
		}
		if err := s.store.DeleteTaskMessagesAfter(taskID, req.MessageID); err != nil {
			JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if r.Method == http.MethodPost {
		var msg types.TaskMessage
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			JSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if msg.Role == "" || msg.Content == "" {
			JSONError(w, http.StatusBadRequest, "role and content required")
			return
		}
		msg.TaskID = taskID
		msg.ID = uuid.New().String()
		msg.CreatedAt = time.Now()
		if err := s.store.CreateTaskMessage(&msg); err != nil {
			JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		JSONResponse(w, http.StatusCreated, msg)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	messages, err := s.store.GetTaskMessages(taskID)
	if err != nil {
		JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	JSONResponse(w, http.StatusOK, messages)
}

func (s *Server) handleTaskMessageByID(w http.ResponseWriter, r *http.Request, projectID, taskID, messageID string) {
	if r.Method == http.MethodPatch {
		var req struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Content == "" {
			JSONError(w, http.StatusBadRequest, "content required")
			return
		}
		if err := s.store.UpdateTaskMessage(messageID, req.Content); err != nil {
			JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReloadPrompts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	names, err := prompts.ValidateTemplates()
	if err != nil {
		log.Printf("[prompts] reload failed: %v", err)
		JSONError(w, http.StatusBadRequest, fmt.Sprintf("template error: %v", err))
		return
	}
	log.Printf("[prompts] templates validated: %v", names)
	JSONResponse(w, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"message": "Prompt templates validated successfully",
		"files":   names,
	})
}

func (s *Server) handleTestReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.store.ResetData(); err != nil {
		log.Printf("[test-reset] failed to reset data: %v", err)
		JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.executor.ResetForTest()
	JSONResponse(w, http.StatusOK, map[string]string{"status": "reset"})
}

func (s *Server) handleGC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	removed, err := s.executor.WorktreeMgr().GarbageCollect(false)
	if err != nil {
		log.Printf("[gc] manual trigger error: %v", err)
		JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	JSONResponse(w, http.StatusOK, map[string]interface{}{"removed": removed})
}

func (s *Server) handleCreateOrphan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg, err := s.configStore.GetConfig()
	if err != nil {
		JSONError(w, http.StatusInternalServerError, "failed to get config")
		return
	}
	worktreesDir := cfg.WorkspaceDir
	if wtDir := os.Getenv("WORKTREES_DIR"); wtDir != "" {
		worktreesDir = wtDir
	}
	orphanName := fmt.Sprintf("orphan-test-%d", time.Now().UnixNano())
	orphanPath := filepath.Join(worktreesDir, orphanName)
	if err := os.MkdirAll(orphanPath, 0o755); err != nil {
		JSONError(w, http.StatusInternalServerError, fmt.Sprintf("create orphan dir: %v", err))
		return
	}
	JSONResponse(w, http.StatusOK, map[string]string{"orphan": orphanName, "path": orphanPath})
}

func (s *Server) handleFS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		path = string(os.PathSeparator)
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		JSONError(w, http.StatusNotFound, err.Error())
		return
	}

	type fsEntry struct {
		Name      string `json:"name"`
		IsDir     bool   `json:"isDir"`
		Path      string `json:"path"`
		IsGitRepo bool   `json:"isGitRepo"`
	}

	isGit := IsGitRepo(path)

	result := struct {
		Path      string    `json:"path"`
		IsGitRepo bool      `json:"isGitRepo"`
		Entries   []fsEntry `json:"entries"`
	}{
		Path:      path,
		IsGitRepo: isGit,
		Entries:   []fsEntry{},
	}

	if path != string(os.PathSeparator) {
		if parent := filepath.Dir(path); parent != path {
			result.Entries = append(result.Entries, fsEntry{Name: "..", IsDir: true, Path: parent, IsGitRepo: IsGitRepo(parent)})
		}
	}

	for _, e := range entries {
		if e.Name()[0] == '.' || !e.IsDir() {
			continue
		}
		fullPath := filepath.Join(path, e.Name())
		result.Entries = append(result.Entries, fsEntry{
			Name:      e.Name(),
			IsDir:     e.IsDir(),
			Path:      fullPath,
			IsGitRepo: IsGitRepo(fullPath),
		})
	}

	sort.SliceStable(result.Entries, func(i, j int) bool {
		return strings.ToLower(result.Entries[i].Name) < strings.ToLower(result.Entries[j].Name)
	})

	JSONResponse(w, http.StatusOK, result)
}

func (s *Server) handleWorktreeExists(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cfg, err := s.configStore.GetConfig()
	if err != nil {
		JSONError(w, http.StatusInternalServerError, "failed to get config")
		return
	}

	worktreePath := filepath.Join(cfg.WorkspaceDir, taskID)
	_, err = os.Stat(worktreePath)
	exists := err == nil

	JSONResponse(w, http.StatusOK, map[string]bool{"exists": exists})
}
