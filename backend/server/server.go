// Package server is the HTTP boundary: routing, session/CSRF middleware, host pinning, the
// WebSocket hub, static serving of the built frontend, preview and file endpoints.
// Handlers stay thin and delegate to backend/managers, backend/exec and backend/extensions.
package server

import (
	"context"
	stdtls "crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"kanban/backend/agent"
	"kanban/backend/auth"
	"kanban/backend/config"
	kanbanexec "kanban/backend/exec"
	"kanban/backend/extensions"
	"kanban/backend/git"
	"kanban/backend/httputil"
	"kanban/backend/llmadapter"
	"kanban/backend/lsp"
	"kanban/backend/managers"
	"kanban/backend/sandbox"
	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/term"
	"kanban/backend/tls"
	"kanban/backend/types"
)

// handlerStore is the persistence the HTTP handlers reach. It is wide because message
// and thread editing talks to the store directly instead of through backend/managers, so
// treat an addition here as that debt growing, not as a free method.
type handlerStore interface {
	ClearStaleSummarizing() error
	ClearStaleTitleGenerating() error
	Close() error
	// DBStats returns the pool counters, which Shutdown logs.
	DBStats() sql.DBStats
	CopyMessagesToThread(sourceThreadID, targetThreadID, upToMessageID string) ([]store.CopiedCheckpoint, error)
	CreateTaskMessage(m *types.TaskMessage) error
	CreateThread(th *types.TaskThread) error
	DeleteOtherSessions(ctx context.Context, currentID string) (int, error)
	DeleteSessionByID(ctx context.Context, id string) (bool, error)
	DeleteTaskMessageByID(messageID string) error
	DeleteTaskMessagesAfter(taskID, messageID, threadID string) ([]string, error)
	DeleteThread(threadID string) error
	DeleteThreadWithMessages(threadID string) (*types.TaskThread, error)
	GetLastCommitHashBeforeMessage(messageID, threadID string) (string, error)
	GetLastUserMessageInThread(threadID string) (string, error)
	GetMessageCreatedAt(messageID string) (*time.Time, error)
	GetProject(id string) (*types.Project, error)
	GetTask(projectID, taskID string) (*types.Task, error)
	GetTaskByID(taskID string) (*types.Task, error)
	GetTaskMessageByID(messageID string) (*types.TaskMessage, error)
	GetTaskMessages(taskID, threadID string) ([]types.TaskMessage, error)
	GetThread(threadID string) (*types.TaskThread, error)
	GetThreadsByCheckpointMessageID(messageID string) ([]types.TaskThread, error)
	GetThreadsByCheckpointMessageTime(messageTime string) ([]types.TaskThread, error)
	GetThreadsByTask(taskID string) ([]types.TaskThread, error)
	ListProjects() ([]types.Project, error)
	ListSessions(ctx context.Context) ([]store.SessionInfo, error)
	SetActiveThread(threadID string) error
	SetTaskAssignedPorts(taskID, projectID string, ports []types.AssignedPort) error
	UpdateTask(t *types.Task) error
	UpdateTaskMessage(messageID, content string) error
	UpdateTaskMessageCommitHash(messageID, hash string) error
	UpdateThreadWorktreeStashHash(threadID, hash string) error
}

var _ handlerStore = (*store.Store)(nil)

