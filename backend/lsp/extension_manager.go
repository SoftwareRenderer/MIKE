package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"kanban/backend/extensions"
	"kanban/backend/store"
	sandbox "kanban/pkg/bridge"
)

// ExtensionLSPManager manages LSP processes through extension sandboxes.
// It implements the lspManager interface and uses the extension system
// to discover available language servers and run them in sandboxes.
type ExtensionLSPManager struct {
	extStore   *extensions.ExtensionStore
	extSandbox *extensions.ExtensionSandboxManager
	projects   *store.Store
	mu         sync.Mutex
	clients    map[string]*sandboxClient // workspaceKey → client state
	// serverCh receives messages from all LSP servers
	serverCh chan json.RawMessage
}

// sandboxClient tracks the state of a single LSP connection in a sandbox.
type sandboxClient struct {
	bridgeClient *sandbox.Client
	workspace    string // host workspace path
	language     string
	extensionID  string
	// Project-based mount mapping for URI translation.
	// Each registered project is mounted at /mnt/<project-dirname>.
	// projectMounts maps host project path → container mount path.
	projectMounts map[string]string
}

// translateURIs recursively translates file URIs in the provided data.
// If toContainer is true, it translates host paths to container paths using projectMounts.
// If toContainer is false, it translates container paths back to host paths.
func translateURIs(val any, workspacePath string, projectMounts map[string]string, toContainer bool) any {
	switch v := val.(type) {
	case string:
		if !strings.HasPrefix(v, "file://") {
			return v
		}
		path := strings.TrimPrefix(v, "file://")
		absWorkspace, _ := filepath.Abs(workspacePath)

		if toContainer {
			// Host -> Container
			if strings.HasPrefix(path, absWorkspace) {
				mount := "/mnt/" + filepath.Base(absWorkspace)
				return "file://" + strings.Replace(path, absWorkspace, mount, 1)
			}
			for host, container := range projectMounts {
				if strings.HasPrefix(path, host) {
					return "file://" + strings.Replace(path, host, container, 1)
				}
			}
		} else {
			// Container -> Host
			for host, container := range projectMounts {
				if strings.HasPrefix(path, container) {
					return "file://" + strings.Replace(path, container, host, 1)
				}
			}
			if strings.HasPrefix(path, "/mnt/"+filepath.Base(absWorkspace)) {
				return "file://" + strings.Replace(path, "/mnt/"+filepath.Base(absWorkspace), absWorkspace, 1)
			}
		}
		return v
	case map[string]any:
		for k, val := range v {
			v[k] = translateURIs(val, workspacePath, projectMounts, toContainer)
		}
		return v
	case []any:
		for i, val := range v {
			v[i] = translateURIs(val, workspacePath, projectMounts, toContainer)
		}
		return v
	default:
		return v
	}
}

// translateURIsToContainer translates file URIs from host paths to container paths.
func translateURIsToContainer(msg json.RawMessage, workspacePath string, projectMounts map[string]string) json.RawMessage {
	var data any
	if err := json.Unmarshal(msg, &data); err != nil {
		return msg
	}
	translated := translateURIs(data, workspacePath, projectMounts, true)
	if b, err := json.Marshal(translated); err == nil {
		return b
	}
	return msg
}

// translateURIsToHost translates file URIs from container paths back to host paths.
func translateURIsToHost(msg json.RawMessage, workspacePath string, projectMounts map[string]string) json.RawMessage {
	var data any
	if err := json.Unmarshal(msg, &data); err != nil {
		return msg
	}
	translated := translateURIs(data, workspacePath, projectMounts, false)
	if b, err := json.Marshal(translated); err == nil {
		return b
	}
	return msg
}

// NewExtensionLSPManager creates a new ExtensionLSPManager.
func NewExtensionLSPManager(extStore *extensions.ExtensionStore, extSandbox *extensions.ExtensionSandboxManager, projects *store.Store) *ExtensionLSPManager {
	return &ExtensionLSPManager{
		extStore:   extStore,
		extSandbox: extSandbox,
		projects:   projects,
		clients:    make(map[string]*sandboxClient),
		serverCh:   make(chan json.RawMessage, 256),
	}
}

// GetMountPath returns the container mount path for a given host workspace path.
// Returns the host path if no mapping is found.
func (m *ExtensionLSPManager) GetMountPath(workspacePath string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, sc := range m.clients {
		if containerPath, ok := sc.projectMounts[workspacePath]; ok {
			return containerPath
		}
	}
	return workspacePath
}

