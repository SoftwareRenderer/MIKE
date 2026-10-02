package extensions

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mike/backend/fsutil"
	"mike/backend/httputil"
	"mike/backend/types"
	"mike/pkg/logging"
)

// sseEvent is one SSE payload: progress events, then a final Done=true event
// carrying the resulting extension (Result) or an Error.
type sseEvent struct {
	Extension string             `json:"extension"`
	Progress  float64            `json:"progress"`
	Message   string             `json:"message"`
	Done      bool               `json:"done"`
	Error     string             `json:"error,omitempty"`
	Result    *ExtensionResponse `json:"result,omitempty"`
}

// inFlight guards concurrent install/update per extension name. The guard stays
// held until the detached background goroutine finishes, so a reinstall can't
// race an in-flight one (even after a page close).
type inFlight struct {
	mu   sync.Mutex
	busy map[string]bool
}

func (g *inFlight) begin(name string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.busy[name] {
		return false
	}
	g.busy[name] = true
	return true
}

func (g *inFlight) end(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.busy, name)
}

// ExtensionHandler handles extension API requests.
type ExtensionHandler struct {
	store              *ExtensionStore
	sandbox            *ExtensionSandboxManager
	registry           *RegistryClient
	config             *ExtensionConfig
	inFlightOps        *inFlight
	onImageUpdated     func(extID string) // callback after successful image update
	onExtensionChanged func()             // callback after any extension change (enable/disable/create/delete)
}

// ExtensionConfig holds configuration for the extension handler.
type ExtensionConfig struct {
	RegistryURL string
	// ExtensionsDir is the directory served as /extensions/ where materialized
	// extension files are written. Defaults to "extensions".
	ExtensionsDir string
}

// ExtensionsDirOrDefault returns the configured extensions directory, defaulting
// to "extensions" (the directory the static server serves as /extensions/).
func (c *ExtensionConfig) ExtensionsDirOrDefault() string {
	if c == nil || c.ExtensionsDir == "" {
		return "extensions"
	}
	return c.ExtensionsDir
}

// authorDir turns a registry author into a safe, single-segment directory name:
// lowercased, with any non-alphanumeric character (other than '-', '_', '.')
// collapsed to '-'. Empty/whitespace authors fall back to "unknown" so the
// install path never escapes or becomes an empty segment.
func authorDir(author string) string {
	slug := strings.ToLower(strings.TrimSpace(author))
	if slug == "" {
		return "unknown"
	}
	var b strings.Builder
	lastDash := false
	for _, r := range slug {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.'
		if !ok {
			if lastDash {
				continue
			}
			b.WriteByte('-')
			lastDash = true
			continue
		}
		b.WriteRune(r)
		lastDash = false
	}
	out := b.String()
	if out == "" {
		return "unknown"
	}
	return strings.TrimRight(out, "-._")
}

func NewExtensionHandler(store *ExtensionStore, sandbox *ExtensionSandboxManager, registry *RegistryClient, cfg *ExtensionConfig) *ExtensionHandler {
	return &ExtensionHandler{
		store:       store,
		sandbox:     sandbox,
		registry:    registry,
		config:      cfg,
		inFlightOps: &inFlight{busy: map[string]bool{}},
	}
}

// SetOnImageUpdated sets a callback invoked after a successful image update.
func (h *ExtensionHandler) SetOnImageUpdated(fn func(extID string)) {
	h.onImageUpdated = fn
}

// SetOnExtensionChanged sets a callback invoked after any extension state change.
func (h *ExtensionHandler) SetOnExtensionChanged(fn func()) {
	h.onExtensionChanged = fn
}

// mountPolicy returns the host-mount allowlist an extension sandbox is held to:
// the sandbox manager's policy (workspace dir, registered projects, extensions
// dir, temp dir, MIKE_EXT_MOUNT_ROOTS) plus this handler's extensions directory.
func (h *ExtensionHandler) mountPolicy() *MountPolicy {
	extDir := h.config.ExtensionsDirOrDefault()
	if h.sandbox != nil {
		return h.sandbox.MountPolicy().withExtraRoots(extDir)
	}
	// No sandbox manager means no wired extras: the policy comes out narrower than the operator's,
	// never wider, which is the only direction a degenerate wiring is allowed to go.
	return newMountPolicy(mountRoots(extDir, "", nil, nil))
}

// revalidateListBeforeUse lets a client keep an extension list answer but not show it without asking
// first. The ETag it revalidates against is `CompressMiddleware`'s digest of the body, so a handler
// that set its own ETag would take the 304 away instead of gaining one.
func revalidateListBeforeUse(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-cache")
}

// HandleList returns all installed extensions with runtime status.
func (h *ExtensionHandler) HandleList(w http.ResponseWriter, r *http.Request) {

	extensions, err := h.store.List()
	if err != nil {
		httputil.Fault(w, "list extensions", err)
		return
	}

	// Sort by priority ASC, then by name ASC (lower priority = loads first)
	sort.Slice(extensions, func(i, j int) bool {
		if extensions[i].Manifest.Priority != extensions[j].Manifest.Priority {
			return extensions[i].Manifest.Priority < extensions[j].Manifest.Priority
		}
		return extensions[i].Name < extensions[j].Name
	})

	// Add runtime status
	statuses := h.sandbox.ListRunning()
	result := make([]ExtensionResponse, len(extensions))
	for i, ext := range extensions {
		result[i] = h.toResponse(&ext, statuses)
	}

	revalidateListBeforeUse(w)
	httputil.JSONResponse(w, http.StatusOK, result)
}

// extensionOr404 returns the stored record for an extension ID, answering 404 (500, logged,
// for any other store failure) and returning false when it is not there.
func (h *ExtensionHandler) extensionOr404(w http.ResponseWriter, id string) (*Extension, bool) {
	ext, err := h.store.GetByID(id)
	if err != nil {
		httputil.Fail(w, err, "get extension", "extension not found")
		return nil, false
	}
	return ext, true
}

// openRegistry reads the configured registry's index, answering 400 when no URL is configured and a
// fault when the registry will not answer. It returns the URL too, because installing fetches again.
func (h *ExtensionHandler) openRegistry(w http.ResponseWriter) (string, RegistryIndex, bool) {
	url := h.config.RegistryURL
	if url == "" {
		httputil.JSONError(w, http.StatusBadRequest, "registry URL not configured")
		return "", nil, false
	}
	index, err := h.registry.FetchIndex(url)
	if err != nil {
		httputil.Fault(w, "fetch registry", err)
		return "", nil, false
	}
	return url, index, true
}

// findRegistryEntry returns the entry with this name, or nil.
func findRegistryEntry(index RegistryIndex, name string) *RegistryEntry {
	for i := range index {
		if index[i].Name == name {
			return &index[i]
		}
	}
	return nil
}

// HandleGet returns a single extension by ID.
func (h *ExtensionHandler) HandleGet(w http.ResponseWriter, r *http.Request, id string) {

	ext, ok := h.extensionOr404(w, id)
	if !ok {
		return
	}

	statuses := h.sandbox.ListRunning()
	httputil.JSONResponse(w, http.StatusOK, h.toResponse(ext, statuses))
}

