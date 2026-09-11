package main

import (
	"fmt"
	"io"
	"net"
	"os/exec"
	"sync"
	"sync/atomic"

	"kanban/pkg/bridge/protocol"
	"kanban/pkg/logging"
	"kanban/pkg/procterm"
)

// handler manages request dispatch for a single client connection.
type handler struct {
	mu        sync.Mutex
	bashProcs map[uint32]*bashProc // msgID → running bash process
	lsp       *lspState            // LSP server state (one per connection)
	writeMu   sync.Mutex           // serializes all writes to conn (bash streaming + dispatch + read loop)
	closed    atomic.Bool          // set true when connection is torn down (atomic: accessed from readLoop + close)
	fileLocks sync.Map             // path → *sync.Mutex, prevents concurrent read-modify-write on same file
}

// lockFile returns a lock for a file path and its unlock function, so only one
// read-modify-write cycle (e.g. handleEdit) runs per file at a time.
func (h *handler) lockFile(path string) func() {
	lock, _ := h.fileLocks.LoadOrStore(path, &sync.Mutex{})
	mtx := lock.(*sync.Mutex)
	mtx.Lock()
	return func() { mtx.Unlock() }
}

// bashProc tracks a running bash command for cancellation.
type bashProc struct {
	cmd  *exec.Cmd
	done chan struct{} // closed when the process exits (normal, killed, or cancelled)

	// reaped is set once cmd.Wait() has returned. Signalling the process group is
	// only safe while the leader is alive: after the reap its pid (== the pgid) can
	// be recycled by an unrelated process.
	reaped atomic.Bool

	// mu guards cmd (cmd.Start() writes cmd.Process while a bash_cancel may be
	// looking at it) and, with finished, orders the output frames: no Chunk may be
	// written after the Result frame, because the client drops it.
	mu       sync.Mutex
	finished bool
}

// kill stops the command's whole process group. The reaped guard is this caller's
// business: once the leader is reaped its pid, which is the pgid, can be recycled onto an
// unrelated process.
func (bp *bashProc) kill() {
	if bp.cmd == nil {
		return
	}
	bp.mu.Lock()
	defer bp.mu.Unlock()
	if bp.reaped.Load() {
		return
	}
	_ = procterm.KillGroup(bp.cmd.Process)
}

// pid returns the command's pid, or 0 before it started.
func (bp *bashProc) pid() int {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	if bp.cmd == nil || bp.cmd.Process == nil {
		return 0
	}
	return bp.cmd.Process.Pid
}

func newHandler() *handler {
	h := &handler{
		bashProcs: make(map[uint32]*bashProc),
	}
	h.closed.Store(true) // set to false by caller when connection is ready
	return h
}

// recoverPanic is a deferred recovery for handler goroutines
func (h *handler) recoverPanic(conn net.Conn, msgID uint32) {
	if r := recover(); r != nil {
		logging.For(logging.Bridge).Warnf("agent: recovered panic (MsgID=%d): %v", msgID, r)
		// Best-effort: report the error to the host so the pending request resolves
		// instead of hanging until a timeout. Notifications (msgID 0) have no
		// pending request to resolve.
		if msgID != 0 {
			_ = h.writeFrame(conn, protocol.NewError(msgID, fmt.Sprintf("agent panic: %v", r)))
		}
	}
}

// errHandlerClosed is returned once the handler has been closed.
var errHandlerClosed = fmt.Errorf("handler closed")

// writeFrame sends a frame to conn under the write lock.
func (h *handler) writeFrame(conn net.Conn, frame *protocol.Frame) error {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	if h.closed.Load() {
		return errHandlerClosed
	}
	return protocol.WriteFrame(conn, frame)
}

func (h *handler) close() {
	h.closed.Store(true) // prevent readLoop from writing to closed connection
	h.mu.Lock()
	// Kill all tracked bash processes (and their process groups)
	for id, bp := range h.bashProcs {
		bp.kill()
		delete(h.bashProcs, id)
	}
	// The language server belongs to this connection too. Otherwise every dropped bridge —
	// a retired workspace, a closed editor, a finished turn — orphans a server in the sandbox
	// until the container itself stops.
	if h.lsp != nil && h.lsp.session != nil {
		h.lsp.session.close()
		h.lsp = nil
	}
	h.mu.Unlock()
}

