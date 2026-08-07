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

// BuildProgress tracks the progress of extension builds.
type BuildProgress struct {
	Extension string  `json:"extension"`
	Progress  float64 `json:"progress"`
	Message   string  `json:"message"`
	Done      bool    `json:"done"`
	Error     string  `json:"error,omitempty"`
}

// BuildProgressTracker manages build progress notifications.
type BuildProgressTracker struct {
	mu          sync.RWMutex
	builds      map[string]*BuildProgress
	subscribers map[string][]chan *BuildProgress
}

// NewBuildProgressTracker creates a new tracker.
func NewBuildProgressTracker() *BuildProgressTracker {
	return &BuildProgressTracker{
		builds:      make(map[string]*BuildProgress),
		subscribers: make(map[string][]chan *BuildProgress),
	}
}

// StartBuild registers a new build.
func (t *BuildProgressTracker) StartBuild(extName string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.builds[extName] = &BuildProgress{
		Extension: extName,
		Progress:  0,
		Message:   "Starting build...",
	}
}

// UpdateBuild updates build progress.
func (t *BuildProgressTracker) UpdateBuild(extName string, progress float64, message string) {
	t.mu.Lock()
	if bp, ok := t.builds[extName]; ok {
		bp.Progress = progress
		bp.Message = message
		// Notify subscribers
		for _, ch := range t.subscribers[extName] {
			select {
			case ch <- bp:
			default:
			}
		}
	}
	t.mu.Unlock()
}

// CompleteBuild marks a build as complete.
func (t *BuildProgressTracker) CompleteBuild(extName string, err error) {
	t.mu.Lock()
	if bp, ok := t.builds[extName]; ok {
		bp.Progress = 100
		bp.Done = true
		if err != nil {
			bp.Error = err.Error()
			bp.Message = "Build failed"
		} else {
			bp.Message = "Build complete"
		}
		// Notify subscribers
		for _, ch := range t.subscribers[extName] {
			select {
			case ch <- bp:
			default:
			}
		}
		// Close channels
		for _, ch := range t.subscribers[extName] {
			close(ch)
		}
		delete(t.subscribers, extName)
	}
	t.mu.Unlock()
}

// Subscribe returns a channel for build progress updates.
func (t *BuildProgressTracker) Subscribe(extName string) <-chan *BuildProgress {
	t.mu.Lock()
	defer t.mu.Unlock()
	ch := make(chan *BuildProgress, 10)
	t.subscribers[extName] = append(t.subscribers[extName], ch)
	// Send current state if available
	if bp, ok := t.builds[extName]; ok {
		ch <- bp
	}
	return ch
}

// ExtensionHandler handles extension API requests.
type ExtensionHandler struct {
	store              *ExtensionStore
	sandbox            *ExtensionSandboxManager
	registry           *RegistryClient
	config             *ExtensionConfig
	progress           *BuildProgressTracker
	onImageUpdated     func(extID string) // callback after successful image update
	onExtensionChanged func()             // callback after any extension change (enable/disable/create/delete)
}

