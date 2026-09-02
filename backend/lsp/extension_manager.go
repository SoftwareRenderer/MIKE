package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"kanban/backend/extensions"
	kanban_sandbox "kanban/backend/sandbox"
	"kanban/backend/types"
	sandbox "kanban/pkg/bridge"
)

// errStaleClient marks a sandbox bridge connection that has outlived its process. The
// handler drops the workspace entry on seeing it so the next request reconnects - match it
// with errors.Is, since matching the text lets a reword disable the recovery.
var errStaleClient = errors.New("stale")

// ServerConfig holds the configuration for a single language server.
type ServerConfig struct {
	Command string   // binary to execute (e.g., "gopls")
	Args    []string // args template, {workspace} replaced with workspace path
	// FileTypes is what the extension declares it serves, and what its server calls each one —
	// the source of the didOpen languageId.
	FileTypes          []extensions.LSPFileType
	Language           string         // routing language, e.g., "go", "typescript"
	InitParamsOverride map[string]any // merged into initialize params sent to server
}

// LanguageIDFor answers what a document notification must call this file — the per-file id, not
// the routing language, because tsserver keeps the first verdict it hears for a URI.
func (c *ServerConfig) LanguageIDFor(name string) string {
	if c == nil {
		return ""
	}
	return (&extensions.LSPConfig{Language: c.Language, FileTypes: c.FileTypes}).LanguageIDFor(name)
}

// lspStartTimeout bounds `lsp_start`, which the bridge answers as soon as it has spawned the
// server, so it only ever waits on a bridge that stopped answering: a whole sandbox coming up
// cold takes well under a second.
const lspStartTimeout = 8 * time.Second

// lspRequestTimeout ceilings one LSP tool request (references, hover, rename). It is derived
// from the caller's context, so stopping a task ends the call instead of letting it hold the
// worktree lock.
const lspRequestTimeout = 15 * time.Second

// lspSendTimeout and lspNotifyTimeout ceiling one sandbox-bridge round trip. Both are derived
// from the caller's context so the caller's deadline always wins; minted from
// context.Background() they would make every budget in the caller advisory.
const (
	lspSendTimeout   = 30 * time.Second
	lspNotifyTimeout = 10 * time.Second
)

// lspInitReplyTimeout bounds the initialize response at four times the slowest healthy reply.
// A server that never finishes initializing must not look like a slow one for half a minute.
const lspInitReplyTimeout = 5 * time.Second

// ExtensionLSPManager manages LSP connections that live inside extension sandboxes, discovering
// each language server through the extension system.
type ExtensionLSPManager struct {
	extStore   *extensions.ExtensionStore
	extSandbox *extensions.ExtensionSandboxManager
	projects   projectPaths
	mu         sync.Mutex
	clients    map[string]*sandboxClient // workspaceKey → client state
	// serverCh receives messages from all LSP servers (responses + notifications)
	serverCh chan json.RawMessage
	// subscribers receive a copy of all LSP server messages (for filtered listening)
	subscribers []chan json.RawMessage
	subMu       sync.Mutex
}

// sandboxClient tracks the state of a single LSP connection in a sandbox.
type sandboxClient struct {
	bridgeClient *sandbox.Client
	workspace    string // host workspace path
	language     string
	extensionID  string
	// projectMounts maps host project path → container mount path (/mnt/<project-dirname>),
	// for URI translation.
	projectMounts map[string]string
	// initMu serializes the handshake on this connection: `initialized` is one flag shared
	// by every caller of EnsureInitialized (tools, the editor channel, the diagnostics pass),
	// so neither the check nor the handshake may race another caller into asking twice.
	initMu sync.Mutex
	// initialized tracks whether the LSP server has been initialized.
	initialized bool
	// openedFiles tracks file URIs that have been sent via didOpen.
	openedFiles map[string]bool
	// openedFileContents tracks the last content sent to the server per URI,
	// so on-disk changes (agent edits between LSP calls) can be re-synced via
	// didChange. Servers like tsserver do not watch the filesystem.
	openedFileContents map[string]string
	// openedFileVersions tracks the last LSP document version sent per URI.
	openedFileVersions map[string]int
	// refs counts the holders that can hand the connection back — the editor WebSocket and a
	// diagnostics pass. Agent tool calls are not counted: they have no lifetime in which to
	// release, so counting them would keep every server alive forever.
	refs int
	// dead is set when the agent's language server for this connection is gone. The bridge
	// connection outlives the server, so without this flag the map keeps handing out a
	// connection whose every request answers `lsp server died`.
	dead atomic.Bool
}

// usable reports whether requests may still go through this connection: both the bridge
// connection and the language server on the far end of it have to be standing.
func (sc *sandboxClient) usable() bool {
	return sc != nil && sc.bridgeClient != nil && sc.bridgeClient.IsAlive() && !sc.dead.Load()
}

