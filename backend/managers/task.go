package managers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"kanban/backend/config"
	"kanban/backend/exec"
	"kanban/backend/git"
	"kanban/backend/httputil"
	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/term"
	"kanban/backend/types"
)

// TaskHandler handles task CRUD operations.
type TaskHandler struct {
	store          *store.Store
	boardStreamMgr *stream.AppStreamManager
	configStore    *config.ConfigStore
	terminalMgr    *term.TerminalManager
	executor       *exec.TaskExecutor
	worktreeMgr    *git.WorktreeManager
	commitQueue    *exec.CommitQueue
	sandboxMgr     taskSandboxOps
}

// taskSandboxOps is the subset of sandbox.SandboxManager methods used for port refresh.
type taskSandboxOps interface {
	GetContainerPorts(taskID string) ([]types.AssignedPort, error)
}

// hasField checks if a key exists in a map[string]any.
func hasField(m map[string]any, key string) bool {
	_, ok := m[key]
	return ok
}

// NewTaskHandler creates a new TaskHandler.
func NewTaskHandler(store *store.Store, boardStreamMgr *stream.AppStreamManager, configStore *config.ConfigStore, terminalMgr *term.TerminalManager, executor *exec.TaskExecutor, worktreeMgr *git.WorktreeManager, commitQueue *exec.CommitQueue, sandboxMgr taskSandboxOps) *TaskHandler {
	return &TaskHandler{store: store, boardStreamMgr: boardStreamMgr, configStore: configStore, terminalMgr: terminalMgr, executor: executor, worktreeMgr: worktreeMgr, commitQueue: commitQueue, sandboxMgr: sandboxMgr}
}

// refreshTaskPorts updates AssignedPorts from the live sandbox container.
func (h *TaskHandler) refreshTaskPorts(task *types.Task) {
	if h.sandboxMgr == nil {
		return
	}
	ports, err := h.sandboxMgr.GetContainerPorts(task.ID)
	if err == nil && ports != nil {
		task.AssignedPorts = ports
	}
}

// EnrichWorktreeExists sets the WorktreeExists field on tasks based on filesystem state.
// It uses the workspace_dir from config to build the worktree path.
func EnrichWorktreeExists(tasks []types.Task, workspaceDir string) {
	for i := range tasks {
		path := filepath.Join(workspaceDir, tasks[i].ID)
		_, err := os.Stat(path)
		tasks[i].WorktreeExists = err == nil
	}
}

// enrichTaskWorktree sets the WorktreeExists field on a single task.
func enrichTaskWorktree(task *types.Task, workspaceDir string) {
	path := filepath.Join(workspaceDir, task.ID)
	_, err := os.Stat(path)
	task.WorktreeExists = err == nil
}