// ExtensionConfig holds configuration for the extension handler.
type ExtensionConfig struct {
	RegistryURL  string
	WorkspaceDir string
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
		store:    store,
		sandbox:  sandbox,
		registry: registry,
		config:   cfg,
		progress: NewBuildProgressTracker(),
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

	var req UpdateExtensionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, "invalid request body")
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
	if req.Config != nil {
		if ml, ok := req.Config["memory_limit"]; ok {
			switch v := ml.(type) {
			case float64:
				ext.SandboxConfig.MemoryLimit = int(v)
			case int:
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := h.store.SetEnabled(id, req.Enabled); err != nil {
		httputil.JSONError(w, http.StatusNotFound, "extension not found")
		return
	}

	// Start/stop sandbox based on enabled state
	if req.Enabled {
		ext, err := h.store.GetByID(id)
		if err == nil && ext.IsLSP() {
			// Will be started on first use (lazy start)
		}
	} else {
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

// HandleRegistryIndex fetches the registry index.
func (h *ExtensionHandler) HandleRegistryIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	url := r.URL.Query().Get("url")
	if url == "" {
		url = h.config.RegistryURL
	}
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

// HandleInstall installs an extension from the registry.
func (h *ExtensionHandler) HandleInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req InstallExtensionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" {
		httputil.JSONError(w, http.StatusBadRequest, "name is required")
		return
	}

	// Fetch registry index
	url := req.RegistryURL
	if url == "" {
		url = h.config.RegistryURL
	}
	if url == "" {
		httputil.JSONError(w, http.StatusBadRequest, "registry URL not configured")
		return
	}

	index, err := h.registry.FetchIndex(url)
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to fetch registry: "+err.Error())
		return
	}

	// Find entry
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

	// Check if already installed
	existing, _ := h.store.GetByID(entry.Name)
	if existing != nil {
		httputil.JSONError(w, http.StatusConflict, "extension already installed: "+req.Name)
		return
	}

	// Git-pinned (self-hosted) path: the registry entry pins a repo + commit.
	// Check out and verify the exact commit, materialize its files, and load the
	// manifest from the verified checkout. Nothing is installed if verification fails.
	gitPinned := entry.Repo != "" && entry.Commit != ""
	var manifest ExtensionManifest
	targetDir := filepath.Join(h.config.ExtensionsDirOrDefault(), authorDir(entry.Author), entry.Name)
	if gitPinned {
		if err := h.registry.CheckoutExtension(entry.Repo, entry.Commit, entry.Subdir, targetDir); err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, "failed to fetch extension (commit "+entry.Commit+"): "+err.Error())
			return
		}
		manifestData, err := os.ReadFile(filepath.Join(targetDir, "manifest.json"))
		if err != nil {
			httputil.JSONError(w, http.StatusBadRequest, "checked-out extension has no manifest.json: "+err.Error())
			return
		}
		if err := json.Unmarshal(manifestData, &manifest); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, "invalid manifest.json in checked-out extension: "+err.Error())
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

	// Create extension record
	id := fmt.Sprintf("%s-%d", strings.ToLower(entry.Name), time.Now().Unix())
	ext := &Extension{
		ID:       id,
		Name:     entry.Name,
		Version:  entry.Version,
		Type:     entry.Type,
		Manifest: manifest,
		Config:   map[string]any{},
		ContainerConfig: ContainerConfig{
			Image: entry.Image,
		},
		SandboxConfig: SandboxConfig{
			NetworkIsolation: true,
			MemoryLimit:      DefaultExtensionMemory,
		},
		InstallSource: "registry",
		ImageRef:      entry.Image,
		Enabled:       true,
		Repo:          entry.Repo,
		Commit:        entry.Commit,
		Subdir:        entry.Subdir,
	}

	if err := h.store.Create(ext); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to create extension: "+err.Error())
		return
	}

	// Build/pull image if none is provided pre-built.
	imageRef := entry.Image
	if imageRef == "" {
		var dockerfile string
		if gitPinned {
			// Build from the verified checkout's Dockerfile.
			dfPath := filepath.Join(targetDir, "Dockerfile")
			if data, err := os.ReadFile(dfPath); err == nil {
				dockerfile = string(data)
			}
		} else if entry.DockerfileURL != "" {
			if dockerfile, err = h.registry.FetchDockerfile(ResolveURL(url, entry.DockerfileURL)); err != nil {
				httputil.JSONError(w, http.StatusInternalServerError, "failed to fetch dockerfile: "+err.Error())
				return
			}
		}
		if dockerfile != "" {
			imageRef = fmt.Sprintf("kanban-ext-%s:%s", entry.Name, entry.Version)
			if err := h.registry.BuildImageFromReader(dockerfile, imageRef); err != nil {
				httputil.JSONError(w, http.StatusInternalServerError, "failed to build image: "+err.Error())
				return
			}
			ext.ContainerConfig.Image = imageRef
			ext.ImageRef = imageRef
			if err := h.store.Update(ext.ID, ext); err != nil {
				httputil.JSONError(w, http.StatusInternalServerError, "failed to update extension: "+err.Error())
				return
			}
		}
	}

	statuses := h.sandbox.ListRunning()
	httputil.JSONResponse(w, http.StatusCreated, h.toResponse(ext, statuses))

	// Notify frontend to refresh UI injection slots
	if h.onExtensionChanged != nil {
		h.onExtensionChanged()
	}
}