// Server is the main HTTP server.
type Server struct {
	store               handlerStore
	configStore         *config.ConfigStore
	sessionMgr          *auth.SessionManager
	projectHandler      *managers.ProjectHandler
	taskHandler         *managers.TaskHandler
	boardHandler        *managers.BoardHandler
	appStreamMgr        *stream.AppStreamManager
	gitHandler          *managers.GitHandler
	executor            *kanbanexec.TaskExecutor
	terminalMgr         *term.TerminalManager
	sandboxMgr          *sandbox.SandboxManager
	sandboxStatsSampler *stream.SandboxStatsSampler
	agentRegistry       *agent.AgentRegistry
	extStore            *extensions.ExtensionStore
	llmAdapter          *llmadapter.Adapter // provider request adaptation (llm extensions)
	extSandboxMgr       *extensions.ExtensionSandboxManager
	extHandler          *extensions.ExtensionHandler
	extLSPMgr           *lsp.ExtensionLSPManager
	lspHandler          *lsp.LSPHandler
	loginLimiter        *RateLimiter
	previewStore        *previewStore
	mux                 *http.ServeMux
	addr                string
	frontendDist        string
	tlsConfig           *tls.Config
	allowedHosts        string // extra accepted Host values (--allowed-hosts, else MIKE_ALLOWED_HOSTS)

	// worktreeMux serializes git operations per task.

	// Prevents concurrent stash/checkout/branch/reset operations that would
	// corrupt worktree state if interleaved (e.g., fork+switch, delete+switch).
	worktreeMux   map[string]*sync.Mutex // taskID -> per-task mutex
	worktreeMuxMu sync.RWMutex           // protects worktreeMux map

	// projectMux serializes project-level git operations (stash, checkout, pull, push)
	// that touch shared state like .git/refs/stash. Multiple tasks in the same
	// project share the same stash ref, so concurrent stash push/rev-parse/drop
	// would clobber each other's commit hashes.
	projectMux   map[string]*sync.Mutex // projectID -> per-project mutex
	projectMuxMu sync.RWMutex           // protects projectMux map
}

