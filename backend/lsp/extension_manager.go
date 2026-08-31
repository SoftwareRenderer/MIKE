package lsp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"kanban/backend/extensions"
	kanban_sandbox "kanban/backend/sandbox"
	"kanban/backend/store"
	"kanban/backend/types"
	sandbox "kanban/pkg/bridge"
	"kanban/pkg/bridge/protocol"
)

// ServerConfig holds the configuration for a single language server.
type ServerConfig struct {
	Command            string         // binary to execute (e.g., "gopls")
	Args               []string       // args template, {workspace} replaced with workspace path
	Extensions         []string       // file extensions (without dot), e.g., []string{"go"}
	Language           string         // language identifier, e.g., "go", "typescript"
	InitParamsOverride map[string]any // merged into initialize params sent to server
}

// lspStartTimeout bounds `lsp_start`. The bridge answers as soon as it has spawned the
// server, so this only ever waits on a bridge that stopped answering.
const lspStartTimeout = 30 * time.Second

// ExtensionLSPManager manages LSP processes through extension sandboxes.
// It implements the lspManager interface and uses the extension system
// to discover available language servers and run them in sandboxes.
type ExtensionLSPManager struct {
	extStore   *extensions.ExtensionStore
	extSandbox *extensions.ExtensionSandboxManager
	projects   *store.Store
	mu         sync.Mutex
	clients    map[string]*sandboxClient // workspaceKey → client state
	// pumps tracks the one notification pump per extension sandbox. The bridge
	// connection is shared by every workspace, so pumping it from each client's
	// goroutine would split notifications between unrelated consumers.
	pumps map[string]bool // extensionID → pump running
	// serverCh receives messages from all LSP servers (responses + notifications)
	serverCh chan json.RawMessage
	// subscribers receive a copy of all LSP server messages (for filtered listening)
	subscribers []chan json.RawMessage
	subMu       sync.Mutex
	// prober runs an extension's detection probe in the workspace's own sandbox.
	// Nil until wired in main.go; without it, detection is skipped.
	prober TaskProber
	// detections caches each probe's outcome per (project, extension) so a
	// workspace pays for detection once, not once per turn.
	detections map[string]detection
	// environments records whether each workspace's language server can actually
	// resolve the project, and when that was last established.
	environments map[string]envStatus
}

