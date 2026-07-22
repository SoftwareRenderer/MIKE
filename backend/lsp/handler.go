package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// JSONRPCMessage represents a JSON-RPC 2.0 message.
// It can be a request (has id and method), a response (has id but no method),
// or a notification (has method but no id).
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

// ClientSession holds the state for a single WebSocket client connected to the LSP handler.
// Workspace and language are tracked per-channel via _workspace/_lang hints, not per-session.
type ClientSession struct {
	conn  *websocket.Conn
	clientID  uint64
	// mu protects the session's maps (incomingIDs, channelIDs, workspaceKeys, activeWorkspaces)
	// from concurrent access by forwardRequest/routeResponse goroutines.
	mu               sync.Mutex
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
	process      *LSPProcess
	language     string
	cfg          *ServerConfig
	clientCount  int
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

// lspRegistry defines the operations the LSP handler needs from the registry.
type lspRegistry interface {
	Lookup(fileOrPath string) *ServerConfig
}

// lspManager defines the operations the LSP handler needs from the process manager.
type lspManager interface {
	GetOrCreate(workspacePath, language string, cfg *ServerConfig) (*LSPProcess, error)
	Release(workspacePath, language string)
	SendToServer(process *LSPProcess, msg json.RawMessage) error
	ServerMessageCh() <-chan json.RawMessage
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
	nextID      atomic.Uint64
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

// Close stops the handler's background goroutines.
func (h *LSPHandler) Close() {
	close(h.serverRouterDone)
	h.wg.Wait()
}

// HandleLSP is the HTTP handler for the LSP WebSocket endpoint at /api/lsp.
func (h *LSPHandler) HandleLSP(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionContextTakeover,
	})
	if err != nil {
		log.Printf("[lsp] accept error: %v", err)
		return
	}

	// Allow large LSP responses (hover docs, completions, etc.)
	conn.SetReadLimit(1024 * 1024) // 1MB

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

		// Clean up workspace state on disconnect.
		// A session can talk to multiple language servers (one per language).
		// Decrement client count for each workspace the session used.
		for _, wk := range wkKeys {
			if ws, ok := h.workspaces[wk]; ok {
				ws.clientCount--
				if ws.clientCount <= 0 {
					h.manager.Release(ws.process.Workspace, ws.process.Language)
					delete(h.workspaces, wk)
					log.Printf("[lsp] removed process for workspace=%s lang=%s (no clients)",
						ws.process.Workspace, ws.process.Language)
				}
			}
		}
		h.mu.Unlock()

		conn.Close(websocket.StatusNormalClosure, "closed")
		log.Printf("[lsp] client %d disconnected", clientID)
	}()

	log.Printf("[lsp] client %d connected", clientID)

	// Ping/pong keepalive
	// If a ping fails, cancel the read context to unblock Read() and close the connection.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		ticker := time.NewTicker(27 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				pingCtx, pingCancel := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Ping(pingCtx)
				if err == nil {
					// Control Ping/Pong frames are transparent to JS and don't reset
					// the client-side stale-detection timer.
					_ = conn.Write(pingCtx, websocket.MessageText, []byte(`{"type":"ping"}`))
				}
				pingCancel()
				if err != nil {
					cancel() // unblock Read() to tear down the connection
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

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

// handleClientMessage processes a message from a WebSocket client.
// Messages can be workspace/subscribe (to register a channel's workspace) or
// JSON-RPC requests/notifications (which must carry _workspace hint).
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
		log.Printf("[lsp] client %d: invalid JSON: %v", session.clientID, err)
		return
	}

	if workspace == "" {
		log.Printf("[lsp] client %d: message missing _workspace hint", session.clientID)
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
		h.mu.Lock()
		ws, ok := h.workspaces[workspaceKey]
		if ok && !ws.sentInitialized {
			ws.sentInitialized = true
			h.mu.Unlock()
			h.forwardRaw(session, stripped, workspaceKey)
			// Notify frontend that LSP is fully ready (after initialized notification forwarded)
			h.sendLSPReady(ctx, session, channelID)
		} else {
			h.mu.Unlock()
			// Silently drop duplicate "initialized" notifications
			// $server/lspReady was already sent (by handleInitialize cached path or first channel)
		}
		return
	}

	// If it's a request (has both id and method), remap the ID
	if rpc.ID != nil && rpc.Method != "" {
		h.forwardRequest(session, stripped, rpc.ID.String(), channelID, workspaceKey)
		return
	}

	// Notification or other message — forward as-is
	h.forwardRaw(session, stripped, workspaceKey)
}

