package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"kanban/backend/auth"
	"kanban/backend/config"
	"kanban/backend/exec"
	"kanban/backend/extensions"
	"kanban/backend/git"
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
	store             *store.Store
	configStore       *config.ConfigStore
	sessionMgr        *auth.SessionManager
	projectHandler    *managers.ProjectHandler
	taskHandler       *managers.TaskHandler
	boardHandler      *managers.BoardHandler
	appStreamMgr      *stream.AppStreamManager
	depHandler        *managers.DependencyHandler
	reviewHandler     *managers.ReviewHandler
	gitHandler        *managers.GitHandler
	executor          *exec.TaskExecutor
	terminalMgr       *term.TerminalManager
	sandboxMgr        *sandbox.SandboxManager
	extStore          *extensions.ExtensionStore
	extSandboxMgr     *extensions.ExtensionSandboxManager
	extHandler        *extensions.ExtensionHandler
	extLSPMgr         *lsp.ExtensionLSPManager
	lspHandler        *lsp.LSPHandler
	mux               *http.ServeMux
	addr              string
	frontendDist      string
	tlsConfig         *tls.Config

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

	cfgStore := config.NewConfigStore(store, dbPath)
	cfg, err := cfgStore.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("get config: %w", err)
	}

	if wtDir := os.Getenv("WORKTREES_DIR"); wtDir != "" {
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
	}

	// Create session manager — fails if KANBAN_PASSWORD is not set
	sessionMgr, err := auth.NewSessionManager(store)
	if err != nil {
		return nil, fmt.Errorf("create session manager: %w\nSet KANBAN_PASSWORD environment variable to enable authentication", err)
	}
	s.sessionMgr = sessionMgr

	// Configure cookie Secure flag.
	// Auto-enable when TLS is active; also respect explicit env var for reverse proxy setups.
	sessionCookieSecure = tlsCfg != nil || os.Getenv("KANBAN_SECURE_COOKIES") == "1"

	worktreeMgr := git.NewWorktreeManager(store, cfgStore)
	s.appStreamMgr = stream.NewAppStreamManager(store, worktreeMgr)
	s.sandboxMgr = sandbox.NewSandboxManager()
	s.boardHandler = managers.NewBoardHandler(store, s.appStreamMgr, cfgStore, s.sandboxMgr)
	s.terminalMgr = term.NewTerminalManager(store, cfgStore, s.sandboxMgr)
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
	commitQueue := exec.NewCommitQueue()
	s.executor = exec.NewTaskExecutor(store, cfgStore, s.appStreamMgr, worktreeMgr, s.sandboxMgr, commitQueue)
	s.projectHandler = managers.NewProjectHandler(store, cfgStore, worktreeMgr, s.terminalMgr, s.sandboxMgr, s.executor)
	s.taskHandler = managers.NewTaskHandler(store, s.appStreamMgr, cfgStore, s.terminalMgr, s.executor, worktreeMgr, commitQueue, s.sandboxMgr)
	s.depHandler = managers.NewDependencyHandler(store)
	s.reviewHandler = managers.NewReviewHandler(store)
	s.gitHandler = managers.NewGitHandler(store, s.executor.WorktreeMgr())
	// Wire worktree mutex so GitHandler can serialize per-task git operations.
	s.gitHandler.SetWorktreeMutex(s.getWorktreeMutex)

	// Extension system
	s.extStore = extensions.NewExtensionStore(store)
	if err := s.extStore.InitSchema(); err != nil {
		return nil, fmt.Errorf("init extension schema: %w", err)
	}

	// Bootstrap built-in extensions
	// (Removed built-in bootstrapping to decouple extensions from core)
	extRegistry := extensions.NewRegistryClient()
	s.extSandboxMgr = extensions.NewExtensionSandboxManager(extRegistry, s.extStore)
	s.extHandler = extensions.NewExtensionHandler(s.extStore, s.extSandboxMgr, extRegistry, &extensions.ExtensionConfig{
		RegistryURL:  cfg.RegistryURL,
		WorkspaceDir: cfg.WorkspaceDir,
	})

	// LSP handler for language server WebSocket bridging
	// Use extension-based registry and manager for dynamic LSP discovery
	extLSPRegistry := lsp.NewExtensionRegistry(s.extStore)
	s.extLSPMgr = lsp.NewExtensionLSPManager(s.extStore, s.extSandboxMgr, store)
	s.lspHandler = lsp.NewLSPHandler(extLSPRegistry, s.extLSPMgr)
	// Wire extension update callback → broadcast LSP reset to frontend
	s.extHandler.SetOnImageUpdated(func(extID string) {
		s.lspHandler.BroadcastLSPReset()
	})
	// Wire project mutex so GitHandler can serialize project-level git operations.
	s.gitHandler.SetProjectMutex(s.getProjectMutex)
	// Wire worktree watcher -> GitHandler diff cache so on-demand file fetches work
	s.appStreamMgr.SetDiffCacheUpdate(s.gitHandler.UpdateDiffCache)

	// Run garbage collector on startup
	dryRun := os.Getenv("KANBAN_GC_DRY_RUN") == "1"
	if removed, err := s.executor.WorktreeMgr().GarbageCollect(dryRun); err != nil {
		log.Printf("[gc] startup error: %v", err)
	} else if removed > 0 {
		log.Printf("[gc] startup: %d orphaned worktrees cleaned", removed)
	}

	// Clear stale title_generating flags from previous server sessions
	if err := store.ClearStaleTitleGenerating(); err != nil {
		log.Printf("[title-gen] startup: failed to clear stale flags: %v", err)
	}

	s.setupRoutes()

	return s, nil
}

// Serve starts the HTTP server.
func (s *Server) Serve() error {
	srv := &http.Server{
		Addr:              s.addr,
		Handler:           PanicRecoveryMiddleware(CORSMiddleware(s.SessionMiddleware(s.CSRFMiddleware(CompressMiddleware(s.mux))))),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	if s.tlsConfig != nil {
		log.Printf("Server starting on https://%s (TLS: %s, %s)", s.addr, s.tlsConfig.CertFile, s.tlsConfig.KeyFile)
		return srv.ListenAndServeTLS(s.tlsConfig.CertFile, s.tlsConfig.KeyFile)
	}

	log.Printf("Server starting on http://%s", s.addr)
	return srv.ListenAndServe()
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
	return s.store.Close()
}

