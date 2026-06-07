package backend

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ProjectManager handles project CRUD operations.
type ProjectManager struct {
	store *Store
}

// NewProjectManager creates a new ProjectManager.
func NewProjectManager(store *Store) *ProjectManager {
	return &ProjectManager{store: store}
}

// HandleList returns all projects.
func (pm *ProjectManager) HandleList(w http.ResponseWriter, r *http.Request) {
	projects, err := pm.store.ListProjects()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "failed to list projects")
		return
	}
	jsonResponse(w, http.StatusOK, projects)
}

// HandleCounts returns task counts (backlog, in_progress, review) for all projects.
func (pm *ProjectManager) HandleCounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	counts := pm.store.GetAllProjectCounts()
	jsonResponse(w, http.StatusOK, counts)
}

// HandleCreate creates a new project from a directory path.
func (pm *ProjectManager) HandleCreate(w http.ResponseWriter, r *http.Request) {
	var req CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Path == "" {
		jsonError(w, http.StatusBadRequest, "path is required")
		return
	}

	// Normalize path
	req.Path = strings.TrimSpace(req.Path)

	// Check path exists and is a directory
	info, err := os.Stat(req.Path)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "path does not exist: "+err.Error())
		return
	}
	if !info.IsDir() {
		jsonError(w, http.StatusBadRequest, "path is not a directory")
		return
	}

	// Check if it's a git repository
	if !isGitRepo(req.Path) {
		jsonError(w, http.StatusBadRequest, "path is not a git repository")
		return
	}

	// Derive name from path if not provided
	name := req.Name
	if name == "" {
		name = basename(req.Path)
	}

	now := time.Now().UTC()
	p := &Project{
		ID:        uuid.New().String(),
		Name:      name,
		Path:      req.Path,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := pm.store.CreateProject(p); err != nil {
		jsonError(w, http.StatusInternalServerError, "failed to create project: "+err.Error())
		return
	}
	jsonResponse(w, http.StatusCreated, p)
}

// HandleByID handles GET, PUT, DELETE for a single project.
func (pm *ProjectManager) HandleByID(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		p, err := pm.store.GetProject(id)
		if err != nil {
			jsonError(w, http.StatusNotFound, "project not found")
			return
		}
		jsonResponse(w, http.StatusOK, p)

	case http.MethodPut:
		var req UpdateProjectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Name == "" {
			jsonError(w, http.StatusBadRequest, "name is required")
			return
		}
		p, err := pm.store.UpdateProject(id, req.Name)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "failed to update project")
			return
		}
		jsonResponse(w, http.StatusOK, p)

	case http.MethodDelete:
		if err := pm.store.DeleteProject(id); err != nil {
			jsonError(w, http.StatusInternalServerError, "failed to delete project")
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// isGitRepo checks if a directory contains a .git subdirectory.
func isGitRepo(path string) bool {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--git-dir")
	cmd.Dir = path
	err := cmd.Run()
	return err == nil
}

func basename(path string) string {
	if len(path) > 0 && (path[len(path)-1] == '/' || path[len(path)-1] == '\\') {
		path = path[:len(path)-1]
	}
	parts := strings.Split(path, "/")
	if last := parts[len(parts)-1]; last != "" {
		return last
	}
	parts = strings.Split(path, "\\")
	return parts[len(parts)-1]
}