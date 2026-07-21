package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"kanban/pkg/bridge/protocol"
)

// lspSession tracks a running LSP server process connected via unix socket.
type lspSession struct {
	cmd        *exec.Cmd
	conn       net.Conn        // connection to LSP server (unix socket)
	socketPath string          // unix socket path
	ready      chan struct{}   // closed when LSP server is accepting connections
	mu         sync.Mutex
	closed     bool
	pending    map[string]chan []byte // JSON-RPC id string -> response channel
	pendingMu  sync.Mutex
}

// isClosed returns true if the session has been closed.
func (s *lspSession) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// LSPParams holds the parameters for an LSP request.
type LSPParams struct {
	// Raw is the raw JSON-RPC message to forward to the LSP server.
	Raw json.RawMessage `json:"raw"`
	// Command is the LSP server command to start (only used for the first request).
	Command string `json:"command,omitempty"`
	// Args are the LSP server arguments (only used for the first request).
	Args []string `json:"args,omitempty"`
}

// newLSPSession creates a new LSP session by starting the LSP server with a unix socket.
func newLSPSession(command string, args []string) (*lspSession, error) {
	// Create a unique unix socket path in /tmp
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("lsp-%d-%d.sock", os.Getpid(), time.Now().UnixNano()))
	_ = os.Remove(socketPath) // clean up any stale socket

	// Build command with -listen flag for unix socket
	listenArgs := append([]string{command}, args...)
	listenArgs = append(listenArgs, "-listen", "unix;"+socketPath)

	cmd := exec.Command(listenArgs[0], listenArgs[1:]...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp stderr pipe: %w", err)
	}

	sess := &lspSession{
		cmd:        cmd,
		socketPath: socketPath,
		ready:      make(chan struct{}),
		pending:    make(map[string]chan []byte),
	}

	// Stream stderr to log
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			log.Printf("lsp[%s]: %s", command, scanner.Text())
		}
	}()

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start lsp %s: %w", command, err)
	}

	// Wait for the socket to be ready (poll with timeout)
	for i := 0; i < 50; i++ {
		time.Sleep(50 * time.Millisecond)
		if _, err := os.Stat(socketPath); err == nil {
			// Socket exists, try to connect
			conn, err := net.Dial("unix", socketPath)
			if err == nil {
				sess.conn = conn
				close(sess.ready)
				go sess.readLoop()
				return sess, nil
			}
			_ = conn.Close()
		}
	}

	// Timeout - clean up
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	_ = os.Remove(socketPath)
	return nil, fmt.Errorf("lsp server did not start listening on socket within 5s")
}

// readLoop reads JSON-RPC responses from the LSP server and dispatches them.
// Gopls uses HTTP-like Content-Length framing for socket connections.
func (s *lspSession) readLoop() {
	defer func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		if s.conn != nil {
			_ = s.conn.Close()
		}
		// Close all pending channels
		s.pendingMu.Lock()
		for id, ch := range s.pending {
			close(ch)
			delete(s.pending, id)
		}
		s.pendingMu.Unlock()
	}()

	reader := bufio.NewReader(s.conn)
	for {
		// Read Content-Length header
		contentLen := 0
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
		
				} else {
		
				}
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break // empty line marks end of headers
			}
			if strings.HasPrefix(line, "Content-Length: ") {
				fmt.Sscanf(line, "Content-Length: %d", &contentLen)
			}
		}
		if contentLen == 0 {
			continue
		}

		// Read the JSON-RPC message
		msg := make([]byte, contentLen)
		if _, err := io.ReadFull(reader, msg); err != nil {
	
			return
		}
	

		// Extract JSON-RPC id to match with pending request
		var envelope struct {
			ID *json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal(msg, &envelope); err != nil {
			// Notification or malformed — no pending match
			continue
		}
		if envelope.ID == nil {
			// Notification — no pending match
			continue
		}
		idKey := string(*envelope.ID)

		s.pendingMu.Lock()
		ch, ok := s.pending[idKey]
		if ok {
			delete(s.pending, idKey)
		}
		s.pendingMu.Unlock()

		if ok {
			select {
			case ch <- msg:
			default:
				// Channel full or goroutine already moved on
			}
		}
	}
}

// sendJSONRPC writes a JSON-RPC message with HTTP-like Content-Length framing.
func sendJSONRPC(w io.Writer, raw json.RawMessage) error {
	_, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(raw), raw)
	return err
}

