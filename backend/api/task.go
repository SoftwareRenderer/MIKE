// Package api holds the request-facing domain operations the API is made of - tasks,
// projects, board state, per-project git - between backend/server and the store, git and
// exec layers below. Long-running work belongs to backend/exec, not here.
package api

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

	"mike/backend/config"
	"mike/backend/errs"
	"mike/backend/exec"
	"mike/backend/git"
	"mike/backend/httputil"
	"mike/backend/prompts"
	"mike/backend/store"
	"mike/backend/stream"
	"mike/backend/term"
	"mike/backend/types"
	"mike/pkg/logging"
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

// taskSandboxOps is the sandbox operations used for port refresh.
type taskSandboxOps interface {
	GetContainerPorts(taskID string) ([]types.AssignedPort, error)
}

// TaskDeps is the dependencies for TaskHandler.
type TaskDeps struct {
	Store       *store.Store
	BoardStream *stream.AppStreamManager
	ConfigStore *config.ConfigStore
	TerminalMgr *term.TerminalManager
	Executor    *exec.TaskExecutor
	WorktreeMgr *git.WorktreeManager
	CommitQueue *exec.CommitQueue
	SandboxMgr  taskSandboxOps
}

// NewTaskHandler creates a new TaskHandler.
func NewTaskHandler(deps TaskDeps) *TaskHandler {
	return &TaskHandler{
		store:          deps.Store,
		boardStreamMgr: deps.BoardStream,
		configStore:    deps.ConfigStore,
		terminalMgr:    deps.TerminalMgr,
		executor:       deps.Executor,
		worktreeMgr:    deps.WorktreeMgr,
		commitQueue:    deps.CommitQueue,
		sandboxMgr:     deps.SandboxMgr,
	}
}

// refreshTaskPorts updates AssignedPorts from the sandbox container.
func (h *TaskHandler) refreshTaskPorts(task *types.Task) {
	if h.sandboxMgr == nil {
		return
	}
	ports, err := h.sandboxMgr.GetContainerPorts(task.ID)
	if err == nil && ports != nil {
		task.AssignedPorts = ports
	}
}

// EnrichWorktreeExists sets WorktreeExists field based on filesystem state.
func EnrichWorktreeExists(tasks []types.Task, workspaceDir string) {
	for i := range tasks {
		path := filepath.Join(workspaceDir, tasks[i].ProjectID, tasks[i].ID)
		_, err := os.Stat(path)
		tasks[i].WorktreeExists = err == nil
	}
}

