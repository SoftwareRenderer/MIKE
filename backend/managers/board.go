package managers

import (
	"net/http"

	"kanban/backend/config"
	"kanban/backend/httputil"
	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/types"
)

// boardStore is the board's whole persistence need: one project and its cards.
type boardStore interface {
	GetProject(id string) (*types.Project, error)
	GetTasksByProject(projectID string) ([]types.Task, error)
}

var _ boardStore = (*store.Store)(nil)

// BoardHandler handles board-level operations.
type BoardHandler struct {
	store          boardStore
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

	httputil.JSONResponse(w, http.StatusOK, types.Board{Tasks: tasks})
}
