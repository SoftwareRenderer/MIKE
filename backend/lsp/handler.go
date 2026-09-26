// Package lsp connects the editor to language servers running in extension sandboxes: a
// browser JSON-RPC WebSocket bridged onto the sandbox-bridge passthrough, the agent-facing
// LSP tools, and extension-driven server discovery.
// One language server per (workspace, language): a server has one root and only answers
// about documents opened on its own connection.
package lsp

import (
	"container/list"
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

// JSONRPCMessage is the routing view of a JSON-RPC 2.0 message: a request (id and method), a
// response (id only), or a notification (method only). Only these members are read — the bytes are
// forwarded unchanged, so `jsonrpc` and `params` are not modelled here.
type JSONRPCMessage struct {
	ID     *json.Number    `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

// RPCError is the error object's message; its code and data are not read.
type RPCError struct {
	Message string `json:"message"`
}

// SubscribeParams carries the workspace and file path for subscription.
type SubscribeParams struct {
	Workspace string `json:"workspace"`
	File      string `json:"file"`
}

// The handler's bounds are counts, not deadlines: nothing here waits on a language server.
const (
	// DefaultMaxSessions bounds live /api/lsp sockets.
	DefaultMaxSessions = 64

	// DefaultMaxPendingRequests bounds the requests one session has waiting for an answer, and so
	// the goroutines waiting with them.
	DefaultMaxPendingRequests = 256

	// DefaultMaxQueuedInitializes bounds the initialize requests waiting behind the one in flight.
	DefaultMaxQueuedInitializes = 64

	// DefaultMaxQueuedMessages bounds one workspace's queue in front of its language server's
	// stdin. A full queue means the server has stopped reading.
	DefaultMaxQueuedMessages = 128

	// DefaultMaxQueuedFrames bounds one browser's outbound queue. Overflow ends the socket, so a
	// browser that cannot drain reconnects rather than losing an answer.
	DefaultMaxQueuedFrames = 128
)

// ClientSession is one WebSocket client of the LSP handler. Workspace and language are tracked
// per-channel via _workspace/_lang hints, not per-session.
type ClientSession struct {
	conn     *websocket.Conn
	clientID uint64
	// mu protects the session's maps (incomingIDs, channelIDs, workspaceKeys, activeWorkspaces)
	// and the pending order below from concurrent access by forwardRequest/routeResponse goroutines.
	mu sync.Mutex
	// backendRequestID → clientRequestID (incoming responses)
	incomingIDs map[string]string
	// backendRequestID → channelID (for multiplex response routing)
	channelIDs map[string]int64
	// backendRequestID → workspaceKey (workspace + language, for per-language routing)
	workspaceKeys map[string]string
	// pendingOrder holds the backend IDs the three maps have a mapping for, oldest forwarded
	// first; pendingElems indexes it by ID. Exceeding a bound is then a lookup, not a scan.
	pendingOrder *list.List
	pendingElems map[string]*list.Element
	// activeWorkspaces is the set of workspace keys this session talks to, however many channels
	// reached them: disconnection releases these keys, a notification reaches these sessions.
	activeWorkspaces map[string]struct{}
	// frames is this session's outbound queue, and quit ends the one goroutine that owns writes
	// to the socket. Nothing else may write to conn.
	frames chan []byte
	quit   chan struct{}
	// done closes when the writer has returned, so hang-up follows the last queued write.
	done chan struct{}
	// stopped makes stop idempotent; once set, deliver refuses.
	stopped bool
}

// deliver queues one frame for this browser without blocking. A session at its bound has its socket
// ended, so a slow browser degrades to a reconnect rather than delaying other traffic — the app
// stream's rule (docs/api-contract.md). ok is false once a frame has been refused.
func (s *ClientSession) deliver(frame []byte) bool {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return false
	}
	select {
	case s.frames <- frame:
		s.mu.Unlock()
		return true
	default:
		s.mu.Unlock()
	}
	logging.For(logging.LSP).Warnf("client %d: %d frames already queued, ending the socket", s.clientID, cap(s.frames))
	s.stop(websocket.StatusTryAgainLater, "the browser stopped reading")
	return false
}

// stop ends the session's writer and the socket underneath it; every path that finds the browser
// unreachable goes through here. CloseNow, not a close handshake: a peer that stopped reading does
// not answer one, and a graceful Close waits 5s for that answer.
func (s *ClientSession) stop(status websocket.StatusCode, reason string) {
	s.mu.Lock()
	if !s.stopped {
		s.stopped = true
		close(s.quit)
		logging.For(logging.LSP).Debugf("client %d: ending the socket (%d): %s", s.clientID, int(status), reason)
	}
	s.mu.Unlock()
	s.conn.CloseNow()
}

// uses records that this session talks to a workspace. A session holds a key once however many
// channels it multiplexes, which is what makes the release decision count sessions.
func (s *ClientSession) uses(workspaceKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeWorkspaces[workspaceKey] = struct{}{}
}

// newClientSession builds the bookkeeping one connection starts with.
func newClientSession(conn *websocket.Conn, clientID uint64, frameBound int) *ClientSession {
	return &ClientSession{
		conn:             conn,
		clientID:         clientID,
		incomingIDs:      make(map[string]string),
		channelIDs:       make(map[string]int64),
		workspaceKeys:    make(map[string]string),
		pendingOrder:     list.New(),
		pendingElems:     make(map[string]*list.Element),
		activeWorkspaces: map[string]struct{}{},
		frames:           make(chan []byte, frameBound),
		quit:             make(chan struct{}),
		done:             make(chan struct{}),
	}
}

// serve is the only writer to this session's socket. A failed write ends the session: the client
// reconnects, and a peer that refused one frame will refuse the next.
func (s *ClientSession) serve() {
	defer close(s.done)
	for {
		select {
		case <-s.quit:
			return
		case frame := <-s.frames:
			if err := httputil.WriteText(context.Background(), s.conn, frame, httputil.WriteTimeout); err != nil {
				logging.For(logging.LSP).Debugf("client %d: write failed, ending the socket: %v", s.clientID, err)
				s.stop(websocket.StatusTryAgainLater, "the write failed")
			}
		}
	}
}

// droppedReq is a forwarded request the session gives up on because too many others are waiting.
// It carries what its failure answer needs, because nothing may be dropped silently.
type droppedReq struct {
	backendID   string
	clientReqID string
	channelID   int64
}

// track records where the answer to a forwarded request must go, and returns the oldest outstanding
// request when this one crossed the session's bound. The caller answers what it returns.
func (s *ClientSession) track(backendID, clientReqID string, channelID int64, workspaceKey string, bound int) *droppedReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.incomingIDs[backendID] = clientReqID
	s.channelIDs[backendID] = channelID
	s.workspaceKeys[backendID] = workspaceKey
	// A forwarded request is this session talking to the workspace; uses states the rule.
	s.activeWorkspaces[workspaceKey] = struct{}{}
	s.pendingElems[backendID] = s.pendingOrder.PushBack(backendID)
	if len(s.incomingIDs) <= bound {
		return nil
	}
	oldest := s.pendingOrder.Front()
	if oldest == nil {
		return nil
	}
	return s.dropLocked(oldest.Value.(string))
}

// forget removes one request's mappings when its answer arrived or its own send failed.
func (s *ClientSession) forget(backendID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropLocked(backendID)
}

// backendIDFor answers the browser's request id with the id this handler gave the server, and reports
// false when nothing is outstanding under it.
func (s *ClientSession) backendIDFor(clientReqID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for backendID, client := range s.incomingIDs {
		if client == clientReqID {
			return backendID, true
		}
	}
	return "", false
}

// dropLocked removes a request's mappings and returns what an answer to it needs, or nil when it is
// already gone. Callers hold s.mu.
func (s *ClientSession) dropLocked(backendID string) *droppedReq {
	elem, ok := s.pendingElems[backendID]
	if !ok {
		return nil
	}
	req := &droppedReq{
		backendID:   backendID,
		clientReqID: s.incomingIDs[backendID],
		channelID:   s.channelIDs[backendID],
	}
	delete(s.pendingElems, backendID)
	s.pendingOrder.Remove(elem)
	delete(s.incomingIDs, backendID)
	delete(s.channelIDs, backendID)
	delete(s.workspaceKeys, backendID)
	return req
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
	// mu guards every field below it, written by the server-message router and by whichever client
	// goroutine arrives first. Lock order is h.mu → mu → ClientSession.mu, never the opposite.
	mu sync.Mutex
	// Only the first channel's initialize reaches the server; later channels get initResponse.
	initialized  bool
	initResponse json.RawMessage
	// Initializes arriving while the first is in flight wait in queuedInitialize.
	pendingInitialize bool
	queuedInitialize  []*queuedInit
	// Only the first "initialized" notification reaches the server.
	sentInitialized bool
	// writes is this workspace's path to its language server's stdin, taken in order by
	// serveWorkspaceWrites. The channel is never closed: a caller may still hold this pointer after
	// a drop, so its enqueue is refused rather than panicking.
	writes chan *writeJob
	quit   chan struct{}
	closed bool
}

// writeJob is one message waiting its turn on a language server's stdin, carrying what a refusal or
// a failed write needs to answer the waiter. clientReqID is empty for a notification.
type writeJob struct {
	ctx          context.Context
	msg          json.RawMessage
	method       string
	process      *LSPProcess
	workspaceKey string
	session      *ClientSession
	clientReqID  string
	channelID    int64
	backendID    string
}

// enqueueWrite hands one message to this workspace's writer. ok is false when the entry is closed or
// its queue is full, and a refusal must be answered: nothing may wait for a write that will not happen.
func (ws *workspaceState) enqueueWrite(job *writeJob) bool {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.closed {
		return false
	}
	select {
	case ws.writes <- job:
		return true
	default:
		return false
	}
}

// closeWrites ends this entry's writer. Nothing can queue afterwards: closed is set under the lock
// the enqueue checks, so the drain that follows sees everything still waiting.
func (ws *workspaceState) closeWrites() {
	ws.mu.Lock()
	if !ws.closed {
		ws.closed = true
		close(ws.quit)
	}
	ws.mu.Unlock()
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
	initCached   initDecision = iota // answer it from the cached response
	initQueued                       // it waits on the response to the request in flight
	initFirst                        // this caller owns the forward to the server
	initOverflow                     // too many are already waiting; the queue is failed, not grown
)

// decideInitialize answers one initialize request against the entry: the cached response, the replay
// queue, or the claim on the forward. The caller holding initFirst owns that request — a failed send
// goes to abandonInitialize, a response to cacheInitResponse.
func (ws *workspaceState) decideInitialize(session *ClientSession, clientReqID string, channelID int64, bound int) (initDecision, json.RawMessage) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	switch {
	case ws.initialized:
		// initialized is set with its response, so an initialized entry has one.
		return initCached, remapResponseIDWithChannel(ws.initResponse, clientReqID, channelID)
	case ws.pendingInitialize:
		if len(ws.queuedInitialize) >= bound {
			return initOverflow, nil
		}
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

// cacheInitResponse stores an initialize response and returns the requests queued behind it, which
// the caller replays. It returns nil for anything that is not an initialize result.
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

// serveWorkspaceWrites owns one workspace's path to its language server's stdin. A notification is
// written and acknowledged before the writer takes the next message, so a server reads a didChange
// before the request computed from it; a request is handed to its own goroutine, because a server
// correlates answers by JSON-RPC id and a slow one must not hold the queue.
func (h *LSPHandler) serveWorkspaceWrites(ws *workspaceState) {
	defer h.wg.Done()
	for {
		// Checked first, without waiting: a dropped entry takes no more messages, so what is still
		// queued gets answered instead.
		select {
		case <-ws.quit:
			h.abandonQueued(ws)
			return
		default:
		}
		select {
		case <-ws.quit:
			h.abandonQueued(ws)
			return
		case job := <-ws.writes:
			if job.clientReqID == "" {
				h.writeNotification(job)
				continue
			}
			go h.writeRequest(job)
		}
	}
}

// writeRequest is one request's round trip, off the writer and off the socket's read loop. A failure
// is answered: the browser waiting on this id has to be told.
func (h *LSPHandler) writeRequest(job *writeJob) {
	err := h.manager.SendToServer(job.ctx, job.process, job.msg)
	if err == nil {
		return
	}
	job.session.forget(job.backendID)
	// failInitialize finds the entry by key, so it runs before the entry is dropped.
	if job.method == "initialize" {
		h.failInitialize(job.workspaceKey, err)
	}
	message := requestNotAnsweredMessage
	if errors.Is(err, errStaleClient) {
		h.dropWorkspace(job.workspaceKey)
		message = serverGoneMessage
	}
	logging.For(logging.LSP).Warnf("client %d: workspace=%s: %s not answered: %v",
		job.session.clientID, job.workspaceKey, job.method, err)
	h.answerError(job.session, job.clientReqID, job.channelID, message)
}

// writeNotification is the serialized half of the writer: it returns once the language server has
// taken the message, so nothing overtakes a document change.
func (h *LSPHandler) writeNotification(job *writeJob) {
	err := h.manager.SendToServer(job.ctx, job.process, job.msg)
	if err == nil {
		return
	}
	if errors.Is(err, errStaleClient) {
		h.dropWorkspace(job.workspaceKey)
	}
	// A notification has no id to answer, so the log is the only report of its unknown document state.
	logging.For(logging.LSP).Warnf("workspace=%s: %s notification not written: %v (the server's copy of that document is unknown)",
		job.workspaceKey, job.method, err)
}

// abandonQueued answers everything still queued when an entry was dropped.
func (h *LSPHandler) abandonQueued(ws *workspaceState) {
	for {
		select {
		case job := <-ws.writes:
			if job.clientReqID == "" {
				continue
			}
			job.session.forget(job.backendID)
			h.answerError(job.session, job.clientReqID, job.channelID, requestNotAnsweredMessage)
		default:
			return
		}
	}
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
	manager    lspManager
	registry   lspRegistry
	mu         sync.Mutex
	clients    map[*websocket.Conn]*ClientSession
	workspaces map[string]*workspaceState
	// Bounds as fields so a test can shrink them instead of opening hundreds of sockets.
	// No environment variable reaches them.
	maxSessions       int
	maxPendingReqs    int
	maxQueuedInit     int
	maxQueuedMessages int
	maxQueuedFrames   int
	// validateWorkspace, if set, restricts which workspace paths a client may subscribe to;
	// nil (the default, used in tests) means no restriction.
	validateWorkspace func(workspace string) bool
	nextID            atomic.Uint64
	nextBackendID     atomic.Int64
	serverRouterDone  chan struct{}
	wg                sync.WaitGroup
}

func NewLSPHandler(reg lspRegistry, mgr lspManager) *LSPHandler {
	h := &LSPHandler{
		registry:          reg,
		manager:           mgr,
		clients:           make(map[*websocket.Conn]*ClientSession),
		workspaces:        make(map[string]*workspaceState),
		serverRouterDone:  make(chan struct{}),
		maxSessions:       DefaultMaxSessions,
		maxPendingReqs:    DefaultMaxPendingRequests,
		maxQueuedInit:     DefaultMaxQueuedInitializes,
		maxQueuedMessages: DefaultMaxQueuedMessages,
		maxQueuedFrames:   DefaultMaxQueuedFrames,
	}
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

// Close stops the handler's background goroutines. Workspace writers go first, so an abandoned queue
// is answered rather than left.
func (h *LSPHandler) Close() {
	h.mu.Lock()
	for key := range h.workspaces {
		h.dropWorkspaceLocked(key)
	}
	h.mu.Unlock()
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
	h.mu.Lock()
	live := len(h.clients)
	h.mu.Unlock()
	if live >= h.maxSessions {
		// Refused before the upgrade, so the browser is not handed a connection it must diagnose.
		logging.For(logging.LSP).Warnf("refusing a session: %d already live (limit %d)", live, h.maxSessions)
		http.Error(w, "too many live LSP sessions", http.StatusServiceUnavailable)
		return
	}

	// Origin checking lives in httputil.AcceptWS.
	conn, ok := httputil.AcceptWS(w, r, "lsp")
	if !ok {
		return
	}

	clientID := h.nextID.Add(1)
	session := newClientSession(conn, clientID, h.framesBound())

	h.mu.Lock()
	// Counting and joining are one decision, so two handshakes cannot both take the last slot.
	pastCap := len(h.clients) >= h.maxSessions
	if !pastCap {
		h.clients[conn] = session
	}
	h.mu.Unlock()

	if pastCap {
		// The upgrade already happened, so the refusal is a close with a code that says retry.
		logging.For(logging.LSP).Warnf("refusing a session after its upgrade: %d already live (limit %d)", h.maxSessions, h.maxSessions)
		_ = conn.Close(websocket.StatusTryAgainLater, "too many live LSP sessions")
		return
	}

	defer func() {
		// Snapshot the keys under the session lock; the handler lock is taken second.
		session.mu.Lock()
		wkKeys := make([]string, 0, len(session.activeWorkspaces))
		for wk := range session.activeWorkspaces {
			wkKeys = append(wkKeys, wk)
		}
		session.mu.Unlock()

		h.mu.Lock()
		delete(h.clients, conn)

		// Hand the server back when no session uses its entry. Sessions are counted, not subscribes:
		// two channels of one editor are one holder.
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
			h.dropWorkspaceLocked(wk)
			logging.For(logging.LSP).Infof("removed process for workspace=%s lang=%s (no clients)", ws.process.Workspace, ws.process.Language)
		}
		h.mu.Unlock()

		session.stop(websocket.StatusNormalClosure, "closed")
		<-session.done
		logging.For(logging.LSP).Infof("client %d disconnected", clientID)
	}()

	logging.For(logging.LSP).Infof("client %d connected", clientID)
	go session.serve()

	// A failed ping cancels the read context: a peer that stopped talking never closes the
	// connection.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	httputil.Keepalive(ctx, conn, nil, cancel)

	for {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			break
		}

		h.handleClientMessage(ctx, session, msg)
	}
}

// serverMessageRouter is the one goroutine that reads every language server's messages, which is
// what lets extension_manager's send wait for room instead of dropping an answer.
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
	var sub struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(msg, &sub); err == nil && sub.Method == "workspace/subscribe" {
		h.handleSubscription(ctx, session, msg)
		return
	}

	channelID, workspace, lang, file, stripped := extractHints(msg)

	var rpc JSONRPCMessage
	if err := json.Unmarshal(stripped, &rpc); err != nil {
		logging.For(logging.LSP).Warnf("client %d: invalid JSON: %v", session.clientID, err)
		return
	}

	if workspace == "" {
		logging.For(logging.LSP).Warnf("client %d: message missing _workspace hint", session.clientID)
		return
	}

	// The _lang hint picks which server answers; the path alone does not.
	routingLang := lang
	workspaceKey := processKey(workspace, routingLang)

	if rpc.Method == "initialize" && rpc.ID != nil {
		h.handleInitialize(ctx, session, stripped, rpc.ID.String(), channelID, workspace, workspaceKey, file)
		return
	}

	if rpc.Method == "initialized" && rpc.ID == nil {
		ws, ok := h.lookupWorkspace(workspaceKey)
		if ok && ws.markInitializedSent() {
			h.forwardRaw(ctx, session, stripped, rpc.Method, workspaceKey)
			h.sendLSPReady(session, channelID)
		}
		// A duplicate is dropped without a second $server/lspReady.
		return
	}

	if rpc.ID != nil && rpc.Method != "" {
		h.forwardRequest(ctx, session, stripped, rpc.Method, rpc.ID.String(), channelID, workspaceKey)
		return
	}

	// $/cancelRequest is the one notification whose params carry a request id, so it is the one
	// message the ID remapping has to follow into.
	if rpc.Method == cancelMethod {
		h.forwardCancel(ctx, session, stripped, workspaceKey)
		return
	}

	h.forwardRaw(ctx, session, stripped, rpc.Method, workspaceKey)
}

// cancelMethod is the JSON-RPC request-cancel notification. Its params name a request by the id
// the browser used, not the one this handler sent.
const cancelMethod = "$/cancelRequest"

// forwardCancel forwards a cancel that still names work the server is doing, with the id the
// server knows. The two sides number requests independently, so the browser's number is never
// passed through.
func (h *LSPHandler) forwardCancel(ctx context.Context, session *ClientSession, msg json.RawMessage, workspaceKey string) {
	rewritten, ok := rewriteCancelID(msg, session)
	if !ok {
		return
	}
	h.forwardRaw(ctx, session, rewritten, cancelMethod, workspaceKey)
}

// rewriteCancelID replaces params.id with the backend id mapped from the browser's. It reports false
// for anything that names no outstanding request, including params that are absent or carry no id.
func rewriteCancelID(msg json.RawMessage, session *ClientSession) (json.RawMessage, bool) {
	var envelope struct {
		Params *struct {
			ID *json.Number `json:"id"`
		} `json:"params"`
	}
	if err := json.Unmarshal(msg, &envelope); err != nil || envelope.Params == nil || envelope.Params.ID == nil {
		return nil, false
	}
	backendID, ok := session.backendIDFor(envelope.Params.ID.String())
	if !ok {
		return nil, false
	}
	var msgMap map[string]any
	if err := json.Unmarshal(msg, &msgMap); err != nil {
		return nil, false
	}
	params, ok := msgMap["params"].(map[string]any)
	if !ok {
		return nil, false
	}
	// json.Number marshals unquoted, as the request id arrived.
	params["id"] = json.Number(backendID)
	out, err := json.Marshal(msgMap)
	if err != nil {
		return nil, false
	}
	return out, true
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

// allowWorkspace is the one answer to "may this client work in this directory?". Every path that
// creates a workspace asks it; a nil validator means no constraint.
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
		session.stop(websocket.StatusProtocolError, "invalid subscribe message")
		return
	}

	if sub.Params.Workspace == "" {
		logging.For(logging.LSP).Warnf("client %d: subscribe missing workspace", session.clientID)
		session.stop(websocket.StatusProtocolError, "missing workspace")
		return
	}

	if !h.allowWorkspace(session, sub.Params.Workspace) {
		session.stop(websocket.StatusProtocolError, "workspace not allowed")
		return
	}

	cfg := h.registry.Lookup(sub.Params.File)
	if cfg == nil {
		// A directory hint alone still names a server.
		cfg = h.registry.Lookup(sub.Params.Workspace)
	}
	if cfg == nil {
		logging.For(logging.LSP).Debugf("client %d: no language server config for file %s", session.clientID, sub.Params.File)
		session.stop(websocket.StatusProtocolError, "no language server config for file")
		return
	}

	workspaceKey := processKey(sub.Params.Workspace, cfg.Language)

	if _, exists := h.lookupWorkspace(workspaceKey); !exists {
		// Spawning happens outside h.mu — it can take seconds.
		process, err := h.manager.GetOrCreate(ctx, sub.Params.Workspace, cfg.Language)
		if err != nil {
			logging.For(logging.LSP).Warnf("client %d: failed to spawn process: %v", session.clientID, err)
			session.stop(websocket.StatusInternalError, fmt.Sprintf("spawn failed: %v", err))
			return
		}
		if _, won := h.registerWorkspace(workspaceKey, process, cfg); !won {
			// Another session won the entry, so this call's own reference goes back.
			h.manager.Release(sub.Params.Workspace, cfg.Language)
		}
	}
	session.uses(workspaceKey)
	logging.For(logging.LSP).Debugf("client %d ch=%d subscribed to workspace=%s lang=%s", session.clientID, sub.ChannelID, sub.Params.Workspace, cfg.Language)

	h.sendLSPReady(session, sub.ChannelID)
}

// handleInitialize intercepts initialize: the first channel's request reaches the server, later
// ones get the cached response, and those arriving while it is in flight are queued and replayed.
// No branch holds a lock across the forward.
func (h *LSPHandler) handleInitialize(ctx context.Context, session *ClientSession, msg json.RawMessage, clientReqID string, channelID int64, workspacePath, workspaceKey, filePath string) {
	// This path creates a workspace from a client-supplied hint, with no subscribe first.
	if !h.allowWorkspace(session, workspacePath) {
		// Answer the request rather than closing: this socket carries every other editor.
		h.answerError(session, clientReqID, channelID, "workspace not allowed")
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
		h.answerError(session, clientReqID, channelID, "language server went away before it initialized")
		return
	}

	decision, cachedResponse := ws.decideInitialize(session, clientReqID, channelID, h.queuedInitBound())
	switch decision {
	case initCached:
		if cachedResponse != nil {
			session.deliver(cachedResponse)
		}
		h.sendLSPReady(session, channelID)
		return
	case initQueued:
		// A first initialize is in flight — routeResponse replays its answer to this request
		return
	case initOverflow:
		// The whole queue is failed, exactly as a send failure fails it; the next initialize is the one
		// that reaches the server.
		cause := fmt.Errorf("too many initialize requests are already waiting (%d)", h.queuedInitBound())
		logging.For(logging.LSP).Warnf("client %d: workspace=%s has %d initialize requests waiting, failing the queue",
			session.clientID, workspaceKey, h.queuedInitBound())
		h.failInitialize(workspaceKey, cause)
		h.answerError(session, clientReqID, channelID, initFailureMessage(cause))
		return
	}

	merged := mergeInitParams(msg, ws.cfg)
	if err := h.forwardRequest(ctx, session, merged, "initialize", clientReqID, channelID, workspaceKey); err != nil {
		// forwardRequest already answered what it never handed over.
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

// registerWorkspace publishes a spawned server under its key and gives it its writer. won is
// false when another spawn took the key first, leaving this caller's process to hand back.
func (h *LSPHandler) registerWorkspace(workspaceKey string, process *LSPProcess, cfg *ServerConfig) (ws *workspaceState, won bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.workspaces[workspaceKey]; ok {
		return existing, false
	}
	ws = &workspaceState{
		process: process,
		cfg:     cfg,
		writes:  make(chan *writeJob, DefaultMaxQueuedMessages),
		quit:    make(chan struct{}),
	}
	h.workspaces[workspaceKey] = ws
	h.wg.Add(1)
	go h.serveWorkspaceWrites(ws)
	return ws, true
}

// dropWorkspace removes an entry and ends its writer. Every removal goes through here, so a
// dropped entry's queue is answered rather than left.
func (h *LSPHandler) dropWorkspace(workspaceKey string) *workspaceState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dropWorkspaceLocked(workspaceKey)
}

// dropWorkspaceLocked is dropWorkspace with h.mu held, which the declared lock order allows.
func (h *LSPHandler) dropWorkspaceLocked(workspaceKey string) *workspaceState {
	ws, ok := h.workspaces[workspaceKey]
	if !ok {
		return nil
	}
	delete(h.workspaces, workspaceKey)
	ws.closeWrites()
	return ws
}

// lazyCreateWorkspace registers a workspace no workspace/subscribe brought. It spawns outside
// h.mu and re-checks the key; ok is false once the client's request has been answered.
func (h *LSPHandler) lazyCreateWorkspace(ctx context.Context, session *ClientSession, clientReqID string, channelID int64, workspacePath, workspaceKey, filePath string) bool {
	logging.For(logging.LSP).Debugf("client %d: workspaceKey=%s not found, lazy init", session.clientID, workspaceKey)
	cfg := h.registry.Lookup(filePath)
	if cfg == nil {
		cfg = h.registry.Lookup(workspacePath)
	}
	if cfg == nil {
		logging.For(logging.LSP).Warnf("client %d: no language server config for file %s (lazy init failed)", session.clientID, filePath)
		h.answerError(session, clientReqID, channelID, "no language server for this file")
		return false
	}
	// The workspace is already allowed: this is the call that bind-mounts it into the
	// extension sandbox (see SetWorkspaceValidator).
	process, err := h.manager.GetOrCreate(ctx, workspacePath, cfg.Language)
	if err != nil {
		logging.For(logging.LSP).Warnf("client %d: failed to spawn process for lazy init: %v", session.clientID, err)
		h.answerError(session, clientReqID, channelID, fmt.Sprintf("failed to start language server: %v", err))
		return false
	}
	if _, won := h.registerWorkspace(workspaceKey, process, cfg); won {
		session.uses(workspaceKey)
		logging.For(logging.LSP).Debugf("client %d: lazy-created workspace=%s lang=%s", session.clientID, workspacePath, cfg.Language)
		return true
	}
	// Another entry already covers this server, so this call's reference goes back.
	h.manager.Release(workspacePath, cfg.Language)
	session.uses(workspaceKey)
	return true
}

// failInitialize unwinds a first initialize that never reached the server, so later initializes
// do not queue behind a failed request.
func (h *LSPHandler) failInitialize(workspaceKey string, cause error) {
	ws, ok := h.lookupWorkspace(workspaceKey)
	if !ok {
		return
	}
	queued := ws.abandonInitialize()

	message := initFailureMessage(cause)
	for _, q := range queued {
		h.answerError(q.session, q.clientReqID, q.channelID, message)
	}
}

// initFailureMessage is what an initialize that will never be answered is answered with.
func initFailureMessage(cause error) string {
	return fmt.Sprintf("language server unreachable: %v", cause)
}

// pendingBound is the outstanding-request bound of one session. The floor is one, so it cannot
// evict the request being forwarded.
func (h *LSPHandler) pendingBound() int {
	if h.maxPendingReqs < 1 {
		return 1
	}
	return h.maxPendingReqs
}

// queuedInitBound is the bound on initialize requests waiting behind the one in flight.
func (h *LSPHandler) queuedInitBound() int {
	if h.maxQueuedInit < 1 {
		return 1
	}
	return h.maxQueuedInit
}

// pendingOverflowMessage answers a request that crossed the session's bound. The bound is named
// because the editor shows the sentence to a person.
func pendingOverflowMessage(bound int) string {
	return fmt.Sprintf("too many requests waiting for the language server (%d); this one was dropped", bound)
}

// requestNotAnsweredMessage answers a forwarded request whose write or answer never happened. The
// cause goes to the log; this is the sentence a person reads.
const requestNotAnsweredMessage = "the language server did not answer this request"

// serverGoneMessage answers a request whose language server stopped while it was in flight, and
// names the action the person can take.
const serverGoneMessage = "the language server for this file stopped; reopen the file to start it again"

// notReadingMessage answers a message refused by a full queue, which means the server stopped
// reading its stdin.
func notReadingMessage(bound int) string {
	return fmt.Sprintf("the language server is not reading (%d messages waiting); this one was not sent", bound)
}

// framesBound is how many frames one browser may leave unread before its socket is ended; a bound
// below one would refuse the frame being queued.
func (h *LSPHandler) framesBound() int {
	if h.maxQueuedFrames < 1 {
		return 1
	}
	return h.maxQueuedFrames
}

// messageBound is how far one workspace may fall behind its server; a bound below one would
// refuse the message being enqueued.
func (h *LSPHandler) messageBound() int {
	if h.maxQueuedMessages < 1 {
		return 1
	}
	return h.maxQueuedMessages
}

// forwardRequest remaps the request ID and hands it to its workspace's writer; channelID routes
// the answer back. ctx is the connection's. The error it returns means the request never reached
// the writer, and the caller unwinds whatever waited on it.
func (h *LSPHandler) forwardRequest(ctx context.Context, session *ClientSession, msg json.RawMessage, method, clientReqID string, channelID int64, workspaceKey string) error {
	backendID := h.nextBackendID.Add(1)
	backendIDStr := itoa(backendID)

	// Record the mapping, and answer whatever this push put over the session's bound.
	bound := h.pendingBound()
	if evicted := session.track(backendIDStr, clientReqID, channelID, workspaceKey, bound); evicted != nil {
		logging.For(logging.LSP).Warnf("client %d: %d requests already waiting for workspace=%s, dropping the oldest (client id=%s backend id=%s)",
			session.clientID, bound, workspaceKey, evicted.clientReqID, evicted.backendID)
		h.answerError(session, evicted.clientReqID, evicted.channelID, pendingOverflowMessage(bound))
	}

	// turnBack answers a request the writer never took, so its mapping holds no slot for an answer
	// that will not be sent.
	turnBack := func(message string) error {
		session.forget(backendIDStr)
		h.answerError(session, clientReqID, channelID, message)
		return errors.New(message)
	}

	remapped := remapRequestID(msg, backendID)
	if remapped == nil {
		logging.For(logging.LSP).Warnf("client %d: failed to remap request id=%s", session.clientID, clientReqID)
		return turnBack(fmt.Sprintf("could not forward request id=%s", clientReqID))
	}

	h.mu.Lock()
	ws, ok := h.workspaces[workspaceKey]
	h.mu.Unlock()
	if !ok || ws.process == nil {
		logging.For(logging.LSP).Debugf("client %d: workspaceKey=%s not found or process nil (ok=%v)", session.clientID, workspaceKey, ok)
		return turnBack("no language server for this workspace")
	}

	job := &writeJob{
		ctx:          ctx,
		msg:          remapped,
		method:       method,
		process:      ws.process,
		workspaceKey: workspaceKey,
		session:      session,
		clientReqID:  clientReqID,
		channelID:    channelID,
		backendID:    backendIDStr,
	}
	if ws.enqueueWrite(job) {
		return nil
	}
	logging.For(logging.LSP).Warnf("client %d: workspace=%s has %d messages waiting, refusing %s",
		session.clientID, workspaceKey, h.messageBound(), method)
	return turnBack(notReadingMessage(h.messageBound()))
}

// answerError replies to a client request that will never reach the language server, so the
// editor is not left waiting on a server that is already gone.
func (h *LSPHandler) answerError(session *ClientSession, clientReqID string, channelID int64, message string) {
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
	if !session.deliver(payload) {
		logging.For(logging.LSP).Debugf("client %d: the failure of request %s had nowhere to go", session.clientID, clientReqID)
	}
}

// forwardRaw hands one message to the workspace's writer without ID remapping. A refused message
// is logged rather than answered: it has no id.
func (h *LSPHandler) forwardRaw(ctx context.Context, session *ClientSession, msg json.RawMessage, method, workspaceKey string) {
	h.mu.Lock()
	ws, ok := h.workspaces[workspaceKey]
	h.mu.Unlock()

	if !ok || ws.process == nil {
		return
	}
	session.uses(workspaceKey)

	job := &writeJob{
		ctx:          ctx,
		msg:          msg,
		method:       method,
		process:      ws.process,
		workspaceKey: workspaceKey,
		session:      session,
	}
	if ws.enqueueWrite(job) {
		return
	}
	logging.For(logging.LSP).Warnf("client %d: workspace=%s refused %s (closed or %d messages waiting): the server's copy of that document is unknown",
		session.clientID, workspaceKey, method, h.messageBound())
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

	if rpc.ID == nil && rpc.Method != "" {
		h.routeNotification(serverMsg)
		return
	}

	// A server request (id and method), e.g. window/workDoneProgress — unhandled, dropped.
	if rpc.ID != nil && rpc.Method != "" {
		return
	}

	// Neither id nor method: usually a truncated body read during teardown.
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
		s.deliver(serverMsg)
	}
}

// routeResponse routes a server response back to the originating client with ID remapping.
// In multiplex mode, includes a channel ID hint (_ch) for frontend routing.
func (h *LSPHandler) routeResponse(serverMsg json.RawMessage, backendID string) {
	h.mu.Lock()
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
			s.dropLocked(backendID)
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

	// Cache an initialize response and replay it to whoever queued behind it. The entry's own lock
	// orders this against an initialize on another connection.
	var queuedReplay []*queuedInit
	if targetWorkspace != nil {
		queuedReplay = targetWorkspace.cacheInitResponse(serverMsg)
	}

	remapped := remapResponseIDWithChannel(serverMsg, originalClientID, channelID)
	if remapped != nil {
		targetSession.deliver(remapped)
	}

	for _, q := range queuedReplay {
		qremapped := remapResponseIDWithChannel(serverMsg, q.clientReqID, q.channelID)
		if qremapped != nil {
			q.session.deliver(qremapped)
		}
		h.sendLSPReady(q.session, q.channelID)
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
		// Some clients send params as an encoded JSON string.
		if err := json.Unmarshal([]byte(p), &params); err != nil {
			return msg
		}
	case nil:
		return msg
	default:
		return msg // unexpected type, forward as-is
	}

	// gopls v0.23+ wants workspaceFolders set and advertised. "rootUri" is the wire spelling;
	// only the Go local is rootURI.
	if rootURI, ok := params["rootUri"]; ok && rootURI != nil && rootURI != "" {
		if _, hasFolders := params["workspaceFolders"]; !hasFolders {
			params["workspaceFolders"] = []map[string]any{
				{"uri": rootURI, "name": "workspace"},
			}
		}
		if caps, ok := params["capabilities"].(map[string]any); ok {
			if wsCaps, ok := caps["workspace"].(map[string]any); ok {
				wsCaps["workspaceFolders"] = true
			} else {
				caps["workspace"] = map[string]any{"workspaceFolders": true}
			}
		}
	}

	if cfg != nil && len(cfg.InitParamsOverride) > 0 {
		for k, v := range cfg.InitParamsOverride {
			if existing, ok := params[k]; ok {
				// A map override replaces its own keys, not the whole map.
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
// hold h.mu and take each session's lock second.
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
func (h *LSPHandler) sendLSPReady(session *ClientSession, channelID int64) {
	msg := map[string]any{
		"jsonrpc": "2.0",
		"method":  "$/lspReady",
	}
	if channelID > 0 {
		msg["_ch"] = channelID
	}
	data, _ := json.Marshal(msg)
	session.deliver(data)
}

// BroadcastLSPReset sends $server/lspReset to every session, which tells the frontend to clear
// stale channels.
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
		s.deliver(msg)
	}
}

// itoa converts an int64 to a string without fmt.
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
