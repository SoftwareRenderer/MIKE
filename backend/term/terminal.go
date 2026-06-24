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
	networkIsolation := false
	baseImage := ""
	sandboxDockerfilePath := ""
	memoryLimit := 0
	projectName := ""
	if projectID != "" {
		proj, err := tm.store.GetProject(projectID)
		if err != nil {
			return nil, fmt.Errorf("get project for sandbox check: %w", err)
		}
		sandboxEnabled = proj.SandboxEnabled
		networkIsolation = proj.SandboxNetworkIsolation
		baseImage = proj.SandboxImage
		sandboxDockerfilePath = proj.SandboxDockerfilePath
		memoryLimit = proj.SandboxMemoryLimit
		projectName = proj.Name
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
		// Determine image: Dockerfile build takes precedence over pre-built image
		imageToUse := ""
		if sandboxDockerfilePath != "" {
			imageToUse = sandbox.ProjectImageName(projectName)
			if !tm.sandboxMgr.ImageReadyForProject(projectName) {
				if err := tm.sandboxMgr.BuildImage(projectName, sandboxDockerfilePath); err != nil {
					return nil, fmt.Errorf("build sandbox image: %w", err)
				}
			}
		} else {
			imageToUse = baseImage
			if imageToUse == "" {
				imageToUse = sandbox.BaseImage
			}
		}
		var shell *sandbox.ShellProcess
		var err error
		// Both project and task terminals create the container on demand if it doesn't exist.
		// Task executor also creates the container, but the terminal may be opened before
		// the task starts (or the task was never started), so we create it here as needed.
		shell, err = tm.sandboxMgr.StartShellWithCreate(taskID, worktreePath, imageToUse, networkIsolation, memoryLimit)
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
		log.Printf("[terminal] %s started sandbox shell with image %s", sessionKey, imageToUse)
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

// RemoveProjectSessions closes all terminal sessions and stops sandbox containers
// for every task in a project. Called when project sandbox settings change.
func (tm *TerminalManager) RemoveProjectSessions(projectID string) {
	tasks, _ := tm.store.GetTasksByProject(projectID)
	for _, task := range tasks {
		tm.RemoveSession(task.ID)
	}
	tm.RemoveSession("proj:" + projectID)
	// Stop sandbox containers so they'll be recreated with the new image
	if tm.sandboxMgr != nil {
		for _, task := range tasks {
			_ = tm.sandboxMgr.Stop(task.ID)
		}
		_ = tm.sandboxMgr.Stop("proj:" + projectID)
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

	tm.handleTerminalWSWithSetup(w, r, taskID, taskID, worktreePath, projectID)
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
	tm.handleTerminalWSWithSetup(w, r, sessionKey, sessionKey, project.Path, projectID)
}

// handleTerminalWSWithSetup upgrades the WebSocket first, then creates the terminal session.
// This allows sending status messages (e.g., pulling image) before the shell is ready.
func (tm *TerminalManager) handleTerminalWSWithSetup(w http.ResponseWriter, r *http.Request, sessionKey, taskID, worktreePath, projectID string) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionContextTakeover,
	})
	if err != nil {
		log.Printf("terminal accept error: %v", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "closed")

	ctx := context.Background()

	// Check if sandbox needs an image pull or build
	sandboxEnabled := false
	baseImage := ""
	sandboxDockerfilePath := ""
	projectName := ""
	if projectID != "" {
		proj, err := tm.store.GetProject(projectID)
		if err == nil && proj.SandboxEnabled {
			sandboxEnabled = true
			baseImage = proj.SandboxImage
			sandboxDockerfilePath = proj.SandboxDockerfilePath
			projectName = proj.Name
		}
	}
	imageToUse := ""
	if sandboxDockerfilePath != "" {
		imageToUse = sandbox.ProjectImageName(projectName)
	} else {
		imageToUse = baseImage
		if imageToUse == "" {
			imageToUse = sandbox.BaseImage
		}
	}
	if sandboxEnabled && tm.sandboxMgr != nil {
		needsImage := false
		if sandboxDockerfilePath != "" {
			needsImage = !tm.sandboxMgr.ImageReadyForProject(projectName)
		} else {
			needsImage = !tm.sandboxMgr.ImageReady(imageToUse)
		}
		if needsImage {
			if sandboxDockerfilePath != "" {
				statusMsg, _ := json.Marshal(types.TerminalMessage{Type: "status", Data: "Building sandbox image from Dockerfile...\n"})
				_ = conn.Write(ctx, websocket.MessageText, statusMsg)
				if buildErr := tm.sandboxMgr.BuildImage(projectName, sandboxDockerfilePath); buildErr != nil {
					errMsg, _ := json.Marshal(types.TerminalMessage{Type: "status", Data: "Failed to build image: " + buildErr.Error() + "\n"})
					_ = conn.Write(ctx, websocket.MessageText, errMsg)
					closeMsg, _ := json.Marshal(types.TerminalMessage{Type: "closed"})
					_ = conn.Write(ctx, websocket.MessageText, closeMsg)
					return
				}
			} else {
				statusMsg, _ := json.Marshal(types.TerminalMessage{Type: "status", Data: "Pulling " + imageToUse + "...\n"})
				_ = conn.Write(ctx, websocket.MessageText, statusMsg)
				if pullErr := tm.sandboxMgr.PullImage(imageToUse); pullErr != nil {
					errMsg, _ := json.Marshal(types.TerminalMessage{Type: "status", Data: "Failed to pull image: " + pullErr.Error() + "\n"})
					_ = conn.Write(ctx, websocket.MessageText, errMsg)
					closeMsg, _ := json.Marshal(types.TerminalMessage{Type: "closed"})
					_ = conn.Write(ctx, websocket.MessageText, closeMsg)
					return
				}
			}
		}
	}

	session, err := tm.getOrCreate(sessionKey, taskID, worktreePath, projectID)
	if err != nil {
		log.Printf("terminal pty error for %s: %v", sessionKey, err)
		errMsg, _ := json.Marshal(types.TerminalMessage{Type: "status", Data: "Failed to start terminal: " + err.Error() + "\n"})
		_ = conn.Write(ctx, websocket.MessageText, errMsg)
		closeMsg, _ := json.Marshal(types.TerminalMessage{Type: "closed"})
		_ = conn.Write(ctx, websocket.MessageText, closeMsg)
		return
	}

	// Relay ready message
	readyMsg, _ := json.Marshal(types.TerminalMessage{Type: "status", Data: "\u001b[2J\u001b[H"}) // clear screen
	_ = conn.Write(ctx, websocket.MessageText, readyMsg)

	// Switch to normal I/O relay
	session.mu.Lock()
	session.conns[conn] = true
	session.lastActive = time.Now()
	bufCopy := make([][]byte, len(session.buf))
	copy(bufCopy, session.buf)
	session.mu.Unlock()

	for _, frame := range bufCopy {
		_ = conn.Write(ctx, websocket.MessageText, frame)
	}

	defer func() {
		session.mu.Lock()
		delete(session.conns, conn)
		session.mu.Unlock()
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