// NewServer creates a new Server instance.
// addr is the listen address (e.g. ":3000"). frontendDist is the path to the frontend build output (empty to skip).
// allowedHosts lists extra hostnames accepted in the Host header, comma-separated;
// empty falls back to MIKE_ALLOWED_HOSTS (see effectiveAllowedHosts).
func NewServer(dbPath, worktreesDir, addr, frontendDist, allowedHosts string, tlsCfg *tls.Config) (*Server, error) {
	store, err := store.NewStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("create store: %w", err)
	}

	cfgStore := config.NewConfigStore(store, dbPath, Version)
	if err := cfgStore.MigrateProviderIDs(); err != nil {
		return nil, fmt.Errorf("migrate provider IDs: %w", err)
	}
	cfg, err := cfgStore.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("get config: %w", err)
	}

	// Set worktrees directory (CLI flag / env var, or default)
	if worktreesDir == "" {
		worktreesDir = config.DefaultWorkspaceDir
	}
	cfgStore.SetWorkspaceDirOverride(worktreesDir)
	cfg.WorkspaceDir = worktreesDir

	// Ensure the effective worktrees directory exists.
	if err := os.MkdirAll(cfg.WorkspaceDir, 0o755); err != nil {
		return nil, fmt.Errorf("create worktrees dir: %w", err)
	}

	s := &Server{
		store:        store,
		configStore:  cfgStore,
		addr:         addr,
		frontendDist: frontendDist,
		tlsConfig:    tlsCfg,
		allowedHosts: allowedHosts,
		worktreeMux:  make(map[string]*sync.Mutex),
		projectMux:   make(map[string]*sync.Mutex),
		loginLimiter: newLoginRateLimiter(),
	}
	s.previewStore = newPreviewStore()
	s.previewStore.startSweeper()

	// Create session manager — fails if MIKE_LOGIN_PASSWORD is not set
	sessionMgr, err := auth.NewSessionManager(store)
	if err != nil {
		return nil, fmt.Errorf("create session manager: %w\nSet MIKE_LOGIN_PASSWORD environment variable to enable authentication", err)
	}
	s.sessionMgr = sessionMgr

	// Configure cookie Secure flag.
	// Auto-enable when TLS is active; also respect explicit env var for reverse proxy setups.
	sessionCookieSecure = tlsCfg != nil || os.Getenv("MIKE_SECURE_COOKIES") == "1"

	worktreeMgr := git.NewWorktreeManager(store, cfgStore)
	s.appStreamMgr = stream.NewAppStreamManager(store, worktreeMgr)
	s.sandboxMgr = sandbox.NewSandboxManager()
	// Sandbox memory stats: sample running tasks every 2 s, push sandbox_stats
	// task events on change, and serve the most recent usage on task subscribe.
	s.sandboxStatsSampler = stream.NewSandboxStatsSampler(s.sandboxMgr, store, s.appStreamMgr, stream.DefaultSandboxStatsInterval)
	s.appStreamMgr.SetSandboxStatsProvider(s.sandboxStatsSampler.LatestStatsProvider())
	s.sandboxStatsSampler.Start()
	s.boardHandler = managers.NewBoardHandler(store, s.appStreamMgr, cfgStore, s.sandboxMgr)
	s.terminalMgr = term.NewTerminalManager(store, cfgStore, s.sandboxMgr, nil) // agentRegistry wired after creation
	// Wire port assignment callback → store + WebSocket broadcast
	s.terminalMgr.SetOnPortsAssigned(func(taskID, projectID string, ports []types.AssignedPort) {
		// Column-only write: a full-row write from a row read a moment ago could
		// undo a concurrent status or execution_state change.
		if err := store.SetTaskAssignedPorts(taskID, projectID, ports); err != nil {
			log.Printf("[terminal] failed to store port assignment for task %s: %v", taskID, err)
			return
		}
		task, err := store.GetTaskByID(taskID)
		if err != nil {
			log.Printf("[terminal] failed to get task %s for port assignment: %v", taskID, err)
			return
		}
		s.appStreamMgr.BroadcastTaskUpdated(projectID, task)
	})
	// Wire project-level terminal port assignment callback → WebSocket broadcast
	s.terminalMgr.SetOnProjectPortsAssigned(func(projectID string, ports []types.AssignedPort) {
		s.appStreamMgr.BroadcastProjectPorts(projectID, ports)
	})
	commitQueue := kanbanexec.NewCommitQueue()

	// Extension system
	s.extStore = extensions.NewExtensionStore(store)
	if err := s.extStore.InitSchema(); err != nil {
		return nil, fmt.Errorf("init extension schema: %w", err)
	}

	// Agent registry — discovers agent extensions and maps agent:// URLs
	agentRegistry := agent.NewAgentRegistry(s.extStore, cfgStore)
	s.agentRegistry = agentRegistry
	// Wire agent registry to config store so agent providers (e.g. agent://pi →
	// 'Pi Coding Agent') are included in the /api/config response.
	cfgStore.SetAgentRegistry(agentRegistry)

	extRegistry := extensions.NewRegistryClient()
	s.extSandboxMgr = extensions.NewExtensionSandboxManager(extRegistry, s.extStore)
	s.extLSPMgr = lsp.NewExtensionLSPManager(s.extStore, s.extSandboxMgr, store)

	// Provider request adapter — enabled `llm` extensions map MIKE's requests
	// to provider-specific formats. Mappings come from each extension's
	// mappings file and are re-collected on every extension change below.
	extCfg := &extensions.ExtensionConfig{RegistryURL: cfg.RegistryURL}
	// Extension sandbox host mounts are restricted to an allowlist; teach it the
	// effective workspace dir and the directory extension files live in.
	s.extSandboxMgr.SetWorkspaceDir(cfg.WorkspaceDir)
	s.extSandboxMgr.SetExtensionsDir(extCfg.ExtensionsDirOrDefault())
	s.llmAdapter = llmadapter.NewAdapter()
	refreshLLMAdapters := func() {
		ms, err := extensions.CollectLLMMappings(s.extStore, extCfg.ExtensionsDirOrDefault())
		if err != nil {
			log.Printf("[server] collect llm mappings: %v", err)
			return
		}
		s.llmAdapter.SetMappings(ms)
	}
	refreshLLMAdapters()
	cfgStore.SetLLMAdapter(s.llmAdapter)

	s.executor = kanbanexec.NewTaskExecutor(store, cfgStore, s.appStreamMgr, worktreeMgr, s.sandboxMgr, agentRegistry, s.extLSPMgr, commitQueue)
	s.executor.SetLLMAdapter(s.llmAdapter)
	s.executor.RecoverStaleTasks()
	// Wire agent registry to terminal manager (created earlier before agentRegistry was available)
	s.terminalMgr.SetAgentRegistry(agentRegistry)
	s.projectHandler = managers.NewProjectHandler(store, cfgStore, worktreeMgr, s.terminalMgr, s.sandboxMgr, s.executor)
	s.taskHandler = managers.NewTaskHandler(store, s.appStreamMgr, cfgStore, s.terminalMgr, s.executor, worktreeMgr, commitQueue, s.sandboxMgr)
	s.gitHandler = managers.NewGitHandler(store, s.executor.WorktreeMgr())
	// Wire worktree mutex so GitHandler can serialize per-task git operations.
	s.gitHandler.SetWorktreeMutex(s.getWorktreeMutex)

	// s.gitHandler wires the worktree mutex in above; extensions are entirely
	// registry-driven now — there is no built-in bootstrap step.
	s.extHandler = extensions.NewExtensionHandler(s.extStore, s.extSandboxMgr, extRegistry, extCfg)

	// LSP handler for language server WebSocket bridging
	// Use extension-based registry and manager for dynamic LSP discovery
	extLSPRegistry := lsp.NewExtensionRegistry(s.extStore)
	s.lspHandler = lsp.NewLSPHandler(extLSPRegistry, s.extLSPMgr)
	// Restrict LSP workspaces to project paths and task worktrees so a client
	// cannot spawn a language server with cwd at an arbitrary host directory.
	s.lspHandler.SetWorkspaceValidator(func(workspace string) bool {
		cfg, err := s.configStore.GetConfig()
		if err != nil {
			return false
		}
		ws := filepath.Clean(workspace)
		// Task worktrees and projects located under the workspace dir.
		wsDir := filepath.Clean(cfg.WorkspaceDir)
		if ws == wsDir || strings.HasPrefix(ws, wsDir+string(os.PathSeparator)) {
			return true
		}
		// Projects may live anywhere on the host; allow their roots and descendants.
		projects, err := s.store.ListProjects()
		if err != nil {
			return false
		}
		for _, p := range projects {
			root := filepath.Clean(p.Path)
			if ws == root || strings.HasPrefix(ws, root+string(os.PathSeparator)) {
				return true
			}
		}
		return false
	})
	// Wire extension change callback → reload agent registry + broadcast to all clients
	s.extHandler.SetOnExtensionChanged(func() {
		agentRegistry.Reload()
		refreshLLMAdapters()
		s.appStreamMgr.BroadcastExtensionChange()
	})
	// Wire extension update callback → broadcast LSP reset to frontend
	s.extHandler.SetOnImageUpdated(func(extID string) {
		s.lspHandler.BroadcastLSPReset()
	})
	// Wire project mutex so GitHandler can serialize project-level git operations.
	s.gitHandler.SetProjectMutex(s.getProjectMutex)
	// Wire worktree watcher -> GitHandler diff cache so on-demand file fetches work
	s.appStreamMgr.SetDiffCacheUpdate(s.gitHandler.UpdateDiffCache)

	// Run garbage collector on startup
	dryRun := os.Getenv("MIKE_GC_DRY_RUN") == "1"
	if removed, err := s.executor.WorktreeMgr().GarbageCollect(dryRun); err != nil {
		log.Printf("[gc] startup error: %v", err)
	} else if removed > 0 {
		log.Printf("[gc] startup: %d orphaned worktrees cleaned", removed)
	}

	// Clear stale title_generating flags from previous server sessions
	if err := store.ClearStaleTitleGenerating(); err != nil {
		log.Printf("[title-gen] startup: failed to clear stale flags: %v", err)
	}
	// Clear stale summarizing flags from previous server sessions
	if err := store.ClearStaleSummarizing(); err != nil {
		log.Printf("[summarize] startup: failed to clear stale flags: %v", err)
	}

	s.setupRoutes()

	return s, nil
}

