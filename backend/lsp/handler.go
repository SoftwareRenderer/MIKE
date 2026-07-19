package lsp

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"sync/atomic"

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
	process   *LSPProcess
	writeMu   sync.Mutex
	// clientRequestID → backendRequestID (outgoing requests)
	outgoingIDs map[string]string
	// backendRequestID → clientRequestID (incoming responses)
	incomingIDs map[string]string
}

// ProcessManagerOps defines the interface for managing LSP processes.
type ProcessManagerOps interface {
	GetOrCreateProcess(workspace string, cfg *ServerConfig) *LSPProcess
	RemoveProcess(workspace string)
	SendToServer(process *LSPProcess, msg json.RawMessage) error
	ServerMessageCh() <-chan json.RawMessage
}

// LSPHandler bridges frontend LSP clients to backend-managed language server processes.
// It handles request ID remapping so multiple clients can share a single language server.
type LSPHandler struct {
	manager  ProcessManagerOps
	registry *Registry
	mu       sync.Mutex
	clients  map[*websocket.Conn]*ClientSession
	nextID   atomic.Uint64
	// nextBackendID generates unique backend request IDs for remapping
	nextBackendID atomic.Int64
	// serverRouterDone signals the server message router to stop
	serverRouterDone chan struct{}
	// wg waits for background goroutines to finish
	wg sync.WaitGroup
}

// NewLSPHandler creates a new LSPHandler.
func NewLSPHandler(reg *Registry, mgr ProcessManagerOps) *LSPHandler {
	h := &LSPHandler{
		registry:         reg,
		manager:          mgr,
		clients:          make(map[*websocket.Conn]*ClientSession),
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

	clientID := h.nextID.Add(1)
	session := &ClientSession{
		conn:        conn,
		clientID:    clientID,
		outgoingIDs: make(map[string]string),
		incomingIDs: make(map[string]string),
	}

	h.mu.Lock()
	h.clients[conn] = session
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.clients, conn)
		h.mu.Unlock()

		if session.process != nil {
			remaining := session.process.RemoveClient()
			if remaining == 0 {
				h.manager.RemoveProcess(session.process.Workspace)
				log.Printf("[lsp] removed process for workspace=%s (no clients)", session.process.Workspace)
			}
		}

		conn.Close(websocket.StatusNormalClosure, "closed")
		log.Printf("[lsp] client %d disconnected", clientID)
	}()

	log.Printf("[lsp] client %d connected", clientID)

	ctx := r.Context()

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
	if session.process == nil {
		h.handleSubscription(ctx, session, msg)
		return
	}

	// Parse to check message type
	var rpc JSONRPCMessage
	if err := json.Unmarshal(msg, &rpc); err != nil {
		log.Printf("[lsp] client %d: invalid JSON: %v", session.clientID, err)
		return
	}

	// Handle initialize interception
	if rpc.Method == "initialize" && rpc.ID != nil {
		h.handleInitialize(ctx, session, msg, rpc.ID.String())
		return
	}

	// If it's a request (has both id and method), remap the ID
	if rpc.ID != nil && rpc.Method != "" {
		h.forwardRequest(ctx, session, msg, rpc.ID.String())
		return
	}

	// Notification or other message — forward as-is
	h.forwardRaw(ctx, session, msg)
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

	// Look up the server config from the registry
	cfg := h.registry.Lookup(sub.Params.File)
	if cfg == nil {
		// Try to look up by workspace path as fallback
		cfg = h.registry.Lookup(sub.Params.Workspace)
	}
	if cfg == nil {
		// No config found — create a process with nil config for manual servers
		cfg = &ServerConfig{}
	}

	// Get or create the process
	process := h.manager.GetOrCreateProcess(sub.Params.Workspace, cfg)
	process.AddClient()

	session.process = process
	log.Printf("[lsp] client %d subscribed to workspace=%s (process=%s, clients=%d)",
		session.clientID, sub.Params.Workspace, process.ID, process.ClientCount())
}

