// Package bridge is the host-side client of the sandbox-bridge protocol. It wraps the stdin/stdout of
// one "podman exec ... /sandbox-bridge" and issues tool calls over it - no dial, no port - so a tool call
// does not pay for a podman exec each.
package bridge

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"kanban/pkg/bridge/protocol"
	"kanban/pkg/logging"
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
	mu      sync.Mutex
	writeMu sync.Mutex // serializes writes to conn (see writeFrame)
	conn    net.Conn
	nextID  atomic.Uint32
	pending map[uint32]*pendingReq

	readDone chan struct{}
	closed   bool
	// lspNotificationCh receives LSP server notifications (e.g., publishDiagnostics)
	// forwarded from the sandbox bridge.
	lspNotificationCh chan json.RawMessage
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
		readDone:          make(chan struct{}),
		lspNotificationCh: make(chan json.RawMessage, 64),
	}
	c.nextID.Store(1)
	go c.readLoop()
	return c
}

// IsAlive reports whether the client can still be used: not closed, and its readLoop still
// running. The closed flag matters because Close() marks the client closed before readLoop notices
// the closed pipe and exits; in that window a readDone-only check reports true while every request
// fails with "client closed", which made GetClient and recoverBridge reuse a dead client.
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

// maxLSPRoundTrip bounds every LSP call and notification write, even when the caller
// passes a context with no deadline. A language server in the sandbox that stops
// answering would otherwise park the caller forever, and the turn-boundary diagnostics
// pass runs on the task's executor goroutine — so a parked call leaves the task
// "running" with nothing left to cancel. Var so tests can shrink it.
var maxLSPRoundTrip = 60 * time.Second

// lspCallCtx bounds one LSP call. An earlier caller deadline is kept as-is.
func lspCallCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= maxLSPRoundTrip {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, maxLSPRoundTrip)
}

// writeFrame serializes writes to the underlying connection.
func (c *Client) writeFrame(frame *protocol.Frame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return protocol.WriteFrame(c.conn, frame)
}

// ErrWriteStalled reports that a frame could not be handed to the bridge before the
// deadline — the peer stopped reading. Callers decide whether to retire the client.
var ErrWriteStalled = errors.New("bridge write stalled past its deadline")

// ErrStreamOverflow reports that frames arrived faster than this process could queue them. The
// lost bytes cannot be recovered, so the request fails rather than answering with a short body.
var ErrStreamOverflow = errors.New("bridge stream overflowed the client buffer")

// writeFrameCtx writes one frame, giving up when ctx is done. Giving up does not close the
// connection, even though the abandoned Write holds writeMu until the peer drains: closing would
// let a task sandbox build a second client over the same pipes and interleave frames mid-frame,
// and EOF on the bridge's stdin kills the `podman exec` that nothing restarts. Callers that can
// reconnect — extension sandboxes, whose Start spawns a fresh bridge — retire the client on
// ErrWriteStalled themselves.
func (c *Client) writeFrameCtx(ctx context.Context, frame *protocol.Frame) error {
	if ctx.Done() == nil {
		return c.writeFrame(frame)
	}
	done := make(chan error, 1)
	go func() { done <- c.writeFrame(frame) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%w: %w", ErrWriteStalled, ctx.Err())
		}
		return ctx.Err()
	}
}

// readLoop reads frames from the connection and dispatches them to pending requests
// and LSP notifications.
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
		c.mu.Unlock()
	}()

	for {
		frame, err := protocol.ReadFrame(c.conn)
		if err != nil {
			return // connection closed
		}

		c.mu.Lock()

		p, isPending := c.pending[frame.MsgID]

		if isPending {
			switch frame.Type {
			case protocol.TypeChunk:
				select {
				case p.chunkCh <- frame:
				default:
					logging.For(logging.Bridge).Warnf("chunk channel full for MsgID=%d, failing the request", frame.MsgID)
					p.closeChunk()
					select {
					case p.done <- protocol.NewError(frame.MsgID, ErrStreamOverflow.Error()):
					default:
					}
					delete(c.pending, frame.MsgID)
				}
			case protocol.TypeResult, protocol.TypeError:
				p.closeChunk()
				select {
				case p.done <- frame:
				default:
				}
			}
			c.mu.Unlock()
			continue
		}

		// Handle LSP notifications (e.g., publishDiagnostics)
		if frame.Type == protocol.TypeNotification && frame.MsgID == 0 {
			select {
			case c.lspNotificationCh <- frame.Payload:
			default:
				logging.For(logging.Bridge).Info("lsp notification channel full")
			}
			c.mu.Unlock()
			continue
		}
		c.mu.Unlock()

		logging.For(logging.Bridge).Infof("unhandled frame MsgID=%d Type=%s", frame.MsgID, protocol.MessageTypeName(frame.Type))
	}
}

