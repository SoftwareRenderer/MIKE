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
	"mike/pkg/subproc"
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
	// jail for file endpoints; nil policy fails closed.
	hostRoot *fsutil.HostRoot
	// resolved DANGER_ZONE grant.
	dangerZone sandbox.DangerZone
	// submoduleFetch is the operator's half of what fetching a submodule may contact. The project's half
	// decides whether a turn asks at all; a person's click always may.
	submoduleFetch git.SubmoduleFetch
	// removes the repository directory of a project the server created under the development
	// namespace, and reports what it could not remove. Declared here so a project's deletion stays one
	// path; nil means this server has no extensions to develop.
	devRepoCleanup func(projectPath string) string
}

// projectSandboxOps is the sandbox operations owned by the project.
type projectSandboxOps interface {
	BuildProjectImage(ctx context.Context, project *types.Project, onProgress func(line string)) (*sandbox.BuildResult, error)
	// WatchImagePreparation joins the build or pull of the project's image that is already running, and returns
	// false when nothing is running.
	WatchImagePreparation(ctx context.Context, project *types.Project) (<-chan types.SandboxImageStatus, bool)
	CancelImagePreparation(project *types.Project) bool
	StopProjectContainers(projectID string, taskIDs []string)
}

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
	// DevRepoCleanup owns the repository directory of a development project; see ProjectHandler.
	DevRepoCleanup func(projectPath string) string
	// SubmoduleFetch carries the operator's decision into both submodule endpoints.
	SubmoduleFetch git.SubmoduleFetch
}

func NewProjectHandler(deps ProjectDeps) *ProjectHandler {
	return &ProjectHandler{
		store:          deps.Store,
		configStore:    deps.ConfigStore,
		worktreeMgr:    deps.WorktreeMgr,
		terminalMgr:    deps.TerminalMgr,
		sandboxMgr:     deps.SandboxMgr,
		executor:       deps.Executor,
		hostRoot:       deps.HostRoot,
		dangerZone:     deps.DangerZone,
		tasks:          deps.Tasks,
		devRepoCleanup: deps.DevRepoCleanup,

		submoduleFetch: deps.SubmoduleFetch,
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

// importSubmodules fetches the submodules of a repository MIKE has just taken in (git.InitSubmodules), and
// never fails the import: a submodule whose upstream is unreachable is the log's and the task's to report, not
// a reason a person cannot use MIKE with this project. onStatus, which may be nil, hears each outcome worth
// reporting.
func (h *ProjectHandler) importSubmodules(ctx context.Context, path string, onStatus func(string)) {
	if _, err := git.InitSubmodules(ctx, path, h.submoduleFetch, onStatus); err != nil {
		logging.For(logging.Git).Warnf("submodules of %s could not be read: %v", path, err)
	}
}

// HandleTaskSubmodules answers GET /api/projects/{projectID}/tasks/{taskID}/submodules with the submodules the
// task's own commit names, each with the address MIKE would fetch it from and whether the person's checkout
// already holds it (project_submodules.go holds the reading).
func (h *ProjectHandler) HandleTaskSubmodules(w http.ResponseWriter, r *http.Request) {
	task, ok := resolveTaskWithWorktree(w, r, h.store, h.worktreeMgr)
	if !ok {
		return
	}
	project, ok := resolveProject(w, r, h.store)
	if !ok {
		return
	}
	states, err := h.taskSubmoduleRows(r.Context(), task.ID, project.Path)
	if err != nil {
		httputil.Fail(w, errs.Operation("read the project's submodules", err), "read submodules", "project not found")
		return
	}
	httputil.JSONResponse(w, http.StatusOK, submodulesAnswer(states))
}

// HandleInitSubmodules answers POST /api/projects/{projectID}/submodules/init: the person asking MIKE to fetch
// what the project's checkout records and does not hold. What stayed out is part of the answer rather than a
// fault — the person has to read which submodule and why, and stay able to act on it.
func (h *ProjectHandler) HandleInitSubmodules(w http.ResponseWriter, r *http.Request) {
	project, ok := resolveProject(w, r, h.store)
	if !ok {
		return
	}
	init, err := h.initProjectSubmodules(r.Context(), project.Path)
	if err != nil {
		httputil.Fail(w, errs.Operation("initialize the project's submodules", err), "initialize submodules", "project not found")
		return
	}
	httputil.JSONResponse(w, http.StatusOK, init.Answer())
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
			// The response gets the short version, because a failed clone can explain itself at length.
			logging.For(logging.Git).Warnf("clone %s into %s failed: %s", types.RedactURL(req.GitURL), targetPath, strings.TrimSpace(string(out)))
			httputil.JSONError(w, http.StatusBadRequest, "git clone failed: "+subproc.Summarize(out, subproc.AnswerMax))
			return
		}
		req.Path = targetPath
	}

	// Submodules come down once the row exists; see importSubmodules.
	p, code, msg := h.createProjectFromRequest(req)
	if p == nil {
		httputil.JSONError(w, code, msg)
		return
	}
	h.importSubmodules(r.Context(), p.Path, nil)
	httputil.JSONResponse(w, code, p)
}

