package extensions

import (
	"encoding/json"
	"fmt"
	"net/http"
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
	mu       sync.RWMutex
	builds   map[string]*BuildProgress
	subscribers map[string][]chan *BuildProgress
}

// NewBuildProgressTracker creates a new tracker.
func NewBuildProgressTracker() *BuildProgressTracker {
	return &BuildProgressTracker{
		builds:        make(map[string]*BuildProgress),
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
	store               *ExtensionStore
	sandbox             *ExtensionSandboxManager
	registry            *RegistryClient
	config              *ExtensionConfig
	progress            *BuildProgressTracker
	onImageUpdated      func(extID string)      // callback after successful image update
	onExtensionChanged  func()                   // callback after any extension change (enable/disable/create/delete)
}

// ExtensionConfig holds configuration for the extension handler.
type ExtensionConfig struct {
	RegistryURL   string
	WorkspaceDir  string
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

// HandleCreate creates a new user-defined extension.
func (h *ExtensionHandler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req CreateExtensionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" {
		httputil.JSONError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Type != ExtensionTypeLSP && req.Type != ExtensionTypeUI {
		httputil.JSONError(w, http.StatusBadRequest, "invalid type: must be 'lsp' or 'ui'")
		return
	}
	if req.Version == "" {
		req.Version = "1.0.0"
	}

	// Generate ID from name
	id := fmt.Sprintf("%s-%d", strings.ToLower(req.Name), time.Now().Unix())

	ext := &Extension{
		ID:            id,
		Name:          req.Name,
		Version:       req.Version,
		Type:          req.Type,
		Manifest:      req.Manifest,
		Config:        req.Config,
		ContainerConfig: req.ContainerConfig,
		SandboxConfig:   req.SandboxConfig,
		InstallSource: "manual",
		Enabled:       true,
	}

	if err := h.store.Create(ext); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to create extension: "+err.Error())
		return
	}

	statuses := h.sandbox.ListRunning()
	httputil.JSONResponse(w, http.StatusCreated, h.toResponse(ext, statuses))

	// Notify frontend to refresh UI injection slots
	if h.onExtensionChanged != nil {
		h.onExtensionChanged()
	}
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
			ext.Manifest.LicenseURL = req.Manifest.LicenseURL
		}
		if req.Manifest.Homepage != "" {
			ext.Manifest.Homepage = req.Manifest.Homepage
		}
	}
	if req.Config != nil {
		// Merge config: preserve existing sensitive values when masked placeholder is sent
		merged := make(map[string]any, len(req.Config))
		for k, v := range req.Config {
			strVal, isStr := v.(string)
			isSensitive := ext.Manifest.ConfigSchema != nil && ext.Manifest.ConfigSchema.Properties[k].Sensitive
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
	if req.Manifest.LSP != nil {
		if ext.Manifest.LSP == nil {
			ext.Manifest.LSP = &LSPConfig{}
		}
		if req.Manifest.LSP.Command != "" {
			ext.Manifest.LSP.Command = req.Manifest.LSP.Command
		}
		if len(req.Manifest.LSP.Args) > 0 {
			ext.Manifest.LSP.Args = req.Manifest.LSP.Args
		}
		if len(req.Manifest.LSP.Extensions) > 0 {
			ext.Manifest.LSP.Extensions = req.Manifest.LSP.Extensions
		}
		if req.Manifest.LSP.Language != "" {
			ext.Manifest.LSP.Language = req.Manifest.LSP.Language
		}
		if len(req.Manifest.LSP.InitParamsOverride) > 0 {
			if ext.Manifest.LSP.InitParamsOverride == nil {
				ext.Manifest.LSP.InitParamsOverride = make(map[string]any)
			}
			for k, v := range req.Manifest.LSP.InitParamsOverride {
				ext.Manifest.LSP.InitParamsOverride[k] = v
			}
		}
	}

	if err := h.store.Update(id, ext); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to update extension: "+err.Error())
		return
	}

	statuses := h.sandbox.ListRunning()
	httputil.JSONResponse(w, http.StatusOK, h.toResponse(ext, statuses))
}

