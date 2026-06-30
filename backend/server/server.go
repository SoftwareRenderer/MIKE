package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	bkg "kanban/backend"
	"kanban/backend/auth"
	"kanban/backend/config"
	"kanban/backend/exec"
	"kanban/backend/git"
	"kanban/backend/managers"
	"kanban/backend/sandbox"
	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/term"
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
	executor       *exec.TaskExecutor
	terminalMgr    *term.TerminalManager
	sandboxMgr     *sandbox.SandboxManager
	mux            *http.ServeMux
	addr           string
}

// NewServer creates a new Server instance.
func NewServer(dbPath, worktreesDir string) (*Server, error) {
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

	addr := fmt.Sprintf(":%s", GetEnv("PORT", "3000"))

	s := &Server{
		store:       store,
		configStore: cfgStore,
		addr:        addr,
	}

	// Create session manager — fails if KANBAN_PASSWORD is not set
	sessionMgr, err := auth.NewSessionManager(store)
	if err != nil {
		return nil, fmt.Errorf("create session manager: %w\nSet KANBAN_PASSWORD environment variable to enable authentication", err)
	}
	s.sessionMgr = sessionMgr

	// Configure cookie Secure flag based on bind address.
	// Secure flag requires HTTPS — for local dev (HTTP), keep it off.
	// Enable only when explicitly behind a reverse proxy with HTTPS.
	sessionCookieSecure = os.Getenv("KANBAN_SECURE_COOKIES") == "1"

	worktreeMgr := git.NewWorktreeManager(store, cfgStore)
	s.appStreamMgr = stream.NewAppStreamManager(store, worktreeMgr)
	s.boardHandler = managers.NewBoardHandler(store, s.appStreamMgr, cfgStore)
	s.sandboxMgr = sandbox.NewSandboxManager()
	s.terminalMgr = term.NewTerminalManager(store, cfgStore, s.sandboxMgr)
	s.executor = exec.NewTaskExecutor(store, cfgStore, s.appStreamMgr, worktreeMgr, s.sandboxMgr)
	s.projectHandler = managers.NewProjectHandler(store, cfgStore, worktreeMgr, s.terminalMgr, s.sandboxMgr, s.executor)
	s.taskHandler = managers.NewTaskHandler(store, s.appStreamMgr, cfgStore, s.terminalMgr, s.executor, worktreeMgr, bkg.NewCommitQueue())
	s.depHandler = managers.NewDependencyHandler(store)
	s.reviewHandler = managers.NewReviewHandler(store)
	s.gitHandler = managers.NewGitHandler(store, s.executor.WorktreeMgr())
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
	log.Printf("Server starting on %s", s.addr)
	return srv.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	// Clean up all sandbox containers
	if s.sandboxMgr != nil {
		s.sandboxMgr.Cleanup()
	}
	return s.store.Close()
}
