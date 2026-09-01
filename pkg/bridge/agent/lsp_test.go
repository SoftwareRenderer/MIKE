package main

// A language-server session belongs to the bridge connection that started it, and only that
// connection may end it: a start must not destroy a running server, a request without one must
// be refused, and the connection ending must take its server along.

import (
	"encoding/json"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"

	"kanban/pkg/bridge/protocol"
)

// fakeLanguageServer is a command that stays alive reading stdin. These tests only ask a session
// to exist, be recognised, and die; LSP protocol behaviour is gopls' business, not the bridge's.
var fakeLanguageServer = "cat"

// lspHandler returns a handler plus the two ends of its connection. The handler writes on one end
// and the test reads on the other: net.Pipe is unbuffered, so reading and writing the same end
// deadlocks against yourself.
func lspHandler(t *testing.T) (*handler, net.Conn, net.Conn) {
	t.Helper()
	agentEnd, testEnd := net.Pipe()
	h := newHandler()
	h.closed.Store(false) // caller marks the connection ready (see newHandler)
	t.Cleanup(func() {
		h.close()
		agentEnd.Close()
		testEnd.Close()
	})
	return h, agentEnd, testEnd
}

// lspRequest drives one bridge LSP frame through the handler and returns the answer.
func lspRequest(t *testing.T, h *handler, agentEnd, testEnd net.Conn, msgID uint32, params protocol.LSPParams) *protocol.Frame {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	req, err := protocol.ParseRequest(protocol.NewRequest(msgID, protocol.MethodLSP, raw))
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}

	done := make(chan *protocol.Frame, 1)
	go func() {
		frame, err := protocol.ReadFrame(testEnd)
		if err != nil {
			t.Errorf("read reply: %v", err)
			done <- nil
			return
		}
		done <- frame
	}()

	h.handleLSP(agentEnd, req)

	select {
	case frame := <-done:
		return frame
	case <-time.After(3 * time.Second):
		t.Fatal("no reply from the handler")
		return nil
	}
}

// currentSession returns the connection's session, or nil. Read under the handler lock: the
// handler writes h.lsp from the request goroutine, and -race is not wrong to notice.
func currentSession(h *handler) *lspSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lsp == nil {
		return nil
	}
	return h.lsp.session
}

func sessionPID(h *handler) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lsp == nil || h.lsp.session == nil || h.lsp.session.cmd == nil || h.lsp.session.cmd.Process == nil {
		return 0
	}
	return h.lsp.session.cmd.Process.Pid
}

// TestLSPStart_DoesNotReplaceALiveSession: starting the same server again gets the same session,
// and asking for a different one is refused instead of taking over the running server.
func TestLSPStart_DoesNotReplaceALiveSession(t *testing.T) {
	h, agentEnd, testEnd := lspHandler(t)

	first := lspRequest(t, h, agentEnd, testEnd, 1, protocol.LSPParams{Command: fakeLanguageServer, Args: []string{"-"}})
	if first.Type == protocol.TypeError {
		t.Fatalf("first start failed: %s", first.ErrorMsg())
	}
	pid := sessionPID(h)
	if pid == 0 {
		t.Fatal("no server process after start")
	}
	session := currentSession(h)

	again := lspRequest(t, h, agentEnd, testEnd, 2, protocol.LSPParams{Command: fakeLanguageServer, Args: []string{"-"}})
	if again.Type == protocol.TypeError {
		t.Errorf("restarting the same server was refused: %s", again.ErrorMsg())
	}
	if currentSession(h) != session || sessionPID(h) != pid {
		t.Error("a repeated start replaced the running session")
	}

	other := lspRequest(t, h, agentEnd, testEnd, 3, protocol.LSPParams{Command: "sleep", Args: []string{"30"}})
	if other.Type != protocol.TypeError {
		t.Errorf("start of a different command answered %v, want an error", other.Type)
	}
	if currentSession(h) != session || sessionPID(h) != pid {
		t.Error("a refused start disturbed the running session")
	}

	// Different arguments mean a different server, so this is refused rather than swapped in.
	diffArgs := lspRequest(t, h, agentEnd, testEnd, 4, protocol.LSPParams{Command: fakeLanguageServer, Args: []string{"-u"}})
	if diffArgs.Type != protocol.TypeError {
		t.Errorf("start with different args answered %v, want an error", diffArgs.Type)
	}
	if currentSession(h) != session || sessionPID(h) != pid {
		t.Error("a refused start replaced the running session")
	}
}