// HandleList lists or creates tasks for a project.
func (h *TaskHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	switch r.Method {
	case http.MethodGet:
		tasks, err := h.store.GetTasksByProject(projectID)
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if h.configStore != nil {
			if cfg, err := h.configStore.GetConfig(); err == nil {
				EnrichWorktreeExists(tasks, cfg.WorkspaceDir)
			}
		}
		// Refresh assigned ports from live sandbox containers
		for i := range tasks {
			h.refreshTaskPorts(&tasks[i])
		}
		httputil.JSONResponse(w, http.StatusOK, tasks)
	case http.MethodPost:
		body, _ := io.ReadAll(r.Body)
		var task types.Task
		var attachments string
		// Parse attachments separately (not a field on Task type)
		var rawBody map[string]any
		if err := json.Unmarshal(body, &rawBody); err == nil {
			if att, ok := rawBody["attachments"]; ok {
				attachments = att.(string)
			}
		}
		if err := json.Unmarshal(body, &task); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		task.ID = types.GenerateTaskID()
		task.ProjectID = projectID
		if task.Status == "" {
			task.Status = "backlog"
		}
		now := time.Now().UTC()
		task.CreatedAt = now
		task.UpdatedAt = now
		if task.Title == "" && task.Prompt != "" {
			task.Title = autoGenerateTitle(task.Prompt)
		}
		if h.configStore != nil {
			if cfg, err := h.configStore.GetConfig(); err == nil {
				// Only apply defaults for fields NOT sent by the client
				if cfg.DefaultAutoCommit != nil && !hasField(rawBody, "auto_commit") {
					task.AutoCommit = *cfg.DefaultAutoCommit
				}
				if cfg.DefaultAutoMerge != nil && !hasField(rawBody, "auto_merge") {
					task.AutoMerge = *cfg.DefaultAutoMerge
				}
				if cfg.DefaultSubtasksAutoMerge != nil && !hasField(rawBody, "subtasks_auto_merge") {
					task.SubtasksAutoMerge = *cfg.DefaultSubtasksAutoMerge
				}
				if cfg.DefaultPlanMode != nil && !hasField(rawBody, "plan_mode") {
					task.PlanMode = *cfg.DefaultPlanMode
				}
				if cfg.DefaultMaxFailedTurns != nil && !hasField(rawBody, "max_consecutive_failed_turns") {
					task.MaxConsecutiveFailedTurns = *cfg.DefaultMaxFailedTurns
				}
			}
		}
		// If this is a child task, inherit auto_commit/auto_merge from parent
		if task.ParentTaskID != "" {
			parent, err := h.store.GetTask(projectID, task.ParentTaskID)
			if err == nil {
				if !hasField(rawBody, "auto_commit") {
					task.AutoCommit = parent.AutoCommit
				}
				if !hasField(rawBody, "auto_merge") {
					task.AutoMerge = parent.SubtasksAutoMerge
				}
			}
		}
		// Validate parent_task_id if provided
		if task.ParentTaskID != "" {
			parent, err := h.store.GetTask(projectID, task.ParentTaskID)
			if err != nil {
				httputil.JSONError(w, http.StatusBadRequest, "parent task not found")
				return
			}
			// Prevent self-reference (won't happen for new tasks, but guard anyway)
			if task.ParentTaskID == task.ID {
				httputil.JSONError(w, http.StatusBadRequest, "task cannot be its own parent")
				return
			}
			// Only tasks in backlog can accept subtasks
			if parent.Status != "backlog" {
				httputil.JSONError(w, http.StatusBadRequest, "parent task must be in backlog to accept subtasks")
				return
			}
		}
		// If the first line is short (≤5 words), use it as the title directly
		if isShortTitle(task.Prompt) {
			task.TitleGenerating = false
		} else {
			task.TitleGenerating = true
		}
		if err := h.store.CreateTask(&task); err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Create default thread for the task
		defaultThreadID := types.GenerateThreadID()
		if err := h.store.CreateThread(&types.TaskThread{
			ID:        defaultThreadID,
			TaskID:    task.ID,
			Name:      "Main",
			IsActive:  true,
			GitBranch: task.Branch,
			CreatedAt: now,
		}); err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("create default thread: %v", err))
			return
		}
		task.CurrentThreadID = defaultThreadID
		if err := h.store.UpdateTask(&task); err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Store the task prompt as the first user message in the conversation
		if task.Prompt != "" {
			tm := &types.TaskMessage{
				ID:        uuid.New().String(),
				TaskID:    task.ID,
				ThreadID:  defaultThreadID,
				Role:      "user",
				Content:   task.Prompt,
				CreatedAt: now,
			}
			if attachments != "" {
				tm.Attachments = attachments
			}
			_ = h.store.CreateTaskMessage(tm)
		}
		// Spawn async goroutine to generate a better title via LLM
		// (skip if first line was already a short, good-enough title)
		if task.TitleGenerating {
			h.startTitleGeneration(projectID, task.ID, task.Prompt)
		}
		h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
		httputil.JSONResponse(w, http.StatusCreated, task)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleByID handles GET/PUT/DELETE for a single task.