// newSandboxClient builds a sandboxClient with its document-sync maps ready to write —
// ensureDocSync writes them unconditionally, so a struct literal panics on the first tool call.
// A new client has no holders; see sandboxClient.refs.
func newSandboxClient(bridgeClient *sandbox.Client, workspace, language, extensionID string, projectMounts map[string]string) *sandboxClient {
	return &sandboxClient{
		bridgeClient:       bridgeClient,
		workspace:          workspace,
		language:           language,
		extensionID:        extensionID,
		projectMounts:      projectMounts,
		openedFiles:        make(map[string]bool),
		openedFileContents: make(map[string]string),
		openedFileVersions: make(map[string]int),
	}
}

// translateURIs recursively rewrites file URIs in data, host paths → container paths when
// toContainer is true and the other way round when it is false.
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

// translateURIsMsg translates file URIs in a JSON message, returning the
// original message unchanged if it is not valid JSON or re-marshal fails.
func translateURIsMsg(msg json.RawMessage, workspacePath string, projectMounts map[string]string, toContainer bool) json.RawMessage {
	var data any
	if err := json.Unmarshal(msg, &data); err != nil {
		return msg
	}
	translated := translateURIs(data, workspacePath, projectMounts, toContainer)
	if b, err := json.Marshal(translated); err == nil {
		return b
	}
	return msg
}

// translateURIsToContainer translates file URIs from host paths to container paths.
func translateURIsToContainer(msg json.RawMessage, workspacePath string, projectMounts map[string]string) json.RawMessage {
	return translateURIsMsg(msg, workspacePath, projectMounts, true)
}

// translateURIsToHost translates file URIs from container paths back to host paths.
func translateURIsToHost(msg json.RawMessage, workspacePath string, projectMounts map[string]string) json.RawMessage {
	return translateURIsMsg(msg, workspacePath, projectMounts, false)
}

// NewExtensionLSPManager creates a new ExtensionLSPManager.
// projectPaths is the single thing the LSP manager needs from the project table: the
// registered workspace paths, which bound which mount a language server may see.
type projectPaths interface {
	ListProjects() ([]types.Project, error)
}

func NewExtensionLSPManager(extStore *extensions.ExtensionStore, extSandbox *extensions.ExtensionSandboxManager, projects projectPaths) *ExtensionLSPManager {
	return &ExtensionLSPManager{
		extStore:    extStore,
		extSandbox:  extSandbox,
		projects:    projects,
		clients:     make(map[string]*sandboxClient),
		serverCh:    make(chan json.RawMessage, 256),
		subscribers: make([]chan json.RawMessage, 0),
	}
}

// SandboxMemory returns the live memory usage of the sandbox running the language server for
// the given workspace and file, resolved to a language through the extension registry. Zero
// stats (Running: false) when nothing is running there.
func (m *ExtensionLSPManager) SandboxMemory(workspacePath, filePath string) *types.SandboxMemoryStats {
	cfg := m.GetRegistry().Lookup(filePath)
	if cfg == nil {
		cfg = m.GetRegistry().Lookup(workspacePath)
	}
	if cfg == nil {
		return &types.SandboxMemoryStats{}
	}

	m.mu.Lock()
	sc, ok := m.clients[processKey(workspacePath, cfg.Language)]
	extensionID := ""
	if ok {
		extensionID = sc.extensionID
	}
	m.mu.Unlock()

	if !ok {
		return &types.SandboxMemoryStats{}
	}
	return m.extSandbox.GetMemoryStats(extensionID)
}

// Close retires every connection rather than emptying the map, so no `podman exec`, language
// server, or sandbox reference is left stranded.
func (m *ExtensionLSPManager) Close() {
	m.mu.Lock()
	pending := make(map[string]*sandboxClient, len(m.clients))
	for key, sc := range m.clients {
		delete(m.clients, key)
		pending[key] = sc
	}
	m.mu.Unlock()

	for key, sc := range pending {
		m.closeConnection(key, sc)
	}
}

func (m *ExtensionLSPManager) GetRegistry() *ExtensionRegistry {
	return &ExtensionRegistry{extStore: m.extStore}
}

// GetOrCreate returns the connection for one workspace and language and takes a reference for
// the caller; Release hands it back, and the connection dies with the last holder. ctx bounds
// creation, including the server start — see lspStartTimeout.
func (m *ExtensionLSPManager) GetOrCreate(ctx context.Context, workspacePath, language string, cfg *ServerConfig) (*LSPProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	workspaceKey := processKey(workspacePath, language)

	if m.liveClient(workspaceKey, true) != nil {
		return &LSPProcess{
			Workspace: workspacePath,
			Language:  language,
		}, nil
	}

	ext, err := m.lookupExtension(language)
	if err != nil {
		return nil, fmt.Errorf("no LSP extension for language %q: %w", language, err)
	}
	if _, err := m.startSession(ctx, ext, cfg, workspacePath, language, 1); err != nil {
		return nil, err
	}
	return &LSPProcess{
		Workspace: workspacePath,
		Language:  language,
	}, nil
}

