package sandbox

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"kanban/pkg/bridge/protocol"
)

// pipeConn wraps separate read/write pipes as a net.Conn.
// Used when the Client connects to the agent via podman exec stdin/stdout.
type pipeConn struct {
	reader io.ReadCloser
	writer io.WriteCloser
}

func (c *pipeConn) Read(b []byte) (int, error)         { return c.reader.Read(b) }
func (c *pipeConn) Write(b []byte) (int, error)        { return c.writer.Write(b) }
func (c *pipeConn) Close() error                       { return c.reader.Close() }
func (c *pipeConn) LocalAddr() net.Addr                { return &net.UnixAddr{Net: "pipe", Name: "stdin"} }
func (c *pipeConn) RemoteAddr() net.Addr               { return &net.UnixAddr{Net: "pipe", Name: "stdout"} }
func (c *pipeConn) SetDeadline(t time.Time) error      { return nil }
func (c *pipeConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *pipeConn) SetWriteDeadline(t time.Time) error { return nil }

// Client connects to a sandbox bridge.
// It handles request/response correlation and chunk streaming.
type Client struct {
	mu       sync.Mutex
	writeMu  sync.Mutex // serializes writes to conn (see writeFrame)
	conn     net.Conn
	nextID   atomic.Uint32
	pending  map[uint32]*pendingReq
	sessions map[uint32]*shellSessionState // sessionID → shell state
	readDone chan struct{}
	closed   bool
	// lspNotificationCh receives LSP server notifications (e.g., publishDiagnostics)
	// forwarded from the sandbox bridge.
	lspNotificationCh chan json.RawMessage
}

// shellSessionState tracks a persistent shell session.
type shellSessionState struct {
	outputCh chan []byte   // output chunks arrive here
	done     chan struct{} // closed when session ends
}

// pendingReq tracks an in-flight request.
type pendingReq struct {
	chunkCh   chan *protocol.Frame // chunk frames arrive here
	done      chan *protocol.Frame // result/error frame arrives here
	chunkOnce sync.Once            // prevent double-close
	doneOnce  sync.Once            // prevent double-close
}

// closeChunk closes the chunk channel safely (idempotent).
func (p *pendingReq) closeChunk() {
	p.chunkOnce.Do(func() { close(p.chunkCh) })
}

// closeDone closes the done channel safely (idempotent).
func (p *pendingReq) closeDone() {
	p.doneOnce.Do(func() { close(p.done) })
}

// BashResultHolder wraps the bash result for streaming.
type BashResultHolder struct {
	outputCh chan []byte
	exitCode chan int32
}

// NewClient creates a Client from stdin/stdout pipes (e.g., from podman exec).
// The stdin pipe is used for writing requests, stdout for reading responses.
func NewClient(stdin io.WriteCloser, stdout io.ReadCloser) *Client {
	conn := &pipeConn{reader: stdout, writer: stdin}
	c := &Client{
		conn:              conn,
		pending:           make(map[uint32]*pendingReq),
		sessions:          make(map[uint32]*shellSessionState),
		readDone:          make(chan struct{}),
		lspNotificationCh: make(chan json.RawMessage, 64),
	}
	c.nextID.Store(1)
	go c.readLoop()
	return c
}

// IsAlive returns true if the client is still usable: it has not been closed
// AND its readLoop is still running.
//
// Checking the closed flag matters: Close() marks the client closed before the
// readLoop goroutine notices the closed pipe and exits. In that window the old
// implementation (readDone only) reported true even though every request fails
// with "client closed", which made SandboxManager.GetClient and recoverBridge
// reuse a dead client and defeat bridge recovery.
func (c *Client) IsAlive() bool {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return false
	}
	return !c.readDoneClosed()
}

// readDoneClosed reports whether the readLoop has exited. Must be called with
// c.mu held: readLoop closes readDone while holding c.mu (after delivering
// errors to pending requests), so this is race-free with request registration.
func (c *Client) readDoneClosed() bool {
	select {
	case <-c.readDone:
		return true
	default:
		return false
	}
}

// LSPNotificationCh returns a channel that receives LSP server notifications
// (e.g., publishDiagnostics) forwarded from the sandbox bridge.
func (c *Client) LSPNotificationCh() <-chan json.RawMessage {
	return c.lspNotificationCh
}

// nextMsgID returns the next unique message ID.
func (c *Client) nextMsgID() uint32 {
	return c.nextID.Add(1) - 1
}

