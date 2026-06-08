package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"time"
)

// Server is the main HTTP server.
type Server struct {
	store          *Store
	configStore    *ConfigStore
	projectMgr     *ProjectManager
	taskMgr        *TaskManager
	boardMgr       *BoardManager
	boardStreamMgr *BoardStreamManager
	depMgr         *DependencyManager
	reviewMgr      *ReviewManager
	gitMgr         *GitManager
	executor       *TaskExecutor
	homeAgentMgr   *HomeAgentManager
	terminalMgr    *TerminalManager
	mux            *http.ServeMux
	addr           string
}

// NewServer creates a new Server instance.
func NewServer(dbPath, worktreesDir string) (*Server, error) {
	store, err := NewStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("create store: %w", err)
	}

	cfgStore := NewConfigStore(store)
	cfg, err := cfgStore.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("get config: %w", err)
	}

	// Use env var override for worktrees dir
	if wtDir := os.Getenv("WORKTREES_DIR"); wtDir != "" {
		cfg.WorkspaceDir = wtDir
	}

	addr := fmt.Sprintf(":%s", getEnv("PORT", "3000"))

	s := &Server{
		store:       store,
		configStore: cfgStore,
		addr:        addr,
	}

	s.projectMgr = NewProjectManager(store)
	s.boardStreamMgr = NewBoardStreamManager(store)
	s.boardMgr = NewBoardManager(store, s.boardStreamMgr)
	s.terminalMgr = NewTerminalManager(store, cfgStore)
	s.executor = NewTaskExecutor(store, cfgStore, s.boardStreamMgr)
	s.taskMgr = NewTaskManager(store, s.boardStreamMgr, cfgStore, s.terminalMgr, s.executor, s.executor.worktreeMgr)
	s.depMgr = NewDependencyManager(store)
	s.reviewMgr = NewReviewManager(store)
	s.gitMgr = NewGitManager(store)
	s.homeAgentMgr = NewHomeAgentManager(store, cfgStore)

	// Run garbage collector on startup
	dryRun := os.Getenv("KANBAN_GC_DRY_RUN") == "1"
	if removed, err := s.executor.worktreeMgr.GarbageCollect(dryRun); err != nil {
		log.Printf("[gc] startup error: %v", err)
	} else if removed > 0 {
		log.Printf("[gc] startup: %d orphaned worktrees cleaned", removed)
	}

	s.setupRoutes()

	return s, nil
}

func (s *Server) setupRoutes() {
	s.mux = http.NewServeMux()

	// Health check
	s.mux.HandleFunc("/api/health", s.handleHealth)

	// Config
	s.mux.HandleFunc("/api/config/test-connection", s.configStore.HandleTestConnection)
	s.mux.HandleFunc("/api/config/fetch-models", s.configStore.HandleFetchModels)
	s.mux.HandleFunc("/api/config", s.configStore.HandleConfig)

	// Test override (in-memory, not persisted)
	s.mux.HandleFunc("/api/test/override", s.configStore.HandleTestOverride)

	// Test database reset (clears all data except config)
	s.mux.HandleFunc("/api/test/reset", s.handleTestReset)

	// Test: create an orphaned worktree directory (for e2e GC testing)
	s.mux.HandleFunc("/api/test/create-orphan", s.handleCreateOrphan)

	// Garbage collector (manual trigger)
	s.mux.HandleFunc("/api/gc", s.handleGC)

	// Filesystem browser
	s.mux.HandleFunc("/api/fs", s.handleFS)

	// Projects
	s.mux.HandleFunc("/api/projects", s.handleProjects)
	s.mux.HandleFunc("/api/projects/", s.handleProjectScoped)

	// Serve built frontend (if available)
	frontendDist := getEnv("FRONTEND_DIST", "./frontend/dist")
	if _, err := os.Stat(frontendDist); err == nil {
		sub, err := fs.Sub(os.DirFS(frontendDist), ".")
		if err == nil {
			s.mux.Handle("/", http.FileServer(http.FS(sub)))
		}
	}
}

func (s *Server) Serve() error {
	srv := &http.Server{
		Addr:    s.addr,
		Handler: panicRecoveryMiddleware(corsMiddleware(s.mux)),
	}
	log.Printf("Server starting on %s", s.addr)
	return srv.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.store.Close()
}

// -- Helper functions --

