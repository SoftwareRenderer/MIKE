package api

import (
	"net/http"

	"mike/backend/config"
	"mike/backend/httputil"
	"mike/backend/store"
	"mike/backend/stream"
	"mike/backend/types"
)

// BoardHandler handles board-level operations.
type BoardHandler struct {
	store       *store.Store
	configStore *config.ConfigStore
	sandboxMgr  sandboxOps
}

// sandboxOps is the subset of sandbox.SandboxManager methods used for port refresh.
type sandboxOps interface {
	GetContainerPorts(taskID string) ([]types.AssignedPort, error)
}

// NewBoardHandler creates a new BoardHandler.
func NewBoardHandler(store *store.Store, configStore *config.ConfigStore, sandboxMgr sandboxOps) *BoardHandler {
	return &BoardHandler{store: store, configStore: configStore, sandboxMgr: sandboxMgr}
}

// HandleBoard returns the board (all tasks) for a project.
func (h *BoardHandler) HandleBoard(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")

	if _, err := h.store.GetProject(projectID); err != nil {
		httputil.Fail(w, err, "get project", "project not found")
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

	// The board is the row the browser renders, so it is the same projection the task_updated frame
	// carries — including no agent result text. A single task read still answers in full.
	httputil.JSONResponse(w, http.StatusOK, types.Board{Tasks: stream.BrowserRows(tasks)})
}
