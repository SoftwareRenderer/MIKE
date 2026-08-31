package extensions

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"kanban/backend/httputil"
	"kanban/backend/types"
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

// NewExtensionHandler creates a new ExtensionHandler.
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
	return newMountPolicy(mountRoots(extDir, "", nil))
}

// HandleList returns all installed extensions with runtime status.
func (h *ExtensionHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	extensions, err := h.store.List()
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to list extensions: "+err.Error())
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

	httputil.JSONResponse(w, http.StatusOK, result)
}

// HandleGet returns a single extension by ID.
func (h *ExtensionHandler) HandleGet(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ext, err := h.store.GetByID(id)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "extension not found")
		return
	}

	statuses := h.sandbox.ListRunning()
	httputil.JSONResponse(w, http.StatusOK, h.toResponse(ext, statuses))
}

// HandleUpdate updates an existing extension.
func (h *ExtensionHandler) HandleUpdate(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPut && r.Method != http.MethodPatch {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ext, err := h.store.GetByID(id)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "extension not found")
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
		if len(req.Manifest.Capabilities.LSP.Extensions) > 0 {
			ext.Manifest.Capabilities.LSP.Extensions = req.Manifest.Capabilities.LSP.Extensions
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
	}

	if err := h.store.Update(id, ext); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to update extension: "+err.Error())
		return
	}

	// Apply the saved memory limit to a running sandbox
	if ext.IsLSP() && ext.SandboxConfig.MemoryLimit != oldMemoryLimit {
		if memErr := h.sandbox.ApplyMemoryLimit(id, ext.SandboxConfig.MemoryLimit); memErr != nil {
			log.Printf("[extensions] ext=%s WARNING: memory limit %dMB not applied to running sandbox: %v", id, ext.SandboxConfig.MemoryLimit, memErr)
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
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Stop sandbox if running
	_ = h.sandbox.Destroy(id)

	// Remove scoped storage for this extension so its data is not orphaned,
	// even if the extension record itself is already gone.
	_ = h.store.StorageDeleteAll(id)

	if err := h.store.Delete(id); err != nil {
		httputil.JSONError(w, http.StatusNotFound, "extension not found")
		return
	}

	httputil.JSONResponse(w, http.StatusOK, map[string]string{"status": "deleted"})

	// Notify frontend to refresh UI injection slots
	if h.onExtensionChanged != nil {
		h.onExtensionChanged()
	}
}

// HandleSetEnabled toggles the enabled state of an extension.
func (h *ExtensionHandler) HandleSetEnabled(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPatch && r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

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

	ext, err := h.store.GetByID(id)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "extension not found")
		return
	}
	statuses := h.sandbox.ListRunning()
	httputil.JSONResponse(w, http.StatusOK, h.toResponse(ext, statuses))

	// Notify frontend to refresh UI injection slots
	if h.onExtensionChanged != nil {
		h.onExtensionChanged()
	}
}

// inferExtensionType returns the extension type implied by its capabilities,
// in priority order (lsp > ui > css > agent > llm). Returns "" when no capability
// maps to a type.
func inferExtensionType(caps *Capabilities) string {
	if caps == nil {
		return ""
	}
	switch {
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

// HandleLocalExtensions lists extensions present in the local extensions
// directory (on the filesystem) as registry-style entries. These are the
// extensions available locally that are not necessarily published in a registry.
// Each entry's name is derived from the manifest's parent directory
// (extensions/<author>/<name>/manifest.json).
func (h *ExtensionHandler) HandleLocalExtensions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	found, err := scanLocalExtensions(h.config.ExtensionsDirOrDefault())
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to scan extensions directory: "+err.Error())
		return
	}

	index := make(RegistryIndex, 0, len(found))
	for _, le := range found {
		index = append(index, RegistryEntry{
			Name:         le.Name,
			Version:      le.Manifest.Version,
			Type:         le.Type,
			DisplayName:  le.Manifest.DisplayName,
			Description:  le.Manifest.Description,
			Author:       le.Author,
			Homepage:     le.Manifest.Homepage,
			LicenseURL:   le.Manifest.LicenseURL,
			Capabilities: le.Manifest.Capabilities,
			LocalPath:    le.Dir,
		})
	}

	httputil.JSONResponse(w, http.StatusOK, index)
}

// HandleRegistryIndex fetches the registry index.
func (h *ExtensionHandler) HandleRegistryIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	url := h.config.RegistryURL
	if url == "" {
		httputil.JSONError(w, http.StatusBadRequest, "registry URL not configured")
		return
	}

	index, err := h.registry.FetchIndex(url)
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to fetch registry: "+err.Error())
		return
	}

	httputil.JSONResponse(w, http.StatusOK, index)
}

