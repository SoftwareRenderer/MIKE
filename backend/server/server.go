package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	stdtls "crypto/tls"
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
	"kanban/backend/lsp"
	"kanban/backend/managers"
	"kanban/backend/sandbox"
	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/term"
	"kanban/backend/tls"
	"kanban/backend/types"
)

// Server is the main HTTP server.
type Server struct {
	store          *store.Store
	configStore    *config.ConfigStore
	sessionMgr     *auth.SessionManager
	projectHandler *managers.ProjectHandler
	taskHandler    *managers.TaskHandler
	boardHandler   *managers.BoardHandler
	appStreamMgr   *stream.AppStreamManager
	depHandler     *managers.DependencyHandler
	reviewHandler  *managers.ReviewHandler
	gitHandler     *managers.GitHandler
	executor       *kanbanexec.TaskExecutor
	terminalMgr    *term.TerminalManager
	sandboxMgr     *sandbox.SandboxManager
	agentRegistry  *agent.AgentRegistry
	extStore       *extensions.ExtensionStore
	extSandboxMgr  *extensions.ExtensionSandboxManager
	extHandler     *extensions.ExtensionHandler
	extLSPMgr      *lsp.ExtensionLSPManager
	lspHandler     *lsp.LSPHandler
	loginLimiter   *RateLimiter
	mux            *http.ServeMux
	addr           string
	frontendDist   string
	tlsConfig      *tls.Config

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
func NewServer(dbPath, worktreesDir, addr, frontendDist string, tlsCfg *tls.Config) (*Server, error) {
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

	if wtDir := os.Getenv("MIKE_WORKTREES_DIR"); wtDir != "" {
		cfg.WorkspaceDir = wtDir
	}

	s := &Server{
		store:        store,
		configStore:  cfgStore,
		addr:         addr,
		frontendDist: frontendDist,
		tlsConfig:    tlsCfg,
		worktreeMux:  make(map[string]*sync.Mutex),
		projectMux:   make(map[string]*sync.Mutex),
		loginLimiter: newLoginRateLimiter(),
	}

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
	s.boardHandler = managers.NewBoardHandler(store, s.appStreamMgr, cfgStore, s.sandboxMgr)
	s.terminalMgr = term.NewTerminalManager(store, cfgStore, s.sandboxMgr, nil) // agentRegistry wired after creation
	// Wire port assignment callback → store + WebSocket broadcast
	s.terminalMgr.SetOnPortsAssigned(func(taskID, projectID string, ports []types.AssignedPort) {
		task, err := store.GetTaskByTaskID(taskID)
		if err != nil {
			log.Printf("[terminal] failed to get task %s for port assignment: %v", taskID, err)
			return
		}
		task.AssignedPorts = ports
		_ = store.UpdateTask(task)
		s.appStreamMgr.BroadcastTaskUpdated(projectID, task)
		s.appStreamMgr.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypePorts, Data: types.StreamPortsEvent{TaskID: taskID, Ports: ports}})
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

	s.executor = kanbanexec.NewTaskExecutor(store, cfgStore, s.appStreamMgr, worktreeMgr, s.sandboxMgr, agentRegistry, s.extLSPMgr, commitQueue)
	// Wire agent registry to terminal manager (created earlier before agentRegistry was available)
	s.terminalMgr.SetAgentRegistry(agentRegistry)
	s.projectHandler = managers.NewProjectHandler(store, cfgStore, worktreeMgr, s.terminalMgr, s.sandboxMgr, s.executor)
	s.taskHandler = managers.NewTaskHandler(store, s.appStreamMgr, cfgStore, s.terminalMgr, s.executor, worktreeMgr, commitQueue, s.sandboxMgr)
	s.depHandler = managers.NewDependencyHandler(store)
	s.reviewHandler = managers.NewReviewHandler(store)
	s.gitHandler = managers.NewGitHandler(store, s.executor.WorktreeMgr())
	// Wire worktree mutex so GitHandler can serialize per-task git operations.
	s.gitHandler.SetWorktreeMutex(s.getWorktreeMutex)

	// Bootstrap built-in extensions
	// (Removed built-in bootstrapping to decouple extensions from core)
	s.extHandler = extensions.NewExtensionHandler(s.extStore, s.extSandboxMgr, extRegistry, &extensions.ExtensionConfig{
		RegistryURL:  cfg.RegistryURL,
		WorkspaceDir: cfg.WorkspaceDir,
	})

	// LSP handler for language server WebSocket bridging
	// Use extension-based registry and manager for dynamic LSP discovery
	extLSPRegistry := lsp.NewExtensionRegistry(s.extStore)
	s.lspHandler = lsp.NewLSPHandler(extLSPRegistry, s.extLSPMgr)
	// Wire extension change callback → reload agent registry + broadcast to all clients
	s.extHandler.SetOnExtensionChanged(func() {
		agentRegistry.Reload()
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.JSONError(w, http.StatusBadRequest, "invalid request body")
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

		isHTTPS := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
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

// hybridListener handles both TLS and plain HTTP on the same port.
type hybridListener struct {
	net.Listener
	stdTlsConfig *stdtls.Config
}

func (l *hybridListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}

	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err != nil {
		conn.Close()
		return nil, err
	}

	if buf[0] == 0x16 {
		return stdtls.Server(newPeekConn(conn, buf), l.stdTlsConfig), nil
	}

	return newPeekConn(conn, buf), nil
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
	srv := &http.Server{
		Addr: s.addr,
		Handler: AddMiddleware(s.mux,
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

	if s.tlsConfig != nil {
		log.Printf("Server starting on https://%s (TLS: %s, %s)", s.addr, s.tlsConfig.CertFile, s.tlsConfig.KeyFile)
		l, err := net.Listen("tcp", s.addr)
		if err != nil {
			return fmt.Errorf("listen: %w", err)
		}
		return srv.Serve(newHybridListener(l, s.tlsConfigToStd()))
	}

	log.Printf("Server starting on http://%s", s.addr)
	return srv.ListenAndServe()
}

func newHybridListener(l net.Listener, stdCfg *stdtls.Config) *hybridListener {
	return &hybridListener{Listener: l, stdTlsConfig: stdCfg}
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	// Stop all LSP connections and sandbox containers
	if s.lspHandler != nil {
		s.lspHandler.Close()
	}
	if s.extLSPMgr != nil {
		s.extLSPMgr.Close()
	}
	if s.extSandboxMgr != nil {
		s.extSandboxMgr.Close()
	}
	// Clean up all task sandbox containers
	if s.sandboxMgr != nil {
		s.sandboxMgr.Cleanup()
	}
	if s.loginLimiter != nil {
		s.loginLimiter.Stop()
	}
	return s.store.Close()
}