// startSession gives one workspace its own language server on a dedicated bridge connection (a
// language server has one root, so two workspaces on one connection answers one of them about
// the other's project), plus the publish pump. A cold attach that loses the key joins the
// installed connection and closes the bridge it opened.
func (m *ExtensionLSPManager) startSession(ctx context.Context, ext *extensions.Extension, cfg *ServerConfig, workspacePath, language string, refs int) (*sandboxClient, error) {
	workspaceKey := processKey(workspacePath, language)
	lspConfig := ext.Manifest.Capabilities.GetLSP()
	if lspConfig == nil {
		return nil, fmt.Errorf("extension %s has no LSP config", ext.Name)
	}

	projectMounts := m.collectProjectMounts(workspacePath)
	if err := m.ensureSandbox(ext, workspacePath, projectMounts); err != nil {
		return nil, err
	}
	bridgeClient, err := m.extSandbox.StartSession(ext.ID)
	if err != nil {
		return nil, fmt.Errorf("start bridge session: %w", err)
	}
	// Until it is installed, this attempt owns a bridge it must close: otherwise its
	// `podman exec` and its language server are left with no holder that can reach them.
	adopted := false
	defer func() {
		if !adopted {
			m.extSandbox.ReleaseSession(ext.ID, bridgeClient)
		}
	}()

	client := newSandboxClient(bridgeClient, workspacePath, language, ext.ID, projectMounts)
	client.refs = refs

	startCtx, cancelStart := context.WithTimeout(ctx, lspStartTimeout)
	defer cancelStart()
	if err := bridgeClient.LSPStart(startCtx, lspConfig.Command, lspConfig.Args); err != nil {
		return nil, fmt.Errorf("start LSP server in sandbox: %w", err)
	}

	// Forward this session's notifications (e.g. publishDiagnostics), host paths back in.
	go func() {
		for notif := range bridgeClient.LSPNotificationCh() {
			translated := translateURIsToHost(notif, workspacePath, projectMounts)
			select {
			case m.serverCh <- translated:
			default:
				log.Printf("[lsp] server channel full, dropping notification")
			}
			m.broadcastToSubscribers(translated)
		}
		// The channel closes only when this connection's language server is gone, so the
		// connection is retired rather than left in the map looking alive. retire matches on
		// identity, so a losing racer whose own bridge closed is a no-op here.
		client.dead.Store(true)
		m.retire(workspaceKey, client)
	}()

	m.mu.Lock()
	if installed, exists := m.clients[workspaceKey]; exists && installed.usable() {
		// Join the winner, and hold it only if this caller can hand the reference back: a tool
		// call that never releases one would pin the connection and the sandbox forever.
		if refs > 0 {
			installed.refs++
		}
		m.mu.Unlock()
		log.Printf("[lsp] workspace=%s lang=%s: another attach won the race, joining its connection", workspacePath, language)
		return installed, nil
	}
	if client.dead.Load() {
		// It died between starting and installing; installing it would leave every later caller
		// with a connection that only answers `lsp server died`.
		m.mu.Unlock()
		return nil, fmt.Errorf("start LSP server in sandbox: the language server exited before it was put to work")
	}
	m.clients[workspaceKey] = client
	m.mu.Unlock()
	adopted = true

	log.Printf("[lsp] started extension %s (lang=%s) in sandbox for workspace %s", ext.Name, language, workspacePath)
	return client, nil
}

// liveClient returns the usable connection for workspaceKey, or nil; one that is no longer
// usable is retired on the spot, never handed out. hold takes the reference in the same
// critical section as the lookup, so a release cannot retire it in between.
func (m *ExtensionLSPManager) liveClient(workspaceKey string, hold bool) *sandboxClient {
	m.mu.Lock()
	sc := m.clients[workspaceKey]
	usable := sc.usable()
	if usable && hold {
		sc.refs++
	}
	m.mu.Unlock()
	if usable {
		return sc
	}
	if sc != nil {
		log.Printf("[lsp] workspace=%s: connection is dead, reconnecting", workspaceKey)
		m.retire(workspaceKey, sc)
	}
	return nil
}

// retire drops a connection that can no longer be used and hands back its sandbox reference.
// Every deletion goes through here — an entry removed without the hand-back leaves a container
// no holder can ever release — and a connection already gone is a no-op.
func (m *ExtensionLSPManager) retire(workspaceKey string, sc *sandboxClient) {
	if sc == nil {
		// Nothing to retire. The test below compares identity, so a nil would match the live
		// connection at this key, delete it, and hand back a reference we never held.
		return
	}
	m.mu.Lock()
	if m.clients[workspaceKey] != sc {
		m.mu.Unlock()
		return
	}
	delete(m.clients, workspaceKey)
	m.mu.Unlock()

	m.closeConnection(workspaceKey, sc)
}