// HandleInstall installs from the registry. Fast validation returns HTTP errors
// synchronously; the actual install runs in a detached goroutine (so it survives
// a client disconnect) while this handler streams SSE progress.
func (h *ExtensionHandler) HandleInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req InstallExtensionRequest
	if !httputil.DecodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		httputil.JSONError(w, http.StatusBadRequest, "name is required")
		return
	}

	url := h.config.RegistryURL
	if url == "" {
		httputil.JSONError(w, http.StatusBadRequest, "registry URL not configured")
		return
	}

	index, err := h.registry.FetchIndex(url)
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to fetch registry: "+err.Error())
		return
	}

	var entry *RegistryEntry
	for i, e := range index {
		if e.Name == req.Name {
			entry = &index[i]
			break
		}
	}
	if entry == nil {
		httputil.JSONError(w, http.StatusNotFound, "extension not found in registry: "+req.Name)
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
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

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

	entry := &RegistryEntry{
		Name:         local.Name,
		Version:      local.Manifest.Version,
		Type:         local.Type,
		DisplayName:  local.Manifest.DisplayName,
		Description:  local.Manifest.Description,
		Author:       local.Author,
		Homepage:     local.Manifest.Homepage,
		LicenseURL:   local.Manifest.LicenseURL,
		Capabilities: local.Manifest.Capabilities,
		localDir:     local.Dir,
	}

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
	go h.installInBackground(entry.Name, entry, "", events)

	h.streamOperation(w, r, events)
}

// installInBackground runs the install detached from the request context so it
// completes even if the client disconnects, reporting progress on events.
func (h *ExtensionHandler) installInBackground(name string, entry *RegistryEntry, url string, events chan<- sseEvent) {
	defer h.inFlightOps.end(name)
	defer close(events)

	emit := func(pct float64, msg string) {
		events <- sseEvent{Extension: name, Progress: pct, Message: msg}
	}
	fail := func(err error) {
		events <- sseEvent{Extension: name, Progress: 100, Message: "Install failed", Done: true, Error: err.Error()}
	}

	// Local (filesystem) extension: files are already materialized, so load the
	// manifest in place rather than fetching from a registry or git repo.
	gitPinned := entry.Repo != "" && entry.Commit != ""
	var manifest ExtensionManifest
	targetDir := filepath.Join(h.config.ExtensionsDirOrDefault(), authorDir(entry.Author), entry.Name)
	if entry.localDir != "" {
		targetDir = entry.localDir
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
	ext := &Extension{
		ID:       id,
		Name:     entry.Name,
		Author:   author,
		Version:  entry.Version,
		Type:     entry.Type,
		Manifest: manifest,
		Config:   map[string]any{},
		ContainerConfig: ContainerConfig{
			Image: entry.Image,
		},
		SandboxConfig: sandboxCfg,
		InstallSource: installSource,

		ImageRef: entry.Image,
		Enabled:  true,
		Repo:     entry.Repo,
		Commit:   entry.Commit,
		Subdir:   entry.Subdir,
	}
	if err := h.store.Create(ext); err != nil {
		fail(fmt.Errorf("failed to create extension: %w", err))
		return
	}

	// Build/pull image if none is provided pre-built.
	imageRef := entry.Image
	if imageRef == "" {
		var dockerfile string
		switch {
		case entry.localDir != "":
			// Build from the local extension's Dockerfile.
			emit(20, "Reading Dockerfile...")
			if data, err := os.ReadFile(filepath.Join(entry.localDir, "Dockerfile")); err == nil {
				dockerfile = string(data)
			}
		case gitPinned:
			// Build from the verified checkout's Dockerfile.
			dfPath := filepath.Join(targetDir, "Dockerfile")
			if data, err := os.ReadFile(dfPath); err == nil {
				dockerfile = string(data)
			}
		case entry.DockerfileURL != "":
			emit(20, "Fetching Dockerfile...")
			df, fetchErr := h.registry.FetchDockerfile(ResolveURL(url, entry.DockerfileURL))
			if fetchErr != nil {
				fail(fmt.Errorf("failed to fetch dockerfile: %w", fetchErr))
				return
			}
			dockerfile = df
		}
		if dockerfile != "" {
			emit(30, "Building image...")
			imageRef = fmt.Sprintf("kanban-ext-%s:%s", entry.Name, entry.Version)
			if err := h.registry.BuildImageFromReader(dockerfile, imageRef); err != nil {
				fail(fmt.Errorf("failed to build image: %w", err))
				return
			}
			ext.ContainerConfig.Image = imageRef
			ext.ImageRef = imageRef
			if err := h.store.Update(ext.ID, ext); err != nil {
				fail(fmt.Errorf("failed to update extension: %w", err))
				return
			}
		}
	}

	statuses := h.sandbox.ListRunning()
	result := h.toResponse(ext, statuses)
	events <- sseEvent{Extension: name, Progress: 100, Message: "Installed", Done: true, Result: &result}

	// Notify frontend to refresh UI injection slots
	if h.onExtensionChanged != nil {
		h.onExtensionChanged()
	}
}

// streamOperation streams SSE events until Done or client disconnect. The
// operation runs detached, so returning on disconnect does not cancel it.
func (h *ExtensionHandler) streamOperation(w http.ResponseWriter, r *http.Request, events <-chan sseEvent) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
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
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			if ev.Done {
				return
			}
		}
	}
}