// extractHints extracts _ch (channel ID), _workspace (workspace path), _lang (language),
// and _file (file path) from a message and returns them along with the message
// with all fields stripped.
// Returns (0, "", "", "", original msg) if no _ch field is present.
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

// handleSubscription processes the workspace/subscribe message.
// A single session can subscribe to multiple workspaces (one per channel).
func (h *LSPHandler) handleSubscription(ctx context.Context, session *ClientSession, msg json.RawMessage) {
	var sub struct {
		ChannelID int64           `json:"_ch"`
		Params    SubscribeParams `json:"params"`
	}
	if err := json.Unmarshal(msg, &sub); err != nil {
		log.Printf("[lsp] client %d: invalid subscribe message: %v", session.clientID, err)
		session.conn.Close(websocket.StatusProtocolError, "invalid subscribe message")
		return
	}

	if sub.Params.Workspace == "" {
		log.Printf("[lsp] client %d: subscribe missing workspace", session.clientID)
		session.conn.Close(websocket.StatusProtocolError, "missing workspace")
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
		log.Printf("[lsp] client %d: no language server config for file %s", session.clientID, sub.Params.File)
		session.conn.Close(websocket.StatusProtocolError, "no language server config for file")
		return
	}

	workspaceKey := processKey(sub.Params.Workspace, cfg.Language)

	// Check if workspace already exists (brief lock)
	h.mu.Lock()
	ws, ok := h.workspaces[workspaceKey]
	if ok {
		ws.clientCount++
		h.mu.Unlock()
	} else {
		h.mu.Unlock()
	}

	if !ok {
		// Spawn a new process (OUTSIDE lock to avoid blocking other operations)
		process, err := h.manager.GetOrCreate(sub.Params.Workspace, cfg.Language, cfg)
		if err != nil {
			log.Printf("[lsp] client %d: failed to spawn process: %v", session.clientID, err)
			session.conn.Close(websocket.StatusInternalError, fmt.Sprintf("spawn failed: %v", err))
			return
		}

		// Re-acquire lock to register workspace (double-check pattern)
		h.mu.Lock()
		// Another subscriber may have created it while we were spawning
		if existing, ok := h.workspaces[workspaceKey]; ok {
			existing.clientCount++
			ws = existing
		} else {
			ws = &workspaceState{
				process:     process,
				language:    cfg.Language,
				cfg:         cfg,
				clientCount: 1,
			}
			h.workspaces[workspaceKey] = ws
		}
		h.mu.Unlock()
	}

	session.mu.Lock()
	session.activeWorkspaces[workspaceKey]++ // track for cleanup on disconnect
	session.mu.Unlock()
	log.Printf("[lsp] client %d ch=%d subscribed to workspace=%s lang=%s (clients=%d)",
		session.clientID, sub.ChannelID, sub.Params.Workspace, cfg.Language, ws.clientCount)

	// Notify frontend that LSP is ready for this channel
	h.sendLSPReady(ctx, session, sub.ChannelID)
}

