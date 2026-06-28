package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"kanban/backend/git"
	"kanban/backend/managers"
	"kanban/backend/prompts"
	"kanban/backend/types"
)

func (s *Server) handleTaskMessages(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method == http.MethodDelete {
		var req struct {
			MessageID    string `json:"message_id"`
			DeleteSingle bool   `json:"delete_single"`
			Regenerate   bool   `json:"regenerate"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.MessageID == "" {
			JSONError(w, http.StatusBadRequest, "message_id required")
			return
		}
		// If delete_single is true, only remove that one message (e.g., dismissing an error).
		if req.DeleteSingle {
			if err := s.store.DeleteTaskMessageByID(req.MessageID); err != nil {
				JSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
			JSONResponse(w, http.StatusOK, map[string]string{"status": "deleted"})
			return
		}
		// Get the task to find its current thread
		task, _ := s.store.GetTask(projectID, taskID)
		threadID := ""
		if task != nil {
			threadID = task.CurrentThreadID
		}
		// Get the message's created_at BEFORE deletion (needed for checkpoint matching below).
		msgCreatedAt, msgTimeErr := s.store.GetMessageCreatedAt(req.MessageID)
		if err := s.store.DeleteTaskMessagesAfter(taskID, req.MessageID, threadID); err != nil {
			JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Check if the deleted message was a checkpoint for any fork threads.
		// If so, delete those orphaned fork threads and switch to parent if needed.
		// Skip during regeneration — the message is being re-sent, not permanently removed.
		if !req.Regenerate {
			s.cleanupDeletedMessageForks(projectID, taskID, task, threadID, req.MessageID, msgCreatedAt, msgTimeErr)
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
		msg.CreatedAt = time.Now().UTC()
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

func (s *Server) handleTaskMessageByID(w http.ResponseWriter, r *http.Request) {
	messageID := r.PathValue("messageID")
	if r.Method == http.MethodGet {
		// Check if deleting this message would delete a fork branch.
		// Used by the frontend to show a warning in the delete dialog.
		checkpointThreads, _ := s.store.GetThreadsByCheckpointMessageID(messageID)
		if len(checkpointThreads) == 0 {
			if msgTime, err := s.store.GetMessageCreatedAt(messageID); err == nil && msgTime != nil {
				checkpointThreads, _ = s.store.GetThreadsByCheckpointMessageTime(msgTime.Format(time.RFC3339))
			}
		}
		willDeleteBranch := false
		var branchName string
		if len(checkpointThreads) > 0 {
			willDeleteBranch = true
			if checkpointThreads[0].GitBranch != "" {
				branchName = checkpointThreads[0].GitBranch
			}
		}
		JSONResponse(w, http.StatusOK, map[string]any{
			"will_delete_branch": willDeleteBranch,
			"branch_name":        branchName,
		})
		return
	}
	if r.Method == http.MethodPatch {
		var req struct {
			Content    string `json:"content"`
			CommitHash string `json:"git_commit_hash"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			JSONError(w, http.StatusBadRequest, "invalid request")
			return
		}
		if req.Content != "" {
			if err := s.store.UpdateTaskMessage(messageID, req.Content); err != nil {
				JSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		if req.CommitHash != "" {
			if err := s.store.UpdateTaskMessageCommitHash(messageID, req.CommitHash); err != nil {
				JSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
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

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		JSONError(w, http.StatusInternalServerError, err.Error())
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
		log.Printf("[prompts] reload failed: %v", err)
		JSONError(w, http.StatusBadRequest, fmt.Sprintf("template error: %v", err))
		return
	}
	log.Printf("[prompts] templates validated: %v", names)
	JSONResponse(w, http.StatusOK, map[string]any{
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
	JSONResponse(w, http.StatusOK, map[string]any{"removed": removed})
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

// handleFSRead reads and returns the content of a file.
func (s *Server) handleFSRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		JSONError(w, http.StatusBadRequest, "path is required")
		return
	}
	// Validate path is within allowed project root
	if err := s.validateFSPath(path, r); err != nil {
		JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Limit file size to 1MB to avoid loading huge files
	content, err := os.ReadFile(path)
	if err != nil {
		JSONError(w, http.StatusNotFound, err.Error())
		return
	}
	if len(content) > 1_000_000 {
		JSONError(w, http.StatusRequestEntityTooLarge, "file too large (max 1MB)")
		return
	}
	JSONResponse(w, http.StatusOK, map[string]string{"content": string(content)})
}

// handleFSWrite writes content to a file.
func (s *Server) handleFSWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		JSONError(w, http.StatusBadRequest, "path is required")
		return
	}
	// Validate path is within allowed project root
	if err := s.validateFSPath(req.Path, r); err != nil {
		JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Limit file size to 1MB
	if len(req.Content) > 1_000_000 {
		JSONError(w, http.StatusRequestEntityTooLarge, "file too large (max 1MB)")
		return
	}
	// Ensure parent directory exists
	dir := filepath.Dir(req.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		JSONError(w, http.StatusInternalServerError, fmt.Sprintf("create directory: %v", err))
		return
	}
	if err := os.WriteFile(req.Path, []byte(req.Content), 0644); err != nil {
		JSONError(w, http.StatusInternalServerError, fmt.Sprintf("write file: %v", err))
		return
	}
	JSONResponse(w, http.StatusOK, map[string]string{"status": "written", "path": req.Path})
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
	// Validate path is within allowed project root
	if err := s.validateFSPath(path, r); err != nil {
		JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	includeFiles := r.URL.Query().Get("files") == "1"

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

	isGit := managers.IsGitRepo(path)

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
			result.Entries = append(result.Entries, fsEntry{Name: "..", IsDir: true, Path: parent, IsGitRepo: managers.IsGitRepo(parent)})
		}
	}

	for _, e := range entries {
		if e.Name()[0] == '.' {
			continue
		}
		if !includeFiles && !e.IsDir() {
			continue
		}
		fullPath := filepath.Join(path, e.Name())
		result.Entries = append(result.Entries, fsEntry{
			Name:      e.Name(),
			IsDir:     e.IsDir(),
			Path:      fullPath,
			IsGitRepo: managers.IsGitRepo(fullPath),
		})
	}

	sort.SliceStable(result.Entries, func(i, j int) bool {
		return strings.ToLower(result.Entries[i].Name) < strings.ToLower(result.Entries[j].Name)
	})

	JSONResponse(w, http.StatusOK, result)
}

// handleFSSearch recursively searches for files under a project or task worktree.
// Query params: q (search term), project (project ID), task (task ID — optional, uses worktree).
// Returns up to 10 matching files with paths relative to the search root.
func (s *Server) handleFSSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	projectID := r.URL.Query().Get("project")
	if projectID == "" {
		JSONError(w, http.StatusBadRequest, "project parameter is required")
		return
	}

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	taskID := r.URL.Query().Get("task")

	// Resolve search root from project or task worktree
	var searchRoot string
	var err error

	if taskID != "" {
		// Search within task worktree.
		// Verify the task belongs to this project first.
		if _, lookupErr := s.store.GetProject(projectID); lookupErr != nil {
			JSONError(w, http.StatusNotFound, "project not found")
			return
		}
		task, taskErr := s.store.GetTask(projectID, taskID)
		if taskErr != nil {
			JSONError(w, http.StatusNotFound, "task not found")
			return
		}
		_ = task // task exists and belongs to project
		searchRoot, err = s.executor.WorktreeMgr().WorktreePath(taskID)
		if err != nil {
			JSONError(w, http.StatusNotFound, "failed to resolve worktree path: "+err.Error())
			return
		}
		// Worktree path is derived from server-side config — no need for client-side path validation.
		// Just ensure it's under the workspace directory.
		searchRoot = filepath.Clean(searchRoot)
		cfg, cfgErr := s.configStore.GetConfig()
		if cfgErr == nil && cfg.WorkspaceDir != "" {
			wtDir := cfg.WorkspaceDir
			if wtOverride := os.Getenv("WORKTREES_DIR"); wtOverride != "" {
				wtDir = wtOverride
			}
			cleanWT := filepath.Clean(wtDir)
			if !strings.HasPrefix(searchRoot, cleanWT+string(os.PathSeparator)) && searchRoot != cleanWT {
				JSONError(w, http.StatusForbidden, "worktree path outside allowed workspace")
				return
			}
		}
	} else {
		// Search within project root
		project, lookupErr := s.store.GetProject(projectID)
		if lookupErr != nil {
			JSONError(w, http.StatusNotFound, "project not found")
			return
		}
		searchRoot = project.Path
		// Validate the resolved search root is within the allowed project directory
		searchRoot = filepath.Clean(searchRoot)
		if err := s.validateFSPath(searchRoot, r); err != nil {
			JSONError(w, http.StatusForbidden, err.Error())
			return
		}
	}

	// Check that the search root exists
	if _, statErr := os.Stat(searchRoot); statErr != nil {
		JSONError(w, http.StatusNotFound, "search root does not exist: "+statErr.Error())
		return
	}

	// Recursively walk and collect matching files
	type searchResult struct {
		Path string `json:"path"` // relative to search root
	}
	var results []searchResult

	err = filepath.WalkDir(searchRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		rel, _ := filepath.Rel(searchRoot, path)
		if rel == "." {
			return nil
		}

		// Skip hidden files/dirs (starting with .)
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip ignored directories
		if git.ShouldIgnorePath(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			return nil
		}

		// Match: filename or relative path contains query (case-insensitive)
		if q != "" && !strings.Contains(strings.ToLower(rel), strings.ToLower(q)) {
			return nil
		}

		results = append(results, searchResult{Path: rel})
		if len(results) >= 10 {
			return errStopWalk
		}
		return nil
	})
	_ = err // ignore errStopWalk

	// Sort results for deterministic output
	sort.SliceStable(results, func(i, j int) bool {
		return strings.ToLower(results[i].Path) < strings.ToLower(results[j].Path)
	})

	JSONResponse(w, http.StatusOK, map[string]any{
		"root":    searchRoot,
		"results": results,
	})
}

// errStopWalk is a sentinel error to stop filepath.WalkDir early.
var errStopWalk = fmt.Errorf("stop walk: limit reached")

// validateFSPath checks that the requested path is within an allowed root.
// If a "project" query param is present, it looks up the project and restricts
// access to its root directory. Without a project param, access is unrestricted
// (for backwards compatibility with Dockerfile picker, project creation, etc.).
func (s *Server) validateFSPath(path string, r *http.Request) error {
	projectID := r.URL.Query().Get("project")
	if projectID == "" {
		return nil // no project constraint
	}
	project, err := s.store.GetProject(projectID)
	if err != nil {
		return fmt.Errorf("project not found")
	}
	// Clean both paths to prevent directory traversal
	cleanPath := filepath.Clean(path)
	cleanRoot := filepath.Clean(project.Path)
	// Ensure path is within the project root
	if !strings.HasPrefix(cleanPath, cleanRoot+string(os.PathSeparator)) && cleanPath != cleanRoot {
		return fmt.Errorf("path outside allowed project directory")
	}
	return nil
}

// handleFSInitGit initializes a git repository in the given path.
func (s *Server) handleFSInitGit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		JSONError(w, http.StatusBadRequest, "path is required")
		return
	}
	req.Path = strings.TrimSpace(req.Path)

	info, err := os.Stat(req.Path)
	if err != nil {
		JSONError(w, http.StatusBadRequest, "path does not exist: "+err.Error())
		return
	}
	if !info.IsDir() {
		JSONError(w, http.StatusBadRequest, "path is not a directory")
		return
	}
	if managers.IsGitRepo(req.Path) {
		JSONError(w, http.StatusBadRequest, "path is already a git repository")
		return
	}

	// git init
	cmd := exec.Command("git", "-C", req.Path, "init")
	if out, err := cmd.CombinedOutput(); err != nil {
		JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git init failed: %s", out))
		return
	}

	// Create README.md
	readmePath := filepath.Join(req.Path, "README.md")
	if err := os.WriteFile(readmePath, []byte("# "+managers.Basename(req.Path)+"\n"), 0644); err != nil {
		JSONError(w, http.StatusInternalServerError, fmt.Sprintf("create README.md: %v", err))
		return
	}

	// git add + commit
	cmd = exec.Command("git", "-C", req.Path, "add", "README.md")
	if out, err := cmd.CombinedOutput(); err != nil {
		JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git add failed: %s", out))
		return
	}
	cmd = exec.Command("git", "-C", req.Path, "-c", "user.email=kanban@local", "-c", "user.name=Kanban", "commit", "-m", "Initial commit")
	if out, err := cmd.CombinedOutput(); err != nil {
		JSONError(w, http.StatusInternalServerError, fmt.Sprintf("git commit failed: %s", out))
		return
	}

	JSONResponse(w, http.StatusOK, map[string]string{"status": "ok", "path": req.Path})
}