// HandleBuildProgress streams build progress via SSE.
func (h *ExtensionHandler) HandleBuildProgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	extName := r.URL.Query().Get("name")
	if extName == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	sub := h.progress.Subscribe(extName)

	// Use request context for cleanup
	ctx := r.Context()
	go func() {
		<-ctx.Done()
		// Cleanup will happen when channel closes
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case bp, ok := <-sub:
			if !ok {
				return
			}
			data, _ := json.Marshal(bp)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// HandleCheckUpdates checks for available updates.
func (h *ExtensionHandler) HandleCheckUpdates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	url := r.URL.Query().Get("url")
	if url == "" {
		url = h.config.RegistryURL
	}
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
				})
			}
			break
		}
	}

	httputil.JSONResponse(w, http.StatusOK, updates)
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

	// Fetch registry index
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

	// Find the registry entry
	var entry *RegistryEntry
	for i, e := range index {
		if e.Name == ext.Name {
			entry = &index[i]
			break
		}
	}
	if entry == nil {
		httputil.JSONError(w, http.StatusNotFound, "extension not found in registry: "+ext.Name)
		return
	}

	// Stop sandbox if running
	_ = h.sandbox.Stop(ext.ID)

	gitPinned := entry.Repo != "" && entry.Commit != ""
	imageRef := fmt.Sprintf("kanban-ext-%s:%s", ext.Name, entry.Version)

	if gitPinned {
		// Re-checkout the newly vetted commit (verified) and rebuild from it.
		targetDir := filepath.Join(h.config.ExtensionsDirOrDefault(), authorDir(entry.Author), entry.Name)
		if err := h.registry.CheckoutExtension(entry.Repo, entry.Commit, entry.Subdir, targetDir); err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, "failed to fetch extension (commit "+entry.Commit+"): "+err.Error())
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
			h.progress.StartBuild(ext.Name)
			if err := h.registry.BuildImageFromReader(string(data), imageRef); err != nil {
				h.progress.CompleteBuild(ext.Name, err)
				httputil.JSONError(w, http.StatusInternalServerError, "failed to build image: "+err.Error())
				return
			}
			h.progress.CompleteBuild(ext.Name, nil)
		} else {
			// No Dockerfile (UI/CSS/agent extension) — nothing to build.
			imageRef = ""
		}
	} else {
		// Rebuild from Dockerfile if available, otherwise pull image
		if entry.DockerfileURL != "" {
			h.progress.StartBuild(ext.Name)
			dockerfileURL := ResolveURL(url, entry.DockerfileURL)
			dockerfile, err := h.registry.FetchDockerfile(dockerfileURL)
			if err != nil {
				h.progress.CompleteBuild(ext.Name, fmt.Errorf("failed to fetch dockerfile: %w", err))
				httputil.JSONError(w, http.StatusInternalServerError, "failed to fetch dockerfile: "+err.Error())
				return
			}

			errCh := make(chan error, 1)
			go func() {
				err := h.registry.BuildImageFromReader(dockerfile, imageRef)
				errCh <- err
			}()

			// Stream build output to progress
			go func() {
				<-errCh
			}()

			err = <-errCh
			if err != nil {
				h.progress.CompleteBuild(ext.Name, fmt.Errorf("failed to build image: %w", err))
				httputil.JSONError(w, http.StatusInternalServerError, "failed to build image: "+err.Error())
				return
			}
			h.progress.CompleteBuild(ext.Name, nil)
		} else if entry.Image != "" {
			h.progress.StartBuild(ext.Name)
			err := h.registry.PullImage(entry.Image)
			if err != nil {
				h.progress.CompleteBuild(ext.Name, fmt.Errorf("failed to pull image: %w", err))
				httputil.JSONError(w, http.StatusInternalServerError, "failed to pull image: "+err.Error())
				return
			}
			imageRef = entry.Image
			h.progress.CompleteBuild(ext.Name, nil)
		} else {
			httputil.JSONError(w, http.StatusBadRequest, "extension has no image or dockerfile to update")
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
		httputil.JSONError(w, http.StatusInternalServerError, "failed to update extension: "+err.Error())
		return
	}

	// Update built_at timestamp
	if err := h.store.SetBuiltAt(id); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to update build timestamp: "+err.Error())
		return
	}

	// Notify listeners (e.g., LSP handler to broadcast reset to frontend)
	if h.onImageUpdated != nil {
		h.onImageUpdated(id)
	}

	statuses := h.sandbox.ListRunning()
	ext, _ = h.store.GetByID(id)
	httputil.JSONResponse(w, http.StatusOK, h.toResponse(ext, statuses))
}

// HandleProxy proxies requests to external APIs on behalf of an extension.
// The extension must declare a `proxy` capability in its manifest with
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
	if ext.Manifest.Capabilities.GetProxy() == nil {
		httputil.JSONError(w, http.StatusForbidden, "extension does not have proxy capability")
		return
	}

	var req struct {
		URL    string `json:"url"`
		Method string `json:"method"`
		Body   string `json:"body,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.URL == "" {
		httputil.JSONError(w, http.StatusBadRequest, "url is required")
		return
	}
	if req.Method == "" {
		req.Method = http.MethodGet
	}

	// Validate domain against allowed_domains
	u, err := parseURL(req.URL)
	if err != nil {
		httputil.JSONError(w, http.StatusBadRequest, "invalid url: "+err.Error())
		return
	}
	allowed := false
	for _, d := range ext.Manifest.Capabilities.GetProxy().AllowedDomains {
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
	for k, template := range ext.Manifest.Capabilities.GetProxy().Headers {
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

	// Execute proxy request
	client := &http.Client{Timeout: 30 * time.Second}
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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.JSONError(w, http.StatusBadRequest, "invalid request body")
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
	Name        string `json:"name"`
	RegistryURL string `json:"registry_url,omitempty"`
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
	Ports           []types.AssignedPort `json:"ports,omitempty"`
	InstalledAt     string               `json:"installed_at"`
	BuiltAt         string               `json:"built_at"`
	Repo            string               `json:"repo,omitempty"`
	Commit          string               `json:"commit,omitempty"`
}

// ExtensionUpdate describes an available update.
type ExtensionUpdate struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	CurrentVersion   string `json:"current_version"`
	AvailableVersion string `json:"available_version"`
	CurrentCommit    string `json:"current_commit,omitempty"`
	AvailableCommit  string `json:"available_commit,omitempty"`
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
	return ExtensionResponse{
		ID:              ext.ID,
		Name:            ext.Name,
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
}