// sandboxClient tracks the state of a single LSP connection in a sandbox.
type sandboxClient struct {
	bridgeClient *sandbox.Client
	workspace    string // host workspace path
	language     string
	extensionID  string
	// extension is the installed record this client was created from. Its manifest
	// carries the probe declarations and the diagnostic codes that mean "could not
	// load the project" — core never interprets either, it only forwards them.
	extension *extensions.Extension
	// Project-based mount mapping for URI translation.
	// Each registered project is mounted at /mnt/<project-dirname>.
	// projectMounts maps host project path → container mount path.
	projectMounts map[string]string
	// initialized tracks whether the LSP server has been initialized.
	initialized bool
	// initResponse is the initialize *response* this session answered with. A
	// session is shared now, so a second consumer (a browser channel opened after
	// a turn already initialized the server) would otherwise send its own
	// initialize and be answered with a protocol error — replaying the first
	// answer is what both the browser and the server expect.
	initResponse json.RawMessage
	// session addresses this workspace's language-server process inside the
	// shared sandbox. The container is shared by every project; the process, its
	// environment and its notifications are not.
	session string
	// openedFiles tracks file URIs that have been sent via didOpen.
	openedFiles map[string]bool
	// openedFileContents tracks the last content sent to the server per URI,
	// so on-disk changes (agent edits between LSP calls) can be re-synced via
	// didChange. Servers like tsserver do not watch the filesystem.
	openedFileContents map[string]string
	// openedFileVersions tracks the last LSP document version sent per URI.
	openedFileVersions map[string]int
	// sessionEnvs is what detection resolved for this workspace — the assignments
	// its language-server process was started with. They belong to this session
	// alone: the shared container never sees them, so one project's dependency
	// cache can neither leak into another's server nor restart it.
	sessionEnvs []string
	// refs counts the consumers of this session — the editor's connection, a
	// diagnostics pass, an agent tool call. The session is stopped when the last
	// one leaves, never when the first one does: a diagnostics pass that tore the
	// process down on exit would leave the editor talking to a dead server.
	refs int
	// gate is this extension's environment check, when it declared one.
	gate *extensions.EnvironmentGate
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
func NewExtensionLSPManager(extStore *extensions.ExtensionStore, extSandbox *extensions.ExtensionSandboxManager, projects *store.Store) *ExtensionLSPManager {
	return &ExtensionLSPManager{
		extStore:     extStore,
		extSandbox:   extSandbox,
		projects:     projects,
		clients:      make(map[string]*sandboxClient),
		pumps:        make(map[string]bool),
		serverCh:     make(chan json.RawMessage, 256),
		subscribers:  make([]chan json.RawMessage, 0),
		detections:   make(map[string]detection),
		environments: make(map[string]envStatus),
	}
}

// sessionFor derives the session id of a workspace's language server. Stable per
// absolute path so a reconnect re-attaches to the same session name, and hashed
// because the id is embedded in a protocol payload, not a filesystem path.
func sessionFor(workspacePath string) string {
	abs, err := filepath.Abs(workspacePath)
	if err != nil {
		abs = workspacePath
	}
	sum := sha256.Sum256([]byte(abs))
	return "ws-" + hex.EncodeToString(sum[:])[:16]
}

// ensureNotificationPump starts the single reader of a sandbox's notification
// channel, if one is not already running. Notifications are attributed to their
// session and URI-translated with the *owning* workspace's mount map — doing it
// any other way serves one project's diagnostics to another.
func (m *ExtensionLSPManager) ensureNotificationPump(bridgeClient *sandbox.Client, extensionID string) {
	m.mu.Lock()
	running := m.pumps[extensionID]
	if !running {
		m.pumps[extensionID] = true
	}
	m.mu.Unlock()
	if running {
		return
	}

	go func() {
		for notif := range bridgeClient.LSPNotificationCh() {
			m.routeNotification(bridgeClient, extensionID, notif)
		}
		m.mu.Lock()
		delete(m.pumps, extensionID)
		m.mu.Unlock()
	}()
}

// routeNotification delivers one notification to the workspace that owns it.
// An unattributed notification (a bridge older than sessions) is delivered only
// when exactly one session of this sandbox could have produced it; anything else
// is dropped, because a misattributed diagnostic is worse than a missing one.
func (m *ExtensionLSPManager) routeNotification(bridgeClient *sandbox.Client, extensionID string, notif protocol.LSPNotification) {
	m.mu.Lock()
	var owner *sandboxClient
	candidates := 0
	for _, sc := range m.clients {
		if sc.extensionID != extensionID || sc.bridgeClient != bridgeClient {
			continue
		}
		if notif.Session == "" {
			candidates++
			owner = sc
		} else if sc.session == notif.Session {
			owner = sc
			candidates = 1
			break
		}
	}
	m.mu.Unlock()

	if owner == nil || candidates != 1 {
		log.Printf("[lsp] dropped notification: session %q has no unique owner (ext=%s)", notif.Session, extensionID)
		return
	}

	translated := translateURIsToHost(notif.Raw, owner.workspace, owner.projectMounts)
	select {
	case m.serverCh <- translated:
	default:
		log.Printf("[lsp] server channel full, dropping notification")
	}
	m.broadcastToSubscribers(translated)
}

// SessionInitialized reports whether this workspace's language-server process
// has already completed the initialize handshake.
//
// The handshake is per-process, not per-client: the executor's diagnostics pass
// and the editor's connection drive the same session, and a second `initialize`
// on a running server is a protocol error the server answers by refusing — after
// which every request that follows is answered by a server that was never told
// what workspace it is looking at.
func (m *ExtensionLSPManager) SessionInitialized(workspacePath, language string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	sc, ok := m.clients[processKey(workspacePath, language)]
	return ok && sc.initialized
}

// MarkSessionInitialized records that a caller completed the handshake, so the
// next consumer of the same session does not repeat it.
func (m *ExtensionLSPManager) MarkSessionInitialized(workspacePath, language string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sc, ok := m.clients[processKey(workspacePath, language)]; ok {
		sc.initialized = true
	}
}

// reloadNeeded reports whether a changed file means the language server has to
// re-read the project. A declared entry matches itself (`go.mod`) or any file of
// that name below the workspace (`internal/foo/go.mod`), because a nested module
// changing is exactly the same event for the server — and because a server
// watching nothing, it only learns what it is told.
func reloadNeeded(reloadFiles []string, changed string) bool {
	if len(reloadFiles) == 0 {
		return false
	}
	changed = filepath.Clean(strings.TrimSpace(changed))
	if changed == "" || changed == "." {
		return false
	}
	base := filepath.Base(changed)
	for _, declared := range reloadFiles {
		declared = filepath.Clean(strings.TrimSpace(declared))
		if declared == "." || declared == "" {
			continue
		}
		if changed == declared || base == filepath.Base(declared) {
			return true
		}
	}
	return false
}

// FilesChanged recycles the language-server session of any workspace the changed
// files belong to whose extension declared one of those files as a reload
// trigger. Returns true when at least one session was restarted.
//
// Recycling means: stop the process, start it again with the same session id and
// the same detected environment, and re-initialize it. Detection is not repeated
// — the answer is cached and the mount set has not changed.
func (m *ExtensionLSPManager) FilesChanged(workspacePath string, changed []string) bool {
	if len(changed) == 0 {
		return false
	}
	m.mu.Lock()
	var targets []*sandboxClient
	for key, sc := range m.clients {
		if sc.workspace != workspacePath || sc.extension == nil {
			continue
		}
		_ = key
		for _, f := range changed {
			if reloadNeeded(sc.extension.SandboxConfig.ReloadFiles, f) {
				targets = append(targets, sc)
				break
			}
		}
	}
	m.mu.Unlock()

	recycled := false
	for _, sc := range targets {
		if err := m.recycleSession(sc); err != nil {
			log.Printf("[lsp] recycle session for %s failed: %v", sc.workspace, err)
			continue
		}
		recycled = true
	}
	return recycled
}

// recycleSession restarts one session in place: same id, same environment, fresh
// process. Siblings in the same sandbox are untouched — that isolation is the
// reason sessions exist instead of one server per container.
func (m *ExtensionLSPManager) recycleSession(sc *sandboxClient) error {
	lspCfg := sc.extension.Manifest.Capabilities.GetLSP()
	if lspCfg == nil || sc.bridgeClient == nil || !sc.bridgeClient.IsAlive() {
		return fmt.Errorf("no live server to recycle")
	}
	wasInitialized := sc.initialized
	ctx, cancel := context.WithTimeout(context.Background(), lspStartTimeout)
	defer cancel()
	if err := sc.bridgeClient.LSPStop(ctx, sc.session); err != nil {
		log.Printf("[lsp] stop before recycle failed: %v", err)
	}
	// Fresh process: forget the documents it was told about and whether it was
	// initialized, so the next request re-sends both.
	sc.initialized = false
	sc.openedFiles = make(map[string]bool)
	sc.openedFileContents = make(map[string]string)
	sc.openedFileVersions = make(map[string]int)
	if err := sc.bridgeClient.LSPStart(ctx, sc.session, lspCfg.Command, lspCfg.Args, sc.sessionEnvs); err != nil {
		return fmt.Errorf("restart session: %w", err)
	}
	log.Printf("[lsp] recycled session for workspace=%s lang=%s (re-reads the project)", sc.workspace, sc.language)
	if wasInitialized {
		if err := m.lspInitialize(ctx, sc, sc.extension); err != nil {
			return fmt.Errorf("re-initialize after recycle: %w", err)
		}
		sc.initialized = true
	}
	return nil
}

// SandboxMemory returns the live memory usage of the sandbox running the
// language server for the given workspace and file. The file is resolved to
// a language via the extension registry (same lookup order as
// handleSubscription). Returns zero stats (Running: false) when no extension
// handles the file or there is no LSP client for the workspace/language pair.
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
// ctx bounds creation, including `lsp_start` — see lspStartTimeout.
func (m *ExtensionLSPManager) GetOrCreate(ctx context.Context, workspacePath, language string, cfg *ServerConfig) (*LSPProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	workspaceKey := processKey(workspacePath, language)

	// Holders of a previous (dead) client for this workspace still intend to call
	// Release exactly once each, so the fresh session inherits their count.
	inherited := 0
	m.mu.Lock()
	sc, ok := m.clients[workspaceKey]
	if ok {
		// Check if the sandbox bridge is still alive (e.g., after a rebuild)
		if sc.bridgeClient != nil && sc.bridgeClient.IsAlive() {
			sc.refs++
			m.mu.Unlock()
			return &LSPProcess{
				Workspace: workspacePath,
				Language:  language,
			}, nil
		}
		// Stale entry — remove and recreate
		inherited = sc.refs
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
	bridgeClient, containerMounts, err := m.getOrCreateSandbox(ext, workspacePath, projectMounts)
	if err != nil {
		return nil, err
	}

	lspConfig := ext.Manifest.Capabilities.GetLSP()
	if lspConfig == nil {
		return nil, fmt.Errorf("extension %s has no LSP config", ext.Name)
	}
	session := sessionFor(workspacePath)

	client := &sandboxClient{
		bridgeClient:  bridgeClient,
		workspace:     workspacePath,
		language:      language,
		extensionID:   ext.ID,
		extension:     ext,
		projectMounts: projectMounts,
		session:       session,
		gate:          ext.SandboxConfig.Environment,
		// Allocated here as well as in LSPRequest: a workspace can be reached by
		// either path first, and document sync writes these maps.
		openedFiles:        make(map[string]bool),
		openedFileContents: make(map[string]string),
		openedFileVersions: make(map[string]int),
		refs:               inherited + 1,
	}

	// Registered before the server starts: notifications are attributed through
	// m.clients, so a server that publishes diagnostics immediately would
	// otherwise find no owner and be dropped.
	m.mu.Lock()
	m.clients[workspaceKey] = client
	m.mu.Unlock()

	// One attributed notification pump per sandbox — never one per client, or two
	// consumers would split the same connection's notifications between them.
	m.ensureNotificationPump(bridgeClient, ext.ID)

	if err := m.startSession(ctx, client, lspConfig, containerMounts); err != nil {
		m.mu.Lock()
		if m.clients[workspaceKey] == client {
			delete(m.clients, workspaceKey)
		}
		m.mu.Unlock()
		return nil, fmt.Errorf("start LSP server in sandbox: %w", err)
	}

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
	if sc.refs > 1 {
		// Still in use by another consumer (the editor, a tool call, a later file in
		// the same pass). Dropping the session here would evict a live server out
		// from under them, and they would keep sending their requests to it.
		sc.refs--
		m.mu.Unlock()
		return
	}
	delete(m.clients, workspaceKey)
	m.mu.Unlock()

	// Stop this workspace's server, not the sandbox: the container is shared, and
	// tearing it down would evict every other project's language server. Without
	// this the process would outlive its client.
	if sc.bridgeClient != nil && sc.bridgeClient.IsAlive() && sc.session != "" {
		ctx, cancel := context.WithTimeout(context.Background(), lspStartTimeout)
		if err := sc.bridgeClient.LSPStop(ctx, sc.session); err != nil {
			log.Printf("[lsp] stop session %s failed: %v", sc.session, err)
		}
		cancel()
	}

	m.extSandbox.Release(sc.extensionID)

	log.Printf("[lsp] released extension sandbox for workspace=%s lang=%s", workspacePath, language)
}

// getOrCreateSandbox gets or creates an extension sandbox.
// Returns the bridge client and the custom mount set the container was created
// with — that set is the whole filesystem the language server can see, so it is
// also the boundary a detected dependency path has to fall inside.
func (m *ExtensionLSPManager) getOrCreateSandbox(ext *extensions.Extension, workspacePath string, projectMounts map[string]string) (*sandbox.Client, []types.SandboxMount, error) {
	// Build the desired custom mounts (user's Sandbox Configuration + every
	// project's own mounts, adapted for the LSP sandbox) so we can check
	// compatibility with an existing sandbox.
	desiredMounts := resolveWorkspaceMounts(ext.SandboxConfig.Mounts, workspacePath, projectMounts)
	projectMountsRO := m.adaptProjectMounts(workspacePath, projectMounts)
	if len(projectMountsRO) > 0 {
		desiredMounts = append(desiredMounts, projectMountsRO...)
	}

	// Check if sandbox already exists and is alive
	bridgeClient, err := m.extSandbox.GetOrCreate(ext.ID)
	if err == nil && bridgeClient != nil && bridgeClient.IsAlive() {
		// Existing sandbox — does it still match what we would create? Only the
		// extension's own envs participate: a value detected for one workspace is
		// that workspace's session env, never a reason to restart the shared container.
		compatible, reason := m.extSandbox.MountsCompatible(ext.ID, projectMounts, desiredMounts, ext.SandboxConfig.Envs)
		if compatible {
			log.Printf("[lsp] reusing existing sandbox for extension %s", ext.ID)
			return bridgeClient, desiredMounts, nil
		}
		// Sandbox exists but its create-time inputs are stale — recreate
		log.Printf("[lsp] sandbox %s, recreating for extension %s", reason, ext.ID)
		_ = m.extSandbox.Stop(ext.ID)
	}

	// Create a new sandbox. Resolve {workspace} in the user's Sandbox Configuration
	// mounts and examine the project's Sandbox Configuration for a node_modules
	// mount — if found, adapt it for the LSP sandbox so the language server can
	// resolve its dependencies. Applied to a copy so the shared extension record
	// isn't mutated.
	log.Printf("[lsp] creating sandbox for extension %s", ext.ID)
	extCopy := *ext
	extCopy.SandboxConfig.Mounts = desiredMounts
	_, err = m.extSandbox.Create(&extCopy, projectMounts)
	if err != nil {
		return nil, nil, fmt.Errorf("create extension sandbox: %w", err)
	}
	bridgeClient, err = m.extSandbox.Start(ext.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("start extension sandbox: %w", err)
	}
	return bridgeClient, desiredMounts, nil
}

// startSession starts this workspace's language-server session inside the shared
// sandbox, with the environment detection resolved for *this* workspace.
//
// The assignments are per-session by design: the container is shared by every
// project, so a detected dependency root that one workspace uses must not become
// a container-wide env — that would make project A's cache project B's truth.
func (m *ExtensionLSPManager) startSession(ctx context.Context, sc *sandboxClient, lspCfg *extensions.LSPConfig, containerMounts []types.SandboxMount) error {
	sc.sessionEnvs = m.detectSessionEnvs(ctx, sc.extension, sc.workspace, containerMounts)
	startCtx, cancel := context.WithTimeout(ctx, lspStartTimeout)
	defer cancel()
	return sc.bridgeClient.LSPStart(startCtx, sc.session, lspCfg.Command, lspCfg.Args, sc.sessionEnvs)
}

// isInitializeRequest reports whether a raw JSON-RPC message is an `initialize`
// request — not the `initialized` notification, which is a different message and
// is safe to send more than once.
func isInitializeRequest(msg json.RawMessage) bool {
	var probe struct {
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(msg, &probe); err != nil {
		return false
	}
	return probe.Method == "initialize" && len(probe.ID) > 0 && string(probe.ID) != "null"
}

// replayInitializeResponse re-addresses a cached initialize response to the id of
// the request asking now, so the handler routes it to *this* client. Without the
// rewrite the answer carries the first requester's id and is delivered to it.
func replayInitializeResponse(cached, request json.RawMessage) json.RawMessage {
	var req struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(request, &req); err != nil || len(req.ID) == 0 {
		return nil
	}
	var resp map[string]any
	if err := json.Unmarshal(cached, &resp); err != nil || resp == nil {
		return nil
	}
	var id any
	if err := json.Unmarshal(req.ID, &id); err != nil {
		return nil
	}
	resp["id"] = id
	out, err := json.Marshal(resp)
	if err != nil {
		return nil
	}
	return out
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
	isInit := isInitializeRequest(msg)
	var replay json.RawMessage
	if isInit && sc.initialized && len(sc.initResponse) > 0 {
		replay = replayInitializeResponse(sc.initResponse, msg)
		if replay != nil {
			sc = nil // signals the loop below to answer from cache
		}
	}
	m.mu.Unlock()

	if sc == nil {
		// Answer a duplicate initialize from the session's own memory: the server
		// has one process, and a process asked to initialize twice answers the
		// second request with an error and never re-reads the workspace.
		// The cached response was already translated to host paths when it was
		// recorded, so it goes out as-is — translating twice would rewrite it twice.
		select {
		case m.serverCh <- replay:
		default:
			log.Printf("[lsp] server channel full, dropping replayed initialize")
		}
		m.broadcastToSubscribers(replay)
		return nil
	}

	// Translate file URIs from host path to container path
	translated := translateURIsToContainer(msg, sc.workspace, sc.projectMounts)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	response, err := sc.bridgeClient.LSPSend(ctx, sc.session, translated)
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

	// Remember the handshake so a later consumer of this session is answered from
	// it instead of asking the process to initialize a second time.
	if isInit && m.rememberInitResponse(workspaceKey, sc, response) {
		log.Printf("[lsp] session initialized for workspace=%s lang=%s", sc.workspace, sc.language)
	}

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

// rememberInitResponse records the initialize handshake on the client that
// performed it — only if that client is still the current one, so a recycled
// session never inherits another process's answer. An error response is not a
// handshake: caching one would replay a failure at every later consumer.
func (m *ExtensionLSPManager) rememberInitResponse(workspaceKey string, sc *sandboxClient, response json.RawMessage) bool {
	var parsed map[string]any
	if err := json.Unmarshal(response, &parsed); err != nil || parsed == nil {
		return false
	}
	if _, failed := parsed["error"]; failed {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.clients[workspaceKey]
	if !ok || cur != sc {
		return false
	}
	cur.initialized = true
	cur.initResponse = response
	return true
}

// LSPNotify sends a JSON-RPC notification to the LSP server without waiting for a response.
// File URIs are translated from host workspace path to container path.
func (m *ExtensionLSPManager) LSPNotify(process *LSPProcess, msg json.RawMessage) error {
	workspaceKey := processKey(process.Workspace, process.Language)

	m.mu.Lock()
	sc, ok := m.clients[workspaceKey]
	if !ok || sc.bridgeClient == nil || !sc.bridgeClient.IsAlive() {
		if ok {
			delete(m.clients, workspaceKey)
		}
		m.mu.Unlock()
		return fmt.Errorf("sandbox client stale for workspace=%s lang=%s", process.Workspace, process.Language)
	}
	m.mu.Unlock()

	translated := translateURIsToContainer(msg, sc.workspace, sc.projectMounts)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := sc.bridgeClient.LSPNotify(ctx, sc.session, translated); err != nil {
		// Drop the client so the next call reconnects, as in SendToServer.
		m.mu.Lock()
		if sc2, ok2 := m.clients[workspaceKey]; ok2 && sc2 == sc {
			delete(m.clients, workspaceKey)
		}
		m.mu.Unlock()
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

// Subscribe returns a channel that receives a copy of all LSP server messages
// (responses and notifications). Call Unsubscribe when done to release resources.
// The returned channel is bidirectional so Unsubscribe can close it.
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

// adaptProjectMounts mirrors projects' Sandbox Configuration mounts into the LSP
// sandbox, in two scopes:
//
//   - Global paths (e.g. /go, /root/.cache) are mirrored for EVERY registered
//     project. The sandbox is shared, and a module cache is the same for every
//     workspace, so union-ing them keeps the mount set from depending on which
//     project happens to be open.
//   - Paths that live under a project's task sandbox root (/home/sandbox/<name>)
//     are workspace-scoped by nature — a node_modules bind has to land inside the
//     tree the language server is rooted at — so only the owning project's are
//     mirrored, remapped under this workspace's container path.
//
// Everything is read-only, and duplicates collapse: podman rejects two mounts
// sharing one destination.
func (m *ExtensionLSPManager) adaptProjectMounts(workspacePath string, projectMounts map[string]string) []types.SandboxMount {
	if m.projects == nil {
		return nil
	}
	projects, err := m.projects.ListProjects()
	if err != nil {
		log.Printf("[lsp] adaptProjectMounts: %v", err)
		return nil
	}
	absWorkspace, err := filepath.Abs(workspacePath)
	if err != nil {
		absWorkspace = workspacePath
	}
	wsContainer := containerWorkspacePath(absWorkspace, projectMounts)

	var adapted []types.SandboxMount
	seen := map[string]bool{}
	add := func(mo types.SandboxMount) {
		if key := mo.HostPath + "\x00" + mo.ContainerPath; seen[key] {
			return
		}
		seen[mo.HostPath+"\x00"+mo.ContainerPath] = true
		log.Printf("[lsp] adapted project mount: %s -> %s (ro)", mo.HostPath, mo.ContainerPath)
		adapted = append(adapted, mo)
	}

	for i := range projects {
		project := &projects[i]
		if len(project.SandboxMounts) == 0 {
			continue
		}
		absPath, err := filepath.Abs(project.Path)
		if err != nil {
			continue
		}
		ownsWorkspace := strings.HasPrefix(absWorkspace, absPath+string(filepath.Separator)) || absWorkspace == absPath
		taskRoot := kanban_sandbox.MountPath(project.Name)

		for _, mo := range project.SandboxMounts {
			if strings.TrimSpace(mo.HostPath) == "" || strings.TrimSpace(mo.ContainerPath) == "" {
				continue
			}
			cp := mo.ContainerPath
			if strings.HasPrefix(cp, taskRoot+"/") {
				// Workspace-scoped: only the owning project's mount belongs here.
				if !ownsWorkspace {
					continue
				}
				cp = filepath.Join(wsContainer, strings.TrimPrefix(cp, taskRoot+"/"))
			}
			add(types.SandboxMount{HostPath: mo.HostPath, ContainerPath: cp, Mode: "ro"})
		}
	}
	return adapted
}

// LSPRequest makes a synchronous JSON-RPC request to the LSP server for the
// given file. It looks up the matching extension by file path, gets/creates
// the sandbox client, sends the request with URI translation, and returns
// the raw JSON response. Returns an error if no LSP extension handles the file.
func (m *ExtensionLSPManager) LSPRequest(workspacePath, filePath, method string, params any) (json.RawMessage, error) {
	ext, err := m.lookupExtensionByFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("no LSP extension for file %q: %w", filePath, err)
	}
	lspCfg := ext.Manifest.Capabilities.GetLSP()
	if lspCfg == nil {
		return nil, fmt.Errorf("extension %s has no LSP config", ext.Name)
	}
	language := lspCfg.Language

	// Get or create the sandbox client
	workspaceKey := processKey(workspacePath, language)
	m.mu.Lock()
	sc, ok := m.clients[workspaceKey]
	m.mu.Unlock()

	if !ok || sc.bridgeClient == nil || !sc.bridgeClient.IsAlive() {
		// Need to create the connection
		cfg := m.GetRegistry().toServerConfig(ext)
		if cfg == nil {
			return nil, fmt.Errorf("no server config for language %q", language)
		}
		projectMounts := m.collectProjectMounts(workspacePath)
		bridgeClient, containerMounts, err := m.getOrCreateSandbox(ext, workspacePath, projectMounts)
		if err != nil {
			return nil, fmt.Errorf("create sandbox: %w", err)
		}

		sc = &sandboxClient{
			bridgeClient:       bridgeClient,
			workspace:          workspacePath,
			language:           language,
			extensionID:        ext.ID,
			extension:          ext,
			projectMounts:      projectMounts,
			session:            sessionFor(workspacePath),
			gate:               ext.SandboxConfig.Environment,
			openedFiles:        make(map[string]bool),
			openedFileContents: make(map[string]string),
			openedFileVersions: make(map[string]int),
		}
		// Registered before the server starts, so the shared notification pump can
		// attribute the first notifications to this workspace instead of dropping them.
		m.mu.Lock()
		m.clients[workspaceKey] = sc
		m.mu.Unlock()

		m.ensureNotificationPump(bridgeClient, ext.ID)

		if err := m.startSession(context.Background(), sc, lspCfg, containerMounts); err != nil {
			m.mu.Lock()
			if m.clients[workspaceKey] == sc {
				delete(m.clients, workspaceKey)
			}
			m.mu.Unlock()
			return nil, fmt.Errorf("start LSP server: %w", err)
		}
	}

	ctx := context.Background()

	// Initialize the LSP server if not already done
	if !sc.initialized {
		if err := m.lspInitialize(ctx, sc, ext); err != nil {
			return nil, fmt.Errorf("LSP initialize failed: %w", err)
		}
		sc.initialized = true
	}

	// Extract the file URI from params and ensure the server's copy is in sync
	// with the on-disk content (didOpen once, then didChange when the file
	// changes on disk — servers like tsserver do not watch the filesystem).
	if paramsMap, ok := params.(map[string]any); ok {
		if td, ok := paramsMap["textDocument"].(map[string]any); ok {
			if uri, ok := td["uri"].(string); ok {
				if err := m.ensureDocSync(ctx, sc, uri, workspacePath, filePath, language); err != nil {
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
	ctxTimeout, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	response, err := sc.bridgeClient.LSPSend(ctxTimeout, sc.session, translated)
	if err != nil {
		m.mu.Lock()
		if sc2, ok2 := m.clients[workspaceKey]; ok2 && sc2 == sc {
			delete(m.clients, workspaceKey)
		}
		m.mu.Unlock()
		return nil, fmt.Errorf("LSP request failed: %w", err)
	}
	if len(response) == 0 {
		return nil, fmt.Errorf("empty LSP response")
	}

	// Translate URIs back to host paths
	return translateURIsToHost(response, sc.workspace, sc.projectMounts), nil
}

// lspInitialize sends an initialize request to the LSP server and waits for the response.
func (m *ExtensionLSPManager) lspInitialize(ctx context.Context, sc *sandboxClient, ext *extensions.Extension) error {
	absWorkspace, _ := filepath.Abs(sc.workspace)
	initParams := map[string]any{
		"processId": os.Getpid(),
		"rootUri":   "file://" + absWorkspace,
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
	lspCfg := ext.Manifest.Capabilities.GetLSP()
	if lspCfg != nil && lspCfg.InitParamsOverride != nil {
		for k, v := range lspCfg.InitParamsOverride {
			initParams[k] = v
		}
	}

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  initParams,
	}
	reqBytes, _ := json.Marshal(req)
	translated := translateURIsToContainer(reqBytes, sc.workspace, sc.projectMounts)

	ctxTimeout, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := sc.bridgeClient.LSPSend(ctxTimeout, sc.session, translated)
	if err != nil {
		return err
	}

	// Send "initialized" notification
	notify := map[string]any{
		"jsonrpc": "2.0",
		"method":  "initialized",
		"params":  map[string]any{},
	}
	notifyBytes, _ := json.Marshal(notify)
	_ = sc.bridgeClient.LSPNotify(ctx, sc.session, notifyBytes)

	return nil
}

// docSyncAction describes what ensureDocSync must send to keep the server's
// document copy in sync with the on-disk file.
type docSyncAction struct {
	kind    string // "" (no sync), "open" (didOpen), or "change" (didChange)
	version int    // LSP document version to send (for open/change)
}

// decideDocSync is the pure sync decision: given the last content sent to the
// server and the current on-disk content, determine whether to didOpen (file
// never sent), didChange (on-disk content diverged), or do nothing.
func decideDocSync(opened bool, lastSent, onDisk string, lastVersion int) docSyncAction {
	if !opened {
		return docSyncAction{kind: "open", version: 1}
	}
	if lastSent != onDisk {
		return docSyncAction{kind: "change", version: lastVersion + 1}
	}
	return docSyncAction{}
}

// ensureDocSync sends didOpen (first time) or didChange (on-disk content
// changed since the last sync) so the language server never operates on stale
// content. Servers like tsserver do not watch the filesystem, so without this
// a file edited on disk between LSP calls would yield stale results.
func (m *ExtensionLSPManager) ensureDocSync(ctx context.Context, sc *sandboxClient, uri, workspacePath, filePath, language string) error {
	absPath := filepath.Join(workspacePath, filePath)
	content, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("read file for doc sync: %w", err)
	}

	action := decideDocSync(sc.openedFiles[uri], sc.openedFileContents[uri], string(content), sc.openedFileVersions[uri])
	switch action.kind {
	case "open":
		if err := m.lspDidOpen(ctx, sc, uri, language, string(content), action.version); err != nil {
			return err
		}
	case "change":
		if err := m.lspDidChange(ctx, sc, uri, language, string(content), action.version); err != nil {
			return err
		}
	default:
		return nil
	}
	sc.openedFiles[uri] = true
	sc.openedFileContents[uri] = string(content)
	sc.openedFileVersions[uri] = action.version
	return nil
}

// lspDidOpen sends a textDocument/didOpen notification to the LSP server.
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
// sync (no range) to the LSP server.
func (m *ExtensionLSPManager) lspDidChange(ctx context.Context, sc *sandboxClient, uri, language, content string, version int) error {
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
	return sc.bridgeClient.LSPNotify(ctx, sc.session, translated)
}

// lookupExtensionByFile finds the matching LSP extension for a given file path.
func (m *ExtensionLSPManager) lookupExtensionByFile(filePath string) (*extensions.Extension, error) {
	extensions, err := m.extStore.GetByType(extensions.ExtensionTypeLSP)
	if err != nil {
		return nil, err
	}

	ext := filepath.Ext(filePath)
	base := filepath.Base(filePath)
	shortExt := ""
	if len(ext) > 1 {
		shortExt = ext[1:]
	}

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
			if extn == ext || strings.TrimPrefix(extn, ".") == shortExt {
				return &extItem, nil
			}
		}
		// Check language directly
		if lspCfg.Language != "" {
			if ext == "."+lspCfg.Language || base == lspCfg.Language {
				return &extItem, nil
			}
		}
	}

	return nil, fmt.Errorf("no enabled LSP extension found for file %q", filePath)
}

// IsToolCallsEnabledForFile returns true if the LSP extension that handles
// the given file has tool calls enabled. Returns false if no matching
// extension is found or tool calls are disabled for that extension.
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
		// Also check file extensions (entries may or may not have a leading dot)
		for _, extn := range lspCfg.Extensions {
			if extn == language || strings.TrimPrefix(extn, ".") == language {
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

	// Extract file extension and basename
	ext := filepath.Ext(fileOrPath)
	base := filepath.Base(fileOrPath)
	shortExt := ""
	if len(ext) > 1 {
		shortExt = ext[1:]
	}

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
			if extn == ext || extn == shortExt {
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

// HasAnyLSPToolCallsExtension returns true if any enabled LSP extension has
// tool calls enabled. This is used to determine whether LSP tools should be
// available to agent tool calls.
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
		Extensions:         lspCfg.Extensions,
		InitParamsOverride: lspCfg.InitParamsOverride,
	}
}
