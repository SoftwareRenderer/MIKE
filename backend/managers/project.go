package managers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"kanban/backend/config"
	"kanban/backend/exec"
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
	executor    *exec.TaskExecutor
}

// NewProjectHandler creates a new ProjectHandler.
func NewProjectHandler(store *store.Store, configStore *config.ConfigStore, worktreeMgr *git.WorktreeManager, terminalMgr *term.TerminalManager, sandboxMgr *sandbox.SandboxManager, taskExecutor *exec.TaskExecutor) *ProjectHandler {
	return &ProjectHandler{store: store, configStore: configStore, worktreeMgr: worktreeMgr, terminalMgr: terminalMgr, sandboxMgr: sandboxMgr, executor: taskExecutor}
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

	if !git.IsGitRepo(req.Path) {
		httputil.JSONError(w, http.StatusBadRequest, "path is not a git repository")
		return
	}

	name := req.Name
	if name == "" {
		name = filepath.Base(req.Path)
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
	// When sandbox is enabled, network isolation must be explicitly specified
	if req.SandboxEnabled && req.SandboxNetworkIsolation == nil {
		httputil.JSONError(w, http.StatusBadRequest, "sandbox_network_isolation is required when sandbox_enabled is true")
		return
	}
	networkIsolation := false
	if req.SandboxNetworkIsolation != nil {
		networkIsolation = *req.SandboxNetworkIsolation
	}
	p := &types.Project{
		ID:                      generateProjectID(req.Path, existingIDs),
		Name:                    name,
		Path:                    req.Path,
		SandboxEnabled:          req.SandboxEnabled,
		SandboxNetworkIsolation: networkIsolation,
		SandboxImage:            req.SandboxImage,
		SandboxDockerfilePath:   req.SandboxDockerfilePath,
		SandboxMemoryLimit:      memoryLimit,
		ForwardPorts:            req.SandboxForwardPorts,
		SandboxMounts:           req.SandboxMounts,
		SandboxEnvs:             req.SandboxEnvs,
		SandboxCapabilities:     req.SandboxCapabilities,
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
func (h *ProjectHandler) HandleStats(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("projectID")
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

// applyPricing computes cost (API + electricity) for each model in stats.
// Agent providers skip power/electricity — they report cost directly.
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

		apiCost := (float64(m.ProcessedTokens)*inputRate +
			float64(m.CacheTokens)*p.CacheReadPrice +
			float64(m.GeneratedTokens)*p.OutputPrice) / 1_000_000

		m.Cost = apiCost
		stats.TotalCost += apiCost

		// Power consumption and electricity cost (LLM providers only)
		if p.EffectiveKind() == types.ProviderKindLLM && p.PowerConsumptionW > 0 && m.PredictedMs > 0 {
			// predicted_ms is cumulative generation time; convert ms to hours
			powerKwh := p.PowerConsumptionW * float64(m.PredictedMs) / 3_600_000_000
			m.PowerKwh = powerKwh
			stats.TotalPowerKwh += powerKwh

			if p.ElectricityCostPerKwh > 0 {
				elecCost := powerKwh * p.ElectricityCostPerKwh
				m.Cost += elecCost
				stats.TotalCost += elecCost
			}
		}
	}
}

// HandleSandboxBuild builds a container image from the project's Dockerfile.
// Streams build progress as SSE and returns final JSON result.
func (h *ProjectHandler) HandleSandboxBuild(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("projectID")
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

	flusher, ok := w.(http.Flusher)
	if !ok {
		httputil.JSONError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	sendLine := func(line string) {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", line)
		flusher.Flush()
	}

	sendLine("Building sandbox image from Dockerfile...")

	err = h.sandboxMgr.BuildImageWithProgress(project.Name, project.SandboxDockerfilePath, func(line string) {
		sendLine(line)
	})

	if err != nil {
		sendLine("error: " + err.Error())
		return
	}

	// Stop all containers and terminal sessions so they pick up the new image
	tasks, _ := h.store.GetTasksByProject(id)
	taskIDs := make([]string, 0, len(tasks))
	for _, task := range tasks {
		taskIDs = append(taskIDs, task.ID)
	}
	if h.sandboxMgr != nil {
		h.sandboxMgr.StopProjectContainers(id, taskIDs)
	}
	if h.terminalMgr != nil {
		h.terminalMgr.RemoveProjectSessions(id)
	}

	// Final result as JSON
	result, _ := json.Marshal(map[string]string{
		"status":  "built",
		"image":   sandbox.ProjectImageName(project.Name),
		"project": id,
	})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", result)
	flusher.Flush()
}

// HandleByID handles GET, PUT, DELETE for a single project.
func (h *ProjectHandler) HandleByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("projectID")
	switch r.Method {
	case http.MethodGet:
		p, err := h.store.GetProject(id)
		if err != nil {
			httputil.JSONError(w, http.StatusNotFound, "project not found")
			return
		}
		// Add active terminal ports from project-level terminal
		if h.terminalMgr != nil {
			p.ActiveTerminalPorts = h.terminalMgr.GetProjectPorts(id)
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
		// Block sandbox setting changes while tasks are running in the project.
		// Modifying sandbox settings during execution would kill bridge processes
		// mid-tool-call, causing "broken pipe" errors.
		hasSandboxChange := req.SandboxEnabled != nil || req.SandboxNetworkIsolation != nil ||
			req.SandboxImage != nil || req.SandboxDockerfilePath != nil ||
			req.SandboxMemoryLimit != nil || req.SandboxForwardPorts != nil ||
			req.SandboxMounts != nil || req.SandboxEnvs != nil ||
			req.SandboxCapabilities != nil
		// Block sandbox setting changes if any task that is actually "In Progress" is running.
		// If no "In Progress" tasks are running, we can safely stop any other running executors
		// (which are stale) and apply the settings.
		if hasSandboxChange && h.executor != nil {
			if h.executor.HasRunningTasks(id) {
				httputil.JSONError(w, http.StatusConflict, "cannot modify sandbox settings while tasks are running — stop all tasks first")
				return
			}
			stopped := h.executor.StopProjectTasks(id)
			if stopped > 0 {
				log.Printf("[project] stopped %d stale running tasks in project %s due to sandbox settings change", stopped, id)
			}
		}
		if req.SandboxEnabled != nil {
			if updateErr := h.store.UpdateProjectSandboxEnabled(id, *req.SandboxEnabled); updateErr != nil {
				httputil.JSONError(w, http.StatusInternalServerError, "failed to update sandbox setting")
				return
			}
		}
		if req.SandboxNetworkIsolation != nil {
			if updateErr := h.store.UpdateProjectSandboxNetworkIsolation(id, *req.SandboxNetworkIsolation); updateErr != nil {
				httputil.JSONError(w, http.StatusInternalServerError, "failed to update sandbox network isolation setting")
				return
			}
		}
		if req.SandboxImage != nil {
			if updateErr := h.store.UpdateProjectSandboxImage(id, *req.SandboxImage); updateErr != nil {
				httputil.JSONError(w, http.StatusInternalServerError, "failed to update sandbox image setting")
				return
			}
		}
		if req.SandboxDockerfilePath != nil {
			if updateErr := h.store.UpdateProjectSandboxDockerfilePath(id, *req.SandboxDockerfilePath); updateErr != nil {
				httputil.JSONError(w, http.StatusInternalServerError, "failed to update sandbox dockerfile path setting")
				return
			}
		}
		if req.SandboxMemoryLimit != nil {
			if updateErr := h.store.UpdateProjectSandboxMemoryLimit(id, *req.SandboxMemoryLimit); updateErr != nil {
				httputil.JSONError(w, http.StatusInternalServerError, "failed to update sandbox memory limit setting")
				return
			}
		}
		if req.SandboxForwardPorts != nil {
			if updateErr := h.store.UpdateProjectSandboxForwardPorts(id, req.SandboxForwardPorts); updateErr != nil {
				httputil.JSONError(w, http.StatusInternalServerError, "failed to update sandbox forward ports setting")
				return
			}
		}
		if req.SandboxMounts != nil {
			if updateErr := h.store.UpdateProjectSandboxMounts(id, req.SandboxMounts); updateErr != nil {
				httputil.JSONError(w, http.StatusInternalServerError, "failed to update sandbox mounts setting")
				return
			}
		}
		if req.SandboxEnvs != nil {
			if updateErr := h.store.UpdateProjectSandboxEnvs(id, req.SandboxEnvs); updateErr != nil {
				httputil.JSONError(w, http.StatusInternalServerError, "failed to update sandbox envs setting")
				return
			}
		}
		if req.SandboxCapabilities != nil {
			if updateErr := h.store.UpdateProjectSandboxCapabilities(id, req.SandboxCapabilities); updateErr != nil {
				httputil.JSONError(w, http.StatusInternalServerError, "failed to update sandbox capabilities setting")
				return
			}
		}
		// When sandbox settings change, stop all containers so they'll be
		// recreated with the new settings on next use.
		if hasSandboxChange {
			tasks, _ := h.store.GetTasksByProject(id)
			taskIDs := make([]string, 0, len(tasks))
			for _, task := range tasks {
				taskIDs = append(taskIDs, task.ID)
			}
			if h.sandboxMgr != nil {
				h.sandboxMgr.StopProjectContainers(id, taskIDs)
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
				// Clean up fork thread branches (thread/<threadID>)
				threads, _ := h.store.GetThreadsByTask(task.ID)
				for _, th := range threads {
					if th.GitBranch != "" {
						h.worktreeMgr.GitRunInProject(id, "branch", "-D", th.GitBranch)
					}
				}
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
				if wtDir := os.Getenv("MIKE_WORKTREES_DIR"); wtDir != "" {
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

// generateProjectID generates a project ID from path (basename or slugified).
func generateProjectID(path string, existingIDs []string) string {
	base := filepath.Base(path)
	if !slicesContains(existingIDs, base) {
		return base
	}
	// Collision — slugify the full path
	slug := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, path)
	// Collapse multiple dashes and trim leading/trailing dashes
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	slug = strings.Trim(slug, "-")
	return strings.ToLower(slug)
}

func slicesContains(slice []string, s string) bool {
	return slices.Contains(slice, s)
}
