package managers

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"kanban/backend/httputil"
	"kanban/backend/store"
	"kanban/backend/types"

	"github.com/google/uuid"
)

// ProjectHandler handles project CRUD operations.
type ProjectHandler struct {
	store *store.Store
}

// NewProjectHandler creates a new ProjectHandler.
func NewProjectHandler(store *store.Store) *ProjectHandler {
	return &ProjectHandler{store: store}
}

// HandleList returns all projects.
func (h *ProjectHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	projects, err := h.store.ListProjects()
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to list projects")
		return
	}
	httputil.JSONResponse(w, http.StatusOK, projects)
}

// HandleCounts returns task counts for all projects.
func (h *ProjectHandler) HandleCounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	counts := h.store.GetAllProjectCounts()
	httputil.JSONResponse(w, http.StatusOK, counts)
}

// HandleCreate creates a new project from a directory path.
func (h *ProjectHandler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	var req types.CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Path == "" {
		httputil.JSONError(w, http.StatusBadRequest, "path is required")
		return
	}

	req.Path = strings.TrimSpace(req.Path)

	info, err := os.Stat(req.Path)
	if err != nil {
		httputil.JSONError(w, http.StatusBadRequest, "path does not exist: "+err.Error())
		return
	}
	if !info.IsDir() {
		httputil.JSONError(w, http.StatusBadRequest, "path is not a directory")
		return
	}

	if !isGitRepo(req.Path) {
		httputil.JSONError(w, http.StatusBadRequest, "path is not a git repository")
		return
	}

	name := req.Name
	if name == "" {
		name = basename(req.Path)
	}

	now := time.Now().UTC()
	p := &types.Project{
		ID:        uuid.New().String(),
		Name:      name,
		Path:      req.Path,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := h.store.CreateProject(p); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to create project: "+err.Error())
		return
	}
	httputil.JSONResponse(w, http.StatusCreated, p)
}

// HandleByID handles GET, PUT, DELETE for a single project.
func (h *ProjectHandler) HandleByID(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		p, err := h.store.GetProject(id)
		if err != nil {
			httputil.JSONError(w, http.StatusNotFound, "project not found")
			return
		}
		httputil.JSONResponse(w, http.StatusOK, p)

	case http.MethodPut:
		var req types.UpdateProjectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Name == "" {
			httputil.JSONError(w, http.StatusBadRequest, "name is required")
			return
		}
		p, err := h.store.UpdateProject(id, req.Name)
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, "failed to update project")
			return
		}
		httputil.JSONResponse(w, http.StatusOK, p)

	case http.MethodDelete:
		if err := h.store.DeleteProject(id); err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, "failed to delete project")
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
