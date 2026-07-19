package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
type ClientSession struct {
	conn      *websocket.Conn
	clientID  uint64
	workspace string
	language  string
	// clientRequestID → backendRequestID (outgoing requests)
	outgoingIDs map[string]string
	// backendRequestID → clientRequestID (incoming responses)
	incomingIDs map[string]string
	// backendRequestID → channelID (for multiplex response routing)
	channelIDs map[string]int64
}

// queuedInit holds a pending initialize request waiting for the server response.
type queuedInit struct {
	session    *ClientSession
	msg        json.RawMessage
	clientReqID string
	channelID  int64
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
	registry *Registry
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
func NewLSPHandler(reg *Registry, mgr lspManager) *LSPHandler {
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
		conn:        conn,
		clientID:    clientID,
		outgoingIDs: make(map[string]string),
		incomingIDs: make(map[string]string),
		channelIDs:  make(map[string]int64),
	}

	h.mu.Lock()
	h.clients[conn] = session
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.clients, conn)

		// Clean up workspace state on disconnect
		if session.workspace != "" {
			if ws, ok := h.workspaces[session.workspace]; ok {
				ws.clientCount--
				if ws.clientCount <= 0 {
					h.manager.Release(ws.process.Workspace, ws.process.Language)
					delete(h.workspaces, session.workspace)
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
func (h *LSPHandler) handleClientMessage(ctx context.Context, session *ClientSession, msg json.RawMessage) {
	// First message must be a subscription
	if session.workspace == "" {
		h.handleSubscription(ctx, session, msg)
		return
	}

	// Extract channel ID and strip wrapper
	channelID, stripped := extractChannelID(msg)

	// Parse to check message type
	var rpc JSONRPCMessage
	if err := json.Unmarshal(stripped, &rpc); err != nil {
		log.Printf("[lsp] client %d: invalid JSON: %v", session.clientID, err)
		return
	}

	// Handle initialize interception
	if rpc.Method == "initialize" && rpc.ID != nil {
		h.handleInitialize(ctx, session, stripped, rpc.ID.String(), channelID)
		return
	}

	// Filter "initialized" notification — only the first should reach the server
	if rpc.Method == "initialized" && rpc.ID == nil {
		h.mu.Lock()
		ws, ok := h.workspaces[session.workspace]
		if ok && !ws.sentInitialized {
			ws.sentInitialized = true
			h.mu.Unlock()
			h.forwardRaw(ctx, session, stripped)
		} else {
			h.mu.Unlock()
			// Silently drop duplicate "initialized" notifications
		}
		return
	}

	// If it's a request (has both id and method), remap the ID
	if rpc.ID != nil && rpc.Method != "" {
		h.forwardRequest(ctx, session, stripped, rpc.ID.String(), channelID)
		return
	}

	// Notification or other message — forward as-is
	h.forwardRaw(ctx, session, stripped)
}

// extractChannelID extracts the _ch field from a message and returns the
// channel ID along with the message with _ch stripped.
// Returns (0, original msg) if no _ch field is present.
func extractChannelID(msg json.RawMessage) (int64, json.RawMessage) {
	var msgMap map[string]any
	if err := json.Unmarshal(msg, &msgMap); err != nil {
		return 0, msg
	}
	chRaw, ok := msgMap["_ch"]
	if !ok {
		return 0, msg
	}
	var channelID int64
	switch v := chRaw.(type) {
	case float64:
		channelID = int64(v)
	case json.Number:
		var err error
		channelID, err = v.Int64()
		if err != nil {
			return 0, msg
		}
	default:
		return 0, msg
	}
	delete(msgMap, "_ch")
	result, err := json.Marshal(msgMap)
	if err != nil {
		return 0, msg
	}
	return channelID, result
}

// handleSubscription processes the workspace/subscribe message.
func (h *LSPHandler) handleSubscription(ctx context.Context, session *ClientSession, msg json.RawMessage) {
	var sub struct {
		Method string          `json:"method"`
		Params SubscribeParams `json:"params"`
	}
	if err := json.Unmarshal(msg, &sub); err != nil {
		log.Printf("[lsp] client %d: invalid subscribe message: %v", session.clientID, err)
		session.conn.Close(websocket.StatusProtocolError, "invalid subscribe message")
		return
	}

	if sub.Method != "workspace/subscribe" {
		log.Printf("[lsp] client %d: expected workspace/subscribe, got %s", session.clientID, sub.Method)
		session.conn.Close(websocket.StatusProtocolError, "expected workspace/subscribe")
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

	h.mu.Lock()
	defer h.mu.Unlock()

	ws, ok := h.workspaces[workspaceKey]
	if !ok {
		// Spawn a new process
		process, err := h.manager.GetOrCreate(sub.Params.Workspace, cfg.Language, cfg)
		if err != nil {
			log.Printf("[lsp] client %d: failed to spawn process: %v", session.clientID, err)
			session.conn.Close(websocket.StatusInternalError, fmt.Sprintf("spawn failed: %v", err))
			return
		}

		ws = &workspaceState{
			process:   process,
			language:  cfg.Language,
			cfg:       cfg,
			clientCount: 1,
		}
		h.workspaces[workspaceKey] = ws
	} else {
		ws.clientCount++
	}

	session.workspace = workspaceKey
	session.language = cfg.Language
	log.Printf("[lsp] client %d subscribed to workspace=%s lang=%s (clients=%d)",
		session.clientID, sub.Params.Workspace, cfg.Language, ws.clientCount)
}

// handleInitialize intercepts initialize requests. Only the first client's
// (or first channel's in multiplex mode) initialize reaches the server;
// subsequent clients/channels get the cached response with a channel ID hint.
// If the first initialize is still in flight, later requests are queued and
// replayed once the server responds.
func (h *LSPHandler) handleInitialize(ctx context.Context, session *ClientSession, msg json.RawMessage, clientReqID string, channelID int64) {
	h.mu.Lock()
	ws, ok := h.workspaces[session.workspace]
	if !ok {
		h.mu.Unlock()
		return
	}

	if ws.initialized {
		// Already initialized — return cached response immediately
		if ws.initResponse != nil {
			remapped := remapResponseIDWithChannel(ws.initResponse, clientReqID, channelID)
			if remapped != nil {
				session.conn.Write(ctx, websocket.MessageText, remapped)
			}
		}
		h.mu.Unlock()
		return
	}

	if ws.pendingInitialize {
		// First initialize is in flight — queue this request
		ws.queuedInitialize = append(ws.queuedInitialize, &queuedInit{
			session:     session,
			msg:         msg,
			clientReqID: clientReqID,
			channelID:   channelID,
		})
		log.Printf("[lsp] client %d: queued initialize (pending, queue size=%d)", session.clientID, len(ws.queuedInitialize))
		h.mu.Unlock()
		return
	}

	// First client/channel — mark pending, merge params, forward
	ws.pendingInitialize = true
	h.mu.Unlock()

	merged := mergeInitParams(msg, ws.cfg)
	h.forwardRequest(ctx, session, merged, clientReqID, channelID)
}

// forwardRequest remaps the request ID and forwards to the language server.
// channelID is used for multiplex response routing (0 for non-multiplex).
func (h *LSPHandler) forwardRequest(ctx context.Context, session *ClientSession, msg json.RawMessage, clientReqID string, channelID int64) {
	// Generate a unique backend ID
	backendID := h.nextBackendID.Add(1)
	backendIDStr := itoa(backendID)

	// Store the mapping
	session.outgoingIDs[clientReqID] = backendIDStr
	session.incomingIDs[backendIDStr] = clientReqID
	session.channelIDs[backendIDStr] = channelID

	// Remap the id in the JSON
	remapped := remapRequestID(msg, backendID)
	if remapped == nil {
		log.Printf("[lsp] client %d: failed to remap request id=%s", session.clientID, clientReqID)
		return
	}

	// Forward to server
	h.mu.Lock()
	ws, ok := h.workspaces[session.workspace]
	h.mu.Unlock()

	if !ok || ws.process == nil {
		return
	}

	if err := h.manager.SendToServer(ws.process, remapped); err != nil {
		log.Printf("[lsp] client %d: send to server failed: %v", session.clientID, err)
	}

	log.Printf("[lsp] client %d: request id=%s → backend id=%s", session.clientID, clientReqID, backendIDStr)
}

// forwardRaw forwards a message to the server without ID remapping.
func (h *LSPHandler) forwardRaw(ctx context.Context, session *ClientSession, msg json.RawMessage) {
	h.mu.Lock()
	ws, ok := h.workspaces[session.workspace]
	h.mu.Unlock()

	if !ok || ws.process == nil {
		return
	}

	// Log the method for debugging
	var rpc JSONRPCMessage
	_ = json.Unmarshal(msg, &rpc)
	log.Printf("[lsp] client %d: notification %s", session.clientID, rpc.Method)

	if err := h.manager.SendToServer(ws.process, msg); err != nil {
		log.Printf("[lsp] client %d: forward raw failed: %v", session.clientID, err)
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

	// Notification: has method but no id — broadcast to all clients on this process
	if rpc.Method != "" && rpc.ID == nil {
		h.broadcastNotification(serverMsg)
		return
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
	var targetWorkspace *workspaceState
	for _, s := range h.clients {
		if s.workspace == "" {
			continue
		}
		if clientID, ok := s.incomingIDs[backendID]; ok {
			targetSession = s
			originalClientID = clientID
			channelID = s.channelIDs[backendID]
			targetWorkspace = h.workspaces[s.workspace]
			// Clean up mappings
			delete(s.incomingIDs, backendID)
			delete(s.outgoingIDs, clientID)
			delete(s.channelIDs, backendID)
			break
		}
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
				log.Printf("[lsp] cached initialize response for workspace=%s (queued=%d)",
					targetWorkspace.process.Workspace, len(queuedReplay))
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
			log.Printf("[lsp] replayed queued initialize to client %d", q.session.clientID)
		}
	}
}

// broadcastNotification sends a notification to all clients subscribed to the same process.
func (h *LSPHandler) broadcastNotification(serverMsg json.RawMessage) {
	h.mu.Lock()

	// Group sessions by workspace key
	workspaceClients := make(map[string][]*ClientSession)
	for _, s := range h.clients {
		if s.workspace != "" {
			workspaceClients[s.workspace] = append(workspaceClients[s.workspace], s)
		}
	}
	h.mu.Unlock()

	ctx := context.Background()
	for _, sessions := range workspaceClients {
		for _, s := range sessions {
			s.conn.Write(ctx, websocket.MessageText, serverMsg)
		}
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

// remapResponseID replaces the "id" field in a JSON-RPC response with the original client ID.
func remapResponseID(msg json.RawMessage, clientID string) json.RawMessage {
	return remapResponseIDWithChannel(msg, clientID, 0)
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