func jsonResponse(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func jsonErrorf(w http.ResponseWriter, status int, format string, args ...interface{}) {
	jsonError(w, status, fmt.Sprintf(format, args...))
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func panicRecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("[PANIC] %s %s: %v\n%s", r.Method, r.URL.Path, err, debug.Stack())
				jsonError(w, http.StatusInternalServerError, fmt.Sprintf("internal server error: %v", err))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// -- Path parameter extraction --

// pathRegex patterns for routing.
var (
	reProjectByID     = regexp.MustCompile(`^/api/projects/([^/]+)$`)
	reProjectBoard    = regexp.MustCompile(`^/api/projects/([^/]+)/board$`)
	reProjectBoardWS  = regexp.MustCompile(`^/api/projects/([^/]+)/board/stream$`)
	reProjectTasks    = regexp.MustCompile(`^/api/projects/([^/]+)/tasks$`)
	reProjectTaskByID = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)$`)
	reTaskMove        = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/move$`)
	reTaskStart       = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/start$`)
	reTaskStop        = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/stop$`)
	reTaskRetry       = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/retry$`)
	reTaskResume      = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/resume$`)
	reTaskStream      = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/stream$`)
	reTaskDiff        = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/diff$`)
	reTaskMessages    = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/messages$`)
	reTaskMessageByID = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/messages/([^/]+)$`)
	reTaskDeps        = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/dependencies$`)
	reTaskDepChild    = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/dependencies/([^/]+)$`)
	reTaskDependents  = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/dependents$`)
	reTaskReview      = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/review-comments$`)
	reTaskReviewByID  = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/review-comments/([^/]+)$`)
	reTaskReorder     = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/reorder$`)
	reProjectGitLog   = regexp.MustCompile(`^/api/projects/([^/]+)/git/log$`)
	reProjectGitBranches = regexp.MustCompile(`^/api/projects/([^/]+)/git/branches$`)
	reProjectGitDiff  = regexp.MustCompile(`^/api/projects/([^/]+)/git/diff$`)
	reProjectGitStatus = regexp.MustCompile(`^/api/projects/([^/]+)/git/status$`)
	reProjectGitCheckout = regexp.MustCompile(`^/api/projects/([^/]+)/git/checkout$`)
	reProjectGitPull  = regexp.MustCompile(`^/api/projects/([^/]+)/git/pull$`)
	reProjectGitPush  = regexp.MustCompile(`^/api/projects/([^/]+)/git/push$`)
	reProjectHomeAgentMsg = regexp.MustCompile(`^/api/projects/([^/]+)/home-agent/message$`)
	reProjectHomeAgentMsgs = regexp.MustCompile(`^/api/projects/([^/]+)/home-agent/messages$`)
	reProjectHomeAgentStream = regexp.MustCompile(`^/api/projects/([^/]+)/home-agent/stream$`)
	reProjectTerminal     = regexp.MustCompile(`^/api/projects/([^/]+)/terminal$`)
	reTaskTerminal        = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/terminal$`)
	reTaskWorktreeExists = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/worktree-exists$`)
)

