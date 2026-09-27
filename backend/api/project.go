package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"mike/backend/config"
	"mike/backend/errs"
	"mike/backend/exec"
	"mike/backend/fsutil"
	"mike/backend/git"
	"mike/backend/httputil"
	"mike/backend/sandbox"
	"mike/backend/store"
	"mike/backend/term"
	"mike/backend/types"
	"mike/pkg/logging"
	"mike/pkg/sandboxruntime"
)

// ProjectHandler handles project CRUD operations.
type ProjectHandler struct {
	store       *store.Store
	configStore *config.ConfigStore
	worktreeMgr *git.WorktreeManager
	terminalMgr *term.TerminalManager
	sandboxMgr  projectSandboxOps
	executor    *exec.TaskExecutor
	tasks       taskTreeDeleter
	// hostRoot is the jail the file endpoints use, injected so "which directories did the
	// operator declare" has one answer. A nil policy fails closed: nothing is inside a jail
	// with no roots.
	hostRoot *fsutil.HostRoot
	// dangerZone is the resolved DANGER_ZONE grant, injected for the same reason as hostRoot: it
	// decides whether an unsandboxed project may be created at all, and a project created and a
	// project switched must consult one answer.
	dangerZone sandbox.DangerZone
}

// projectSandboxOps is the sandbox work a project owns rather than a task. These are the only two
// methods ProjectHandler calls, so a test can check that the containers were stopped.
type projectSandboxOps interface {
	BuildImageWithOptions(projectName, dockerfilePath string, opts sandbox.BuildOptions, onProgress func(line string)) error
	StopProjectContainers(projectID string, taskIDs []string)
}

// ProjectDeps is what a ProjectHandler is wired with.
type ProjectDeps struct {
	Store       *store.Store
	ConfigStore *config.ConfigStore
	WorktreeMgr *git.WorktreeManager
	TerminalMgr *term.TerminalManager
	SandboxMgr  projectSandboxOps
	Executor    *exec.TaskExecutor
	Tasks       taskTreeDeleter
	HostRoot    *fsutil.HostRoot
	DangerZone  sandbox.DangerZone
}

