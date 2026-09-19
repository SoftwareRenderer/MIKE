// Package lsp connects the editor to language servers running in extension sandboxes: a
// browser JSON-RPC WebSocket bridged onto the sandbox-bridge passthrough, the agent-facing
// LSP tools, and extension-driven server discovery.
// One language server per (workspace, language): a server has one root and only answers
// about documents opened on its own connection.
package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"

	"mike/backend/httputil"
	"mike/backend/types"
	"mike/pkg/logging"
)

// JSONRPCMessage is a JSON-RPC 2.0 message: a request (id and method), a response (id only),
// or a notification (method only).
type JSONRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *json.Number    `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError represents a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// SubscribeParams carries the workspace and file path for subscription.
type SubscribeParams struct {
	Workspace string `json:"workspace"`
	File      string `json:"file"`
}

// ClientSession is one WebSocket client of the LSP handler. Workspace and language are tracked
// per-channel via _workspace/_lang hints, not per-session.
type ClientSession struct {
	conn     *websocket.Conn
	clientID uint64
	// mu protects the session's maps (incomingIDs, channelIDs, workspaceKeys, activeWorkspaces)
	// from concurrent access by forwardRequest/routeResponse goroutines.
	mu sync.Mutex
	// backendRequestID → clientRequestID (incoming responses)
	incomingIDs map[string]string
	// backendRequestID → channelID (for multiplex response routing)
	channelIDs map[string]int64
	// backendRequestID → workspaceKey (workspace + language, for per-language routing)
	workspaceKeys map[string]string
	// activeWorkspaces tracks all workspace keys this session has forwarded requests to.
	// Used for cleanup on disconnect (a session can talk to multiple language servers).
	activeWorkspaces map[string]int
}

// queuedInit holds a pending initialize request waiting for the server response.
type queuedInit struct {
	session     *ClientSession
	clientReqID string
	channelID   int64
}

// workspaceState tracks per-workspace+language state in the handler.
type workspaceState struct {
	process *LSPProcess
	cfg     *ServerConfig
	// mu guards every field below it — the initialize lifecycle and the subscriber count — because
	// those are written by the single server-message router goroutine (routeResponse) and by
	// whichever client connection's goroutine arrives first (handleInitialize, handleSubscription,
	// handleClientMessage). h.mu guards the handler's maps (clients, workspaces) and reaches none of
	// them; process, cfg and the entry's own first clientCount are written as the entry is built,
	// before its pointer reaches the map. Lock order is h.mu → mu → ClientSession.mu: an entry's
	// lock may be taken under h.mu, and a session's lock under either, never in the opposite
	// direction (sessionsUsingLocked states the last step).
	mu sync.Mutex
	// clientCount counts the subscribe messages that joined this entry. It is a log line only:
	// it went up per subscribe and down once per session, so the release decision counts
	// sessions instead — see sessionsUsingLocked.
	clientCount int
	// initialized tracks whether the LSP server has been initialized.
	// In multiplex mode, only the first channel's initialize reaches the server;
	// subsequent channels get the cached response.
	initialized  bool
	initResponse json.RawMessage
	// pendingInitialize is true while the first initialize request is in flight.
	// Subsequent initialize requests during this window are queued.
	pendingInitialize bool
	// queuedInitialize holds initialize requests that arrived while the first was pending.
	queuedInitialize []*queuedInit
	// sentInitialized tracks whether the "initialized" notification has been forwarded.
	// Only the first one should reach the server.
	sentInitialized bool
}

// addSubscriber counts a subscribe message on the entry and returns the running total.
func (ws *workspaceState) addSubscriber() int {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.clientCount++
	return ws.clientCount
}

// markInitializedSent reports whether this is the entry's first "initialized" notification.
func (ws *workspaceState) markInitializedSent() bool {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.sentInitialized {
		return false
	}
	ws.sentInitialized = true
	return true
}

// initDecision is what one initialize request found on the entry.
type initDecision int

const (
	initCached initDecision = iota // answer it from the cached response
	initQueued                     // it waits on the response to the request in flight
	initFirst                      // this caller owns the forward to the server
)

// decideInitialize answers one initialize request against the entry: the cached response, the
// replay queue, or the claim on the forward. The caller holding initFirst owns that request: a
// send that fails goes to abandonInitialize, a response to cacheInitResponse.
func (ws *workspaceState) decideInitialize(session *ClientSession, clientReqID string, channelID int64) (initDecision, json.RawMessage) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	switch {
	case ws.initialized:
		if ws.initResponse == nil {
			return initCached, nil
		}
		return initCached, remapResponseIDWithChannel(ws.initResponse, clientReqID, channelID)
	case ws.pendingInitialize:
		ws.queuedInitialize = append(ws.queuedInitialize, &queuedInit{
			session:     session,
			clientReqID: clientReqID,
			channelID:   channelID,
		})
		return initQueued, nil
	default:
		ws.pendingInitialize = true
		return initFirst, nil
	}
}

// cacheInitResponse stores an initialize response for the channels that arrive after it and
// returns the requests queued behind it, which the caller replays. It returns nil for a response
// that is not an initialize result and for an entry that is initialized already.
func (ws *workspaceState) cacheInitResponse(serverMsg json.RawMessage) []*queuedInit {
	var rpc JSONRPCMessage
	if err := json.Unmarshal(serverMsg, &rpc); err != nil || rpc.Result == nil {
		return nil
	}
	var result struct {
		Capabilities json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(rpc.Result, &result); err != nil || len(result.Capabilities) == 0 {
		return nil
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.initialized {
		return nil
	}
	ws.initialized = true
	ws.pendingInitialize = false
	ws.initResponse = serverMsg
	queued := ws.queuedInitialize
	ws.queuedInitialize = nil
	return queued
}

// abandonInitialize unwinds a first initialize that never reached the server and returns everyone
// queued behind it, so later initializes don't queue behind a failed request.
func (ws *workspaceState) abandonInitialize() []*queuedInit {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	queued := ws.queuedInitialize
	ws.queuedInitialize = nil
	ws.pendingInitialize = false
	return queued
}

// lspRegistry defines the operations the LSP handler needs from the registry.
type lspRegistry interface {
	Lookup(fileOrPath string) *ServerConfig
}

// lspManager defines the operations the LSP handler needs from the process manager.
type lspManager interface {
	GetOrCreate(ctx context.Context, workspacePath, language string) (*LSPProcess, error)
	Release(workspacePath, language string)
	SendToServer(ctx context.Context, process *LSPProcess, msg json.RawMessage) error
	ServerMessageCh() <-chan json.RawMessage
	SandboxMemory(workspacePath, filePath string) *types.SandboxMemoryStats
}

// LSPHandler bridges frontend LSP clients to backend-managed language server processes.
// It handles request ID remapping so multiple clients can share a single language server.
type LSPHandler struct {
	manager  lspManager
	registry lspRegistry
	mu       sync.Mutex
	clients  map[*websocket.Conn]*ClientSession
	// Per workspace+language state
	workspaces map[string]*workspaceState
	// validateWorkspace, if set, restricts which workspace paths a client may subscribe to;
	// nil (the default, used in tests) means no restriction.
	validateWorkspace func(workspace string) bool
	nextID            atomic.Uint64
	// nextBackendID generates unique backend request IDs for remapping
	nextBackendID atomic.Int64
	// serverRouterDone signals the server message router to stop
	serverRouterDone chan struct{}
	// wg waits for background goroutines to finish
	wg sync.WaitGroup
}

// NewLSPHandler creates a new LSPHandler.
func NewLSPHandler(reg lspRegistry, mgr lspManager) *LSPHandler {
	h := &LSPHandler{
		registry:         reg,
		manager:          mgr,
		clients:          make(map[*websocket.Conn]*ClientSession),
		workspaces:       make(map[string]*workspaceState),
		serverRouterDone: make(chan struct{}),
	}
	// Start the server message router
	h.wg.Add(1)
	go h.serverMessageRouter()
	return h
}

// SetWorkspaceValidator sets the callback that decides which workspace paths a client may
// subscribe to. Never set means any workspace is allowed (used by tests).
func (h *LSPHandler) SetWorkspaceValidator(fn func(workspace string) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.validateWorkspace = fn
}

// Close stops the handler's background goroutines.
func (h *LSPHandler) Close() {
	close(h.serverRouterDone)
	h.wg.Wait()
}

// HandleSandboxMemory serves GET /api/lsp/memory: the cgroup memory usage of the sandbox
// running the language server for a workspace+file (query params: workspace, file).
func (h *LSPHandler) HandleSandboxMemory(w http.ResponseWriter, r *http.Request) {

	workspace := r.URL.Query().Get("workspace")
	file := r.URL.Query().Get("file")
	if workspace == "" || file == "" {
		httputil.JSONError(w, http.StatusBadRequest, "workspace and file query params are required")
		return
	}

	// validateWorkspace is set once at startup (write-once) — read without
	// locking, mirroring handleSubscription.
	if h.validateWorkspace != nil && !h.validateWorkspace(workspace) {
		httputil.JSONError(w, http.StatusForbidden, "workspace not allowed")
		return
	}

	httputil.JSONResponse(w, http.StatusOK, h.manager.SandboxMemory(workspace, file))
}

// HandleLSP is the HTTP handler for the LSP WebSocket endpoint at /api/lsp.
func (h *LSPHandler) HandleLSP(w http.ResponseWriter, r *http.Request) {
	// Origin checking lives in httputil.AcceptWS; this endpoint hands out a workspace's
	// language servers.
	conn, ok := httputil.AcceptWS(w, r, "lsp")
	if !ok {
		return
	}

	clientID := h.nextID.Add(1)
	session := &ClientSession{
		conn:             conn,
		clientID:         clientID,
		incomingIDs:      make(map[string]string),
		channelIDs:       make(map[string]int64),
		workspaceKeys:    make(map[string]string),
		activeWorkspaces: make(map[string]int),
	}

	h.mu.Lock()
	h.clients[conn] = session
	h.mu.Unlock()

	defer func() {
		// Snapshot workspace keys under session lock, then process under handler lock
		// to avoid holding both locks simultaneously.
		session.mu.Lock()
		wkKeys := make([]string, 0, len(session.activeWorkspaces))
		for wk := range session.activeWorkspaces {
			wkKeys = append(wkKeys, wk)
		}
		session.mu.Unlock()

		h.mu.Lock()
		delete(h.clients, conn)

		// Hand the language server back when no browser session is left using its entry.
		// The count comes from the sessions themselves: clientCount counted one per subscribe
		// but was decremented once per session, so two editor channels on one workspace left it
		// above zero forever and the server was never given back.
		for _, wk := range wkKeys {
			ws, ok := h.workspaces[wk]
			if !ok {
				continue
			}
			if held := h.sessionsUsingLocked(wk); held > 0 {
				logging.For(logging.LSP).Infof("workspace=%s lang=%s still used by %d session(s), keeping the server", ws.process.Workspace, ws.process.Language, held)
				continue
			}
			h.manager.Release(ws.process.Workspace, ws.process.Language)
			delete(h.workspaces, wk)
			logging.For(logging.LSP).Infof("removed process for workspace=%s lang=%s (no clients)", ws.process.Workspace, ws.process.Language)
		}
		h.mu.Unlock()

		conn.Close(websocket.StatusNormalClosure, "closed")
		logging.For(logging.LSP).Infof("client %d disconnected", clientID)
	}()

	logging.For(logging.LSP).Infof("client %d connected", clientID)

	// A failed ping cancels the read context, which is what unblocks the Read loop below:
	// a peer that stopped talking never closes the connection itself.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	httputil.Keepalive(ctx, conn, nil, cancel)

	// Read loop — handle client messages
	for {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			break
		}

		h.handleClientMessage(ctx, session, msg)
	}
}

// serverMessageRouter reads messages from the language server and routes them to clients.
// A single goroutine handles all server messages, avoiding duplication.
func (h *LSPHandler) serverMessageRouter() {
	defer h.wg.Done()

	for {
		select {
		case <-h.serverRouterDone:
			return
		case serverMsg, ok := <-h.manager.ServerMessageCh():
			if !ok {
				return
			}
			h.routeServerMessage(serverMsg)
		}
	}
}

// handleClientMessage processes one WebSocket message: workspace/subscribe registers a
// channel's workspace, a JSON-RPC message must carry the _workspace hint.
func (h *LSPHandler) handleClientMessage(ctx context.Context, session *ClientSession, msg json.RawMessage) {
	// Check if this is a workspace/subscribe message
	var sub struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(msg, &sub); err == nil && sub.Method == "workspace/subscribe" {
		h.handleSubscription(ctx, session, msg)
		return
	}

	// Extract channel ID, workspace, language, file path, and strip wrapper
	channelID, workspace, lang, file, stripped := extractHints(msg)

	// Parse to check message type
	var rpc JSONRPCMessage
	if err := json.Unmarshal(stripped, &rpc); err != nil {
		logging.For(logging.LSP).Warnf("client %d: invalid JSON: %v", session.clientID, err)
		return
	}

	if workspace == "" {
		logging.For(logging.LSP).Warnf("client %d: message missing _workspace hint", session.clientID)
		return
	}

	// Derive workspace key from workspace path + language.
	// _lang hint tells us the language for per-language routing.
	routingLang := lang
	workspaceKey := processKey(workspace, routingLang)

	// Handle initialize interception
	if rpc.Method == "initialize" && rpc.ID != nil {
		h.handleInitialize(ctx, session, stripped, rpc.ID.String(), channelID, workspace, workspaceKey, file)
		return
	}

	// Filter "initialized" notification — only the first per language should reach the server
	if rpc.Method == "initialized" && rpc.ID == nil {
		ws, ok := h.lookupWorkspace(workspaceKey)
		if ok && ws.markInitializedSent() {
			h.forwardRaw(ctx, session, stripped, workspaceKey)
			// Notify frontend that LSP is fully ready (after initialized notification forwarded)
			h.sendLSPReady(ctx, session, channelID)
		}
		// A duplicate "initialized" is dropped silently: $server/lspReady was already sent (by
		// handleInitialize cached path or first channel)
		return
	}

	// If it's a request (has both id and method), remap the ID
	if rpc.ID != nil && rpc.Method != "" {
		h.forwardRequest(ctx, session, stripped, rpc.ID.String(), channelID, workspaceKey)
		return
	}

	// Notification or other message — forward as-is
	h.forwardRaw(ctx, session, stripped, workspaceKey)
}

// extractHints pulls _ch, _workspace, _lang and _file out of a message and returns them with
// the message stripped of those fields, or (0, "", "", "", msg) when there is no _ch.
func extractHints(msg json.RawMessage) (int64, string, string, string, json.RawMessage) {
	var msgMap map[string]any
	if err := json.Unmarshal(msg, &msgMap); err != nil {
		return 0, "", "", "", msg
	}
	chRaw, ok := msgMap["_ch"]
	if !ok {
		return 0, "", "", "", msg
	}
	var channelID int64
	switch v := chRaw.(type) {
	case float64:
		channelID = int64(v)
	case json.Number:
		var err error
		channelID, err = v.Int64()
		if err != nil {
			return 0, "", "", "", msg
		}
	default:
		return 0, "", "", "", msg
	}
	delete(msgMap, "_ch")
	workspace, _ := msgMap["_workspace"].(string)
	delete(msgMap, "_workspace")
	lang, _ := msgMap["_lang"].(string)
	delete(msgMap, "_lang")
	file, _ := msgMap["_file"].(string)
	delete(msgMap, "_file")
	result, err := json.Marshal(msgMap)
	if err != nil {
		return 0, "", "", "", msg
	}
	return channelID, workspace, lang, file, result
}

// allowWorkspace is the one answer to "may this client work in this directory?". Every
// path that creates a workspace must ask it. A nil validator means no constraint (tests).
func (h *LSPHandler) allowWorkspace(session *ClientSession, workspace string) bool {
	if h.validateWorkspace == nil || h.validateWorkspace(workspace) {
		return true
	}
	logging.For(logging.LSP).Debugf("client %d: workspace %q not allowed", session.clientID, workspace)
	return false
}

// handleSubscription processes the workspace/subscribe message.
// A single session can subscribe to multiple workspaces (one per channel).
func (h *LSPHandler) handleSubscription(ctx context.Context, session *ClientSession, msg json.RawMessage) {
	var sub struct {
		ChannelID int64           `json:"_ch"`
		Params    SubscribeParams `json:"params"`
	}
	if err := json.Unmarshal(msg, &sub); err != nil {
		logging.For(logging.LSP).Warnf("client %d: invalid subscribe message: %v", session.clientID, err)
		session.conn.Close(websocket.StatusProtocolError, "invalid subscribe message")
		return
	}

	if sub.Params.Workspace == "" {
		logging.For(logging.LSP).Warnf("client %d: subscribe missing workspace", session.clientID)
		session.conn.Close(websocket.StatusProtocolError, "missing workspace")
		return
	}

	// Restrict the workspace to allowed paths when a validator is configured
	if !h.allowWorkspace(session, sub.Params.Workspace) {
		session.conn.Close(websocket.StatusProtocolError, "workspace not allowed")
		return
	}

	// Look up the server config from the registry using the file extension
	cfg := h.registry.Lookup(sub.Params.File)
	if cfg == nil {
		// Try to look up by workspace path as fallback
		cfg = h.registry.Lookup(sub.Params.Workspace)
	}
	if cfg == nil {
		// No config found — close connection with error
		logging.For(logging.LSP).Debugf("client %d: no language server config for file %s", session.clientID, sub.Params.File)
		session.conn.Close(websocket.StatusProtocolError, "no language server config for file")
		return
	}

	workspaceKey := processKey(sub.Params.Workspace, cfg.Language)

	// Count this subscribe on the entry, spawning a process first when there is no entry yet.
	ws, exists := h.lookupWorkspace(workspaceKey)
	if !exists {
		// Spawn a new process (OUTSIDE lock to avoid blocking other operations)
		process, err := h.manager.GetOrCreate(ctx, sub.Params.Workspace, cfg.Language)
		if err != nil {
			logging.For(logging.LSP).Warnf("client %d: failed to spawn process: %v", session.clientID, err)
			session.conn.Close(websocket.StatusInternalError, fmt.Sprintf("spawn failed: %v", err))
			return
		}

		// Re-acquire lock to register workspace (double-check pattern)
		var droppedProcess bool
		h.mu.Lock()
		// Another subscriber may have created it while we were spawning
		if existing, ok := h.workspaces[workspaceKey]; ok {
			ws = existing
			droppedProcess = true
		} else {
			ws = &workspaceState{
				process: process,
				cfg:     cfg,
			}
			h.workspaces[workspaceKey] = ws
		}
		h.mu.Unlock()
		if droppedProcess {
			// GetOrCreate took a reference on a server this call did not end up registering,
			// because another session won the entry. A reference nobody holds is a server that
			// is never handed back, so drop it here.
			h.manager.Release(sub.Params.Workspace, cfg.Language)
		}
	}
	clients := ws.addSubscriber()

	session.mu.Lock()
	session.activeWorkspaces[workspaceKey]++ // track for cleanup on disconnect
	session.mu.Unlock()
	logging.For(logging.LSP).Debugf("client %d ch=%d subscribed to workspace=%s lang=%s (clients=%d)", session.clientID, sub.ChannelID, sub.Params.Workspace, cfg.Language, clients)

	// Notify frontend that LSP is ready for this channel
	h.sendLSPReady(ctx, session, sub.ChannelID)
}

// handleInitialize intercepts initialize requests: only the first client's (or first channel's
// in multiplex mode) reaches the server, later ones get the cached response, and those arriving
// while it is in flight are queued and replayed. A workspace that does not exist yet — a second
// language on the same WS, or a client that sends initialize before subscribing — is created
// lazily from the file path's server config.
//
// Nothing holds h.mu on entry, and no branch keeps a lock across the forward.
func (h *LSPHandler) handleInitialize(ctx context.Context, session *ClientSession, msg json.RawMessage, clientReqID string, channelID int64, workspacePath, workspaceKey, filePath string) {
	// This path creates a workspace from a client-supplied hint, with no subscribe first.
	if !h.allowWorkspace(session, workspacePath) {
		// Answer the request rather than closing: this socket carries every other editor.
		h.answerError(ctx, session, clientReqID, channelID, "workspace not allowed")
		return
	}

	if _, ok := h.lookupWorkspace(workspaceKey); !ok {
		if !h.lazyCreateWorkspace(ctx, session, clientReqID, channelID, workspacePath, workspaceKey, filePath) {
			return // the client's request was already answered
		}
	}

	ws, ok := h.lookupWorkspace(workspaceKey)
	if !ok {
		// Released by a disconnect between the create above and this lookup.
		h.answerError(ctx, session, clientReqID, channelID, "language server went away before it initialized")
		return
	}

	decision, cachedResponse := ws.decideInitialize(session, clientReqID, channelID)
	switch decision {
	case initCached:
		// Already initialized — the cached response answers this request immediately
		if cachedResponse != nil {
			_ = httputil.WriteText(ctx, session.conn, cachedResponse, httputil.WriteTimeout)
		}
		// Notify frontend that LSP is ready for this channel
		h.sendLSPReady(ctx, session, channelID)
		return
	case initQueued:
		// A first initialize is in flight — routeResponse replays its answer to this request
		return
	}

	// First client/channel — merge params and forward
	merged := mergeInitParams(msg, ws.cfg)
	if err := h.forwardRequest(ctx, session, merged, clientReqID, channelID, workspaceKey); err != nil {
		// The request itself is already answered — forwardRequest answers what it fails to send.
		h.failInitialize(workspaceKey, err)
	}
}

// lookupWorkspace returns the state registered for a workspace key. The lock is not held
// on return, so "found" is not "held".
func (h *LSPHandler) lookupWorkspace(workspaceKey string) (*workspaceState, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ws, ok := h.workspaces[workspaceKey]
	return ws, ok
}

// lazyCreateWorkspace registers a workspace that no workspace/subscribe brought — the
// second language on a connection, or a client that sends initialize first. It spawns
// outside h.mu and registers with a double-check, handing back a reference another caller
// won. ok is false after the client's request has been answered.
func (h *LSPHandler) lazyCreateWorkspace(ctx context.Context, session *ClientSession, clientReqID string, channelID int64, workspacePath, workspaceKey, filePath string) bool {
	logging.For(logging.LSP).Debugf("client %d: workspaceKey=%s not found, lazy init", session.clientID, workspaceKey)
	cfg := h.registry.Lookup(filePath)
	if cfg == nil {
		cfg = h.registry.Lookup(workspacePath)
	}
	if cfg == nil {
		logging.For(logging.LSP).Warnf("client %d: no language server config for file %s (lazy init failed)", session.clientID, filePath)
		h.answerError(ctx, session, clientReqID, channelID, "no language server for this file")
		return false
	}
	// The workspace is already allowed: this is the call that bind-mounts it into the
	// extension sandbox (see SetWorkspaceValidator).
	process, err := h.manager.GetOrCreate(ctx, workspacePath, cfg.Language)
	if err != nil {
		logging.For(logging.LSP).Warnf("client %d: failed to spawn process for lazy init: %v", session.clientID, err)
		h.answerError(ctx, session, clientReqID, channelID, fmt.Sprintf("failed to start language server: %v", err))
		return false
	}
	// Register it, re-checking the window the spawn left open.
	var droppedProcess bool
	h.mu.Lock()
	if _, ok := h.workspaces[workspaceKey]; ok {
		droppedProcess = true
	} else {
		h.workspaces[workspaceKey] = &workspaceState{
			process:     process,
			cfg:         cfg,
			clientCount: 1,
		}
		session.mu.Lock()
		session.activeWorkspaces[workspaceKey]++
		session.mu.Unlock()
		logging.For(logging.LSP).Debugf("client %d: lazy-created workspace=%s lang=%s", session.clientID, workspacePath, cfg.Language)
	}
	h.mu.Unlock()
	if droppedProcess {
		// Another entry already covers this server, so this call's reference goes back.
		h.manager.Release(workspacePath, cfg.Language)
	}
	return true
}

// failInitialize unwinds a first `initialize` that never reached the server: the pending marker
// and everyone queued behind it, so later initializes don't queue behind a failed request.
func (h *LSPHandler) failInitialize(workspaceKey string, cause error) {
	ws, ok := h.lookupWorkspace(workspaceKey)
	if !ok {
		return
	}
	queued := ws.abandonInitialize()

	message := fmt.Sprintf("language server unreachable: %v", cause)
	ctx := context.Background()
	for _, q := range queued {
		h.answerError(ctx, q.session, q.clientReqID, q.channelID, message)
	}
}

// forwardRequest remaps the request ID and forwards it to the workspaceKey server; channelID
// routes the response back in multiplex mode (0 otherwise). ctx is the connection's, so a browser
// that goes away stops holding a write to a dead server.
// A failed send is returned and answered to the client, so nothing waits forever.
func (h *LSPHandler) forwardRequest(ctx context.Context, session *ClientSession, msg json.RawMessage, clientReqID string, channelID int64, workspaceKey string) (err error) {
	// Generate a unique backend ID
	backendID := h.nextBackendID.Add(1)
	backendIDStr := itoa(backendID)

	// Store the mapping
	session.mu.Lock()
	session.incomingIDs[backendIDStr] = clientReqID
	session.channelIDs[backendIDStr] = channelID
	session.workspaceKeys[backendIDStr] = workspaceKey
	// Track that this session uses this workspace (for cleanup on disconnect)
	session.activeWorkspaces[workspaceKey]++
	session.mu.Unlock()

	defer func() {
		if err != nil {
			// No answer is coming, so nothing may keep waiting for one: not the browser
			// (answerError) and not the map that routes answers back.
			session.mu.Lock()
			delete(session.incomingIDs, backendIDStr)
			delete(session.channelIDs, backendIDStr)
			delete(session.workspaceKeys, backendIDStr)
			session.mu.Unlock()
			h.answerError(ctx, session, clientReqID, channelID, err.Error())
		}
	}()

	// Remap the id in the JSON
	remapped := remapRequestID(msg, backendID)
	if remapped == nil {
		logging.For(logging.LSP).Warnf("client %d: failed to remap request id=%s", session.clientID, clientReqID)
		return fmt.Errorf("could not forward request id=%s", clientReqID)
	}

	// Forward to server
	h.mu.Lock()
	ws, ok := h.workspaces[workspaceKey]
	h.mu.Unlock()

	if !ok || ws.process == nil {
		logging.For(logging.LSP).Debugf("client %d: workspaceKey=%s not found or process nil (ok=%v, process=%v)", session.clientID, workspaceKey, ok, ok && ws.process != nil)
		return fmt.Errorf("no language server for workspace")
	}

	if err := h.manager.SendToServer(ctx, ws.process, remapped); err != nil {
		logging.For(logging.LSP).Warnf("client %d: send to server failed: %v", session.clientID, err)
		// If sandbox is stale, remove workspace entry so next message triggers reconnect
		if errors.Is(err, errStaleClient) {
			h.mu.Lock()
			delete(h.workspaces, workspaceKey)
			h.mu.Unlock()
		}
		return err
	}

	return nil
}

// answerError replies to a client request that will never reach the language server, so the
// editor is not left waiting on a server that is already gone.
func (h *LSPHandler) answerError(ctx context.Context, session *ClientSession, clientReqID string, channelID int64, message string) {
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      clientReqID,
		"_ch":     channelID,
		"error": map[string]any{
			"code":    -32000,
			"message": message,
		},
	})
	if err != nil {
		return
	}
	// WithoutCancel: a request that was cancelled still gets its answer.
	if err := httputil.WriteText(context.WithoutCancel(ctx), session.conn, payload, httputil.WriteTimeout); err != nil {
		logging.For(logging.LSP).Warnf("client %d: could not deliver the failure of request %s: %v", session.clientID, clientReqID, err)
	}
}

// forwardRaw forwards a message to the workspaceKey server without ID remapping. ctx is the
// connection's.
func (h *LSPHandler) forwardRaw(ctx context.Context, session *ClientSession, msg json.RawMessage, workspaceKey string) {
	h.mu.Lock()
	ws, ok := h.workspaces[workspaceKey]
	if ok {
		session.mu.Lock()
		session.activeWorkspaces[workspaceKey]++
		session.mu.Unlock()
	}
	h.mu.Unlock()

	if !ok || ws.process == nil {
		return
	}

	if err := h.manager.SendToServer(ctx, ws.process, msg); err != nil {
		logging.For(logging.LSP).Warnf("client %d: forward raw failed: %v", session.clientID, err)
		if errors.Is(err, errStaleClient) {
			h.mu.Lock()
			delete(h.workspaces, workspaceKey)
			h.mu.Unlock()
		}
	}
}

// routeServerMessage routes a message from the language server to the appropriate client(s).
func (h *LSPHandler) routeServerMessage(serverMsg json.RawMessage) {
	var rpc JSONRPCMessage
	if err := json.Unmarshal(serverMsg, &rpc); err != nil {
		logging.For(logging.LSP).Warnf("invalid server message: %v", err)
		return
	}

	if rpc.ID != nil && rpc.Method == "" {
		h.routeResponse(serverMsg, rpc.ID.String())
		return
	}

	// Notification: has method but no id — broadcast to all clients
	if rpc.ID == nil && rpc.Method != "" {
		h.routeNotification(serverMsg)
		return
	}

	// A server request (id and method), e.g. window/workDoneProgress — unhandled, dropped.
	if rpc.ID != nil && rpc.Method != "" {
		return
	}

	// Malformed message (neither id nor method) — can occur during process
	// teardown when a partial read dispatches a truncated body. Silently drop.
}

// routeNotification delivers a server notification to every session with active workspaces;
// the client-side dispatcher scopes it to the channels sharing that workspace.
func (h *LSPHandler) routeNotification(serverMsg json.RawMessage) {
	h.mu.Lock()
	clients := make([]*ClientSession, 0, len(h.clients))
	for _, s := range h.clients {
		s.mu.Lock()
		if len(s.activeWorkspaces) > 0 {
			clients = append(clients, s)
		}
		s.mu.Unlock()
	}
	h.mu.Unlock()

	for _, s := range clients {
		_ = httputil.WriteText(context.Background(), s.conn, serverMsg, httputil.WriteTimeout)
	}
}

// routeResponse routes a server response back to the originating client with ID remapping.
// In multiplex mode, includes a channel ID hint (_ch) for frontend routing.
func (h *LSPHandler) routeResponse(serverMsg json.RawMessage, backendID string) {
	h.mu.Lock()
	// Find the session that owns this backend ID
	var targetSession *ClientSession
	var originalClientID string
	var channelID int64
	var workspaceKey string
	var targetWorkspace *workspaceState
	for _, s := range h.clients {
		s.mu.Lock()
		if clientID, ok := s.incomingIDs[backendID]; ok {
			targetSession = s
			originalClientID = clientID
			channelID = s.channelIDs[backendID]
			workspaceKey = s.workspaceKeys[backendID]
			// Clean up mappings
			delete(s.incomingIDs, backendID)
			delete(s.channelIDs, backendID)
			delete(s.workspaceKeys, backendID)
		}
		s.mu.Unlock()
		if targetSession != nil {
			break
		}
	}
	if targetSession != nil {
		targetWorkspace = h.workspaces[workspaceKey]
	}
	h.mu.Unlock()

	if targetSession == nil {
		logging.For(logging.LSP).Infof("no client found for backend response id=%s", backendID)
		return
	}

	// Cache an initialize response and replay it to whoever queued behind it; the entry's own lock
	// is what orders this against a client's initialize arriving on another connection.
	var queuedReplay []*queuedInit
	if targetWorkspace != nil {
		queuedReplay = targetWorkspace.cacheInitResponse(serverMsg)
	}

	// Remap the id back to the original client ID, with channel hint if multiplexed
	remapped := remapResponseIDWithChannel(serverMsg, originalClientID, channelID)
	if remapped != nil {
		_ = httputil.WriteText(context.Background(), targetSession.conn, remapped, httputil.WriteTimeout)
	}

	// Replay cached initialize response to queued clients
	for _, q := range queuedReplay {
		qremapped := remapResponseIDWithChannel(serverMsg, q.clientReqID, q.channelID)
		if qremapped != nil {
			_ = httputil.WriteText(context.Background(), q.session.conn, qremapped, httputil.WriteTimeout)
		}
		// Notify frontend that LSP is ready for this queued channel
		h.sendLSPReady(context.Background(), q.session, q.channelID)
	}
}

// mergeInitParams merges the config's InitParamsOverride into an initialize request and makes
// sure workspaceFolders is set from rootUri (gopls v0.23+ requires it).
func mergeInitParams(msg json.RawMessage, cfg *ServerConfig) json.RawMessage {
	var msgMap map[string]any
	if err := json.Unmarshal(msg, &msgMap); err != nil {
		return msg // malformed, forward as-is
	}

	paramsRaw, ok := msgMap["params"]
	if !ok {
		return msg // no params field, forward as-is
	}

	var params map[string]any
	switch p := paramsRaw.(type) {
	case map[string]any:
		params = p
	case string:
		// params might be a JSON string in some edge cases
		if err := json.Unmarshal([]byte(p), &params); err != nil {
			return msg
		}
	case nil:
		return msg
	default:
		return msg // unexpected type, forward as-is
	}

	// Ensure workspaceFolders is set from rootUri (required by gopls v0.23+).
	// The key is the LSP wire name, spelled rootUri; only the Go local is rootURI.
	if rootURI, ok := params["rootUri"]; ok && rootURI != nil && rootURI != "" {
		if _, hasFolders := params["workspaceFolders"]; !hasFolders {
			params["workspaceFolders"] = []map[string]any{
				{"uri": rootURI, "name": "workspace"},
			}
		}
		// Also ensure the workspaceFolders capability is advertised
		if caps, ok := params["capabilities"].(map[string]any); ok {
			if wsCaps, ok := caps["workspace"].(map[string]any); ok {
				wsCaps["workspaceFolders"] = true
			} else {
				caps["workspace"] = map[string]any{"workspaceFolders": true}
			}
		}
	}

	// Deep-merge InitParamsOverride into params
	if cfg != nil && len(cfg.InitParamsOverride) > 0 {
		for k, v := range cfg.InitParamsOverride {
			if existing, ok := params[k]; ok {
				// Both sides are maps — merge recursively
				if existingMap, ok := existing.(map[string]any); ok {
					if overrideMap, ok := v.(map[string]any); ok {
						for ok2, ov := range overrideMap {
							existingMap[ok2] = ov
						}
						continue
					}
				}
			}
			params[k] = v
		}
	}

	msgMap["params"] = params
	result, err := json.Marshal(msgMap)
	if err != nil {
		return msg // marshal failed, forward original
	}
	return result
}

// remapRequestID replaces the "id" field in a JSON-RPC request with a new backend ID.
func remapRequestID(msg json.RawMessage, backendID int64) json.RawMessage {
	var msgMap map[string]any
	if err := json.Unmarshal(msg, &msgMap); err != nil {
		return nil
	}
	msgMap["id"] = backendID
	result, err := json.Marshal(msgMap)
	if err != nil {
		return nil
	}
	return result
}

// remapResponseIDWithChannel replaces the "id" field, and when channelID > 0 adds {"_ch": n}
// so the frontend routes the response to the right virtual transport.
func remapResponseIDWithChannel(msg json.RawMessage, clientID string, channelID int64) json.RawMessage {
	var msgMap map[string]any
	if err := json.Unmarshal(msg, &msgMap); err != nil {
		return nil
	}
	msgMap["id"] = clientID
	if channelID > 0 {
		msgMap["_ch"] = channelID
	}
	result, err := json.Marshal(msgMap)
	if err != nil {
		return nil
	}
	return result
}

// sessionsUsingLocked counts the connected sessions that still reference workspaceKey. Callers
// hold h.mu and take each session's own lock second, the order declared at workspaceState.
func (h *LSPHandler) sessionsUsingLocked(workspaceKey string) int {
	used := 0
	for _, s := range h.clients {
		s.mu.Lock()
		_, ok := s.activeWorkspaces[workspaceKey]
		s.mu.Unlock()
		if ok {
			used++
		}
	}
	return used
}

// sendLSPReady tells the client its server is up, with a _ch hint when channelID > 0.
func (h *LSPHandler) sendLSPReady(ctx context.Context, session *ClientSession, channelID int64) {
	msg := map[string]any{
		"jsonrpc": "2.0",
		"method":  "$/lspReady",
	}
	if channelID > 0 {
		msg["_ch"] = channelID
	}
	data, _ := json.Marshal(msg)
	_ = httputil.WriteText(ctx, session.conn, data, httputil.WriteTimeout)
}

// BroadcastLSPReset sends a $server/lspReset notification to all active sessions.
// Tells the frontend to clear stale LSP channels (e.g., after extension rebuild).
func (h *LSPHandler) BroadcastLSPReset() {
	msg, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "$/lspReset",
	})
	h.mu.Lock()
	clients := make([]*ClientSession, 0, len(h.clients))
	for _, s := range h.clients {
		clients = append(clients, s)
	}
	h.mu.Unlock()

	for _, s := range clients {
		_ = httputil.WriteText(context.Background(), s.conn, msg, httputil.WriteTimeout)
	}
}

// itoa converts an int64 to a string without allocation-heavy fmt calls.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := make([]byte, 0, 20)
	for n > 0 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	if neg {
		buf = append(buf, '-')
	}
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
