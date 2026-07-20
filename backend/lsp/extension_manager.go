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
	extStore     *extensions.ExtensionStore
	extSandbox   *extensions.ExtensionSandboxManager
	projects     *store.Store
	mu           sync.Mutex
	clients      map[string]*sandboxClient // workspaceKey → client state
	// serverCh receives messages from all LSP servers
	serverCh chan json.RawMessage
	// readLoopDone signals the background read goroutines to stop
	readLoopDone chan struct{}
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
	// readLoopDone signals the read goroutine for this client to stop
	readLoopDone chan struct{}
}

// translateURIsToContainer translates file URIs from host paths to container paths.
// Uses project-based mounts: each project at /mnt/<project-dirname>.
// E.g., /home/user/projects/kanban mounted at /mnt/kanban means:
//   file:///home/user/projects/kanban/main.go -> file:///mnt/kanban/main.go
func translateURIsToContainer(msg json.RawMessage, workspacePath string, projectMounts map[string]string) json.RawMessage {
	if len(projectMounts) == 0 {
		return msg
	}
	absWorkspace, err := filepath.Abs(workspacePath)
	if err != nil {
		return msg
	}
	result := string(msg)
	// Try each project mount mapping
	for hostPath, containerPath := range projectMounts {
		oldPrefix := "file://" + hostPath
		newPrefix := "file://" + containerPath
		result = strings.ReplaceAll(result, oldPrefix, newPrefix)
	}
	// Fallback: try workspace path directly
	if result == string(msg) {
		oldPrefix := "file://" + absWorkspace
		newPrefix := "file:///mnt/" + filepath.Base(absWorkspace)
		result = strings.ReplaceAll(result, oldPrefix, newPrefix)
	}
	if result == string(msg) {
		return msg // no change
	}
	return json.RawMessage(result)
}

// translateURIsToHost translates file URIs from container paths back to host paths.
func translateURIsToHost(msg json.RawMessage, workspacePath string, projectMounts map[string]string) json.RawMessage {
	if len(projectMounts) == 0 {
		return msg
	}
	absWorkspace, err := filepath.Abs(workspacePath)
	if err != nil {
		return msg
	}
	result := string(msg)
	// Try each project mount mapping (reverse)
	for hostPath, containerPath := range projectMounts {
		oldPrefix := "file://" + containerPath
		newPrefix := "file://" + hostPath
		result = strings.ReplaceAll(result, oldPrefix, newPrefix)
	}
	// Fallback: try /mnt/<basename> → workspace
	if result == string(msg) {
		oldPrefix := "file:///mnt/" + filepath.Base(absWorkspace)
		newPrefix := "file://" + absWorkspace
		result = strings.ReplaceAll(result, oldPrefix, newPrefix)
	}
	if result == string(msg) {
		return msg // no change
	}
	return json.RawMessage(result)
}

// NewExtensionLSPManager creates a new ExtensionLSPManager.
func NewExtensionLSPManager(extStore *extensions.ExtensionStore, extSandbox *extensions.ExtensionSandboxManager, projects *store.Store) *ExtensionLSPManager {
	m := &ExtensionLSPManager{
		extStore:     extStore,
		extSandbox:   extSandbox,
		projects:     projects,
		clients:      make(map[string]*sandboxClient),
		serverCh:     make(chan json.RawMessage, 256),
		readLoopDone: make(chan struct{}),
	}
	return m
}

// Close stops the manager and all background goroutines.
func (m *ExtensionLSPManager) Close() {
	close(m.readLoopDone)
	m.mu.Lock()
	for key, sc := range m.clients {
		close(sc.readLoopDone)
		delete(m.clients, key)
	}
	m.mu.Unlock()
}