func (h *TaskHandler) HandleByID(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	switch r.Method {
	case http.MethodGet:
		task, err := h.store.GetTask(projectID, taskID)
		if err != nil {
			httputil.JSONError(w, http.StatusNotFound, "task not found")
			return
		}
		if h.configStore != nil {
			if cfg, err := h.configStore.GetConfig(); err == nil {
				enrichTaskWorktree(task, cfg.WorkspaceDir)
			}
		}
		h.refreshTaskPorts(task)
		httputil.JSONResponse(w, http.StatusOK, task)
	case http.MethodPut:
		task, err := h.store.GetTask(projectID, taskID)
		if err != nil {
			httputil.JSONError(w, http.StatusNotFound, "task not found")
			return
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, task)
		task.ID = taskID
		task.ProjectID = projectID
		task.UpdatedAt = time.Now()
		// Auto-commit is a prerequisite for auto-merge
		if task.AutoMerge {
			task.AutoCommit = true
		}
		if err := h.store.UpdateTask(task); err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if h.configStore != nil {
			if cfg, err := h.configStore.GetConfig(); err == nil {
				enrichTaskWorktree(task, cfg.WorkspaceDir)
			}
		}
		h.refreshTaskPorts(task)
		h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
		httputil.JSONResponse(w, http.StatusOK, task)
	case http.MethodDelete:
		if _, err := h.store.GetTask(projectID, taskID); err != nil {
			httputil.JSONError(w, http.StatusNotFound, "task not found")
			return
		}
		h.broadcastTaskDeleted(projectID, taskID)
		h.cleanupTask(taskID)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleClearDone deletes all tasks with status 'done' for a project.
func (h *TaskHandler) HandleClearDone(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tasks, err := h.store.GetTasksByProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to get tasks")
		return
	}

	var deleted int
	for _, task := range tasks {
		if task.Status != "done" {
			continue
		}
		taskID := task.ID
		h.broadcastTaskDeleted(projectID, taskID)
		h.cleanupTask(taskID)
		deleted++
	}

	httputil.JSONResponse(w, http.StatusOK, map[string]any{"deleted": deleted})
}

// HandleMove moves a task to a different column.
func (h *TaskHandler) HandleMove(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req types.MoveTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	task, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task not found")
		return
	}
	// Block subtasks from moving to Backlog when an ancestor is In Progress.
	if req.Status == "backlog" && task.ParentTaskID != "" {
		if err := h.checkAncestorInProgress(task); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	originalStatus := task.Status // save for revert on auto-start failure
	task.Status = req.Status
	task.UpdatedAt = time.Now()

	// Stop the executor if the task is moved out of In Progress.
	// This ensures the goroutine exits and HasRunningTasks() reflects the new state.
	if task.Status == "in_progress" && req.Status != "in_progress" && h.executor != nil {
		_ = h.executor.StopTask(taskID)
	}
	if req.Status == "done" {
		task.ExecutionState = "completed"
		if h.executor != nil {
			h.executor.StopSandbox(taskID)
		}
	} else if task.ExecutionState == "completed" {
		// Task moved away from Done — reset status so it can run again
		task.ExecutionState = "idle"
	}
	if req.Status == "in_progress" && task.ExecutionState != "running" {
		// If this task is a subtask, use subtask-aware start (moves parent only, not siblings)
		if task.ParentTaskID != "" {
			if err := h.StartSubtaskWithParent(projectID, taskID); err != nil {
				log.Printf("[move] task=%s subtask start failed: %v", taskID, err)
				task.ExecutionState = "idle"
				task.Status = originalStatus
				task.Output = err.Error()
			}
		} else {
			// Check if task has subtasks — if so, move all incomplete children and start at deepest level
			children, _ := h.store.GetChildren(taskID)
			if len(children) > 0 {
				selectedID := h.SelectSubtaskToStart(task, children)
				if selectedID != "" {
					log.Printf("[move] task=%s has subtasks, starting subtask %s via LLM selection", taskID, selectedID)
					if err := h.StartSubtaskWithParent(projectID, selectedID); err != nil {
						log.Printf("[move] task=%s subtask start failed: %v", taskID, err)
						// Keep parent in in_progress (user's explicit action); record error for visibility
						task.ExecutionState = "idle"
						task.Output = err.Error()
					}
				}
			} else {
				if err := h.executor.StartTask(projectID, taskID); err != nil {
					log.Printf("[move] task=%s auto-start failed: %v", taskID, err)
					task.ExecutionState = "idle"
					task.Status = originalStatus
					task.Output = err.Error()
				}
			}
		}
	}
	if err := h.store.UpdateTask(task); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Move all descendants to the same column (recursive, leaf-first)
	// Use task.Status (actual final status) in case auto-start failed and reverted
	h.moveAllDescendants(projectID, taskID, task.Status)

	// If a subtask moved to review/done, auto-start the next sibling
	if task.ParentTaskID != "" && (req.Status == "review" || req.Status == "done") {
		_ = h.autoStartNextSibling(projectID, taskID, task.ParentTaskID, req.Status)
	}

	h.refreshTaskPorts(task)
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// autoStartNextSibling delegates to the executor's sibling auto-start logic.
func (h *TaskHandler) autoStartNextSibling(projectID, finishedTaskID, parentTaskID, finishedStatus string) error {
	return h.executor.AutoStartNextSibling(projectID, finishedTaskID, parentTaskID, finishedStatus)
}

// moveAllDescendantsToInProgress recursively moves all descendant tasks (children, grandchildren, etc.)
// to in_progress/idle. Only tasks in backlog or in_progress+idle are moved.
func (h *TaskHandler) moveAllDescendantsToInProgress(taskID string) {
	descendants, err := h.store.GetAllDescendants(taskID)
	if err != nil {
		log.Printf("[move-descendants] failed to get descendants of %s: %v", taskID, err)
		return
	}
	for i := range descendants {
		if descendants[i].Status == "backlog" || (descendants[i].Status == "in_progress" && descendants[i].ExecutionState == "idle") {
			descendants[i].Status = "in_progress"
			descendants[i].ExecutionState = "idle"
			descendants[i].UpdatedAt = time.Now().UTC()
			if err := h.store.UpdateTask(&descendants[i]); err != nil {
				log.Printf("[move-descendants] failed to update descendant %s: %v", descendants[i].ID, err)
			} else {
				h.boardStreamMgr.BroadcastTaskUpdated(descendants[i].ProjectID, &descendants[i])
			}
		}
	}
}

// moveAllDescendants recursively moves all descendant tasks (children, grandchildren, etc.)
// to the target status. Skips descendants that are already running (e.g., just auto-started).
// Processes in reverse order (leaves first) to ensure child tasks are moved before their parents.
func (h *TaskHandler) moveAllDescendants(projectID, taskID, targetStatus string) {
	descendants, err := h.store.GetAllDescendants(taskID)
	if err != nil {
		log.Printf("[move-descendants] failed to get descendants of %s: %v", taskID, err)
		return
	}
	// Process in reverse order (leaves first) so children are moved before their parents
	for i := len(descendants) - 1; i >= 0; i-- {
		d := &descendants[i]
		// Skip already-running descendants (e.g., just auto-started by the move handler)
		if d.ExecutionState == "running" {
			continue
		}
		// Stop the executor if the descendant was in In Progress and is being moved out
		if d.Status == "in_progress" && targetStatus != "in_progress" && h.executor != nil {
			_ = h.executor.StopTask(d.ID)
		}
		d.Status = targetStatus
		d.UpdatedAt = time.Now().UTC()
		// Set execution_state based on target column
		switch targetStatus {
		case "done":
			d.ExecutionState = "completed"
			if h.executor != nil {
				h.executor.StopSandbox(d.ID)
			}
		case "backlog", "in_progress", "review":
			if d.ExecutionState == "completed" {
				// Moved away from done — reset
				d.ExecutionState = "idle"
			}
			// Keep existing state (failed, stopped, awaiting_review, idle) if not overridden above
		}
		if err := h.store.UpdateTask(d); err != nil {
			log.Printf("[move-descendants] failed to update descendant %s: %v", d.ID, err)
		} else {
			h.boardStreamMgr.BroadcastTaskUpdated(projectID, d)
		}
	}
}

// checkAncestorInProgress walks up the ancestor chain and returns an error
// if any ancestor (parent, grandparent, etc.) has status "in_progress".
func (h *TaskHandler) checkAncestorInProgress(task *types.Task) error {
	visited := make(map[string]struct{})
	currentID := task.ParentTaskID
	for currentID != "" {
		if _, ok := visited[currentID]; ok {
			break // avoid infinite loop on circular references
		}
		visited[currentID] = struct{}{}
		ancestor, err := h.store.GetTaskByTaskID(currentID)
		if err != nil {
			break // ancestor not found, stop walking
		}
		if ancestor.Status == "in_progress" {
			return fmt.Errorf("cannot move to Backlog: parent task is In Progress")
		}
		currentID = ancestor.ParentTaskID
	}
	return nil
}

// generateCommitMessage builds a commit message from task title, summary, and model.
// If task.ChangeSummary is empty, generates one via LLM from conversation + diff.
func (h *TaskHandler) generateCommitMessage(taskID string, task *types.Task) string {
	return h.executor.GenerateCommitMessageForTask(taskID, task)
}

// resolveGitAuthor returns the effective git author name/email.
// Priority: env var override > project git config > defaults.
func resolveGitAuthor(projectID string, s *store.Store) (string, string) {
	name := git.GitAuthorName
	email := git.GitAuthorEmail

	// If env vars are set, they take priority
	nameSet := name != ""
	emailSet := email != ""

	// Try to get values from project's git config
	if proj, err := s.GetProject(projectID); err == nil && proj.Path != "" {
		if !nameSet {
			if out, err := osexec.Command("git", "-C", proj.Path, "config", "user.name").CombinedOutput(); err == nil {
				name = strings.TrimSpace(string(out))
			}
		}
		if !emailSet {
			if out, err := osexec.Command("git", "-C", proj.Path, "config", "user.email").CombinedOutput(); err == nil {
				email = strings.TrimSpace(string(out))
			}
		}
	}

	// Fallback defaults if still empty
	if name == "" {
		name = "Kanban"
	}
	if email == "" {
		email = "kanban@local"
	}

	return name, email
}

// HandleCommitPreview generates and returns the commit message that would be used
// for a commit-and-done operation, without performing the commit.
func (h *TaskHandler) HandleCommitPreview(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	task, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task not found")
		return
	}
	if h.worktreeMgr == nil {
		httputil.JSONError(w, http.StatusInternalServerError, "worktree manager not available")
		return
	}

	commitMsg := h.generateCommitMessage(taskID, task)

	// Resolve effective git author: env var override, then project git config, then defaults
	authorName, authorEmail := resolveGitAuthor(projectID, h.store)

	// Resolve merge target branch
	mergeTarget := h.resolveMergeTarget(projectID, task)

	httputil.JSONResponse(w, http.StatusOK, map[string]string{
		"commit_message": commitMsg,
		"author_name":    authorName,
		"author_email":   authorEmail,
		"merge_target":   mergeTarget,
	})
}

// HandleCommitPreviewStream streams the commit message generation via SSE.
// Events: "metadata" (author/merge info), "prompt_progress", "token", "done".
func (h *TaskHandler) HandleCommitPreviewStream(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	task, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: error\ndata: {\"error\":\"task not found\"}\n\n")
		return
	}

	// Send metadata immediately (author + merge target)
	authorName, authorEmail := resolveGitAuthor(projectID, h.store)
	mergeTarget := h.resolveMergeTarget(projectID, task)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	metadata, _ := json.Marshal(map[string]string{
		"author_name":  authorName,
		"author_email": authorEmail,
		"merge_target": mergeTarget,
	})
	fmt.Fprintf(w, "event: metadata\ndata: %s\n\n", string(metadata))
	w.(http.Flusher).Flush()

	// Stream the commit message generation
	flusher, ok := w.(exec.SSEFlusher)
	if !ok {
		fmt.Fprintf(w, "event: error\ndata: {\"error\":\"streaming not supported\"}\n\n")
		return
	}
	h.executor.StreamCommitMessage(flusher, taskID, task)
}