// handleInitialize intercepts initialize requests. Only the first client's
// (or first channel's in multiplex mode) initialize reaches the server;
// subsequent clients/channels get the cached response with a channel ID hint.
// If the first initialize is still in flight, later requests are queued and
// replayed once the server responds.
// workspaceKey identifies the (workspace, language) pair for this request.
// If the workspace doesn't exist yet (e.g., a second language on the same WS),
// it is created lazily using the file path to look up the server config.
func (h *LSPHandler) handleInitialize(ctx context.Context, session *ClientSession, msg json.RawMessage, clientReqID string, channelID int64, workspacePath, workspaceKey, filePath string) {
	h.mu.Lock()
	ws, ok := h.workspaces[workspaceKey]
	if !ok {
		log.Printf("[lsp] client %d: workspaceKey=%s not found, lazy init", session.clientID, workspaceKey)
		h.mu.Unlock()
		// Lazy workspace creation: this is the first request for this language.
		// Look up the server config from the file path and create the workspace.
		// Do this OUTSIDE the lock to avoid blocking other operations.
		cfg := h.registry.Lookup(filePath)
		if cfg == nil {
			cfg = h.registry.Lookup(workspacePath)
		}
		if cfg == nil {
			log.Printf("[lsp] client %d: no language server config for file %s (lazy init failed)", session.clientID, filePath)
			return
		}
		process, err := h.manager.GetOrCreate(workspacePath, cfg.Language, cfg)
		if err != nil {
			log.Printf("[lsp] client %d: failed to spawn process for lazy init: %v", session.clientID, err)
			return
		}
		// Re-acquire lock to register workspace (double-check pattern)
		h.mu.Lock()
		if existing, ok := h.workspaces[workspaceKey]; ok {
			ws = existing
		} else {
			ws = &workspaceState{
				process:     process,
				language:    cfg.Language,
				cfg:         cfg,
				clientCount: 1,
			}
			h.workspaces[workspaceKey] = ws
			session.mu.Lock()
			session.activeWorkspaces[workspaceKey]++
			session.mu.Unlock()
			log.Printf("[lsp] client %d: lazy-created workspace=%s lang=%s", session.clientID, workspacePath, cfg.Language)
		}
	}

	if ws.initialized {
		// Already initialized — return cached response immediately
		var cachedResponse json.RawMessage
		if ws.initResponse != nil {
			cachedResponse = remapResponseIDWithChannel(ws.initResponse, clientReqID, channelID)
		}
		h.mu.Unlock()
		if cachedResponse != nil {
			session.conn.Write(ctx, websocket.MessageText, cachedResponse)
		}
		// Notify frontend that LSP is ready for this channel
		h.sendLSPReady(ctx, session, channelID)
		return
	}

	if ws.pendingInitialize {
		// First initialize is in flight — queue this request
		ws.queuedInitialize = append(ws.queuedInitialize, &queuedInit{
			session:     session,
			clientReqID: clientReqID,
			channelID:   channelID,
		})
		h.mu.Unlock()
		return
	}

	// First client/channel — mark pending, merge params, forward
	ws.pendingInitialize = true
	h.mu.Unlock()

	merged := mergeInitParams(msg, ws.cfg)
	h.forwardRequest(session, merged, clientReqID, channelID, workspaceKey)
}

// forwardRequest remaps the request ID and forwards to the language server.
// channelID is used for multiplex response routing (0 for non-multiplex).
// workspaceKey identifies the (workspace, language) pair.
func (h *LSPHandler) forwardRequest(session *ClientSession, msg json.RawMessage, clientReqID string, channelID int64, workspaceKey string) {
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

	// Remap the id in the JSON
	remapped := remapRequestID(msg, backendID)
	if remapped == nil {
		log.Printf("[lsp] client %d: failed to remap request id=%s", session.clientID, clientReqID)
		return
	}

	// Forward to server
	h.mu.Lock()
	ws, ok := h.workspaces[workspaceKey]
	h.mu.Unlock()

	if !ok || ws.process == nil {
		log.Printf("[lsp] client %d: workspaceKey=%s not found or process nil (ok=%v, process=%v)", session.clientID, workspaceKey, ok, ok && ws.process != nil)
		return
	}

	if err := h.manager.SendToServer(ws.process, remapped); err != nil {
		log.Printf("[lsp] client %d: send to server failed: %v", session.clientID, err)
		// If sandbox is stale, remove workspace entry so next message triggers reconnect
		if strings.Contains(err.Error(), "stale") {
			h.mu.Lock()
			delete(h.workspaces, workspaceKey)
			h.mu.Unlock()
		}
	}

}