// GetOrCreate gets or creates an LSP connection for the given workspace and language.
// It looks up the matching extension by file extension and starts the LSP server in the sandbox.
func (m *ExtensionLSPManager) GetOrCreate(workspacePath, language string, cfg *ServerConfig) (*LSPProcess, error) {
	workspaceKey := processKey(workspacePath, language)

	m.mu.Lock()
	_, ok := m.clients[workspaceKey]
	if ok {
		m.mu.Unlock()
		return &LSPProcess{
			Workspace: workspacePath,
			Language:  language,
		}, nil
	}
	m.mu.Unlock()

	// Look up the matching extension by language
	ext, err := m.lookupExtension(language)
	if err != nil {
		return nil, fmt.Errorf("no LSP extension for language %q: %w", language, err)
	}

	// Collect all registered project paths for mount-based sandbox creation
	projectMounts := m.collectProjectMounts(workspacePath)

	// Get or create the sandbox
	bridgeClient, err := m.extSandbox.GetOrCreate(ext.ID)
	if err != nil {
		// Sandbox not running — create and start it with project mounts
		log.Printf("[lsp] creating sandbox for extension %s", ext.ID)
		_, err = m.extSandbox.Create(ext, projectMounts)
		if err != nil {
			return nil, fmt.Errorf("create extension sandbox: %w", err)
		}
		bridgeClient, err = m.extSandbox.Start(ext.ID)
		if err != nil {
			return nil, fmt.Errorf("start extension sandbox: %w", err)
		}
	} else if !m.extSandbox.MountsCompatible(ext.ID, projectMounts) {
		// Sandbox exists but mounts are stale — recreate
		log.Printf("[lsp] sandbox mounts changed, recreating for extension %s", ext.ID)
		_ = m.extSandbox.Stop(ext.ID)
		_, err = m.extSandbox.Create(ext, projectMounts)
		if err != nil {
			return nil, fmt.Errorf("create extension sandbox: %w", err)
		}
		bridgeClient, err = m.extSandbox.Start(ext.ID)
		if err != nil {
			return nil, fmt.Errorf("start extension sandbox: %w", err)
		}
	}

	// Start the LSP server process in the sandbox
	ctx := context.Background()
	lspConfig := ext.Manifest.LSP
	if lspConfig == nil {
		return nil, fmt.Errorf("extension %s has no LSP config", ext.Name)
	}

	command := lspConfig.Command
	args := lspConfig.Args

	// Merge with provided config if available
	if cfg != nil && len(cfg.Args) > 0 {
		args = cfg.Args
	}
	if cfg != nil && cfg.Command != "" {
		command = cfg.Command
	}

	if err := bridgeClient.LSPStart(ctx, command, args); err != nil {
		return nil, fmt.Errorf("start LSP server in sandbox: %w", err)
	}

	sc := &sandboxClient{
		bridgeClient:  bridgeClient,
		workspace:     workspacePath,
		language:      language,
		extensionID:   ext.ID,
		projectMounts: projectMounts,
		readLoopDone:  make(chan struct{}),
	}

	m.mu.Lock()
	m.clients[workspaceKey] = sc
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

	// Stop the read loop
	close(sc.readLoopDone)

	// Release the sandbox
	m.extSandbox.Release(sc.extensionID)

	log.Printf("[lsp] released extension sandbox for workspace=%s lang=%s", workspacePath, language)
}

// SendToServer sends a JSON-RPC message to the LSP server in the sandbox.
// File URIs are translated from host workspace path to container path (/workspace).
func (m *ExtensionLSPManager) SendToServer(process *LSPProcess, msg json.RawMessage) error {
	workspaceKey := processKey(process.Workspace, process.Language)

	m.mu.Lock()
	sc, ok := m.clients[workspaceKey]
	m.mu.Unlock()

	if !ok {
		return fmt.Errorf("no sandbox client for workspace=%s lang=%s", process.Workspace, process.Language)
	}

	// Translate file URIs from host path to container path
	translated := translateURIsToContainer(msg, sc.workspace, sc.projectMounts)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	response, err := sc.bridgeClient.LSPSend(ctx, translated)
	if err != nil {
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
		lspCfg := ext.Manifest.LSP
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

// createBridgeClient creates a bridge client for an extension sandbox.
// This is a placeholder implementation that will be wired to the actual
// sandbox bridge connection in the full implementation.
func (m *ExtensionLSPManager) createBridgeClient(extensionID string, port uint16) *sandbox.Client {
	// TODO: Connect to the sandbox bridge via TCP or pipe
	// For now, return nil — will be implemented when sandbox networking is ready
	return nil
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
		lspCfg := extItem.Manifest.LSP
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
		if ext.Enabled && ext.Manifest.LSP != nil && ext.Manifest.LSP.Language != "" {
			langs = append(langs, ext.Manifest.LSP.Language)
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
		if ext.Manifest.LSP != nil && ext.Manifest.LSP.Language == language {
			return r.toServerConfig(&ext)
		}
	}
	return nil
}

// toServerConfig converts an Extension to a ServerConfig.
func (r *ExtensionRegistry) toServerConfig(ext *extensions.Extension) *ServerConfig {
	lspCfg := ext.Manifest.LSP
	if lspCfg == nil {
		return nil
	}

	cfg := &ServerConfig{
		Language:           lspCfg.Language,
		Command:            lspCfg.Command,
		Args:               lspCfg.Args,
		Extensions:         lspCfg.Extensions,
		InitParamsOverride: lspCfg.InitParamsOverride,
	}

	// Merge any user-provided config overrides
	if ext.Config != nil {
		if cmd, ok := ext.Config["command"].(string); ok && cmd != "" {
			cfg.Command = cmd
		}
		if args, ok := ext.Config["args"].([]any); ok && len(args) > 0 {
			cfg.Args = make([]string, len(args))
			for i, a := range args {
				if s, ok := a.(string); ok {
					cfg.Args[i] = s
				}
			}
		}
	}

	return cfg
}
