package term

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"unsafe"

	"kanban/backend/config"
	"kanban/backend/sandbox"
	"kanban/backend/store"
	"kanban/backend/types"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

// mockSandbox implements sandboxOps for testing without gVisor.
type mockSandbox struct {
	mu           sync.Mutex
	available    bool
	shells       map[string]*mockShell // taskID -> mockShell
	stopped      map[string]bool       // taskIDs that had Stop() called
	stopMu       sync.Mutex
	imageReady   bool
	buildImageErr error
	startShellErr error
}

type mockShell struct {
	cmd  *exec.Cmd
	ptmx *os.File
}

func newMockSandbox(t *testing.T) *mockSandbox {
	m := &mockSandbox{
		available:    true,
		shells:       make(map[string]*mockShell),
		stopped:      make(map[string]bool),
		imageReady:   true,
	}
	t.Cleanup(func() {
		m.mu.Lock()
		for _, sh := range m.shells {
			sh.ptmx.Close()
			sh.cmd.Process.Kill()
			sh.cmd.Wait()
		}
		m.mu.Unlock()
	})
	return m
}

func (m *mockSandbox) IsAvailable() bool { return m.available }
func (m *mockSandbox) ImageReady(string) bool { return m.imageReady }
func (m *mockSandbox) ImageReadyForProject(string) bool { return m.imageReady }
func (m *mockSandbox) BuildImage(string, string) error { return m.buildImageErr }
func (m *mockSandbox) BuildImageWithProgress(string, string, func(string)) error { return m.buildImageErr }
func (m *mockSandbox) PullImageWithProgress(string, func(string)) error { return nil }

func (m *mockSandbox) StartShellWithCreate(taskID, worktreePath, projectName, projectPath, projectID string, opts sandbox.SandboxOpts) (*sandbox.ShellProcess, []types.AssignedPort, error) {
	if m.startShellErr != nil {
		return nil, nil, m.startShellErr
	}
	// Create a real PTY shell so the readLoop can run
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell)
	cmd.Dir = worktreePath
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		return nil, nil, err
	}
	m.mu.Lock()
	m.shells[taskID] = &mockShell{cmd: cmd, ptmx: ptmx}
	m.mu.Unlock()
	return &sandbox.ShellProcess{Cmd: cmd, Ptmx: ptmx}, nil, nil
}

func (m *mockSandbox) Stop(taskID string) error {
	m.stopMu.Lock()
	m.stopped[taskID] = true
	m.stopMu.Unlock()
	// Don't touch the PTY/process here — the test or readLoop handles that.
	// This mock only tracks whether Stop was called.
	return nil
}

func (m *mockSandbox) GetContainerPorts(string) ([]types.AssignedPort, error) { return nil, nil }

func (m *mockSandbox) wasStopped(taskID string) bool {
	m.stopMu.Lock()
	defer m.stopMu.Unlock()
	return m.stopped[taskID]
}

// newTestStore creates a temporary SQLite store for testing.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// newTestConfigStore creates a temporary config store for testing.
// Returns both the store and config store so tests can set config values.
func newTestConfigStore(t *testing.T) (*store.Store, *config.ConfigStore) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "config.db")
	s := newTestStore(t)
	cs := config.NewConfigStore(s, dbPath, "test")
	// Set minimal config so terminal can start
	err := s.SetConfigValue("workspace_dir", dir)
	if err != nil {
		t.Fatalf("set workspace_dir: %v", err)
	}
	return s, cs
}

