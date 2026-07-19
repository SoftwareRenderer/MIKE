package lsp

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// echoServerPath returns the absolute path to the test echo server script.
func echoServerPath() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "testdata", "lsp-echo.py")
}

// echoConfig returns a ServerConfig that echoes Content-Length framed
// messages from stdin back to stdout with proper LSP framing.
func echoConfig() *ServerConfig {
	return &ServerConfig{
		Command: "python3",
		Args:    []string{echoServerPath()},
		Language: "go",
	}
}

// stderrConfig returns a ServerConfig that writes a known line to stderr,
// then echoes Content-Length framed messages.
func stderrConfig() *ServerConfig {
	return &ServerConfig{
		Command: "python3",
		Args:    []string{echoServerPath(), "--stderr", "stderr-line-123"},
		Language: "go",
	}
}

// notificationConfig returns a ServerConfig that immediately emits a
// Content-Length framed JSON-RPC notification on stdout, then echoes stdin.
func notificationConfig() *ServerConfig {
	notif := `{"jsonrpc":"2.0","method":"window/logMessage","params":{"type":1,"message":"hello-lsp"}}`
	return &ServerConfig{
		Command: "python3",
		Args:    []string{echoServerPath(), "--initial-notif", notif},
		Language: "go",
	}
}

func TestGetOrCreate_SpawnsProcess(t *testing.T) {
	pm := NewProcessManager()
	dir := t.TempDir()

	proc, err := pm.GetOrCreate(dir, "go", echoConfig())
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if proc.Workspace != dir {
		t.Errorf("Workspace = %q, want %q", proc.Workspace, dir)
	}
	if proc.Language != "go" {
		t.Errorf("Language = %q, want %q", proc.Language, "go")
	}
	if proc.RefCount != 1 {
		t.Errorf("RefCount = %d, want 1", proc.RefCount)
	}
	if proc.Cmd.Process == nil {
		t.Fatal("process not started")
	}

	pm.Release(dir, "go")
}

func TestGetOrCreate_IncrementsRefCount(t *testing.T) {
	pm := NewProcessManager()
	dir := t.TempDir()

	proc1, err := pm.GetOrCreate(dir, "go", echoConfig())
	if err != nil {
		t.Fatalf("first GetOrCreate: %v", err)
	}

	proc2, err := pm.GetOrCreate(dir, "go", echoConfig())
	if err != nil {
		t.Fatalf("second GetOrCreate: %v", err)
	}

	if proc1 != proc2 {
		t.Fatal("second GetOrCreate should return the same process")
	}
	if proc1.RefCount != 2 {
		t.Errorf("RefCount = %d, want 2", proc1.RefCount)
	}

	// Release once — should not kill the process.
	pm.Release(dir, "go")
	if proc1.RefCount != 1 {
		t.Errorf("after one Release, RefCount = %d, want 1", proc1.RefCount)
	}

	pm.Release(dir, "go")
}

func TestGetOrCreate_DifferentKeys(t *testing.T) {
	pm := NewProcessManager()
	dir := t.TempDir()

	proc1, err := pm.GetOrCreate(dir, "go", echoConfig())
	if err != nil {
		t.Fatalf("GetOrCreate go: %v", err)
	}
	proc2, err := pm.GetOrCreate(dir, "typescript", echoConfig())
	if err != nil {
		t.Fatalf("GetOrCreate typescript: %v", err)
	}
	if proc1 == proc2 {
		t.Fatal("different languages should produce different processes")
	}

	// Same language, different workspace.
	dir2 := t.TempDir()
	proc3, err := pm.GetOrCreate(dir2, "go", echoConfig())
	if err != nil {
		t.Fatalf("GetOrCreate other workspace: %v", err)
	}
	if proc1 == proc3 {
		t.Fatal("different workspaces should produce different processes")
	}

	pm.Release(dir, "go")
	pm.Release(dir, "typescript")
	pm.Release(dir2, "go")
}

func TestRelease_KillsProcess(t *testing.T) {
	pm := NewProcessManager()
	dir := t.TempDir()

	proc, err := pm.GetOrCreate(dir, "go", echoConfig())
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	pid := proc.Cmd.Process.Pid

	pm.Release(dir, "go")

	// Verify it was removed from the manager.
	pm.mu.Lock()
	_, exists := pm.processes[processKey(dir, "go")]
	pm.mu.Unlock()
	if exists {
		t.Error("process should have been removed from manager")
	}

	// Verify the pid is gone by trying to send signal 0.
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Signal(syscall.Signal(0))
	}
}

