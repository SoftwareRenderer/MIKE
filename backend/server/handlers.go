package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"kanban/backend/llama"
	"kanban/backend/prompts"
	"kanban/backend/types"
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
		// Get the task to find its current thread
		task, _ := s.store.GetTask(projectID, taskID)
		threadID := ""
		if task != nil {
			threadID = task.CurrentThreadID
		}
		if err := s.store.DeleteTaskMessagesAfter(taskID, req.MessageID, threadID); err != nil {
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
		// Set thread_id from task's current thread if not provided
		if msg.ThreadID == "" {
			task, _ := s.store.GetTask(projectID, taskID)
			if task != nil {
				msg.ThreadID = task.CurrentThreadID
			}
		}
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
	// Get the task to find its current thread
		task, taskErr := s.store.GetTask(projectID, taskID)
		threadID := ""
		if taskErr == nil && task != nil {
			threadID = task.CurrentThreadID
		}
		messages, err := s.store.GetTaskMessages(taskID, threadID)
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

// handleThreads lists all threads for a task.
func (s *Server) handleThreads(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	threads, err := s.store.GetThreadsByTask(taskID)
	if err != nil {
		JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	JSONResponse(w, http.StatusOK, threads)
}

// handleThreadSwitch switches the active thread for a task.
func (s *Server) handleThreadSwitch(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ThreadID string `json:"thread_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ThreadID == "" {
		JSONError(w, http.StatusBadRequest, "thread_id required")
		return
	}
	// Validate task exists
	task, err := s.store.GetTask(projectID, taskID)
	if err != nil {
		JSONError(w, http.StatusNotFound, "task not found")
		return
	}
	// Validate thread exists and belongs to this task
	thread, err := s.store.GetThread(req.ThreadID)
	if err != nil || thread.TaskID != taskID {
		JSONError(w, http.StatusBadRequest, "thread not found")
		return
	}
	// Stop executor if running
	if task.StatusState == "running" {
		_ = s.executor.StopTask(taskID)
	}
	// Commit current worktree state to old thread's branch, checkout new thread's branch
	if task.Branch != "" {
		oldThreadID := task.CurrentThreadID
		if oldThreadID != "" && oldThreadID != req.ThreadID {
			// Commit current state to old thread's branch
			if thread.GitBranch != "" {
				_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "add", "-A")
				_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "commit", "-m", "Thread switch snapshot", "--allow-empty")
				_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "checkout", "-B", thread.GitBranch)
			}
		}
	}
	// Set new active thread
	if err := s.store.SetActiveThread(req.ThreadID); err != nil {
		JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Update task's current_thread_id
	task.CurrentThreadID = req.ThreadID
	task.UpdatedAt = time.Now().UTC()
	if err := s.store.UpdateTask(task); err != nil {
		JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.boardStreamMgr.Broadcast(projectID)
	// Broadcast thread_switch event to WebSocket clients
	b := s.executor.GetOrCreateStream(taskID)
	b.Send(types.StreamEvent{Type: types.WSTypeThreadSwitch, Data: map[string]string{"thread_id": req.ThreadID, "task_id": taskID}})
	JSONResponse(w, http.StatusOK, thread)
}

// handleThreadFork creates a new thread by forking from a checkpoint.
func (s *Server) handleThreadFork(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		FromThreadID  string `json:"from_thread_id"`
		FromMessageID string `json:"from_message_id"`
		NewContent    string `json:"new_content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.FromThreadID == "" || req.FromMessageID == "" {
		JSONError(w, http.StatusBadRequest, "from_thread_id and from_message_id required")
		return
	}
	// If new_content is provided, stop the current executor so the next run
	// loads the forked context instead of the stale original thread.
	if req.NewContent != "" {
		_ = s.executor.StopTask(taskID)
	}
	// Validate task exists
	task, err := s.store.GetTask(projectID, taskID)
	if err != nil {
		JSONError(w, http.StatusNotFound, "task not found")
		return
	}
	// Validate source thread exists
	sourceThread, err := s.store.GetThread(req.FromThreadID)
	if err != nil || sourceThread.TaskID != taskID {
		JSONError(w, http.StatusBadRequest, "source thread not found")
		return
	}
	// Generate new thread ID and branch name
	newThreadID := uuid.New().String()
	newBranchName := fmt.Sprintf("thread/%s", newThreadID[:8])
	// Create new git branch from current HEAD (current worktree state)
	if task.Branch != "" {
		_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "branch", newBranchName)
	}
	// Look up the checkpoint message's created_at for frontend filtering
	var checkpointMsgTime time.Time
	if ct, err := s.store.GetMessageCreatedAt(req.FromMessageID); err == nil && ct != nil {
		checkpointMsgTime = *ct
	}
	// Create new thread
	now := time.Now().UTC()
	newThread := &types.TaskThread{
		ID:                  newThreadID,
		TaskID:              taskID,
		ParentThreadID:      req.FromThreadID,
		Name:                "",
		IsActive:            true,
		GitBranch:           newBranchName,
		CheckpointMessageID: req.FromMessageID,
		CheckpointMessageTime: checkpointMsgTime,
		CreatedAt:           now,
	}
	// Generate thread name from new_content if provided, otherwise use "Fork #N"
	threads, _ := s.store.GetThreadsByTask(taskID)
	if req.NewContent != "" {
		newThread.Name = s.generateThreadName(task, req.NewContent)
	} else {
		newThread.Name = fmt.Sprintf("Fork #%d", len(threads)+1)
	}
	if err := s.store.CreateThread(newThread); err != nil {
		JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Copy messages from source thread up to and including the checkpoint.
	if err := s.store.CopyMessagesToThread(req.FromThreadID, newThreadID, req.FromMessageID); err != nil {
		JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// If new_content is provided, update the checkpoint message in the fork
	// to the edited content. Find the last user message in the new thread
	// (the copied checkpoint) and update its content.
	if req.NewContent != "" {
		if copiedMsgID, err := s.store.GetLastUserMessageInThread(newThreadID); err == nil && copiedMsgID != "" {
			_ = s.store.UpdateTaskMessage(copiedMsgID, req.NewContent)
		}
		// Stop the current executor so the next run loads the forked context.
		_ = s.executor.StopTask(taskID)
	}
	// Set new thread as active
	if err := s.store.SetActiveThread(newThreadID); err != nil {
		JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Update task's current_thread_id
	task.CurrentThreadID = newThreadID
	task.UpdatedAt = now
	if err := s.store.UpdateTask(task); err != nil {
		JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.boardStreamMgr.Broadcast(projectID)
	// Broadcast thread_switch event
	b := s.executor.GetOrCreateStream(taskID)
	b.Send(types.StreamEvent{Type: types.WSTypeThreadSwitch, Data: map[string]string{"thread_id": newThreadID, "task_id": taskID}})
	JSONResponse(w, http.StatusCreated, newThread)
}

// generateThreadName uses the LLM to generate a short thread label from a message.
// Falls back to truncating the text if the LLM is unavailable or errors.
func (s *Server) generateThreadName(task *types.Task, content string) string {
	trimmed := strings.TrimSpace(content)
	if len(trimmed) == 0 {
		return "Fork"
	}

	// Try LLM generation if config is available
	if task.LlamaServerURL != "" && task.ModelName != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		client := llama.NewLlamaClient(task.LlamaServerURL, task.ModelName, "off")
		messages := []types.ChatMessage{
			{Role: "system", Content: "You are a thread name generator. Given a user message, generate a very short, descriptive thread label (max 30 characters, no quotes). Return only the label, nothing else."},
			{Role: "user", Content: trimmed},
		}

		_, name, err := client.ChatCompletion(ctx, messages)
		if err == nil && strings.TrimSpace(name) != "" {
			name = strings.TrimSpace(name)
			if len(name) > 35 {
				name = name[:35]
			}
			log.Printf("[thread-name] task=%s generated: %q", task.ID, name)
			return name
		}
		log.Printf("[thread-name] task=%s LLM call failed, using fallback: %v", task.ID, err)
	}

	// Fallback: truncate the text
	if len(trimmed) > 35 {
		return trimmed[:35]
	}
	return trimmed
}
