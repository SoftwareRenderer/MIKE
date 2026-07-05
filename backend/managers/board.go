package managers

import (
	"encoding/json"
	"net/http"

	"kanban/backend/config"
	"kanban/backend/httputil"
	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/types"
)

// BoardHandler handles board-level operations.
type BoardHandler struct {
	store          *store.Store
	boardStreamMgr *stream.AppStreamManager
	configStore    *config.ConfigStore
	sandboxMgr     sandboxOps
}

// sandboxOps is the subset of sandbox.SandboxManager methods used for port refresh.
type sandboxOps interface {
	GetContainerPorts(taskID string) ([]types.AssignedPort, error)
}

// NewBoardHandler creates a new BoardHandler.
func NewBoardHandler(store *store.Store, boardStreamMgr *stream.AppStreamManager, configStore *config.ConfigStore, sandboxMgr sandboxOps) *BoardHandler {
	return &BoardHandler{store: store, boardStreamMgr: boardStreamMgr, configStore: configStore, sandboxMgr: sandboxMgr}
}

// HandleGetBoard returns the board (all tasks) for a project.
func (h *BoardHandler) HandleGetBoard(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")

	if _, err := h.store.GetProject(projectID); err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}

	tasks, err := h.store.GetTasksByProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to get tasks")
		return
	}
	if tasks == nil {
		tasks = []types.Task{}
	}

	// Enrich tasks with worktree existence info
	if h.configStore != nil {
		if cfg, err := h.configStore.GetConfig(); err == nil {
			EnrichWorktreeExists(tasks, cfg.WorkspaceDir)
		}
	}

	// Refresh assigned ports from live sandbox containers
	if h.sandboxMgr != nil {
		for i := range tasks {
			ports, err := h.sandboxMgr.GetContainerPorts(tasks[i].ID)
			if err == nil && ports != nil {
				tasks[i].AssignedPorts = ports
			}
		}
	}

	httputil.JSONResponse(w, http.StatusOK, types.Board{Tasks: tasks})
}

// HandleReorder bulk-reorders tasks within columns.
func (h *BoardHandler) HandleReorder(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")

	if _, err := h.store.GetProject(projectID); err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}

	var reorders []types.ReorderRequest
	if err := json.NewDecoder(r.Body).Decode(&reorders); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := h.store.ReorderTasks(projectID, reorders); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to reorder tasks")
		return
	}

	h.boardStreamMgr.Broadcast(projectID)
	httputil.JSONResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}