// closeConnection ends one connection and hands back its sandbox reference without consulting
// the map, so it also works for an already-unregistered connection. See the orphan rule in
// ExtensionSandboxManager.ReleaseSession.
func (m *ExtensionLSPManager) closeConnection(workspaceKey string, sc *sandboxClient) {
	if m.extSandbox == nil || !m.extSandbox.ReleaseSession(sc.extensionID, sc.bridgeClient) {
		log.Printf("[lsp] retired dead connection for %s (its sandbox is already gone)", workspaceKey)
		return
	}
	log.Printf("[lsp] retired connection for %s and handed back sandbox %s", workspaceKey, sc.extensionID)
}

// Release hands back one holder's reference; the connection dies with the last one out, so no
// holder can take the server away from another.
func (m *ExtensionLSPManager) Release(workspacePath, language string) {
	workspaceKey := processKey(workspacePath, language)

	m.mu.Lock()
	sc, ok := m.clients[workspaceKey]
	if !ok {
		m.mu.Unlock()
		return
	}
	if sc.refs > 1 {
		sc.refs--
		m.mu.Unlock()
		log.Printf("[lsp] released one holder of workspace=%s lang=%s (%d still using it)", workspacePath, language, sc.refs)
		return
	}
	m.mu.Unlock()

	m.retire(workspaceKey, sc)
}

// ensureSandbox makes sure the extension's container exists with the mounts this workspace
// needs, recreating it when the mount set changed. It hands out no connection — consumers get
// their own from ExtensionSandboxManager.StartSession.
func (m *ExtensionLSPManager) ensureSandbox(ext *extensions.Extension, workspacePath string, projectMounts map[string]string) error {
	// Build the desired custom mounts (user's Sandbox Configuration + project
	// custom mounts adapted for the LSP sandbox) so we can check compatibility
	// with an existing sandbox.
	wsContainer := containerWorkspacePath(workspacePath, projectMounts)
	desiredMounts := resolveWorkspaceMounts(ext.SandboxConfig.Mounts, workspacePath, projectMounts)
	projectMountsRO := m.adaptProjectMounts(workspacePath, wsContainer)
	if len(projectMountsRO) > 0 {
		desiredMounts = append(desiredMounts, projectMountsRO...)
	}

	// Check if sandbox already exists and is alive
	if m.extSandbox.IsRunning(ext.ID) {
		// Existing sandbox — check if mounts are compatible
		if m.extSandbox.MountsCompatible(ext.ID, projectMounts, desiredMounts) {
			log.Printf("[lsp] reusing existing sandbox for extension %s", ext.ID)
			return nil
		}
		// Sandbox exists but mounts are stale — recreate
		log.Printf("[lsp] sandbox mounts changed, recreating for extension %s", ext.ID)
		_ = m.extSandbox.Stop(ext.ID)
	}

	// Resolve {workspace} in the user's Sandbox Configuration mounts, and adapt a node_modules
	// mount for the LSP sandbox if the project configures one, so the server can resolve
	// dependencies. Applied to a copy so the shared extension record isn't mutated.
	log.Printf("[lsp] creating sandbox for extension %s", ext.ID)
	extCopy := *ext
	extCopy.SandboxConfig.Mounts = desiredMounts
	if _, err := m.extSandbox.Create(&extCopy, projectMounts); err != nil {
		return fmt.Errorf("create extension sandbox: %w", err)
	}
	return nil
}

// SendToServer sends a JSON-RPC message to the server and waits for the response, translating
// file URIs host → container. The caller's deadline always wins over lspSendTimeout.
func (m *ExtensionLSPManager) SendToServer(ctx context.Context, process *LSPProcess, msg json.RawMessage) error {
	workspaceKey := processKey(process.Workspace, process.Language)

	sc := m.liveClient(workspaceKey, false)
	if sc == nil {
		// Stale or dead — liveClient has retired it, giving the sandbox reference back, and the
		// caller reconnects through GetOrCreate.
		return fmt.Errorf("sandbox client %w for workspace=%s lang=%s", errStaleClient, process.Workspace, process.Language)
	}

	// Translate file URIs from host path to container path
	translated := translateURIsToContainer(msg, sc.workspace, sc.projectMounts)

	sendCtx, cancel := context.WithTimeout(ctx, lspSendTimeout)
	defer cancel()

	response, err := sc.bridgeClient.LSPSend(sendCtx, translated)
	if err != nil {
		// Connection died — retire it so the next call reconnects with a fresh sandbox reference.
		m.retire(workspaceKey, sc)
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

	// Broadcast to subscribers (e.g., triggerLSPDiagnostics)
	m.broadcastToSubscribers(response)

	return nil
}

// LSPNotify sends a JSON-RPC notification without waiting for a response, translating file
// URIs host → container. See lspNotifyTimeout for the caller's deadline.
func (m *ExtensionLSPManager) LSPNotify(ctx context.Context, process *LSPProcess, msg json.RawMessage) error {
	workspaceKey := processKey(process.Workspace, process.Language)

	sc := m.liveClient(workspaceKey, false)
	if sc == nil {
		return fmt.Errorf("sandbox client %w for workspace=%s lang=%s", errStaleClient, process.Workspace, process.Language)
	}

	translated := translateURIsToContainer(msg, sc.workspace, sc.projectMounts)

	notifyCtx, cancel := context.WithTimeout(ctx, lspNotifyTimeout)
	defer cancel()

	if err := sc.bridgeClient.LSPNotify(notifyCtx, translated); err != nil {
		// Drop the connection so the next call reconnects, as in SendToServer.
		m.retire(workspaceKey, sc)
		// A stalled write means the bridge stopped reading. Closing it makes the next
		// GetOrCreate start a fresh bridge: extension sandboxes get new pipes.
		if errors.Is(err, sandbox.ErrWriteStalled) {
			log.Printf("[lsp] bridge write stalled for workspace=%s lang=%s, retiring client", process.Workspace, process.Language)
			_ = sc.bridgeClient.Close()
		}
		return fmt.Errorf("notify LSP server: %w", err)
	}
	return nil
}

// ServerMessageCh returns a channel that receives messages from LSP servers.
func (m *ExtensionLSPManager) ServerMessageCh() <-chan json.RawMessage {
	return m.serverCh
}

// Subscribe returns a channel that receives a copy of every server message; call Unsubscribe
// when done. It is bidirectional so Unsubscribe can close it.
func (m *ExtensionLSPManager) Subscribe() chan json.RawMessage {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	ch := make(chan json.RawMessage, 64)
	m.subscribers = append(m.subscribers, ch)
	return ch
}

// Unsubscribe removes a subscriber channel and closes it.
func (m *ExtensionLSPManager) Unsubscribe(ch chan json.RawMessage) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	for i, s := range m.subscribers {
		if s == ch {
			m.subscribers = append(m.subscribers[:i], m.subscribers[i+1:]...)
			close(ch)
			break
		}
	}
}