func TestProjectTerminalStopsContainerOnExit(t *testing.T) {
	dir := t.TempDir()
	s, cs := newTestConfigStore(t)
	mock := newMockSandbox(t)
	tm := NewTerminalManager(s, cs, mock, nil)

	// Create a project with sandbox enabled
	projID := "test-proj-stop"
	proj := &types.Project{
		ID:             projID,
		Name:           "Test Project",
		Path:           dir,
		SandboxEnabled: true,
	}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Get or create the project-level terminal session
	sessionKey := "proj:" + projID
	sess, err := tm.getOrCreate(sessionKey, sessionKey, dir, projID)
	if err != nil {
		t.Fatalf("getOrCreate: %v", err)
	}

	// Verify session exists
	tm.mu.RLock()
	_, ok := tm.sessions[sessionKey]
	tm.mu.RUnlock()
	if !ok {
		t.Fatal("session should exist after creation")
	}

	// Simulate shell exit by closing the PTY
	sess.mu.Lock()
	if sess.shell != nil {
		sess.shell.Ptmx.Close()
		sess.shell.Cmd.Process.Kill()
		sess.shell.Cmd.Wait()
	} else {
		sess.ptmx.Close()
		sess.cmd.Process.Kill()
		sess.cmd.Wait()
	}
	sess.mu.Unlock()

	// Wait briefly for readLoop to detect exit and call onExit.
	// The readLoop blocks on ptmx.Read, which returns EOF when PTY is closed.
	// Wait for BOTH session removal AND Stop() call to avoid a race
	// between the two operations inside onExit.
	done := make(chan struct{})
	go func() {
		for {
			tm.mu.RLock()
			_, ok := tm.sessions[sessionKey]
			tm.mu.RUnlock()
			if !ok && mock.wasStopped(sessionKey) {
				close(done)
				return
			}
		}
	}()

	select {
	case <-done:
		// Session was removed and Stop was called
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for session removal and container stop")
	}
}

func TestMultiClientResizeSmallestWidthWins(t *testing.T) {
	dir := t.TempDir()
	s, cs := newTestConfigStore(t)
	mock := newMockSandbox(t)
	tm := NewTerminalManager(s, cs, mock, nil)

	projID := "test-proj-resize"
	proj := &types.Project{
		ID:             projID,
		Name:           "Test Project",
		Path:           dir,
		SandboxEnabled: true,
	}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	taskID := "resize-task"
	sess, err := tm.getOrCreate(taskID, taskID, dir, projID)
	if err != nil {
		t.Fatalf("getOrCreate: %v", err)
	}

	// Verify initial PTY size is the default 40x120
	rows, cols, err := pty.Getsize(sess.ptmx)
	if err != nil {
		t.Fatalf("get initial winsize: %v", err)
	}
	if cols != 120 || rows != 40 {
		t.Fatalf("initial size = %dx%d, want 120x40", cols, rows)
	}

	// Create dummy conn pointers to simulate multiple clients.
	// Use a non-empty struct so Go doesn't coalesce allocations.
	type dummyConn struct{ id int }
	desktop := &dummyConn{id: 1}
	phone := &dummyConn{id: 2}
	desktopPtr := (*websocket.Conn)(unsafe.Pointer(desktop))
	phonePtr := (*websocket.Conn)(unsafe.Pointer(phone))

	// Simulate desktop client resize: 80x200
	sess.handleResize(desktopPtr, 80, 200)
	_, cols, _ = pty.Getsize(sess.ptmx)
	if cols != 200 {
		t.Fatalf("after desktop resize cols = %d, want 200", cols)
	}

	// Simulate phone client resize: 30x100 — smaller width should win
	sess.handleResize(phonePtr, 30, 100)
	_, cols, _ = pty.Getsize(sess.ptmx)
	if cols != 100 {
		t.Fatalf("after phone resize cols = %d, want 100 (smallest width)", cols)
	}

	// Desktop resizes again to even wider — should NOT change PTY (phone is still smallest)
	sess.handleResize(desktopPtr, 50, 300)
	_, cols, _ = pty.Getsize(sess.ptmx)
	if cols != 100 {
		t.Fatalf("after desktop wider resize cols = %d, want 100 (phone still smallest)", cols)
	}

	// Phone disconnects — desktop size should take over
	sess.removeConnSize(phonePtr)
	_, cols, _ = pty.Getsize(sess.ptmx)
	if cols != 300 {
		t.Fatalf("after phone disconnect cols = %d, want 300 (desktop now only client)", cols)
	}

	// Desktop also disconnects — should revert to default
	sess.removeConnSize(desktopPtr)
	_, cols, _ = pty.Getsize(sess.ptmx)
	if cols != 120 {
		t.Fatalf("after all disconnect cols = %d, want 120 (default)", cols)
	}
}

