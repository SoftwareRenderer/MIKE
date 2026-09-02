// Package managers holds the request-facing domain operations the API is made of - tasks,
// projects, board state, per-project git - between backend/server and the store, git and
// exec layers below. Long-running work belongs to backend/exec, not here.
package managers

import (
	"context"
	"encoding/json"
	"errors"
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
	"kanban/backend/prompts"
	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/term"
	"kanban/backend/types"
)

// taskStore is the task lifecycle the API drives. It has no execution-state methods
// on purpose - backend/exec owns that.
type taskStore interface {
	ClearTitleGenerating(projectID, taskID string) error
	CreateTask(t *types.Task) error
	CreateTaskMessage(m *types.TaskMessage) error
	CreateThread(th *types.TaskThread) error
	DeleteTask(projectID, taskID string) error
	DeleteTaskDiffSnapshot(taskID string) error
	GetAllDescendants(taskID string) ([]types.Task, error)
	GetChildren(taskID string) ([]types.Task, error)
	GetProject(id string) (*types.Project, error)
	GetTask(projectID, taskID string) (*types.Task, error)
	GetTaskByID(taskID string) (*types.Task, error)
	GetTasksByProject(projectID string) ([]types.Task, error)
	GetThread(threadID string) (*types.TaskThread, error)
	GetThreadsByTask(taskID string) ([]types.TaskThread, error)
	SetTaskTitle(projectID, taskID, title string) error
	UpdateTask(t *types.Task) error
}

var _ taskStore = (*store.Store)(nil)

// TaskHandler handles task CRUD operations.
type TaskHandler struct {
	store          taskStore
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
		path := filepath.Join(workspaceDir, tasks[i].ProjectID, tasks[i].ID)
		_, err := os.Stat(path)
		tasks[i].WorktreeExists = err == nil
	}
}