// HandleCheckUpdates checks for available updates.
func (h *ExtensionHandler) HandleCheckUpdates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	url := h.config.RegistryURL
	if url == "" {
		httputil.JSONError(w, http.StatusBadRequest, "registry URL not configured")
		return
	}

	index, err := h.registry.FetchIndex(url)
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to fetch registry: "+err.Error())
		return
	}

	// Compare with installed extensions
	extensions, err := h.store.List()
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to list extensions: "+err.Error())
		return
	}

	updates := make([]ExtensionUpdate, 0)
	for _, ext := range extensions {
		for _, entry := range index {
			if entry.Name != ext.Name {
				continue
			}
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
	data, err := h.registry.FetchChangelog(h.config.RegistryURL, entry.Author, entry.Name)
	if err != nil {
		return nil
	}
	return ChangelogSince(ParseChangelog(data), sinceHash)
}

// HandleSandboxLogs returns sandbox logs (streaming).
func (h *ExtensionHandler) HandleSandboxLogs(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	_, err := h.store.GetByID(id)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "extension not found")
		return
	}

	// Check if sandbox is running
	if !h.sandbox.IsRunning(id) {
		httputil.JSONError(w, http.StatusServiceUnavailable, "extension sandbox not running")
		return
	}

	// TODO: Stream logs from container
	// For now, return empty
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "Logs streaming not yet implemented")
}

// HandleUpdateImage updates an extension's image from the registry.
// Rebuilds from Dockerfile if available, otherwise pulls latest pre-built image.
func (h *ExtensionHandler) HandleUpdateImage(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ext, err := h.store.GetByID(id)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "extension not found")
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

	// Prevent a concurrent update of the same extension.
	if !h.inFlightOps.begin(ext.Name) {
		httputil.JSONError(w, http.StatusConflict, "update already in progress: "+ext.Name)
		return
	}

	events := make(chan sseEvent, 16)
	go h.updateImageInBackground(id, ext.Name, url, events)

	h.streamOperation(w, r, events)
}

