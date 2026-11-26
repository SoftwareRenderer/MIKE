package main

import (
	"encoding/json/v2"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mike/pkg/bridge/protocol"
)

// runEditRequest drives handleEdit over a net.Pipe connection and returns the
// response frame the agent writes back.
func runEditRequest(t *testing.T, path string, edits []protocol.Edit) (*protocol.Frame, error) {
	t.Helper()
	h := newHandler()
	h.closed.Store(false) // caller marks the connection ready (see newHandler)
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	params, err := json.Marshal(protocol.EditParams{Path: path, Edits: edits})
	if err != nil {
		t.Fatal(err)
	}
	req := &protocol.Request{MsgID: 7, Method: protocol.MethodEdit, Params: params}

	go h.dispatch(serverConn, req)

	// Read the response frame from the client side of the pipe. dispatch writes
	// a result frame on success and an error frame on failure.
	frame, readErr := protocol.ReadFrame(clientConn)
	return frame, readErr
}

// TestHandleEdit holds the one contract of the edit tool: the replacements the file does hold are
// all applied and the bytes written are reported, and text it does not hold is refused with what the
// file actually contains — the model rewrites from that sentence, so a refusal that says nothing
// costs a retry.
func TestHandleEdit(t *testing.T) {
	write := func(t *testing.T, content string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "test.txt")
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("applies every edit and reports the bytes", func(t *testing.T) {
		path := write(t, "alpha\nbeta\ngamma\n")

		frame, err := runEditRequest(t, path, []protocol.Edit{
			{OldText: "alpha", NewText: "ALPHA"},
			{OldText: "gamma", NewText: "GAMMA"},
		})
		if err != nil {
			t.Fatalf("handleEdit: %v", err)
		}
		if frame.Type == protocol.TypeError {
			t.Fatalf("unexpected error frame: %s", frame.ErrorMsg())
		}
		if frame.Type != protocol.TypeResult {
			t.Fatalf("frame type = %d, want TypeResult", frame.Type)
		}
		// Result payload is the number of bytes written.
		const want = "ALPHA\nbeta\nGAMMA\n"
		if n, err := protocol.Uint64Result(frame.Payload); err != nil || n != uint64(len(want)) {
			t.Errorf("bytes written = %d (err %v), want %d", n, err, len(want))
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read edited file: %v", err)
		}
		if string(data) != want {
			t.Errorf("got %q, want %q", string(data), want)
		}
	})

	t.Run("refuses text the file does not hold", func(t *testing.T) {
		path := write(t, "hello world")

		frame, err := runEditRequest(t, path, []protocol.Edit{{OldText: "nope", NewText: "x"}})
		if err != nil {
			t.Fatalf("handleEdit: %v", err)
		}
		if frame.Type != protocol.TypeError {
			t.Fatalf("frame type = %d, want TypeError", frame.Type)
		}
		msg := frame.ErrorMsg()
		if !strings.Contains(msg, "not found in file") || !strings.Contains(msg, "Got 1 lines in file") {
			t.Errorf("unexpected error message: %q", msg)
		}
	})
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
		_ = h.handleConn(testToAgentPr, agentToTestPw)
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

	h.closed.Store(true)
	testToAgentPw.Close()
	agentToTestPw.Close()
	<-handlerDone
}

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
