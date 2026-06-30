package sandbox

import (
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

func (c *pipeConn) Read(b []byte) (int, error)       { return c.reader.Read(b) }
func (c *pipeConn) Write(b []byte) (int, error)      { return c.writer.Write(b) }
func (c *pipeConn) Close() error                     { return c.reader.Close() }
func (c *pipeConn) LocalAddr() net.Addr              { return &net.UnixAddr{Net: "pipe", Name: "stdin"} }
func (c *pipeConn) RemoteAddr() net.Addr             { return &net.UnixAddr{Net: "pipe", Name: "stdout"} }
func (c *pipeConn) SetDeadline(t time.Time) error    { return nil }
func (c *pipeConn) SetReadDeadline(t time.Time) error { return nil }
func (c *pipeConn) SetWriteDeadline(t time.Time) error { return nil }

// Client connects to a sandbox bridge.
// It handles request/response correlation and chunk streaming.
type Client struct {
	mu       sync.Mutex
	conn     net.Conn
	nextID   atomic.Uint32
	pending  map[uint32]*pendingReq
	sessions map[uint32]*shellSessionState // sessionID → shell state
	readDone chan struct{}
	closed   bool
}

// shellSessionState tracks a persistent shell session.
type shellSessionState struct {
	outputCh chan []byte // output chunks arrive here
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
		conn:     conn,
		pending:  make(map[uint32]*pendingReq),
		sessions: make(map[uint32]*shellSessionState),
		readDone: make(chan struct{}),
	}
	c.nextID.Store(1)
	go c.readLoop()
	return c
}

// Dial connects to the agent at the given TCP address (host:port).
func Dial(addr string) (*Client, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	c := &Client{
		conn:     conn,
		pending:  make(map[uint32]*pendingReq),
		sessions: make(map[uint32]*shellSessionState),
		readDone: make(chan struct{}),
	}
	c.nextID.Store(1)
	go c.readLoop()
	return c, nil
}

// nextMsgID returns the next unique message ID.
func (c *Client) nextMsgID() uint32 {
	return c.nextID.Add(1) - 1
}

// readLoop reads frames from the connection and dispatches them to pending requests
// and shell sessions.
func (c *Client) readLoop() {
	defer func() {
		c.mu.Lock()
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
		close(c.readDone)
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

		log.Printf("[sandbox/client] unhandled frame MsgID=%d Type=%s", frame.MsgID, protocol.MessageTypeName(frame.Type))
	}
}

// waitFor sends a request and waits for the final Result/Error frame.
// Returns the final frame and any chunks received.
func (c *Client) waitFor(frame *protocol.Frame, timeout time.Duration) (*protocol.Frame, []*protocol.Frame, error) {
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
	c.pending[msgID] = p
	c.mu.Unlock()

	if err := protocol.WriteFrame(c.conn, frame); err != nil {
		c.cleanup(msgID)
		p.closeChunk()
		p.closeDone()
		return nil, nil, fmt.Errorf("send request: %w", err)
	}

	var chunks []*protocol.Frame
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	// Phase 1: collect chunks
	for {
		select {
		case chunk, ok := <-p.chunkCh:
			if !ok {
				goto waitDone
			}
			chunks = append(chunks, chunk)
		case final := <-p.done:
			// Result arrived before chunk channel closed
			c.cleanup(msgID)
			return final, chunks, nil
		case <-timer.C:
			c.cleanup(msgID)
			return nil, chunks, fmt.Errorf("timeout waiting for result")
		}
	}

waitDone:
	// Phase 2: wait for final result/error
	select {
	case final := <-p.done:
		c.cleanup(msgID)
		return final, chunks, nil
	case <-timer.C:
		c.cleanup(msgID)
		return nil, chunks, fmt.Errorf("timeout waiting for result")
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
func (c *Client) ReadFile(path string) (string, int64, error) {
	params, _ := json.Marshal(protocol.ReadParams{Path: path})
	frame := protocol.NewRequest(c.nextMsgID(), protocol.MethodRead, params)

	final, chunks, err := c.waitFor(frame, 60*time.Second)
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
func (c *Client) WriteFile(path, content string) (int64, error) {
	params, _ := json.Marshal(protocol.WriteParams{Path: path, Content: content})
	frame := protocol.NewRequest(c.nextMsgID(), protocol.MethodWrite, params)

	final, _, err := c.waitFor(frame, 120*time.Second)
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
func (c *Client) EditFile(path string, edits []protocol.Edit) (int64, error) {
	params, _ := json.Marshal(protocol.EditParams{Path: path, Edits: edits})
	frame := protocol.NewRequest(c.nextMsgID(), protocol.MethodEdit, params)

	final, _, err := c.waitFor(frame, 120*time.Second)
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
func (c *Client) Bash(command string, timeout int64) (*BashResultHolder, error) {
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
	c.pending[msgID] = p
	c.mu.Unlock()

	if err := protocol.WriteFrame(c.conn, frame); err != nil {
		c.cleanup(msgID)
		close(holder.outputCh)
		close(holder.exitCode)
		return nil, fmt.Errorf("send bash request: %w", err)
	}

	// Process response in background
	go func() {
		defer func() {
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

	final, _, err := c.waitFor(frame, 30*time.Second)
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
	return protocol.WriteFrame(c.conn, frame)
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
	return protocol.WriteFrame(c.conn, frame)
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
	return protocol.WriteFrame(c.conn, frame)
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