// writeFrame serializes writes to the underlying connection.
func (c *Client) writeFrame(frame *protocol.Frame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return protocol.WriteFrame(c.conn, frame)
}

// readLoop reads frames from the connection and dispatches them to pending requests
// and shell sessions.
func (c *Client) readLoop() {
	defer func() {
		c.mu.Lock()
		// Mark the connection dead BEFORE delivering errors: requests that
		// arrive after this point fail fast (see waitForCtx) instead of being
		// registered as pending and hanging forever with no reader.
		close(c.readDone)
		// Deliver error to all pending requests
		for _, p := range c.pending {
			select {
			case p.done <- protocol.NewError(0, "connection closed"):
			default:
			}
			p.closeChunk()
			p.closeDone()
		}
		// Close all shell sessions
		for _, s := range c.sessions {
			close(s.outputCh)
		}
		c.mu.Unlock()
	}()

	for {
		frame, err := protocol.ReadFrame(c.conn)
		if err != nil {
			return // connection closed
		}

		// Check pending requests first
		c.mu.Lock()
		p, isPending := c.pending[frame.MsgID]
		s, isShell := c.sessions[frame.MsgID]
		c.mu.Unlock()

		if isPending {
			switch frame.Type {
			case protocol.TypeChunk:
				select {
				case p.chunkCh <- frame:
				default:
					log.Printf("[sandbox/client] chunk channel full for MsgID=%d", frame.MsgID)
				}
			case protocol.TypeResult, protocol.TypeError:
				p.closeChunk()
				select {
				case p.done <- frame:
				default:
				}
			}
			continue
		}

		// Check shell sessions (chunks use sessionID as MsgID)
		if isShell && frame.Type == protocol.TypeChunk {
			chunk, err := protocol.ParseShellChunk(frame.Payload)
			if err != nil {
				continue
			}
			// Empty data = session closed signal
			if len(chunk.Data) == 0 {
				close(s.outputCh)
				c.mu.Lock()
				delete(c.sessions, frame.MsgID)
				c.mu.Unlock()
				continue
			}
			select {
			case s.outputCh <- chunk.Data:
			default:
				log.Printf("[sandbox/client] shell output channel full for session %d", frame.MsgID)
			}
			continue
		}

		// Handle LSP notifications (e.g., publishDiagnostics)
		if frame.Type == protocol.TypeNotification && frame.MsgID == 0 {
			select {
			case c.lspNotificationCh <- frame.Payload:
			default:
				log.Printf("[sandbox/client] lsp notification channel full")
			}
			continue
		}

		log.Printf("[sandbox/client] unhandled frame MsgID=%d Type=%s", frame.MsgID, protocol.MessageTypeName(frame.Type))
	}
}

// waitFor sends a request and waits for the final Result/Error frame.
// Returns the final frame and any chunks received.
func (c *Client) waitFor(frame *protocol.Frame, timeout time.Duration) (*protocol.Frame, []*protocol.Frame, error) {
	return c.waitForCtx(context.Background(), frame, timeout)
}

func (c *Client) waitForCtx(ctx context.Context, frame *protocol.Frame, timeout time.Duration) (*protocol.Frame, []*protocol.Frame, error) {
	msgID := frame.MsgID

	p := &pendingReq{
		chunkCh: make(chan *protocol.Frame, 64),
		done:    make(chan *protocol.Frame, 1),
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, nil, errors.New("client closed")
	}
	if c.readDoneClosed() {
		// readLoop already exited (e.g. bridge died) — fail fast so callers can
		// trigger bridge recovery instead of hanging forever.
		c.mu.Unlock()
		return nil, nil, errors.New("connection closed")
	}
	c.pending[msgID] = p
	c.mu.Unlock()

	if err := c.writeFrame(frame); err != nil {
		c.cleanup(msgID)
		p.closeChunk()
		p.closeDone()
		return nil, nil, fmt.Errorf("send request: %w", err)
	}

	var chunks []*protocol.Frame

	// timeout == 0 means block indefinitely (no timer)
	hasTimeout := timeout > 0
	var timer *time.Timer
	if hasTimeout {
		timer = time.NewTimer(timeout)
		defer timer.Stop()
	}

	// Phase 1: collect chunks
	for {
		if hasTimeout {
			select {
			case chunk, ok := <-p.chunkCh:
				if !ok {
					goto waitDone
				}
				chunks = append(chunks, chunk)
			case final := <-p.done:
				c.cleanup(msgID)
				return final, chunks, nil
			case <-timer.C:
				c.cleanup(msgID)
				return nil, chunks, fmt.Errorf("timeout waiting for result")
			case <-ctx.Done():
				c.cleanup(msgID)
				return nil, chunks, ctx.Err()
			}
		} else {
			select {
			case chunk, ok := <-p.chunkCh:
				if !ok {
					goto waitDone
				}
				chunks = append(chunks, chunk)
			case final := <-p.done:
				c.cleanup(msgID)
				return final, chunks, nil
			case <-ctx.Done():
				c.cleanup(msgID)
				return nil, chunks, ctx.Err()
			}
		}
	}

waitDone:
	// Phase 2: wait for final result/error
	if hasTimeout {
		select {
		case final := <-p.done:
			c.cleanup(msgID)
			return final, chunks, nil
		case <-timer.C:
			c.cleanup(msgID)
			return nil, chunks, fmt.Errorf("timeout waiting for result")
		case <-ctx.Done():
			c.cleanup(msgID)
			return nil, nil, ctx.Err()
		}
	} else {
		select {
		case final := <-p.done:
			c.cleanup(msgID)
			return final, chunks, nil
		case <-ctx.Done():
			c.cleanup(msgID)
			return nil, nil, ctx.Err()
		}
	}
}

