package backend

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/coder/websocket"
)

const (
	// TerminalBufferSize is the max number of output frames to keep when no clients are connected.
	TerminalBufferSize = 4096
)

// taskTerminal holds a persistent PTY session for a single task.
type taskTerminal struct {
	cmd        *exec.Cmd
	ptmx       *os.File
	mu         sync.Mutex
	conns      map[*websocket.Conn]bool
	buf        [][]byte    // buffered output frames when no clients
	lastActive time.Time   // when last client was connected
}

// TerminalManager manages per-task persistent terminal sessions.
type TerminalManager struct {
	mu       sync.RWMutex
	sessions map[string]*taskTerminal // taskID -> session
	store    *Store
	cfgStore *ConfigStore
}

// NewTerminalManager creates a new TerminalManager.
func NewTerminalManager(store *Store, cfgStore *ConfigStore) *TerminalManager {
	return &TerminalManager{
		sessions: make(map[string]*taskTerminal),
		store:    store,
		cfgStore: cfgStore,
	}
}

// getOrCreate returns an existing session or creates a new one for the task.
func (tm *TerminalManager) getOrCreate(taskID string, worktreePath string) (*taskTerminal, error) {
	tm.mu.RLock()
	session, ok := tm.sessions[taskID]
	tm.mu.RUnlock()
	if ok {
		return session, nil
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()

	// Double-check after acquiring write lock.
	if session, ok = tm.sessions[taskID]; ok {
		return session, nil
	}

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

	session = &taskTerminal{
		cmd:        cmd,
		ptmx:       ptmx,
		conns:      make(map[*websocket.Conn]bool),
		lastActive: time.Now(),
	}

	tm.sessions[taskID] = session

	// Start reading PTY output in background.
	go session.readLoop()

	return session, nil
}

// removeSession kills the PTY for a task (called when task is deleted).
func (tm *TerminalManager) removeSession(taskID string) {
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
func (t *taskTerminal) readLoop() {
	buf := make([]byte, 32768)
	for {
		n, err := t.ptmx.Read(buf)
		if err != nil {
			return
		}
		data := TerminalMessage{Type: "output", Data: string(buf[:n])}
		encoded, _ := json.Marshal(data)

		t.mu.Lock()
		// Always buffer the frame.
		t.buf = append(t.buf, encoded)
		if len(t.buf) > TerminalBufferSize {
			t.buf = t.buf[len(t.buf)-TerminalBufferSize:]
		}
		// Broadcast to connected clients.
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

	t.ptmx.Close()
	t.cmd.Process.Kill()
	t.cmd.Wait()
}

// HandleTerminal handles WebSocket connections for a task terminal.
func (tm *TerminalManager) HandleTerminal(w http.ResponseWriter, r *http.Request, projectID, taskID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Resolve worktree path.
	cfg, err := tm.cfgStore.GetConfig()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "failed to get config")
		return
	}
	worktreePath := filepath.Join(cfg.WorkspaceDir, taskID)

	session, err := tm.getOrCreate(taskID, worktreePath)
	if err != nil {
		log.Printf("terminal pty error for task %s: %v", taskID, err)
		jsonError(w, http.StatusInternalServerError, "failed to start terminal")
		return
	}

	tm.handleTerminalWS(w, r, session)
}

// HandleProjectTerminal handles WebSocket connections for a project-level terminal (persistent).
func (tm *TerminalManager) HandleProjectTerminal(w http.ResponseWriter, r *http.Request, projectID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	project, err := tm.store.GetProject(projectID)
	if err != nil {
		jsonError(w, http.StatusNotFound, "project not found")
		return
	}

	sessionKey := "proj:" + projectID
	session, err := tm.getOrCreate(sessionKey, project.Path)
	if err != nil {
		log.Printf("terminal pty error for project %s: %v", projectID, err)
		jsonError(w, http.StatusInternalServerError, "failed to start terminal")
		return
	}

	tm.handleTerminalWS(w, r, session)
}

// handleTerminalWS is the shared WebSocket handler for both task and project terminals.
func (tm *TerminalManager) handleTerminalWS(w http.ResponseWriter, r *http.Request, session *taskTerminal) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Printf("terminal accept error: %v", err)
		return
	}

	// Register this connection and replay full buffer.
	session.mu.Lock()
	session.conns[conn] = true
	session.lastActive = time.Now()
	bufCopy := make([][]byte, len(session.buf))
	copy(bufCopy, session.buf)
	session.mu.Unlock()

	ctx := context.Background()

	// Replay all buffered frames as individual WebSocket messages.
	for _, frame := range bufCopy {
		_ = conn.Write(ctx, websocket.MessageText, frame)
	}

	// Client → PTY
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

		var msg TerminalMessage
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