// HandleUpdate updates an existing extension.
func (h *ExtensionHandler) HandleUpdate(w http.ResponseWriter, r *http.Request, id string) {

	ext, ok := h.extensionOr404(w, id)
	if !ok {
		return
	}

	oldMemoryLimit := ext.SandboxConfig.MemoryLimit

	var req UpdateExtensionRequest
	if !httputil.DecodeJSON(w, r, &req) {
		return
	}

	// A client must not be able to widen a sandbox past the extension's purpose:
	// mounts are limited to the app's own roots and capabilities that reach past the
	// container are refused. Nothing is persisted on rejection, and
	// buildExtensionCreateArgs re-checks at container creation.
	if err := h.mountPolicy().ValidateSandboxAdditions(req.SandboxConfig.Mounts, req.SandboxConfig.Capabilities); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Apply partial update
	if req.Version != "" {
		ext.Version = req.Version
	}
	if req.Manifest.DisplayName != "" || req.Manifest.Description != "" || req.Manifest.Author != "" || req.Manifest.LicenseURL != "" || req.Manifest.Homepage != "" {
		if req.Manifest.DisplayName != "" {
			ext.Manifest.DisplayName = req.Manifest.DisplayName
		}
		if req.Manifest.Description != "" {
			ext.Manifest.Description = req.Manifest.Description
		}
		if req.Manifest.Author != "" {
			ext.Manifest.Author = req.Manifest.Author
		}
		if req.Manifest.LicenseURL != "" {
			ext.Manifest.LicenseURL = sanitizeURL(req.Manifest.LicenseURL)
		}
		if req.Manifest.Homepage != "" {
			ext.Manifest.Homepage = sanitizeURL(req.Manifest.Homepage)
		}
	}
	if req.Config != nil {
		// Approved host folders have their own endpoint. Accepting one here would let it reach the
		// containers without the checks that endpoint runs.
		if _, declares := req.Config[AgentMountConfigKey]; declares && ext.Manifest.Capabilities.GetAgent() != nil {
			httputil.JSONError(w, http.StatusBadRequest,
				"sandbox_mount is granted through the extension's mount control, not through its configuration")
			return
		}
		// Merge config: preserve existing sensitive values when masked placeholder is sent
		merged := make(map[string]any, len(req.Config))
		for k, v := range req.Config {
			strVal, isStr := v.(string)
			isSensitive := ext.Manifest.ConfigSchema != nil && ext.Manifest.ConfigSchema.GetProperty(k) != nil && ext.Manifest.ConfigSchema.GetProperty(k).Sensitive
			if isSensitive && isStr && strVal == maskedValue {
				// Preserve existing value (user didn't change the field)
				if existing, ok := ext.Config[k]; ok {
					merged[k] = existing
					continue
				}
				// No existing value — treat empty masked as empty
				merged[k] = ""
				continue
			}
			merged[k] = v
		}
		// Keep existing keys not present in the update
		for k, v := range ext.Config {
			if _, exists := merged[k]; !exists {
				merged[k] = v
			}
		}
		ext.Config = merged
		// A `url` property is an address this extension may contact, so a save checks it.
		if err := ValidateConfigValues(ext.Manifest.ConfigSchema, ext.Config); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.ContainerConfig.Image != "" || req.ContainerConfig.Dockerfile != "" {
		if req.ContainerConfig.Image != "" {
			ext.ContainerConfig.Image = req.ContainerConfig.Image
		}
		if req.ContainerConfig.Dockerfile != "" {
			ext.ContainerConfig.Dockerfile = req.ContainerConfig.Dockerfile
		}
		if len(req.ContainerConfig.Entrypoint) > 0 {
			ext.ContainerConfig.Entrypoint = req.ContainerConfig.Entrypoint
		}
		if req.ContainerConfig.WorkingDir != "" {
			ext.ContainerConfig.WorkingDir = req.ContainerConfig.WorkingDir
		}
	}
	// Sandbox config — merge fields
	if req.SandboxConfig.MemoryLimit > 0 {
		ext.SandboxConfig.MemoryLimit = req.SandboxConfig.MemoryLimit
	}
	// Sync config.memory_limit → SandboxConfig.MemoryLimit
	// The edit form saves memory_limit via config_schema, but sandbox reads SandboxConfig.
	// 0 = no limit; negative values are clamped to 0.
	if req.Config != nil {
		if ml, ok := req.Config["memory_limit"]; ok {
			switch v := ml.(type) {
			case float64:
				if v < 0 {
					v = 0
				}
				ext.SandboxConfig.MemoryLimit = int(v)
			case int:
				if v < 0 {
					v = 0
				}
				ext.SandboxConfig.MemoryLimit = v
			}
		}
	}
	if req.SandboxConfig.NetworkIsolation {
		ext.SandboxConfig.NetworkIsolation = req.SandboxConfig.NetworkIsolation
	}
	if len(req.SandboxConfig.ForwardPorts) > 0 {
		ext.SandboxConfig.ForwardPorts = req.SandboxConfig.ForwardPorts
	}
	if len(req.SandboxConfig.Mounts) > 0 {
		ext.SandboxConfig.Mounts = req.SandboxConfig.Mounts
	}
	if len(req.SandboxConfig.Envs) > 0 {
		ext.SandboxConfig.Envs = req.SandboxConfig.Envs
	}
	if len(req.SandboxConfig.Capabilities) > 0 {
		ext.SandboxConfig.Capabilities = req.SandboxConfig.Capabilities
	}
	// LSP config — merge fields from manifest
	if req.Manifest.Capabilities != nil && req.Manifest.Capabilities.LSP != nil {
		if ext.Manifest.Capabilities == nil {
			ext.Manifest.Capabilities = &Capabilities{}
		}
		if ext.Manifest.Capabilities.LSP == nil {
			ext.Manifest.Capabilities.LSP = &LSPConfig{}
		}
		if req.Manifest.Capabilities.LSP.Command != "" {
			ext.Manifest.Capabilities.LSP.Command = req.Manifest.Capabilities.LSP.Command
		}
		if len(req.Manifest.Capabilities.LSP.Args) > 0 {
			ext.Manifest.Capabilities.LSP.Args = req.Manifest.Capabilities.LSP.Args
		}
		if len(req.Manifest.Capabilities.LSP.FileTypes) > 0 {
			ext.Manifest.Capabilities.LSP.FileTypes = req.Manifest.Capabilities.LSP.FileTypes
		}
		if len(req.Manifest.Capabilities.LSP.Syntax) > 0 {
			ext.Manifest.Capabilities.LSP.Syntax = req.Manifest.Capabilities.LSP.Syntax
		}
		if req.Manifest.Capabilities.LSP.Language != "" {
			ext.Manifest.Capabilities.LSP.Language = req.Manifest.Capabilities.LSP.Language
		}
		if len(req.Manifest.Capabilities.LSP.InitParamsOverride) > 0 {
			if ext.Manifest.Capabilities.LSP.InitParamsOverride == nil {
				ext.Manifest.Capabilities.LSP.InitParamsOverride = make(map[string]any)
			}
			for k, v := range req.Manifest.Capabilities.LSP.InitParamsOverride {
				ext.Manifest.Capabilities.LSP.InitParamsOverride[k] = v
			}
		}
		if req.Manifest.Capabilities.LSP.EditorEnabled != nil {
			ext.Manifest.Capabilities.LSP.EditorEnabled = req.Manifest.Capabilities.LSP.EditorEnabled
		}
		if req.Manifest.Capabilities.LSP.ToolCallsEnabled != nil {
			ext.Manifest.Capabilities.LSP.ToolCallsEnabled = req.Manifest.Capabilities.LSP.ToolCallsEnabled
		}
		if req.Manifest.Capabilities.LSP.ChatDiagnosticsEnabled != nil {
			ext.Manifest.Capabilities.LSP.ChatDiagnosticsEnabled = req.Manifest.Capabilities.LSP.ChatDiagnosticsEnabled
		}
		// Checked after the merge, because that is the shape that gets stored.
		if err := ext.Manifest.Capabilities.GetLSP().Validate(); err != nil {
			http.Error(w, "extension declares an unusable language configuration: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	// A translation pack has no user-editable field, so the stored declaration is re-checked on
	// every save, after the merges above: an extension must not be able to rewrite its own way
	// out of validation.
	if err := ext.Manifest.Capabilities.GetI18N().Validate(); err != nil {
		http.Error(w, "extension declares an unusable translation pack: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Same rule for the stylesheet: it is the one stored declaration the browser resolves into a
	// <link> and into the pre-paint storage, so a save must not be able to aim it elsewhere.
	if err := ext.Manifest.Capabilities.GetCSS().Validate(); err != nil {
		http.Error(w, "extension declares an unusable stylesheet: "+err.Error(), http.StatusBadRequest)
		return
	}

	// The stored declaration decides what the model may call and where, so an update is vetted too.
	if err := validateTools(ext.Manifest.Capabilities, ext.Manifest.ConfigSchema); err != nil {
		http.Error(w, "extension declares an unusable tool: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.store.Update(id, ext); err != nil {
		httputil.Fault(w, "update extension", err)
		return
	}

	// Apply the saved memory limit to a running sandbox
	if ext.IsLSP() && ext.SandboxConfig.MemoryLimit != oldMemoryLimit {
		if memErr := h.sandbox.ApplyMemoryLimit(id, ext.SandboxConfig.MemoryLimit); memErr != nil {
			logging.For(logging.Extensions).Infof("ext=%s WARNING: memory limit %dMB not applied to running sandbox: %v", id, ext.SandboxConfig.MemoryLimit, memErr)
		}
	}

	statuses := h.sandbox.ListRunning()
	httputil.JSONResponse(w, http.StatusOK, h.toResponse(ext, statuses))

	// Notify frontend + reload agent registry so config changes take effect immediately
	if h.onExtensionChanged != nil {
		h.onExtensionChanged()
	}
}

// HandleDelete removes an extension.
func (h *ExtensionHandler) HandleDelete(w http.ResponseWriter, r *http.Request, id string) {

	// Read the record first: the files to delete are derived from its author and name.
	ext, _ := h.store.GetByID(id)

	// Stop sandbox if running
	_ = h.sandbox.Destroy(id)

	// Remove scoped storage for this extension so its data is not orphaned,
	// even if the extension record itself is already gone.
	_ = h.store.StorageDeleteAll(id)

	if err := h.store.Delete(id); err != nil {
		httputil.JSONError(w, http.StatusNotFound, "extension not found")
		return
	}

	// Take the files with the row. The directory is what gets served and what the local scan offers
	// for install, so leaving it behind resurrects the extension with its old manifest.
	response := map[string]string{"status": "deleted"}
	if warning := h.removeExtensionFiles(ext); warning != "" {
		// Reported, not swallowed: from the UI the extension is gone either way.
		response["leftover"] = warning
	}

	httputil.JSONResponse(w, http.StatusOK, response)

	// Notify frontend to refresh UI injection slots
	if h.onExtensionChanged != nil {
		h.onExtensionChanged()
	}
}

// HandleSetEnabled toggles the enabled state of an extension.
func (h *ExtensionHandler) HandleSetEnabled(w http.ResponseWriter, r *http.Request, id string) {

	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !httputil.DecodeJSON(w, r, &req) {
		return
	}

	if err := h.store.SetEnabled(id, req.Enabled); err != nil {
		httputil.JSONError(w, http.StatusNotFound, "extension not found")
		return
	}

	// Disabling stops the sandbox. Enabling needs nothing here: an LSP sandbox is
	// started lazily on first use.
	if !req.Enabled {
		_ = h.sandbox.Stop(id)
	}

	ext, ok := h.extensionOr404(w, id)
	if !ok {
		return
	}
	statuses := h.sandbox.ListRunning()
	httputil.JSONResponse(w, http.StatusOK, h.toResponse(ext, statuses))

	// Notify frontend to refresh UI injection slots
	if h.onExtensionChanged != nil {
		h.onExtensionChanged()
	}
}

// HandleSetDependencyCache grants — or revokes — the one host directory an extension's language
// server reads its resolved dependencies from. Its own endpoint because an empty value means
// "revoke" while the form handler copies only non-empty fields, and because applying it must
// destroy the running container. Only a human names the directory: a manifest carrying
// dependency_host_path is refused at install.
func (h *ExtensionHandler) HandleSetDependencyCache(w http.ResponseWriter, r *http.Request, id string) {

	// A pointer, because "empty" and "absent" must not mean the same thing here. An empty value is a
	// revoke — that is the documented way to take a grant back — but a body whose key is misspelled
	// would otherwise decode to the empty string and silently revoke a working grant while answering
	// 200. Absent is a caller mistake, so it is answered as one.
	var req struct {
		HostPath *string `json:"host_path"`
	}
	if !httputil.DecodeJSON(w, r, &req) {
		return
	}
	if req.HostPath == nil {
		httputil.JSONError(w, http.StatusBadRequest, "host_path is required; send an empty value to revoke the grant")
		return
	}

	ext, ok := h.extensionOr404(w, id)
	if !ok {
		return
	}
	declared := ext.SandboxConfig.Dependency
	if !ext.IsLSP() || declared == nil {
		httputil.JSONError(w, http.StatusBadRequest, "this extension declares no dependency cache, so there is nothing to grant")
		return
	}

	granted := strings.TrimSpace(*req.HostPath)
	if granted != "" {
		validated, err := ValidateDependencyGrant(granted)
		if err != nil {
			httputil.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		granted = validated
	}

	previous := ext.SandboxConfig.DependencyHostPath
	if granted == previous {
		// Nothing changed: answering without stopping the container, so clicking Apply twice does
		// not cost a cold start of a server someone is using.
		httputil.JSONResponse(w, http.StatusOK, h.toResponse(ext, h.sandbox.ListRunning()))
		return
	}

	ext.SandboxConfig.DependencyHostPath = granted
	if err := h.store.Update(id, ext); err != nil {
		httputil.Fault(w, "save the dependency-cache grant", err)
		return
	}

	// The mount set is fixed at container creation, so a running container now has the wrong view
	// of the world. Stopping it is the whole application: the bridge connection dies with it and
	// the next request starts a container with the new mount.
	recreated := "no sandbox was running, so the next start picks it up"
	if h.sandbox.IsRunning(id) {
		h.sandbox.Stop(id)
		recreated = "running sandbox stopped, so the next use recreates it with this mount"
	}
	if granted == "" {
		logging.For(logging.Extensions).Infof("ext=%s dependency grant revoked (was %s); %s reads the image's own copy again — %s", id, previous, declared.Env, recreated)
	} else {
		// The mode is stated, not computed: this extension's sandbox is the only container that binds the
		// grant, and it binds it read-only.
		logging.For(logging.Extensions).Infof("ext=%s dependency grant %s -> %s (mounted read-only at %s, in this extension's sandbox only) — %s", id, previous, granted, declared.ContainerPath, recreated)
	}

	httputil.JSONResponse(w, http.StatusOK, h.toResponse(ext, h.sandbox.ListRunning()))
	if h.onExtensionChanged != nil {
		h.onExtensionChanged()
	}
}

const AgentMountConfigKey = "sandbox_mount"

// HandleSetAgentMount writes the folders a person approved for one extension's agent sandbox. Sending
// an empty list clears the approval, which is why this is its own endpoint rather than a field of the
// configuration form.
func (h *ExtensionHandler) HandleSetAgentMount(w http.ResponseWriter, r *http.Request, id string) {
	// A pointer, so a request with no mounts key is rejected instead of clearing a working approval.
	var req struct {
		Mounts *[]types.SandboxMount `json:"mounts"`
	}
	if !httputil.DecodeJSON(w, r, &req) {
		return
	}
	if req.Mounts == nil {
		httputil.JSONError(w, http.StatusBadRequest, "mounts is required; send an empty list to revoke the grant")
		return
	}

	ext, ok := h.extensionOr404(w, id)
	if !ok {
		return
	}
	declared := ext.Manifest.Capabilities.GetAgent()
	if declared == nil || declared.Sandbox == nil || len(declared.Sandbox.Mounts) == 0 {
		httputil.JSONError(w, http.StatusBadRequest, "this extension declares no agent sandbox mount, so there is nothing to grant")
		return
	}

	// Every folder is checked before anything is saved, so a refusal partway through the list cannot leave
	// an earlier folder approved.
	granted := make([]types.SandboxMount, 0, len(*req.Mounts))
	for i, m := range *req.Mounts {
		validated, err := ValidateAgentMountGrant(m)
		if err != nil {
			httputil.JSONError(w, http.StatusBadRequest, fmt.Sprintf("mount %d: %v", i, err))
			return
		}
		granted = append(granted, validated)
	}

	previous := describeMounts(agentMountGrant(ext))
	setAgentMountGrant(ext, granted)
	if err := h.store.Update(id, ext); err != nil {
		httputil.Fault(w, "save the agent mount grant", err)
		return
	}

	// An agent extension has no container of its own. The executor recreates the containers of the tasks
	// that run it when this list changes; see TaskExecutor.agentGrantHash.
	if len(granted) == 0 {
		logging.For(logging.Extensions).Infof("ext=%s agent mount grant revoked (was %s) — a task running it keeps its container's view until that container is recreated", id, previous)
	} else {
		logging.For(logging.Extensions).Infof("ext=%s agent mount grant %s -> %s — it reaches the containers of the tasks that run this agent, which are recreated when the grant changes", id, previous, describeMounts(granted))
	}

	httputil.JSONResponse(w, http.StatusOK, h.toResponse(ext, h.sandbox.ListRunning()))
	if h.onExtensionChanged != nil {
		h.onExtensionChanged()
	}
}

// ParseAgentMountGrant leaves a missing mode empty: each reader decides what a missing mode means.
func ParseAgentMountGrant(raw any) ([]types.SandboxMount, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("expected an array of mounts, got %T", raw)
	}
	out := make([]types.SandboxMount, 0, len(items))
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("mount %d: expected an object, got %T", i, item)
		}
		hp, _ := m["host_path"].(string)
		cp, _ := m["container_path"].(string)
		mode, _ := m["mode"].(string)
		out = append(out, types.SandboxMount{HostPath: hp, ContainerPath: cp, Mode: mode})
	}
	return out, nil
}

// agentMountGrant counts a value that cannot be read as nothing approved.
func agentMountGrant(ext *Extension) []types.SandboxMount {
	mounts, err := ParseAgentMountGrant(ext.Config[AgentMountConfigKey])
	if err != nil {
		return nil
	}
	return mounts
}

// setAgentMountGrant deletes the key when the list is empty, so a cleared approval and one that was never
// made look the same.
func setAgentMountGrant(ext *Extension, mounts []types.SandboxMount) {
	if ext.Config == nil {
		ext.Config = map[string]any{}
	}
	if len(mounts) == 0 {
		delete(ext.Config, AgentMountConfigKey)
		return
	}
	items := make([]any, 0, len(mounts))
	for _, m := range mounts {
		items = append(items, map[string]any{"host_path": m.HostPath, "container_path": m.ContainerPath, "mode": m.Mode})
	}
	ext.Config[AgentMountConfigKey] = items
}

func describeMounts(mounts []types.SandboxMount) string {
	parts := make([]string, 0, len(mounts))
	for _, m := range mounts {
		parts = append(parts, fmt.Sprintf("%s -> %s (%s)", m.HostPath, m.ContainerPath, m.Mode))
	}
	return strings.Join(parts, ", ")
}

// inferExtensionType returns the extension type implied by its capabilities, in priority order
// (lsp > ui > css > agent > llm > i18n), or "" when none maps to a type. i18n is last because a
// pack is pure data and validateI18N refuses a pack with a sibling capability, so this order only
// decides what a rejected manifest is called in the log.
func inferExtensionType(caps *Capabilities) string {
	if caps == nil {
		return ""
	}
	switch {
	case caps.Tools != nil:
		return ExtensionTypeTool
	case caps.LSP != nil:
		return ExtensionTypeLSP
	case caps.UI != nil:
		return ExtensionTypeUI
	case caps.CSS != nil:
		return ExtensionTypeCSS
	case caps.Agent != nil:
		return ExtensionTypeAgent
	case caps.LLM != nil:
		return ExtensionTypeLLM
	case caps.I18N != nil:
		return ExtensionTypeI18N
	}
	return ""
}

// localExtension is a discovered extension in the local extensions directory.
type localExtension struct {
	Author   string
	Name     string
	Dir      string
	Type     string
	Manifest ExtensionManifest
}

// scanLocalExtensions walks the extensions directory and returns every local
// extension found there (a directory containing a manifest.json), sorted by
// name. Unreadable/malformed entries are skipped. The name is the manifest's
// parent directory (extensions/<author>/<name>/manifest.json).
func scanLocalExtensions(root string) ([]localExtension, error) {
	var found []localExtension
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Skip unreadable entries (e.g. missing dir, permission errors).
			return nil
		}
		if d.IsDir() || d.Name() != "manifest.json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var manifest ExtensionManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return nil
		}
		dir := filepath.Dir(path)
		name := filepath.Base(dir)
		// The extension is expected to live at extensions/<author>/<name>/.
		// The author is the on-disk namespace directory; if the manifest sits
		// directly under the extensions root there is no author folder, so it
		// is treated as a "local" extension.
		parent := filepath.Dir(dir)
		author := filepath.Base(parent)
		if filepath.Clean(parent) == filepath.Clean(root) {
			author = "local"
		}
		found = append(found, localExtension{
			Author:   author,
			Name:     name,
			Dir:      dir,
			Type:     inferExtensionType(manifest.Capabilities),
			Manifest: manifest,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].Author != found[j].Author {
			return found[i].Author < found[j].Author
		}
		return found[i].Name < found[j].Name
	})
	return found, nil
}

// findLocalExtension returns the local extension with the given author and
// name, or nil if no such extension exists in the local extensions directory.
func findLocalExtension(root, author, name string) *localExtension {
	found, err := scanLocalExtensions(root)
	if err != nil {
		return nil
	}
	for i := range found {
		if found[i].Author == author && found[i].Name == name {
			return &found[i]
		}
	}
	return nil
}

// localRegistryEntry is the one mapping from a folder on disk to the registry-shaped entry both the
// browse listing and the install path use.
func localRegistryEntry(le localExtension) RegistryEntry {
	return RegistryEntry{
		Name:         le.Name,
		Version:      le.Manifest.Version,
		Type:         le.Type,
		DisplayName:  le.Manifest.DisplayName,
		Description:  le.Manifest.Description,
		Author:       le.Author,
		Homepage:     le.Manifest.Homepage,
		LicenseURL:   le.Manifest.LicenseURL,
		Image:        le.Manifest.Image,
		Capabilities: resolvedSandboxManifest(le.Manifest).Capabilities,
		LocalPath:    le.Dir,
		localDir:     le.Dir,
	}
}

// HandleLocalExtensions lists the extensions in the local extensions directory as registry-style
// entries, whether or not they are published. Each entry's name comes from the manifest's parent
// directory (extensions/<author>/<name>/manifest.json).
func (h *ExtensionHandler) HandleLocalExtensions(w http.ResponseWriter, r *http.Request) {

	found, err := scanLocalExtensions(h.config.ExtensionsDirOrDefault())
	if err != nil {
		httputil.Fault(w, "scan the extensions directory", err)
		return
	}

	index := make(RegistryIndex, 0, len(found))
	for _, le := range found {
		index = append(index, localRegistryEntry(le))
	}

	revalidateListBeforeUse(w)
	httputil.JSONResponse(w, http.StatusOK, index)
}

// HandleRegistryIndex fetches the registry index.
func (h *ExtensionHandler) HandleRegistryIndex(w http.ResponseWriter, r *http.Request) {

	_, index, ok := h.openRegistry(w)
	if !ok {
		return
	}

	revalidateListBeforeUse(w)
	httputil.JSONResponse(w, http.StatusOK, index)
}

// HandleInstall installs from the registry. Fast validation returns HTTP errors
// synchronously; the actual install runs in a detached goroutine (so it survives
// a client disconnect) while this handler streams SSE progress.
func (h *ExtensionHandler) HandleInstall(w http.ResponseWriter, r *http.Request) {

	var req InstallExtensionRequest
	if !httputil.DecodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		httputil.JSONError(w, http.StatusBadRequest, "name is required")
		return
	}

	url, index, ok := h.openRegistry(w)
	if !ok {
		return
	}

	entry := findRegistryEntry(index, req.Name)
	if entry == nil {
		httputil.JSONError(w, http.StatusNotFound, "extension not found in registry: "+req.Name)
		return
	}
	// 400 before the SSE stream starts; installInBackground checks again before it writes.
	if err := refuseUninstallable(entry); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Check by identity — records use timestamped IDs, so an ID lookup misses
	// duplicates. Author is normalized to the on-disk namespace slug.
	author := authorDir(entry.Author)
	if existing, _ := h.store.GetByAuthorName(author, entry.Name); existing != nil {
		httputil.JSONError(w, http.StatusConflict, "extension already installed: "+author+"/"+entry.Name)
		return
	}

	// Prevent racing an in-flight install (e.g. after a page reload).
	if !h.inFlightOps.begin(req.Name) {
		httputil.JSONError(w, http.StatusConflict, "install already in progress: "+req.Name)
		return
	}

	events := make(chan sseEvent, 16)
	go h.installInBackground(req.Name, entry, url, events)

	h.streamOperation(w, r, events)
}

// HandleInstallLocal installs an extension from the local extensions directory
// on the filesystem (an "unpublished" extension). Unlike HandleInstall it does
// not consult the registry, so it works even when no registry is reachable. The
// actual install runs in a detached goroutine and streams SSE progress.
func (h *ExtensionHandler) HandleInstallLocal(w http.ResponseWriter, r *http.Request) {

	var req InstallExtensionRequest
	if !httputil.DecodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" || req.Author == "" {
		httputil.JSONError(w, http.StatusBadRequest, "author and name are required")
		return
	}

	local := findLocalExtension(h.config.ExtensionsDirOrDefault(), req.Author, req.Name)
	if local == nil {
		httputil.JSONError(w, http.StatusNotFound, "local extension not found: "+req.Author+"/"+req.Name)
		return
	}

	entry := localRegistryEntry(*local)

	if existing, _ := h.store.GetByAuthorName(entry.Author, entry.Name); existing != nil {
		httputil.JSONError(w, http.StatusConflict, "extension already installed: "+entry.Author+"/"+entry.Name)
		return
	}

	// Prevent racing an in-flight install (e.g. after a page reload).
	if !h.inFlightOps.begin(entry.Name) {
		httputil.JSONError(w, http.StatusConflict, "install already in progress: "+req.Name)
		return
	}

	events := make(chan sseEvent, 16)
	go h.installInBackground(entry.Name, &entry, "", events)

	h.streamOperation(w, r, events)
}