func NewProjectHandler(deps ProjectDeps) *ProjectHandler {
	return &ProjectHandler{
		store:       deps.Store,
		configStore: deps.ConfigStore,
		worktreeMgr: deps.WorktreeMgr,
		terminalMgr: deps.TerminalMgr,
		sandboxMgr:  deps.SandboxMgr,
		executor:    deps.Executor,
		hostRoot:    deps.HostRoot,
		dangerZone:  deps.DangerZone,
		tasks:       deps.Tasks,
	}
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
	if req.GitURL != "" {
		targetPath, err := h.prepareCloneTarget(req)
		if err != nil {
			code := http.StatusBadRequest
			if fsutil.IsHostRootRefusal(err) {
				code = http.StatusForbidden
			}
			httputil.JSONError(w, code, err.Error())
			return
		}
		// Same restricted argv/env as the SSE clone (transport allowlist,
		// no interactive credential prompt).
		cmd := git.Command("", git.CloneArgs(req.GitURL, targetPath)...)
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

// checkHostPath is the project side of the host jail: a request path must be inside $HOME plus
// the declared directories unless MIKE_ALLOW_ROOT_FS=1 widened it. It answers nil when the
// caller's own checks describe the failure better (a missing path is a 400, not a 403), and for
// an empty path, which is a setting left at its default.
func (h *ProjectHandler) checkHostPath(path string) error {
	if strings.TrimSpace(path) == "" || h.hostRoot.Unrestricted() {
		return nil
	}
	if _, err := h.hostRoot.Stat(path); err != nil && fsutil.IsHostRootRefusal(err) {
		return err
	}
	return nil
}

// prepareCloneTarget validates the clone source URL and resolves the clone
// target directory (<path>/<repo-name>) inside the existing parent dir
// req.Path. Returns the target path, or an error describing what is wrong.
func (h *ProjectHandler) prepareCloneTarget(req types.CreateProjectRequest) (string, error) {
	if err := validateGitCloneURL(req.GitURL); err != nil {
		return "", err
	}
	// Before any byte is fetched: a refused clone must leave no checkout behind.
	if err := h.checkHostPath(req.Path); err != nil {
		return "", err
	}
	repoName, err := repoNameFromGitURL(req.GitURL)
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
	if req.Path == "" {
		return nil, http.StatusBadRequest, "path is required"
	}
	// The project ID, the default name and the stored path are all derived from this string, so it
	// is cleaned once here: Base("<repo>/sub/..") is "..", and the first two would inherit it.
	req.Path = filepath.Clean(req.Path)

	// The server reads, watches and runs git here, so the picker's jail gates it. Checked first:
	// a refusal creates no row.
	if err := h.checkHostPath(req.Path); err != nil {
		return nil, http.StatusForbidden, err.Error()
	}

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

	// A sandbox image must name its registry: a short one would break every later sandbox start of
	// this project (see pkg/sandboxruntime/image_ref.go).
	if err := sandboxruntime.ValidateConfiguredImageRef(req.SandboxImage); err != nil {
		return nil, http.StatusBadRequest, err.Error()
	}

	// Forwarded ports reach podman as configured; only their shape is checked here.
	forwardPorts, err := types.NormalizeSandboxForwardPorts(req.SandboxForwardPorts)
	if err != nil {
		return nil, http.StatusBadRequest, err.Error()
	}

	// One mount of "/" or one CAP_SYS_ADMIN would undo the gVisor boundary around every
	// task in the project, so a project is measured by the rules an extension manifest
	// already has to meet.
	if err := types.ValidateSandboxAdditions(req.SandboxMounts, req.SandboxCapabilities); err != nil {
		return nil, http.StatusBadRequest, err.Error()
	}
	// Checked where it is accepted: the build reads the stored value with the server's own
	// privileges and never re-checks it.
	if err := h.checkHostPath(req.SandboxDockerfilePath); err != nil {
		return nil, http.StatusForbidden, "sandbox_dockerfile_path: " + err.Error()
	}

	// Collect existing project IDs to avoid collisions
	existingIDs := []string{}
	projects, _ := h.store.ListProjects()
	for _, p := range projects {
		existingIDs = append(existingIDs, p.ID)
	}
	// The ID is a URL path segment and a worktree directory name (<worktrees>/<id>/<taskID>), so it
	// is checked as a name rather than trusted because it came out of a path: ID ".." puts every
	// task worktree in the parent of the declared worktrees dir, out of reach of their GC.
	projectID := generateProjectID(req.Path, existingIDs)
	if err := sandboxruntime.ValidateProjectName(projectID); err != nil {
		return nil, http.StatusBadRequest, "cannot derive a project id from " + req.Path + ": " + err.Error()
	}

	now := time.Now().UTC()
	// Block creating a project with sandbox disabled unless DANGER_ZONE=1
	if !req.SandboxEnabled && !h.dangerZone.Allows() {
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
		ID:                      projectID,
		Name:                    name,
		Path:                    req.Path,
		SandboxEnabled:          req.SandboxEnabled,
		SandboxNetworkIsolation: networkIsolation,
		SandboxImage:            req.SandboxImage,
		SandboxDockerfilePath:   req.SandboxDockerfilePath,
		SandboxMemoryLimit:      req.SandboxMemoryLimit,
		ForwardPorts:            forwardPorts,
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

// HandleClone streams a git clone with live progress over SSE, then creates the project from
// the cloned directory. Body: CreateProjectRequest with Path = the clone-into parent directory
// and GitURL = the repository to clone (both required).
//
// Each "data:" frame is one JSON object:
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
	if req.GitURL == "" {
		httputil.JSONError(w, http.StatusBadRequest, "git_url is required")
		return
	}

	sse, ok := httputil.NewSSE(w)
	if !ok {
		httputil.JSONError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	sendEvent := sse.JSON

	// Same rules as HandleCreate, so common failures are known before any work starts; the host
	// jail's refusal arrives here as an error frame rather than a 403.
	targetPath, err := h.prepareCloneTarget(req)
	if err != nil {
		sendEvent(map[string]any{"type": string(types.SSETypeError), "message": err.Error()})
		return
	}

	sendEvent(map[string]any{"type": string(types.SSETypeStatus), "message": "Cloning " + req.GitURL + " into " + targetPath + "..."})

	out, err := git.CloneContext(r.Context(), req.GitURL, targetPath, func(percent int) {
		sendEvent(map[string]any{"type": string(types.SSETypeProgress), "percent": percent})
	})
	if err != nil {
		// Drop the partial clone so a retry can start clean.
		_ = os.RemoveAll(targetPath)
		if errors.Is(err, context.Canceled) {
			// The client went away; nobody is reading this stream, and calling a cancelled
			// clone a "git clone failure" would put the user's own navigation in the error log.
			logging.For(logging.Git).Infof("cancelled by client: %s", req.GitURL)
			return
		}
		sendEvent(map[string]any{"type": string(types.SSETypeError), "message": "git clone failed: " + strings.TrimSpace(out)})
		return
	}

	req.Path = targetPath
	if p, _, msg := h.createProjectFromRequest(req); p != nil {
		sendEvent(map[string]any{"type": string(types.SSETypeDone), "project": p})
	} else {
		sendEvent(map[string]any{"type": string(types.SSETypeError), "message": msg})
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
		httputil.MethodNotAllowed(w)
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
	noCache := buildWantsNoCache(r)

	// Get the project to find the Dockerfile path
	project, err := h.store.GetProject(id)
	if err != nil {
		httputil.Fail(w, err, "get project", "project not found")
		return
	}
	if project.SandboxDockerfilePath == "" {
		httputil.JSONError(w, http.StatusBadRequest, "no Dockerfile path configured for this project")
		return
	}
	sse, ok := httputil.NewSSE(w)
	if !ok {
		httputil.JSONError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Each frame is one raw progress line, not JSON: this stream's reader shows the lines.
	sendLine := sse.Data

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
	h.stopProjectSandbox(id)

	// Final result as JSON
	sse.JSON(map[string]string{
		"status":  "built",
		"image":   sandbox.ProjectImageName(project.Name),
		"project": id,
	})
}

// HandleByID handles GET, PUT, DELETE for a single project.
// HandleByID is one route answering three verbs. Each verb gets its own method below because the work
// behind them does not overlap: a read, a partial update that must validate before it writes anything,
// and a delete whose cleanup has to finish before the row goes.
func (h *ProjectHandler) HandleByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("projectID")
	switch r.Method {
	case http.MethodGet:
		h.handleGetProject(w, id)

	case http.MethodPut:
		h.handleUpdateProject(w, r, id)

	case http.MethodDelete:
		h.handleDeleteProject(w, id)

	default:
		httputil.MethodNotAllowed(w)
	}
}

// handleGetProject answers the project row plus the ports its terminal sessions are holding open, which is
// live state the row does not own.
func (h *ProjectHandler) handleGetProject(w http.ResponseWriter, id string) {
	p, err := h.store.GetProject(id)
	if err != nil {
		httputil.Fail(w, err, "get project", "project not found")
		return
	}
	// Add active terminal ports from project-level terminal
	if h.terminalMgr != nil {
		p.ActiveTerminalPorts = h.terminalMgr.GetProjectPorts(id)
	}
	httputil.JSONResponse(w, http.StatusOK, p)
}

// handleUpdateProject is a partial update: a field that is absent is left alone, and every value is
// validated before the first write, so a refused request leaves the project as it was.
func (h *ProjectHandler) handleUpdateProject(w http.ResponseWriter, r *http.Request, id string) {
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
	// Validate before writing any sandbox field, so a rejected value can't leave the project
	// half-updated.
	if req.SandboxImage != nil {
		if err := sandboxruntime.ValidateConfiguredImageRef(*req.SandboxImage); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	var forwardPorts []types.PortForward
	if req.SandboxForwardPorts != nil {
		normalized, err := types.NormalizeSandboxForwardPorts(req.SandboxForwardPorts)
		if err != nil {
			httputil.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		forwardPorts = normalized
	}
	// Validate before anything is written or any task is stopped: a refused request
	// must leave the project as it was.
	if req.SandboxMounts != nil || req.SandboxCapabilities != nil {
		if err := types.ValidateSandboxAdditions(req.SandboxMounts, req.SandboxCapabilities); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.SandboxDockerfilePath != nil {
		if err := h.checkHostPath(*req.SandboxDockerfilePath); err != nil {
			httputil.JSONError(w, http.StatusForbidden, "sandbox_dockerfile_path: "+err.Error())
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
			logging.For(logging.Managers).Infof("stopped %d stale running tasks in project %s due to sandbox settings change", stopped, id)
		}
	}
	// A policy refusal is answered before any write, not in the middle of the writes it exists to
	// prevent: disabling sandboxing is the operator's decision and needs DANGER_ZONE=1.
	if req.SandboxEnabled != nil && !*req.SandboxEnabled && !h.dangerZone.Allows() {
		httputil.JSONError(w, http.StatusForbidden, "cannot disable sandbox isolation — set DANGER_ZONE=1 to allow")
		return
	}
	if err := h.applySandboxFields(id, req, forwardPorts); err != nil {
		httputil.Fail(w, err, "update project", "project not found")
		return
	}
	// Changing the trailer answer recreates no container and stops no task, so it is not a sandbox field.
	if req.CommitAssistedBy != nil {
		if err := h.store.UpdateProjectCommitAssistedBy(id, *req.CommitAssistedBy); err != nil {
			httputil.Fail(w, errs.Operation("update commit trailer setting", err), "update project", "project not found")
			return
		}
	}

	// Containers and sessions are recreated with the new settings on next use.
	if hasSandboxChange {
		h.stopProjectSandbox(id)
	}
	// Fetch latest state
	p, err := h.store.GetProject(id)
	if err != nil {
		httputil.Fail(w, err, "get project", "project not found")
		return
	}
	httputil.JSONResponse(w, http.StatusOK, p)
}

// applySandboxFields writes the sandbox fields the request provided, one store statement each. Every value
// was validated by the caller before the first write, so a failure here is the store refusing, not the
// caller: each returns an operation-classified fault naming the field, which the transport answers as a
// server fault with the store's error in the log — none of these logged anything before.
func (h *ProjectHandler) applySandboxFields(id string, req types.UpdateProjectRequest, forwardPorts []types.PortForward) error {
	if req.SandboxEnabled != nil {
		if updateErr := h.store.UpdateProjectSandboxEnabled(id, *req.SandboxEnabled); updateErr != nil {
			return errs.Operation("update sandbox setting", updateErr)
		}
	}
	if req.SandboxNetworkIsolation != nil {
		if updateErr := h.store.UpdateProjectSandboxNetworkIsolation(id, *req.SandboxNetworkIsolation); updateErr != nil {
			return errs.Operation("update sandbox network isolation setting", updateErr)
		}
	}
	if req.SandboxImage != nil {
		if updateErr := h.store.UpdateProjectSandboxImage(id, *req.SandboxImage); updateErr != nil {
			return errs.Operation("update sandbox image setting", updateErr)
		}
	}
	if req.SandboxDockerfilePath != nil {
		if updateErr := h.store.UpdateProjectSandboxDockerfilePath(id, *req.SandboxDockerfilePath); updateErr != nil {
			return errs.Operation("update sandbox dockerfile path setting", updateErr)
		}
	}
	if req.SandboxMemoryLimit != nil {
		limit := *req.SandboxMemoryLimit
		if limit < 0 {
			limit = 0
		}
		if updateErr := h.store.UpdateProjectSandboxMemoryLimit(id, limit); updateErr != nil {
			return errs.Operation("update sandbox memory limit setting", updateErr)
		}
	}
	if req.SandboxForwardPorts != nil {
		if updateErr := h.store.UpdateProjectSandboxForwardPorts(id, forwardPorts); updateErr != nil {
			return errs.Operation("update sandbox forward ports setting", updateErr)
		}
	}
	if req.SandboxMounts != nil {
		if updateErr := h.store.UpdateProjectSandboxMounts(id, req.SandboxMounts); updateErr != nil {
			return errs.Operation("update sandbox mounts setting", updateErr)
		}
	}
	if req.SandboxEnvs != nil {
		if updateErr := h.store.UpdateProjectSandboxEnvs(id, req.SandboxEnvs); updateErr != nil {
			return errs.Operation("update sandbox envs setting", updateErr)
		}
	}
	if req.SandboxCapabilities != nil {
		if updateErr := h.store.UpdateProjectSandboxCapabilities(id, req.SandboxCapabilities); updateErr != nil {
			return errs.Operation("update sandbox capabilities setting", updateErr)
		}
	}
	return nil
}

// stopProjectSandbox stops a project's own sandbox container and its terminal sessions. Both are
// needed: that container is keyed "proj:"+projectID, and a file-browser listing creates it with no
// task and no terminal open, so closing sessions alone leaves it running on the project's checkout.
func (h *ProjectHandler) stopProjectSandbox(projectID string) {
	tasks, _ := h.store.GetTasksByProject(projectID)
	taskIDs := make([]string, 0, len(tasks))
	for _, task := range tasks {
		taskIDs = append(taskIDs, task.ID)
	}
	if h.sandboxMgr != nil {
		h.sandboxMgr.StopProjectContainers(projectID, taskIDs)
	}
	if h.terminalMgr != nil {
		h.terminalMgr.RemoveProjectSessions(projectID)
	}
}

// handleDeleteProject deletes a project by deleting its tasks, so each task's executor, sandbox
// container and uploaded files go before its row does. Nothing removes a container once its task row is
// gone: the startup sweep skips containers that are running.
func (h *ProjectHandler) handleDeleteProject(w http.ResponseWriter, id string) {
	tasks, err := h.store.GetTasksByProject(id)
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to list tasks for cleanup")
		return
	}
	if h.tasks == nil {
		// Nothing else reports this: the project row is deleted and its tasks' containers keep running.
		logging.For(logging.Managers).Warnf("project %s deleted with no task delete path wired: %d task(s) not torn down", id, len(tasks))
	} else if roots := rootTaskIDs(tasks); len(roots) > 0 {
		h.tasks.deleteTaskTrees(id, roots...)
	}
	// Deleting the tasks does not cover what belongs to the project itself: its sandbox container, its
	// terminal session, and its worktree directory. The directory is removed again here because a task
	// whose worktree removal failed would otherwise leave it behind.
	h.stopProjectSandbox(id)
	if h.worktreeMgr != nil {
		if err := h.worktreeMgr.RemoveProjectWorktrees(id); err != nil {
			logging.For(logging.Managers).Warnf("project %s: worktree directory cleanup failed: %v", id, err)
		}
	}
	if err := h.store.DeleteProject(id); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to delete project")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// rootTaskIDs returns the tasks whose deletion covers the whole set. Deleting a task deletes its
// subtree, and parent_task_id is a foreign key, so every parent is a row in the same list.
func rootTaskIDs(tasks []types.Task) []string {
	var roots []string
	for _, t := range tasks {
		if t.ParentTaskID == "" {
			roots = append(roots, t.ID)
		}
	}
	return roots
}

// validateGitCloneURL validates a clone source before it reaches `git clone`.
// The rules live in git.ValidateCloneURL — the same answer project creation and
// an extension install get — so this is the project-creation spelling of it.
func validateGitCloneURL(gitURL string) error {
	return git.ValidateCloneURL(gitURL)
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

// generateProjectID derives a project ID from path: its base name when that is a safe project
// name, otherwise its slug. The slug can come back empty — a path with no letters or digits — so
// the caller checks the answer.
func generateProjectID(path string, existingIDs []string) string {
	base := filepath.Base(filepath.Clean(path))
	// Slugging the whole path keeps colliding basenames distinguishable.
	if slices.Contains(existingIDs, base) || sandboxruntime.ValidateProjectName(base) != nil {
		return types.Slugify(path, 0)
	}
	return base
}
