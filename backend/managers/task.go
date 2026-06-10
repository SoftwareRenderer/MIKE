package managers

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"kanban/backend/config"
	"kanban/backend/exec"
	"kanban/backend/git"
	"kanban/backend/httputil"
	"kanban/backend/stream"
	"kanban/backend/store"
	"kanban/backend/term"
	"kanban/backend/types"
)

// TaskHandler handles task CRUD operations.
type TaskHandler struct {
	store          *store.Store
	boardStreamMgr *stream.BoardStreamManager
	configStore    *config.ConfigStore
	terminalMgr    *term.TerminalManager
	executor       *exec.TaskExecutor
	worktreeMgr    *git.WorktreeManager
}

// NewTaskHandler creates a new TaskHandler.
func NewTaskHandler(store *store.Store, boardStreamMgr *stream.BoardStreamManager, configStore *config.ConfigStore, terminalMgr *term.TerminalManager, executor *exec.TaskExecutor, worktreeMgr *git.WorktreeManager) *TaskHandler {
	return &TaskHandler{store: store, boardStreamMgr: boardStreamMgr, configStore: configStore, terminalMgr: terminalMgr, executor: executor, worktreeMgr: worktreeMgr}
}

// HandleList lists or creates tasks for a project.
func (h *TaskHandler) HandleList(w http.ResponseWriter, r *http.Request, projectID string) {
	switch r.Method {
	case http.MethodGet:
		tasks, err := h.store.GetTasksByProject(projectID)
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		httputil.JSONResponse(w, http.StatusOK, tasks)
	case http.MethodPost:
		body, _ := io.ReadAll(r.Body)
		var task types.Task
		if err := json.Unmarshal(body, &task); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		task.ID = generateTaskID()
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
				if cfg.DefaultAutoCommit != nil && task.AutoCommit == false {
					task.AutoCommit = *cfg.DefaultAutoCommit
				}
				if cfg.DefaultPlanMode != nil && task.PlanMode == false {
					task.PlanMode = *cfg.DefaultPlanMode
				}
				if cfg.DefaultMaxFailedTurns != nil && task.MaxConsecutiveFailedTurns == 0 {
					task.MaxConsecutiveFailedTurns = *cfg.DefaultMaxFailedTurns
				}
			}
		}
		if err := h.store.CreateTask(&task); err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		h.boardStreamMgr.Broadcast(projectID)
		httputil.JSONResponse(w, http.StatusCreated, task)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleByID handles GET/PUT/DELETE for a single task.
func (h *TaskHandler) HandleByID(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	switch r.Method {
	case http.MethodGet:
		task, err := h.store.GetTask(projectID, taskID)
		if err != nil {
			httputil.JSONError(w, http.StatusNotFound, "task not found")
			return
		}
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
		if err := h.store.UpdateTask(task); err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		h.boardStreamMgr.Broadcast(projectID)
		httputil.JSONResponse(w, http.StatusOK, task)
	case http.MethodDelete:
		if _, err := h.store.GetTask(projectID, taskID); err != nil {
			httputil.JSONError(w, http.StatusNotFound, "task not found")
			return
		}
		h.cleanupTask(taskID)
		h.boardStreamMgr.Broadcast(projectID)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleClearDone deletes all tasks with status 'done' for a project.
func (h *TaskHandler) HandleClearDone(w http.ResponseWriter, r *http.Request, projectID string) {
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
		h.cleanupTask(taskID)
		deleted++
	}

	h.boardStreamMgr.Broadcast(projectID)
	httputil.JSONResponse(w, http.StatusOK, map[string]interface{}{"deleted": deleted})
}

// HandleMove moves a task to a different column.
func (h *TaskHandler) HandleMove(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
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
	task.Status = req.Status
	task.UpdatedAt = time.Now()

	if req.Status == "in_progress" && task.StatusState != "running" {
		if err := h.executor.StartTask(projectID, taskID); err != nil {
			log.Printf("[move] task=%s auto-start failed: %v", taskID, err)
			task.StatusState = "failed"
			task.Output = err.Error()
		}
	}
	if req.Status == "done" {
		task.StatusState = "completed"
	}
	if err := h.store.UpdateTask(task); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.boardStreamMgr.Broadcast(projectID)
	httputil.JSONResponse(w, http.StatusOK, task)
}

// cleanupTask performs all cleanup steps for a deleted task.
func (h *TaskHandler) cleanupTask(taskID string) {
	if h.executor != nil {
		_ = h.executor.StopTask(taskID)
		h.executor.RemoveStream(taskID)
	}
	if h.worktreeMgr != nil {
		// Best-effort, find project from task
		task, err := h.store.GetTaskByTaskID(taskID)
		if err == nil {
			if err := h.worktreeMgr.RemoveWorktreeDir(task.ProjectID, taskID); err != nil {
				log.Printf("task delete: worktree cleanup failed for %s: %v", taskID, err)
			}
		}
	}
	h.terminalMgr.RemoveSession(taskID)
	// Delete from database - find project first
	task, err := h.store.GetTaskByTaskID(taskID)
	if err == nil {
		_ = h.store.DeleteTask(task.ProjectID, taskID)
	}
}