// send sends a JSON-RPC message to the LSP server and waits for the response.
func (s *lspSession) send(ctx context.Context, raw json.RawMessage) ([]byte, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("lsp session closed")
	}
	s.mu.Unlock()

	// Extract JSON-RPC id for correlation
	var envelope struct {
		ID *json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.ID == nil {
		// Notification — just send, no response expected
		_ = sendJSONRPC(s.conn, raw)
		return nil, nil
	}
	idKey := string(*envelope.ID)

	// Register pending response channel
	ch := make(chan []byte, 1)
	s.pendingMu.Lock()
	s.pending[idKey] = ch
	s.pendingMu.Unlock()

	// Send request with Content-Length framing
	err := sendJSONRPC(s.conn, raw)
	if err != nil {
		s.pendingMu.Lock()
		delete(s.pending, idKey)
		s.pendingMu.Unlock()
		close(ch)
		return nil, fmt.Errorf("lsp send: %w", err)
	}

	// Wait for response with context timeout
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		s.pendingMu.Lock()
		delete(s.pending, idKey)
		s.pendingMu.Unlock()
		close(ch)
		return nil, ctx.Err()
	}
}

// close terminates the LSP server process.
func (s *lspSession) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()

	if s.conn != nil {
		_ = s.conn.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
	_ = os.Remove(s.socketPath)
}

// lspState tracks the LSP server for a connection.
type lspState struct {
	session *lspSession
	command string   // LSP server command (for restarts)
	args    []string // LSP server args (for restarts)
}

// handleLSP handles LSP requests by forwarding raw JSON-RPC to the LSP server.
// The first request with a non-empty Command starts the LSP server.
// Subsequent requests forward the raw JSON-RPC message.
func (h *handler) handleLSP(conn net.Conn, req *protocol.Request) {
	var p LSPParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = h.writeFrame(conn, protocol.NewError(req.MsgID, fmt.Sprintf("parse lsp params: %v", err)))
		return
	}

	h.mu.Lock()
	lsp := h.lsp
	h.mu.Unlock()

	if p.Command != "" {
		// LSPStart request: (re)start the LSP server.
		// Always restart to get a fresh session for the new workspace,
		// since gopls doesn't support re-initialization for a different rootUri.
		cmd, args := p.Command, p.Args

		// Close existing session if any
		if lsp != nil && lsp.session != nil {
			lsp.session.close()
		}

		sess, err := newLSPSession(cmd, args)
		if err != nil {
			_ = h.writeFrame(conn, protocol.NewError(req.MsgID, err.Error()))
			return
		}

		h.mu.Lock()
		h.lsp = &lspState{session: sess, command: cmd, args: args}
		h.mu.Unlock()

		// Send init response
		result := protocol.NewResult(req.MsgID, json.RawMessage(`{"status":"started"}`))
		_ = h.writeFrame(conn, result)
		return
	}

	if lsp == nil || lsp.session == nil || lsp.session.isClosed() {
		// LSPSend request but no session — try to auto-restart with stored command
		cmd, args := "", []string{}
		if lsp != nil && lsp.command != "" {
			cmd, args = lsp.command, lsp.args
		}
		if cmd == "" {
			_ = h.writeFrame(conn, protocol.NewError(req.MsgID, "lsp server not started"))
			return
		}

		sess, err := newLSPSession(cmd, args)
		if err != nil {
			_ = h.writeFrame(conn, protocol.NewError(req.MsgID, err.Error()))
			return
		}

		h.mu.Lock()
		h.lsp = &lspState{session: sess, command: cmd, args: args}
		h.mu.Unlock()
	}

	// Forward raw JSON-RPC to LSP server
	if len(p.Raw) == 0 {
		log.Printf("lsp: raw JSON-RPC message required for MsgID=%d", req.MsgID)
		_ = h.writeFrame(conn, protocol.NewError(req.MsgID, "raw JSON-RPC message required"))
		return
	}


	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := lsp.session.send(ctx, p.Raw)
	if err != nil {
		log.Printf("lsp: send error: %v", err)
		_ = h.writeFrame(conn, protocol.NewError(req.MsgID, err.Error()))
		return
	}


	result := protocol.NewResult(req.MsgID, resp)
	_ = h.writeFrame(conn, result)
}