// installInBackground runs the install detached from the request context so it
// completes even if the client disconnects, reporting progress on events.
func (h *ExtensionHandler) installInBackground(name string, entry *RegistryEntry, url string, events chan<- sseEvent) {
	// The terminal event goes out last: a client that sees "install failed" while the rollback is still
	// running re-lists the folder the rollback has not deleted yet, offered as installable, and the
	// retry its message invites is refused as "install already in progress".
	gitPinned := entry.Repo != "" && entry.Commit != ""
	var failure error
	var materialized, committed bool
	defer func() {
		if failure != nil && materialized && !committed {
			h.rollbackFailedInstall(entry, gitPinned)
		}
		h.inFlightOps.end(name)
		if failure != nil {
			events <- sseEvent{Extension: name, Progress: 100, Message: "Install failed", Done: true, Error: failure.Error()}
		}
		close(events)
	}()

	emit := func(pct float64, msg string) {
		events <- sseEvent{Extension: name, Progress: pct, Message: msg}
	}
	// Every failure site stays terminal (`fail(err); return`) and is answered by the deferred tail above.
	fail := func(err error) { failure = err }

	// Local (filesystem) extension: files are already materialized, so load the
	// manifest in place rather than fetching from a registry or git repo.
	var manifest ExtensionManifest
	root := h.config.ExtensionsDirOrDefault()
	if err := refuseUninstallable(entry); err != nil {
		fail(err)
		return
	}
	targetDir := filepath.Join(root, authorDir(entry.Author), entry.Name)
	if entry.localDir != "" {
		targetDir = entry.localDir

		readable := extensionDir(root, entry.Author, entry.Name)
		if filepath.Clean(readable) != filepath.Clean(targetDir) {
			fail(fmt.Errorf(
				"local extension folder %q is not the author folder the installed record reads it from — rename it to %q and install again",
				filepath.Base(filepath.Dir(targetDir)),
				filepath.Base(filepath.Dir(readable)),
			))
			return
		}
		manifestData, err := os.ReadFile(filepath.Join(targetDir, "manifest.json"))
		if err != nil {
			fail(fmt.Errorf("local extension has no manifest.json: %w", err))
			return
		}
		if err := json.Unmarshal(manifestData, &manifest); err != nil {
			fail(fmt.Errorf("invalid manifest.json in local extension: %w", err))
			return
		}
	} else if gitPinned {
		emit(10, "Fetching extension...")
		if err := h.registry.CheckoutExtension(entry.Repo, entry.Commit, entry.Subdir, targetDir); err != nil {
			fail(fmt.Errorf("failed to fetch extension (commit %s): %w", entry.Commit, err))
			return
		}
		materialized = true
		manifestData, err := os.ReadFile(filepath.Join(targetDir, "manifest.json"))
		if err != nil {
			fail(fmt.Errorf("checked-out extension has no manifest.json: %w", err))
			return
		}
		if err := json.Unmarshal(manifestData, &manifest); err != nil {
			fail(fmt.Errorf("invalid manifest.json in checked-out extension: %w", err))
			return
		}
	} else {
		manifest = ExtensionManifest{
			DisplayName: entry.DisplayName,
			Description: entry.Description,
			Author:      entry.Author,
			LicenseURL:  entry.LicenseURL,
			Version:     entry.Version,
		}
	}

	// Sanitize manifest URLs to prevent javascript:/data: injection
	sanitizeManifestURLs(&manifest)

	installSource := "registry"
	if entry.localDir != "" {
		installSource = "local"
	}

	// Create extension record
	author := authorDir(entry.Author)
	id := fmt.Sprintf("%s-%s-%d", author, strings.ToLower(entry.Name), time.Now().Unix())
	// A manifest may not grant itself mounts outside the app's own roots or
	// capabilities that reach past the container. The install is rejected rather
	// than stored and then silently skipped at every container start.
	sandboxCfg := sandboxConfigFromManifest(lspSandboxFromManifest(&manifest))
	if err := h.mountPolicy().ValidateSandboxAdditions(sandboxCfg.Mounts, sandboxCfg.Capabilities); err != nil {
		fail(fmt.Errorf("extension %s declares an unusable sandbox configuration: %w", entry.Name, err))
		return
	}
	if err := manifest.Capabilities.GetLSP().Validate(); err != nil {
		fail(fmt.Errorf("extension %s declares an unusable language configuration: %w", entry.Name, err))
		return
	}
	// Checked on the fetched manifest and against the files it shipped into targetDir, so a bad
	// locale, a foreign-origin or unparsable catalog, or a prompt overlay that will not compile is
	// refused now rather than discovered in the browser.
	if err := validateI18N(manifest.Capabilities, targetDir); err != nil {
		fail(fmt.Errorf("extension %s declares an unusable translation pack: %w", entry.Name, err))
		return
	}
	// The stylesheet of a css pack gets the same containment the pack's files get, and for the
	// same reason: the browser resolves it into a <link> before anything here can look again.
	if err := manifest.Capabilities.GetCSS().Validate(); err != nil {
		fail(fmt.Errorf("extension %s declares an unusable stylesheet: %w", entry.Name, err))
		return
	}
	// The declaration comes from registry text, so it is vetted before it is stored.
	if err := validateTools(manifest.Capabilities, manifest.ConfigSchema); err != nil {
		fail(fmt.Errorf("extension %s declares an unusable tool: %w", entry.Name, err))
		return
	}
	// An image that names no registry cannot be pulled headless, so reject the entry here rather
	// than failing at every later start.
	if err := h.registry.ValidateImage(entry.Image); err != nil {
		fail(fmt.Errorf("extension %s declares an unusable image: %w", entry.Name, err))
		return
	}

	imageRef, err := h.acquireInstallImage(entry, url, targetDir, gitPinned, emit)
	if err != nil {
		fail(err)
		return
	}
	if imageRef == "" && installNeedsImage(entry) {
		fail(fmt.Errorf("extension %s is a language server but ships no image and no Dockerfile to build one from, so it has nothing to run in a sandbox", entry.Name))
		return
	}

	ext := &Extension{
		ID:       id,
		Name:     entry.Name,
		Author:   author,
		Version:  entry.Version,
		Type:     entry.Type,
		Manifest: manifest,
		Config:   map[string]any{},
		ContainerConfig: ContainerConfig{
			Image: imageRef,
		},
		SandboxConfig: sandboxCfg,
		InstallSource: installSource,

		ImageRef: imageRef,
		Enabled:  true,
		Repo:     entry.Repo,
		Commit:   entry.Commit,
		Subdir:   entry.Subdir,
	}
	if err := h.store.Create(ext); err != nil {
		fail(fmt.Errorf("failed to create extension: %w", err))
		return
	}
	committed = true

	statuses := h.sandbox.ListRunning()
	result := h.toResponse(ext, statuses)
	events <- sseEvent{Extension: name, Progress: 100, Message: "Installed", Done: true, Result: &result}

	// Notify frontend to refresh UI injection slots
	if h.onExtensionChanged != nil {
		h.onExtensionChanged()
	}
}

