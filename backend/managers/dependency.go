package managers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"kanban/backend/httputil"
	"kanban/backend/store"
	"kanban/backend/types"

	"github.com/google/uuid"
)

// DependencyHandler handles task dependency operations.
type DependencyHandler struct {
	store *store.Store
}

// NewDependencyHandler creates a new DependencyHandler.
func NewDependencyHandler(store *store.Store) *DependencyHandler {
	return &DependencyHandler{store: store}
}

// HandleDependencies lists or creates dependencies for a task.
func (h *DependencyHandler) HandleDependencies(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	switch r.Method {
	case http.MethodGet:
		deps, err := h.store.GetDependents(taskID)
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		httputil.JSONResponse(w, http.StatusOK, deps)
	case http.MethodPost:
		var req types.CreateDependencyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		dep := &types.TaskDependency{
			ID:           uuid.New().String(),
			ParentTaskID: taskID,
			ChildTaskID:  req.ChildTaskID,
			AutoStart:    req.AutoStart,
			CreatedAt:    time.Now().UTC(),
		}
		if err := h.store.CreateDependency(dep); err != nil {
			if strings.Contains(err.Error(), "circular") {
				httputil.JSONError(w, http.StatusConflict, err.Error())
			} else {
				httputil.JSONError(w, http.StatusInternalServerError, err.Error())
			}
			return
		}
		httputil.JSONResponse(w, http.StatusCreated, dep)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleDependency handles DELETE for a specific dependency.
func (h *DependencyHandler) HandleDependency(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	childTaskID := r.PathValue("childID")
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := h.store.DeleteDependency(taskID, childTaskID); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleDependents lists tasks that depend on this task.
func (h *DependencyHandler) HandleDependents(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	deps, err := h.store.GetDependencies(taskID)
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httputil.JSONResponse(w, http.StatusOK, deps)
}