// Close stops the manager and removes all client state.
func (m *ExtensionLSPManager) Close() {
	m.mu.Lock()
	for key := range m.clients {
		delete(m.clients, key)
	}
	m.mu.Unlock()
}

func (m *ExtensionLSPManager) GetRegistry() *ExtensionRegistry {
	return &ExtensionRegistry{extStore: m.extStore}
}

// GetOrCreate gets or creates an LSP connection for the given workspace and language.
// It looks up the matching extension by file extension and starts the LSP server in the sandbox.
func (m *ExtensionLSPManager) GetOrCreate(workspacePath, language string, cfg *ServerConfig) (*LSPProcess, error) {
	workspaceKey := processKey(workspacePath, language)

	m.mu.Lock()
	sc, ok := m.clients[workspaceKey]
	if ok {
		// Check if the sandbox bridge is still alive (e.g., after a rebuild)
		if sc.bridgeClient != nil && sc.bridgeClient.IsAlive() {
			m.mu.Unlock()
			return &LSPProcess{
				Workspace: workspacePath,
				Language:  language,
			}, nil
		}
		// Stale entry — remove and recreate
		delete(m.clients, workspaceKey)
	}
	m.mu.Unlock()

	// Look up the matching extension by language
	ext, err := m.lookupExtension(language)
	if err != nil {
		return nil, fmt.Errorf("no LSP extension for language %q: %w", language, err)
	}

	// Collect all registered project paths for mount-based sandbox creation
	projectMounts := m.collectProjectMounts(workspacePath)

	// Get or create the sandbox.
	// When reusing an existing sandbox (grace period), the LSP process from the
	// previous workspace may still be running. LSPStart handles this by restarting
	// the LSP process to get a fresh session for the new workspace.
	bridgeClient, _, err := m.getOrCreateSandbox(ext, projectMounts)
	if err != nil {
		return nil, err
	}

	// Start (or restart) the LSP server process in the sandbox.
	// Always called — the agent restarts the LSP if already running.
	ctx := context.Background()
	lspConfig := ext.Manifest.Capabilities.GetLSP()
	if lspConfig == nil {
		return nil, fmt.Errorf("extension %s has no LSP config", ext.Name)
	}

	command := lspConfig.Command
	args := lspConfig.Args

	if err := bridgeClient.LSPStart(ctx, command, args); err != nil {
		return nil, fmt.Errorf("start LSP server in sandbox: %w", err)
	}

	client := &sandboxClient{
		bridgeClient:  bridgeClient,
		workspace:     workspacePath,
		language:      language,
		extensionID:   ext.ID,
		projectMounts: projectMounts,
	}

	m.mu.Lock()
	m.clients[workspaceKey] = client
	m.mu.Unlock()

	log.Printf("[lsp] started extension %s (lang=%s) in sandbox for workspace %s", ext.Name, language, workspacePath)

	return &LSPProcess{
		Workspace: workspacePath,
		Language:  language,
	}, nil
}

// Release releases the LSP connection when no clients are using it.
func (m *ExtensionLSPManager) Release(workspacePath, language string) {
	workspaceKey := processKey(workspacePath, language)

	m.mu.Lock()
	sc, ok := m.clients[workspaceKey]
	if !ok {
		m.mu.Unlock()
		return
	}
	delete(m.clients, workspaceKey)
	m.mu.Unlock()

	// Release the sandbox
	m.extSandbox.Release(sc.extensionID)

	log.Printf("[lsp] released extension sandbox for workspace=%s lang=%s", workspacePath, language)
}

// getOrCreateSandbox gets or creates an extension sandbox.
// Returns the bridge client and a boolean indicating whether a NEW sandbox was created.
// When reusing an existing sandbox (grace period), newSandbox is false.
func (m *ExtensionLSPManager) getOrCreateSandbox(ext *extensions.Extension, projectMounts map[string]string) (*sandbox.Client, bool, error) {
	// Check if sandbox already exists and is alive
	bridgeClient, err := m.extSandbox.GetOrCreate(ext.ID)
	if err == nil && bridgeClient != nil && bridgeClient.IsAlive() {
		// Existing sandbox — check if mounts are compatible
		if m.extSandbox.MountsCompatible(ext.ID, projectMounts) {
			log.Printf("[lsp] reusing existing sandbox for extension %s", ext.ID)
			return bridgeClient, false, nil
		}
		// Sandbox exists but mounts are stale — recreate
		log.Printf("[lsp] sandbox mounts changed, recreating for extension %s", ext.ID)
		_ = m.extSandbox.Stop(ext.ID)
	}

	// Create a new sandbox
	log.Printf("[lsp] creating sandbox for extension %s", ext.ID)
	_, err = m.extSandbox.Create(ext, projectMounts)
	if err != nil {
		return nil, false, fmt.Errorf("create extension sandbox: %w", err)
	}
	bridgeClient, err = m.extSandbox.Start(ext.ID)
	if err != nil {
		return nil, false, fmt.Errorf("start extension sandbox: %w", err)
	}
	return bridgeClient, true, nil
}

