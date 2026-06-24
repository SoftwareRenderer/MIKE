package managers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kanban/backend/config"
	"kanban/backend/git"
	"kanban/backend/httputil"
	"kanban/backend/sandbox"
	"kanban/backend/store"
	"kanban/backend/term"
	"kanban/backend/types"
)

// ProjectHandler handles project CRUD operations.
type ProjectHandler struct {
	store       *store.Store
	configStore *config.ConfigStore
	worktreeMgr *git.WorktreeManager
	terminalMgr *term.TerminalManager
	sandboxMgr  *sandbox.SandboxManager
}

// NewProjectHandler creates a new ProjectHandler.
func NewProjectHandler(store *store.Store, configStore *config.ConfigStore, worktreeMgr *git.WorktreeManager, terminalMgr *term.TerminalManager, sandboxMgr *sandbox.SandboxManager) *ProjectHandler {
	return &ProjectHandler{store: store, configStore: configStore, worktreeMgr: worktreeMgr, terminalMgr: terminalMgr, sandboxMgr: sandboxMgr}
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

	if !IsGitRepo(req.Path) {
		httputil.JSONError(w, http.StatusBadRequest, "path is not a git repository")
		return
	}

	name := req.Name
	if name == "" {
		name = Basename(req.Path)
	}

	// Collect existing project IDs to avoid collisions
	existingIDs := []string{}
	projects, _ := h.store.ListProjects()
	for _, p := range projects {
		existingIDs = append(existingIDs, p.ID)
	}

	now := time.Now().UTC()
	memoryLimit := req.SandboxMemoryLimit
	if memoryLimit == 0 {
		memoryLimit = 1024 // default 1 GB
	}
	p := &types.Project{
		ID:                      GenerateProjectID(req.Path, existingIDs),
		Name:                    name,
		Path:                    req.Path,
		SandboxEnabled:          req.SandboxEnabled,
		SandboxNetworkIsolation: req.SandboxNetworkIsolation,
		SandboxImage:            req.SandboxImage,
		SandboxDockerfilePath:   req.SandboxDockerfilePath,
		SandboxMemoryLimit:      memoryLimit,
		CreatedAt:               now,
		UpdatedAt:               now,
	}

	if err := h.store.CreateProject(p); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to create project: "+err.Error())
		return
	}
	httputil.JSONResponse(w, http.StatusCreated, p)
}

// HandleStats returns aggregated token usage stats for a project.
func (h *ProjectHandler) HandleStats(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		stats, err := h.store.GetProjectStats(id)
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, "failed to get project stats")
			return
		}
		// Compute costs using provider pricing from config
		h.applyPricing(stats)
		httputil.JSONResponse(w, http.StatusOK, stats)

	case http.MethodDelete:
		modelName := r.URL.Query().Get("model_name")
		if modelName == "" {
			httputil.JSONError(w, http.StatusBadRequest, "model_name query parameter is required")
			return
		}
		providerName := r.URL.Query().Get("provider_name")
		err := h.store.DeleteProjectStats(id, modelName, providerName)
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, "failed to reset stats")
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// applyPricing computes cost for each model in stats using provider pricing from config.
// Cost formula (USD):
//   input_rate = cache_write_price if > 0, else input_price
//   cost = (processed_tokens * input_rate + cache_tokens * cache_read_price + generated_tokens * output_rate) / 1,000,000
func (h *ProjectHandler) applyPricing(stats *types.ProjectStats) {
	cfg, err := h.configStore.GetConfig()
	if err != nil {
		return // no config, skip pricing
	}

	// Build provider name -> provider map (match by both Name and DisplayName)
	providerMap := make(map[string]types.Provider)
	for _, p := range cfg.Providers {
		providerMap[p.Name] = p
		if p.DisplayName != "" && p.DisplayName != p.Name {
			providerMap[p.DisplayName] = p
		}
	}

	for i := range stats.ByModel {
		m := &stats.ByModel[i]
		p, ok := providerMap[m.ProviderName]
		if !ok {
			continue // no pricing configured for this provider
		}

		// Use cache_write_price for non-cached input if set, else input_price
		inputRate := p.InputPrice
		if p.CacheWritePrice > 0 {
			inputRate = p.CacheWritePrice
		}

		cost := (float64(m.ProcessedTokens)*inputRate +
			float64(m.CacheTokens)*p.CacheReadPrice +
			float64(m.GeneratedTokens)*p.OutputPrice) / 1_000_000

		m.Cost = cost
		stats.TotalCost += cost
	}
}