// broadcastToSubscribers sends a copy of the message to all subscribers.
func (m *ExtensionLSPManager) broadcastToSubscribers(msg json.RawMessage) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	for _, sub := range m.subscribers {
		select {
		case sub <- msg:
		default:
			// Drop if subscriber is too slow
		}
	}
}

// collectProjectMounts maps every registered project's host path to its container mount path
// (/mnt/<project-dirname>), so one extension sandbox can serve any project.
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

// findProjectForWorkspace returns the registered project that owns the given
// workspace path (worktree or main project directory).
func (m *ExtensionLSPManager) findProjectForWorkspace(workspacePath string) (*types.Project, error) {
	if m.projects == nil {
		return nil, fmt.Errorf("no project store")
	}
	projects, err := m.projects.ListProjects()
	if err != nil {
		return nil, err
	}
	absWorkspace, err := filepath.Abs(workspacePath)
	if err != nil {
		return nil, err
	}
	// Find the longest matching project path prefix.
	var bestPath string
	var bestProject *types.Project
	for i := range projects {
		absPath, err := filepath.Abs(projects[i].Path)
		if err != nil {
			continue
		}
		if strings.HasPrefix(absWorkspace, absPath) && (bestProject == nil || len(absPath) > len(bestPath)) {
			bestPath = absPath
			bestProject = &projects[i]
		}
	}
	if bestProject == nil {
		return nil, fmt.Errorf("no project found for workspace %s", workspacePath)
	}
	return bestProject, nil
}

// adaptProjectMounts rewrites the project's Sandbox Configuration mounts for the LSP sandbox:
// paths under the task sandbox project root (/home/sandbox/<project-name>) are remapped onto the
// LSP workspace path, global paths (e.g. /go) pass through, everything is forced read-only, and
// anything shadowing the server's own writable state is dropped (see shadowsToolState).
func (m *ExtensionLSPManager) adaptProjectMounts(workspacePath, wsContainerPath string) []types.SandboxMount {
	project, err := m.findProjectForWorkspace(workspacePath)
	if err != nil {
		log.Printf("[lsp] adaptProjectMounts: %v", err)
		return nil
	}
	if len(project.SandboxMounts) == 0 {
		return nil
	}
	taskRoot := kanban_sandbox.MountPath(project.Name)
	var adapted []types.SandboxMount
	for _, mo := range project.SandboxMounts {
		cp := mo.ContainerPath
		// If the mount lives under the task sandbox project root, remap it
		// relative to the LSP workspace container path.
		if strings.HasPrefix(cp, taskRoot+"/") {
			rel := strings.TrimPrefix(cp, taskRoot+"/")
			cp = filepath.Join(wsContainerPath, rel)
		}
		if shadowsToolState(cp) {
			log.Printf("[lsp] not borrowing project mount %s -> %s: the language server needs to write there",
				mo.HostPath, cp)
			continue
		}
		lspMount := types.SandboxMount{
			HostPath:      mo.HostPath,
			ContainerPath: cp,
			Mode:          "ro",
		}
		log.Printf("[lsp] adapted project mount: %s -> %s (ro)", lspMount.HostPath, lspMount.ContainerPath)
		adapted = append(adapted, lspMount)
	}
	return adapted
}