// Serve starts the HTTP server.
// handleFetchModels handles POST /api/config/fetch-models.
// It supports both standard OpenAI-compatible LLM servers and Pi agent extensions.
func (s *Server) handleFetchModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req config.FetchModelsRequest
	if !httputil.DecodeJSON(w, r, &req) {
		return
	}
	if req.LlamaServerURL == "" {
		httputil.JSONError(w, http.StatusBadRequest, "llama_server_url is required")
		return
	}

	if strings.HasPrefix(req.LlamaServerURL, "agent://") {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		agentName := strings.TrimPrefix(req.LlamaServerURL, "agent://")
		ext := s.agentRegistry.LookupByName(agentName)
		if ext == nil {
			httputil.JSONError(w, http.StatusNotFound, fmt.Sprintf("Agent %q not found", agentName))
			return
		}

		// Load the adapter and ask it for available models
		adapter, err := agent.LoadAdapter(ext)
		if err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to load agent adapter: %v", err))
			return
		}
		defer adapter.Close()

		// Initialize the adapter with minimal config (just enough for SubprocessConfig)
		initCfg, _ := json.Marshal(map[string]any{
			"extension_name": ext.Name,
			"session_name":   "fetch-models-probe",
			"worktree_path":  os.TempDir(),
			"sandbox":        false,
			"config":         ext.Config,
		})
		if err := adapter.Init(ctx, initCfg); err != nil {
			httputil.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to init agent adapter: %v", err))
			return
		}

		agentModels := adapter.ListModels(ctx)
		var fetchedModels []config.FetchedModel
		for _, m := range agentModels {
			fetchedModels = append(fetchedModels, config.FetchedModel{
				Name:          m.Name,
				ContextWindow: m.ContextWindow,
			})
		}

		httputil.JSONResponse(w, http.StatusOK, config.FetchModelsResponse{Models: fetchedModels})
		return
	}

	// Fallback to standard LLM fetching.
	// A masked/empty key means "use the stored key for this provider".
	req.APIKey = s.configStore.ResolveAPIKey(req.LlamaServerURL, req.APIKey)
	result := s.configStore.FetchModels(req)
	httputil.JSONResponse(w, http.StatusOK, result)
}