// SetDevRepoCleanup aims the project deletion path at the development namespace. The extensions handler
// is built after this one, so the aim is declared beside the rest of that wiring rather than reordered.
func (h *ProjectHandler) SetDevRepoCleanup(cleanup func(projectPath string) string) {
	h.devRepoCleanup = cleanup
}

// checkHostPath checks if path is inside the host jail.
func (h *ProjectHandler) checkHostPath(path string) error {
	if strings.TrimSpace(path) == "" || h.hostRoot.Unrestricted() {
		return nil
	}
	if _, err := h.hostRoot.Stat(path); err != nil && fsutil.IsHostRootRefusal(err) {
		return err
	}
	return nil
}

// prepareCloneTarget resolves clone target directory inside parent dir.
func (h *ProjectHandler) prepareCloneTarget(req types.CreateProjectRequest) (string, error) {
	if err := git.ValidateCloneURL(req.GitURL); err != nil {
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

// createProjectFromRequest validates path as git repo and persists the project.
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
	// The build context names a subtree of a commit, so it is checked as a repository path and not as a
	// host path: a value that names a directory on this machine is a misunderstanding of the field.
	contextRoot, err := sandbox.NormalizeBuildContextValue(req.SandboxBuildContext)
	if err != nil {
		return nil, http.StatusBadRequest, "sandbox_build_context: " + err.Error()
	}

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
		SandboxBuildContext:     contextRoot,
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

// HandleClone streams git clone over SSE and creates the project.
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

	sendEvent(map[string]any{"type": string(types.SSETypeStatus), "message": "Cloning " + types.RedactURL(req.GitURL) + " into " + targetPath + "..."})

	out, err := git.CloneContext(r.Context(), req.GitURL, targetPath, func(percent int) {
		sendEvent(map[string]any{"type": string(types.SSETypeProgress), "percent": percent})
	}, func(phase string) {
		sendEvent(map[string]any{"type": string(types.SSETypeStatus), "message": types.RedactURL(phase + "...")})
	})
	if err != nil {
		// Drop the partial clone so a retry can start clean.
		_ = os.RemoveAll(targetPath)
		if errors.Is(err, context.Canceled) {
			// The client went away; nobody is reading this stream, and calling a cancelled
			// clone a "git clone failure" would put the user's own navigation in the error log.
			logging.For(logging.Git).Infof("cancelled by client: %s", types.RedactURL(req.GitURL))
			return
		}
		logging.For(logging.Git).Warnf("clone %s into %s failed: %s", types.RedactURL(req.GitURL), targetPath, strings.TrimSpace(out))
		// Git's own words stay with the failure: whoever typed the address is the only one who can
		// act on them.
		sendEvent(map[string]any{"type": string(types.SSETypeError), "message": "git clone failed", "detail": cloneFailureDetail(out)})
		return
	}

	// Each submodule outcome the person would want is a status line on the stream the clone used.
	sendEvent(map[string]any{"type": string(types.SSETypeStatus), "message": "Fetching submodules..."})
	h.importSubmodules(r.Context(), targetPath, func(line string) {
		sendEvent(map[string]any{"type": string(types.SSETypeStatus), "message": line})
	})

	req.Path = targetPath
	if p, _, msg := h.createProjectFromRequest(req); p != nil {
		sendEvent(map[string]any{"type": string(types.SSETypeDone), "project": p})
	} else {
		sendEvent(map[string]any{"type": string(types.SSETypeError), "message": msg})
	}
}

// cloneFailureDetail is the end of what a failed clone said: bounded lines for a browser, each read for
// a credential typed into the address.
func cloneFailureDetail(out string) string {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		lines[i] = types.RedactLine(line)
	}
	return subproc.Tail([]byte(strings.Join(lines, "\n")), subproc.AnswerTailLines, subproc.AnswerTailMax)
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
		// The lock is a boot-time decision, so it is asked before anything in the request is read.
		if h.configStore.StatResetLocked() {
			httputil.JSONError(w, http.StatusForbidden, config.ErrStatsResetLocked.Error())
			return
		}
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

// applyPricing computes cost per model; overrides win, provider values fall back.
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

// buildResultFrame is the build stream's last frame: the build's own fields flattened beside the `status` the
// reader picks the result out by. Every other frame of that stream is a line for the log, so a result the reader
// cannot tell from a line is a build that finished and reads as one that never answered.
type buildResultFrame struct {
	Status string `json:"status"`
	*sandbox.BuildResult
}

// HandleSandboxBuild builds the project's sandbox image from its Dockerfile, streaming progress over SSE.
func (h *ProjectHandler) HandleSandboxBuild(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("projectID")

	project, ok := resolveProject(w, r, h.store)
	if !ok {
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

	if err := rejectBuildQuery(r); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	sendLine("Building the sandbox image…")

	// The request's context bounds the build: closing the tab stops the container.
	result, err := h.sandboxMgr.BuildProjectImage(r.Context(), project, func(line string) {
		_ = sendLine(line)
	})
	if err != nil {
		sendLine("error: " + err.Error())
		return
	}

	// Stop all containers and terminal sessions so they pick up the new image
	h.stopProjectSandbox(id)

	sse.JSON(buildResultFrame{Status: "built", BuildResult: result})
}

// rejectBuildQuery answers a build request's controls before the first frame. A key the route cannot honour is
// refused by name rather than ignored.
func rejectBuildQuery(r *http.Request) error {
	// By presence rather than by value: `?no_cache=0` still asks for a skip that does not exist.
	if _, ok := r.URL.Query()["no_cache"]; ok {
		return fmt.Errorf("no_cache: a build always rebuilds, so there is no build cache to skip")
	}
	return nil
}

// HandleSandboxBuildWatch streams the build or pull this project's image is already running, in the statuses
// `WatchImagePreparation` returns. It starts nothing, and closing the request ends the stream and not the build.
//
// A frame is a JSON status rather than a raw line like the ones the build route sends, because this reader
// shows a state as well as a log. A project with no build running gets a 404 and its own message before any
// stream is opened, which is how the screen knows to show the plain form instead of an empty log.
func (h *ProjectHandler) HandleSandboxBuildWatch(w http.ResponseWriter, r *http.Request) {
	project, ok := resolveProject(w, r, h.store)
	if !ok {
		return
	}
	if h.sandboxMgr == nil {
		httputil.JSONError(w, http.StatusNotFound, noBuildRunningMessage)
		return
	}
	statuses, watching := h.sandboxMgr.WatchImagePreparation(r.Context(), project)
	if !watching {
		httputil.JSONError(w, http.StatusNotFound, noBuildRunningMessage)
		return
	}
	sse, ok := httputil.NewSSE(w)
	if !ok {
		httputil.JSONError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	// The build's own goroutine only put statuses into the channel, so this loop stays the one writer of this
	// socket.
	for status := range statuses {
		sse.JSON(status)
	}
}

// noBuildRunningMessage is what both the watch and the cancel answer when no image is being prepared. A project
// with nothing running is an answer and not a fault, so nothing is logged for it.
const noBuildRunningMessage = "no sandbox image build is running for this project"

// buildCancelFrame is the cancel route's whole answer: whether the click stopped something.
type buildCancelFrame struct {
	Cancelled bool `json:"cancelled"`
}

// HandleSandboxBuildCancel answers `{"cancelled":bool}`: whether the click stopped anything. A `false` beside a
// 200 means no build was running, which is not a fault. `CancelImagePreparation` holds the rule about which
// caller the stop reaches and what happens to it.
func (h *ProjectHandler) HandleSandboxBuildCancel(w http.ResponseWriter, r *http.Request) {
	project, ok := resolveProject(w, r, h.store)
	if !ok {
		return
	}
	cancelled := h.sandboxMgr != nil && h.sandboxMgr.CancelImagePreparation(project)
	httputil.JSONResponse(w, http.StatusOK, buildCancelFrame{Cancelled: cancelled})
}

// HandleByID handles GET, PUT, DELETE for a single project.
// HandleByID is one route answering three verbs. Each verb gets its own method below because the work
// behind them does not overlap: a read, a partial update that must validate before it writes anything,
// and a delete whose cleanup has to finish before the row goes.
func (h *ProjectHandler) HandleByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("projectID")
	switch r.Method {
	case http.MethodGet:
		h.handleGetProject(w, r)

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
func (h *ProjectHandler) handleGetProject(w http.ResponseWriter, r *http.Request) {
	p, ok := resolveProject(w, r, h.store)
	if !ok {
		return
	}
	// Add active terminal ports from project-level terminal
	if h.terminalMgr != nil {
		p.ActiveTerminalPorts = h.terminalMgr.GetProjectPorts(p.ID)
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
	if req.SandboxBuildContext != nil {
		if _, err := sandbox.NormalizeBuildContextValue(*req.SandboxBuildContext); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, "sandbox_build_context: "+err.Error())
			return
		}
	}
	// The MCP opt-in list is validated here rather than at its write, so a list over the bound leaves
	// the project exactly as it was.
	var mcpIDs []string
	if req.MCPServerIDs != nil {
		ids, err := types.NormalizeMCPServerIDs(*req.MCPServerIDs)
		if err != nil {
			httputil.JSONError(w, http.StatusBadRequest, "mcp_server_ids: "+err.Error())
			return
		}
		mcpIDs = ids
	}
	// The commit identity is validated beside the rest. Each half is trimmed rather than refused: the
	// whitespace around a typed name belongs to the form, not to the identity.
	var gitAuthorName, gitAuthorEmail string
	if req.GitAuthorName != nil {
		gitAuthorName = strings.TrimSpace(*req.GitAuthorName)
		if err := types.ValidateGitAuthorName(gitAuthorName); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, "git_author_name: "+err.Error())
			return
		}
	}
	if req.GitAuthorEmail != nil {
		gitAuthorEmail = strings.TrimSpace(*req.GitAuthorEmail)
		if err := types.ValidateGitAuthorEmail(gitAuthorEmail); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, "git_author_email: "+err.Error())
			return
		}
	}
	// Block sandbox setting changes while tasks are running in the project.
	// Modifying sandbox settings during execution would kill bridge processes
	// mid-tool-call, causing "broken pipe" errors.
	hasSandboxChange := req.SandboxEnabled != nil || req.SandboxNetworkIsolation != nil ||
		req.SandboxImage != nil || req.SandboxDockerfilePath != nil || req.SandboxBuildContext != nil ||
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
	// Nor is the MCP opt-in: which servers a project may offer changes what the next turn's tools are,
	// and nothing about the container it runs in.
	if req.MCPServerIDs != nil {
		if err := h.store.UpdateProjectMCPServerIDs(id, mcpIDs); err != nil {
			httputil.Fail(w, errs.Operation("update MCP server list", err), "update project", "project not found")
			return
		}
	}
	// Nor the commit identity: it names the person the next commit carries and changes nothing about
	// where that commit is made.
	if req.GitAuthorName != nil {
		if err := h.store.UpdateProjectGitAuthorName(id, gitAuthorName); err != nil {
			httputil.Fail(w, errs.Operation("update git author name", err), "update project", "project not found")
			return
		}
	}
	if req.GitAuthorEmail != nil {
		if err := h.store.UpdateProjectGitAuthorEmail(id, gitAuthorEmail); err != nil {
			httputil.Fail(w, errs.Operation("update git author email", err), "update project", "project not found")
			return
		}
	}

	// Containers and sessions are recreated with the new settings on next use.
	if hasSandboxChange {
		h.stopProjectSandbox(id)
	}
	// Fetch latest state
	p, ok := resolveProject(w, r, h.store)
	if !ok {
		return
	}
	httputil.JSONResponse(w, http.StatusOK, p)
}