// LSPRequest makes a synchronous JSON-RPC request to the server that handles the given file
// and returns the raw JSON response. ctx bounds every step: a stopped task must not keep
// talking to the server.
func (m *ExtensionLSPManager) LSPRequest(ctx context.Context, workspacePath, filePath, method string, params any) (json.RawMessage, error) {
	ext, err := m.lookupExtensionByFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("no LSP extension for file %q: %w", filePath, err)
	}
	lspCfg := ext.Manifest.Capabilities.GetLSP()
	if lspCfg == nil {
		return nil, fmt.Errorf("extension %s has no LSP config", ext.Name)
	}
	language := lspCfg.Language

	// One ServerConfig for the whole call: it is the same shape the diagnostics pass gets from
	// the registry, so the handshake and the process key cannot disagree about which server a
	// file belongs to — including for a manifest that leaves `language` empty.
	cfg := m.GetRegistry().toServerConfig(ext)
	if cfg == nil {
		return nil, fmt.Errorf("no server config for language %q", language)
	}

	// Attach to this workspace's connection, starting one if nothing holds it yet. A tool call
	// takes no reference: it has no lifetime to release one at, and holding one would keep every
	// workspace's server alive forever.
	workspaceKey := processKey(workspacePath, language)
	sc := m.liveClient(workspaceKey, false)
	if sc == nil {
		var err error
		sc, err = m.startSession(ctx, ext, cfg, workspacePath, language, 0)
		if err != nil {
			return nil, fmt.Errorf("create sandbox: %w", err)
		}
	}

	// The handshake belongs to the connection, not to the request: one per connection,
	// because it costs a second of server time (measured: gopls answers initialize 1.06s
	// after a cold start) and a second one teaches the server nothing it did not already know.
	if err := m.EnsureInitialized(ctx, workspacePath, cfg); err != nil {
		return nil, err
	}

	// Extract the file URI from params and ensure the server's copy is in sync
	// with the on-disk content (didOpen once, then didChange when the file
	// changes on disk — servers like tsserver do not watch the filesystem).
	if paramsMap, ok := params.(map[string]any); ok {
		if td, ok := paramsMap["textDocument"].(map[string]any); ok {
			if uri, ok := td["uri"].(string); ok {
				if _, err := m.ensureDocSync(ctx, sc, uri, workspacePath, filePath, cfg); err != nil {
					log.Printf("[lsp] doc sync failed for %s: %v", filePath, err)
					// Continue anyway — the server may still have the file
				}
			}
		}
	}

	// Build the JSON-RPC request
	reqID := int64(time.Now().UnixNano())
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      reqID,
		"method":  method,
		"params":  params,
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Translate URIs and send
	translated := translateURIsToContainer(reqBytes, sc.workspace, sc.projectMounts)
	reqCtx, cancel := context.WithTimeout(ctx, lspRequestTimeout)
	defer cancel()

	response, err := sc.bridgeClient.LSPSend(reqCtx, translated)
	if err != nil {
		// The connection is unusable, and it is retired rather than forgotten: dropping the map
		// entry alone would leave the sandbox reference it took with no holder left to hand it
		// back, pinning the container open past every task that ever used it, and would tear the
		// server down for any other holder without closing the bridge underneath them.
		m.retire(workspaceKey, sc)
		return nil, fmt.Errorf("LSP request failed: %w", err)
	}
	if len(response) == 0 {
		return nil, fmt.Errorf("empty LSP response")
	}

	// Translate URIs back to host paths
	return translateURIsToHost(response, sc.workspace, sc.projectMounts), nil
}

// EnsureInitialized performs the initialize handshake on an existing connection, at most once:
// the flag lives on the connection, which is what every holder shares and what costs real work
// to initialize.
//
// The caller hands in the ServerConfig it resolved, not a language string: `language` is
// optional in a manifest, while the config names the connection and carries the init-params.
func (m *ExtensionLSPManager) EnsureInitialized(ctx context.Context, workspacePath string, cfg *ServerConfig) error {
	if cfg == nil {
		return fmt.Errorf("no server config for workspace=%s", workspacePath)
	}
	language := cfg.Language
	sc := m.liveClient(processKey(workspacePath, language), false)
	if sc == nil {
		return fmt.Errorf("no live LSP connection for workspace=%s lang=%s", workspacePath, language)
	}

	sc.initMu.Lock()
	defer sc.initMu.Unlock()
	if sc.initialized {
		return nil
	}
	if err := m.lspInitialize(ctx, sc, cfg); err != nil {
		return fmt.Errorf("LSP initialize failed: %w", err)
	}
	sc.initialized = true
	return nil
}

