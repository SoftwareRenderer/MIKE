package term

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"kanban/backend/config"
	"kanban/backend/httputil"
	"kanban/backend/sandbox"
	"kanban/backend/store"
	"kanban/backend/types"

	"github.com/creack/pty"
	"github.com/coder/websocket"
)

const (
	// TerminalBufferSize is the max number of output frames to keep when no clients are connected.
	TerminalBufferSize = 4096
)

// taskTerminal holds a persistent PTY session for a single task.
type taskTerminal struct {
	cmd         *exec.Cmd
	ptmx        *os.File
	shell       *sandbox.ShellProcess // non-nil when running in sandbox
	mu          sync.Mutex
	conns       map[*websocket.Conn]bool
	buf         [][]byte
	lastActive  time.Time
	onExit      func() // called when shell process exits, to remove session from manager
}

// TerminalManager manages per-task persistent terminal sessions.
type TerminalManager struct {
	mu         sync.RWMutex
	sessions   map[string]*taskTerminal
	store      *store.Store
	cfgStore   *config.ConfigStore
	sandboxMgr *sandbox.SandboxManager
}

// NewTerminalManager creates a new TerminalManager.
func NewTerminalManager(store *store.Store, cfgStore *config.ConfigStore, sandboxMgr *sandbox.SandboxManager) *TerminalManager {
	return &TerminalManager{
		sessions:   make(map[string]*taskTerminal),
		store:      store,
		cfgStore:   cfgStore,
		sandboxMgr: sandboxMgr,
	}
}

// getOrCreate returns an existing session or creates a new one for the task.
// projectID is used to check if sandbox is enabled for the project.
func (tm *TerminalManager) getOrCreate(sessionKey string, taskID string, worktreePath string, projectID string) (*taskTerminal, error) {
	tm.mu.RLock()
	session, ok := tm.sessions[sessionKey]
	tm.mu.RUnlock()
	if ok {
		return session, nil
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()

	if session, ok = tm.sessions[sessionKey]; ok {
		return session, nil
	}

	// Check if sandbox is enabled for this project
	sandboxEnabled := false
	if projectID != "" {
		proj, err := tm.store.GetProject(projectID)
		if err != nil {
			return nil, fmt.Errorf("get project for sandbox check: %w", err)
		}
		sandboxEnabled = proj.SandboxEnabled
	}

	var sess *taskTerminal
	if sandboxEnabled {
		// Sandbox is required — no fallback
		if tm.sandboxMgr == nil {
			return nil, fmt.Errorf("sandbox manager not configured")
		}
		if !tm.sandboxMgr.IsAvailable() {
			return nil, fmt.Errorf("gVisor (runsc) is not installed — sandbox cannot be used")
		}
		// Get configured base image from global config
		baseImage := ""
		if cfg, cfgErr := tm.cfgStore.GetConfig(); cfgErr == nil {
			baseImage = cfg.DefaultSandboxImage
		}
		var shell *sandbox.ShellProcess
		var err error
		// Both project and task terminals create the container on demand if it doesn't exist.
		// Task executor also creates the container, but the terminal may be opened before
		// the task starts (or the task was never started), so we create it here as needed.
		shell, err = tm.sandboxMgr.StartShellWithCreate(taskID, worktreePath, baseImage)
		if err != nil {
			log.Printf("[terminal] %s sandbox shell start failed: %v", sessionKey, err)
			return nil, fmt.Errorf("sandbox not available: %w", err)
		}
		sess = &taskTerminal{
			cmd:        shell.Cmd,
			ptmx:       shell.Ptmx,
			shell:      shell,
			conns:      make(map[*websocket.Conn]bool),
			lastActive: time.Now(),
		}
		log.Printf("[terminal] %s started sandbox shell", sessionKey)
	}

	if sess == nil {
		// Direct shell (sandbox not enabled for this project)
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}

		cmd := exec.Command(shell)
		cmd.Dir = worktreePath
		cmd.Env = append(os.Environ(), "TERM=xterm-256color")

		ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
		if err != nil {
			return nil, err
		}

		sess = &taskTerminal{
			cmd:        cmd,
			ptmx:       ptmx,
			conns:      make(map[*websocket.Conn]bool),
			lastActive: time.Now(),
		}
	}

	sess.onExit = func() {
		tm.mu.Lock()
		delete(tm.sessions, sessionKey)
		tm.mu.Unlock()
		log.Printf("[terminal] removed session %s after shell exit", sessionKey)
	}
	tm.sessions[sessionKey] = sess
	go sess.readLoop()

	return sess, nil
}

// RemoveSession kills the PTY for a task (called when task is deleted).
func (tm *TerminalManager) RemoveSession(taskID string) {
	tm.mu.Lock()
	session, ok := tm.sessions[taskID]
	delete(tm.sessions, taskID)
	tm.mu.Unlock()
	if ok {
		session.close()
		log.Printf("terminal: removed session for task %s", taskID)
	}
}