func (s *Server) handleTaskMessages(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method == http.MethodDelete || r.Method == http.MethodPost {
		// DELETE and POST both delete the message and all messages after it (regenerate)
		var req struct {
			MessageID string `json:"message_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.MessageID == "" {
			jsonError(w, http.StatusBadRequest, "message_id required")
			return
		}
		if err := s.store.DeleteTaskMessagesAfter(taskID, req.MessageID); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	messages, err := s.store.GetTaskMessages(taskID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, messages)
}

func (s *Server) handleTaskMessageByID(w http.ResponseWriter, r *http.Request, projectID, taskID, messageID string) {
	if r.Method == http.MethodPatch {
		var req struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Content == "" {
			jsonError(w, http.StatusBadRequest, "content required")
			return
		}
		if err := s.store.UpdateTaskMessage(messageID, req.Content); err != nil {
			jsonError(w, http.StatusInternalServerError, err.Error())
			return
		}
		jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleTestReset clears all data except config. Used by e2e tests for a clean slate.
func (s *Server) handleTestReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.store.ResetData(); err != nil {
		log.Printf("[test-reset] failed to reset data: %v", err)
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Reset executors and streams
	s.executor.ResetForTest()
	jsonResponse(w, http.StatusOK, map[string]string{"status": "reset"})
}

// handleGC triggers the garbage collector manually.
func (s *Server) handleGC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	removed, err := s.executor.worktreeMgr.GarbageCollect(false)
	if err != nil {
		log.Printf("[gc] manual trigger error: %v", err)
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, map[string]interface{}{"removed": removed})
}

// handleCreateOrphan creates a fake orphaned worktree directory for e2e testing.
func (s *Server) handleCreateOrphan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg, err := s.configStore.GetConfig()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "failed to get config")
		return
	}
	worktreesDir := cfg.WorkspaceDir
	if wtDir := os.Getenv("WORKTREES_DIR"); wtDir != "" {
		worktreesDir = wtDir
	}
	// Create a uniquely named orphan directory
	orphanName := fmt.Sprintf("orphan-test-%d", time.Now().UnixNano())
	orphanPath := filepath.Join(worktreesDir, orphanName)
	if err := os.MkdirAll(orphanPath, 0o755); err != nil {
		jsonError(w, http.StatusInternalServerError, fmt.Sprintf("create orphan dir: %v", err))
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"orphan": orphanName, "path": orphanPath})
}

// handleFS lists directory contents for the file browser.
func (s *Server) handleFS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		path = string(os.PathSeparator)
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}

	type fsEntry struct {
		Name      string `json:"name"`
		IsDir     bool   `json:"isDir"`
		Path      string `json:"path"`
		IsGitRepo bool   `json:"isGitRepo"`
	}

	// Check if this is a git repo
	isGit := isGitRepo(path)

	result := struct {
		Path      string    `json:"path"`
		IsGitRepo bool      `json:"isGitRepo"`
		Entries   []fsEntry `json:"entries"`
	}{
		Path:      path,
		IsGitRepo: isGit,
		Entries:   []fsEntry{},
	}

	// Add parent directory entry if not at root
	if path != string(os.PathSeparator) {
		if parent := filepath.Dir(path); parent != path {
			result.Entries = append(result.Entries, fsEntry{Name: "..", IsDir: true, Path: parent, IsGitRepo: isGitRepo(parent)})
		}
	}

	for _, e := range entries {
		// Skip hidden entries and files
		if e.Name()[0] == '.' || !e.IsDir() {
			continue
		}
		fullPath := filepath.Join(path, e.Name())
		result.Entries = append(result.Entries, fsEntry{
			Name:      e.Name(),
			IsDir:     e.IsDir(),
			Path:      fullPath,
			IsGitRepo: isGitRepo(fullPath),
		})
	}

	// Sort entries alphabetically (case-insensitive), keeping ".." first
	sort.SliceStable(result.Entries, func(i, j int) bool {
		return strings.ToLower(result.Entries[i].Name) < strings.ToLower(result.Entries[j].Name)
	})

	jsonResponse(w, http.StatusOK, result)
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path != "/api/projects" {
		s.handleProjectScoped(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.projectMgr.HandleList(w, r)
	case http.MethodPost:
		s.projectMgr.HandleCreate(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleProjectScoped(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// Project counts (must check before reProjectByID, which matches /api/projects/counts)
	if path == "/api/projects/counts" {
		s.projectMgr.HandleCounts(w, r)
		return
	}

	// Project CRUD
	if m := reProjectByID.FindStringSubmatch(path); m != nil {
		s.projectMgr.HandleByID(w, r, m[1])
		return
	}

	// Board stream (WebSocket)
	if m := reProjectBoardWS.FindStringSubmatch(path); m != nil {
		s.boardStreamMgr.HandleStream(w, r, m[1])
		return
	}

	// Board
	if m := reProjectBoard.FindStringSubmatch(path); m != nil {
		s.boardMgr.HandleGetBoard(w, r, m[1])
		return
	}

	// Task reorder
	if m := reTaskReorder.FindStringSubmatch(path); m != nil {
		s.boardMgr.HandleReorder(w, r, m[1])
		return
	}

	// Task CRUD
	if m := reProjectTasks.FindStringSubmatch(path); m != nil {
		s.taskMgr.HandleList(w, r, m[1])
		return
	}

	// Task dependencies
	if m := reTaskDeps.FindStringSubmatch(path); m != nil {
		s.depMgr.HandleDependencies(w, r, m[1], m[2])
		return
	}
	if m := reTaskDepChild.FindStringSubmatch(path); m != nil {
		s.depMgr.HandleDependency(w, r, m[1], m[2], m[3])
		return
	}

	// Task dependents
	if m := reTaskDependents.FindStringSubmatch(path); m != nil {
		s.depMgr.HandleDependents(w, r, m[1], m[2])
		return
	}

	// Review comments
	if m := reTaskReview.FindStringSubmatch(path); m != nil {
		s.reviewMgr.HandleComments(w, r, m[1], m[2])
		return
	}
	if m := reTaskReviewByID.FindStringSubmatch(path); m != nil {
		s.reviewMgr.HandleComment(w, r, m[1], m[2], m[3])
		return
	}

	// Task execution
	if m := reTaskStream.FindStringSubmatch(path); m != nil {
		s.executor.HandleStream(w, r, m[1], m[2])
		return
	}
	if m := reTaskDiff.FindStringSubmatch(path); m != nil {
		s.gitMgr.HandleTaskDiff(w, r, m[1], m[2])
		return
	}

	// Task messages
	if m := reTaskMessageByID.FindStringSubmatch(path); m != nil {
		s.handleTaskMessageByID(w, r, m[1], m[2], m[3])
		return
	}
	if m := reTaskMessages.FindStringSubmatch(path); m != nil {
		s.handleTaskMessages(w, r, m[1], m[2])
		return
	}

	// Task move
	if m := reTaskMove.FindStringSubmatch(path); m != nil {
		s.taskMgr.HandleMove(w, r, m[1], m[2])
		return
	}

	// Task start/stop/retry/resume
	if m := reTaskStart.FindStringSubmatch(path); m != nil {
		s.executor.HandleStart(w, r, m[1], m[2])
		return
	}
	if m := reTaskStop.FindStringSubmatch(path); m != nil {
		s.executor.HandleStop(w, r, m[1], m[2])
		return
	}
	if m := reTaskRetry.FindStringSubmatch(path); m != nil {
		s.executor.HandleRetry(w, r, m[1], m[2])
		return
	}
	if m := reTaskResume.FindStringSubmatch(path); m != nil {
		s.executor.HandleResume(w, r, m[1], m[2])
		return
	}

	// Task by ID (must be after move/start/stop etc.)
	if m := reProjectTaskByID.FindStringSubmatch(path); m != nil {
		s.taskMgr.HandleByID(w, r, m[1], m[2])
		return
	}

	// Git endpoints
	if m := reProjectGitLog.FindStringSubmatch(path); m != nil {
		s.gitMgr.HandleLog(w, r, m[1])
		return
	}
	if m := reProjectGitBranches.FindStringSubmatch(path); m != nil {
		s.gitMgr.HandleBranches(w, r, m[1])
		return
	}
	if m := reProjectGitDiff.FindStringSubmatch(path); m != nil {
		s.gitMgr.HandleDiff(w, r, m[1])
		return
	}
	if m := reProjectGitStatus.FindStringSubmatch(path); m != nil {
		s.gitMgr.HandleStatus(w, r, m[1])
		return
	}
	if m := reProjectGitCheckout.FindStringSubmatch(path); m != nil {
		s.gitMgr.HandleCheckout(w, r, m[1])
		return
	}
	if m := reProjectGitPull.FindStringSubmatch(path); m != nil {
		s.gitMgr.HandlePull(w, r, m[1])
		return
	}
	if m := reProjectGitPush.FindStringSubmatch(path); m != nil {
		s.gitMgr.HandlePush(w, r, m[1])
		return
	}

	// Home agent
	if m := reProjectHomeAgentMsg.FindStringSubmatch(path); m != nil {
		s.homeAgentMgr.HandleMessage(w, r, m[1])
		return
	}
	if m := reProjectHomeAgentMsgs.FindStringSubmatch(path); m != nil {
		s.homeAgentMgr.HandleMessages(w, r, m[1])
		return
	}
	if m := reProjectHomeAgentStream.FindStringSubmatch(path); m != nil {
		s.homeAgentMgr.HandleStream(w, r, m[1])
		return
	}

	// Worktree exists check
	if m := reTaskWorktreeExists.FindStringSubmatch(path); m != nil {
		s.handleWorktreeExists(w, r, m[1], m[2])
		return
	}

	// Task terminal (persistent, per-task)
	if m := reTaskTerminal.FindStringSubmatch(path); m != nil {
		s.terminalMgr.HandleTerminal(w, r, m[1], m[2])
		return
	}

	// Project terminal (persistent)
	if m := reProjectTerminal.FindStringSubmatch(path); m != nil {
		s.terminalMgr.HandleProjectTerminal(w, r, m[1])
		return
	}

	http.NotFound(w, r)
}

// -- Worktree exists handler --

func (s *Server) handleWorktreeExists(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cfg, err := s.configStore.GetConfig()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "failed to get config")
		return
	}

	worktreePath := filepath.Join(cfg.WorkspaceDir, taskID)
	_, err = os.Stat(worktreePath)
	exists := err == nil

	jsonResponse(w, http.StatusOK, map[string]bool{"exists": exists})
}