// enrichTaskWorktree sets WorktreeExists for one task.
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
			EnrichWorktreeExists(tasks, h.configStore.WorkspaceDir())
		}
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
		var clientToken string
		if _, err := rawBody.decode("client_token", &clientToken); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		// A create whose answer was lost is answered by the task that exists; inserting it again would make two
		// cards for one click.
		if existing, claimed := h.store.ClaimedCreate(clientToken, projectID); claimed {
			claimedTask, err := h.store.GetTask(projectID, existing)
			if err != nil {
				httputil.Fail(w, err, "get created task", "task not found")
				return
			}
			httputil.JSONResponse(w, http.StatusCreated, claimedTask)
			return
		}
		// A mode nothing dispatches on must not reach the row: the tool-set switch falls through
		// to act, so the task would be offered write tools while reading as something else.
		if _, valid := types.ParseOperatingMode(string(task.OperatingMode)); task.OperatingMode != "" && !valid {
			httputil.JSONError(w, http.StatusBadRequest, "unknown operating mode")
			return
		}
		// plan_mode is the retired boolean spelling of operating_mode. Two spellings deciding one
		// mode is how the older one wins over an explicit operating_mode in the same body, so a
		// request that still carries it is refused rather than reinterpreted.
		if rawBody.has("plan_mode") {
			httputil.JSONError(w, http.StatusBadRequest, "plan_mode is not a task field: send operating_mode (act, plan or explore)")
			return
		}
		task.ID = ""
		task.ProjectID = projectID
		// The sandbox layer records these as a container is removed; a body cannot produce them.
		task.SandboxMemoryPeakMB = 0
		task.SandboxOOMKills = 0
		task.MemoryAlertAck = nil
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
				skipFields := make(map[string]struct{})
				for _, k := range []string{"auto_commit", "auto_merge", "subtasks_auto_merge"} {
					if rawBody.has(k) {
						skipFields[k] = struct{}{}
					}
				}
				var parentTask *types.Task
				if task.ParentTaskID != "" {
					parentTask, _ = h.store.GetTask(projectID, task.ParentTaskID)
				}
				types.ApplyTaskCreationDefaults(&task, cfg, parentTask, skipFields)

				if !rawBody.has("operating_mode") {
					if cfg.DefaultOperatingMode != nil {
						task.OperatingMode = *cfg.DefaultOperatingMode
					}
				}
				if task.OperatingMode == "" {
					task.OperatingMode = types.ModeAct
				}
				if cfg.DefaultMaxFailedTurns != nil && !rawBody.has("max_consecutive_failed_turns") {
					task.MaxConsecutiveFailedTurns = *cfg.DefaultMaxFailedTurns
				}
			}
		}
		if task.ParentTaskID != "" {
			parent, err := h.store.GetTask(projectID, task.ParentTaskID)
			if err != nil {
				httputil.JSONError(w, http.StatusBadRequest, "parent task not found")
				return
			}
			if task.ParentTaskID == task.ID {
				httputil.JSONError(w, http.StatusBadRequest, "task cannot be its own parent")
				return
			}
			task.Status = types.SubtaskStatusFor(parent.Status)
			if task.Status == types.StatusInProgress {
				task.ExecutionState = types.ExecStateIdle
			}
		}
		types.ClearSubtaskBaseBranch(&task)
		if isShortTitle(task.Prompt) {
			task.TitleGenerating = false
		} else {
			task.TitleGenerating = true
		}
		if err := h.store.CreateTask(&task); err != nil {
			httputil.Fail(w, err, "create task", "project not found")
			return
		}
		if err := h.store.ClaimWrite(clientToken, projectID, task.ID, store.ClaimCreateTask, task.ID); err != nil {
			logging.For(logging.HTTP).With(logging.TaskID, task.ID).Warnf("created task went unclaimed: %v", err)
		}
		// CreateThread mints the thread's ID into mainThread.
		mainThread := &types.TaskThread{
			TaskID:    task.ID,
			Name:      "Main",
			IsActive:  true,
			GitBranch: task.Branch,
			CreatedAt: now,
		}
		if err := h.store.CreateThread(mainThread); err != nil {
			httputil.Fail(w, err, "create default thread", "task not found")
			return
		}
		task.CurrentThreadID = mainThread.ID
		if err := h.store.UpdateTask(&task); err != nil {
			httputil.Fail(w, err, "update task", "task not found")
			return
		}
		if task.Prompt != "" {
			tm := &types.TaskMessage{
				ID:        uuid.New().String(),
				TaskID:    task.ID,
				ThreadID:  mainThread.ID,
				Role:      "user",
				Content:   task.Prompt,
				CreatedAt: now,
			}
			if attachments != "" {
				tm.Attachments = attachments
			}
			_ = h.store.CreateTaskMessage(tm)
		}
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
			enrichTaskWorktree(task, h.configStore.WorkspaceDir())
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
		types.ClearSubtaskBaseBranch(task)
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
			enrichTaskWorktree(task, h.configStore.WorkspaceDir())
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

