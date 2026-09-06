// Package term implements interactive terminals: a podman exec (or a host shell for the
// rare unsandboxed project) on a host-side PTY, piped to the browser over a WebSocket.
// Terminals bypass the sandbox bridge, and the endpoint checks Origin as well as the
// session cookie, because it hands out a shell.
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
	"strings"
	"sync"
	"time"

	"kanban/backend/agent"
	"kanban/backend/config"
	"kanban/backend/httputil"
	"kanban/backend/prompts"
	"kanban/backend/sandbox"
	"kanban/backend/store"
	"kanban/backend/types"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

const (
	// TerminalBufferSize is the max number of output frames to keep when no clients are connected.
	TerminalBufferSize = 4096
)

// connSize tracks the terminal dimensions reported by a connected client.
type connSize struct {
	rows uint16
	cols uint16
}

// taskTerminal holds a persistent PTY session for a single task.
type taskTerminal struct {
	cmd        *exec.Cmd
	ptmx       *os.File
	shell      *sandbox.ShellProcess // non-nil when running in sandbox
	mu         sync.Mutex
	conns      map[*websocket.Conn]bool
	connSizes  map[*websocket.Conn]connSize // per-client terminal dimensions
	buf        [][]byte
	lastActive time.Time
	onExit     func() // called when shell process exits, to remove session from manager
}

// defaultTermSize is the fallback PTY size when no clients have reported dimensions.
const (
	defaultRows = 40
	defaultCols = 120
)

// minSize returns the smallest rows/cols across connected clients (defaults if
// none), so the PTY fits inside every client's viewport.
func (t *taskTerminal) minSize() (rows, cols uint16) {
	minRows, minCols := uint16(0), uint16(0)
	for _, sz := range t.connSizes {
		if sz.rows == 0 || sz.cols == 0 {
			continue
		}
		if minRows == 0 || sz.rows < minRows {
			minRows = sz.rows
		}
		if minCols == 0 || sz.cols < minCols {
			minCols = sz.cols
		}
	}
	if minRows == 0 {
		minRows = defaultRows
	}
	if minCols == 0 {
		minCols = defaultCols
	}
	return minRows, minCols
}

// clampDim bounds a client-reported dimension to 1..1000, preventing
// malformed values from wrapping through uint16.
func clampDim(v int) uint16 {
	if v < 1 {
		return 1
	}
	if v > 1000 {
		return 1000
	}
	return uint16(v)
}

// applySize sets the PTY to the given rows/cols if it differs from current size.
func (t *taskTerminal) applySize(rows, cols uint16) {
	curRows, curCols, err := pty.Getsize(t.ptmx)
	if err != nil {
		log.Printf("terminal getsz error: %v", err)
		return
	}
	if uint16(curRows) == rows && uint16(curCols) == cols {
		return
	}
	if err := pty.Setsize(t.ptmx, &pty.Winsize{Rows: rows, Cols: cols}); err != nil {
		log.Printf("terminal resize error: %v", err)
	}
}

// hostShellEnv is the environment of a host terminal — the one terminal that runs
// outside a container, for a project with sandboxing disabled.
//
// Under DANGER_ZONE=1, the flag that allows such a project, the terminal is the
// operator's own shell and gets its environment, credentials included: a scrubbed one
// cannot run the ssh-agent or registry logins the flag was set up for. Without the
// flag the shell gets SafeAgentEnv instead, because a project restored from an old
// database must not be handed MIKE_LOGIN_PASSWORD or MIKE_ENCRYPTION_KEY.
func hostShellEnv() []string {
	if os.Getenv("DANGER_ZONE") == "1" {
		return append(os.Environ(), "TERM=xterm-256color")
	}
	return agent.SafeAgentEnv("TERM=xterm-256color")
}

// handleResize records a client's size and applies the smallest rows/cols
// across all connected clients, so narrow devices dictate the PTY size.
func (t *taskTerminal) handleResize(conn *websocket.Conn, rows, cols uint16) {
	t.mu.Lock()
	t.connSizes[conn] = connSize{rows: rows, cols: cols}
	r, c := t.minSize()
	t.mu.Unlock()
	t.applySize(r, c)
}

// removeConnSize drops a client's size and re-applies the smallest among remaining clients.
func (t *taskTerminal) removeConnSize(conn *websocket.Conn) {
	t.mu.Lock()
	delete(t.conns, conn)
	delete(t.connSizes, conn)
	r, c := t.minSize()
	t.mu.Unlock()
	t.applySize(r, c)
}