func (c *Client) cleanup(msgID uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.pending[msgID]; ok {
		p.closeChunk()
		p.closeDone()
		delete(c.pending, msgID)
	}
}

// ReadFile reads a file from the sandbox.
// Returns the content as a string and the total size.
func (c *Client) ReadFile(ctx context.Context, path string) (string, int64, error) {
	params, _ := json.Marshal(protocol.ReadParams{Path: path})
	frame := protocol.NewRequest(c.nextMsgID(), protocol.MethodRead, params)

	final, chunks, err := c.waitForCtx(ctx, frame, 0)
	if err != nil {
		return "", 0, err
	}
	if final.Type == protocol.TypeError {
		return "", 0, errors.New(final.ErrorMsg())
	}

	// Assemble content from chunks
	var content []byte
	for _, chunk := range chunks {
		content = append(content, chunk.Payload...)
	}

	// Parse size from result
	size, err := protocol.Uint64Result(final.Payload)
	if err != nil {
		return "", 0, fmt.Errorf("parse read result: %w", err)
	}

	return string(content), int64(size), nil
}

// WriteFile writes content to a file in the sandbox.
// Returns the number of bytes written.
func (c *Client) WriteFile(ctx context.Context, path, content string) (int64, error) {
	params, _ := json.Marshal(protocol.WriteParams{Path: path, Content: content})
	frame := protocol.NewRequest(c.nextMsgID(), protocol.MethodWrite, params)

	final, _, err := c.waitForCtx(ctx, frame, 0)
	if err != nil {
		return 0, err
	}
	if final.Type == protocol.TypeError {
		return 0, errors.New(final.ErrorMsg())
	}

	size, err := protocol.Uint64Result(final.Payload)
	if err != nil {
		return 0, fmt.Errorf("parse write result: %w", err)
	}
	return int64(size), nil
}

// EditFile applies text replacements to a file in the sandbox.
// Returns the number of bytes written.
func (c *Client) EditFile(ctx context.Context, path string, edits []protocol.Edit) (int64, error) {
	params, _ := json.Marshal(protocol.EditParams{Path: path, Edits: edits})
	frame := protocol.NewRequest(c.nextMsgID(), protocol.MethodEdit, params)

	final, _, err := c.waitForCtx(ctx, frame, 0)
	if err != nil {
		return 0, err
	}
	if final.Type == protocol.TypeError {
		return 0, errors.New(final.ErrorMsg())
	}

	size, err := protocol.Uint64Result(final.Payload)
	if err != nil {
		return 0, fmt.Errorf("parse edit result: %w", err)
	}
	return int64(size), nil
}

// Output returns an io.Reader that yields bash output as it arrives.
func (b *BashResultHolder) Output() io.Reader {
	pr, pw := io.Pipe()
	go func() {
		for data := range b.outputCh {
			_, _ = pw.Write(data)
		}
		pw.Close()
	}()
	return pr
}

// ExitCode returns a channel that receives the exit code when the command completes.
func (b *BashResultHolder) ExitCode() <-chan int32 {
	return b.exitCode
}