// handleFSMkdir creates a new directory.
func (s *Server) handleFSMkdir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		JSONError(w, http.StatusBadRequest, "path is required")
		return
	}
	req.Path = strings.TrimSpace(req.Path)

	// Check parent exists
	parent := filepath.Dir(req.Path)
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		JSONError(w, http.StatusBadRequest, "parent directory does not exist")
		return
	}

	// Check target doesn't already exist
	if _, err := os.Stat(req.Path); err == nil {
		JSONError(w, http.StatusBadRequest, "path already exists")
		return
	}

	if err := os.MkdirAll(req.Path, 0755); err != nil {
		JSONError(w, http.StatusInternalServerError, fmt.Sprintf("create directory: %v", err))
		return
	}

	JSONResponse(w, http.StatusOK, map[string]string{"status": "ok", "path": req.Path})
}

// handleThreads lists all threads for a task.
func (s *Server) handleThreads(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	switch r.Method {
	case http.MethodGet:
		threads, err := s.store.GetThreadsByTask(taskID)
		if err != nil {
			JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		JSONResponse(w, http.StatusOK, threads)
	case http.MethodDelete:
		s.handleThreadDelete(w, r, projectID, taskID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleThreadDelete removes a thread and its messages.
func (s *Server) handleThreadDelete(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
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
	// Can't delete the only thread (Main)
	if task.CurrentThreadID == req.ThreadID {
		threads, _ := s.store.GetThreadsByTask(taskID)
		if len(threads) <= 1 {
			JSONError(w, http.StatusBadRequest, "cannot delete the last thread")
			return
		}
	}
	// Delete messages for this thread
	_, err = s.store.Exec(`DELETE FROM task_messages WHERE thread_id = ?`, req.ThreadID)
	if err != nil {
		JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Get the thread's git branch before deleting (needed for branch cleanup)
	deletedThread, _ := s.store.GetThread(req.ThreadID)
	deletedBranch := ""
	if deletedThread != nil {
		deletedBranch = deletedThread.GitBranch
	}
	// Delete the thread
	_, err = s.store.Exec(`DELETE FROM task_threads WHERE id = ?`, req.ThreadID)
	if err != nil {
		JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// If the deleted thread was the active one, switch to another
	if task.CurrentThreadID == req.ThreadID {
		remaining, _ := s.store.GetThreadsByTask(taskID)
		if len(remaining) > 0 {
			_ = s.store.SetActiveThread(remaining[0].ID)
			task.CurrentThreadID = remaining[0].ID
			task.UpdatedAt = time.Now().UTC()
			_ = s.store.UpdateTask(task)
			// Checkout the new active thread's branch and delete orphaned branch
			targetThread, _ := s.store.GetThread(remaining[0].ID)
			s.checkoutThreadBranch(taskID, targetThread)
			// Reset file state to match the new thread's conversation
			lastCommit := s.getLastCommitHashInThread(remaining[0].ID)
			s.resetWorktreeToCommit(taskID, lastCommit)
			s.broadcastTaskDiff(taskID)
			s.deleteThreadBranch(taskID, deletedBranch)
		}
	} else if deletedBranch != "" {
		// Not active but still has a branch — clean it up
		s.deleteThreadBranch(taskID, deletedBranch)
	}
	task, _ = s.store.GetTask(projectID, taskID)
	if task != nil {
		s.appStreamMgr.BroadcastTaskUpdated(projectID, *task)
	}
	JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleThreadSwitch switches the active thread for a task.
func (s *Server) handleThreadSwitch(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
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
	// Stash current worktree state before switching threads.
	// Uses stash (not commit) to avoid polluting the git log.
	if task.Branch != "" {
		oldThreadID := task.CurrentThreadID
		if oldThreadID != "" && oldThreadID != req.ThreadID {
			oldThread, _ := s.store.GetThread(oldThreadID)
			oldBranch := oldThread.GitBranch
			if oldBranch == "" {
				oldBranch = task.Branch
			}
			_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "checkout", oldBranch)
			_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "stash", "push", "-u", "-m", fmt.Sprintf("switch-%s", taskID))
		}
		// Checkout target thread's branch (or task's original branch if none)
		targetBranch := thread.GitBranch
		if targetBranch == "" {
			targetBranch = fmt.Sprintf("task/%s", taskID)
		}
		_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "checkout", targetBranch)
		task.Branch = targetBranch
		// Reset worktree to the target thread's last checkpoint (stash entry).
		// Checkpoints are stored as git stash entries, not branch commits.
		// The stash label includes the branch name: "On <branch>: <description>"
		stashList, _ := s.executor.WorktreeMgr().GitRunInWorktreeOutput(taskID, "stash", "list")
		targetPrefix := "On " + targetBranch + ":"
		foundCheckpoint := false
		for line := range strings.SplitSeq(stashList, "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, targetPrefix) {
				// Found the latest stash for this branch — extract the stash ref
				// Format: "stash@{0}: On thread/jade-honor: Create count_to_3.txt"
				stashRef := strings.Split(line, ":")[0] // "stash@{0}"
				s.executor.WorktreeMgr().ResetWorktreeTo(taskID, strings.TrimSpace(stashRef))
				foundCheckpoint = true
				break
			}
		}
		if !foundCheckpoint {
			// No checkpoint for this thread — reset to base ref (clean initial state).
			// Do NOT use HEAD — branch history may be corrupted by old stash operations.
			baseRef := task.BaseRef
			if baseRef == "" {
				baseRef = "master"
			}
			_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "reset", "--hard", baseRef)
			_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "clean", "-fd")
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
	// Broadcast updated task so frontend has new currentThreadID before diff loads
	s.appStreamMgr.BroadcastTaskUpdated(projectID, *task)
	// Broadcast thread_switch event to WebSocket clients
	s.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeThreadSwitch, Data: map[string]string{"thread_id": req.ThreadID, "task_id": taskID}})
	// Send fresh diff so the Changes panel reflects the new thread's branch
	s.broadcastTaskDiff(taskID)
	JSONResponse(w, http.StatusOK, thread)
}

// handleThreadFork creates a new thread by forking from a checkpoint.
func (s *Server) handleThreadFork(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
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
	newThreadID := managers.GenerateThreadID()
	newBranchName := fmt.Sprintf("thread/%s", newThreadID)
	// Find the file state at the checkpoint: last commit BEFORE the checkpoint message.
	// This is the state before its tool calls ran, so the fork starts from a clean slate.
	checkpointCommit, _ := s.store.GetLastCommitHashBeforeMessage(req.FromMessageID, req.FromThreadID)
	// Determine the branch point for the fork
	branchPoint := checkpointCommit
	if branchPoint == "" {
		branchPoint = task.BaseRef
	}
	// Save current worktree state before forking, so uncommitted changes
	// aren't lost when we switch back to the old thread. Uses stash (not commit)
	// to avoid polluting the git log with internal snapshot commits.
	// The fork branch starts clean from the checkpoint commit — do NOT pop
	// the stash, or old thread's uncommitted files would leak into the fork.
	if task.Branch != "" {
		_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "checkout", task.Branch)
		_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "stash", "push", "-u", "-m", fmt.Sprintf("fork-%s", taskID))
		_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "branch", newBranchName, branchPoint)
		_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "checkout", newBranchName)
		// Update task.Branch to track the currently checked-out branch
		task.Branch = newBranchName
		// Reset worktree to the checkpoint state.
		// branchPoint is a stash hash (from git stash create) or a branch ref.
		// ResetWorktreeTo handles both cases.
		if branchPoint != "" {
			s.executor.WorktreeMgr().ResetWorktreeTo(taskID, branchPoint)
		} else {
			// No checkpoint — reset to base ref (clean initial state).
			baseRef := task.BaseRef
			if baseRef == "" {
				baseRef = "master"
			}
			_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "reset", "--hard", baseRef)
			_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "clean", "-fd")
		}
	}
	// Look up the checkpoint message's created_at for frontend filtering
	var checkpointMsgTime *time.Time
	if ct, err := s.store.GetMessageCreatedAt(req.FromMessageID); err == nil && ct != nil {
		checkpointMsgTime = ct
	}
	// Create new thread
	now := time.Now().UTC()
	newThread := &types.TaskThread{
		ID:                    newThreadID,
		TaskID:                taskID,
		ParentThreadID:        req.FromThreadID,
		Name:                  "",
		IsActive:              true,
		GitBranch:             newBranchName,
		CheckpointMessageID:   req.FromMessageID,
		CheckpointMessageTime: checkpointMsgTime,
		CheckpointCommitHash:  branchPoint,
		CreatedAt:             now,
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
	// Broadcast updated task so frontend has new currentThreadID before diff loads
	s.appStreamMgr.BroadcastTaskUpdated(projectID, *task)
	// Broadcast thread_switch event
	s.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeThreadSwitch, Data: map[string]string{"thread_id": newThreadID, "task_id": taskID}})
	// Send fresh diff so the Changes panel reflects the forked branch
	s.broadcastTaskDiff(taskID)
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

		messages := []types.ChatMessage{
			{Role: "system", Content: "You are a thread name generator. Given a user message, generate a very short, descriptive thread label (max 30 characters, no quotes). Return only the label, nothing else."},
			{Role: "user", Content: trimmed},
		}

		_, name, err := s.executor.ChatCompletion(ctx, task.LlamaServerURL, task.ModelName, messages)
		if err == nil && strings.TrimSpace(name) != "" {
			name = strings.TrimSpace(name)
			if len(name) > 35 {
				name = name[:35]
			}
			log.Printf("[thread-name] task=%s generated: %q", task.ID, name)
			return name
		}
	}

	// Fallback: truncate the text
	if len(trimmed) > 35 {
		return trimmed[:35]
	}
	return trimmed
}