func (s *Server) RedirectHTTPSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.tlsConfig == nil {
			next.ServeHTTP(w, r)
			return
		}

		// Only trust X-Forwarded-Proto when explicitly behind a trusted proxy.
		// Otherwise an attacker can set it to "https" to bypass the redirect.
		isHTTPS := r.TLS != nil
		if !isHTTPS && trustProxy() && r.Header.Get("X-Forwarded-Proto") == "https" {
			isHTTPS = true
		}
		if !isHTTPS {
			target := fmt.Sprintf("https://%s%s", r.Host, r.URL.RequestURI())
			http.Redirect(w, r, target, http.StatusMovedPermanently)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withNonce wraps an http.Handler (the frontend file server) and intercepts
// requests for "/" to serve index.html with a request-specific CSP nonce.
// All other requests are passed through to the underlying handler.
func (s *Server) withNonce(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only intercept exact "/" requests (the index page)
		if r.URL.Path == "/" {
			indexPath := filepath.Join(s.frontendDist, "index.html")
			content, err := os.ReadFile(indexPath)
			if err != nil {
				http.Error(w, "failed to read index.html", http.StatusInternalServerError)
				return
			}

			nonce, ok := r.Context().Value(nonceKey).(string)
			if !ok || nonce == "" {
				nonce = "fallback"
			}

			// Inject nonce into the inline script
			updatedContent := strings.Replace(string(content), "nonce=\"{{.Nonce}}\"", fmt.Sprintf("nonce=\"%s\"", nonce), 1)

			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(updatedContent))
			return
		}

		// Pass all other requests (assets, favicon, etc.) to the file server
		next.ServeHTTP(w, r)
	})
}