// resolveMergeTarget determines the branch that changes will be merged into.
// For child tasks, the target is the parent's branch.
// For root tasks, the target is the project's origin branch (or current HEAD).
func (h *TaskHandler) resolveMergeTarget(projectID string, task *types.Task) string {
	if task.ParentTaskID != "" {
		parent, err := h.store.GetTaskByTaskID(task.ParentTaskID)
		if err == nil && parent.Branch != "" {
			return parent.Branch
		}
	}
	project, err := h.store.GetProject(projectID)
	if err != nil || project.OriginBranch == "" {
		return ""
	}
	return project.OriginBranch
}

// HandleCommitAndDone commits the worktree changes, merges into the origin branch, and moves the task to Done.
// Operations are serialized per project to prevent git race conditions.
func (h *TaskHandler) HandleCommitAndDone(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	task, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task not found")
		return
	}
	if h.worktreeMgr == nil {
		httputil.JSONError(w, http.StatusInternalServerError, "worktree manager not available")
		return
	}

	// Read optional commit_message from request body
	var body struct {
		CommitMessage string `json:"commit_message"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1024*1024)).Decode(&body)

	// Serialize commit-and-done per project to prevent git race conditions
	h.boardStreamMgr.BroadcastCommitStatus(projectID, taskID, "queued")
	release := h.commitQueue.Acquire(projectID)
	defer release()
	h.boardStreamMgr.BroadcastCommitStatus(projectID, taskID, "processing")

	// Try to commit worktree changes (skip if worktree doesn't exist)
	_, wtErr := h.worktreeMgr.GitRunInWorktreeOutput(taskID, "diff", "--stat")
	worktreeExists := wtErr == nil
	if worktreeExists {
		commitMsg := body.CommitMessage
		if commitMsg == "" {
			commitMsg = h.generateCommitMessage(taskID, task)
		}
		_ = h.worktreeMgr.GitRunInWorktree(taskID, append(git.GitAuthorFlags(), "commit", "-m", commitMsg, "--allow-empty")...)
	}

	// Capture branch tip before merge (for post-commit diff comparison)
	if task.Branch != "" {
		project, pErr := h.store.GetProject(projectID)
		if pErr == nil {
			c := osexec.Command("git", "-C", project.Path, "rev-parse", task.Branch)
			if out, err := c.CombinedOutput(); err == nil {
				task.BranchTip = strings.TrimSpace(string(out))
			}
		}
	}

	// Merge task branch
	if mergeErr := h.executor.MergeBranch(projectID, taskID, task); mergeErr != nil {
		log.Printf("[commit-and-done] task=%s merge failed: %v", taskID, mergeErr)
	}

	// Move task to Done
	task.Status = "done"
	task.ExecutionState = "completed"
	task.UpdatedAt = time.Now()
	if err := h.store.UpdateTask(task); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Stop sandbox container
	if h.executor != nil {
		h.executor.StopSandbox(taskID)
	}

	// If a subtask was marked done, auto-start the next sibling
	if task.ParentTaskID != "" {
		_ = h.autoStartNextSibling(projectID, taskID, task.ParentTaskID, "done")
	}

	h.refreshTaskPorts(task)
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// HandleCompleteMerge completes a merge that is stuck in merge_conflict state.
// The user (or LLM) has resolved the conflicted files; this endpoint stages,
// commits, continues the merge, and then attempts the final merge.
func (h *TaskHandler) HandleCompleteMerge(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	task, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task not found")
		return
	}
	if task.ExecutionState != "merge_conflict" {
		httputil.JSONError(w, http.StatusBadRequest, fmt.Sprintf("task is not in merge_conflict state (current: %s)", task.ExecutionState))
		return
	}
	if h.worktreeMgr == nil {
		httputil.JSONError(w, http.StatusInternalServerError, "worktree manager not available")
		return
	}

	// Serialize per project
	h.boardStreamMgr.BroadcastCommitStatus(projectID, taskID, "queued")
	release := h.commitQueue.Acquire(projectID)
	defer release()
	h.boardStreamMgr.BroadcastCommitStatus(projectID, taskID, "processing")

	// Get task's worktree path
	worktreePath, err := h.worktreeMgr.WorktreePath(taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to get worktree path")
		return
	}
	if _, err := os.Stat(worktreePath); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "task worktree not found")
		return
	}

	// Stage all resolved files
	if err := h.worktreeMgr.GitRunInDir(worktreePath, append(git.GitAuthorFlags(), "add", "-A")...); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to stage files: %v", err))
		return
	}

	// Commit the resolution
	modelName := task.ModelName
	if modelName == "" {
		modelName = "unknown"
	}
	commitMsg := fmt.Sprintf("%s\n\nMerge conflict resolution\n\nCo-authored-by: %s",
		task.Title, modelName)
	if err := h.worktreeMgr.GitRunInDir(worktreePath, append(git.GitAuthorFlags(), "commit", "-m", commitMsg)...); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to commit resolution: %v", err))
		return
	}

	// Continue the merge
	if err := h.worktreeMgr.GitRunInDir(worktreePath, "merge", "--continue"); err != nil {
		// Merge continue failed — keep merge_conflict state, log error
		log.Printf("[complete-merge] task=%s merge --continue failed: %v", taskID, err)
		h.boardStreamMgr.BroadcastCommitStatus(projectID, taskID, "failed")
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("merge continue failed: %v", err))
		return
	}

	// Sync merge succeeded — reload task and attempt final merge
	task, _ = h.store.GetTask(projectID, taskID)
	task.ExecutionState = "idle"
	_ = h.store.UpdateTaskExecutionState(taskID, projectID, "idle")

	if h.executor != nil {
		if mergeErr := h.executor.MergeBranch(projectID, taskID, task); mergeErr != nil {
			log.Printf("[complete-merge] task=%s final merge failed: %v", taskID, mergeErr)
			// Don't fail the whole operation — the sync was successful
		}
	}

	// Move task to Done
	task, _ = h.store.GetTask(projectID, taskID)
	task.Status = "done"
	task.ExecutionState = "completed"
	task.UpdatedAt = time.Now()
	if err := h.store.UpdateTask(task); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Stop sandbox container
	if h.executor != nil {
		h.executor.StopSandbox(taskID)
	}

	// If a subtask was marked done, auto-start the next sibling
	if task.ParentTaskID != "" {
		_ = h.autoStartNextSibling(projectID, taskID, task.ParentTaskID, "done")
	}

	h.refreshTaskPorts(task)
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	h.boardStreamMgr.BroadcastCommitStatus(projectID, taskID, "completed")
	httputil.JSONResponse(w, http.StatusOK, task)
}

// broadcastTaskDeleted sends a task_deleted event for the given task and all its descendants.
func (h *TaskHandler) broadcastTaskDeleted(projectID, taskID string) {
	if h.boardStreamMgr == nil {
		return
	}
	// Collect all descendants first
	descendants, _ := h.store.GetAllDescendants(taskID)
	for _, d := range descendants {
		h.boardStreamMgr.BroadcastTaskDeleted(projectID, d.ID)
	}
	h.boardStreamMgr.BroadcastTaskDeleted(projectID, taskID)
}

// cleanupTask performs all cleanup steps for a deleted task,
// including recursively cleaning up all descendant subtasks.
func (h *TaskHandler) cleanupTask(taskID string) {
	// Collect all descendants first (before any deletions)
	descendants, _ := h.store.GetAllDescendants(taskID)
	// Clean up descendants deep-first (reverse order ensures children before parents)
	for i := len(descendants) - 1; i >= 0; i-- {
		h.cleanupSingleTask(descendants[i].ID)
	}
	// Clean up the parent task last
	h.cleanupSingleTask(taskID)
}

// cleanupSingleTask performs cleanup for a single task (no recursion).
func (h *TaskHandler) cleanupSingleTask(taskID string) {
	if h.executor != nil {
		_ = h.executor.StopTask(taskID)
		h.executor.StopSandbox(taskID)
	}
	task, taskErr := h.store.GetTaskByTaskID(taskID)
	if h.worktreeMgr != nil && taskErr == nil {
		if task.Branch != "" {
			h.boardStreamMgr.UnwatchWorktree(taskID)
			if err := h.worktreeMgr.RemoveWorktree(task.ProjectID, taskID, task.Branch); err != nil {
				log.Printf("task delete: worktree cleanup failed for %s: %v", taskID, err)
			}
		}
		// Clean up fork thread branches (thread/<threadID>) before DB CASCADE deletes thread rows.
		threads, _ := h.store.GetThreadsByTask(taskID)
		for _, th := range threads {
			if th.GitBranch != "" {
				h.worktreeMgr.GitRunInProject(task.ProjectID, "branch", "-D", th.GitBranch)
			}
		}
	}
	h.terminalMgr.RemoveSession(taskID)
	// Delete from database - find project first
	var projectID string
	if taskErr == nil {
		projectID = task.ProjectID
		_ = h.store.DeleteTask(task.ProjectID, taskID)
	} else {
		task, taskErr = h.store.GetTaskByTaskID(taskID)
		if taskErr == nil {
			projectID = task.ProjectID
			_ = h.store.DeleteTask(task.ProjectID, taskID)
		}
	}
	// Clean up stash entries for all branches associated with this task.
	// A task may have stashes on its original branch (task/<taskID>), its current
	// branch (which may be a fork), and thread branches (thread/<threadID>).
	// Runs after DB delete so it works even when the initial task lookup failed.
	if h.worktreeMgr != nil && projectID != "" {
		_ = h.worktreeMgr.DropStashEntriesForTask(projectID, taskID)
		// Also clean branch-labeled stashes (non-kanban stashes from agent operations).
		var branchesToClean []string
		originalBranch := fmt.Sprintf("task/%s", taskID)
		branchesToClean = append(branchesToClean, originalBranch)
		if taskErr == nil && task.Branch != "" && task.Branch != originalBranch {
			branchesToClean = append(branchesToClean, task.Branch)
		}
		for _, branch := range branchesToClean {
			_ = h.worktreeMgr.DropStashEntriesForBranch(projectID, branch)
		}
	}
	// Prune any orphaned git branches (task/*, thread/*) that no longer have
	// matching entries in the database after the task and its CASCADE-deleted
	// threads/messages are removed.
	if h.worktreeMgr != nil && projectID != "" {
		h.worktreeMgr.PruneOrphanedBranches(projectID)
	}
}

// startTitleGeneration spawns a goroutine that uses the LLM to generate
// a concise title for the task. On success, it updates the task title.
// On failure or timeout, it keeps the existing (first-line) title.
func (h *TaskHandler) startTitleGeneration(projectID, taskID, prompt string) {
	go func() {
		ctx := context.Background()

		// Get LLM config from the task itself
		task, err := h.store.GetTask(projectID, taskID)
		if err != nil || task.LlamaServerURL == "" || task.ModelName == "" {
			log.Printf("[title-gen] task=%s skipping: no LLM config on task (%v)", taskID, err)
			h.finishTitleGeneration(projectID, taskID, true)
			return
		}

		messages := []types.ChatMessage{
			{Role: "system", Content: "You are a title generator. Given a task description, generate a short, descriptive title (max 50 characters). Return only the title, nothing else. No quotes, no explanation."},
			{Role: "user", Content: prompt},
		}

		usage, title, err := h.executor.ChatCompletion(ctx, task.LlamaServerURL, task.ModelName, messages, nil)
		if err != nil {
			log.Printf("[title-gen] task=%s LLM call failed: %v", taskID, err)
			h.finishTitleGeneration(projectID, taskID, true)
			return
		}

		if title == "" {
			log.Printf("[title-gen] task=%s empty title from LLM", taskID)
			h.finishTitleGeneration(projectID, taskID, true)
			return
		}

		// Store title generation usage as an invisible message (role='title')
		// so it appears in project stats but not in the chat UI.
		if usage != nil {
			providerName := h.configStore.GetProviderNameByURL(task.LlamaServerURL)
			if err := h.store.CreateTaskMessage(&types.TaskMessage{
				ID:              uuid.New().String(),
				TaskID:          taskID,
				ThreadID:        task.CurrentThreadID,
				Role:            "title",
				Content:         title,
				ModelName:       task.ModelName,
				ProviderName:    providerName,
				PromptTokens:    usage.PromptTokens,
				PredictedTokens: usage.CompletionTokens,
				CacheTokens:     usage.PromptTokensDetails.CachedTokens,
				PromptMs:        int(usage.PromptMs),
				PredictedMs:     int(usage.PredictedMs),
				CreatedAt:       time.Now().UTC(),
			}); err != nil {
				log.Printf("[title-gen] task=%s failed to store usage message: %v", taskID, err)
			}
		}

		// Update the task with the generated title
		task.Title = title
		task.TitleGenerating = false
		task.UpdatedAt = time.Now().UTC()
		if err := h.store.UpdateTask(task); err != nil {
			log.Printf("[title-gen] task=%s update failed: %v", taskID, err)
			return
		}

		log.Printf("[title-gen] task=%s title updated to: %q", taskID, title)
		h.boardStreamMgr.BroadcastTaskUpdated(projectID, *task)
	}()
}

// finishTitleGeneration sets title_generating to false without changing the title.
func (h *TaskHandler) finishTitleGeneration(projectID, taskID string, broadcast bool) {
	task, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		return
	}
	if !task.TitleGenerating {
		return // already finished
	}
	task.TitleGenerating = false
	task.UpdatedAt = time.Now().UTC()
	if err := h.store.UpdateTask(task); err != nil {
		log.Printf("[title-gen] task=%s finish update failed: %v", taskID, err)
		return
	}
	if broadcast {
		h.boardStreamMgr.BroadcastTaskUpdated(projectID, *task)
	}
}

// HandleStartWithSubtasks is the HTTP handler for starting a task.
// If the task has incomplete subtasks, it uses the LLM to pick one and starts it.
// If the task has no subtasks, it starts normally.
func (h *TaskHandler) HandleStartWithSubtasks(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	task, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task not found")
		return
	}

	// Check if task has children
	children, err := h.store.GetChildren(taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("get children: %v", err))
		return
	}

	if len(children) > 0 {
		// Move all incomplete descendants (children, grandchildren, etc.) to in_progress.
		// The parent stays in its current column (e.g., backlog) while subtasks run.
		h.moveAllDescendantsToInProgress(taskID)
		// Task has subtasks — use LLM to pick the best one
		selectedID := h.SelectSubtaskToStart(task, children)
		if selectedID == "" {
			// All subtasks are done — start the parent's own execution
			if err := h.executor.StartTask(projectID, taskID); err != nil {
				log.Printf("[start-parent] task=%s all subtasks done, start failed: %v", taskID, err)
				httputil.JSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
			task, _ = h.store.GetTask(projectID, taskID)
			h.refreshTaskPorts(task)
			h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
			httputil.JSONResponse(w, http.StatusOK, task)
			return
		}
		log.Printf("[start-parent] task=%s selected subtask %s to start", taskID, selectedID)
		if err := h.StartSubtaskWithParent(projectID, selectedID); err != nil {
			log.Printf("[start-parent] task=%s error: %v", taskID, err)
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		startedTask, _ := h.store.GetTask(projectID, selectedID)
		if startedTask != nil {
			h.refreshTaskPorts(startedTask)
			h.boardStreamMgr.BroadcastTaskUpdated(projectID, startedTask)
		}
		httputil.JSONResponse(w, http.StatusOK, startedTask)
		return
	}

	// No subtasks — start normally
	if err := h.executor.StartTask(projectID, taskID); err != nil {
		log.Printf("[start] task=%s error: %v", taskID, err)
		if strings.Contains(err.Error(), "already running") {
			httputil.JSONError(w, http.StatusConflict, err.Error())
		} else {
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	log.Printf("[start] task=%s started", taskID)
	task, _ = h.store.GetTask(projectID, taskID)
	h.refreshTaskPorts(task)
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// HandleStopWithCascade is the HTTP handler for stopping a task.
// If the task has running descendants, it stops them all.
func (h *TaskHandler) HandleStopWithCascade(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check if task has descendants
	descendants, _ := h.store.GetAllDescendants(taskID)
	hasDescendants := len(descendants) > 0

	if hasDescendants {
		if err := h.StopTaskAndDescendants(projectID, taskID); err != nil {
			httputil.JSONError(w, http.StatusNotFound, err.Error())
			return
		}
	} else {
		if err := h.executor.StopTask(taskID); err != nil {
			httputil.JSONError(w, http.StatusNotFound, err.Error())
			return
		}
	}

	task, _ := h.store.GetTask(projectID, taskID)
	h.refreshTaskPorts(task)
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// HandleStartSubtask explicitly starts a single subtask without moving the parent.
func (h *TaskHandler) HandleStartSubtask(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	task, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "task not found")
		return
	}
	task.Status = "in_progress"
	task.UpdatedAt = time.Now().UTC()
	if err := h.store.UpdateTask(task); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	if err := h.executor.StartTask(projectID, taskID); err != nil {
		log.Printf("[start-subtask] task=%s error: %v", taskID, err)
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	task, _ = h.store.GetTask(projectID, taskID)
	h.refreshTaskPorts(task)
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// SelectSubtaskToStart delegates to the executor's subtask selection logic.
func (h *TaskHandler) SelectSubtaskToStart(parent *types.Task, subtasks []types.Task) string {
	return h.executor.SelectSubtaskToStart(parent, subtasks)
}

// StartSubtaskWithParent delegates to the executor.
func (h *TaskHandler) StartSubtaskWithParent(projectID, subtaskID string) error {
	return h.executor.StartSubtaskWithParent(projectID, subtaskID)
}

// StopTaskAndDescendants stops a task and all its running descendants.
func (h *TaskHandler) StopTaskAndDescendants(projectID, taskID string) error {
	// Get all descendants
	descendants, err := h.store.GetAllDescendants(taskID)
	if err != nil {
		log.Printf("[stop-cascade] failed to get descendants of %s: %v", taskID, err)
	}

	// Stop all running descendants (leaf-first order: reverse the list)
	for i := len(descendants) - 1; i >= 0; i-- {
		d := descendants[i]
		if d.ExecutionState == "running" {
			if err := h.executor.StopTask(d.ID); err != nil {
				log.Printf("[stop-cascade] failed to stop descendant %s: %v", d.ID, err)
			}
			d.ExecutionState = "stopped"
			d.Output = "Stopped by parent"
			d.UpdatedAt = time.Now().UTC()
			_ = h.store.UpdateTask(&d)
			h.boardStreamMgr.BroadcastTaskUpdated(projectID, &d)
		}
	}

	// Stop the task itself
	if err := h.executor.StopTask(taskID); err != nil {
		// Task might not be running (parent may be idle while subtask runs)
		task, _ := h.store.GetTask(projectID, taskID)
		if task != nil && task.ExecutionState == "running" {
			task.ExecutionState = "stopped"
			task.Output = "Stopped by user"
			task.UpdatedAt = time.Now().UTC()
			_ = h.store.UpdateTask(task)
		}
		return nil // executor not running is OK
	}

	task, _ := h.store.GetTask(projectID, taskID)
	if task != nil {
		task.ExecutionState = "stopped"
		task.Output = "Stopped by user"
		task.UpdatedAt = time.Now().UTC()
		_ = h.store.UpdateTask(task)
	}

	return nil
}

func autoGenerateTitle(prompt string) string {
	line := strings.Split(prompt, "\n")[0]
	line = strings.TrimSpace(line)
	if len(line) > 80 {
		line = line[:77] + "..."
	}
	return line
}

func isShortTitle(prompt string) bool {
	line := strings.Split(prompt, "\n")[0]
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	return len(strings.Fields(line)) <= 5
}