// SendToServer sends a JSON-RPC message to the LSP server in the sandbox.
// File URIs are translated from host workspace path to container path (/workspace).
func (m *ExtensionLSPManager) SendToServer(process *LSPProcess, msg json.RawMessage) error {
	workspaceKey := processKey(process.Workspace, process.Language)

	m.mu.Lock()
	sc, ok := m.clients[workspaceKey]
	if !ok || sc.bridgeClient == nil || !sc.bridgeClient.IsAlive() {
		// Stale or dead — clean up and signal caller to reconnect
		if ok {
			delete(m.clients, workspaceKey)
		}
		m.mu.Unlock()
		return fmt.Errorf("sandbox client stale for workspace=%s lang=%s", process.Workspace, process.Language)
	}
	m.mu.Unlock()

	// Translate file URIs from host path to container path
	translated := translateURIsToContainer(msg, sc.workspace, sc.projectMounts)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	response, err := sc.bridgeClient.LSPSend(ctx, translated)
	if err != nil {
		// Connection died — clean up for next retry
		m.mu.Lock()
		if sc2, ok2 := m.clients[workspaceKey]; ok2 && sc2 == sc {
			delete(m.clients, workspaceKey)
		}
		m.mu.Unlock()
		return fmt.Errorf("send to LSP server: %w", err)
	}

	// Notifications return empty response — nothing to route
	if len(response) == 0 {
		return nil
	}

	// Translate file URIs back from container path to host path
	response = translateURIsToHost(response, sc.workspace, sc.projectMounts)

	// Route the response back through the server channel
	// This allows the handler's serverMessageRouter to process it
	select {
	case m.serverCh <- response:
	default:
		log.Printf("[lsp] server channel full, dropping message")
	}

	return nil
}

// ServerMessageCh returns a channel that receives messages from LSP servers.
func (m *ExtensionLSPManager) ServerMessageCh() <-chan json.RawMessage {
	return m.serverCh
}

// collectProjectMounts queries registered projects and builds a map of
// host project path → container mount path (/mnt/<project-dirname>).
// This allows the extension sandbox to mount all projects at once,
// so the container can serve any workspace without recreation.
func (m *ExtensionLSPManager) collectProjectMounts(workspacePath string) map[string]string {
	mounts := make(map[string]string)
	if m.projects == nil {
		return mounts
	}
	projects, err := m.projects.ListProjects()
	if err != nil {
		log.Printf("[lsp] failed to list projects for mount setup: %v", err)
		return mounts
	}
	for _, p := range projects {
		absPath, err := filepath.Abs(p.Path)
		if err != nil {
			continue
		}
		mounts[absPath] = "/mnt/" + filepath.Base(absPath)
	}
	// Ensure the current workspace is included even if not a registered project
	absWorkspace, err := filepath.Abs(workspacePath)
	if err == nil {
		if _, ok := mounts[absWorkspace]; !ok {
			mounts[absWorkspace] = "/mnt/" + filepath.Base(absWorkspace)
		}
	}
	return mounts
}

// lookupExtension finds the matching LSP extension for a given language.
func (m *ExtensionLSPManager) lookupExtension(language string) (*extensions.Extension, error) {
	extensions, err := m.extStore.GetByType(extensions.ExtensionTypeLSP)
	if err != nil {
		return nil, err
	}

	// Find the extension that matches this language
	for _, ext := range extensions {
		if !ext.Enabled {
			continue
		}
		lspCfg := ext.Manifest.Capabilities.GetLSP()
		if lspCfg == nil {
			continue
		}
		// Check if the extension handles this language
		if lspCfg.Language == language {
			return &ext, nil
		}
		// Also check file extensions
		for _, extn := range lspCfg.Extensions {
			if filepath.Ext(extn) == "."+language {
				return &ext, nil
			}
		}
	}

	return nil, fmt.Errorf("no enabled LSP extension found for language %q", language)
}