// sandboxOps is the subset of sandbox.SandboxManager methods used by the terminal manager.
// Defined here so tests can provide a mock without depending on gVisor.
type sandboxOps interface {
	IsAvailable() bool
	EnsureImageForProject(project *types.Project, onProgress func(types.SandboxImageStatus)) (string, error)
	StartShell(taskID, worktreePath string, project *types.Project, opts sandbox.SandboxOpts) (*sandbox.ShellProcess, []types.AssignedPort, error)
	Stop(taskID string) error
	GetContainerPorts(taskID string) ([]types.AssignedPort, error)
}

// terminalStore is the read-only persistence a terminal session needs: which project a
// task belongs to, and that project's sandbox settings.
type terminalStore interface {
	GetProject(id string) (*types.Project, error)
	GetTask(projectID, taskID string) (*types.Task, error)
	GetTasksByProject(projectID string) ([]types.Task, error)
}

var _ terminalStore = (*store.Store)(nil)

// TerminalManager manages per-task persistent terminal sessions.
type TerminalManager struct {
	mu                     sync.RWMutex
	sessions               map[string]*taskTerminal
	store                  terminalStore
	cfgStore               *config.ConfigStore
	sandboxMgr             sandboxOps
	agentRegistry          *agent.AgentRegistry
	onPortsAssigned        func(taskID, projectID string, ports []types.AssignedPort) // callback when task ports are assigned
	onProjectPortsAssigned func(projectID string, ports []types.AssignedPort)         // callback when project-level terminal ports are assigned
}

// NewTerminalManager creates a new TerminalManager.
func NewTerminalManager(store *store.Store, cfgStore *config.ConfigStore, sandboxMgr sandboxOps, agentRegistry *agent.AgentRegistry) *TerminalManager {
	return &TerminalManager{
		sessions:      make(map[string]*taskTerminal),
		store:         store,
		cfgStore:      cfgStore,
		sandboxMgr:    sandboxMgr,
		agentRegistry: agentRegistry,
	}
}

// SetAgentRegistry sets the agent registry after creation (needed because agentRegistry is created after terminalMgr).
func (tm *TerminalManager) SetAgentRegistry(ar *agent.AgentRegistry) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.agentRegistry = ar
}

// shellSandboxOpts is what a sandboxed shell's container is created with. It is a method rather than a
// literal in the session setup so it is testable, and so it stays in step with the task container's own
// options. A dependency directory granted to an LSP extension is not in it: a shell sees the host only
// through the project's Sandbox Configuration mounts, like every other container here.
func (tm *TerminalManager) shellSandboxOpts(image string, proj *types.Project, agentSandbox *types.AgentSandbox) sandbox.SandboxOpts {
	return sandbox.SandboxOpts{
		BaseImage:        image,
		NetworkIsolation: proj.SandboxNetworkIsolation,
		MemoryLimit:      proj.SandboxMemoryLimit,
		ForwardPorts:     proj.ForwardPorts,
		AgentSandbox:     agentSandbox,
		SkillsDir:        prompts.SkillsDir(),
		SandboxMounts:    proj.SandboxMounts,
		SandboxEnvs:      proj.SandboxEnvs,
		Capabilities:     proj.SandboxCapabilities,
	}
}

// getAgentSandbox returns the agent sandbox config for the current agent, or nil.
// agentRegistry is set once during initialization and only read afterwards, so no lock needed.
func (tm *TerminalManager) getAgentSandbox() *types.AgentSandbox {
	if tm.agentRegistry == nil {
		return nil
	}
	return tm.agentRegistry.GetAnyAgentSandbox()
}

// SetOnPortsAssigned sets a callback invoked when sandbox ports are assigned during terminal shell creation.
func (tm *TerminalManager) SetOnPortsAssigned(fn func(taskID, projectID string, ports []types.AssignedPort)) {
	tm.onPortsAssigned = fn
}

// SetOnProjectPortsAssigned sets a callback invoked when project-level terminal ports are assigned.
func (tm *TerminalManager) SetOnProjectPortsAssigned(fn func(projectID string, ports []types.AssignedPort)) {
	tm.onProjectPortsAssigned = fn
}

// termStatusMessage encodes one status line for the terminal's WebSocket channel.
func termStatusMessage(data string) []byte {
	msg, _ := json.Marshal(types.TerminalMessage{Type: "status", Data: data})
	return msg
}

// imageStatusLabel names what an image-preparation event is doing, for the terminal's text channel,
// or false when the event carries nothing the user needs (the completion marker, or a failure the
// caller reports itself).
func imageStatusLabel(status types.SandboxImageStatus) (string, bool) {
	switch status.Phase {
	case types.SandboxImagePhaseBuilding:
		if status.Line != "" {
			return status.Line + "\n", true
		}
		return "Building sandbox image " + status.Image + "...\n", true
	case types.SandboxImagePhasePulling:
		if status.Line != "" {
			return status.Line + "\n", true
		}
		return "Pulling " + status.Image + "...\n", true
	}
	return "", false
}