// acquireInstallImage returns the image an installing extension will run, or "" if it ships neither
// a pre-built image nor a Dockerfile. It is the install-side twin of extensionImageSource. A
// declared image is not pulled here: the sandbox pulls it on first use, so an install never
// downloads an image the user may never start.
func (h *ExtensionHandler) acquireInstallImage(entry *RegistryEntry, registryURL, targetDir string, gitPinned bool, emit func(float64, string)) (string, error) {
	if entry.Image != "" {
		return entry.Image, nil
	}
	dockerfile, source, err := h.installDockerfile(entry, registryURL, targetDir, gitPinned, emit)
	if err != nil {
		return "", err
	}
	if dockerfile == "" {
		return "", nil
	}
	imageRef := ExtensionImageRef(entry.Name, entry.Version)
	emit(30, "Building image...")
	if err := h.registry.BuildExtensionImage(dockerfile, imageRef); err != nil {
		return "", fmt.Errorf("failed to build image from %s: %w", source, err)
	}
	return imageRef, nil
}

// installDockerfile reads the Dockerfile to build from, wherever the package lives: the extension's
// own folder (local install), the checkout (git-pinned), or the registry's URL. ("", "", nil) means
// it shipped no Dockerfile. A read failure is an error, not "no Dockerfile": an unreadable one used
// to look absent and the install recorded an extension whose image would never exist.
func (h *ExtensionHandler) installDockerfile(entry *RegistryEntry, registryURL, targetDir string, gitPinned bool, emit func(float64, string)) (content, source string, err error) {
	var path string
	switch {
	case entry.localDir != "":
		emit(20, "Reading Dockerfile...")
		path = filepath.Join(entry.localDir, "Dockerfile")
	case gitPinned:
		emit(20, "Reading Dockerfile...")
		path = filepath.Join(targetDir, "Dockerfile")
	case entry.DockerfileURL != "":
		emit(20, "Fetching Dockerfile...")
		df, fetchErr := h.registry.FetchDockerfile(ResolveURL(registryURL, entry.DockerfileURL))
		if fetchErr != nil {
			return "", "", fmt.Errorf("failed to fetch dockerfile: %w", fetchErr)
		}
		return df, entry.DockerfileURL, nil
	default:
		return "", "", nil
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("failed to read %s: %w", path, err)
	}
	return string(data), path, nil
}

