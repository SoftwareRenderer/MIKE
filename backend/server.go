package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/creack/pty"
	"github.com/coder/websocket"
)

// Server is the main HTTP server.
type Server struct {
	store        *Store
	configStore  *ConfigStore
	projectMgr   *ProjectManager
	taskMgr      *TaskManager
	boardMgr     *BoardManager
	depMgr       *DependencyManager
	reviewMgr    *ReviewManager
	gitMgr       *GitManager
	executor     *TaskExecutor
	homeAgentMgr *HomeAgentManager
	mux          *http.ServeMux
	addr         string
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
	s.boardMgr = NewBoardManager(store)
	s.taskMgr = NewTaskManager(store)
	s.depMgr = NewDependencyManager(store)
	s.reviewMgr = NewReviewManager(store)
	s.gitMgr = NewGitManager(store)
	s.executor = NewTaskExecutor(store, cfgStore)
	s.homeAgentMgr = NewHomeAgentManager(store, cfgStore)

	s.setupRoutes()

	return s, nil
}

func (s *Server) setupRoutes() {
	s.mux = http.NewServeMux()

	// Health check
	s.mux.HandleFunc("/api/health", s.handleHealth)

	// Config
	s.mux.HandleFunc("/api/config", s.configStore.HandleConfig)

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
		Handler: corsMiddleware(s.mux),
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

// -- Path parameter extraction --

// pathRegex patterns for routing.
var (
	reProjectByID     = regexp.MustCompile(`^/api/projects/([^/]+)$`)
	reProjectBoard    = regexp.MustCompile(`^/api/projects/([^/]+)/board$`)
	reProjectTasks    = regexp.MustCompile(`^/api/projects/([^/]+)/tasks$`)
	reProjectTaskByID = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)$`)
	reTaskMove        = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/move$`)
	reTaskStart       = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/start$`)
	reTaskStop        = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/stop$`)
	reTaskRetry       = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/retry$`)
	reTaskResume      = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/resume$`)
	reTaskStream      = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/stream$`)
	reTaskDiff        = regexp.MustCompile(`^/api/projects/([^/]+)/tasks/([^/]+)/diff$`)
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
	reProjectTerminal = regexp.MustCompile(`^/api/projects/([^/]+)/terminal$`)
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
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

	// Project CRUD
	if m := reProjectByID.FindStringSubmatch(path); m != nil {
		s.projectMgr.HandleByID(w, r, m[1])
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

	// Terminal
	if m := reProjectTerminal.FindStringSubmatch(path); m != nil {
		s.handleTerminal(w, r, m[1])
		return
	}

	http.NotFound(w, r)
}

// -- Terminal handler --

func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request, projectID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	project, err := s.store.GetProject(projectID)
	if err != nil {
		jsonError(w, http.StatusNotFound, "project not found")
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Printf("terminal accept error: %v", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "closed")

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell)
	cmd.Dir = project.Path
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		log.Printf("terminal pty error: %v", err)
		return
	}
	defer ptmx.Close()

	sz := &pty.Winsize{Rows: 40, Cols: 120}

	ctx := r.Context()

	// PTY → WebSocket
	go func() {
		buf := make([]byte, 32768)
		for {
			n, err := ptmx.Read(buf)
			if err != nil {
				return
			}
			msg := TerminalMessage{Type: "output", Data: string(buf[:n])}
			data, _ := json.Marshal(msg)
			if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
				return
			}
		}
	}()

	// WebSocket → PTY
	for {
		msgType, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		if msgType != websocket.MessageText {
			continue
		}
		var tm TerminalMessage
		if err := json.Unmarshal(data, &tm); err != nil {
			continue
		}
		switch tm.Type {
		case "input":
			ptmx.Write([]byte(tm.Data))
		case "resize":
			sz.Rows = uint16(tm.Rows)
			sz.Cols = uint16(tm.Cols)
			if err := pty.Setsize(ptmx, sz); err != nil {
				log.Printf("terminal resize error: %v", err)
			}
		}
	}
}