// applySandboxFields writes sandbox fields to store.
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
	if req.SandboxBuildContext != nil {
		// The value was validated before anything was written; normalizing it again is what stores the
		// one spelling of the subtree rather than the one that was typed.
		contextRoot, _ := sandbox.NormalizeBuildContextValue(*req.SandboxBuildContext)
		if updateErr := h.store.UpdateProjectSandboxBuildContext(id, contextRoot); updateErr != nil {
			return errs.Operation("update sandbox setting", updateErr)
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

// stopProjectSandbox stops project's sandbox container and terminal sessions.
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

// handleDeleteProject deletes project by tearing down tasks, then project resources.
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
	// A project the server created under the development namespace has no home elsewhere: its
	// directory *is* the extension repository, so it goes with the project. Anywhere else the
	// directory is the person's own work and nothing here touches it.
	if h.devRepoCleanup != nil {
		if p, err := h.store.GetProject(id); err == nil {
			if leftover := h.devRepoCleanup(p.Path); leftover != "" {
				logging.For(logging.Managers).Warnf("project %s: development repository cleanup failed: %s", id, leftover)
			}
		}
	}
	if err := h.store.DeleteProject(id); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to delete project")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// rootTaskIDs returns root tasks for deletion.
func rootTaskIDs(tasks []types.Task) []string {
	var roots []string
	for _, t := range tasks {
		if t.ParentTaskID == "" {
			roots = append(roots, t.ID)
		}
	}
	return roots
}

// repoNameFromGitURL extracts repository name from git URL.
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

// generateProjectID derives project ID from path.
func generateProjectID(path string, existingIDs []string) string {
	base := filepath.Base(filepath.Clean(path))
	// Slugging the whole path keeps colliding basenames distinguishable.
	if slices.Contains(existingIDs, base) || sandboxruntime.ValidateProjectName(base) != nil {
		return types.Slugify(path, 0)
	}
	return base
}
