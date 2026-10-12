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

// sandboxOps interface for container port refresh.
type sandboxOps interface {
	GetContainerPorts(taskID string) ([]types.AssignedPort, error)
}

func NewBoardHandler(store *store.Store, configStore *config.ConfigStore, sandboxMgr sandboxOps) *BoardHandler {
	return &BoardHandler{store: store, configStore: configStore, sandboxMgr: sandboxMgr}
}

// HandleBoard returns board (all tasks) for a project.
func (h *BoardHandler) HandleBoard(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	if _, ok := resolveProject(w, r, h.store); !ok {
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

	// enrich tasks with worktree existence.
	if h.configStore != nil {
		if cfg, err := h.configStore.GetConfig(); err == nil {
			EnrichWorktreeExists(tasks, cfg.WorkspaceDir)
		}
	}

	// refresh ports from live sandboxes.
	if h.sandboxMgr != nil {
		for i := range tasks {
			ports, err := h.sandboxMgr.GetContainerPorts(tasks[i].ID)
			if err == nil && ports != nil {
				tasks[i].AssignedPorts = ports
			}
		}
	}

	// returns browser-ready task rows.
	httputil.JSONResponse(w, http.StatusOK, types.Board{Tasks: stream.BrowserRows(tasks)})
}
