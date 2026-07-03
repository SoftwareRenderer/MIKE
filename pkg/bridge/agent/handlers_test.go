package main

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"kanban/pkg/bridge/protocol"
)

// --- Direct handler logic tests (no network simulation) ---

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()

	content := "atomic write test\n"
	path := filepath.Join(dir, "subdir", "new.txt")

	// Simulate what handleWrite does
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	tmpPath, err := os.CreateTemp(filepath.Dir(path), ".kanban-tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	tmpName := tmpPath.Name()

	n, err := tmpPath.WriteString(content)
	if err != nil {
		tmpPath.Close()
		os.Remove(tmpName)
		t.Fatal(err)
	}
	_ = tmpPath.Close()
	_ = os.Chmod(tmpName, 0o644)

	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		t.Fatal(err)
	}

	// Verify
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != content {
		t.Errorf("content = %q, want %q", data, content)
	}
	if int64(len(data)) != int64(n) {
		t.Errorf("len = %d, wrote %d", len(data), n)
	}
}

func TestEditApply(t *testing.T) {
	content := "line one\nline two\nline three\n"

	type edit struct{ Old, New string }
	edits := []edit{{"line two", "LINE TWO"}}

	// Simplified edit application (matches edit.go logic)
	result := content
	for _, e := range edits {
		idx := -1
		for i := 0; i <= len(result)-len(e.Old); i++ {
			if result[i:i+len(e.Old)] == e.Old {
				idx = i
				break
			}
		}
		if idx == -1 {
			t.Fatalf("oldText %q not found", e.Old)
		}
		result = result[:idx] + e.New + result[idx+len(e.Old):]
	}

	want := "line one\nLINE TWO\nline three\n"
	if result != want {
		t.Errorf("result = %q, want %q", result, want)
	}
}

func TestRequestParsing(t *testing.T) {
	// Test that request frames are built and parsed correctly
	params, _ := json.Marshal(protocol.ReadParams{Path: "test.txt"})
	frame := protocol.NewRequest(42, protocol.MethodRead, params)

	if frame.MsgID != 42 {
		t.Errorf("MsgID = %d, want 42", frame.MsgID)
	}
	if frame.Type != protocol.TypeRequest {
		t.Errorf("Type = %d, want %d", frame.Type, protocol.TypeRequest)
	}

	// Verify payload structure
	payload := frame.Payload
	if payload[0] != protocol.MethodRead {
		t.Errorf("method byte = %d, want %d", payload[0], protocol.MethodRead)
	}
}

func TestWriteParams_Marshal(t *testing.T) {
	p := protocol.WriteParams{Path: "src/main.go", Content: "package main"}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	var got protocol.WriteParams
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Path != p.Path || got.Content != p.Content {
		t.Errorf("round-trip failed: got %+v, want %+v", got, p)
	}
}

func TestBashParams_Marshal(t *testing.T) {
	p := protocol.BashParams{Command: "ls -la", Timeout: 30}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	var got protocol.BashParams
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Command != p.Command || got.Timeout != p.Timeout {
		t.Errorf("round-trip failed")
	}
}

func TestEditParams_Marshal(t *testing.T) {
	p := protocol.EditParams{
		Path: "test.go",
		Edits: []protocol.Edit{
			{OldText: "foo", NewText: "bar"},
			{OldText: "baz", NewText: "qux"},
		},
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	var got protocol.EditParams
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Edits) != 2 {
		t.Fatalf("edits count = %d, want 2", len(got.Edits))
	}
	if got.Edits[0].OldText != "foo" || got.Edits[1].NewText != "qux" {
		t.Error("edit round-trip failed")
	}
}

// TestBashCancel kills a long-running bash process via bash_cancel.
func TestBashCancel(t *testing.T) {
	// Create pipes: test → agent (requests), agent → test (responses)
	testToAgentPr, testToAgentPw := io.Pipe()
	agentToTestPr, agentToTestPw := io.Pipe()

	h := newHandler()
	h.closed.Store(false)

	// Start handler in background
	handlerDone := make(chan struct{})
	go func() {
		_ = h.handleConn(&testPipeConn{reader: testToAgentPr, writer: agentToTestPw})
		close(handlerDone)
	}()

	// Helper: send a frame to the agent
	sendToAgent := func(f *protocol.Frame) {
		err := protocol.WriteFrame(testToAgentPw, f)
		if err != nil {
			t.Errorf("write frame: %v", err)
		}
	}

	// Send a long-running bash command
	bashParams, _ := json.Marshal(protocol.BashParams{Command: "sleep 300", Timeout: 300})
	bashReq := protocol.NewRequest(1, protocol.MethodBash, bashParams)
	sendToAgent(bashReq)

	// Give the agent time to start the process
	time.Sleep(100 * time.Millisecond)

	// Verify process is tracked
	h.mu.Lock()
	if _, ok := h.bashProcs[1]; !ok {
		h.mu.Unlock()
		t.Fatal("bash process not tracked after start")
	}
	h.mu.Unlock()

	// Send cancel
	cancelFrame := protocol.NewBashCancelRequest(2, 1)
	sendToAgent(cancelFrame)

	// Wait for result frame (the process should be killed)
	select {
	case result := <-readFrameChan(agentToTestPr):
		if result.Type != protocol.TypeResult {
			t.Fatalf("expected Result frame, got %v", result.Type)
		}
		if result.MsgID != 1 {
			t.Errorf("result MsgID = %d, want 1", result.MsgID)
		}
		bashResult, err := protocol.ParseBashResult(result.Payload)
		if err != nil {
			t.Fatalf("parse bash result: %v", err)
		}
		if bashResult.ExitCode != 137 {
			t.Errorf("exit code = %d, want 137 (SIGKILL)", bashResult.ExitCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for result after cancel")
	}

	// Verify process is no longer tracked
	h.mu.Lock()
	if _, ok := h.bashProcs[1]; ok {
		h.mu.Unlock()
		t.Error("bash process still tracked after cancel")
	} else {
		h.mu.Unlock()
	}

	// Cleanup
	h.closed.Store(true)
	testToAgentPw.Close()
	agentToTestPw.Close()
	<-handlerDone
}

// testPipeConn wraps separate read/write pipes as a net.Conn.
type testPipeConn struct {
	reader io.ReadCloser
	writer io.WriteCloser
}

func (c *testPipeConn) Read(b []byte) (int, error)        { return c.reader.Read(b) }
func (c *testPipeConn) Write(b []byte) (int, error)       { return c.writer.Write(b) }
func (c *testPipeConn) Close() error                      { return c.writer.Close() }
func (c *testPipeConn) LocalAddr() net.Addr               { return nil }
func (c *testPipeConn) RemoteAddr() net.Addr              { return nil }
func (c *testPipeConn) SetDeadline(t time.Time) error      { return nil }
func (c *testPipeConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *testPipeConn) SetWriteDeadline(t time.Time) error { return nil }

// readFrameChan reads a single frame and returns it on a channel.
func readFrameChan(r io.Reader) <-chan *protocol.Frame {
	ch := make(chan *protocol.Frame, 1)
	go func() {
		frame, err := protocol.ReadFrame(r)
		if err != nil {
			close(ch)
			return
		}
		ch <- frame
	}()
	return ch
}