type Middleware func(http.Handler) http.Handler

// defaultSniffTimeout is how long a connection gets to send the byte that identifies
// it. Real clients send it immediately; it only bounds a peer that connected and went
// silent (lost first packet, cancelled preconnect, health check, scan).
const defaultSniffTimeout = 10 * time.Second

// readyQueue paces sniffed connections against net/http's accept loop. No client
// waits behind it: a connection lands in it only once it can be served.
const readyQueue = 8

// tlsRecordTypeHandshake is the first byte of a TLS record, which is how TLS is told
// apart from plain HTTP before a request line exists.
const tlsRecordTypeHandshake = 0x16

// hybridListener handles both TLS and plain HTTP on the same port.
type hybridListener struct {
	net.Listener
	stdTlsConfig *stdtls.Config
	// sniffTimeout bounds the first-byte read. Zero means defaultSniffTimeout.
	sniffTimeout time.Duration
	drops        *sniffDropLogger

	ready chan net.Conn // connections whose protocol is known
	done  chan struct{} // closed once the listener is finished
	once  sync.Once
	mu    sync.Mutex
	last  error // why the accept loop stopped, if a real failure caused it
}

// Accept hands net/http a connection whose protocol is already known.
//
// The first byte decides TLS vs plain HTTP, and net/http only looks at the concrete
// type Accept returns, so that read cannot be deferred — but it must not happen
// inside Accept: net/http accepts serially, so a peer that connects and sends nothing
// would delay every later handshake, and its EOF or ECONNRESET would reach Serve as a
// dead listener and end it.
//
// So Accept never touches a socket — acceptLoop sniffs each connection in its own
// goroutine and queues the result. No worker pool: a pool of N only moves the stall to
// the Nth silent peer. Only the listener's own error is returned, since it is the only
// one that can end Serve.
// one that can end Serve.
func (l *hybridListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.ready:
		return conn, nil
	case <-l.done:
		if err := l.stopErr(); err != nil {
			return nil, err
		}
		// net/http turns this into http.ErrServerClosed while shutting down.
		return nil, net.ErrClosed
	}
}

func (l *hybridListener) Close() error {
	err := l.Listener.Close()
	l.shutdown(err)
	return err
}

// acceptLoop is the only caller of the underlying Accept, and it never reads from a
// connection, so no client can block it.
func (l *hybridListener) acceptLoop() {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			// Listener gone (Close on shutdown, or a real failure): release Accept
			// with the reason, since nothing more can be accepted.
			l.shutdown(err)
			return
		}
		go l.identify(conn)
	}
}

// identify reads the first byte under a deadline and queues the connection wrapped for
// the protocol it announced. A peer that stalled, hung up or reset is logged and closed
// here: it says nothing about the listener and must not reach net/http.
func (l *hybridListener) identify(raw net.Conn) {
	timeout := l.sniffTimeout
	if timeout <= 0 {
		timeout = defaultSniffTimeout
	}
	conn := raw
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		l.drops.note(conn.RemoteAddr(), err)
		conn.Close()
		return
	}

	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err != nil {
		l.drops.note(conn.RemoteAddr(), err)
		conn.Close()
		return
	}
	// The deadline was for the sniff alone. net/http sets its own (ReadHeaderTimeout),
	// and a tls.Conn wrapping this socket would inherit the stale one.
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		l.drops.note(conn.RemoteAddr(), err)
		conn.Close()
		return
	}

	if buf[0] == tlsRecordTypeHandshake {
		conn = stdtls.Server(newPeekConn(conn, buf), l.stdTlsConfig)
	} else {
		conn = newPeekConn(conn, buf)
	}

	select {
	case l.ready <- conn:
	case <-l.done:
		conn.Close()
	}
}

