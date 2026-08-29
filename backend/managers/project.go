package managers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"slices"
	"strconv"
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
	"kanban/pkg/sandboxruntime"
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
	if !httputil.DecodeJSON(w, r, &req) {
		return
	}
	if req.Path == "" {
		httputil.JSONError(w, http.StatusBadRequest, "path is required")
		return
	}

	req.Path = strings.TrimSpace(req.Path)

	// If git_url is provided, clone the repo into <path>/<repo-name> first,
	// then proceed with normal project creation from the cloned directory.
	if req.GitUrl != "" {
		targetPath, err := prepareCloneTarget(req)
		if err != nil {
			httputil.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		// Same restricted argv/env as the SSE clone (transport allowlist,
		// no interactive credential prompt).
		cmd := osexec.Command("git", git.CloneArgs(req.GitUrl, targetPath)...)
		cmd.Env = git.CloneEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, "git clone failed: "+strings.TrimSpace(string(out)))
			return
		}
		req.Path = targetPath
	}

	p, code, msg := h.createProjectFromRequest(req)
	if p == nil {
		httputil.JSONError(w, code, msg)
		return
	}
	httputil.JSONResponse(w, code, p)
}

// prepareCloneTarget validates the clone source URL and resolves the clone
// target directory (<path>/<repo-name>) inside the existing parent dir
// req.Path. Returns the target path, or an error describing what is wrong.
func prepareCloneTarget(req types.CreateProjectRequest) (string, error) {
	if err := validateGitCloneURL(req.GitUrl); err != nil {
		return "", err
	}
	repoName, err := repoNameFromGitURL(req.GitUrl)
	if err != nil {
		return "", err
	}
	// Validate the target parent exists and is a directory.
	info, err := os.Stat(req.Path)
	if err != nil {
		return "", fmt.Errorf("clone target parent does not exist: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("clone target parent is not a directory")
	}
	targetPath := filepath.Join(req.Path, repoName)
	if _, err := os.Stat(targetPath); err == nil {
		return "", fmt.Errorf("target directory already exists: %s", targetPath)
	}
	return targetPath, nil
}

// createProjectFromRequest validates that req.Path is an existing git
// repository directory and persists the new project. On success it returns
// the project and http.StatusCreated; on failure it returns nil with the
// HTTP status code and a client-facing message.
func (h *ProjectHandler) createProjectFromRequest(req types.CreateProjectRequest) (*types.Project, int, string) {
	req.Path = strings.TrimSpace(req.Path)

	info, err := os.Stat(req.Path)
	if err != nil {
		return nil, http.StatusBadRequest, "path does not exist: " + err.Error()
	}
	if !info.IsDir() {
		return nil, http.StatusBadRequest, "path is not a directory"
	}

	if !git.IsGitRepo(req.Path) {
		return nil, http.StatusBadRequest, "path is not a git repository"
	}

	name := req.Name
	if name == "" {
		name = filepath.Base(req.Path)
	}

	// Validate project name to prevent injection when used in image refs, mount paths, etc.
	if err := sandboxruntime.ValidateProjectName(name); err != nil {
		return nil, http.StatusBadRequest, err.Error()
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
	// Block creating a project with sandbox disabled unless DANGER_ZONE=1
	if !req.SandboxEnabled && os.Getenv("DANGER_ZONE") != "1" {
		return nil, http.StatusForbidden, "cannot create project with sandbox disabled — set DANGER_ZONE=1 to allow"
	}
	// When sandbox is enabled, network isolation must be explicitly specified
	if req.SandboxEnabled && req.SandboxNetworkIsolation == nil {
		return nil, http.StatusBadRequest, "sandbox_network_isolation is required when sandbox_enabled is true"
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
		return nil, http.StatusInternalServerError, "failed to create project: " + err.Error()
	}
	return p, http.StatusCreated, ""
}

// HandleClone streams a git clone with live progress over SSE, then creates
// the project from the cloned directory.
//
// Body: CreateProjectRequest with Path = clone-into parent directory and
// GitUrl = repository to clone (both required).
//
// Emits one JSON object per "data:" frame:
//
//	{"type":"status","message":"..."}  — informational
//	{"type":"progress","percent":42}   — clone progress (0-100)
//	{"type":"done","project":{...}}    — success, project created
//	{"type":"error","message":"..."}   — failure, no project created
func (h *ProjectHandler) HandleClone(w http.ResponseWriter, r *http.Request) {
	var req types.CreateProjectRequest
	if !httputil.DecodeJSON(w, r, &req) {
		return
	}
	req.Path = strings.TrimSpace(req.Path)
	if req.Path == "" {
		httputil.JSONError(w, http.StatusBadRequest, "path is required")
		return
	}
	if req.GitUrl == "" {
		httputil.JSONError(w, http.StatusBadRequest, "git_url is required")
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

	sendEvent := func(event map[string]any) {
		data, _ := json.Marshal(event)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	// Validate source URL and target location up front (same rules as
	// HandleCreate) so common failures are known before any work starts.
	targetPath, err := prepareCloneTarget(req)
	if err != nil {
		sendEvent(map[string]any{"type": "error", "message": err.Error()})
		return
	}

	sendEvent(map[string]any{"type": "status", "message": "Cloning " + req.GitUrl + " into " + targetPath + "..."})

	out, err := git.Clone(req.GitUrl, targetPath, func(percent int) {
		sendEvent(map[string]any{"type": "progress", "percent": percent})
	})
	if err != nil {
		// Drop the partial clone so a retry can start clean.
		_ = os.RemoveAll(targetPath)
		sendEvent(map[string]any{"type": "error", "message": "git clone failed: " + strings.TrimSpace(out)})
		return
	}

	req.Path = targetPath
	if p, _, msg := h.createProjectFromRequest(req); p != nil {
		sendEvent(map[string]any{"type": "done", "project": p})
	} else {
		sendEvent(map[string]any{"type": "error", "message": msg})
	}
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

// findModelConfig returns the model config matching name, or nil.
func findModelConfig(p types.Provider, modelName string) *types.ModelConfig {
	for i := range p.Models {
		if p.Models[i].Name == modelName {
			return &p.Models[i]
		}
	}
	return nil
}

// applyPricing computes cost per model: per-model overrides win, provider values fall back.
// Agent providers report their own cost directly.
func (h *ProjectHandler) applyPricing(stats *types.ProjectStats) {
	cfg, err := h.configStore.GetConfig()
	if err != nil {
		return // no config, skip pricing
	}

	// Build provider ID -> provider map.
	providerMap := make(map[string]types.Provider)
	for _, p := range cfg.Providers {
		if p.ID != 0 {
			providerMap[strconv.Itoa(p.ID)] = p
		}
	}

	for i := range stats.ByModel {
		m := &stats.ByModel[i]
		p, ok := providerMap[m.ProviderName]
		if !ok {
			continue // no pricing configured for this provider
		}

		// Agent providers report their own cost — keep the DB-accumulated value.
		// GetProjectStats already scanned cost into m.Cost and stats.TotalCost.
		if p.EffectiveKind() == types.ProviderKindAgent {
			continue
		}

		model := findModelConfig(p, m.ModelName)

		// Model overrides win over provider defaults; cache_write_price replaces
		// input_price for non-cached input (same rules as EstimateCallCost).
		inputRate, cacheRead, outputRate := config.ResolveProviderPricing(&p, m.ModelName)

		apiCost := (float64(m.ProcessedTokens)*inputRate +
			float64(m.CacheTokens)*cacheRead +
			float64(m.GeneratedTokens)*outputRate) / 1_000_000

		m.Cost = apiCost
		stats.TotalCost += apiCost

		// Power consumption and electricity cost (LLM providers only).
		// Per-model power overrides the provider-level value when set.
		powerW := p.PowerConsumptionW
		if model != nil && model.PowerConsumptionW != nil {
			powerW = *model.PowerConsumptionW
		}
		if p.EffectiveKind() == types.ProviderKindLLM && powerW > 0 && m.PredictedMs > 0 {
			// predicted_ms is cumulative generation time; convert ms to hours
			powerKwh := powerW * float64(m.PredictedMs) / 3_600_000_000
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

// buildWantsNoCache reports whether a build request asked to skip the layer
// cache. Absent or unparsable values mean "use the cache".
func buildWantsNoCache(r *http.Request) bool {
	v := r.URL.Query().Get("no_cache")
	if v == "" {
		return false
	}
	noCache, err := strconv.ParseBool(v)
	if err != nil {
		return false
	}
	return noCache
}

// HandleSandboxBuild builds a container image from the project's Dockerfile.
// Streams build progress as SSE and returns final JSON result.
// ?no_cache=1 skips the layer cache for this build only.
func (h *ProjectHandler) HandleSandboxBuild(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("projectID")
	if r.Method != http.MethodPost {
		httputil.JSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	noCache := buildWantsNoCache(r)

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

	if noCache {
		sendLine("Building sandbox image from Dockerfile (no cache)...")
	} else {
		sendLine("Building sandbox image from Dockerfile...")
	}

	err = h.sandboxMgr.BuildImageWithOptions(project.Name, project.SandboxDockerfilePath, sandbox.BuildOptions{NoCache: noCache}, func(line string) {
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
		if !httputil.DecodeJSON(w, r, &req) {
			return
		}
		// Partial update: apply only provided fields
		if req.Name != "" {
			if err := sandboxruntime.ValidateProjectName(req.Name); err != nil {
				httputil.JSONError(w, http.StatusBadRequest, err.Error())
				return
			}
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
			// Block disabling sandbox unless DANGER_ZONE=1
			if !*req.SandboxEnabled && os.Getenv("DANGER_ZONE") != "1" {
				httputil.JSONError(w, http.StatusForbidden, "cannot disable sandbox isolation — set DANGER_ZONE=1 to allow")
				return
			}
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
			limit := *req.SandboxMemoryLimit
			if limit < 0 {
				limit = 0
			}
			if updateErr := h.store.UpdateProjectSandboxMemoryLimit(id, limit); updateErr != nil {
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
				_ = os.RemoveAll(filepath.Join(worktreesDir, id))
			}
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

// validateGitCloneURL validates a clone source before it reaches `git clone`.
// It closes two risks:
//
//   - Argument injection: a leading '-' would be read by git as a flag
//     ("--upload-pack=…", "--reference"), so it is rejected.
//   - Transport injection: git runs a URL whose scheme is an executable helper,
//     so `ext::sh -c 'id'` runs a shell. Git refuses helper transports by
//     default, but that is a downstream default this product does not control
//     (`-c protocol.ext.allow=always` in the user's config or environment
//     re-enables it), so the scheme is allowlisted here too. git.CloneArgs and
//     git.CloneEnv apply the same restriction at runtime.
//
// Accepted: URLs with an allowed scheme (http, https, git, ssh, file), scp-like
// SSH syntax (git@host:org/repo.git), or a bare filesystem path — local cloning
// is a supported workflow.
func validateGitCloneURL(gitURL string) error {
	s := strings.TrimSpace(gitURL)
	if s == "" {
		return fmt.Errorf("git_url is required")
	}
	if strings.HasPrefix(s, "-") {
		return fmt.Errorf("git_url must not start with a dash")
	}
	if strings.ContainsAny(s, "\x00\n\r\v\f") {
		return fmt.Errorf("git_url must not contain control characters")
	}
	// Remote-helper transport syntax (`<helper>::<args>`, e.g. "ext::sh -c 'id'")
	// never appears in a real repository URL and has no "://", so it must be
	// caught before the scheme rules. Only the text before a scheme is searched,
	// so an IPv6 host (http://[::1]:8080/…) still passes.
	head := s
	if i := strings.Index(s, "://"); i >= 0 {
		head = s[:i]
	}
	if strings.Contains(head, "::") {
		return fmt.Errorf("git_url must not use the git remote-helper transport syntax (::)")
	}
	// Explicit scheme: only the transports in git.CloneAllowedProtocols.
	// Compared verbatim, because git's own GIT_ALLOW_PROTOCOL check is
	// case-sensitive ("HTTPS://…" is refused as transport 'HTTPS').
	if scheme, ok := cloneURLScheme(s); ok {
		if !slices.Contains(git.CloneAllowedProtocols, scheme) {
			return fmt.Errorf("git_url scheme %q is not allowed (allowed: %s; schemes are case-sensitive)",
				scheme, strings.Join(git.CloneAllowedProtocols, ", "))
		}
		return nil
	}
	// No scheme: git picks between an scp-style SSH remote and a local path by
	// looking at the first ':' — text before it that holds no '/' is a host.
	if i := strings.Index(s, ":"); i >= 0 {
		if slash := strings.Index(s, "/"); slash < 0 || i < slash {
			return validateScpLikeGitURL(s, i)
		}
	}
	// Anything else is a local filesystem path (absolute or relative).
	return nil
}

// cloneURLScheme returns the scheme of a "scheme://rest" URL, exactly as written
// (git matches schemes case-sensitively, so the validator must not normalise it).
func cloneURLScheme(s string) (string, bool) {
	i := strings.Index(s, "://")
	if i <= 0 {
		return "", false
	}
	scheme := s[:i]
	for j := 0; j < len(scheme); j++ {
		c := scheme[j]
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (j == 0 || (c != '+' && c != '-' && c != '.')) {
			return "", false
		}
	}
	return scheme, true
}

// validateScpLikeGitURL validates the scp-style SSH form `[user@]host:path`
// (e.g. `git@github.com:org/repo.git`), the only scheme-less form git treats as a
// remote. Anything looser (whitespace, a second colon or slash in the host, an
// empty path) is rejected: it is either not a repository or something trying to
// get past the checks.
func validateScpLikeGitURL(s string, colon int) error {
	host, path := s[:colon], s[colon+1:]
	if host == "" || path == "" {
		return fmt.Errorf("git_url is not a valid repository URL or path")
	}
	for i := 0; i < len(host); i++ {
		c := host[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '-' || c == '_' || c == '@'
		if !ok {
			return fmt.Errorf("git_url host %q is not valid", host)
		}
	}
	if strings.HasPrefix(path, ":") || strings.ContainsAny(path, " \t") {
		return fmt.Errorf("git_url must not contain whitespace")
	}
	return nil
}

// repoNameFromGitURL extracts the repository name from a git URL.
// Supports HTTPS (https://host/user/repo.git) and SSH (git@host:user/repo.git) forms.
func repoNameFromGitURL(gitURL string) (string, error) {
	s := strings.TrimSpace(gitURL)
	if s == "" {
		return "", fmt.Errorf("git_url is required")
	}
	// SSH form uses ':' as the separator after the host (no scheme present).
	if strings.Contains(s, ":") && !strings.Contains(s, "://") {
		idx := strings.LastIndex(s, ":")
		s = s[idx+1:]
	}
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimSuffix(s, "/")
	base := filepath.Base(s)
	if base == "" || base == "." || base == "/" {
		return "", fmt.Errorf("could not determine repository name from git URL")
	}
	return base, nil
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
