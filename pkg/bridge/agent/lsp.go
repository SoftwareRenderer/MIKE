package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"mike/pkg/bridge/protocol"
	"mike/pkg/logging"
)

// lspSession is one language server process started on this connection, spoken to over
// stdin/stdout with Content-Length framing (the standard LSP transport).
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

// newLSPSession starts one language server process, spoken to over stdin/stdout with Content-Length
// framing, using the container's own environment: the image's plus whatever `-e` the container was
// created with. There is deliberately no per-session environment:
// the host decided what the server reads its dependencies from at container creation, and the
// extension's own environment check runs in the same container.
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
			logging.For(logging.Bridge).Infof("lsp %s: %s", command, scanner.Text())
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
		// Closing outputCh ends the goroutine that forwards this session's notifications to the
		// host. Without it that goroutine blocks on the channel for the life of the bridge — one
		// leaked goroutine and one unread buffer per language server ever started.
		close(s.outputCh)
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
				logging.For(logging.Bridge).Infof("lsp: notification output channel full")
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
	command string   // LSP server command this session was started with
	args    []string // LSP server args this session was started with
}

// handleLSP starts this connection's language server (a request carrying a non-empty Command)
// and otherwise forwards raw JSON-RPC to it.
func (h *handler) handleLSP(out io.Writer, req *protocol.Request) {
	defer h.recoverPanic(out, req.MsgID)
	var p protocol.LSPParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = h.writeFrame(out, protocol.NewError(req.MsgID, fmt.Sprintf("parse lsp params: %v", err)))
		return
	}

	h.mu.Lock()
	lsp := h.lsp
	h.mu.Unlock()

	if p.Command != "" {
		// The session belongs to this connection: the same server again is idempotent, a
		// different one is refused. Replacing a server out from under the holder that handshaked
		// with it leaves that holder talking to a process that never saw `initialize`.
		if lsp != nil && lsp.session != nil && !lsp.session.isClosed() {
			if lsp.command == p.Command && slices.Equal(lsp.args, p.Args) {
				_ = h.writeFrame(out, protocol.NewResult(req.MsgID, json.RawMessage(`{"status":"started"}`)))
				return
			}
			_ = h.writeFrame(out, protocol.NewError(req.MsgID,
				fmt.Sprintf("lsp session on this connection is running %q; %q needs its own connection", lsp.command, p.Command)))
			return
		}

		sess, err := newLSPSession(p.Command, p.Args)
		if err != nil {
			_ = h.writeFrame(out, protocol.NewError(req.MsgID, err.Error()))
			return
		}

		h.mu.Lock()
		h.lsp = &lspState{session: sess, command: p.Command, args: p.Args}
		h.mu.Unlock()

		// Forward notifications (e.g. publishDiagnostics) to the host through h.writeFrame, like
		// every other writer: a raw WriteFrame here can land in the middle of a bash output frame.
		go func() {
			for notif := range sess.outputCh {
				_ = h.writeFrame(out, &protocol.Frame{
					MsgID:   0, // Notifications use MsgID 0
					Type:    protocol.TypeNotification,
					Payload: []byte(notif),
				})
			}
		}()

		// Send init response
		result := protocol.NewResult(req.MsgID, json.RawMessage(`{"status":"started"}`))
		_ = h.writeFrame(out, result)
		return
	}

	if lsp == nil || lsp.session == nil || lsp.session.isClosed() {
		// Refuse, do not restart: a server restarted here never saw `initialize`, so it answers a
		// document with a warning about itself. Starting one is the host's decision.
		_ = h.writeFrame(out, protocol.NewError(req.MsgID, "lsp server not started"))
		return
	}

	// Forward raw JSON-RPC to LSP server
	if len(p.Raw) == 0 {
		logging.For(logging.Bridge).Infof("lsp: raw JSON-RPC message required for MsgID=%d", req.MsgID)
		_ = h.writeFrame(out, protocol.NewError(req.MsgID, "raw JSON-RPC message required"))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := lsp.session.send(ctx, p.Raw)
	if err != nil {
		logging.For(logging.Bridge).Warnf("lsp: send error: %v", err)
		_ = h.writeFrame(out, protocol.NewError(req.MsgID, err.Error()))
		return
	}

	result := protocol.NewResult(req.MsgID, resp)
	_ = h.writeFrame(out, result)
}

// handleLSPNotify handles LSP notification frames (no response expected).
// Forwards raw JSON-RPC notifications to the LSP server.
func (h *handler) handleLSPNotify(frame *protocol.Frame) {
	defer h.recoverPanic(nil, 0)
	// Extract JSON params from frame payload: [method:1][reserved:1][jsonLen:4][json:params]
	if len(frame.Payload) < 6 {
		logging.For(logging.Bridge).Infof("lsp: notification payload too short")
		return
	}
	jsonLen := binary.BigEndian.Uint32(frame.Payload[2:6])
	if uint32(len(frame.Payload)) < 6+jsonLen {
		logging.For(logging.Bridge).Infof("lsp: notification payload truncated")
		return
	}
	paramsJSON := frame.Payload[6 : 6+jsonLen]

	var p protocol.LSPParams
	if err := json.Unmarshal(paramsJSON, &p); err != nil {
		logging.For(logging.Bridge).Infof("lsp: parse notification params: %v", err)
		return
	}

	h.mu.Lock()
	lsp := h.lsp
	h.mu.Unlock()

	if lsp == nil || lsp.session == nil || lsp.session.isClosed() {
		logging.For(logging.Bridge).Infof("lsp: notification dropped — server not started")
		return
	}

	if len(p.Raw) == 0 {
		logging.For(logging.Bridge).Infof("lsp: raw JSON-RPC message required for notification")
		return
	}

	// Forward raw JSON-RPC notification to LSP server (fire-and-forget)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = lsp.session.send(ctx, p.Raw) // send() handles notifications correctly
}