// SyncDocument brings the server's copy of a document in line with the file on disk — didOpen,
// didChange, or nothing — because tsserver and gopls do not watch the filesystem and the agent
// edits behind their backs.
//
// It returns the URI the server was told about (the string publishDiagnostics will name) and
// whether anything was sent, which is how a caller knows no notification is coming.
func (m *ExtensionLSPManager) SyncDocument(ctx context.Context, workspacePath, filePath string, cfg *ServerConfig) (string, bool, error) {
	if cfg == nil {
		return "", false, fmt.Errorf("no server config for workspace=%s", workspacePath)
	}
	language := cfg.Language
	sc := m.liveClient(processKey(workspacePath, language), false)
	if sc == nil {
		return "", false, fmt.Errorf("no LSP connection for workspace=%s lang=%s", workspacePath, language)
	}

	uri := "file://" + filepath.Join(workspacePath, filePath)
	synced, err := m.ensureDocSync(ctx, sc, uri, workspacePath, filePath, cfg)
	return uri, synced, err
}

// lspInitialize sends an initialize request to the LSP server and waits for the response.
func (m *ExtensionLSPManager) lspInitialize(ctx context.Context, sc *sandboxClient, cfg *ServerConfig) error {
	absWorkspace, _ := filepath.Abs(sc.workspace)
	initParams := map[string]any{
		"rootUri": "file://" + absWorkspace,
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"publishDiagnostics": map[string]any{
					"relatedInformation": true,
				},
				"hover": map[string]any{
					"contentFormat": []string{"markdown", "plaintext"},
				},
			},
			"workspace": map[string]any{
				"workspaceFolders": true,
			},
		},
		"workspaceFolders": []map[string]any{
			{"uri": "file://" + absWorkspace, "name": filepath.Base(absWorkspace)},
		},
	}
	// Merge init params from extension config
	for k, v := range cfg.InitParamsOverride {
		initParams[k] = v
	}

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  initParams,
	}
	reqBytes, _ := json.Marshal(req)
	translated := translateURIsToContainer(reqBytes, sc.workspace, sc.projectMounts)

	ctxTimeout, cancel := context.WithTimeout(ctx, lspInitReplyTimeout)
	defer cancel()
	response, err := sc.bridgeClient.LSPSend(ctxTimeout, translated)
	if err != nil {
		return err
	}

	// LSPSend only reports transport failure. A refusal of these parameters arrives as an ordinary
	// payload holding a JSON-RPC error, which would otherwise read as a completed handshake.
	var reply struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if len(response) > 0 && json.Unmarshal(response, &reply) == nil && reply.Error != nil {
		return fmt.Errorf("initialize rejected by %s (code %d): %s", cfg.Command, reply.Error.Code, reply.Error.Message)
	}

	// Send "initialized" notification
	notify := map[string]any{
		"jsonrpc": "2.0",
		"method":  "initialized",
		"params":  map[string]any{},
	}
	notifyBytes, _ := json.Marshal(notify)
	_ = sc.bridgeClient.LSPNotify(ctx, notifyBytes)

	return nil
}

// docSyncAction describes what ensureDocSync must send to keep the server's
// document copy in sync with the on-disk file.
type docSyncAction struct {
	kind    string // "" (no sync), "open" (didOpen), or "change" (didChange)
	version int    // LSP document version to send (for open/change)
}

// decideDocSync is the pure sync decision: didOpen when the server has never seen the file,
// didChange when the on-disk content diverged, nothing otherwise.
func decideDocSync(opened bool, lastSent, onDisk string, lastVersion int) docSyncAction {
	if !opened {
		return docSyncAction{kind: "open", version: 1}
	}
	if lastSent != onDisk {
		return docSyncAction{kind: "change", version: lastVersion + 1}
	}
	return docSyncAction{}
}

// ensureDocSync sends didOpen or didChange so the server never works from stale content, and
// reports whether it sent anything: a server that already holds this exact text will not
// republish it, so a caller waiting for a notification needs to know none is coming.
func (m *ExtensionLSPManager) ensureDocSync(ctx context.Context, sc *sandboxClient, uri, workspacePath, filePath string, cfg *ServerConfig) (bool, error) {
	absPath := filepath.Join(workspacePath, filePath)
	content, err := os.ReadFile(absPath)
	if err != nil {
		return false, fmt.Errorf("read file for doc sync: %w", err)
	}

	action := decideDocSync(sc.openedFiles[uri], sc.openedFileContents[uri], string(content), sc.openedFileVersions[uri])
	switch action.kind {
	case "open":
		// The extension's per-file languageId, never the routing language.
		if err := m.lspDidOpen(ctx, sc, uri, cfg.LanguageIDFor(filePath), string(content), action.version); err != nil {
			return false, err
		}
	case "change":
		if err := m.lspDidChange(ctx, sc, uri, string(content), action.version); err != nil {
			return false, err
		}
	default:
		return false, nil
	}
	sc.openedFiles[uri] = true
	sc.openedFileContents[uri] = string(content)
	sc.openedFileVersions[uri] = action.version
	return true, nil
}