// TestLSPSessionsAreIndependentAcrossConnections: each connection gets its own server process,
// and one connection ending leaves the other's server standing.
func TestLSPSessionsAreIndependentAcrossConnections(t *testing.T) {
	h1, agent1, test1 := lspHandler(t)
	h2, agent2, test2 := lspHandler(t)

	start := func(t *testing.T, h *handler, agentEnd, testEnd net.Conn) {
		t.Helper()
		if got := lspRequest(t, h, agentEnd, testEnd, 1, protocol.LSPParams{Command: fakeLanguageServer}); got.Type == protocol.TypeError {
			t.Fatalf("start: %s", got.ErrorMsg())
		}
	}
	start(t, h1, agent1, test1)
	pid1, session1 := sessionPID(h1), currentSession(h1)
	if pid1 == 0 || session1 == nil {
		t.Fatal("no server process on the first connection")
	}
	start(t, h2, agent2, test2)
	if sessionPID(h2) == pid1 {
		t.Fatal("both connections were handed the same process")
	}

	// The second connection's start left the first one's session and process alone.
	if currentSession(h1) != session1 || sessionPID(h1) != pid1 {
		t.Error("a start on another connection took over this connection's session")
	}

	// The first connection still answers.
	if got := lspRequest(t, h1, agent1, test1, 2, protocol.LSPParams{Raw: []byte(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)}); got.Type == protocol.TypeError {
		t.Fatalf("request after another connection started its server: %s", got.ErrorMsg())
	}

	// Closing one connection reaps only its own server.
	h2.close()
	if !processAlive(pid1) {
		t.Error("closing another connection killed this connection's server")
	}
	if got := lspRequest(t, h1, agent1, test1, 3, protocol.LSPParams{Raw: []byte(`{"jsonrpc":"2.0","id":3,"method":"ping"}`)}); got.Type == protocol.TypeError {
		t.Errorf("request after another connection closed: %s", got.ErrorMsg())
	}
}

// TestLSPRequest_WithoutASessionIsRefused: a request with no server answers with an error the
// host can act on. Restarting one here would produce an uninitialized server that replies to a
// document with a warning about itself instead of an error.
func TestLSPRequest_WithoutASessionIsRefused(t *testing.T) {
	h, agentEnd, testEnd := lspHandler(t)

	reply := lspRequest(t, h, agentEnd, testEnd, 7, protocol.LSPParams{
		Raw: json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"textDocument/hover","params":{}}`),
	})
	if reply.Type != protocol.TypeError {
		t.Fatalf("reply type = %v, want an error", reply.Type)
	}
	if reply.ErrorMsg() != "lsp server not started" {
		t.Errorf("error = %q, want %q", reply.ErrorMsg(), "lsp server not started")
	}
	if pid := sessionPID(h); pid != 0 {
		t.Errorf("a request with no session started process %d; only a start request may start one", pid)
	}
}

// TestHandlerClose_KillsItsLSPSession: ending the bridge connection ends its server. A server
// left behind is stuck in the sandbox for the life of the container.
func TestHandlerClose_KillsItsLSPSession(t *testing.T) {
	h, agentEnd, testEnd := lspHandler(t)

	start := lspRequest(t, h, agentEnd, testEnd, 1, protocol.LSPParams{Command: fakeLanguageServer})
	if start.Type == protocol.TypeError {
		t.Fatalf("start failed: %s", start.ErrorMsg())
	}
	session := currentSession(h)
	pid := sessionPID(h)

	h.close()

	if !session.isClosed() {
		t.Error("the connection ended with its language server still running")
	}
	// close() kills the process and reaps it, so the pid is gone rather than a zombie.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && processAlive(pid) {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Errorf("the language server process %d outlived the connection", pid)
	}

	h.close() // the deferred cleanup must not fall over on a handler that is already closed
}

// processAlive reports whether the pid can still be signalled. A reaped child cannot.
func processAlive(pid int) bool {
	if pid == 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
