package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"kanban/pkg/bridge/protocol"
)

// lspSession tracks a running LSP server process.
// Communication is always over stdin/stdout with Content-Length framing
// (the standard LSP transport protocol).
type lspSession struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	mu        sync.Mutex
	closed    bool
	pending   map[string]chan []byte // JSON-RPC id string -> response channel
	pendingMu sync.Mutex
	// outputCh receives LSP server notifications (e.g., publishDiagnostics)
	// so they can be forwarded to the host asynchronously.
	outputCh chan json.RawMessage
}

// isClosed returns true if the session has been closed.
func (s *lspSession) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// newLSPSession creates a new LSP session by starting the server process
// and communicating over stdin/stdout with Content-Length framing.
func newLSPSession(command string, args []string) (*lspSession, error) {
	cmd := exec.Command(command, args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp stderr pipe: %w", err)
	}

	sess := &lspSession{
		cmd:      cmd,
		stdin:    stdin,
		stdout:   stdout,
		pending:  make(map[string]chan []byte),
		outputCh: make(chan json.RawMessage, 64),
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

	// Start the read loop on stdout
	go sess.readLoop()

	return sess, nil
}

// readLoop reads JSON-RPC responses from the LSP server's stdout.
// Uses Content-Length framing (standard LSP protocol).
func (s *lspSession) readLoop() {
	defer func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		// Close all pending channels
		s.pendingMu.Lock()
		for id, ch := range s.pending {
			close(ch)
			delete(s.pending, id)
		}
		s.pendingMu.Unlock()
	}()

	reader := bufio.NewReader(s.stdout)
	for {
		// Read Content-Length header
		contentLen := 0
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
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
			// Notification (e.g., publishDiagnostics) — forward to host
			select {
			case s.outputCh <- msg:
			default:
				log.Printf("lsp: notification output channel full")
			}
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

// sendJSONRPC writes a JSON-RPC message with Content-Length framing.
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
		// Notification — just send, no response expected.
		// Lock around sendJSONRPC to prevent concurrent writes from interleaving.
		s.mu.Lock()
		_ = sendJSONRPC(s.stdin, raw)
		s.mu.Unlock()
		return nil, nil
	}
	idKey := string(*envelope.ID)

	// Register pending response channel
	ch := make(chan []byte, 1)
	s.pendingMu.Lock()
	s.pending[idKey] = ch
	s.pendingMu.Unlock()

	// Send request with Content-Length framing.
	// Lock around sendJSONRPC to prevent concurrent writes from interleaving.
	s.mu.Lock()
	err := sendJSONRPC(s.stdin, raw)
	s.mu.Unlock()
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

	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.stdout != nil {
		_ = s.stdout.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		s.cmd.Process.Wait() // ignore ProcessState
	}
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
	var p protocol.LSPParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = h.writeFrame(conn, protocol.NewError(req.MsgID, fmt.Sprintf("parse lsp params: %v", err)))
		return
	}

	h.mu.Lock()
	lsp := h.lsp
	h.mu.Unlock()

	if p.Command != "" {
		// LSPStart request: (re)start the LSP server.
		// Always restart to get a fresh session for the new workspace.
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

		// Start a goroutine to forward LSP notifications (e.g., publishDiagnostics) to the host.
		go func() {
			for notif := range sess.outputCh {
				notifFrame := &protocol.Frame{
					MsgID:   0, // Notifications use MsgID 0
					Type:    protocol.TypeNotification,
					Payload: []byte(notif),
				}
				_ = protocol.WriteFrame(conn, notifFrame)
			}
		}()

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
		lsp = h.lsp // update reference to new state
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

// handleLSPNotify handles LSP notification frames (no response expected).
// Forwards raw JSON-RPC notifications to the LSP server.
func (h *handler) handleLSPNotify(_ net.Conn, frame *protocol.Frame) {
	// Extract JSON params from frame payload: [method:1][reserved:1][jsonLen:4][json:params]
	if len(frame.Payload) < 6 {
		log.Printf("lsp: notification payload too short")
		return
	}
	jsonLen := binary.BigEndian.Uint32(frame.Payload[2:6])
	if uint32(len(frame.Payload)) < 6+jsonLen {
		log.Printf("lsp: notification payload truncated")
		return
	}
	paramsJSON := frame.Payload[6 : 6+jsonLen]

	var p protocol.LSPParams
	if err := json.Unmarshal(paramsJSON, &p); err != nil {
		log.Printf("lsp: parse notification params: %v", err)
		return
	}

	h.mu.Lock()
	lsp := h.lsp
	h.mu.Unlock()

	if lsp == nil || lsp.session == nil || lsp.session.isClosed() {
		log.Printf("lsp: notification dropped — server not started")
		return
	}

	if len(p.Raw) == 0 {
		log.Printf("lsp: raw JSON-RPC message required for notification")
		return
	}

	// Forward raw JSON-RPC notification to LSP server (fire-and-forget)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = lsp.session.send(ctx, p.Raw) // send() handles notifications correctly
}