// updateImageInBackground updates an extension's image, detached like install.
func (h *ExtensionHandler) updateImageInBackground(id, name, url string, events chan<- sseEvent) {
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

	var entry *RegistryEntry
	for i, e := range index {
		if e.Name == name {
			entry = &index[i]
			break
		}
	}
	if entry == nil {
		fail(fmt.Errorf("extension not found in registry: %s", name))
		return
	}

	// Stop sandbox if running
	_ = h.sandbox.Stop(id)

	ext, err := h.store.GetByID(id)
	if err != nil {
		fail(fmt.Errorf("extension not found: %w", err))
		return
	}

	gitPinned := entry.Repo != "" && entry.Commit != ""
	imageRef := fmt.Sprintf("kanban-ext-%s:%s", name, entry.Version)

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
			}
		}
		ext.Repo = entry.Repo
		ext.Commit = entry.Commit
		ext.Subdir = entry.Subdir
		if data, err := os.ReadFile(filepath.Join(targetDir, "Dockerfile")); err == nil {
			emit(30, "Building image...")
			if err := h.registry.BuildImageFromReader(string(data), imageRef); err != nil {
				fail(fmt.Errorf("failed to build image: %w", err))
				return
			}
		} else {
			// No Dockerfile (UI/CSS/agent extension) — nothing to build.
			imageRef = ""
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
			if err := h.registry.BuildImageFromReader(dockerfile, imageRef); err != nil {
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

// HandleProxy proxies requests to external APIs on behalf of an extension.
// The extension must declare a `network` capability in its manifest with
// allowed_domains and optional header templates.
func (h *ExtensionHandler) HandleProxy(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ext, err := h.store.GetByID(id)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "extension not found")
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
	if req.Method == "" {
		req.Method = http.MethodGet
	}

	// Validate
	u, err := parseURL(req.URL)
	if err != nil {
		httputil.JSONError(w, http.StatusBadRequest, "invalid url: "+err.Error())
		return
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		httputil.JSONError(w, http.StatusBadRequest, "only http/https URLs are allowed")
		return
	}
	allowed := false
	for _, d := range ext.Manifest.Capabilities.GetNetwork().AllowedDomains {
		if d == u.Hostname() {
			allowed = true
			break
		}
	}
	if !allowed {
		httputil.JSONError(w, http.StatusForbidden, "domain not in allowed_domains: "+u.Hostname())
		return
	}

	// Resolve header templates from extension config
	headers := make(map[string]string)
	for k, template := range ext.Manifest.Capabilities.GetNetwork().Headers {
		resolved := resolveProxyTemplate(template, ext.Config)
		if resolved == "" || resolved == template {
			continue // skip unresolved or empty headers
		}
		headers[k] = resolved
	}

	// Create proxy request
	proxyReq, err := http.NewRequest(req.Method, req.URL, strings.NewReader(req.Body))
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to create proxy request: "+err.Error())
		return
	}
	for k, v := range headers {
		proxyReq.Header.Set(k, v)
	}
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyReq.Header.Set("Accept", "application/json")

	// Log the proxy request
	log.Printf("[proxy] extension=%s method=%s url=%s status=pending", ext.Name, req.Method, req.URL)

	// Execute proxy request. Redirects are NOT followed: the allowlist only
	// validates the original URL's hostname, so following a redirect could
	// otherwise reach an arbitrary internal host (SSRF via redirect).
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(proxyReq)
	if err != nil {
		httputil.JSONError(w, http.StatusBadGateway, "proxy request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		httputil.JSONError(w, http.StatusBadGateway, "failed to read proxy response: "+err.Error())
		return
	}

	// Log the proxy response
	log.Printf("[proxy] extension=%s method=%s url=%s status=%d", ext.Name, req.Method, req.URL, resp.StatusCode)

	// Return raw response
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

// HandleStorage provides scoped key-value storage for an extension.
// Keys are scoped by (extension, namespace) so extensions cannot read or
// write each other's data. Namespace is derived from the host injection
// context (e.g. project/task), keeping the server feature-agnostic.
func (h *ExtensionHandler) HandleStorage(w http.ResponseWriter, r *http.Request, id string) {
	if _, err := h.store.GetByID(id); err != nil {
		httputil.JSONError(w, http.StatusNotFound, "extension not found")
		return
	}

	switch r.Method {
	case http.MethodGet:
		namespace := r.URL.Query().Get("namespace")
		key := r.URL.Query().Get("key")
		if key != "" {
			entry, err := h.store.StorageGet(id, namespace, key)
			if err != nil {
				httputil.JSONError(w, http.StatusInternalServerError, "failed to read storage: "+err.Error())
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
			httputil.JSONError(w, http.StatusInternalServerError, "failed to list storage: "+err.Error())
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
			httputil.JSONError(w, http.StatusInternalServerError, "failed to delete storage: "+err.Error())
			return
		}
		httputil.JSONResponse(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// parseURL parses a URL and returns the parsed URL.
func parseURL(rawURL string) (*url.URL, error) {
	return url.Parse(rawURL)
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
	// Changelog lists the changes (oldest → newest) newer than the installed
	// commit, from the extension's published changelog. Empty when no
	// changelog is published, it cannot be fetched, the installed commit is
	// not in it, or the update is not git-pinned.
	Changelog []ChangelogEntry `json:"changelog,omitempty"`
}

// maskConfig masks sensitive config values based on the manifest's config_schema.
// Sensitive fields are replaced with a bullet placeholder.
const maskedValue = "\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022" // 10 bullets

func maskConfig(config map[string]any, schema *ConfigSchema) map[string]any {
	if schema == nil || len(schema.Properties) == 0 || config == nil {
		return config
	}
	result := make(map[string]any, len(config))
	for k, v := range config {
		prop := schema.GetProperty(k)
		if prop == nil || !prop.Sensitive {
			result[k] = v
			continue
		}
		// Mask non-empty sensitive values
		strVal := fmt.Sprintf("%v", v)
		if strVal != "" {
			result[k] = maskedValue
		} else {
			result[k] = v
		}
	}
	return result
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
		Manifest:        ext.Manifest,
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
	// Local (filesystem) extensions have a concrete on-disk path. For an
	// author-namespaced extension that is <extensions>/<author>/<name>; an
	// extension not organized under an author folder (author "local") lives
	// directly at <extensions>/<name>.
	if ext.InstallSource == "local" {
		root := h.config.ExtensionsDirOrDefault()
		if ext.Author != "" && ext.Author != "local" {
			resp.LocalPath = filepath.Join(root, ext.Author, ext.Name)
		} else {
			resp.LocalPath = extensionDir(root, ext.Author, ext.Name)
		}
	}
	return resp
}