// installNeedsImage reports whether an extension cannot be installed without an image. That is the type
// that gets a container: sandboxes are created for rows of type lsp (GetByType); every other type is
// just files.
func installNeedsImage(entry *RegistryEntry) bool {
	if entry.Type == ExtensionTypeLSP {
		return true
	}
	// Registry entries are not normalized, so an index may omit the type; infer it the same way the
	// local scan does.
	return entry.Type == "" && inferExtensionType(entry.Capabilities) == ExtensionTypeLSP
}

// rollbackFailedInstall deletes the files an install checked out before failing. It never touches a
// local install's folder: that belongs to whoever put it there, and install-local refuses a folder it
// cannot read back as its own.
func (h *ExtensionHandler) rollbackFailedInstall(entry *RegistryEntry, gitPinned bool) {
	if !gitPinned || entry.localDir != "" {
		return
	}
	if warning := h.removeMaterializedDir(entry.Name, entry.Author, entry.Name); warning != "" {
		logging.For(logging.Extensions).Warnf("%s/%s install failed and left files behind: %s", entry.Author, entry.Name, warning)
	}
}

// streamOperation streams SSE events until Done or client disconnect. The
// operation runs detached, so returning on disconnect does not cancel it.
func (h *ExtensionHandler) streamOperation(w http.ResponseWriter, r *http.Request, events <-chan sseEvent) {
	sse, ok := httputil.NewSSE(w)
	if !ok {
		// Streaming unsupported; drain the channel in the background so the
		// detached operation can still finish without blocking on sends.
		go func() {
			for range events {
			}
		}()
		return
	}

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			// The browser is gone; the operation is not. Draining is what keeps it alive — an operation
			// blocked on a send to a reader that left never reaches its store.Create, and its name stays
			// in inFlightOps, so every later attempt at that extension answers "install already in
			// progress" with nothing in the UI able to say why.
			for range events {
			}
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			sse.JSON(ev)
			if ev.Done {
				return
			}
		}
	}
}

