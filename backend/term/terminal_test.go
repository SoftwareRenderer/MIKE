package term

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"kanban/backend/config"
	"kanban/backend/sandbox"
	"kanban/backend/store"
	"kanban/backend/types"

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

func (m *mockSandbox) StartShellWithCreate(taskID, worktreePath, baseImage, projectName, projectPath, projectID string, networkIsolation bool, memoryLimit int, forwardPorts []types.PortForward, piAgentDir string) (*sandbox.ShellProcess, []types.AssignedPort, error) {
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
	cs := config.NewConfigStore(s, dbPath)
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
	tm := NewTerminalManager(s, cs, mock)

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

func TestTaskTerminalDoesNotStopContainerOnExit(t *testing.T) {
	dir := t.TempDir()
	s, cs := newTestConfigStore(t)
	mock := newMockSandbox(t)
	tm := NewTerminalManager(s, cs, mock)

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
