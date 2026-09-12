// Package managers holds the request-facing domain operations the API is made of - tasks,
// projects, board state, per-project git - between backend/server and the store, git and
// exec layers below. Long-running work belongs to backend/exec, not here.
package managers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
	"kanban/pkg/logging"
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
		// The body is decoded twice, on purpose: into types.Task for the values, and into the
		// still-encoded fields to answer "did the client send this key at all", which no struct
		// can tell (an omitted field and a zero one look the same once decoded).
		var rawBody fieldsPresent
		if err := json.Unmarshal(body, &rawBody); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if err := json.Unmarshal(body, &task); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		// attachments is not a field on Task, so it is the one value read back out of the raw
		// fields - decoded rather than asserted, because `att.(string)` panicked on a number.
		var attachments string
		if _, err := rawBody.decode("attachments", &attachments); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		// A mode nothing dispatches on must not reach the row: the tool-set switch falls through
		// to act, so the task would be offered write tools while reading as something else.
		if _, valid := types.ParseOperatingMode(string(task.OperatingMode)); task.OperatingMode != "" && !valid {
			httputil.JSONError(w, http.StatusBadRequest, "unknown operating mode")
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
		// The peak is the sandbox layer's to record as a container is removed; one from the body
		// would be a number no container ever produced.
		task.SandboxMemoryPeakMB = 0
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
					if rawBody.has(k) {
						skipFields[k] = struct{}{}
					}
				}
				// Apply config defaults + parent inheritance for auto-merge fields
				var parentTask *types.Task
				if task.ParentTaskID != "" {
					parentTask, _ = h.store.GetTask(projectID, task.ParentTaskID)
				}
				types.ApplyTaskCreationDefaults(&task, cfg, parentTask, skipFields)

				if !rawBody.has("operating_mode") && !rawBody.has("plan_mode") {
					if cfg.DefaultOperatingMode != nil {
						task.OperatingMode = *cfg.DefaultOperatingMode
					}
				}
				// Backward compat: if client sends plan_mode (bool), convert to operating_mode
				var planMode bool
				if _, err := rawBody.decode("plan_mode", &planMode); err != nil {
					httputil.JSONError(w, http.StatusBadRequest, err.Error())
					return
				}
				if planMode {
					task.OperatingMode = types.ModePlan
				}
				// Default to act if nothing set it: the zero OperatingMode is "", which is
				// not a mode, and an empty mode gets the act tool set by fall-through while
				// reading as "unset" everywhere else.
				if task.OperatingMode == "" {
					task.OperatingMode = types.ModeAct
				}
				if cfg.DefaultMaxFailedTurns != nil && !rawBody.has("max_consecutive_failed_turns") {
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
			// Any task may have subtasks; the parent's status picks the child's column.
			task.Status = types.SubtaskStatusFor(parent.Status)
			if task.Status == types.StatusInProgress {
				// Pending: created, not started. The user starts it.
				task.ExecutionState = types.ExecStateIdle
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
			httputil.Fail(w, err, "create default thread", "task not found")
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
		httputil.MethodNotAllowed(w)
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
		task, ok := resolveTask(w, r, h.store)
		if !ok {
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
		task, ok := resolveTask(w, r, h.store)
		if !ok {
			return
		}
		var req *taskUpdateRequest
		if !httputil.DecodeJSON(w, r, &req) {
			return
		}
		if req == nil {
			httputil.JSONError(w, http.StatusBadRequest, "request body required")
			return
		}
		// Captured before any field moves: both are compared against the stored row
		// afterwards to decide what else has to change.
		originalSubtasksAutoMerge := task.SubtasksAutoMerge
		originalParentTaskID := task.ParentTaskID

		modeChanged, applyErr := req.apply(task)
		if applyErr != nil {
			httputil.JSONError(w, http.StatusBadRequest, applyErr.Error())
			return
		}
		// Remount container if not currently running to apply new mount mode (ro vs rw)
		if modeChanged && task.ExecutionState != types.ExecStateRunning && h.executor != nil {
			h.executor.StopSandbox(task.ID)
		}

		// When parent_task_id changes, re-derive auto_merge/auto_commit to respect
		// the new parent's "allow subtask to auto merge" preference
		if req.ParentTaskID != nil && task.ParentTaskID != originalParentTaskID {
			skipFields := req.autoMergeFieldsSent()
			var parentTask *types.Task
			if task.ParentTaskID != "" {
				parent, err := h.store.GetTask(projectID, task.ParentTaskID)
				if err != nil {
					httputil.JSONError(w, http.StatusBadRequest, "parent task not found")
					return
				}
				parentTask = parent
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
		h.deleteTaskTrees(projectID, taskID)
		w.WriteHeader(http.StatusNoContent)
	default:
		httputil.MethodNotAllowed(w)
	}
}

// HandleClearDone deletes all tasks with status 'done' for a project.
func (h *TaskHandler) HandleClearDone(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")

	tasks, err := h.store.GetTasksByProject(projectID)
	if err != nil {
		httputil.Fail(w, err, "list tasks", "project not found")
		return
	}

	var cleared []string
	for _, task := range tasks {
		if task.Status != types.StatusDone {
			continue
		}
		taskID := task.ID
		// Deleting a task deletes its whole subtree, so a Done parent that still holds
		// unfinished work stays on the board to be cleared deliberately.
		if unfinished := h.unfinishedDescendants(taskID); unfinished > 0 {
			logging.For(logging.Managers).With(logging.TaskID, taskID).Infof("not cleared: %d descendant(s) still unfinished", unfinished)
			continue
		}
		cleared = append(cleared, taskID)
	}

	h.deleteTaskTrees(projectID, cleared...)
	httputil.JSONResponse(w, http.StatusOK, map[string]any{"deleted": len(cleared)})
}

// HandleMove moves a task to a different column.
func (h *TaskHandler) HandleMove(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	var req types.MoveTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	task, ok := resolveTask(w, r, h.store)
	if !ok {
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
		wasTurn := task.ExecutionState == types.ExecStateRunning || task.ExecutionState == types.ExecStatePullingImage ||
			task.ExecutionState == types.ExecStatePaused
		_ = h.executor.StopTask(taskID)
		if wasTurn {
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
				c := git.Command("", "-C", project.Path, "rev-parse", task.Branch)
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
		children, childErr := h.store.GetChildren(taskID)
		if childErr != nil {
			// An unreadable child list is not "no children". Starting here could run this
			// task next to its own subtasks, so the move stands and the auto-start does
			// not — the user starts it once the read works again.
			logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("children unreadable — not auto-starting: %v", childErr)
		} else if len(children) > 0 {
			// Task with subtasks: start the deepest startable one; this task
			// stays started-but-pending until all children are done.
			h.moveAllDescendantsToInProgress(taskID)
			// Task with subtasks: start the deepest startable one (or all of
			// them when "run siblings in parallel" is enabled). This task stays
			// started-but-pending until all children are done.
			toStart := h.subtasksToStart(task, children)
			if len(toStart) > 0 {
				if _, anyStarted, err := h.startSubtaskIDs(projectID, taskID, "[move]", toStart); anyStarted {
					logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("started %d subtask(s): %v", len(toStart), toStart)
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
					err = h.executor.StartSubtask(projectID, taskID, false)
				} else {
					err = h.executor.StartTask(projectID, taskID, false)
				}
				if err != nil {
					logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("start failed: %v", err)
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
			if err := h.executor.StartSubtask(projectID, taskID, false); err != nil {
				logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("subtask start failed: %v", err)
				task.ExecutionState = types.ExecStateIdle
				task.Status = originalStatus
				task.Output = err.Error()
			} else {
				started = true
			}
		} else {
			if err := h.executor.StartTask(projectID, taskID, false); err != nil {
				logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("auto-start failed: %v", err)
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
		_ = h.executor.AutoStartNextSibling(projectID, taskID, task.ParentTaskID)
	}

	h.refreshTaskPorts(task)
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// moveAllDescendantsToInProgress recursively moves all descendant tasks (children, grandchildren, etc.)
// to in_progress/idle. Tasks in backlog or in_progress (not running/failed) are moved.
//
// allowed Pending: the work is in these children, not in the parent's own turn.
func (h *TaskHandler) moveAllDescendantsToInProgress(taskID string) {
	descendants, err := h.store.GetAllDescendants(taskID)
	if err != nil {
		logging.For(logging.Merge).Warnf("failed to get descendants of %s: %v", taskID, err)
		return
	}
	for i := range descendants {
		d := &descendants[i]
		if d.Status == types.StatusBacklog || (d.Status == types.StatusInProgress && d.ExecutionState != types.ExecStateRunning && d.ExecutionState != types.ExecStateFailed) {
			d.Status = types.StatusInProgress
			d.ExecutionState = types.ExecStateIdle
			d.UpdatedAt = time.Now().UTC()
			if err := h.store.UpdateTask(d); err != nil {
				logging.For(logging.Merge).Warnf("failed to update descendant %s: %v", d.ID, err)
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
		logging.For(logging.Merge).Warnf("failed to get descendants of %s: %v", taskID, err)
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
			logging.For(logging.Merge).Warnf("failed to update descendant %s: %v", d.ID, err)
		} else {
			h.boardStreamMgr.BroadcastTaskUpdated(projectID, d)
		}
	}
}

// unfinishedDescendants counts the descendants of a task that are not done, so Clear
// Done can leave a finished parent whose subtree still holds live work.
func (h *TaskHandler) unfinishedDescendants(taskID string) int {
	descendants, err := h.store.GetAllDescendants(taskID)
	if err != nil {
		// An unreadable subtree is not an empty one: keep the parent.
		logging.For(logging.Managers).Warnf("descendants of %s unreadable: %v", taskID, err)
		return 1
	}
	n := 0
	for i := range descendants {
		if descendants[i].Status != types.StatusDone {
			n++
		}
	}
	return n
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
		logging.For(logging.Merge).Warnf("failed to get descendants of %s: %v", taskID, err)
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
			logging.For(logging.Merge).Warnf("failed to update descendant %s: %v", d.ID, err)
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
	name, email := git.AuthorIdentity()

	// If env vars are set, they take priority
	nameSet := name != ""
	emailSet := email != ""

	// Try to get values from project's git config
	if proj, err := s.GetProject(projectID); err == nil && proj.Path != "" {
		if !nameSet {
			if out, err := git.Command("", "-C", proj.Path, "config", "user.name").CombinedOutput(); err == nil {
				name = strings.TrimSpace(string(out))
			}
		}
		if !emailSet {
			if out, err := git.Command("", "-C", proj.Path, "config", "user.email").CombinedOutput(); err == nil {
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
	task, ok := resolveTask(w, r, h.store)
	if !ok {
		return
	}
	if !requireWorktreeMgr(w, h.worktreeMgr) {
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
	task, ok := resolveTask(w, r, h.store)
	if !ok {
		return
	}
	if !requireWorktreeMgr(w, h.worktreeMgr) {
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

	// Commit worktree changes, serialized against tool execution. The wait is announced because one that
	// lands behind a long tool call looks like a click that went nowhere.
	unlockWorktree := h.executor.LockWorktreeWatched(taskID, stream.LockWaitNoticeEvery,
		h.boardStreamMgr.LockWaitNotifier(projectID, taskID, "worktree"))
	_, wtErr := h.worktreeMgr.GitRunInWorktreeOutput(taskID, "diff", "--stat")
	worktreeExists := wtErr == nil
	if !worktreeExists {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("worktree does not exist, skipping commit")
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
	unlockWorktree()

	// Capture branch tip before merge (for post-commit diff comparison)
	if task.Branch != "" {
		project, pErr := h.store.GetProject(projectID)
		if pErr == nil {
			c := git.Command("", "-C", project.Path, "rev-parse", task.Branch)
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
			logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("parent %s has no branch, creating it", task.ParentTaskID)
			if err := h.executor.EnsureParentBranch(projectID, parent); err != nil {
				logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("failed to ensure parent branch: %v", err)
			} else {
				// Reload task with updated parent info
				task, _ = h.store.GetTask(projectID, taskID)
			}
		}
	}

	if mergeErr := h.executor.MergeBranch(projectID, taskID, task); mergeErr != nil {
		logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("merge failed: %v", mergeErr)
		// Reload to check for merge conflict flag set by MergeBranch
		task, _ = h.store.GetTask(projectID, taskID)
		if !task.HasMergeConflict {
			// Non-conflict merge failure (e.g., branch creation failed) — return error
			h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
			httputil.Fail(w, mergeErr, "merge branch", "task not found")
			return
		}
	}

	// Reload task — MergeBranch may have set merge conflict flag
	task, _ = h.store.GetTask(projectID, taskID)
	if task.HasMergeConflict {
		// The turn that resolves this conflict has to finish the click that produced it, and only here
		// do we learn the click did. Armed ahead of the hand-off and the early return below.
		if h.executor != nil {
			h.executor.ArmCommitAndDone(taskID)
		}
		// Already resolving a merge conflict — don't add duplicate messages
		if task.ExecutionState == types.ExecStateRunning {
			logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("already resolving merge conflict, skipping")
			h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
			httputil.JSONResponse(w, http.StatusOK, task)
			return
		}
		// Merge conflict — trigger LLM to resolve via user message (handleLazyStart)
		logging.For(logging.Merge).With(logging.TaskID, taskID).Infof("merge conflict detected, triggering LLM resolution")

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
			logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("failed to render merge conflict prompt: %v", pErr)
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
		_ = h.executor.AutoStartNextSibling(projectID, taskID, task.ParentTaskID)
	}

	h.refreshTaskPorts(task)
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// taskTreeDeleter is the normal task-delete path, handed to the project handler so a project's
// deletion is its tasks' deletions.
type taskTreeDeleter interface {
	deleteTaskTrees(projectID string, taskIDs ...string)
}

// deleteTaskTrees is the whole of deleting a task: the deleted-events, the teardown of each task in the
// subtree, then one prune and one board broadcast for the batch. Every task deletion goes through here,
// so a caller cannot report a deletion without carrying out its teardown.
func (h *TaskHandler) deleteTaskTrees(projectID string, taskIDs ...string) {
	for _, taskID := range taskIDs {
		// Read before anything is deleted: the descendants go with their root by CASCADE, so a list
		// taken afterwards would answer empty.
		descendants, _ := h.store.GetAllDescendants(taskID)
		h.broadcastTaskDeleted(projectID, taskID, descendants)
		h.cleanupTask(taskID, descendants)
	}
	// Once for the batch, not once per task: PruneOrphanedBranches scans the whole project (three table
	// reads plus a git run) and sees the branches the batch orphaned only once all of its rows are gone.
	if len(taskIDs) > 0 && h.worktreeMgr != nil {
		h.worktreeMgr.PruneOrphanedBranches(projectID)
	}
	h.boardStreamMgr.Broadcast(projectID)
}

// broadcastTaskDeleted sends a task_deleted event for the given task and all its descendants.
func (h *TaskHandler) broadcastTaskDeleted(projectID, taskID string, descendants []types.Task) {
	if h.boardStreamMgr == nil {
		return
	}
	for _, d := range descendants {
		h.boardStreamMgr.BroadcastTaskDeleted(projectID, d.ID)
	}
	h.boardStreamMgr.BroadcastTaskDeleted(projectID, taskID)
}

// cleanupTask tears down a deleted task and the descendants the caller read beforehand.
func (h *TaskHandler) cleanupTask(taskID string, descendants []types.Task) {
	// Reverse of the depth-first order: subtasks before their parent.
	for i := len(descendants) - 1; i >= 0; i-- {
		h.cleanupSingleTask(descendants[i].ID)
	}
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
				logging.For(logging.Managers).Warnf("task delete: worktree cleanup failed for %s: %v", taskID, err)
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
	var projectID string
	if taskErr == nil {
		projectID = task.ProjectID
		_ = h.store.DeleteTask(projectID, taskID)
	}
	// Clean up stash entries for all branches associated with this task.
	// A task may have stashes on its original branch (task/<taskID>), its current
	// branch (which may be a fork), and thread branches (thread/<threadID>).
	// Runs after DB delete so it works even when the initial task lookup failed.
	if h.worktreeMgr != nil && projectID != "" {
		// Unpin this task's snapshots so git can reclaim their objects. These refs are MIKE's own
		// namespace; the stash sweep below still serves repos written before snapshots were refs.
		if n, err := h.worktreeMgr.DeleteTaskSnapshots(projectID, taskID); err != nil {
			logging.For(logging.Managers).Warnf("task delete: snapshot cleanup failed for %s: %v", taskID, err)
		} else if n > 0 {
			logging.For(logging.Managers).Infof("task delete: unpinned %d snapshot(s) for %s", n, taskID)
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
}

// startTitleGeneration spawns a goroutine that uses the LLM to generate
// a concise title for the task. On success, it updates the task title.
// On failure or timeout, it keeps the existing (first-line) title.
func (h *TaskHandler) startTitleGeneration(projectID, taskID, prompt string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// A handler can exist without an executor, as guarded everywhere else in this file.
		// finishTitleGeneration still has to run, or the task shows "title coming" forever.
		if h.executor == nil {
			logging.For(logging.Generate).With(logging.TaskID, taskID).Infof("skipping: no executor configured")
			h.finishTitleGeneration(projectID, taskID, true)
			return
		}

		// Resolve the LLM config for title generation through the configured
		// Utility provider/model, falling back to the task's own values when
		// Utility is unset (same routing as commit messages, checkpoint labels,
		// and summarization).
		task, err := h.store.GetTask(projectID, taskID)
		if err != nil {
			logging.For(logging.Generate).With(logging.TaskID, taskID).Infof("skipping: %v", err)
			h.finishTitleGeneration(projectID, taskID, true)
			return
		}
		serverURL, modelName := h.executor.ResolveUtilityTarget(task)
		if serverURL == "" || modelName == "" {
			logging.For(logging.Generate).With(logging.TaskID, taskID).Infof("skipping: no LLM config on task")
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
			logging.For(logging.Generate).With(logging.TaskID, taskID).Warnf("LLM call failed: %v", err)
			h.finishTitleGeneration(projectID, taskID, true)
			return
		}

		if title == "" {
			logging.For(logging.Generate).With(logging.TaskID, taskID).Infof("empty title from LLM")
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
				logging.For(logging.Generate).With(logging.TaskID, taskID).Warnf("failed to store usage message: %v", err)
			}
		}

		// Update only the title — a full-row UpdateTask would clobber
		// concurrent changes made since the row was read at creation time
		// (e.g. status: backlog → in_progress when the task was started
		// while the title call was still queued on the provider gate).
		if err := h.store.SetTaskTitle(projectID, taskID, title); err != nil {
			logging.For(logging.Generate).With(logging.TaskID, taskID).Warnf("update failed: %v", err)
			return
		}

		logging.For(logging.Generate).With(logging.TaskID, taskID).Infof("title updated to: %q", title)
		if updated, err := h.store.GetTask(projectID, taskID); err == nil {
			h.boardStreamMgr.BroadcastTaskUpdated(projectID, updated)
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
		logging.For(logging.Generate).With(logging.TaskID, taskID).Warnf("finish update failed: %v", err)
		return
	}
	if broadcast {
		if updated, err := h.store.GetTask(projectID, taskID); err == nil {
			h.boardStreamMgr.BroadcastTaskUpdated(projectID, updated)
		}
	}
}

// HandleInit creates the worktree and branch for a task without starting LLM execution.
// This enables the terminal and file explorer before the task is started.
// It is idempotent — calling it on an already-initialized task returns the task as-is.
func (h *TaskHandler) HandleInit(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if _, ok := resolveTask(w, r, h.store); !ok {
		return
	}
	task, err := h.executor.InitTask(projectID, taskID)
	if err != nil {
		httputil.Fail(w, err, "initialize task", "task not found")
		return
	}
	logging.For(logging.Startup).With(logging.TaskID, taskID).Infof("worktree ready")
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

// HandleStart starts a task; with incomplete subtasks it uses the LLM to pick one and starts that.
func (h *TaskHandler) HandleStart(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	task, ok := resolveTask(w, r, h.store)
	if !ok {
		return
	}

	// Check if task has children
	children, err := h.store.GetChildren(taskID)
	if err != nil {
		httputil.Fail(w, err, "list subtasks", "task not found")
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
				httputil.Fail(w, firstErr, "start subtask", "subtask not found")
				return
			}
			logging.For(logging.Startup).With(logging.TaskID, taskID).Infof("started %d subtask(s): %v", len(toStart), toStart)
			h.respondWithStartedTask(w, projectID, lastID)
			return
		}
		// No startable subtask: start its own execution only if all children
		// are done, else stay started-but-pending.
		if exec.ChildrenAllDone(children) {
			if err := h.executor.StartTask(projectID, taskID, false); err != nil {
				httputil.Fail(w, err, "start task", "task not found")
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
		// Already-running arrives as errs.ErrConflict and answers 409 with its own sentence; a genuine
		// fault answers 500 without echoing its text. The branch that used to live here tested
		// errors.Is(err, exec.ErrTaskAlreadyRunning) and could never match — the executor returned a
		// fresh error with the same words — so a double-click was reported as a server fault.
		httputil.Fail(w, err, "start task", "task not found")
		return
	}
	logging.For(logging.Startup).With(logging.TaskID, taskID).Infof("started")
	task, _ = h.store.GetTask(projectID, taskID)
	h.refreshTaskPorts(task)
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// HandleStop stops a task and every running descendant.
func (h *TaskHandler) HandleStop(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")

	// Check if task has descendants
	descendants, _ := h.store.GetAllDescendants(taskID)
	hasDescendants := len(descendants) > 0

	if hasDescendants {
		if err := h.StopTaskAndDescendants(projectID, taskID); err != nil {
			// A stop of a task that no longer exists is a 404 because the store says so; before this it
			// was a 404 because this line said so, which reported a database fault as a missing row.
			httputil.Fail(w, err, "stop task and descendants", "task not found")
			return
		}
	} else {
		if err := h.executor.StopTask(taskID); err != nil {
			httputil.Fail(w, err, "stop task", "task not found")
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
	task, ok := resolveTask(w, r, h.store)
	if !ok {
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

	children, err := h.store.GetChildren(taskID)
	if err != nil {
		httputil.Fail(w, err, "list subtasks", "task not found")
		return
	}
	if len(children) > 0 {
		h.moveAllDescendantsToInProgress(taskID)
		toStart := h.subtasksToStart(task, children)
		if len(toStart) > 0 {
			lastID, anyStarted, firstErr := h.startSubtaskIDs(projectID, taskID, "[start-subtask]", toStart)
			if !anyStarted {
				httputil.Fail(w, firstErr, "start subtask", "subtask not found")
				return
			}
			logging.For(logging.Startup).With(logging.TaskID, taskID).Infof("has subtasks, started %d: %v", len(toStart), toStart)
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
		httputil.Fail(w, err, "start subtask", "task not found")
		return
	}
	task, _ = h.store.GetTask(projectID, taskID)
	h.refreshTaskPorts(task)
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// subtasksToStart returns the subtask IDs to start for a parent. With "run siblings in
// parallel" it returns every startable (deepest) subtask so they all begin at once; otherwise
// the single subtask the depth/LLM heuristic picks. The per-provider concurrency gate still
// caps how many run — the rest queue as "Pending".
func (h *TaskHandler) subtasksToStart(parent *types.Task, subtasks []types.Task) []string {
	if h.configStore != nil && h.configStore.GetRunSiblingsParallel() {
		deepest := h.executor.FindDeepestStartableSubtasks(subtasks)
		ids := make([]string, 0, len(deepest))
		for _, d := range deepest {
			ids = append(ids, d.ID)
		}
		return ids
	}
	if id := h.executor.SelectSubtaskToStart(parent, subtasks); id != "" {
		return []string{id}
	}
	return nil
}

// startSubtaskIDs starts each subtask autostarted — what that decides is on
// TaskExecutor.StartSubtask. A failed start is logged, not fatal, so one error does not block the
// other siblings. Returns the last requested ID, whether any start succeeded, and the first error.
func (h *TaskHandler) startSubtaskIDs(projectID, taskID, logPrefix string, ids []string) (lastID string, anyStarted bool, firstErr error) {
	for _, id := range ids {
		if err := h.executor.StartSubtask(projectID, id, true); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			logging.For(logging.Managers).Warnf("%s task=%s error starting %s: %v", logPrefix, taskID, id, err)
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

// StopTaskAndDescendants stops a task and all its in-flight descendants.
// Each in-flight descendant (leaf-first) and the task itself are transitioned
// via executor.StopTask (in_progress → paused, done → completed, other
// columns → idle). Descendants that are not in-flight (pending subtasks) are
// left untouched.
func (h *TaskHandler) StopTaskAndDescendants(projectID, taskID string) error {
	descendants, err := h.store.GetAllDescendants(taskID)
	if err != nil {
		logging.For(logging.Exec).Warnf("failed to get descendants of %s: %v", taskID, err)
	}

	// Stop all in-flight descendants (leaf-first order: reverse the list)
	for i := len(descendants) - 1; i >= 0; i-- {
		d := descendants[i]
		if d.ExecutionState == types.ExecStateRunning || d.ExecutionState == types.ExecStatePullingImage {
			if err := h.executor.StopTask(d.ID); err != nil {
				logging.For(logging.Exec).Warnf("failed to stop descendant %s: %v", d.ID, err)
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