// Bash runs a shell command in the sandbox with streaming output.
// If ctx is cancelled, the running bash process is killed on the agent.
func (c *Client) Bash(ctx context.Context, command string, timeout int64) (*BashResultHolder, error) {
	if timeout <= 0 {
		timeout = 5
	}
	if timeout > 300 {
		timeout = 300
	}

	params, _ := json.Marshal(protocol.BashParams{Command: command, Timeout: timeout})
	frame := protocol.NewRequest(c.nextMsgID(), protocol.MethodBash, params)

	holder := &BashResultHolder{
		outputCh: make(chan []byte, 64),
		exitCode: make(chan int32, 1),
	}

	// Start the request
	msgID := frame.MsgID
	p := &pendingReq{
		chunkCh: make(chan *protocol.Frame, 64),
		done:    make(chan *protocol.Frame, 1),
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		close(holder.outputCh)
		close(holder.exitCode)
		return nil, errors.New("client closed")
	}
	if c.readDoneClosed() {
		// readLoop already exited — fail fast so callers can trigger bridge
		// recovery instead of blocking until the (very long) bash timeout.
		c.mu.Unlock()
		close(holder.outputCh)
		close(holder.exitCode)
		return nil, errors.New("connection closed")
	}
	c.pending[msgID] = p
	c.mu.Unlock()

	if err := c.writeFrame(frame); err != nil {
		c.cleanup(msgID)
		close(holder.outputCh)
		close(holder.exitCode)
		return nil, fmt.Errorf("send bash request: %w", err)
	}

	// Watch for context cancellation — send cancel frame to agent
	cancelled := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			// Send cancel frame to agent (fire-and-forget)
			cancelFrame := protocol.NewBashCancelRequest(c.nextMsgID(), msgID)
			_ = c.writeFrame(cancelFrame)
		case <-cancelled:
			// Request completed normally, no need to cancel
		}
	}()

	// Process response in background
	go func() {
		defer func() {
			close(cancelled) // signal the cancel goroutine to exit
			c.cleanup(msgID)
			close(holder.outputCh)
			close(holder.exitCode)
		}()

		timeoutDur := time.Duration(timeout+30) * time.Second
		timer := time.NewTimer(timeoutDur)
		defer timer.Stop()

		for {
			select {
			case chunk, ok := <-p.chunkCh:
				if !ok {
					// Chunks done, wait for result
					select {
					case final := <-p.done:
						if final.Type == protocol.TypeResult {
							result, err := protocol.ParseBashResult(final.Payload)
							if err != nil {
								holder.exitCode <- 1
								return
							}
							holder.exitCode <- result.ExitCode
						} else {
							holder.exitCode <- 1
						}
						return
					case <-timer.C:
						holder.exitCode <- 124
						return
					}
				}
				// Parse bash chunk
				chunkData, err := protocol.ParseBashChunk(chunk.Payload)
				if err != nil {
					continue
				}
				select {
				case holder.outputCh <- chunkData.Data:
				case <-timer.C:
					holder.exitCode <- 124
					return
				}
			case <-timer.C:
				holder.exitCode <- 124
				return
			}
		}
	}()

	return holder, nil
}

// ShellOpen starts a new shell session in the sandbox.
// Returns a session ID for subsequent ShellIO/ShellResize/ShellClose calls.
func (c *Client) ShellOpen(shell string, rows, cols uint16) (uint32, error) {
	params, _ := json.Marshal(protocol.ShellOpenParams{Shell: shell, Rows: rows, Cols: cols})
	frame := protocol.NewRequest(c.nextMsgID(), protocol.MethodShellOpen, params)

	final, _, err := c.waitFor(frame, 0)
	if err != nil {
		return 0, err
	}
	if final.Type == protocol.TypeError {
		return 0, errors.New(final.ErrorMsg())
	}

	result, err := protocol.ParseShellOpenResult(final.Payload)
	if err != nil {
		return 0, fmt.Errorf("parse shell_open result: %w", err)
	}

	// Register session for chunk routing
	c.mu.Lock()
	c.sessions[result.SessionID] = &shellSessionState{
		outputCh: make(chan []byte, 128),
		done:     make(chan struct{}),
	}
	c.mu.Unlock()

	return result.SessionID, nil
}

// ShellIO sends input to a shell session.
func (c *Client) ShellIO(sessionID uint32, input []byte) error {
	frame := protocol.NewShellIORequest(c.nextMsgID(), sessionID, input)
	return c.writeFrame(frame)
}