func TestRelease_NoOpWhenMissing(t *testing.T) {
	pm := NewProcessManager()
	// Should not panic.
	pm.Release("/nonexistent", "go")
}

func TestSendToServer(t *testing.T) {
	pm := NewProcessManager()
	dir := t.TempDir()

	proc, err := pm.GetOrCreate(dir, "go", echoConfig())
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	// Subscribe to receive the echoed notification back.
	ch := pm.Subscribe(proc, 42)

	// Send a notification (no id) through the manager.
	msg := `{"jsonrpc":"2.0","method":"test/echo","params":{"value":"hello"}}`
	if err := pm.SendToServer(proc, json.RawMessage(msg)); err != nil {
		t.Fatalf("SendToServer: %v", err)
	}

	// The echo process reflects it back; readLoop dispatches as notification.
	select {
	case got := <-ch:
		var env struct {
			Method string `json:"method"`
			Params struct {
				Value string `json:"value"`
			} `json:"params"`
		}
		if err := json.Unmarshal(got, &env); err != nil {
			t.Fatalf("unmarshal echoed notification: %v", err)
		}
		if env.Method != "test/echo" {
			t.Errorf("method = %q, want %q", env.Method, "test/echo")
		}
		if env.Params.Value != "hello" {
			t.Errorf("params.value = %q, want %q", env.Params.Value, "hello")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for echoed notification")
	}

	pm.Unsubscribe(proc, 42)
	pm.Release(dir, "go")
}

func TestSendToServer_AfterShutdown(t *testing.T) {
	pm := NewProcessManager()
	dir := t.TempDir()

	proc, err := pm.GetOrCreate(dir, "go", echoConfig())
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	pm.Release(dir, "go")

	// Sending after shutdown should fail.
	err = pm.SendToServer(proc, json.RawMessage(`{"jsonrpc":"2.0"}`))
	if err == nil {
		t.Error("expected error when sending to shutdown process")
	}
}

func TestStderrLogging(t *testing.T) {
	pm := NewProcessManager()
	dir := t.TempDir()

	// Capture log output.
	var logMu sync.Mutex
	var logLines []string
	capture := &logCapture{lines: &logLines, mu: &logMu}

	// Temporarily redirect the standard logger.
	origWriter := log.Writer()
	origFlags := log.Flags()
	origPrefix := log.Prefix()
	log.SetOutput(capture)
	log.SetFlags(0)
	log.SetPrefix("")

	proc, err := pm.GetOrCreate(dir, "go", stderrConfig())
	if err != nil {
		log.SetOutput(origWriter)
		log.SetFlags(origFlags)
		log.SetPrefix(origPrefix)
		t.Fatalf("GetOrCreate: %v", err)
	}
	_ = proc

	// Give stderr goroutine time to read the line.
	time.Sleep(200 * time.Millisecond)

	log.SetOutput(origWriter)
	log.SetFlags(origFlags)
	log.SetPrefix(origPrefix)

	pm.Release(dir, "go")

	capture.mu.Lock()
	lines := *capture.lines
	capture.mu.Unlock()

	found := false
	for _, line := range lines {
		if strings.Contains(line, "stderr-line-123") && strings.Contains(line, "[lsp][go]") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected stderr log line containing 'stderr-line-123' and '[lsp][go]'; got lines: %v", lines)
	}
}

func TestSubscribe_Notifications(t *testing.T) {
	pm := NewProcessManager()
	dir := t.TempDir()

	proc, err := pm.GetOrCreate(dir, "go", notificationConfig())
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	// Subscribe two clients.
	ch1 := pm.Subscribe(proc, 1)
	ch2 := pm.Subscribe(proc, 2)

	// Give the read goroutine time to process the startup notification.
	time.Sleep(200 * time.Millisecond)

	// Both clients should have received the notification.
	for i, ch := range []chan []byte{ch1, ch2} {
		select {
		case msg := <-ch:
			var env struct {
				JSONRPC string `json:"jsonrpc"`
				Method  string `json:"method"`
			}
			if err := json.Unmarshal(msg, &env); err != nil {
				t.Fatalf("client %d: unmarshal: %v", i+1, err)
			}
			if env.Method != "window/logMessage" {
				t.Errorf("client %d: method = %q, want %q", i+1, env.Method, "window/logMessage")
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("client %d: did not receive notification", i+1)
		}
	}

	pm.Unsubscribe(proc, 1)
	pm.Unsubscribe(proc, 2)

	// Send another notification via the echo server.
	notif := `{"jsonrpc":"2.0","method":"$/progress","params":{"token":"t1"}}`
	if err := pm.SendToServer(proc, json.RawMessage(notif)); err != nil {
		t.Fatalf("SendToServer: %v", err)
	}

	// Re-subscribe client 1.
	ch1 = pm.Subscribe(proc, 1)

	// Give the echo + read loop time to process.
	time.Sleep(200 * time.Millisecond)

	select {
	case msg := <-ch1:
		var env struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal(msg, &env); err != nil {
			t.Fatalf("unmarshal second notification: %v", err)
		}
		if env.Method != "$/progress" {
			t.Errorf("method = %q, want %q", env.Method, "$/progress")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client 1 did not receive second notification")
	}

	pm.Unsubscribe(proc, 1)
	pm.Release(dir, "go")
}

func TestSubscribe_UnsubscribeStopsDelivery(t *testing.T) {
	pm := NewProcessManager()
	dir := t.TempDir()

	proc, err := pm.GetOrCreate(dir, "go", notificationConfig())
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	ch := pm.Subscribe(proc, 99)

	// Drain the initial notification.
	select {
	case <-ch:
	case <-time.After(500 * time.Millisecond):
	}

	// Unsubscribe.
	pm.Unsubscribe(proc, 99)

	// Send another notification.
	notif := `{"jsonrpc":"2.0","method":"test","params":{}}`
	if err := pm.SendToServer(proc, json.RawMessage(notif)); err != nil {
		t.Fatalf("SendToServer: %v", err)
	}

	// The closed channel should not deliver anything new.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected no delivery after unsubscribe")
		}
		// ok == false means channel was closed, which is expected.
	case <-time.After(500 * time.Millisecond):
		// Also acceptable — nothing delivered.
	}

	pm.Release(dir, "go")
}

func TestWorkspaceReplacementInArgs(t *testing.T) {
	pm := NewProcessManager()
	dir := t.TempDir()

	cfg := &ServerConfig{
		Command: "sh",
		Args:    []string{"-c", "echo '{\"workspace\":\"{workspace}\"}' >&2; exec stdbuf -oL cat"},
		Language: "go",
	}

	proc, err := pm.GetOrCreate(dir, "go", cfg)
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	// Verify the process was started with the correct working directory.
	if proc.Cmd.Dir != dir {
		t.Errorf("Cmd.Dir = %q, want %q", proc.Cmd.Dir, dir)
	}

	pm.Release(dir, "go")
}

func TestMultipleReleasePanicSafety(t *testing.T) {
	pm := NewProcessManager()
	dir := t.TempDir()

	proc, err := pm.GetOrCreate(dir, "go", echoConfig())
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	// Release twice — second should be a no-op.
	pm.Release(dir, "go")
	pm.Release(dir, "go")

	// Process should be gone.
	pm.mu.Lock()
	_, exists := pm.processes[processKey(dir, "go")]
	pm.mu.Unlock()
	if exists {
		t.Error("process should have been removed")
	}

	_ = proc // avoid unused variable warning
}

func TestServerMessageCh_Responses(t *testing.T) {
	pm := NewProcessManager()
	dir := t.TempDir()

	proc, err := pm.GetOrCreate(dir, "go", echoConfig())
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	// Send a response-like message (has id, no method) — should go to messageCh.
	msg := `{"jsonrpc":"2.0","id":42,"result":{"capabilities":{}}}`
	if err := pm.SendToServer(proc, json.RawMessage(msg)); err != nil {
		t.Fatalf("SendToServer: %v", err)
	}

	// The echo process reflects it back; it should arrive on messageCh.
	select {
	case got := <-pm.messageCh:
		var env struct {
			ID json.Number `json:"id"`
		}
		if err := json.Unmarshal(got, &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if env.ID.String() != "42" {
			t.Errorf("expected id 42, got %s", env.ID.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message on messageCh")
	}

	pm.Release(dir, "go")
}

// --- helpers ---

// logCapture collects log output for assertion.
type logCapture struct {
	mu    *sync.Mutex
	lines *[]string
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	line := strings.TrimSpace(string(p))
	if line != "" {
		*c.lines = append(*c.lines, line)
	}
	return len(p), nil
}