// GetProjectPorts returns the assigned ports for a project-level terminal, if active.
// Queries podman on demand for live port mappings.
func (tm *TerminalManager) GetProjectPorts(projectID string) []types.AssignedPort {
	if tm.sandboxMgr == nil {
		return nil
	}
	ports, _ := tm.sandboxMgr.GetContainerPorts("proj:" + projectID)
	return ports
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
	var proj *types.Project
	if projectID != "" {
		var err error
		proj, err = tm.store.GetProject(projectID)
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
		// The project's image has to exist before a container can run from it. This session has no
		// stream to report progress on — the WS handler that opens the terminal does, and the two
		// share one build (see SandboxManager.EnsureImageForProject). It is called again here rather
		// than passed down on purpose: this is the function that creates the container, so it must
		// not depend on its caller having prepared the image. After a completed build the repeat
		// costs one `podman image exists`.
		imageToUse, imgErr := tm.sandboxMgr.EnsureImageForProject(proj, nil)
		if imgErr != nil {
			return nil, fmt.Errorf("sandbox not available: %w", imgErr)
		}
		// Mount agent sandbox if any coding agent is configured
		agentSandbox := tm.getAgentSandbox()
		var shell *sandbox.ShellProcess
		var err error
		// Both project and task terminals create the container on demand if it doesn't exist.
		// Task executor also creates the container, but the terminal may be opened before
		// the task starts (or the task was never started), so we create it here as needed.
		// Only mount .git/ for task-level containers (not project-level terminals)
		shellProject := proj
		if strings.HasPrefix(taskID, "proj:") {
			shellProject = &types.Project{ID: proj.ID, Name: proj.Name}
		}
		shell, assignedPorts, err := tm.sandboxMgr.StartShell(taskID, worktreePath, shellProject, tm.shellSandboxOpts(imageToUse, proj, agentSandbox))
		if err != nil {
			log.Printf("[terminal] %s sandbox shell start failed: %v", sessionKey, err)
			return nil, fmt.Errorf("sandbox not available: %w", err)
		}
		// Notify caller of assigned ports
		if len(assignedPorts) > 0 {
			if !strings.HasPrefix(taskID, "proj:") && tm.onPortsAssigned != nil {
				// Task-level terminal: broadcast via callback
				tm.onPortsAssigned(taskID, projectID, assignedPorts)
			} else if strings.HasPrefix(taskID, "proj:") && tm.onProjectPortsAssigned != nil {
				// Project-level terminal: broadcast via callback
				tm.onProjectPortsAssigned(projectID, assignedPorts)
			}
		}
		sess = &taskTerminal{
			cmd:        shell.Cmd,
			ptmx:       shell.Ptmx,
			shell:      shell,
			conns:      make(map[*websocket.Conn]bool),
			connSizes:  make(map[*websocket.Conn]connSize),
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
		cmd.Env = hostShellEnv()

		ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
		if err != nil {
			return nil, err
		}

		sess = &taskTerminal{
			cmd:        cmd,
			ptmx:       ptmx,
			conns:      make(map[*websocket.Conn]bool),
			connSizes:  make(map[*websocket.Conn]connSize),
			lastActive: time.Now(),
		}
	}

	sess.onExit = func() {
		tm.mu.Lock()
		delete(tm.sessions, sessionKey)
		tm.mu.Unlock()
		log.Printf("[terminal] removed session %s after shell exit", sessionKey)
		// For project-level sandbox terminals, stop the container when the shell exits.
		// Task-level containers are managed by the task executor lifecycle.
		if strings.HasPrefix(sessionKey, "proj:") && sandboxEnabled && tm.sandboxMgr != nil {
			tm.sandboxMgr.Stop(sessionKey)
		}
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

// activeSessionCount reports how many terminal sessions are registered.
func (tm *TerminalManager) activeSessionCount() int {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return len(tm.sessions)
}

// Close terminates every live terminal session. Each holds a PTY and a child shell, which would
// otherwise outlive shutdown; Server.Shutdown calls this before sandbox cleanup so the kill lands
// while the container is still there to receive it.
//
// Detach under the lock, close outside it: taskTerminal.close waits on the read loop, and the read
// loop calls back into RemoveSession, which takes this same lock.
func (tm *TerminalManager) Close() {
	tm.mu.Lock()
	sessions := make([]*taskTerminal, 0, len(tm.sessions))
	for key, session := range tm.sessions {
		sessions = append(sessions, session)
		delete(tm.sessions, key)
	}
	tm.mu.Unlock()

	for _, session := range sessions {
		session.close()
	}
	if len(sessions) > 0 {
		log.Printf("terminal: closed %d session(s) on shutdown", len(sessions))
	}
}

// RemoveProjectSessions closes all terminal sessions for every task in a project.
// Called when project sandbox settings change. Container cleanup is handled
// separately by the caller (StopProjectContainers) to ensure all containers
// are stopped even if there are no active terminal sessions.
func (tm *TerminalManager) RemoveProjectSessions(projectID string) {
	tasks, _ := tm.store.GetTasksByProject(projectID)
	for _, task := range tasks {
		tm.RemoveSession(task.ID)
	}
	tm.RemoveSession("proj:" + projectID)
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
func (tm *TerminalManager) HandleTerminal(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	taskID := r.PathValue("taskID")
	if r.Method != http.MethodGet {
		httputil.MethodNotAllowed(w)
		return
	}

	cfg, err := tm.cfgStore.GetConfig()
	if err != nil {
		httputil.JSONError(w, http.StatusInternalServerError, "failed to get config")
		return
	}
	worktreesDir := cfg.WorkspaceDir

	// Resolve the worktree path from the task record
	task, err := tm.store.GetTask(projectID, taskID)
	if err != nil {
		httputil.Fail(w, err, "get task", "task not found")
		return
	}
	if task.ProjectID != projectID {
		httputil.JSONError(w, http.StatusNotFound, "task not found")
		return
	}
	worktreePath := filepath.Join(worktreesDir, task.ProjectID, taskID)

	tm.handleTerminalWSWithSetup(w, r, taskID, taskID, worktreePath, projectID)
}

// HandleProjectTerminal handles WebSocket connections for a project-level terminal.
func (tm *TerminalManager) HandleProjectTerminal(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	if r.Method != http.MethodGet {
		httputil.MethodNotAllowed(w)
		return
	}

	project, err := tm.store.GetProject(projectID)
	if err != nil {
		httputil.Fail(w, err, "get project", "project not found")
		return
	}

	sessionKey := "proj:" + projectID
	tm.handleTerminalWSWithSetup(w, r, sessionKey, sessionKey, project.Path, projectID)
}

// handleTerminalWSWithSetup upgrades the WebSocket first, then creates the terminal session.
// This allows sending status messages (e.g., pulling image) before the shell is ready.
func (tm *TerminalManager) handleTerminalWSWithSetup(w http.ResponseWriter, r *http.Request, sessionKey, taskID, worktreePath, projectID string) {
	// Origin checking lives in httputil.AcceptWS: the second lock on an endpoint that
	// hands out a shell.
	conn, ok := httputil.AcceptWS(w, r, "terminal")
	if !ok {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "closed")

	ctx := context.Background()

	// This is the terminal's own progress channel: the image is prepared before the shell starts, so
	// its output is what the user sees while the terminal is still blank.
	var sandboxProject *types.Project
	if projectID != "" {
		if proj, err := tm.store.GetProject(projectID); err == nil && proj.SandboxEnabled {
			sandboxProject = proj
		}
	}
	sandboxEnabled := sandboxProject != nil
	if sandboxEnabled && tm.sandboxMgr != nil {
		_, imgErr := tm.sandboxMgr.EnsureImageForProject(sandboxProject, func(status types.SandboxImageStatus) {
			if line, ok := imageStatusLabel(status); ok {
				_ = conn.Write(ctx, websocket.MessageText, termStatusMessage(line))
			}
		})
		if imgErr != nil {
			_ = conn.Write(ctx, websocket.MessageText, termStatusMessage("Failed to prepare sandbox image: "+imgErr.Error()+"\n"))
			closeMsg, _ := json.Marshal(types.TerminalMessage{Type: "closed"})
			_ = conn.Write(ctx, websocket.MessageText, closeMsg)
			return
		}
	}

	// Notify user before container creation (can take several seconds)
	if sandboxEnabled {
		statusMsg, _ := json.Marshal(types.TerminalMessage{Type: "status", Data: "Starting sandbox container..."})
		_ = conn.Write(ctx, websocket.MessageText, statusMsg)
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

	// Shell is ready — switch to normal I/O relay. Status messages remain visible.
	// Send a newline so shell output starts on a fresh line at column 0.
	newlineMsg, _ := json.Marshal(types.TerminalMessage{Type: "output", Data: "\r\n"})
	_ = conn.Write(ctx, websocket.MessageText, newlineMsg)

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
		session.removeConnSize(conn)
	}()

	// A failed ping cancels the read context, which unblocks the Read loop below.
	ctxRead, cancelRead := context.WithCancel(context.Background())
	defer cancelRead()
	httputil.Keepalive(ctxRead, conn, nil, cancelRead)

	for {
		msgType, data, err := conn.Read(ctxRead)
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
			session.handleResize(conn, clampDim(msg.Rows), clampDim(msg.Cols))
		}
	}
}