// enrichTaskWorktree sets the WorktreeExists field on a single task.
func enrichTaskWorktree(task *types.Task, workspaceDir string) {
	path := filepath.Join(workspaceDir, task.ProjectID, task.ID)
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
			httputil.Fail(w, err, "get tasks", "project not found")
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
		// Ensure uniqueness
		for {
			if _, err := h.store.GetTaskByID(task.ID); err != nil {
				break // unique
			}
			task.ID = types.GenerateTaskID()
		}
		task.ProjectID = projectID
		if task.Status == "" {
			task.Status = types.StatusBacklog
		}
		now := time.Now().UTC()
		task.CreatedAt = now
		task.UpdatedAt = now
		if task.Title == "" && task.Prompt != "" {
			task.Title = autoGenerateTitle(task.Prompt)
		}
		if h.configStore != nil {
			if cfg, err := h.configStore.GetConfig(); err == nil {
				// Build skip set for fields explicitly sent by the client
				skipFields := make(map[string]struct{})
				for _, k := range []string{"auto_commit", "auto_merge", "subtasks_auto_merge"} {
					if hasField(rawBody, k) {
						skipFields[k] = struct{}{}
					}
				}
				// Apply config defaults + parent inheritance for auto-merge fields
				var parentTask *types.Task
				if task.ParentTaskID != "" {
					parentTask, _ = h.store.GetTask(projectID, task.ParentTaskID)
				}
				types.ApplyTaskCreationDefaults(&task, cfg, parentTask, skipFields)

				if !hasField(rawBody, "operating_mode") && !hasField(rawBody, "plan_mode") {
					if cfg.DefaultOperatingMode != nil {
						task.OperatingMode = *cfg.DefaultOperatingMode
					}
				}
				// Backward compat: if client sends plan_mode (bool), convert to operating_mode
				if pm, ok := rawBody["plan_mode"]; ok {
					if v, ok := pm.(bool); ok && v {
						task.OperatingMode = "plan"
					}
				}
				// Default to "act" if not set
				if task.OperatingMode == "" {
					task.OperatingMode = "act"
				}
				if cfg.DefaultMaxFailedTurns != nil && !hasField(rawBody, "max_consecutive_failed_turns") {
					task.MaxConsecutiveFailedTurns = *cfg.DefaultMaxFailedTurns
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
			if parent.Status != types.StatusBacklog {
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
			httputil.Fail(w, err, "create task", "project not found")
			return
		}
		// Create default thread for the task
		defaultThreadID := types.GenerateThreadID()
		// Ensure uniqueness
		for {
			if _, err := h.store.GetThread(defaultThreadID); err != nil {
				break // unique
			}
			defaultThreadID = types.GenerateThreadID()
		}
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
			httputil.Fail(w, err, "update task", "task not found")
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
		h.boardStreamMgr.Broadcast(projectID)
		httputil.JSONResponse(w, http.StatusCreated, task)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleByID handles GET/PUT/DELETE for a single task.
func (h *TaskHandler) HandleByID(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if err := types.ValidateTaskID(taskID); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		task, err := h.store.GetTask(projectID, taskID)
		if err != nil {
			httputil.Fail(w, err, "get task", "task not found")
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
			httputil.Fail(w, err, "get task", "task not found")
			return
		}
		// Capture original values before partial update
		originalSubtasksAutoMerge := task.SubtasksAutoMerge
		originalParentTaskID := task.ParentTaskID
		body, _ := io.ReadAll(r.Body)
		var rawBody map[string]any
		json.Unmarshal(body, &rawBody)
		// Apply partial update: only modify fields present in the request body
		if v, ok := rawBody["title"]; ok {
			task.Title = v.(string)
		}
		if v, ok := rawBody["prompt"]; ok {
			task.Prompt = v.(string)
		}
		if v, ok := rawBody["status"]; ok {
			// As in HandleMove: validate the column before it reaches the row.
			newStatus, valid := types.ParseStatus(v.(string))
			if !valid {
				httputil.JSONError(w, http.StatusBadRequest, "unknown status")
				return
			}
			task.Status = newStatus
		}
		if v, ok := rawBody["session_id"]; ok {
			task.SessionID = v.(string)
		}
		if v, ok := rawBody["branch"]; ok {
			branch := v.(string)
			if err := types.ValidateBranchName(branch); err != nil {
				httputil.JSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			task.Branch = branch
		}
		if v, ok := rawBody["base_ref"]; ok {
			baseRef := v.(string)
			if err := types.ValidateBranchName(baseRef); err != nil {
				httputil.JSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			task.BaseRef = baseRef
		}
		if v, ok := rawBody["branch_tip"]; ok {
			branchTip := v.(string)
			if err := types.ValidateBranchName(branchTip); err != nil {
				httputil.JSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			task.BranchTip = branchTip
		}
		if v, ok := rawBody["output"]; ok {
			task.Output = v.(string)
		}
		if v, ok := rawBody["execution_state"]; ok {
			// An unrecognised execution_state would leave a card with no spinner, no badge
			// and no transition that could move it out again.
			newState, valid := types.ParseExecutionState(v.(string))
			if !valid {
				httputil.JSONError(w, http.StatusBadRequest, "unknown execution state")
				return
			}
			task.ExecutionState = newState
		}
		if v, ok := rawBody["has_merge_conflict"]; ok {
			task.HasMergeConflict = v.(bool)
		}
		if v, ok := rawBody["llama_server_url"]; ok {
			task.LlamaServerURL = v.(string)
		}
		if v, ok := rawBody["model_name"]; ok {
			task.ModelName = v.(string)
		}
		if v, ok := rawBody["reasoning_effort"]; ok {
			task.ReasoningEffort = v.(string)
		}
		if v, ok := rawBody["max_consecutive_failed_turns"]; ok {
			task.MaxConsecutiveFailedTurns = int(v.(float64))
		}
		if v, ok := rawBody["auto_commit"]; ok {
			task.AutoCommit = v.(bool)
		}
		if v, ok := rawBody["auto_merge"]; ok {
			task.AutoMerge = v.(bool)
		}
		if v, ok := rawBody["subtasks_auto_merge"]; ok {
			task.SubtasksAutoMerge = v.(bool)
		}
		if v, ok := rawBody["operating_mode"]; ok {
			newMode := v.(string)
			if newMode != task.OperatingMode {
				task.OperatingMode = newMode
				// Remount container if not currently running to apply new mount mode (ro vs rw)
				if task.ExecutionState != types.ExecStateRunning && h.executor != nil {
					h.executor.StopSandbox(task.ID)
				}
			}
		}
		if v, ok := rawBody["parent_task_id"]; ok {
			task.ParentTaskID = v.(string)
		}
		if v, ok := rawBody["title_generating"]; ok {
			task.TitleGenerating = v.(bool)
		}
		if v, ok := rawBody["current_thread_id"]; ok {
			task.CurrentThreadID = v.(string)
		}
		// When parent_task_id changes, re-derive auto_merge/auto_commit to respect
		// the new parent's "allow subtask to auto merge" preference
		if hasField(rawBody, "parent_task_id") && task.ParentTaskID != originalParentTaskID {
			skipFields := make(map[string]struct{})
			for _, k := range []string{"auto_commit", "auto_merge", "subtasks_auto_merge"} {
				if hasField(rawBody, k) {
					skipFields[k] = struct{}{}
				}
			}
			var parentTask *types.Task
			if task.ParentTaskID != "" {
				parentTask, err = h.store.GetTask(projectID, task.ParentTaskID)
				if err != nil {
					httputil.JSONError(w, http.StatusBadRequest, "parent task not found")
					return
				}
			}
			var cfg *types.Config
			if h.configStore != nil {
				if c, err := h.configStore.GetConfig(); err == nil {
					cfg = c
				}
			}
			types.ApplyTaskCreationDefaults(task, cfg, parentTask, skipFields)
		}
		task.UpdatedAt = time.Now()
		// Auto-commit is a prerequisite for auto-merge
		if task.AutoMerge {
			task.AutoCommit = true
		}
		if err := h.store.UpdateTask(task); err != nil {
			httputil.Fail(w, err, "update task", "task not found")
			return
		}
		// Propagate subtasks_auto_merge change to all descendants
		if task.SubtasksAutoMerge != originalSubtasksAutoMerge {
			h.propagateSubtasksAutoMerge(projectID, taskID, task.SubtasksAutoMerge)
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
			httputil.Fail(w, err, "get task", "task not found")
			return
		}
		h.broadcastTaskDeleted(projectID, taskID)
		h.cleanupTask(taskID)
		h.boardStreamMgr.Broadcast(projectID)
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
		if task.Status != types.StatusDone {
			continue
		}
		taskID := task.ID
		h.broadcastTaskDeleted(projectID, taskID)
		h.cleanupTask(taskID)
		deleted++
	}

	h.boardStreamMgr.Broadcast(projectID)
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
		httputil.Fail(w, err, "get task", "task not found")
		return
	}
	// Status is the one field a client sends as free text: checked here, typed below. A
	// value outside the columns leaves the task in the database but on no column -
	// invisible, and unstuckable from the UI. The offending text is not echoed back.
	targetStatus, ok := types.ParseStatus(req.Status)
	if !ok {
		httputil.JSONError(w, http.StatusBadRequest, "unknown status")
		return
	}
	// Block subtasks from moving to Backlog when an ancestor is In Progress.
	if targetStatus == types.StatusBacklog && task.ParentTaskID != "" {
		if err := h.checkAncestorInProgress(task); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	originalStatus := task.Status // save for revert on auto-start failure
	task.Status = targetStatus
	task.UpdatedAt = time.Now()

	// Stop the executor if the task is moved out of In Progress.
	// This ensures the goroutine exits and HasRunningTasks() reflects the new state.
	if originalStatus == types.StatusInProgress && targetStatus != types.StatusInProgress && h.executor != nil {
		wasInFlight := task.ExecutionState == types.ExecStateRunning || task.ExecutionState == types.ExecStatePullingImage
		_ = h.executor.StopTask(taskID)
		if wasInFlight {
			task.ExecutionState = types.ExecStateIdle
			task.Output = ""
		}
	}
	if targetStatus == types.StatusDone {
		task.ExecutionState = types.ExecStateCompleted
		if h.executor != nil {
			h.executor.StopSandbox(taskID)
		}
		// Capture branch tip for post-commit diff comparison.
		// If already set (commit-and-done flow), keep the existing value.
		if task.BranchTip == "" && task.Branch != "" {
			project, pErr := h.store.GetProject(projectID)
			if pErr == nil {
				c := osexec.Command("git", "-C", project.Path, "rev-parse", task.Branch)
				if out, err := c.CombinedOutput(); err == nil {
					task.BranchTip = strings.TrimSpace(string(out))
				}
			}
		}
	} else if task.ExecutionState == types.ExecStateCompleted {
		// Task moved away from Done — reset status so it can run again
		task.ExecutionState = types.ExecStateIdle
	}
	started := false
	// set when the start had nothing to run; the move must not cascade into children then
	awaitingUser := false
	if targetStatus == types.StatusInProgress && task.ExecutionState != types.ExecStateRunning {
		children, _ := h.store.GetChildren(taskID)
		if len(children) > 0 {
			// Task with subtasks: start the deepest startable one; this task
			// stays started-but-pending until all children are done.
			h.moveAllDescendantsToInProgress(taskID)
			// Task with subtasks: start the deepest startable one (or all of
			// them when "run siblings in parallel" is enabled). This task stays
			// started-but-pending until all children are done.
			toStart := h.subtasksToStart(task, children)
			if len(toStart) > 0 {
				if _, anyStarted, err := h.startSubtaskIDs(projectID, taskID, "[move]", toStart); anyStarted {
					log.Printf("[move] task=%s started %d subtask(s): %v", taskID, len(toStart), toStart)
					started = true
				} else if err != nil {
					// All subtask starts failed — keep task in_progress (user's
					// explicit action); record error for visibility.
					task.ExecutionState = types.ExecStateIdle
					task.Output = err.Error()
				}
			} else if exec.ChildrenAllDone(children) {
				// All subtasks done — start this task's own execution.
				var err error
				if task.ParentTaskID != "" {
					err = h.StartSubtask(projectID, taskID, false)
				} else {
					err = h.executor.StartTask(projectID, taskID, false)
				}
				if err != nil {
					log.Printf("[move] task=%s start failed: %v", taskID, err)
					task.ExecutionState = types.ExecStateIdle
					task.Status = originalStatus
					task.Output = err.Error()
				} else {
					started = true
				}
			}
			// else: subtasks still finishing (review/failed) — stay started-but-pending
			// (allowed Pending: waiting on the children).
		} else if task.ParentTaskID != "" {
			// Leaf subtask: subtask-aware start (branch from parent, inherit model config)
			if err := h.StartSubtask(projectID, taskID, false); err != nil {
				log.Printf("[move] task=%s subtask start failed: %v", taskID, err)
				task.ExecutionState = types.ExecStateIdle
				task.Status = originalStatus
				task.Output = err.Error()
			} else {
				started = true
			}
		} else {
			if err := h.executor.StartTask(projectID, taskID, false); err != nil {
				log.Printf("[move] task=%s auto-start failed: %v", taskID, err)
				task.ExecutionState = types.ExecStateIdle
				task.Status = originalStatus
				task.Output = err.Error()
			} else {
				started = true
			}
		}
	}
	// Reload after a successful start so the final UpdateTask doesn't clobber the
	// branch set by the start path. Re-apply the target status: starting a
	// subtask instead of this task leaves its row's status stale. Not when the start
	// found nothing to run and left the row in review — that is where it belongs.
	if started {
		reloaded, err := h.store.GetTask(projectID, taskID)
		if err == nil {
			if targetStatus == types.StatusInProgress && reloaded.Status == types.StatusReview {
				awaitingUser = true
			} else {
				reloaded.Status = targetStatus
			}
			task = reloaded
		}
	}
	if err := h.store.UpdateTask(task); err != nil {
		httputil.Fail(w, err, "update task", "task not found")
		return
	}

	// Move all descendants to the same column (recursive, leaf-first)
	// Use task.Status (actual final status) in case auto-start failed and reverted.
	// Skipped when nothing ran: dragging a parent whose children are all Done must not
	// pull those Done children into review.
	if !awaitingUser {
		h.moveAllDescendants(projectID, taskID, task.Status)
	}

	// If a subtask moved to review/done, auto-start the next sibling
	if task.ParentTaskID != "" && (targetStatus == types.StatusReview || targetStatus == types.StatusDone) {
		_ = h.autoStartNextSibling(projectID, taskID, task.ParentTaskID)
	}

	h.refreshTaskPorts(task)
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// autoStartNextSibling delegates to the executor's sibling auto-start logic.
func (h *TaskHandler) autoStartNextSibling(projectID, finishedTaskID, parentTaskID string) error {
	return h.executor.AutoStartNextSibling(projectID, finishedTaskID, parentTaskID)
}

// moveAllDescendantsToInProgress recursively moves all descendant tasks (children, grandchildren, etc.)
// to in_progress/idle. Tasks in backlog or in_progress (not running/failed) are moved.
//
// allowed Pending: the work is in these children, not in the parent's own turn.
func (h *TaskHandler) moveAllDescendantsToInProgress(taskID string) {
	descendants, err := h.store.GetAllDescendants(taskID)
	if err != nil {
		log.Printf("[move-descendants] failed to get descendants of %s: %v", taskID, err)
		return
	}
	for i := range descendants {
		d := &descendants[i]
		if d.Status == types.StatusBacklog || (d.Status == types.StatusInProgress && d.ExecutionState != types.ExecStateRunning && d.ExecutionState != types.ExecStateFailed) {
			d.Status = types.StatusInProgress
			d.ExecutionState = types.ExecStateIdle
			d.UpdatedAt = time.Now().UTC()
			if err := h.store.UpdateTask(d); err != nil {
				log.Printf("[move-descendants] failed to update descendant %s: %v", d.ID, err)
			} else {
				h.boardStreamMgr.BroadcastTaskUpdated(d.ProjectID, d)
			}
		}
	}
}

// moveAllDescendants recursively moves all descendant tasks (children, grandchildren, etc.)
// to the target status. Skips descendants that are already running (e.g., just auto-started).
// Processes in reverse order (leaves first) to ensure child tasks are moved before their parents.
func (h *TaskHandler) moveAllDescendants(projectID, taskID string, targetStatus types.Status) {
	descendants, err := h.store.GetAllDescendants(taskID)
	if err != nil {
		log.Printf("[move-descendants] failed to get descendants of %s: %v", taskID, err)
		return
	}
	// Process in reverse order (leaves first) so children are moved before their parents
	for i := len(descendants) - 1; i >= 0; i-- {
		d := &descendants[i]
		// Skip in-flight descendants
		if d.ExecutionState == types.ExecStateRunning || d.ExecutionState == types.ExecStatePullingImage {
			continue
		}
		// Moving to In Progress is a start: only backlog descendants join. Review
		// awaits a human, done work is complete, and in_progress tasks are managed
		// by the autostart chain — dragging them out would bypass the gates.
		if targetStatus == types.StatusInProgress && d.Status != types.StatusBacklog {
			continue
		}
		// Stop the executor if the descendant was in In Progress and is being moved out
		if d.Status == types.StatusInProgress && targetStatus != types.StatusInProgress && h.executor != nil {
			_ = h.executor.StopTask(d.ID)
		}
		d.Status = targetStatus
		d.UpdatedAt = time.Now().UTC()
		// Set execution_state based on target column
		switch targetStatus {
		case types.StatusDone:
			d.ExecutionState = types.ExecStateCompleted
			if h.executor != nil {
				h.executor.StopSandbox(d.ID)
			}
		case types.StatusBacklog, types.StatusInProgress, types.StatusReview:
			if d.ExecutionState == types.ExecStateCompleted {
				// Moved away from done — reset
				d.ExecutionState = types.ExecStateIdle
			}
			// Keep existing state (failed, stopped, idle) if not overridden above
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
		ancestor, err := h.store.GetTaskByID(currentID)
		if err != nil {
			break // ancestor not found, stop walking
		}
		if ancestor.Status == types.StatusInProgress {
			return fmt.Errorf("cannot move to Backlog: parent task is In Progress")
		}
		currentID = ancestor.ParentTaskID
	}
	return nil
}

// propagateSubtasksAutoMerge recursively sets auto_merge on all descendant tasks
// to match the parent's subtasks_auto_merge setting (enable or disable).
func (h *TaskHandler) propagateSubtasksAutoMerge(projectID, taskID string, enabled bool) {
	descendants, err := h.store.GetAllDescendants(taskID)
	if err != nil {
		log.Printf("[propagate-subtasks-auto-merge] failed to get descendants of %s: %v", taskID, err)
		return
	}
	for i := range descendants {
		d := &descendants[i]
		if d.AutoMerge == enabled {
			continue // already matches
		}
		d.AutoMerge = enabled
		if enabled {
			d.AutoCommit = true // prerequisite for auto_merge
		}
		d.UpdatedAt = time.Now().UTC()
		if err := h.store.UpdateTask(d); err != nil {
			log.Printf("[propagate-subtasks-auto-merge] failed to update descendant %s: %v", d.ID, err)
		} else {
			h.boardStreamMgr.BroadcastTaskUpdated(projectID, d)
		}
	}
}

// resolveGitAuthor returns the effective git author name/email.
// Priority: env var override > project git config > defaults.
// resolveGitAuthor returns the author a commit-and-done commit is signed with: env vars
// win, then the project checkout's own git config.
func resolveGitAuthor(projectID string, s interface {
	GetProject(id string) (*types.Project, error)
}) (string, string) {
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
		httputil.Fail(w, err, "get task", "task not found")
		return
	}
	if h.worktreeMgr == nil {
		httputil.JSONError(w, http.StatusInternalServerError, "worktree manager not available")
		return
	}

	commitMsg := h.executor.GenerateCommitMessage(r.Context(), task)

	// Resolve effective git author: env var override, then project git config, then defaults
	authorName, authorEmail := resolveGitAuthor(projectID, h.store)

	// Resolve merge target branch
	mergeTarget := h.executor.ResolveMergeTarget(projectID, task)

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
	mergeTarget := h.executor.ResolveMergeTarget(projectID, task)
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
		httputil.Fail(w, err, "get task", "task not found")
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

	// Commit worktree changes, serialized against tool execution.
	h.executor.GetWorktreeMu(taskID).Lock()
	_, wtErr := h.worktreeMgr.GitRunInWorktreeOutput(taskID, "diff", "--stat")
	worktreeExists := wtErr == nil
	if !worktreeExists {
		log.Printf("[commit-and-done] task=%s worktree does not exist, skipping commit", taskID)
	}
	if worktreeExists {
		commitMsg := body.CommitMessage
		if commitMsg == "" {
			commitMsg = h.executor.GenerateCommitMessage(r.Context(), task)
		}
		// Refresh staged files from the working tree so post-staging manual
		// edits are included in the commit; unstaged files stay out.
		h.worktreeMgr.StageIndexedFiles(taskID)
		_ = h.worktreeMgr.GitRunInWorktree(taskID, append(git.GitAuthorFlags(), "commit", "-m", commitMsg, "--allow-empty")...)
	}
	h.executor.GetWorktreeMu(taskID).Unlock()

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

	// Ensure parent branch exists for child tasks (safety net for manual commit-and-done).
	// Normally this is done during task start (ensureParentBranch), but if the user
	// commits a subtask without starting it, the parent branch may not exist yet.
	if task.ParentTaskID != "" {
		parent, pErr := h.store.GetTaskByID(task.ParentTaskID)
		if pErr == nil && parent.Branch == "" {
			log.Printf("[commit-and-done] task=%s parent %s has no branch, creating it", taskID, task.ParentTaskID)
			if err := h.executor.EnsureParentBranch(projectID, parent); err != nil {
				log.Printf("[commit-and-done] task=%s failed to ensure parent branch: %v", taskID, err)
			} else {
				// Reload task with updated parent info
				task, _ = h.store.GetTask(projectID, taskID)
			}
		}
	}

	if mergeErr := h.executor.MergeBranch(projectID, taskID, task); mergeErr != nil {
		log.Printf("[commit-and-done] task=%s merge failed: %v", taskID, mergeErr)
		// Reload to check for merge conflict flag set by MergeBranch
		task, _ = h.store.GetTask(projectID, taskID)
		if !task.HasMergeConflict {
			// Non-conflict merge failure (e.g., branch creation failed) — return error
			h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
			httputil.JSONError(w, http.StatusInternalServerError, mergeErr.Error())
			return
		}
	}

	// Reload task — MergeBranch may have set merge conflict flag
	task, _ = h.store.GetTask(projectID, taskID)
	if task.HasMergeConflict {
		// Already resolving a merge conflict — don't add duplicate messages
		if task.ExecutionState == types.ExecStateRunning {
			log.Printf("[commit-and-done] task=%s already resolving merge conflict, skipping", taskID)
			h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
			httputil.JSONResponse(w, http.StatusOK, task)
			return
		}
		// Merge conflict — trigger LLM to resolve via user message (handleLazyStart)
		log.Printf("[commit-and-done] task=%s merge conflict detected, triggering LLM resolution", taskID)

		// Get conflicted files for the prompt
		worktreePath, _ := h.worktreeMgr.WorktreePath(taskID)
		conflictedFiles := exec.GetConflictedFiles(worktreePath)

		// Render merge conflict prompt template
		userContent, pErr := prompts.RenderMergeConflictPrompt(prompts.MergeConflictData{
			TaskBranch:      task.Branch,
			TargetBranch:    h.executor.ResolveMergeTarget(projectID, task),
			TaskTitle:       task.Title,
			TaskDescription: task.Prompt,
			ConflictedFiles: conflictedFiles,
		})
		if pErr != nil {
			log.Printf("[commit-and-done] task=%s failed to render merge conflict prompt: %v", taskID, pErr)
			userContent = "Resolve the merge conflicts and complete the task"
		}

		// Persist system message in chat.
		// The executor loop's HasMergeConflict path handles the post-resolution
		// merge (stage resolved files, commit, pop stash, MergeBranch, mark done).
		h.store.CreateTaskMessage(&types.TaskMessage{
			ID:        uuid.New().String(),
			TaskID:    taskID,
			ThreadID:  task.CurrentThreadID,
			Role:      "system",
			Content:   "Merge conflict detected during commit — resolving with agent",
			CreatedAt: time.Now().UTC(),
		})

		// Persist user message with conflict context
		h.store.CreateTaskMessage(&types.TaskMessage{
			ID:        uuid.New().String(),
			TaskID:    taskID,
			ThreadID:  task.CurrentThreadID,
			Role:      "user",
			Content:   userContent,
			CreatedAt: time.Now().UTC(),
		})

		// Move task to In Progress so the board reflects the LLM is working
		task.Status = types.StatusInProgress
		task.UpdatedAt = time.Now()
		_ = h.store.UpdateTask(task)

		// Release commit lock BEFORE starting LLM (defer release handles this on return)
		// Start LLM to resolve — handleLazyStart will set executionState=running
		if h.executor != nil {
			h.executor.ForwardUserMessage(projectID, taskID, stream.UserMessage{
				Content:   userContent,
				ModelName: task.ModelName,
				ServerURL: task.LlamaServerURL,
			})
		}

		h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
		httputil.JSONResponse(w, http.StatusOK, task)
		return
	}

	// Clean merge — move task to Done
	task.Status = types.StatusDone
	task.ExecutionState = types.ExecStateCompleted
	task.UpdatedAt = time.Now()
	if err := h.store.UpdateTask(task); err != nil {
		httputil.Fail(w, err, "update task", "task not found")
		return
	}

	// Stop sandbox container
	if h.executor != nil {
		h.executor.StopSandbox(taskID)
	}

	// If a subtask was marked done, auto-start the next sibling
	if task.ParentTaskID != "" {
		_ = h.autoStartNextSibling(projectID, taskID, task.ParentTaskID)
	}

	h.refreshTaskPorts(task)
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
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
	task, taskErr := h.store.GetTaskByID(taskID)
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
	// Drop the cached done-task diff snapshot (the FK cascade is a backstop).
	_ = h.store.DeleteTaskDiffSnapshot(taskID)
	// Delete from database - find project first
	var projectID string
	if taskErr == nil {
		projectID = task.ProjectID
		_ = h.store.DeleteTask(task.ProjectID, taskID)
	} else {
		task, taskErr = h.store.GetTaskByID(taskID)
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
		// Unpin this task's snapshots so git can reclaim their objects. These refs are MIKE's own
		// namespace; the stash sweep below still serves repos written before snapshots were refs.
		if n, err := h.worktreeMgr.DeleteTaskSnapshots(projectID, taskID); err != nil {
			log.Printf("task delete: snapshot cleanup failed for %s: %v", taskID, err)
		} else if n > 0 {
			log.Printf("task delete: unpinned %d snapshot(s) for %s", n, taskID)
		}
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
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// Resolve the LLM config for title generation through the configured
		// Utility provider/model, falling back to the task's own values when
		// Utility is unset (same routing as commit messages, checkpoint labels,
		// and summarization).
		task, err := h.store.GetTask(projectID, taskID)
		if err != nil {
			log.Printf("[title-gen] task=%s skipping: %v", taskID, err)
			h.finishTitleGeneration(projectID, taskID, true)
			return
		}
		serverURL, modelName := h.executor.ResolveUtilityTarget(task)
		if serverURL == "" || modelName == "" {
			log.Printf("[title-gen] task=%s skipping: no LLM config on task", taskID)
			h.finishTitleGeneration(projectID, taskID, true)
			return
		}

		messages := []types.ChatMessage{
			{Role: "system", Content: "You are a title generator. Given a task description, generate a short, descriptive title (max 50 characters). Return only the title, nothing else. No quotes, no explanation."},
			{Role: "user", Content: prompt},
		}

		maxTokens := 50
		usage, title, err := h.executor.ChatCompletion(ctx, serverURL, modelName, messages, &exec.ChatCompletionOpts{MaxTokens: &maxTokens, ReasoningEffort: "off"})
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
			providerName := h.configStore.GetProviderIDByURL(serverURL)
			if err := h.store.CreateTaskMessage(&types.TaskMessage{
				ID:              uuid.New().String(),
				TaskID:          taskID,
				ThreadID:        task.CurrentThreadID,
				Role:            "title",
				Content:         title,
				ModelName:       modelName,
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

		// Update only the title — a full-row UpdateTask would clobber
		// concurrent changes made since the row was read at creation time
		// (e.g. status: backlog → in_progress when the task was started
		// while the title call was still queued on the provider gate).
		if err := h.store.SetTaskTitle(projectID, taskID, title); err != nil {
			log.Printf("[title-gen] task=%s update failed: %v", taskID, err)
			return
		}

		log.Printf("[title-gen] task=%s title updated to: %q", taskID, title)
		if updated, err := h.store.GetTask(projectID, taskID); err == nil {
			h.boardStreamMgr.BroadcastTaskUpdated(projectID, *updated)
		}
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
	if err := h.store.ClearTitleGenerating(projectID, taskID); err != nil {
		log.Printf("[title-gen] task=%s finish update failed: %v", taskID, err)
		return
	}
	if broadcast {
		if updated, err := h.store.GetTask(projectID, taskID); err == nil {
			h.boardStreamMgr.BroadcastTaskUpdated(projectID, *updated)
		}
	}
}

// HandleInit creates the worktree and branch for a task without starting LLM execution.
// This enables the terminal and file explorer before the task is started.
// It is idempotent — calling it on an already-initialized task returns the task as-is.
func (h *TaskHandler) HandleInit(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.Fail(w, err, "get task", "task not found")
		return
	}
	task, err := h.executor.InitTask(projectID, taskID)
	if err != nil {
		log.Printf("[init] task=%s error: %v", taskID, err)
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("[init] task=%s worktree ready", taskID)
	// Enrich with worktree existence so the frontend knows the terminal is available
	if h.worktreeMgr != nil {
		cfg, _ := h.configStore.GetConfig()
		if cfg != nil {
			enrichTaskWorktree(task, cfg.WorkspaceDir)
		}
	}
	h.refreshTaskPorts(task)
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// HandleStart is the HTTP handler for starting a task.
// If the task has incomplete subtasks, it uses the LLM to pick one and starts it.
// If the task has no subtasks, it starts normally.
func (h *TaskHandler) HandleStart(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	task, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.Fail(w, err, "get task", "task not found")
		return
	}

	// Check if task has children
	children, err := h.store.GetChildren(taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("get children: %v", err))
		return
	}

	if len(children) > 0 {
		// User explicitly started this task: move it to In Progress
		// (started-but-pending) until its subtasks are done.
		if task.Status == types.StatusBacklog {
			task.Status = types.StatusInProgress
			task.ExecutionState = types.ExecStateIdle
			task.UpdatedAt = time.Now().UTC()
			if err := h.store.UpdateTask(task); err != nil {
				httputil.Fail(w, err, "update task", "task not found")
				return
			}
			h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
		}
		// Move all incomplete descendants (children, grandchildren, etc.) to in_progress.
		h.moveAllDescendantsToInProgress(taskID)
		// Task has subtasks — start the deepest startable one (LLM picks when
		// several), or all of them when "run siblings in parallel" is enabled.
		toStart := h.subtasksToStart(task, children)
		if len(toStart) > 0 {
			lastID, anyStarted, firstErr := h.startSubtaskIDs(projectID, taskID, "[start-parent]", toStart)
			if !anyStarted {
				httputil.JSONError(w, http.StatusInternalServerError, firstErr.Error())
				return
			}
			log.Printf("[start-parent] task=%s started %d subtask(s): %v", taskID, len(toStart), toStart)
			h.respondWithStartedTask(w, projectID, lastID)
			return
		}
		// No startable subtask: start its own execution only if all children
		// are done, else stay started-but-pending.
		if exec.ChildrenAllDone(children) {
			if err := h.executor.StartTask(projectID, taskID, false); err != nil {
				log.Printf("[start-parent] task=%s all subtasks done, start failed: %v", taskID, err)
				httputil.JSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		task, _ = h.store.GetTask(projectID, taskID)
		h.refreshTaskPorts(task)
		h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
		httputil.JSONResponse(w, http.StatusOK, task)
		return
	}

	// No subtasks — start normally
	if err := h.executor.StartTask(projectID, taskID, false); err != nil {
		log.Printf("[start] task=%s error: %v", taskID, err)
		if errors.Is(err, exec.ErrTaskAlreadyRunning) {
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

// HandleStop is the HTTP handler for stopping a task.
// If the task has running descendants, it stops them all.
func (h *TaskHandler) HandleStop(w http.ResponseWriter, r *http.Request) {
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

// HandleStartSubtask starts a subtask. If it has children, the deepest
// startable descendant runs instead; the subtask waits started-but-pending
// until its children are done.
func (h *TaskHandler) HandleStartSubtask(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	task, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.Fail(w, err, "get task", "task not found")
		return
	}
	if task.Status == types.StatusBacklog {
		task.ExecutionState = types.ExecStateIdle
	}
	task.Status = types.StatusInProgress
	task.UpdatedAt = time.Now().UTC()
	if err := h.store.UpdateTask(task); err != nil {
		httputil.Fail(w, err, "update task", "task not found")
		return
	}
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)

	children, _ := h.store.GetChildren(taskID)
	if len(children) > 0 {
		h.moveAllDescendantsToInProgress(taskID)
		toStart := h.subtasksToStart(task, children)
		if len(toStart) > 0 {
			lastID, anyStarted, firstErr := h.startSubtaskIDs(projectID, taskID, "[start-subtask]", toStart)
			if !anyStarted {
				httputil.JSONError(w, http.StatusInternalServerError, firstErr.Error())
				return
			}
			log.Printf("[start-subtask] task=%s has subtasks, started %d: %v", taskID, len(toStart), toStart)
			h.respondWithStartedTask(w, projectID, lastID)
			return
		}
		if !exec.ChildrenAllDone(children) {
			// Subtasks still finishing (review/failed) — stay started-but-pending.
			task, _ = h.store.GetTask(projectID, taskID)
			h.refreshTaskPorts(task)
			h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
			httputil.JSONResponse(w, http.StatusOK, task)
			return
		}
		// All children done — fall through and start the subtask's own execution.
	}

	if err := h.executor.StartTask(projectID, taskID, false); err != nil {
		log.Printf("[start-subtask] task=%s error: %v", taskID, err)
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	task, _ = h.store.GetTask(projectID, taskID)
	h.refreshTaskPorts(task)
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// subtasksToStart returns the subtask IDs to start for a parent task. When
// "run siblings in parallel" is enabled it returns every currently startable
// (deepest) subtask so they all kick off at once; otherwise it returns the
// single subtask picked by the existing depth/LLM heuristic. The per-provider
// concurrency gate still caps how many actually run — the rest queue as
// "Pending".
func (h *TaskHandler) subtasksToStart(parent *types.Task, subtasks []types.Task) []string {
	if h.configStore != nil && h.configStore.GetRunSiblingsParallel() {
		deepest := h.executor.FindDeepestStartableSubtasks(subtasks)
		ids := make([]string, 0, len(deepest))
		for _, d := range deepest {
			ids = append(ids, d.ID)
		}
		return ids
	}
	if id := h.SelectSubtaskToStart(parent, subtasks); id != "" {
		return []string{id}
	}
	return nil
}

// startSubtaskIDs starts each subtask as an autostarted execution (no human
// present to answer ask_question). Per-start failures are logged but not
// fatal, so one error doesn't block the other parallel siblings. Returns the
// last requested ID, whether any start succeeded, and the first error (nil if
// every start succeeded).
func (h *TaskHandler) startSubtaskIDs(projectID, taskID, logPrefix string, ids []string) (lastID string, anyStarted bool, firstErr error) {
	for _, id := range ids {
		if err := h.StartSubtask(projectID, id, true); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			log.Printf("%s task=%s error starting %s: %v", logPrefix, taskID, id, err)
		} else {
			anyStarted = true
		}
	}
	if len(ids) > 0 {
		lastID = ids[len(ids)-1]
	}
	return lastID, anyStarted, firstErr
}

// respondWithStartedTask loads the just-started task, refreshes its dev ports,
// broadcasts the update to the board, and returns it as a 200 JSON response.
func (h *TaskHandler) respondWithStartedTask(w http.ResponseWriter, projectID, taskID string) {
	startedTask, _ := h.store.GetTask(projectID, taskID)
	if startedTask != nil {
		h.refreshTaskPorts(startedTask)
		h.boardStreamMgr.BroadcastTaskUpdated(projectID, startedTask)
	}
	httputil.JSONResponse(w, http.StatusOK, startedTask)
}

// SelectSubtaskToStart delegates to the executor's subtask selection logic.
//
// Start paths should prefer subtasksToStart, which honors the "run siblings in
// parallel" setting (it returns every startable subtask when that is on).
// Use this directly only when a single pick is genuinely wanted.
func (h *TaskHandler) SelectSubtaskToStart(parent *types.Task, subtasks []types.Task) string {
	return h.executor.SelectSubtaskToStart(parent, subtasks)
}

// StartSubtask delegates to the executor.
// The autostarted flag records whether the subtask runs autonomously on behalf
// of its parent (no human present to answer ask_question): true when started via
// HandleStart or AutoStartNextSibling, false only when the user
// explicitly starts a single subtask (HandleStartSubtask).
func (h *TaskHandler) StartSubtask(projectID, subtaskID string, autostarted bool) error {
	return h.executor.StartSubtask(projectID, subtaskID, autostarted)
}

// StopTaskAndDescendants stops a task and all its in-flight descendants.
// Each in-flight descendant (leaf-first) and the task itself are transitioned
// via executor.StopTask (in_progress → paused, done → completed, other
// columns → idle). Descendants that are not in-flight (pending subtasks) are
// left untouched.
func (h *TaskHandler) StopTaskAndDescendants(projectID, taskID string) error {
	descendants, err := h.store.GetAllDescendants(taskID)
	if err != nil {
		log.Printf("[stop-cascade] failed to get descendants of %s: %v", taskID, err)
	}

	// Stop all in-flight descendants (leaf-first order: reverse the list)
	for i := len(descendants) - 1; i >= 0; i-- {
		d := descendants[i]
		if d.ExecutionState == types.ExecStateRunning || d.ExecutionState == types.ExecStatePullingImage {
			if err := h.executor.StopTask(d.ID); err != nil {
				log.Printf("[stop-cascade] failed to stop descendant %s: %v", d.ID, err)
			}
		}
	}

	// Stop the task itself (StopTask works with or without a live executor).
	return h.executor.StopTask(taskID)
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
