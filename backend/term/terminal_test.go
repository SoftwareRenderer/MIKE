package term

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"kanban/backend/config"
	"kanban/backend/sandbox"
	"kanban/backend/store"
	"kanban/backend/types"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

// newTestConn creates a real websocket.Conn for testing.
// Returns a distinct connection each call — suitable as a map key.
func newTestConn(t *testing.T) *websocket.Conn {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusInternalError, "")
		for {
			_, _, err := conn.Read(r.Context())
			if err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	u, _ := url.Parse(server.URL)
	u.Scheme = "ws"
	conn, _, err := websocket.Dial(context.Background(), u.String(), nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	t.Cleanup(func() { conn.Close(websocket.StatusInternalError, "") })
	return conn
}

// mockSandbox implements sandboxOps for testing without gVisor.
type mockSandbox struct {
	mu        sync.Mutex
	available bool
	shells    map[string]*mockShell // taskID -> mockShell
	stopped   map[string]bool       // taskIDs that had Stop() called
	stopMu    sync.Mutex
	// imageErr, when set, is what EnsureImageForProject reports.
	imageErr      error
	ensureCalls   int
	startShellErr error
	// lastOpts is what the most recent StartShell was asked to create its container with — the only
	// way to see whether the configuration assembled for a shell is the configuration it gets.
	lastOpts sandbox.SandboxOpts
}

type mockShell struct {
	cmd  *exec.Cmd
	ptmx *os.File
}

func newMockSandbox(t *testing.T) *mockSandbox {
	m := &mockSandbox{
		available: true,
		shells:    make(map[string]*mockShell),
		stopped:   make(map[string]bool),
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

// EnsureImageForProject stands in for the shared build/pull step: it reports imageErr when set, and
// counts calls so a test can assert the image is prepared once per session.
func (m *mockSandbox) EnsureImageForProject(project *types.Project, _ func(types.SandboxImageStatus)) (string, error) {
	m.mu.Lock()
	m.ensureCalls++
	err := m.imageErr
	m.mu.Unlock()
	if err != nil {
		return "", err
	}
	return sandbox.ProjectImageName(project.Name), nil
}

func (m *mockSandbox) StartShell(taskID, worktreePath string, project *types.Project, opts sandbox.SandboxOpts) (*sandbox.ShellProcess, []types.AssignedPort, error) {
	if m.startShellErr != nil {
		return nil, nil, m.startShellErr
	}
	// A real PTY, so the readLoop is actually running.
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
	m.lastOpts = opts
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
	cs.SetWorkspaceDirOverride(dir)
	return s, cs
}

// A sandbox terminal must not open on a missing image: the user would get an empty shell whose
// every command fails, so the failure is reported instead.
func TestTerminalReportsImageFailure(t *testing.T) {
	dir := t.TempDir()
	s, cs := newTestConfigStore(t)
	mock := newMockSandbox(t)
	imageErr := errors.New("project Dockerfile /srv/Dockerfile is not readable: no such file or directory")
	mock.imageErr = imageErr
	tm := NewTerminalManager(s, cs, mock, nil)

	proj := &types.Project{ID: "test-proj-image", Name: "Image Project", Path: dir, SandboxEnabled: true}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	sessionKey := "proj:" + proj.ID
	sess, err := tm.getOrCreate(sessionKey, sessionKey, dir, proj.ID)
	if err == nil {
		t.Fatal("getOrCreate must fail when the project's sandbox image cannot be prepared")
	}
	if sess != nil {
		t.Error("no session may be returned when the image failed")
	}
	if !strings.Contains(err.Error(), "not readable") {
		t.Errorf("error = %v, want it to carry the image failure for the user", err)
	}
	if mock.ensureCalls != 1 {
		t.Errorf("EnsureImageForProject called %d times, want 1", mock.ensureCalls)
	}
	mock.mu.Lock()
	shells := len(mock.shells)
	mock.mu.Unlock()
	if shells != 0 {
		t.Errorf("StartShell was attempted %d times despite the image failure", shells)
	}

	tm.mu.RLock()
	_, registered := tm.sessions[sessionKey]
	tm.mu.RUnlock()
	if registered {
		t.Error("no session may be registered when the image failed")
	}
}

func TestProjectTerminalStopsContainerOnExit(t *testing.T) {
	dir := t.TempDir()
	s, cs := newTestConfigStore(t)
	mock := newMockSandbox(t)
	tm := NewTerminalManager(s, cs, mock, nil)

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

	sessionKey := "proj:" + projID
	sess, err := tm.getOrCreate(sessionKey, sessionKey, dir, projID)
	if err != nil {
		t.Fatalf("getOrCreate: %v", err)
	}

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

	// Wait for BOTH session removal AND the Stop() call: they happen in sequence inside
	// onExit, and reading only one of them races the other.
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

	rows, cols, err := pty.Getsize(sess.ptmx)
	if err != nil {
		t.Fatalf("get initial winsize: %v", err)
	}
	if cols != 120 || rows != 40 {
		t.Fatalf("initial size = %dx%d, want 120x40", cols, rows)
	}

	// Real connections: each is a distinct map key.
	desktopPtr := newTestConn(t)
	phonePtr := newTestConn(t)

	// Simulate desktop client resize: 80x200
	sess.handleResize(desktopPtr, 80, 200)
	rows, cols, _ = pty.Getsize(sess.ptmx)
	if cols != 200 || rows != 80 {
		t.Fatalf("after desktop resize size = %dx%d, want 200x80", cols, rows)
	}

	// Simulate phone client resize: 30x100 — smaller width AND height should win
	sess.handleResize(phonePtr, 30, 100)
	rows, cols, _ = pty.Getsize(sess.ptmx)
	if cols != 100 || rows != 30 {
		t.Fatalf("after phone resize size = %dx%d, want 100x30 (smallest on both axes)", cols, rows)
	}

	// Desktop resizes again to even wider/taller — should NOT change PTY (phone is still smallest)
	sess.handleResize(desktopPtr, 50, 300)
	rows, cols, _ = pty.Getsize(sess.ptmx)
	if cols != 100 || rows != 30 {
		t.Fatalf("after desktop larger resize size = %dx%d, want 100x30 (phone still smallest)", cols, rows)
	}

	// Phone disconnects — desktop size should take over
	sess.removeConnSize(phonePtr)
	rows, cols, _ = pty.Getsize(sess.ptmx)
	if cols != 300 || rows != 50 {
		t.Fatalf("after phone disconnect size = %dx%d, want 300x50 (desktop now only client)", cols, rows)
	}

	// Desktop also disconnects — should revert to default
	sess.removeConnSize(desktopPtr)
	rows, cols, _ = pty.Getsize(sess.ptmx)
	if cols != 120 || rows != 40 {
		t.Fatalf("after all disconnect size = %dx%d, want 120x40 (default)", cols, rows)
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

	// Create real websocket connections to get distinct map keys
	aPtr := newTestConn(t) // desktop
	bPtr := newTestConn(t) // tablet
	cPtr := newTestConn(t) // phone

	// Three clients connect with different sizes
	sess.handleResize(aPtr, 40, 120) // desktop
	sess.handleResize(bPtr, 30, 80)  // tablet
	sess.handleResize(cPtr, 20, 40)  // phone

	// PTY should match the smallest size on both axes (phone: 20 rows x 40 cols)
	rows, cols, _ := pty.Getsize(sess.ptmx)
	if cols != 40 || rows != 20 {
		t.Fatalf("with 3 clients size = %dx%d, want 40x20 (smallest)", cols, rows)
	}

	// Phone disconnects → tablet should win (30x80)
	sess.removeConnSize(cPtr)
	rows, cols, _ = pty.Getsize(sess.ptmx)
	if cols != 80 || rows != 30 {
		t.Fatalf("after phone leaves size = %dx%d, want 80x30 (tablet)", cols, rows)
	}

	// Tablet disconnects → desktop should win (40x120)
	sess.removeConnSize(bPtr)
	rows, cols, _ = pty.Getsize(sess.ptmx)
	if cols != 120 || rows != 40 {
		t.Fatalf("after tablet leaves size = %dx%d, want 120x40 (desktop)", cols, rows)
	}

	// Last client disconnects → should revert to default
	sess.removeConnSize(aPtr)
	rows, cols, _ = pty.Getsize(sess.ptmx)
	if cols != 120 || rows != 40 {
		t.Fatalf("after all leave size = %dx%d, want 120x40 (default)", cols, rows)
	}
}

func TestClampDim(t *testing.T) {
	cases := []struct {
		in   int
		want uint16
	}{
		{-5, 1}, // negative wraps through uint16 — clamp up
		{0, 1},  // zero is invalid for a PTY dimension
		{1, 1},
		{80, 80},
		{1000, 1000},
		{1001, 1000}, // oversized — clamp down
		{65535, 1000},
	}
	for _, c := range cases {
		if got := clampDim(c.in); got != c.want {
			t.Errorf("clampDim(%d) = %d, want %d", c.in, got, c.want)
		}
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

	// The delay lets onExit finish: removal happens before the Stop decision.
	done := make(chan struct{})
	go func() {
		for {
			tm.mu.RLock()
			_, ok := tm.sessions[taskID]
			tm.mu.RUnlock()
			if !ok {
				time.Sleep(100 * time.Millisecond)
				close(done)
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for session to be removed")
	}

	// The task executor owns a task container's lifecycle, not the terminal.
	if mock.wasStopped(taskID) {
		t.Error("expected Stop to NOT be called for task-level terminal session")
	}
}

func TestHandleTerminal_RejectsUnknownOrTraversalTask(t *testing.T) {
	s, cs := newTestConfigStore(t)
	mock := newMockSandbox(t)
	tm := NewTerminalManager(s, cs, mock, nil)

	// A non-existent task (including path-traversal strings) must return 404
	// before any shell/websocket is created, so a traversal taskID cannot be
	// used to open a shell with a cwd outside the workspace.
	for _, taskID := range []string{"../../../../etc", "no-such-task", "../other"} {
		req := httptest.NewRequest(http.MethodGet, "/api/projects/p1/tasks/"+taskID+"/terminal", nil)
		req.SetPathValue("projectID", "p1")
		req.SetPathValue("taskID", taskID)
		w := httptest.NewRecorder()
		tm.HandleTerminal(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("taskID %q: status = %d, want 404", taskID, w.Code)
		}
	}
}

func TestHandleTerminal_RejectsTaskFromAnotherProject(t *testing.T) {
	s, cs := newTestConfigStore(t)
	mock := newMockSandbox(t)
	tm := NewTerminalManager(s, cs, mock, nil)

	// A task that exists but belongs to a different project must be rejected.
	projID := "project-a"
	if err := s.CreateProject(&types.Project{ID: projID, Name: "A", Path: t.TempDir(), SandboxEnabled: true}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	taskID := "real-task"
	now := time.Now().UTC()
	if err := s.CreateTask(&types.Task{
		ID:             taskID,
		ProjectID:      projID,
		Status:         types.StatusBacklog,
		ExecutionState: types.ExecStateIdle,
		CreatedAt:      now,
		UpdatedAt:      now,
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/projects/other/tasks/"+taskID+"/terminal", nil)
	req.SetPathValue("projectID", "other")
	req.SetPathValue("taskID", taskID)
	w := httptest.NewRecorder()
	tm.HandleTerminal(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-project task: status = %d, want 404", w.Code)
	}
}

// wsUpgradeStatus performs a raw WebSocket upgrade handshake against urlStr and
// returns the HTTP status the server answered with (101 = accepted). The Origin
// header is set verbatim when non-empty, so the test observes exactly what the
// library's same-host Origin verification does.
func wsUpgradeStatus(t *testing.T, urlStr, origin string) int {
	t.Helper()

	u, err := url.Parse(urlStr)
	if err != nil {
		t.Fatalf("parse %q: %v", urlStr, err)
	}
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatalf("dial %s: %v", u.Host, err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodGet, urlStr, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", base64.StdEncoding.EncodeToString([]byte("0123456789abcdef")))
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if err := req.Write(conn); err != nil {
		t.Fatalf("write handshake: %v", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatalf("read handshake response: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// TestHandleTerminal_OriginVerification: the endpoint that hands out a shell verifies the
// WebSocket Origin, so a cross-site page cannot ride the session cookie into a shell. Cookies are
// not involved here — this isolates the Origin check itself.
func TestHandleTerminal_OriginVerification(t *testing.T) {
	s, cs := newTestConfigStore(t)
	mock := newMockSandbox(t)
	tm := NewTerminalManager(s, cs, mock, nil)

	projID := "origin-proj"
	if err := s.CreateProject(&types.Project{ID: projID, Name: "OriginProj", Path: t.TempDir(), SandboxEnabled: true}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	taskID := "origin-task"
	now := time.Now().UTC()
	if err := s.CreateTask(&types.Task{
		ID: taskID, ProjectID: projID, Status: types.StatusInProgress,
		ExecutionState: types.ExecStateIdle, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	// The terminal's cwd must exist for the (mock) shell to start.
	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.WorkspaceDir, projID, taskID), 0o755); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("projectID", projID)
		r.SetPathValue("taskID", taskID)
		tm.HandleTerminal(w, r)
	}))
	t.Cleanup(srv.Close)

	t.Run("same-host origin is accepted", func(t *testing.T) {
		if code := wsUpgradeStatus(t, srv.URL, srv.URL); code != http.StatusSwitchingProtocols {
			t.Errorf("same-host origin: status = %d, want 101", code)
		}
	})

	t.Run("cross-site origin is rejected", func(t *testing.T) {
		for _, origin := range []string{"http://evil.example", "https://evil.example", "http://localhost:1234", "null"} {
			if code := wsUpgradeStatus(t, srv.URL, origin); code != http.StatusForbidden {
				t.Errorf("origin %q: status = %d, want 403", origin, code)
			}
		}
	})
}

// TestTerminalShellOptsCarryTheProjectConfiguration: what a sandboxed shell's container is created with
// is the project's own Sandbox Configuration — image, isolation, mounts, envs. It is not a place an
// install-wide grant gets grafted on: a directory handed to an LSP extension is bound into that
// extension's own sandbox and nowhere else, and a shell that needs the same folder gets it the way a
// task does, from the project's own mount list.
func TestTerminalShellOptsCarryTheProjectConfiguration(t *testing.T) {
	proj := &types.Project{
		ID: "test-proj-shell-opts", Name: "Shell Opts Project", Path: t.TempDir(), SandboxEnabled: true,
		SandboxEnvs:             []types.SandboxEnv{{Key: "GOFLAGS", Value: "-mod=mod"}},
		SandboxMounts:           []types.SandboxMount{{HostPath: "/tmp/data", ContainerPath: "/data", Mode: "ro"}},
		SandboxMemoryLimit:      2048,
		SandboxNetworkIsolation: true,
	}

	tm := NewTerminalManager(nil, nil, nil, nil)
	got := tm.shellSandboxOpts("localhost/sandbox-example.com", proj, nil)

	if got.BaseImage != "localhost/sandbox-example.com" {
		t.Errorf("BaseImage = %q, want the image the caller passed in", got.BaseImage)
	}
	if !got.NetworkIsolation || got.MemoryLimit != 2048 {
		t.Errorf("isolation/limit = %v/%d, want the project's settings", got.NetworkIsolation, got.MemoryLimit)
	}
	if len(got.SandboxEnvs) != 1 || got.SandboxEnvs[0].Key != "GOFLAGS" {
		t.Errorf("SandboxEnvs = %v, want the project's envs", got.SandboxEnvs)
	}
	if len(got.SandboxMounts) != 1 || got.SandboxMounts[0].ContainerPath != "/data" {
		t.Errorf("SandboxMounts = %+v, want the project's mounts", got.SandboxMounts)
	}
}

// TestTerminalManagerClose is the regression for "shutdown leaves shells running": two real host
// PTY sessions, and after Close both the registry and the child processes must be gone.
func TestTerminalManagerClose(t *testing.T) {
	dir := t.TempDir()
	s, cs := newTestConfigStore(t)
	tm := NewTerminalManager(s, cs, newMockSandbox(t), nil)
	projID := "close-proj"
	if err := s.CreateProject(&types.Project{ID: projID, Name: "Close Project", Path: dir}); err != nil {
		t.Fatalf("create project: %v", err)
	}

	pids := make([]int, 0, 2)
	for _, taskID := range []string{"close-task-1", "close-task-2"} {
		sess, err := tm.getOrCreate(taskID, taskID, dir, projID)
		if err != nil {
			t.Fatalf("getOrCreate %s: %v", taskID, err)
		}
		if sess.cmd == nil || sess.cmd.Process == nil {
			t.Fatalf("session %s has no host process to kill", taskID)
		}
		pids = append(pids, sess.cmd.Process.Pid)
	}
	if got := tm.activeSessionCount(); got != 2 {
		t.Fatalf("sessions before Close = %d, want 2", got)
	}

	tm.Close()

	if got := tm.activeSessionCount(); got != 0 {
		t.Errorf("sessions after Close = %d, want 0", got)
	}
	// A killed-but-unwaited process is still a live process to anything that looks, and readLoop is
	// what reaps it — so poll for a moment rather than asserting immediately.
	deadline := time.Now().Add(3 * time.Second)
	for _, pid := range pids {
		for {
			proc, err := os.FindProcess(pid)
			if err != nil {
				break // reaped
			}
			if err := proc.Signal(syscall.Signal(0)); err != nil { // signal 0 probes, it does not kill
				break
			}
			if time.Now().After(deadline) {
				t.Errorf("pid %d is still alive after Close — the shell outlived the shutdown", pid)
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	// Shutdown may call Close twice (SIGTERM, then deferred cleanup).
	tm.Close()
}