// forwardRaw forwards a message to the server without ID remapping.
// workspaceKey identifies the (workspace, language) pair.
func (h *LSPHandler) forwardRaw(session *ClientSession, msg json.RawMessage, workspaceKey string) {
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

	if err := h.manager.SendToServer(ws.process, msg); err != nil {
		log.Printf("[lsp] client %d: forward raw failed: %v", session.clientID, err)
		if strings.Contains(err.Error(), "stale") {
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
		log.Printf("[lsp] invalid server message: %v", err)
		return
	}

	// Response: has id but no method
	if rpc.ID != nil && rpc.Method == "" {
		h.routeResponse(serverMsg, rpc.ID.String())
		return
	}

	// Notification: has method but no id — broadcast to all clients
	if rpc.ID == nil && rpc.Method != "" {
		h.routeNotification(serverMsg)
		return
	}

	// Server request (has both id and method) — e.g. window/workDoneProgress.
	// We don't implement server request handling yet; silently drop.
	if rpc.ID != nil && rpc.Method != "" {
		return
	}

	// Malformed message (neither id nor method) — can occur during process
	// teardown when a partial read dispatches a truncated body. Silently drop.
}

// routeNotification routes a server notification to the appropriate clients.
// Notifications are delivered to all sessions that have active workspaces.
// Within a session, the client-side dispatcher scopes delivery to channels
// sharing the same workspace (via the textDocument.uri in the notification).
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
		_ = s.conn.Write(context.Background(), websocket.MessageText, serverMsg)
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
		log.Printf("[lsp] no client found for backend response id=%s", backendID)
		return
	}

	// Check if this is an initialize response — cache it and replay to queued clients
	var queuedReplay []*queuedInit
	if targetWorkspace != nil && !targetWorkspace.initialized {
		var rpc JSONRPCMessage
		if err := json.Unmarshal(serverMsg, &rpc); err == nil && rpc.Result != nil {
			var result struct {
				Capabilities json.RawMessage `json:"capabilities"`
			}
			if err := json.Unmarshal(rpc.Result, &result); err == nil && len(result.Capabilities) > 0 {
				// This is the initialize response — cache it and wake up queued clients
				targetWorkspace.initialized = true
				targetWorkspace.pendingInitialize = false
				targetWorkspace.initResponse = serverMsg
				queuedReplay = targetWorkspace.queuedInitialize
				targetWorkspace.queuedInitialize = nil
			}
		}
	}

	// Remap the id back to the original client ID, with channel hint if multiplexed
	remapped := remapResponseIDWithChannel(serverMsg, originalClientID, channelID)
	if remapped != nil {
		targetSession.conn.Write(context.Background(), websocket.MessageText, remapped)
	}

	// Replay cached initialize response to queued clients
	for _, q := range queuedReplay {
		qremapped := remapResponseIDWithChannel(serverMsg, q.clientReqID, q.channelID)
		if qremapped != nil {
			q.session.conn.Write(context.Background(), websocket.MessageText, qremapped)
		}
		// Notify frontend that LSP is ready for this queued channel
		h.sendLSPReady(context.Background(), q.session, q.channelID)
	}
}

// mergeInitParams merges the server config's InitParamsOverride into the
// initialize request's params before forwarding to the language server.
// It also ensures workspaceFolders is set from rootUri (required by gopls v0.23+).
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

	// Ensure workspaceFolders is set from rootUri (required by gopls v0.23+)
	if rootUri, ok := params["rootUri"]; ok && rootUri != nil && rootUri != "" {
		if _, hasFolders := params["workspaceFolders"]; !hasFolders {
			params["workspaceFolders"] = []map[string]any{
				{"uri": rootUri, "name": "workspace"},
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

// remapResponseIDWithChannel replaces the "id" field and optionally adds
// a _ch (channel) hint for multiplex frontend routing.
// When channelID > 0, the response includes {"_ch": channelID} so the
// frontend can route it to the correct virtual transport.
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

// sendLSPReady sends a $server/lspReady notification to the client.
// If channelID > 0, includes a _ch hint for multiplex routing.
func (h *LSPHandler) sendLSPReady(ctx context.Context, session *ClientSession, channelID int64) {
	msg := map[string]any{
		"jsonrpc": "2.0",
		"method":  "$/lspReady",
	}
	if channelID > 0 {
		msg["_ch"] = channelID
	}
	data, _ := json.Marshal(msg)
	_ = session.conn.Write(ctx, websocket.MessageText, data)
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
		_ = s.conn.Write(context.Background(), websocket.MessageText, msg)
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
	// Reverse
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