// checkoutThreadBranch switches the worktree to the target thread's git branch.
// Discards any uncommitted changes (does not commit first).
func (s *Server) checkoutThreadBranch(taskID string, targetThread *types.TaskThread) {
	if targetThread == nil || targetThread.GitBranch == "" {
		return
	}
	_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "checkout", targetThread.GitBranch)
}

// deleteThreadBranch removes a thread's git branch from the repo.
func (s *Server) deleteThreadBranch(taskID string, branchName string) {
	if branchName == "" {
		return
	}
	_ = s.executor.WorktreeMgr().GitRunInWorktree(taskID, "branch", "-D", branchName)
}

// resetWorktreeToCommit resets the task's worktree to a specific commit or stash hash.
func (s *Server) resetWorktreeToCommit(taskID, commitHash string) {
	if commitHash == "" {
		return
	}
	_ = s.executor.WorktreeMgr().ResetWorktreeTo(taskID, commitHash)
}

// getLastCommitHashInThread finds the last commit hash in a thread's messages.
func (s *Server) getLastCommitHashInThread(threadID string) string {
	messages, err := s.store.GetTaskMessages("", threadID)
	if err != nil || len(messages) == 0 {
		return ""
	}
	// Walk backward to find the last message with a commit hash
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].GitCommitHash != "" {
			return messages[i].GitCommitHash
		}
	}
	return ""
}