// HandleSandboxBuild builds a container image from the project's Dockerfile.
func (h *ProjectHandler) HandleSandboxBuild(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		httputil.JSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Get the project to find the Dockerfile path
	project, err := h.store.GetProject(id)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}
	if project.SandboxDockerfilePath == "" {
		httputil.JSONError(w, http.StatusBadRequest, "no Dockerfile path configured for this project")
		return
	}
	if h.sandboxMgr == nil {
		httputil.JSONError(w, http.StatusInternalServerError, "sandbox manager not available")
		return
	}

	if err := h.sandboxMgr.BuildImage(project.Name, project.SandboxDockerfilePath); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Remove terminal sessions so they pick up the new image
	if h.terminalMgr != nil {
		h.terminalMgr.RemoveProjectSessions(id)
	}

	httputil.JSONResponse(w, http.StatusOK, map[string]string{
		"status":  "built",
		"image":   sandbox.ProjectImageName(project.Name),
		"project": id,
	})
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
		// Partial update: apply only provided fields
		if req.Name != "" {
			if _, updateErr := h.store.UpdateProject(id, req.Name); updateErr != nil {
				httputil.JSONError(w, http.StatusInternalServerError, "failed to update project")
				return
			}
		}
		if req.SandboxEnabled != nil {
			if updateErr := h.store.UpdateProjectSandboxEnabled(id, *req.SandboxEnabled); updateErr != nil {
				httputil.JSONError(w, http.StatusInternalServerError, "failed to update sandbox setting")
				return
			}
			if h.terminalMgr != nil {
				h.terminalMgr.RemoveProjectSessions(id)
			}
		}
		if req.SandboxNetworkIsolation != nil {
			if updateErr := h.store.UpdateProjectSandboxNetworkIsolation(id, *req.SandboxNetworkIsolation); updateErr != nil {
				httputil.JSONError(w, http.StatusInternalServerError, "failed to update sandbox network isolation setting")
				return
			}
			if h.terminalMgr != nil {
				h.terminalMgr.RemoveProjectSessions(id)
			}
		}
		if req.SandboxImage != nil {
			if updateErr := h.store.UpdateProjectSandboxImage(id, *req.SandboxImage); updateErr != nil {
				httputil.JSONError(w, http.StatusInternalServerError, "failed to update sandbox image setting")
				return
			}
			if h.terminalMgr != nil {
				h.terminalMgr.RemoveProjectSessions(id)
			}
		}
		if req.SandboxDockerfilePath != nil {
			if updateErr := h.store.UpdateProjectSandboxDockerfilePath(id, *req.SandboxDockerfilePath); updateErr != nil {
				httputil.JSONError(w, http.StatusInternalServerError, "failed to update sandbox dockerfile path setting")
				return
			}
			if h.terminalMgr != nil {
				h.terminalMgr.RemoveProjectSessions(id)
			}
		}
		if req.SandboxMemoryLimit != nil {
			if updateErr := h.store.UpdateProjectSandboxMemoryLimit(id, *req.SandboxMemoryLimit); updateErr != nil {
				httputil.JSONError(w, http.StatusInternalServerError, "failed to update sandbox memory limit setting")
				return
			}
			if h.terminalMgr != nil {
				h.terminalMgr.RemoveProjectSessions(id)
			}
		}
		// Fetch latest state
		p, err := h.store.GetProject(id)
		if err != nil {
			httputil.JSONError(w, http.StatusNotFound, "project not found")
			return
		}
		httputil.JSONResponse(w, http.StatusOK, p)

	case http.MethodDelete:
		// Clean up worktrees and terminal sessions for all tasks before deleting the project
		tasks, err := h.store.GetTasksByProject(id)
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, "failed to list tasks for cleanup")
			return
		}
		for _, task := range tasks {
			if h.worktreeMgr != nil && task.Branch != "" {
				_ = h.worktreeMgr.RemoveWorktree(id, task.ID, task.Branch)
			}
			if h.terminalMgr != nil {
				h.terminalMgr.RemoveSession(task.ID)
			}
		}
		// Clean up project-level terminal session
		if h.terminalMgr != nil {
			h.terminalMgr.RemoveSession("proj:" + id)
		}
		// Clean up any project-level worktree directory (safety check)
		if h.worktreeMgr != nil {
			cfg, cfgErr := h.configStore.GetConfig()
			if cfgErr == nil {
				worktreesDir := cfg.WorkspaceDir
				if wtDir := os.Getenv("WORKTREES_DIR"); wtDir != "" {
					worktreesDir = wtDir
				}
				_ = os.RemoveAll(filepath.Join(worktreesDir, id))
			}
		}
		if err := h.store.DeleteProjectWithData(id); err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, "failed to delete project")
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