func (c *Client) waitForCtx(ctx context.Context, frame *protocol.Frame, timeout time.Duration) (*protocol.Frame, []*protocol.Frame, error) {
	msgID := frame.MsgID

	p, err := c.register(msgID)
	if err != nil {
		return nil, nil, err
	}

	if err := c.writeFrameCtx(ctx, frame); err != nil {
		c.cleanup(msgID)
		return nil, nil, fmt.Errorf("send request: %w", err)
	}

	var chunks []*protocol.Frame
	chunkBytes := 0

	// timeout == 0 means block indefinitely, and a receive from a nil channel blocks
	// forever, so one select answers both cases. It used to be two copies of every branch
	// per phase, kept in step by hand — which is exactly how a new case ends up added to
	// one copy and not the other.
	var timeoutC <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timeoutC = timer.C
	}

	// Phase 1: collect chunks until the stream closes or the answer arrives.
	for {
		select {
		case chunk, ok := <-p.chunkCh:
			if !ok {
				goto waitDone
			}
			chunks = append(chunks, chunk)
			// Counted as it arrives: a total checked at assembly has already cost the host the
			// whole response. Read is the only method that streams through this path.
			chunkBytes += len(chunk.Payload)
			if chunkBytes > protocol.MaxReadBytes {
				c.cleanup(msgID)
				return nil, nil, fmt.Errorf("%w: stream carried %d bytes, limit %d",
					protocol.ErrResponseTooLarge, chunkBytes, protocol.MaxReadBytes)
			}
		case final := <-p.done:
			// The readLoop closes the chunk channel before it answers, so a chunk still queued
			// here belongs to this body: returning on the answer alone handed back a random
			// prefix of a streamed file as a success.
			var queued int
			chunks, queued = drainQueued(p.chunkCh, chunks)
			if chunkBytes += queued; chunkBytes > protocol.MaxReadBytes {
				c.cleanup(msgID)
				return nil, nil, fmt.Errorf("%w: stream carried %d bytes, limit %d",
					protocol.ErrResponseTooLarge, chunkBytes, protocol.MaxReadBytes)
			}
			c.cleanup(msgID)
			return final, chunks, nil
		case <-timeoutC:
			c.cleanup(msgID)
			return nil, chunks, fmt.Errorf("timeout waiting for result")
		case <-ctx.Done():
			c.cleanup(msgID)
			return nil, chunks, ctx.Err()
		}
	}

waitDone:
	// Phase 2: the chunk stream ended with no answer; wait for the result alone. Chunks are
	// dropped on cancellation here and never were returned, which is what the caller wants:
	// a cancelled call has nothing downstream that reads a partial stream.
	select {
	case final := <-p.done:
		c.cleanup(msgID)
		return final, chunks, nil
	case <-timeoutC:
		c.cleanup(msgID)
		return nil, chunks, fmt.Errorf("timeout waiting for result")
	case <-ctx.Done():
		c.cleanup(msgID)
		return nil, nil, ctx.Err()
	}
}

// drainQueued collects the frames a chunk channel still holds. Receiving from a closed channel
// yields its buffered frames first, and the default branch keeps this from waiting on a stream
// that has not been closed.
func drainQueued(ch chan *protocol.Frame, into []*protocol.Frame) ([]*protocol.Frame, int) {
	total := 0
	for {
		select {
		case chunk, ok := <-ch:
			if !ok {
				return into, total
			}
			into = append(into, chunk)
			total += len(chunk.Payload)
		default:
			return into, total
		}
	}
}