// HandleSync returns a digest of task state for synchronization.
func (h *TaskHandler) HandleSync(w http.ResponseWriter, r *http.Request) {
	if err := types.ValidateTaskID(r.PathValue("taskID")); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	task, ok := resolveTask(w, r, h.store)
	if !ok {
		return
	}
	digest, err := h.syncDigest(task)
	if err != nil {
		httputil.Fault(w, "count task messages", err)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	httputil.JSONResponse(w, http.StatusOK, digest)
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
		wasTurn := task.ExecutionState.InFlight() || task.ExecutionState == types.ExecStatePaused
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
			logging.For(logging.Startup).With(logging.TaskID, taskID).Infof("children unreadable — not auto-starting: %v", childErr)
		} else {
			out := h.startUnderSchedule(projectID, task, children)
			switch {
			case len(out.startedChildren) > 0:
				started = true
			case out.childStartErr != nil:
				// All subtask starts failed — keep task in_progress (user's
				// explicit action); record error for visibility.
				task.ExecutionState = types.ExecStateIdle
				task.Output = out.childStartErr.Error()
			case out.selfStartErr != nil:
				logging.For(logging.Startup).With(logging.TaskID, taskID).Warnf("start failed: %v", out.selfStartErr)
				task.ExecutionState = types.ExecStateIdle
				task.Status = originalStatus
				task.Output = out.selfStartErr.Error()
			default:
				// Nothing failed: either this row's own turn started, or its children are
				// still finishing (review/failed) and it rests started-but-pending
				// (allowed Pending: waiting on the children).
				started = out.startedSelf
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

// moveAllDescendantsToInProgress recursively moves descendants to in_progress/idle.
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

// moveAllDescendants recursively moves descendants to target status, leaves first.
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
		if d.ExecutionState.InFlight() {
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

// unfinishedDescendants counts descendants not yet done.
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

// checkAncestorInProgress checks if any ancestor is In Progress.
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

// propagateSubtasksAutoMerge recursively sets auto_merge on descendants.
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

// resolveGitAuthor returns effective git author identity.
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

// HandleCommitPreviewStream streams commit message generation via SSE.
func (h *TaskHandler) HandleCommitPreviewStream(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	sse, ok := httputil.NewSSE(w)
	if !ok {
		httputil.JSONError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	task, err := h.store.GetTask(projectID, taskID)
	if err != nil {
		sse.EventJSON("error", map[string]string{"error": "task not found"})
		return
	}

	// Send metadata immediately (author + merge target)
	authorName, authorEmail := resolveGitAuthor(projectID, h.store)
	mergeTarget := h.executor.ResolveMergeTarget(projectID, task)
	sse.EventJSON("metadata", map[string]string{
		"author_name":  authorName,
		"author_email": authorEmail,
		"merge_target": mergeTarget,
	})

	h.executor.StreamCommitMessage(sse, taskID, task)
}

// handConflictToAgent handles merge conflicts by flagging the task and triggering the LLM.
func (h *TaskHandler) handConflictToAgent(w http.ResponseWriter, projectID, taskID string, task *types.Task) {
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
		ReviewContext:   h.executor.ReviewContextFor(task),
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
}

// HandleCommitAndDone commits work, merges to origin, and marks task Done.
func (h *TaskHandler) HandleCommitAndDone(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	task, ok := resolveTaskWithWorktree(w, r, h.store, h.worktreeMgr)
	if !ok {
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
	// A conflict left in the worktree is work the click asked for, not a reason to say no. It goes to
	// the resolver like any other; committing it instead would mean `git add`, which marks it resolved.
	var commitOut string
	var commitErr error
	if worktreeExists {
		if unmerged := h.worktreeMgr.StageIndexedFiles(taskID); len(unmerged) > 0 {
			unlockWorktree()
			logging.For(logging.Merge).With(logging.TaskID, taskID).Warnf("worktree has %d unmerged file(s) at commit: %s", len(unmerged), strings.Join(unmerged, ", "))
			if err := h.store.SetTaskMergeConflict(taskID, projectID, true); err != nil {
				httputil.Fail(w, err, "flag merge conflict", "task not found")
				return
			}
			conflicted, err := h.store.GetTask(projectID, taskID)
			if err != nil {
				httputil.Fail(w, err, "reload task", "task not found")
				return
			}
			h.handConflictToAgent(w, projectID, taskID, conflicted)
			return
		}
		commitMsg := body.CommitMessage
		if commitMsg == "" {
			commitMsg = h.executor.GenerateCommitMessage(r.Context(), task)
		}
		// --allow-empty is what lets a task whose work is already committed still reach Done.
		commitOut, commitErr = h.worktreeMgr.GitRunInWorktreeOutput(taskID, append(git.GitAuthorFlags(), "commit", "-m", commitMsg, "--allow-empty")...)
	}
	unlockWorktree()

	// A refused commit is not a no-op: merging on past it marks the task Done and deletes the
	// worktree holding the work.
	if commitErr != nil {
		httputil.Fail(w, errs.Operation("commit staged work", fmt.Errorf("%s (%v)", strings.TrimSpace(commitOut), commitErr)), "commit staged work", "task not found")
		return
	}

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

	// The click asked for commit + merge + done, and a landed merge delivered it — see MergedDelivered.
	if mergeErr := h.executor.MergeBranch(projectID, taskID, task); !exec.MergedDelivered(mergeErr) {
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
		h.handConflictToAgent(w, projectID, taskID, task)
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

// taskTreeDeleter is the interface for task tree deletion.
type taskTreeDeleter interface {
	deleteTaskTrees(projectID string, taskIDs ...string)
}

// deleteTaskTrees deletes task and its subtree, then prunes orphans.
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

// broadcastTaskDeleted broadcasts task_deleted event for task and descendants.
func (h *TaskHandler) broadcastTaskDeleted(projectID, taskID string, descendants []types.Task) {
	for _, d := range descendants {
		h.boardStreamMgr.BroadcastTaskDeleted(projectID, d.ID)
	}
	h.boardStreamMgr.BroadcastTaskDeleted(projectID, taskID)
}

// cleanupTask tears down a task and its descendants.
func (h *TaskHandler) cleanupTask(taskID string, descendants []types.Task) {
	// Reverse of the depth-first order: subtasks before their parent.
	for i := len(descendants) - 1; i >= 0; i-- {
		h.cleanupSingleTask(descendants[i].ID)
	}
	h.cleanupSingleTask(taskID)
}

// cleanupSingleTask cleans up a single task.
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

// startTitleGeneration spawns goroutine to generate task title via LLM.
func (h *TaskHandler) startTitleGeneration(projectID, taskID, prompt string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// A handler can exist without an executor, as guarded everywhere else in this file.
		// finishTitleGeneration still has to run, or the task shows "title coming" forever.
		if h.executor == nil {
			logging.For(logging.Generate).With(logging.TaskID, taskID).Infof("skipping: no executor configured")
			h.finishTitleGeneration(projectID, taskID)
			return
		}

		// Resolve the LLM config for title generation through the configured
		// Utility provider/model, falling back to the task's own values when
		// Utility is unset (same routing as commit messages, checkpoint labels,
		// and summarization).
		task, err := h.store.GetTask(projectID, taskID)
		if err != nil {
			logging.For(logging.Generate).With(logging.TaskID, taskID).Infof("skipping: %v", err)
			h.finishTitleGeneration(projectID, taskID)
			return
		}
		serverURL, modelName := h.executor.ResolveUtilityTarget(task)
		if serverURL == "" || modelName == "" {
			logging.For(logging.Generate).With(logging.TaskID, taskID).Infof("skipping: no LLM config on task")
			h.finishTitleGeneration(projectID, taskID)
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
			h.finishTitleGeneration(projectID, taskID)
			return
		}

		if title == "" {
			logging.For(logging.Generate).With(logging.TaskID, taskID).Infof("empty title from LLM")
			h.finishTitleGeneration(projectID, taskID)
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

// finishTitleGeneration sets title_generating to false.
func (h *TaskHandler) finishTitleGeneration(projectID, taskID string) {
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
	if updated, err := h.store.GetTask(projectID, taskID); err == nil {
		h.boardStreamMgr.BroadcastTaskUpdated(projectID, updated)
	}
}

// HandleInit initializes worktree and branch for a task.
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
	if h.worktreeMgr != nil && h.configStore != nil {
		enrichTaskWorktree(task, h.configStore.WorkspaceDir())
	}
	h.refreshTaskPorts(task)
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// HandleStart starts task and its descendants via the schedule.
func (h *TaskHandler) HandleStart(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	task, ok := resolveTask(w, r, h.store)
	if !ok {
		return
	}

	children, err := h.store.GetChildren(taskID)
	if err != nil {
		httputil.Fail(w, err, "list subtasks", "task not found")
		return
	}
	// User explicitly started this task: it goes to In Progress (started-but-pending) before
	// anything under it runs, so the board shows the row the click named.
	if len(children) > 0 && task.Status == types.StatusBacklog {
		task.Status = types.StatusInProgress
		task.ExecutionState = types.ExecStateIdle
		task.UpdatedAt = time.Now().UTC()
		if err := h.store.UpdateTask(task); err != nil {
			httputil.Fail(w, err, "update task", "task not found")
			return
		}
		h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	}

	out := h.startUnderSchedule(projectID, task, children)
	if len(out.startedChildren) > 0 {
		h.respondWithStartedTask(w, projectID, out.startedChildren[len(out.startedChildren)-1])
		return
	}
	if out.childStartErr != nil {
		httputil.Fail(w, out.childStartErr, "start subtask", "subtask not found")
		return
	}
	if out.selfStartErr != nil {
		// Already-running arrives as errs.ErrConflict and answers 409 with its own sentence;
		// a genuine fault answers 500 without echoing its text.
		httputil.Fail(w, out.selfStartErr, "start task", "task not found")
		return
	}
	// Nothing started and nothing failed: the row rests on children that are still finishing.
	task, _ = h.store.GetTask(projectID, taskID)
	h.refreshTaskPorts(task)
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// HandleStop pauses task and its descendants.
func (h *TaskHandler) HandleStop(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")

	if err := h.executor.PauseTaskAndDescendants(taskID); err != nil {
		// The store's missing-row error is the 404; a database fault answers 500.
		httputil.Fail(w, err, "stop task", "task not found")
		return
	}

	task, _ := h.store.GetTask(projectID, taskID)
	h.refreshTaskPorts(task)
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// HandlePrioritize prioritizes task in provider queue.
func (h *TaskHandler) HandlePrioritize(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	if _, ok := resolveTask(w, r, h.store); !ok {
		return
	}
	httputil.JSONResponse(w, http.StatusOK, map[string]any{"promoted": h.executor.PrioritizeQueuedTask(taskID)})
}

// HandleDismissMemoryAlert dismisses memory alert on task row.
func (h *TaskHandler) HandleDismissMemoryAlert(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	// Decode before touching a task. A missing number would under-cover the alert, so all three are required.
	var raw fieldsPresent
	if !httputil.DecodeJSON(w, r, &raw) {
		return
	}
	var ack types.MemoryAlertAck
	for _, f := range []struct {
		key    string
		target *int64
	}{{"peak_mb", &ack.PeakMB}, {"oom_kills", &ack.OOMKills}, {"limit_mb", &ack.LimitMB}} {
		sent, err := raw.decode(f.key, f.target)
		switch {
		case err != nil:
			httputil.JSONError(w, http.StatusBadRequest, err.Error())
			return
		case !sent:
			httputil.JSONError(w, http.StatusBadRequest, f.key+" is required")
			return
		case *f.target < 0:
			httputil.JSONError(w, http.StatusBadRequest, f.key+" must not be negative")
			return
		}
	}
	task, ok := resolveTask(w, r, h.store)
	if !ok {
		return
	}
	if err := h.store.DismissTaskMemoryAlert(taskID, ack.PeakMB, ack.OOMKills, ack.LimitMB); err != nil {
		httputil.Fail(w, err, "dismiss memory alert", "task not found")
		return
	}
	task.MemoryAlertAck = &ack
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// startOutcome is what startUnderSchedule decided, so each entry point renders its own answer.
type startOutcome struct {
	// startedChildren are the descendants that started, deepest last; startedSelf is this row's own
	// turn running. No fault and neither set means the row rests on children still finishing, which
	// is a state, not a failure.
	startedChildren []string
	startedSelf     bool
	// A descendant that would not start and this row's own turn that would not start are two
	// faults, because the routes answer them with different sentences.
	childStartErr error
	selfStartErr  error
}

// startUnderSchedule starts what the schedule picks under a row: the descendants that are ready to
// run, or the row's own turn once nothing under it is left to run. Starting a row, retrying it and
// dragging it to In Progress all ask for this one thing, so the choice has one home; what each of
// them answers, and what a fault costs the row, stays with the entry point. The caller reads the
// child list, because it decides what an unreadable one means before anything starts.
//
// A row with a parent starts through StartSubtask, the starter that takes the parent's model config
// with it: a child written by plan-mode decomposition arrives without one, so which route the click
// arrived through would decide whether the row can run at all. backend/api/task_test.go holds that.
func (h *TaskHandler) startUnderSchedule(projectID string, task *types.Task, children []types.Task) startOutcome {
	var out startOutcome
	if len(children) > 0 {
		// The work is in these children, so they go first and this row waits for them.
		h.moveAllDescendantsToInProgress(task.ID)
		started, err := h.executor.StartReadySubtasks(projectID, task.ID)
		if len(started) > 0 {
			logging.For(logging.Startup).With(logging.TaskID, task.ID).Infof("started %d subtask(s): %v", len(started), started)
			out.startedChildren = started
			return out
		}
		if err != nil {
			out.childStartErr = err
			return out
		}
		if !exec.ChildrenAllDone(children) {
			return out
		}
	}

	var err error
	if task.ParentTaskID != "" {
		err = h.executor.StartSubtask(projectID, task.ID, false)
	} else {
		err = h.executor.StartTask(projectID, task.ID, false)
	}
	if err != nil {
		out.selfStartErr = err
		return out
	}
	out.startedSelf = true
	logging.For(logging.Startup).With(logging.TaskID, task.ID).Infof("started")
	return out
}

// HandleStartSubtask starts a subtask: the descendants the schedule picks under it run first, and
// the row starts its own turn once nothing under it is left to run.
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
	out := h.startUnderSchedule(projectID, task, children)
	if len(out.startedChildren) > 0 {
		h.respondWithStartedTask(w, projectID, out.startedChildren[len(out.startedChildren)-1])
		return
	}
	if out.childStartErr != nil {
		httputil.Fail(w, out.childStartErr, "start subtask", "task not found")
		return
	}
	if out.selfStartErr != nil {
		httputil.Fail(w, out.selfStartErr, "start subtask", "task not found")
		return
	}
	// Nothing started and nothing failed: the row rests on children that are still finishing.
	task, _ = h.store.GetTask(projectID, taskID)
	h.refreshTaskPorts(task)
	h.boardStreamMgr.BroadcastTaskUpdated(projectID, task)
	httputil.JSONResponse(w, http.StatusOK, task)
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