// broadcastTaskDiff sends the current git diff for a task's worktree via WebSocket.
func (s *Server) broadcastTaskDiff(taskID string) {
	task, _ := s.store.GetTaskByTaskID(taskID)
	if task == nil {
		return
	}
	thread, _ := s.store.GetThread(task.CurrentThreadID)
	forkBranch := ""
	if thread != nil {
		forkBranch = thread.GitBranch
	}
	baseRef, _ := s.executor.WorktreeMgr().ResolveDiffBaseline(taskID, task.BaseRef, task.Branch, forkBranch)
	taskDiff, err := s.executor.WorktreeMgr().GetTaskDiff(taskID, baseRef, "")
	if err != nil {
		return
	}
	s.appStreamMgr.SendTaskEvent(task.ProjectID, taskID, types.StreamEvent{Type: types.WSTypeDiffChanged, Data: taskDiff})
}

// cleanupDeletedMessageForks handles orphan fork thread cleanup when a checkpoint
// message is deleted. Deletes orphaned forks, switches to surviving parent if needed,
// checkouts branch, resets worktree, and broadcasts updates.
func (s *Server) cleanupDeletedMessageForks(projectID, taskID string, task *types.Task, threadID, messageID string, msgCreatedAt *time.Time, msgTimeErr error) {
	// Match by message ID first (direct match), then by created_at time (for copied messages
	// in fork threads that have new IDs but preserved timestamps).
	checkpointThreads, _ := s.store.GetThreadsByCheckpointMessageID(messageID)
	if len(checkpointThreads) == 0 && msgTimeErr == nil && msgCreatedAt != nil {
		checkpointThreads, _ = s.store.GetThreadsByCheckpointMessageTime(msgCreatedAt.Format(time.RFC3339))
	}
	if len(checkpointThreads) > 0 {
		activeWasDeleted := false
		// Collect branches to delete before removing threads
		var branchesToDelete []string
		for _, th := range checkpointThreads {
			if th.IsActive {
				activeWasDeleted = true
			}
			if th.GitBranch != "" {
				branchesToDelete = append(branchesToDelete, th.GitBranch)
			}
			_ = s.store.DeleteThread(th.ID)
		}
		if activeWasDeleted {
			// Switch to the parent thread of the deleted active thread.
			// Walk up the parent chain in case parents were also deleted.
			allThreads, _ := s.store.GetThreadsByTask(taskID)
			threadMap := make(map[string]types.TaskThread)
			for _, t := range allThreads {
				threadMap[t.ID] = t
			}
			var targetThread *types.TaskThread
			candidate := checkpointThreads[0].ParentThreadID
			for candidate != "" {
				parent, ok := threadMap[candidate]
				if !ok {
					// Parent was also deleted; walk up further
					candidate = parent.ParentThreadID
					continue
				}
				// Found a surviving parent
				_ = s.store.SetActiveThread(parent.ID)
				task.CurrentThreadID = parent.ID
				task.UpdatedAt = time.Now().UTC()
				_ = s.store.UpdateTask(task)
				targetThread = &parent
				candidate = "" // break
			}
			if candidate != "" {
				// No surviving parent found; fall back to root/Main thread
				for _, t := range allThreads {
					if t.Name == "Main" || t.ParentThreadID == "" {
						_ = s.store.SetActiveThread(t.ID)
						task.CurrentThreadID = t.ID
						task.UpdatedAt = time.Now().UTC()
						_ = s.store.UpdateTask(task)
						targetThread = &t
						break
					}
				}
			}
			// Checkout the new active thread's branch and delete orphaned branches
			s.checkoutThreadBranch(taskID, targetThread)
			for _, branch := range branchesToDelete {
				s.deleteThreadBranch(taskID, branch)
			}
		} else {
			// Non-active fork deleted; just clean up its branch
			for _, branch := range branchesToDelete {
				s.deleteThreadBranch(taskID, branch)
			}
		}
		// Reset file state to match the (possibly new) active thread's conversation
		lastCommit := s.getLastCommitHashInThread(task.CurrentThreadID)
		s.resetWorktreeToCommit(taskID, lastCommit)
		s.broadcastTaskDiff(taskID)
		// Broadcast updated task so frontend has new currentThreadID before diff loads
		s.appStreamMgr.BroadcastTaskUpdated(projectID, *task)
		s.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeThreadSwitch, Data: map[string]string{"thread_id": task.CurrentThreadID, "task_id": taskID}})
	} else {
		// No fork threads affected, but messages were deleted.
		// Reset file state to match remaining conversation in active thread.
		lastCommit := s.getLastCommitHashInThread(threadID)
		s.resetWorktreeToCommit(taskID, lastCommit)
		s.broadcastTaskDiff(taskID)
	}
}