// ShellOutput returns an io.Reader that yields shell output for a session.
func (c *Client) ShellOutput(sessionID uint32) io.Reader {
	c.mu.Lock()
	s := c.sessions[sessionID]
	c.mu.Unlock()

	if s == nil {
		pr, pw := io.Pipe()
		_ = pw.Close()
		return pr
	}

	pr, pw := io.Pipe()
	go func() {
		for data := range s.outputCh {
			if len(data) == 0 {
				break
			}
			_, _ = pw.Write(data)
		}
		pw.Close()
	}()
	return pr
}

// ShellResize resizes a shell session's PTY.
func (c *Client) ShellResize(sessionID uint32, rows, cols uint16) error {
	frame := protocol.NewShellResizeRequest(c.nextMsgID(), sessionID, rows, cols)
	return c.writeFrame(frame)
}

// ShellClose terminates a shell session.
func (c *Client) ShellClose(sessionID uint32) error {
	c.mu.Lock()
	if s, ok := c.sessions[sessionID]; ok {
		close(s.outputCh)
		delete(c.sessions, sessionID)
	}
	c.mu.Unlock()

	frame := protocol.NewShellCloseRequest(c.nextMsgID(), sessionID)
	return c.writeFrame(frame)
}

// LSPSend sends a raw JSON-RPC message to the LSP server and waits for the response.
// The raw parameter is the raw JSON-RPC message bytes.
// Returns the raw JSON-RPC response bytes.
func (c *Client) LSPSend(ctx context.Context, raw json.RawMessage) ([]byte, error) {
	params, _ := json.Marshal(protocol.LSPParams{Raw: raw})
	frame := protocol.NewRequest(c.nextMsgID(), protocol.MethodLSP, params)

	final, _, err := c.waitForCtx(ctx, frame, 0)
	if err != nil {
		return nil, err
	}
	if final.Type == protocol.TypeError {
		return nil, errors.New(final.ErrorMsg())
	}
	return final.Payload, nil
}

// LSPNotify sends a raw JSON-RPC notification to the LSP server without waiting for a response.
// Use this for notifications (messages without an 'id' field) like initialized, didOpen, etc.
func (c *Client) LSPNotify(_ context.Context, raw json.RawMessage) error {
	params, _ := json.Marshal(protocol.LSPParams{Raw: raw})
	// Same payload format as NewRequest: [method:1][reserved:1][jsonLen:4][json:params]
	payload := make([]byte, 2+4+len(params))
	payload[0] = protocol.MethodLSP
	payload[1] = 0 // reserved
	binary.BigEndian.PutUint32(payload[2:6], uint32(len(params)))
	copy(payload[6:], params)
	frame := &protocol.Frame{MsgID: 0, Type: protocol.TypeNotification, Payload: payload}
	return c.writeFrame(frame)
}

// LSPStart starts the LSP server process inside the sandbox.
// Command and args specify the LSP server to start.
// Communication is over stdin/stdout with Content-Length framing.
func (c *Client) LSPStart(ctx context.Context, command string, args []string) error {
	params, _ := json.Marshal(protocol.LSPParams{Command: command, Args: args})
	frame := protocol.NewRequest(c.nextMsgID(), protocol.MethodLSP, params)

	final, _, err := c.waitForCtx(ctx, frame, 0)
	if err != nil {
		return err
	}
	if final.Type == protocol.TypeError {
		return errors.New(final.ErrorMsg())
	}
	return nil
}

// Ping performs a lightweight round-trip to verify the bridge is responsive.
// Any response — including an error frame — proves the bridge answered, so a
// TypeError result still counts as a successful ping. Used by bridge recovery
// to avoid declaring success on a client whose underlying pipes are dead.
// Bounded internally by a 3s timeout so callers can pass their own (possibly
// longer-lived) context safely.
func (c *Client) Ping(ctx context.Context) error {
	params, _ := json.Marshal(protocol.ReadParams{Path: "."})
	frame := protocol.NewRequest(c.nextMsgID(), protocol.MethodRead, params)
	_, _, err := c.waitForCtx(ctx, frame, 3*time.Second)
	return err
}

// Close closes the client connection and all shell sessions.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	// Close all shell sessions
	for _, s := range c.sessions {
		close(s.outputCh)
	}
	c.sessions = nil
	return c.conn.Close()
}