// HandleCheckUpdates checks for available updates.
func (h *ExtensionHandler) HandleCheckUpdates(w http.ResponseWriter, r *http.Request) {

	_, index, ok := h.openRegistry(w)
	if !ok {
		return
	}

	// Compare with installed extensions
	extensions, err := h.store.List()
	if err != nil {
		httputil.Fault(w, "list extensions", err)
		return
	}

	updates := make([]ExtensionUpdate, 0)
	for _, ext := range extensions {
		for _, entry := range index {
			if entry.Name != ext.Name {
				continue
			}
			domainsAdded := len(newEgressHosts(ext.Manifest.Capabilities, entry.Capabilities)) > 0
			// Git-pinned extensions update when the curated (vetted) commit changes.
			if ext.Commit != "" && entry.Commit != "" {
				if ext.Commit != entry.Commit {
					updates = append(updates, ExtensionUpdate{
						ID:               ext.ID,
						Name:             ext.Name,
						CurrentVersion:   ext.Version,
						AvailableVersion: entry.Version,
						CurrentCommit:    ext.Commit,
						AvailableCommit:  entry.Commit,
						NewPermissions:   newPermissions(ext.Manifest.Capabilities, entry.Capabilities),
						DomainsAdded:     domainsAdded,
						Changelog:        h.changelogForUpdate(entry, ext.Commit),
					})
				}
				break
			}
			if UpdateAvailable(ext.Version, entry.Version) {
				updates = append(updates, ExtensionUpdate{
					ID:               ext.ID,
					Name:             ext.Name,
					CurrentVersion:   ext.Version,
					AvailableVersion: entry.Version,
					NewPermissions:   newPermissions(ext.Manifest.Capabilities, entry.Capabilities),
					DomainsAdded:     domainsAdded,
				})
			}
			break
		}
	}

	httputil.JSONResponse(w, http.StatusOK, updates)
}

// changelogForUpdate fetches the extension's published changelog and returns
// the entries newer than sinceHash (oldest → newest). Changelogs are
// best-effort metadata: any failure (not published, fetch error) yields an
// empty list and never blocks update detection.
func (h *ExtensionHandler) changelogForUpdate(entry RegistryEntry, sinceHash string) []ChangelogEntry {
	if sinceHash == "" {
		return nil
	}
	// The changelog path is built from entry.Name; this metadata is best-effort, so a bad
	// entry is skipped rather than reported.
	if err := refuseUninstallable(&entry); err != nil {
		return nil
	}
	data, err := h.registry.FetchChangelog(h.config.RegistryURL, entry.Author, entry.Name)
	if err != nil {
		return nil
	}
	return ChangelogSince(ParseChangelog(data), sinceHash)
}