// HandleDelete removes an extension.
func (h *ExtensionHandler) HandleDelete(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Stop sandbox if running
	_ = h.sandbox.Destroy(id)

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

	// Fetch manifest if available
	var manifest ExtensionManifest
	if entry.ManifestURL != "" {
		manifestURL := ResolveURL(url, entry.ManifestURL)
		manifest, err = h.registry.FetchManifest(manifestURL)
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, "failed to fetch manifest: "+err.Error())
			return
		}
	} else {
		manifest = ExtensionManifest{
			DisplayName: entry.DisplayName,
			Description: entry.Description,
			Author:      entry.Author,
			LicenseURL:  entry.LicenseURL,
		}
	}

	// Create extension record
	id := fmt.Sprintf("%s-%d", strings.ToLower(entry.Name), time.Now().Unix())
	ext := &Extension{
		ID:            id,
		Name:          entry.Name,
		Version:       entry.Version,
		Type:          entry.Type,
		Manifest:      manifest,
		Config:        map[string]any{},
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
	}

	if err := h.store.Create(ext); err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to create extension: "+err.Error())
		return
	}

	// Build image from Dockerfile if no pre-built image is provided
	imageRef := entry.Image
	if imageRef == "" && entry.DockerfileURL != "" {
		imageRef = fmt.Sprintf("kanban-ext-%s:%s", entry.Name, entry.Version)
		dockerfileURL := ResolveURL(url, entry.DockerfileURL)
		dockerfile, err := h.registry.FetchDockerfile(dockerfileURL)
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, "failed to fetch dockerfile: "+err.Error())
			return
		}
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
			if entry.Name == ext.Name {
				if UpdateAvailable(ext.Version, entry.Version) {
					updates = append(updates, ExtensionUpdate{
						ID:            ext.ID,
						Name:          ext.Name,
						CurrentVersion: ext.Version,
						AvailableVersion: entry.Version,
					})
				}
				break
			}
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

	// Fetch the latest manifest
	if entry.ManifestURL != "" {
		manifestURL := ResolveURL(url, entry.ManifestURL)
		manifest, err := h.registry.FetchManifest(manifestURL)
		if err == nil {
			ext.Manifest = manifest
		}
		// We don't fail the whole update if manifest fetch fails, but we could log it
	}

	// Stop sandbox if running
	_ = h.sandbox.Stop(ext.ID)

	imageRef := fmt.Sprintf("kanban-ext-%s:%s", ext.Name, entry.Version)

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

	// Update extension with new image ref and version
	ext.Version = entry.Version
	ext.ImageRef = imageRef
	ext.ContainerConfig.Image = imageRef
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

// --- Request/Response types ---

// CreateExtensionRequest is the request body for creating an extension.
type CreateExtensionRequest struct {
	Name            string            `json:"name"`
	Version         string            `json:"version,omitempty"`
	Type            string            `json:"type"`
	Manifest        ExtensionManifest `json:"manifest"`
	Config          map[string]any    `json:"config,omitempty"`
	ContainerConfig ContainerConfig   `json:"container_config,omitempty"`
	SandboxConfig   SandboxConfig     `json:"sandbox_config,omitempty"`
}

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
	ID              string                 `json:"id"`
	Name            string                 `json:"name"`
	Version         string                 `json:"version"`
	Type            string                 `json:"type"`
	Manifest        ExtensionManifest      `json:"manifest"`
	Config          map[string]any         `json:"config"`
	ContainerConfig ContainerConfig        `json:"container_config"`
	SandboxConfig   SandboxConfig          `json:"sandbox_config"`
	InstallSource   string                 `json:"install_source"`
	ImageRef        string                 `json:"image_ref,omitempty"`
	Enabled         bool                   `json:"enabled"`
	Running         bool                   `json:"running"`
	Ports           []types.AssignedPort   `json:"ports,omitempty"`
	InstalledAt     string                 `json:"installed_at"`
	BuiltAt         string                 `json:"built_at"`
}

// ExtensionUpdate describes an available update.
type ExtensionUpdate struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	CurrentVersion   string `json:"current_version"`
	AvailableVersion string `json:"available_version"`
}

// maskConfig masks sensitive config values based on the manifest's config_schema.
// Sensitive fields are replaced with a bullet placeholder.
const maskedValue = "\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022" // 10 bullets

func maskConfig(config map[string]any, schema *ConfigSchema) map[string]any {
	if schema == nil || schema.Properties == nil || config == nil {
		return config
	}
	result := make(map[string]any, len(config))
	for k, v := range config {
		prop, ok := schema.Properties[k]
		if !ok || !prop.Sensitive {
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
	}
}