// shutdown records why the listener stopped, releases Accept, and closes any queued
// connection nobody will serve. The first reason wins, so Close cannot mask a failure.
func (l *hybridListener) shutdown(reason error) {
	l.mu.Lock()
	if l.last == nil {
		l.last = reason
	}
	l.mu.Unlock()

	l.once.Do(func() {
		close(l.done)
		for {
			select {
			case conn := <-l.ready:
				conn.Close()
			default:
				return
			}
		}
	})
}

// stopErr returns why the accept loop stopped, if a real failure caused it.
func (l *hybridListener) stopErr() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last
}

// sniffLogInterval is how often one host may be reported for dropped connections;
// a scanner or a device retrying on a dead network drops one per attempt.
const sniffLogInterval = time.Minute

// maxSniffHosts bounds the rate-limit map. Past it, new hosts are simply suppressed.
const maxSniffHosts = 256

// sniffDropLogger rate-limits drop logging per remote host, like tlsTrustNoiseFilter
// does for handshake noise.
type sniffDropLogger struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// note logs the drop unless this host was reported within the last sniffLogInterval.
func (l *sniffDropLogger) note(addr net.Addr, cause error) {
	if l == nil || addr == nil || !l.allowed(addr) {
		return
	}
	log.Printf("[accept] dropped connection from %s before its first byte: %v (further drops from this host are suppressed for %s)",
		addr, cause, sniffLogInterval)
}

// allowed stamps addr's host and reports whether it may be logged now.
func (l *sniffDropLogger) allowed(addr net.Addr) bool {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		l.last = map[string]time.Time{}
	}
	if at, ok := l.last[host]; ok && time.Since(at) < sniffLogInterval {
		return false
	}
	if _, ok := l.last[host]; !ok && len(l.last) >= maxSniffHosts {
		return false
	}
	l.last[host] = time.Now()
	return true
}

type peekConn struct {
	net.Conn
	buf []byte
	pos int
}

func newPeekConn(conn net.Conn, buf []byte) *peekConn {
	return &peekConn{Conn: conn, buf: buf}
}

func (p *peekConn) Read(b []byte) (n int, err error) {
	if p.pos < len(p.buf) {
		n = copy(b, p.buf[p.pos:])
		p.pos += n
		return n, nil
	}
	return p.Conn.Read(b)
}

// AddMiddleware wraps successive middleware within each other
func AddMiddleware(h http.Handler, middleware ...Middleware) http.Handler {

	for _, m := range slices.Backward(middleware) {
		h = m(h)
	}

	return h
}