// handleConn reads frames and dispatches them: binary methods (bash_cancel) synchronously on
// this goroutine, JSON methods on their own.
func (h *handler) handleConn(conn net.Conn) error {
	for {
		frame, err := protocol.ReadFrame(conn)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil // clean disconnect
			}
			return fmt.Errorf("read frame: %w", err)
		}

		// Check method from payload[0] before full parsing
		if len(frame.Payload) < 1 {
			continue
		}
		method := frame.Payload[0]

		switch frame.Type {
		case protocol.TypeNotification:
			// Notifications (e.g., LSP notifications like initialized, didOpen)
			if method == protocol.MethodLSP {
				go h.handleLSPNotify(conn, frame)
			}
			continue

		case protocol.TypeRequest:
			// Binary methods — parse directly from frame
			switch method {
			case protocol.MethodLSP:
				req, err := protocol.ParseRequest(frame)
				if err != nil {
					logging.For(logging.Bridge).Infof("agent: parse request MsgID %d: %v", frame.MsgID, err)
					_ = h.writeFrame(conn, protocol.NewError(frame.MsgID, err.Error()))
					continue
				}
				go h.handleLSP(conn, req)
				continue
			case protocol.MethodBashCancel:
				h.handleBashCancel(frame)
				continue
			}

			// JSON-based methods — parse into Request
			req, err := protocol.ParseRequest(frame)
			if err != nil {
				logging.For(logging.Bridge).Infof("agent: parse request MsgID %d: %v", frame.MsgID, err)
				_ = h.writeFrame(conn, protocol.NewError(frame.MsgID, err.Error()))
				continue
			}

			go h.dispatch(conn, req)
		}
	}
}

// dispatch parses JSON params and routes to the appropriate handler.
func (h *handler) dispatch(conn net.Conn, req *protocol.Request) {
	defer h.recoverPanic(conn, req.MsgID)
	var err error
	switch req.Method {
	case protocol.MethodRead:
		err = handleRead(h, conn, req)
	case protocol.MethodWrite:
		err = handleWrite(h, conn, req)
	case protocol.MethodWriteBytes:
		err = handleWriteBytes(h, conn, req)
	case protocol.MethodEdit:
		err = handleEdit(h, conn, req)
	case protocol.MethodBash:
		err = handleBash(h, conn, req)
	default:
		_ = h.writeFrame(conn, protocol.NewError(req.MsgID, fmt.Sprintf("unknown method: %s", req.MethodString())))
		return
	}

	if err != nil {
		logging.For(logging.Bridge).Warnf("agent: MsgID %d method %s error: %v", req.MsgID, req.MethodString(), err)
		_ = h.writeFrame(conn, protocol.NewError(req.MsgID, err.Error()))
	}
}

// handleBashCancel kills a running bash process identified by its message ID.
// This is a fire-and-forget operation — no response frame is sent.
func (h *handler) handleBashCancel(frame *protocol.Frame) {
	params, err := protocol.ParseBashCancelParams(frame)
	if err != nil {
		logging.For(logging.Bridge).Warnf("agent: bash_cancel parse error: %v", err)
		return
	}

	h.mu.Lock()
	bp, ok := h.bashProcs[params.TargetMsgID]
	if ok {
		delete(h.bashProcs, params.TargetMsgID)
	}
	h.mu.Unlock()

	if !ok {
		logging.For(logging.Bridge).Infof("agent: bash_cancel MsgID=%d not found (already completed)", params.TargetMsgID)
		return
	}

	if pid := bp.pid(); pid > 0 {
		logging.For(logging.Bridge).Infof("agent: bash_cancel MsgID=%d killing process group pgid=%d", params.TargetMsgID, pid)
	}
	bp.kill()
	// Signal the done channel so the handleBash goroutine unblocks
	select {
	case <-bp.done:
		// Already closed by handleBash
	default:
		close(bp.done)
	}
}