// readLoop reads from the PTY, buffers all output, and broadcasts to connected clients.
// When the shell process exits, it sends a "closed" message to all clients and cleans up.
func (t *taskTerminal) readLoop() {
	buf := make([]byte, 32768)
	for {
		n, err := t.ptmx.Read(buf)
		if err != nil {
			// Shell process exited — notify all connected clients and clean up
			closedMsg, _ := json.Marshal(types.TerminalMessage{Type: "closed"})
			t.mu.Lock()
			conns := make([]*websocket.Conn, 0, len(t.conns))
			for c := range t.conns {
				conns = append(conns, c)
			}
			t.conns = nil
			t.mu.Unlock()

			ctx := context.Background()
			for _, c := range conns {
				_ = c.Write(ctx, websocket.MessageText, closedMsg)
				c.Close(websocket.StatusNormalClosure, "shell exited")
			}
			log.Printf("[terminal] shell exited, notified %d client(s)", len(conns))
			if t.onExit != nil {
				t.onExit()
			}
			return
		}
		data := types.TerminalMessage{Type: "output", Data: string(buf[:n])}
		encoded, _ := json.Marshal(data)

		t.mu.Lock()
		t.buf = append(t.buf, encoded)
		if len(t.buf) > TerminalBufferSize {
			t.buf = t.buf[len(t.buf)-TerminalBufferSize:]
		}
		conns := make([]*websocket.Conn, 0, len(t.conns))
		for c := range t.conns {
			conns = append(conns, c)
		}
		t.mu.Unlock()

		if len(conns) > 0 {
			ctx := context.Background()
			for _, c := range conns {
				_ = c.Write(ctx, websocket.MessageText, encoded)
			}
		}
	}
}

// close kills the PTY and cleans up.
func (t *taskTerminal) close() {
	t.mu.Lock()
	for c := range t.conns {
		c.Close(websocket.StatusNormalClosure, "closed")
	}
	t.conns = nil
	t.mu.Unlock()

	if t.shell != nil {
		_ = t.shell.Close()
	} else {
		t.ptmx.Close()
		t.cmd.Process.Kill()
		t.cmd.Wait()
	}
}

// HandleTerminal handles WebSocket connections for a task terminal.
func (tm *TerminalManager) HandleTerminal(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cfg, err := tm.cfgStore.GetConfig()
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to get config")
		return
	}
	worktreePath := filepath.Join(cfg.WorkspaceDir, taskID)

	session, err := tm.getOrCreate(taskID, taskID, worktreePath, projectID)
	if err != nil {
		log.Printf("terminal pty error for task %s: %v", taskID, err)
		httputil.JSONError(w, http.StatusInternalServerError, "failed to start terminal")
		return
	}

	tm.handleTerminalWS(w, r, session)
}

// HandleProjectTerminal handles WebSocket connections for a project-level terminal.
func (tm *TerminalManager) HandleProjectTerminal(w http.ResponseWriter, r *http.Request, projectID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	project, err := tm.store.GetProject(projectID)
	if err != nil {
		httputil.JSONError(w, http.StatusNotFound, "project not found")
		return
	}

	sessionKey := "proj:" + projectID
	session, err := tm.getOrCreate(sessionKey, sessionKey, project.Path, projectID)
	if err != nil {
		log.Printf("terminal pty error for project %s: %v", projectID, err)
		httputil.JSONError(w, http.StatusInternalServerError, "failed to start terminal")
		return
	}

	tm.handleTerminalWS(w, r, session)
}

// handleTerminalWS is the shared WebSocket handler for both task and project terminals.
func (tm *TerminalManager) handleTerminalWS(w http.ResponseWriter, r *http.Request, session *taskTerminal) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionContextTakeover,
	})
	if err != nil {
		log.Printf("terminal accept error: %v", err)
		return
	}

	session.mu.Lock()
	session.conns[conn] = true
	session.lastActive = time.Now()
	bufCopy := make([][]byte, len(session.buf))
	copy(bufCopy, session.buf)
	session.mu.Unlock()

	ctx := context.Background()

	for _, frame := range bufCopy {
		_ = conn.Write(ctx, websocket.MessageText, frame)
	}

	defer func() {
		session.mu.Lock()
		delete(session.conns, conn)
		session.mu.Unlock()
		conn.Close(websocket.StatusNormalClosure, "closed")
	}()

	rctx := r.Context()
	for {
		msgType, data, err := conn.Read(rctx)
		if err != nil {
			return
		}
		if msgType != websocket.MessageText {
			continue
		}

		var msg types.TerminalMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "input":
			session.ptmx.Write([]byte(msg.Data))
		case "resize":
			sz := &pty.Winsize{Rows: uint16(msg.Rows), Cols: uint16(msg.Cols)}
			if err := pty.Setsize(session.ptmx, sz); err != nil {
				log.Printf("terminal resize error: %v", err)
			}
		}
	}
}