func TestMultiClientResizeMultipleClients(t *testing.T) {
	dir := t.TempDir()
	s, cs := newTestConfigStore(t)
	mock := newMockSandbox(t)
	tm := NewTerminalManager(s, cs, mock, nil)

	projID := "test-proj-multi"
	proj := &types.Project{
		ID:             projID,
		Name:           "Test Project",
		Path:           dir,
		SandboxEnabled: true,
	}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	taskID := "multi-task"
	sess, err := tm.getOrCreate(taskID, taskID, dir, projID)
	if err != nil {
		t.Fatalf("getOrCreate: %v", err)
	}

	// Create dummy conn pointers to simulate three clients.
	// Use a non-empty struct so Go doesn't coalesce allocations.
	type dummyConn struct{ id int }
	clientA := &dummyConn{id: 1} // desktop
	clientB := &dummyConn{id: 2} // tablet
	clientC := &dummyConn{id: 3} // phone

	aPtr := (*websocket.Conn)(unsafe.Pointer(clientA))
	bPtr := (*websocket.Conn)(unsafe.Pointer(clientB))
	cPtr := (*websocket.Conn)(unsafe.Pointer(clientC))

	// Three clients connect with different sizes
	sess.handleResize(aPtr, 40, 120) // desktop
	sess.handleResize(bPtr, 30, 80)  // tablet
	sess.handleResize(cPtr, 20, 40)  // phone

	// PTY should match smallest width (phone: 40 cols)
	_, cols, _ := pty.Getsize(sess.ptmx)
	if cols != 40 {
		t.Fatalf("with 3 clients cols = %d, want 40 (smallest)", cols)
	}

	// Phone disconnects → tablet should win (80 cols)
	sess.removeConnSize(cPtr)
	_, cols, _ = pty.Getsize(sess.ptmx)
	if cols != 80 {
		t.Fatalf("after phone leaves cols = %d, want 80 (tablet)", cols)
	}

	// Tablet disconnects → desktop should win (120 cols)
	sess.removeConnSize(bPtr)
	_, cols, _ = pty.Getsize(sess.ptmx)
	if cols != 120 {
		t.Fatalf("after tablet leaves cols = %d, want 120 (desktop)", cols)
	}

	// Last client disconnects → should revert to default
	sess.removeConnSize(aPtr)
	_, cols, _ = pty.Getsize(sess.ptmx)
	if cols != 120 {
		t.Fatalf("after all leave cols = %d, want 120 (default)", cols)
	}
}

func TestTaskTerminalDoesNotStopContainerOnExit(t *testing.T) {
	dir := t.TempDir()
	s, cs := newTestConfigStore(t)
	mock := newMockSandbox(t)
	tm := NewTerminalManager(s, cs, mock, nil)

	// Create a project with sandbox enabled
	projID := "test-proj-no-stop"
	proj := &types.Project{
		ID:             projID,
		Name:           "Test Project",
		Path:           dir,
		SandboxEnabled: true,
	}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create a task-level terminal session
	taskID := "test-task-no-stop"

	sess, err := tm.getOrCreate(taskID, taskID, dir, projID)
	if err != nil {
		t.Fatalf("getOrCreate: %v", err)
	}

	// Simulate shell exit by closing the PTY
	sess.mu.Lock()
	if sess.shell != nil {
		sess.shell.Ptmx.Close()
		sess.shell.Cmd.Process.Kill()
		sess.shell.Cmd.Wait()
	} else {
		sess.ptmx.Close()
		sess.cmd.Process.Kill()
		sess.cmd.Wait()
	}
	sess.mu.Unlock()

	// Wait for session to be removed, then verify Stop was NOT called.
	// Small delay to ensure onExit has fully completed.
	done := make(chan struct{})
	go func() {
		for {
			tm.mu.RLock()
			_, ok := tm.sessions[taskID]
			tm.mu.RUnlock()
			if !ok {
				// Give onExit a moment to finish all its work
				time.Sleep(100 * time.Millisecond)
				close(done)
				return
			}
		}
	}()

	select {
	case <-done:
		// Session was removed, onExit was called
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for session to be removed")
	}

	// Verify Stop was NOT called for task-level terminal
	// (task-level containers are managed by the task executor lifecycle)
	if mock.wasStopped(taskID) {
		t.Error("expected Stop to NOT be called for task-level terminal session")
	}
}
