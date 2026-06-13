package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"kanban/backend/config"
	"kanban/backend/exec"
	"kanban/backend/git"
	"kanban/backend/managers"
	"kanban/backend/stream"
	"kanban/backend/store"
	"kanban/backend/term"
)

// Server is the main HTTP server.
type Server struct {
	store          *store.Store
	configStore    *config.ConfigStore
	projectHandler *managers.ProjectHandler
	taskHandler    *managers.TaskHandler
	boardHandler   *managers.BoardHandler
	boardStreamMgr *stream.BoardStreamManager
	depHandler     *managers.DependencyHandler
	reviewHandler  *managers.ReviewHandler
	gitHandler     *managers.GitHandler
	executor       *exec.TaskExecutor
	homeAgentMgr   *managers.HomeAgentHandler
	terminalMgr    *term.TerminalManager
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

	worktreeMgr := git.NewWorktreeManager(store, cfgStore)
	s.boardStreamMgr = stream.NewBoardStreamManager(store, worktreeMgr)
	s.boardHandler = managers.NewBoardHandler(store, s.boardStreamMgr)
	s.terminalMgr = term.NewTerminalManager(store, cfgStore)
	s.projectHandler = managers.NewProjectHandler(store, cfgStore, worktreeMgr, s.terminalMgr)
	s.executor = exec.NewTaskExecutor(store, cfgStore, s.boardStreamMgr, worktreeMgr)
	s.taskHandler = managers.NewTaskHandler(store, s.boardStreamMgr, cfgStore, s.terminalMgr, s.executor, worktreeMgr)
	s.depHandler = managers.NewDependencyHandler(store)
	s.reviewHandler = managers.NewReviewHandler(store)
	s.gitHandler = managers.NewGitHandler(store, s.executor.WorktreeMgr())
	s.homeAgentMgr = managers.NewHomeAgentHandler(store, cfgStore)

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
		Handler:           PanicRecoveryMiddleware(CORSMiddleware(CompressMiddleware(s.mux))),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Printf("Server starting on %s", s.addr)
	return srv.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.store.Close()
}