// lspDidOpen sends a textDocument/didOpen notification. The declared languageId matters for
// tsserver, which turns it into a ScriptKind whose first answer sticks; other servers take the
// language from the file name.
func (m *ExtensionLSPManager) lspDidOpen(ctx context.Context, sc *sandboxClient, uri, language, content string, version int) error {
	notify := map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/didOpen",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri":        uri,
				"languageId": language,
				"version":    version,
				"text":       content,
			},
		},
	}
	return m.lspNotify(ctx, sc, notify)
}

// lspDidChange sends a textDocument/didChange notification with a full-text
// sync (no range) to the LSP server. It carries no languageId: the document's language was
// fixed when it was opened, which is exactly why didOpen has to get it right.
func (m *ExtensionLSPManager) lspDidChange(ctx context.Context, sc *sandboxClient, uri, content string, version int) error {
	notify := map[string]any{
		"jsonrpc": "2.0",
		"method":  "textDocument/didChange",
		"params": map[string]any{
			"textDocument": map[string]any{
				"uri":     uri,
				"version": version,
			},
			"contentChanges": []map[string]any{{"text": content}},
		},
	}
	return m.lspNotify(ctx, sc, notify)
}

// lspNotify marshals a JSON-RPC notification, translates its URIs to
// container paths, and sends it to the LSP server via the bridge.
func (m *ExtensionLSPManager) lspNotify(ctx context.Context, sc *sandboxClient, notify map[string]any) error {
	notifyBytes, _ := json.Marshal(notify)
	translated := translateURIsToContainer(notifyBytes, sc.workspace, sc.projectMounts)
	return sc.bridgeClient.LSPNotify(ctx, translated)
}

// lookupExtensionByFile finds the matching LSP extension for a given file path.
func (m *ExtensionLSPManager) lookupExtensionByFile(filePath string) (*extensions.Extension, error) {
	extensions, err := m.extStore.GetByType(extensions.ExtensionTypeLSP)
	if err != nil {
		return nil, err
	}

	for _, extItem := range extensions {
		if !extItem.Enabled {
			continue
		}
		lspCfg := extItem.Manifest.Capabilities.GetLSP()
		if lspCfg == nil {
			continue
		}
		// One matcher for every caller: the extension's own file-type declaration decides, and
		// it also answers the case of an extensionless file named after its language.
		if lspCfg.MatchesFile(filePath) {
			return &extItem, nil
		}
	}

	return nil, fmt.Errorf("no enabled LSP extension found for file %q", filePath)
}

// IsToolCallsEnabledForFile reports whether the LSP extension handling the given file has tool
// calls enabled; false when no extension handles it.
func (m *ExtensionLSPManager) IsToolCallsEnabledForFile(filePath string) bool {
	ext, err := m.lookupExtensionByFile(filePath)
	if err != nil {
		return false
	}
	lspCfg := ext.Manifest.Capabilities.GetLSP()
	if lspCfg == nil {
		return false
	}
	return lspCfg.IsToolCallsEnabled()
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
		// A caller may name a file type instead ("ts", "javascriptreact"); both are answers the
		// extension declared, not strings this function knows.
		for _, ft := range lspCfg.FileTypes {
			if ft.LanguageID == language || ft.Extension == language {
				return &ext, nil
			}
		}
	}

	return nil, fmt.Errorf("no enabled LSP extension found for language %q", language)
}

// ExtensionRegistry provides language server configuration from installed extensions.
// It implements the lspRegistry interface used by the LSP handler.
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

	for _, extItem := range extensions {
		if !extItem.Enabled {
			continue
		}
		lspCfg := extItem.Manifest.Capabilities.GetLSP()
		if lspCfg == nil {
			continue
		}
		if lspCfg.MatchesFile(fileOrPath) {
			return r.toServerConfig(&extItem)
		}
		// A workspace path (no suffix) still resolves by its directory name, as when the name
		// names the language — the declared language, not a table here.
		if lspCfg.Language != "" && strings.EqualFold(filepath.Base(fileOrPath), lspCfg.Language) {
			return r.toServerConfig(&extItem)
		}
	}

	return nil
}

// HasAnyLSPToolCallsExtension reports whether any enabled LSP extension has tool calls
// enabled, which decides whether the agent gets LSP tools at all.
func (r *ExtensionRegistry) HasAnyLSPToolCallsExtension() bool {
	extensions, err := r.extStore.GetByType(extensions.ExtensionTypeLSP)
	if err != nil {
		return false
	}
	for _, ext := range extensions {
		if ext.Enabled && ext.Manifest.Capabilities.GetLSP() != nil && ext.Manifest.Capabilities.LSP.IsToolCallsEnabled() {
			return true
		}
	}
	return false
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
func (r *ExtensionRegistry) toServerConfig(ext *extensions.Extension) *ServerConfig {
	lspCfg := ext.Manifest.Capabilities.GetLSP()
	if lspCfg == nil {
		return nil
	}

	return &ServerConfig{
		Language:           lspCfg.Language,
		Command:            lspCfg.Command,
		Args:               lspCfg.Args,
		FileTypes:          lspCfg.FileTypes,
		InitParamsOverride: lspCfg.InitParamsOverride,
	}
}