// HandleUpdateImage updates an extension's image from the registry.
// Rebuilds from Dockerfile if available, otherwise pulls latest pre-built image.
func (h *ExtensionHandler) HandleUpdateImage(w http.ResponseWriter, r *http.Request, id string) {

	ext, ok := h.extensionOr404(w, id)
	if !ok {
		return
	}
	if ext.InstallSource != "registry" {
		httputil.JSONError(w, http.StatusBadRequest, "only registry-installed extensions can be updated")
		return
	}

	url := h.config.RegistryURL
	if url == "" {
		httputil.JSONError(w, http.StatusBadRequest, "registry URL not configured")
		return
	}

	// Read the body first: rejecting a request must not leave the extension marked busy.
	// A request with no body sends no acknowledgment.
	var req struct {
		AcknowledgeNewDomains bool `json:"acknowledge_new_domains"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			httputil.JSONError(w, http.StatusBadRequest, "acknowledge_new_domains must be true or false")
			return
		}
	}

	// Prevent a concurrent update of the same extension.
	if !h.inFlightOps.begin(ext.Name) {
		httputil.JSONError(w, http.StatusConflict, "update already in progress: "+ext.Name)
		return
	}

	events := make(chan sseEvent, 16)
	go h.updateImageInBackground(id, ext.Name, url, req.AcknowledgeNewDomains, events)

	h.streamOperation(w, r, events)
}

func summarizeHosts(hosts []string) string {
	const shown = 5
	if len(hosts) <= shown {
		return strings.Join(hosts, ", ")
	}
	return strings.Join(hosts[:shown], ", ") + fmt.Sprintf(" (+%d more)", len(hosts)-shown)
}

// updateImageInBackground updates an extension's image, detached like install.
func (h *ExtensionHandler) updateImageInBackground(id, name, url string, ackNewDomains bool, events chan<- sseEvent) {
	defer h.inFlightOps.end(name)
	defer close(events)

	emit := func(pct float64, msg string) {
		events <- sseEvent{Extension: name, Progress: pct, Message: msg}
	}
	fail := func(err error) {
		events <- sseEvent{Extension: name, Progress: 100, Message: "Update failed", Done: true, Error: err.Error()}
	}

	emit(5, "Fetching registry...")
	index, err := h.registry.FetchIndex(url)
	if err != nil {
		fail(fmt.Errorf("failed to fetch registry: %w", err))
		return
	}

	entry := findRegistryEntry(index, name)
	if entry == nil {
		fail(fmt.Errorf("extension not found in registry: %s", name))
		return
	}

	ext, err := h.store.GetByID(id)
	if err != nil {
		fail(fmt.Errorf("extension not found: %w", err))
		return
	}

	// This check stays before the sandbox stop and the checkout, so a refused update changes nothing.
	if added := newEgressHosts(ext.Manifest.Capabilities, entry.Capabilities); len(added) > 0 && !ackNewDomains {
		logging.For(logging.Extensions).Warnf("ext=%s update to %s refused: %d newly granted host(s): %s",
			id, entry.Version, len(added), strings.Join(added, ", "))
		fail(fmt.Errorf("%s %s reaches a host this extension was never granted (%s) — review its allowed domains, then update again to accept them",
			name, entry.Version, summarizeHosts(added)))
		return
	}
	// Stop sandbox if running
	_ = h.sandbox.Stop(id)

	if err := refuseUninstallable(entry); err != nil {
		fail(err)
		return
	}

	gitPinned := entry.Repo != "" && entry.Commit != ""
	imageRef := ExtensionImageRef(name, entry.Version)
	// Same rule as install: a registry image that names no registry cannot be pulled headless.
	if err := h.registry.ValidateImage(entry.Image); err != nil {
		fail(fmt.Errorf("extension %s declares an unusable image: %w", name, err))
		return
	}

	if gitPinned {
		// Re-checkout the newly vetted commit (verified) and rebuild from it.
		targetDir := filepath.Join(h.config.ExtensionsDirOrDefault(), authorDir(entry.Author), entry.Name)
		emit(10, "Fetching extension...")
		if err := h.registry.CheckoutExtension(entry.Repo, entry.Commit, entry.Subdir, targetDir); err != nil {
			fail(fmt.Errorf("failed to fetch extension (commit %s): %w", entry.Commit, err))
			return
		}
		if data, err := os.ReadFile(filepath.Join(targetDir, "manifest.json")); err == nil {
			var m ExtensionManifest
			if err := json.Unmarshal(data, &m); err == nil {
				ext.Manifest = m
				// The sandbox declaration is part of what this commit delivers, so re-read it: an
				// update that moves the cache must not keep running on install-time paths. A
				// manifest that self-grants beyond the policy fails as it does at install.
				before := ext.SandboxConfig
				merged := mergeManifestSandboxAdditions(ext.SandboxConfig, lspSandboxFromManifest(&m))
				if err := h.mountPolicy().ValidateSandboxAdditions(merged.Mounts, merged.Capabilities); err != nil {
					fail(fmt.Errorf("extension %s declares an unusable sandbox configuration: %w", entry.Name, err))
					return
				}
				// Check the new commit's language declarations before they replace the stored manifest.
				if err := m.Capabilities.GetLSP().Validate(); err != nil {
					fail(fmt.Errorf("extension %s declares an unusable language configuration: %w", entry.Name, err))
					return
				}
				// Same rule for a translation pack: the new commit's declaration is vetted and its
				// catalog and templates are read from the checkout already on disk, before the stored
				// manifest is replaced.
				if err := validateI18N(m.Capabilities, targetDir); err != nil {
					fail(fmt.Errorf("extension %s declares an unusable translation pack: %w", entry.Name, err))
					return
				}
				// And the same for the new commit's stylesheet: an update that moves or escapes
				// css_url is refused before the stored manifest is replaced.
				if err := m.Capabilities.GetCSS().Validate(); err != nil {
					fail(fmt.Errorf("extension %s declares an unusable stylesheet: %w", entry.Name, err))
					return
				}
				if err := validateTools(m.Capabilities, m.ConfigSchema); err != nil {
					fail(fmt.Errorf("extension %s declares an unusable tool: %w", entry.Name, err))
					return
				}
				if len(merged.Envs) > len(before.Envs) || len(merged.Mounts) > len(before.Mounts) ||
					len(merged.Capabilities) > len(before.Capabilities) {
					logging.For(logging.Extensions).Infof("ext=%s update added %d env, %d mount, %d capability declaration(s) from the manifest", id, len(merged.Envs)-len(before.Envs), len(merged.Mounts)-len(before.Mounts), len(merged.Capabilities)-len(before.Capabilities))
				}
				ext.SandboxConfig = merged
			}
		}
		ext.Repo = entry.Repo
		ext.Commit = entry.Commit
		ext.Subdir = entry.Subdir
		dockerfile, source, dfErr := h.installDockerfile(entry, url, targetDir, true, emit)
		if dfErr != nil {
			fail(dfErr)
			return
		}
		switch {
		case dockerfile != "":
			emit(30, "Building image...")
			if err := h.registry.BuildExtensionImage(dockerfile, imageRef); err != nil {
				fail(fmt.Errorf("failed to build image from %s: %w", source, err))
				return
			}
		case entry.Image != "":
			// The new commit ships no Dockerfile but the registry names a pre-built image.
			imageRef = entry.Image
		default:
			// Neither. Fine for a UI pack or a translation pack, fatal for a language server: keeping the
			// previous commit's image would run new code against a server that is not in it.
			imageRef = ""
			if installNeedsImage(entry) || ext.IsLSP() {
				fail(fmt.Errorf("extension %s %s ships no image and no Dockerfile to build one from, so this update has nothing to run in a sandbox", entry.Name, entry.Version))
				return
			}
		}
	} else {
		// Rebuild from Dockerfile if available, otherwise pull image
		if entry.DockerfileURL != "" {
			emit(20, "Fetching Dockerfile...")
			dockerfileURL := ResolveURL(url, entry.DockerfileURL)
			dockerfile, err := h.registry.FetchDockerfile(dockerfileURL)
			if err != nil {
				fail(fmt.Errorf("failed to fetch dockerfile: %w", err))
				return
			}
			emit(30, "Building image...")
			if err := h.registry.BuildExtensionImage(dockerfile, imageRef); err != nil {
				fail(fmt.Errorf("failed to build image: %w", err))
				return
			}
		} else if entry.Image != "" {
			emit(30, "Pulling image...")
			if err := h.registry.PullImage(entry.Image); err != nil {
				fail(fmt.Errorf("failed to pull image: %w", err))
				return
			}
			imageRef = entry.Image
		} else {
			fail(fmt.Errorf("extension has no image or dockerfile to update"))
			return
		}
	}

	// Update extension with new image ref, version, and commit
	ext.Version = entry.Version
	if imageRef != "" {
		ext.ImageRef = imageRef
		ext.ContainerConfig.Image = imageRef
	}
	if err := h.store.Update(id, ext); err != nil {
		fail(fmt.Errorf("failed to update extension: %w", err))
		return
	}

	// Update built_at timestamp
	if err := h.store.SetBuiltAt(id); err != nil {
		fail(fmt.Errorf("failed to update build timestamp: %w", err))
		return
	}

	// Notify listeners (e.g., LSP handler to broadcast reset to frontend)
	if h.onImageUpdated != nil {
		h.onImageUpdated(id)
	}

	statuses := h.sandbox.ListRunning()
	if updated, _ := h.store.GetByID(id); updated != nil {
		ext = updated
	}
	result := h.toResponse(ext, statuses)
	events <- sseEvent{Extension: name, Progress: 100, Message: "Updated", Done: true, Result: &result}
}

// proxyContentType decides what the browser is told a proxied body IS. The upstream's own
// Content-Type is never echoed: the response is served from MIKE's own origin, so a third-party
// upstream must not choose how that origin labels its bytes. JSON is kept because the client
// branches on it; everything else maps to a type that cannot execute.
func proxyContentType(upstream string) string {
	if httputil.IsJSONContentType(upstream) {
		return "application/json"
	}
	return "text/plain; charset=utf-8"
}

// HandleProxy proxies a request on an extension's behalf. The egress path decides what it may reach and
// what content type it answers with, the same as for a tool call.
func (h *ExtensionHandler) HandleProxy(w http.ResponseWriter, r *http.Request, id string) {

	ext, ok := h.extensionOr404(w, id)
	if !ok {
		return
	}
	if !ext.Enabled {
		httputil.JSONError(w, http.StatusForbidden, "extension is not enabled")
		return
	}
	if ext.Manifest.Capabilities.GetNetwork() == nil {
		httputil.JSONError(w, http.StatusForbidden, "extension does not have network capability")
		return
	}

	var req struct {
		URL    string `json:"url"`
		Method string `json:"method"`
		Body   string `json:"body,omitempty"`
	}
	if !httputil.DecodeJSON(w, r, &req) {
		return
	}
	if req.URL == "" {
		httputil.JSONError(w, http.StatusBadRequest, "url is required")
		return
	}

	// No grants: this URL comes from an extension's script, not from an address a person typed.
	result, err := Egress(r.Context(), ext, EgressRequest{
		URL:    req.URL,
		Method: req.Method,
		Body:   req.Body,
		Source: "proxy",
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrEgressForbidden):
			httputil.JSONError(w, http.StatusForbidden, err.Error())
		case errors.Is(err, ErrEgressUnusable):
			httputil.JSONError(w, http.StatusBadRequest, err.Error())
		default:
			httputil.JSONError(w, http.StatusBadGateway, "proxy request failed: "+err.Error())
		}
		return
	}

	// The content type is MIKE's choice, never the upstream's.
	w.Header().Set("Content-Type", proxyContentType(result.ContentType))
	w.WriteHeader(result.StatusCode)
	w.Write(result.Body)
}

// HandleTestURL probes one configured address before it is saved, so an unsaved value is what gets tested.
// It is POST because it sends a request, and still a read because nothing changes.
func (h *ExtensionHandler) HandleTestURL(w http.ResponseWriter, r *http.Request, id string) {
	ext, ok := h.extensionOr404(w, id)
	if !ok {
		return
	}
	if ext.Manifest.Capabilities.GetNetwork() == nil {
		httputil.JSONError(w, http.StatusForbidden, "extension does not have network capability")
		return
	}

	var req testURLRequest
	if !httputil.DecodeJSON(w, r, &req) {
		return
	}
	tool, problem := probeTarget(ext, strings.TrimSpace(req.Key))
	if problem != "" {
		httputil.JSONError(w, http.StatusBadRequest, problem)
		return
	}
	result := runProbe(r.Context(), ext, tool, req)
	logging.For(logging.Extensions).Infof("extension=%s probe=%s class=%s", ext.Name, strings.TrimSpace(req.Key), result.Class)
	httputil.JSONResponse(w, http.StatusOK, result)
}

// HandleStorage provides scoped key-value storage for an extension.
// Keys are scoped by (extension, namespace) so extensions cannot read or
// write each other's data. Namespace is derived from the host injection
// context (e.g. project/task), keeping the server feature-agnostic.
func (h *ExtensionHandler) HandleStorage(w http.ResponseWriter, r *http.Request, id string) {
	if _, err := h.store.GetByID(id); err != nil {
		httputil.Fail(w, err, "get extension", "extension not found")
		return
	}

	switch r.Method {
	case http.MethodGet:
		namespace := r.URL.Query().Get("namespace")
		key := r.URL.Query().Get("key")
		if key != "" {
			entry, err := h.store.StorageGet(id, namespace, key)
			if err != nil {
				httputil.Fault(w, "read extension storage", err)
				return
			}
			if entry == nil {
				httputil.JSONError(w, http.StatusNotFound, "key not found")
				return
			}
			httputil.JSONResponse(w, http.StatusOK, entry)
			return
		}
		entries, err := h.store.StorageList(id, namespace, r.URL.Query().Get("prefix"))
		if err != nil {
			httputil.Fault(w, "list extension storage", err)
			return
		}
		httputil.JSONResponse(w, http.StatusOK, entries)
	case http.MethodPut:
		var req struct {
			Namespace string          `json:"namespace"`
			Key       string          `json:"key"`
			Value     json.RawMessage `json:"value"`
		}
		if !httputil.DecodeJSON(w, r, &req) {
			return
		}
		if req.Key == "" {
			httputil.JSONError(w, http.StatusBadRequest, "key is required")
			return
		}
		entry, err := h.store.StorageSet(id, req.Namespace, req.Key, req.Value)
		if err != nil {
			httputil.JSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		httputil.JSONResponse(w, http.StatusOK, entry)
	case http.MethodDelete:
		namespace := r.URL.Query().Get("namespace")
		key := r.URL.Query().Get("key")
		if key == "" {
			httputil.JSONError(w, http.StatusBadRequest, "key is required")
			return
		}
		if err := h.store.StorageDelete(id, namespace, key); err != nil {
			httputil.Fault(w, "delete extension storage", err)
			return
		}
		httputil.JSONResponse(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		httputil.MethodNotAllowed(w)
	}
}

// resolveProxyTemplate resolves {{property_name}} placeholders in a template string
// using values from the extension's configuration.
func resolveProxyTemplate(template string, config map[string]any) string {
	for k, v := range config {
		placeholder := "{{" + k + "}}"
		template = strings.ReplaceAll(template, placeholder, fmt.Sprintf("%v", v))
	}
	return template
}

// servedURLFromDir is the URL prefix an extension's own files are served under
//
// Returns "" when the directory is not under the extensions root
func servedURLFromDir(extensionsDir, dir string) string {
	extensionsDir = filepath.Clean(extensionsDir)
	rel, err := filepath.Rel(extensionsDir, dir)
	if err != nil || !fsutil.RelBelowRoot(rel) {
		return ""
	}
	segments := strings.Split(rel, string(filepath.Separator))
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return "/extensions/" + strings.Join(segments, "/")
}

// --- Request/Response types ---

// UpdateExtensionRequest is the request body for updating an extension.
type UpdateExtensionRequest struct {
	Version         string            `json:"version,omitempty"`
	Manifest        ExtensionManifest `json:"manifest,omitempty"`
	Config          map[string]any    `json:"config,omitempty"`
	ContainerConfig ContainerConfig   `json:"container_config,omitempty"`
	SandboxConfig   SandboxConfig     `json:"sandbox_config,omitempty"`
}

// InstallExtensionRequest is the request body for installing from registry.
type InstallExtensionRequest struct {
	Name   string `json:"name"`
	Author string `json:"author,omitempty"`
}

// ExtensionResponse is the API response for an extension.
type ExtensionResponse struct {
	ID              string               `json:"id"`
	Name            string               `json:"name"`
	Version         string               `json:"version"`
	Type            string               `json:"type"`
	Manifest        ExtensionManifest    `json:"manifest"`
	Config          map[string]any       `json:"config"`
	ContainerConfig ContainerConfig      `json:"container_config"`
	SandboxConfig   SandboxConfig        `json:"sandbox_config"`
	InstallSource   string               `json:"install_source"`
	ImageRef        string               `json:"image_ref,omitempty"`
	Enabled         bool                 `json:"enabled"`
	Running         bool                 `json:"running"`
	LocalPath       string               `json:"local_path,omitempty"`
	ServedURL       string               `json:"served_url,omitempty"`
	Ports           []types.AssignedPort `json:"ports,omitempty"`
	InstalledAt     string               `json:"installed_at"`
	BuiltAt         string               `json:"built_at"`
	Repo            string               `json:"repo,omitempty"`
	Commit          string               `json:"commit,omitempty"`
	Author          string               `json:"author,omitempty"`
}

// ExtensionUpdate describes an available update.
type ExtensionUpdate struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	CurrentVersion   string `json:"current_version"`
	AvailableVersion string `json:"available_version"`
	CurrentCommit    string `json:"current_commit,omitempty"`
	AvailableCommit  string `json:"available_commit,omitempty"`
	// NewPermissions lists the UI permissions the update would newly grant
	// (present in the registry version but not the installed one).
	NewPermissions []string `json:"new_permissions,omitempty"`
	// DomainsAdded is set when the update names a host in `allowed_domains` that the installed
	// manifest does not. Removing a host never sets it.
	DomainsAdded bool `json:"domains_added,omitempty"`
	// Changelog lists the changes (oldest → newest) newer than the installed
	// commit, from the extension's published changelog. Empty when no
	// changelog is published, it cannot be fetched, the installed commit is
	// not in it, or the update is not git-pinned.
	Changelog []ChangelogEntry `json:"changelog,omitempty"`
}

// maskedValue is what a sensitive field shows in the browser.
const maskedValue = "\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022" // 10 bullets

// maskConfig replaces every schema-declared sensitive value with maskedValue. An empty
// value passes through — a mask in a blank field reads as if a secret were set.
func maskConfig(config map[string]any, schema *ConfigSchema) map[string]any {
	return mapSensitiveConfig(config, schema, func(_ string, v any) (any, bool) {
		if fmt.Sprintf("%v", v) == "" {
			return nil, false
		}
		return maskedValue, true
	})
}

// toResponse converts an Extension to an ExtensionResponse with runtime status.
func (h *ExtensionHandler) toResponse(ext *Extension, statuses map[string]ExtensionRuntimeStatus) ExtensionResponse {
	status, ok := statuses[ext.ID]
	resp := ExtensionResponse{
		ID:              ext.ID,
		Name:            ext.Name,
		Author:          ext.Author,
		Version:         ext.Version,
		Type:            ext.Type,
		Manifest:        resolvedSandboxManifest(ext.Manifest),
		Config:          maskConfig(ext.Config, ext.Manifest.ConfigSchema),
		ContainerConfig: ext.ContainerConfig,
		SandboxConfig:   ext.SandboxConfig,
		InstallSource:   ext.InstallSource,
		ImageRef:        ext.ImageRef,
		Enabled:         ext.Enabled,
		Running:         ok && status.Running,
		Ports:           status.Ports,
		InstalledAt:     ext.InstalledAt,
		BuiltAt:         ext.BuiltAt,
		Repo:            ext.Repo,
		Commit:          ext.Commit,
	}
	// One directory, two readers. `extensionDir` is what every other consumer
	// resolves an extension through (and it stats the disk for a `local` record,
	// so asking it twice per row would be two stats for one answer); the on-disk
	// path is what the Settings row shows, and the served URL is what the browser
	// fetches assets from — see servedURLFromDir for why that has to be the server's
	// answer and not something the client guesses from a manifest author.
	root := h.config.ExtensionsDirOrDefault()
	extDir := extensionDir(root, ext.Author, ext.Name)
	resp.ServedURL = servedURLFromDir(root, extDir)
	// Local (filesystem) extensions have a concrete on-disk path; files stay where
	// a human put them, which is why the path comes from the same derivation.
	if ext.InstallSource == "local" {
		resp.LocalPath = extDir
	}
	return resp
}