// ExtensionRegistry provides language server configuration from installed extensions.
// It implements a lookup interface compatible with the existing Registry.
type ExtensionRegistry struct {
	extStore *extensions.ExtensionStore
}

// NewExtensionRegistry creates a new ExtensionRegistry.
func NewExtensionRegistry(extStore *extensions.ExtensionStore) *ExtensionRegistry {
	return &ExtensionRegistry{extStore: extStore}
}

// Lookup finds a ServerConfig for the given file path or workspace.
// It searches installed LSP extensions for a matching file pattern.
func (r *ExtensionRegistry) Lookup(fileOrPath string) *ServerConfig {
	extensions, err := r.extStore.GetByType(extensions.ExtensionTypeLSP)
	if err != nil {
		return nil
	}

	// Extract file extension and basename
	ext := filepath.Ext(fileOrPath)
	base := filepath.Base(fileOrPath)

	for _, extItem := range extensions {
		if !extItem.Enabled {
			continue
		}
		lspCfg := extItem.Manifest.Capabilities.GetLSP()
		if lspCfg == nil {
			continue
		}
		// Check file extensions
		for _, extn := range lspCfg.Extensions {
			if extn == ext || extn == ext[1:] {
				return r.toServerConfig(&extItem)
			}
		}
		// Check language directly
		if lspCfg.Language != "" {
			if ext == "."+lspCfg.Language || base == lspCfg.Language {
				return r.toServerConfig(&extItem)
			}
		}
	}

	return nil
}

// GetAvailable returns all available language servers from installed extensions.
func (r *ExtensionRegistry) GetAvailable() []string {
	extensions, err := r.extStore.GetByType(extensions.ExtensionTypeLSP)
	if err != nil {
		return nil
	}

	langs := make([]string, 0, len(extensions))
	for _, ext := range extensions {
		lspCfg := ext.Manifest.Capabilities.GetLSP()
		if ext.Enabled && lspCfg != nil && lspCfg.Language != "" {
			langs = append(langs, lspCfg.Language)
		}
	}
	return langs
}

// GetConfig returns the ServerConfig for a given language.
func (r *ExtensionRegistry) GetConfig(language string) *ServerConfig {
	extensions, err := r.extStore.GetByType(extensions.ExtensionTypeLSP)
	if err != nil {
		return nil
	}

	for _, ext := range extensions {
		if !ext.Enabled {
			continue
		}
		lspCfg := ext.Manifest.Capabilities.GetLSP()
		if lspCfg != nil && lspCfg.Language == language {
			return r.toServerConfig(&ext)
		}
	}
	return nil
}

// toServerConfig converts an Extension to a ServerConfig.
// It applies runtime overrides for known extensions (e.g., tsserver.path for TypeScript).
func (r *ExtensionRegistry) toServerConfig(ext *extensions.Extension) *ServerConfig {
	lspCfg := ext.Manifest.Capabilities.GetLSP()
	if lspCfg == nil {
		return nil
	}

	initParams := lspCfg.InitParamsOverride

	// Ensure typescript-language-server has tsserver.path pointing to global install.
	// The sandbox image installs TypeScript globally, not in the workspace.
	// typescript-language-server expects: initializationOptions.tsserver.path (nested object).
	if lspCfg.Language == "typescript" || strings.Contains(ext.Name, "typescript") {
		if initParams == nil {
			initParams = make(map[string]any)
		}
		opts, ok := initParams["initializationOptions"].(map[string]any)
		if !ok {
			opts = make(map[string]any)
			initParams["initializationOptions"] = opts
		}
		tsserverOpts, ok := opts["tsserver"].(map[string]any)
		if !ok {
			tsserverOpts = make(map[string]any)
			opts["tsserver"] = tsserverOpts
		}
		if _, hasPath := tsserverOpts["path"]; !hasPath {
			tsserverOpts["path"] = "/usr/local/lib/node_modules/typescript"
		}
	}

	return &ServerConfig{
		Language:           lspCfg.Language,
		Command:            lspCfg.Command,
		Args:               lspCfg.Args,
		Extensions:         lspCfg.Extensions,
		InitParamsOverride: initParams,
	}
}