func (s *Server) tlsConfigToStd() *stdtls.Config {
	cert, err := stdtls.LoadX509KeyPair(s.tlsConfig.CertFile, s.tlsConfig.KeyFile)
	if err != nil {
		log.Printf("failed to load key pair: %v", err)
		return nil
	}
	return &stdtls.Config{Certificates: []stdtls.Certificate{cert}}
}
func (s *Server) Serve() error {
	caFile := ""
	if s.tlsConfig != nil {
		caFile = s.tlsConfig.CAFile
	}

	srv := &http.Server{
		Addr: s.addr,
		// Silences repeated "client does not trust the local CA" handshake noise;
		// see tls_error_log.go.
		ErrorLog: httpErrorLog(caFile),
		Handler: AddMiddleware(s.mux,
			// Outermost: every request — including the /api/stream, /api/lsp and
			// terminal WebSocket upgrades and the HTTPS redirect — is origin-checked
			// before it reaches a route.
			s.HostPinMiddleware,
			s.RedirectHTTPSMiddleware,
			PanicRecoveryMiddleware,
			NonceMiddleware,
			s.CORSMiddleware,
			s.CSRFMiddleware,
			s.SessionMiddleware,
			CompressMiddleware,
		),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Echo the Host allowlist extras: a 421 through a reverse proxy is almost
	// always a missing entry here, so make the effective value visible at startup.
	if extra := s.effectiveAllowedHosts(); extra != "" {
		log.Printf("[host] accepting extra Host header values: %s", extra)
	}

	if s.tlsConfig != nil {
		log.Printf("Server starting on https://%s (TLS: %s, %s)", s.addr, s.tlsConfig.CertFile, s.tlsConfig.KeyFile)
		if s.tlsConfig.CAFile != "" {
			log.Printf("TLS: import %s into a device's trust store to trust this certificate", s.tlsConfig.CAFile)
		}
		l, err := net.Listen("tcp", s.addr)
		if err != nil {
			return fmt.Errorf("listen: %w", err)
		}
		return srv.Serve(newHybridListener(l, s.tlsConfigToStd(), 0))
	}

	log.Printf("Server starting on http://%s", s.addr)
	return srv.ListenAndServe()
}

func newHybridListener(l net.Listener, stdCfg *stdtls.Config, sniffTimeout time.Duration) *hybridListener {
	h := &hybridListener{
		Listener:     l,
		stdTlsConfig: stdCfg,
		sniffTimeout: sniffTimeout, // zero means defaultSniffTimeout
		drops:        &sniffDropLogger{},
		ready:        make(chan net.Conn, readyQueue),
		done:         make(chan struct{}),
	}
	// A sniff still running when the listener closes lingers for at most sniffTimeout
	// and costs one parked goroutine: closing done has already released Accept, Serve
	// and every queued connection, so there is nothing to wait for.
	go h.acceptLoop()
	return h
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	// Logged before it runs: none of these can be interrupted, so when the shutdown deadline fires
	// the last line here is the only thing that names what hung.
	shutdownStep := func(name string, run func()) {
		if run == nil {
			return
		}
		log.Printf("shutdown: %s", name)
		run()
	}
	// Stop preview document cleanup
	shutdownStep("preview sweeper", func() {
		if s.previewStore != nil {
			s.previewStore.stopSweeper()
		}
	})
	// Stop all LSP connections and sandbox containers
	shutdownStep("lsp handler", func() {
		if s.lspHandler != nil {
			s.lspHandler.Close()
		}
	})
	shutdownStep("extension lsp manager", func() {
		if s.extLSPMgr != nil {
			s.extLSPMgr.Close()
		}
	})
	shutdownStep("extension sandbox manager", func() {
		if s.extSandboxMgr != nil {
			s.extSandboxMgr.Close()
		}
	})
	// Stop sandbox memory sampling, then clean up all task sandbox containers
	shutdownStep("sandbox stats sampler", func() {
		if s.sandboxStatsSampler != nil {
			s.sandboxStatsSampler.Stop()
		}
	})
	shutdownStep("sandbox containers", func() {
		if s.sandboxMgr != nil {
			s.sandboxMgr.Cleanup()
		}
	})
	shutdownStep("login rate limiter", func() {
		if s.loginLimiter != nil {
			s.loginLimiter.Stop()
		}
	})
	// Logged once on the way out: a zero wait count is the evidence the single-connection
	// pool is not the constraint, and a large one is the trigger to widen it.
	stats := s.store.DBStats()
	log.Printf("database pool over this run: %d connection waits totalling %s (open=%d, inUse=%d, maxOpen=%d)",
		stats.WaitCount, stats.WaitDuration.Round(time.Millisecond), stats.OpenConnections, stats.InUse, stats.MaxOpenConnections)
	shutdownStep("database", func() {
		if err := s.store.Close(); err != nil {
			log.Printf("shutdown: closing database: %v", err)
		}
	})
	return nil
}