// register puts a pending request into the table under msgID, refusing when nothing could
// ever answer it. Both refusals say the same thing to a caller: a request registered now
// would wait for a reply that can no longer arrive - for an LSP call up to its round-trip
// bound, for bash up to the whole command timeout - so each one is a fast error that lets
// the caller trigger bridge recovery instead of parking.
func (c *Client) register(msgID uint32) (*pendingReq, error) {
	p := &pendingReq{
		chunkCh: make(chan *protocol.Frame, 64),
		done:    make(chan *protocol.Frame, 1),
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("client closed")
	}
	if c.readDoneClosed() {
		return nil, errors.New("connection closed")
	}
	c.pending[msgID] = p
	return p, nil
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

// sendUint64 sends a request and returns the uint64 in its Result payload.
// op names the operation for error messages (e.g. "write", "edit").
func (c *Client) sendUint64(ctx context.Context, op string, method uint8, params any) (int64, error) {
	payload, _ := json.Marshal(params)
	frame := protocol.NewRequest(c.nextMsgID(), method, payload)

	final, _, err := c.waitForCtx(ctx, frame, 0)
	if err != nil {
		return 0, err
	}
	if final.Type == protocol.TypeError {
		return 0, errors.New(final.ErrorMsg())
	}

	size, err := protocol.Uint64Result(final.Payload)
	if err != nil {
		return 0, fmt.Errorf("parse %s result: %w", op, err)
	}
	return int64(size), nil
}

// WriteFile writes content to a file in the sandbox.
// Returns the number of bytes written.
func (c *Client) WriteFile(ctx context.Context, path, content string) (int64, error) {
	return c.sendUint64(ctx, "write", protocol.MethodWrite, protocol.WriteParams{Path: path, Content: content})
}

// EditFile applies text replacements to a file in the sandbox.
// Returns the number of bytes written.
func (c *Client) EditFile(ctx context.Context, path string, edits []protocol.Edit) (int64, error) {
	return c.sendUint64(ctx, "edit", protocol.MethodEdit, protocol.EditParams{Path: path, Edits: edits})
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
	p, err := c.register(msgID)
	if err != nil {
		// Both halves of the holder close even on this early exit: a caller that got no
		// result must see "no more output" rather than block on a channel no one will write.
		close(holder.outputCh)
		close(holder.exitCode)
		return nil, err
	}

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

// LSPSend sends a raw JSON-RPC message to the LSP server and waits for the response.
// The raw parameter is the raw JSON-RPC message bytes.
// Returns the raw JSON-RPC response bytes.
func (c *Client) LSPSend(ctx context.Context, raw json.RawMessage) ([]byte, error) {
	ctx, cancel := lspCallCtx(ctx)
	defer cancel()

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
// ctx bounds the write, which is the only thing here that can block.
func (c *Client) LSPNotify(ctx context.Context, raw json.RawMessage) error {
	ctx, cancel := lspCallCtx(ctx)
	defer cancel()

	params, _ := json.Marshal(protocol.LSPParams{Raw: raw})
	// Same payload format as NewRequest: [method:1][reserved:1][jsonLen:4][json:params]
	payload := make([]byte, 2+4+len(params))
	payload[0] = protocol.MethodLSP
	payload[1] = 0 // reserved
	binary.BigEndian.PutUint32(payload[2:6], uint32(len(params)))
	copy(payload[6:], params)
	frame := &protocol.Frame{MsgID: 0, Type: protocol.TypeNotification, Payload: payload}
	return c.writeFrameCtx(ctx, frame)
}

// LSPStart starts the LSP server process inside the sandbox. Command and args specify the server.
//
// There is deliberately no environment parameter: the server's environment is the container's, set at
// creation, which is also what the extension's own environment check sees.
func (c *Client) LSPStart(ctx context.Context, command string, args []string) error {
	ctx, cancel := lspCallCtx(ctx)
	defer cancel()

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

// Close closes the client connection. The readLoop's own shutdown delivers
// errors to any requests still in flight, so Close only has to mark the client
// closed (which fails fast for later calls) and drop the pipes.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close()
}
