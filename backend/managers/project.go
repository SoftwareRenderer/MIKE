package managers

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"kanban/backend/config"
	"kanban/backend/git"
	"kanban/backend/httputil"
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
}

// NewProjectHandler creates a new ProjectHandler.
func NewProjectHandler(store *store.Store, configStore *config.ConfigStore, worktreeMgr *git.WorktreeManager, terminalMgr *term.TerminalManager) *ProjectHandler {
	return &ProjectHandler{store: store, configStore: configStore, worktreeMgr: worktreeMgr, terminalMgr: terminalMgr}
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
	p := &types.Project{
		ID:             GenerateProjectID(req.Path, existingIDs),
		Name:           name,
		Path:           req.Path,
		SandboxEnabled: req.SandboxEnabled,
		CreatedAt:      now,
		UpdatedAt:      now,
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
			if h.worktreeMgr != nil {
				_ = h.worktreeMgr.RemoveWorktreeDir(id, task.ID)
			}
			if h.terminalMgr != nil {
				h.terminalMgr.RemoveSession(task.ID)
			}
		}
		// Clean up project-level terminal session
		if h.terminalMgr != nil {
			h.terminalMgr.RemoveSession("proj:" + id)
		}
		if err := h.store.DeleteProject(id); err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, "failed to delete project")
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