// handleInitialize intercepts initialize requests. Only the first client's
// initialize reaches the server; subsequent clients get the cached response.
func (h *LSPHandler) handleInitialize(ctx context.Context, session *ClientSession, msg json.RawMessage, clientID string) {
	if session.process.Initialized() {
		// Return cached response immediately
		cached := session.process.GetInitResponse()
		if cached == nil {
			return
		}
		remapped := remapResponseID(cached, clientID)
		if remapped == nil {
			return
		}
		session.writeMu.Lock()
		session.conn.Write(ctx, websocket.MessageText, remapped)
		session.writeMu.Unlock()
		return
	}

	// First client — forward to server with ID remapping
	h.forwardRequest(ctx, session, msg, clientID)
}

// forwardRequest remaps the request ID and forwards to the language server.
func (h *LSPHandler) forwardRequest(ctx context.Context, session *ClientSession, msg json.RawMessage, clientID string) {
	// Generate a unique backend ID
	backendID := h.nextBackendID.Add(1)
	backendIDStr := itoa(backendID)

	// Store the mapping
	session.outgoingIDs[clientID] = backendIDStr
	session.incomingIDs[backendIDStr] = clientID

	// Remap the id in the JSON
	remapped := remapRequestID(msg, backendID)
	if remapped == nil {
		log.Printf("[lsp] client %d: failed to remap request id=%s", session.clientID, clientID)
		return
	}

	// Forward to server
	if err := h.manager.SendToServer(session.process, remapped); err != nil {
		log.Printf("[lsp] client %d: send to server failed: %v", session.clientID, err)
	}

	log.Printf("[lsp] client %d: request id=%s → backend id=%s", session.clientID, clientID, backendIDStr)
}

// forwardRaw forwards a message to the server without ID remapping.
func (h *LSPHandler) forwardRaw(ctx context.Context, session *ClientSession, msg json.RawMessage) {
	if err := h.manager.SendToServer(session.process, msg); err != nil {
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
func (h *LSPHandler) routeResponse(serverMsg json.RawMessage, backendID string) {
	h.mu.Lock()
	// Find the session that owns this backend ID
	var targetSession *ClientSession
	var originalClientID string
	for _, s := range h.clients {
		if s.process == nil {
			continue
		}
		if clientID, ok := s.incomingIDs[backendID]; ok {
			targetSession = s
			originalClientID = clientID
			// Clean up mappings
			delete(s.incomingIDs, backendID)
			delete(s.outgoingIDs, clientID)
			break
		}
	}
	h.mu.Unlock()

	if targetSession == nil {
		log.Printf("[lsp] no client found for backend response id=%s", backendID)
		return
	}

	// Check if this is an initialize response — cache it before remapping
	if targetSession.process != nil && !targetSession.process.Initialized() {
		// Check if the original request was "initialize"
		var rpc JSONRPCMessage
		if err := json.Unmarshal(serverMsg, &rpc); err == nil {
			// Cache the original server response (with backend ID)
			targetSession.process.MarkInitialized(serverMsg)
		}
	}

	// Remap the id back to the original client ID
	remapped := remapResponseID(serverMsg, originalClientID)
	if remapped == nil {
		return
	}

	targetSession.writeMu.Lock()
	targetSession.conn.Write(context.Background(), websocket.MessageText, remapped)
	targetSession.writeMu.Unlock()
}

// broadcastNotification sends a notification to all clients subscribed to the same process.
func (h *LSPHandler) broadcastNotification(serverMsg json.RawMessage) {
	h.mu.Lock()

	// Group sessions by process
	processClients := make(map[*LSPProcess][]*ClientSession)
	for _, s := range h.clients {
		if s.process != nil {
			processClients[s.process] = append(processClients[s.process], s)
		}
	}
	h.mu.Unlock()

	ctx := context.Background()
	for _, sessions := range processClients {
		for _, s := range sessions {
			s.writeMu.Lock()
			s.conn.Write(ctx, websocket.MessageText, serverMsg)
			s.writeMu.Unlock()
		}
	}
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
	var msgMap map[string]any
	if err := json.Unmarshal(msg, &msgMap); err != nil {
		return nil
	}
	msgMap["id"] = clientID
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
