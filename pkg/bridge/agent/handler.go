package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"

	"kanban/pkg/bridge/protocol"
)

// handler manages request dispatch and shell sessions for a single client connection.
type handler struct {
	mu          sync.Mutex
	sessions    map[uint32]*shellSession
	nextSession uint32
	writeMu     sync.Mutex        // serializes all writes to conn (shell readLoop + dispatch + read loop)
	closed      atomic.Bool       // set true when connection is torn down (atomic: accessed from readLoop + close)
}

func newHandler() *handler {
	h := &handler{
		sessions:    make(map[uint32]*shellSession),
		nextSession: 1,
	}
	h.closed.Store(true) // set to false by caller when connection is ready
	return h
}

// writeFrame sends a frame to conn under the write lock.
// Returns ErrClosed if the handler has been closed.
var errHandlerClosed = fmt.Errorf("handler closed")

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
	defer h.mu.Unlock()
	for id, s := range h.sessions {
		s.close()
		delete(h.sessions, id)
	}
}

// handleConn reads frames from conn and dispatches them to handlers.
// Binary shell methods (shell_io, shell_resize, shell_close) are handled
// synchronously on this goroutine. JSON methods are dispatched to goroutines.
func (h *handler) handleConn(conn net.Conn) error {
	for {
		frame, err := protocol.ReadFrame(conn)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil // clean disconnect
			}
			return fmt.Errorf("read frame: %w", err)
		}

		if frame.Type != protocol.TypeRequest {
			log.Printf("agent: unexpected frame type %s (MsgID=%d)", protocol.MessageTypeName(frame.Type), frame.MsgID)
			continue
		}

		// Check method from payload[0] before full parsing
		if len(frame.Payload) < 1 {
			continue
		}
		method := frame.Payload[0]

		// Binary shell methods — parse directly from frame
		switch method {
		case protocol.MethodShellIO:
			h.handleShellIO(conn, frame)
			continue
		case protocol.MethodShellResize:
			h.handleShellResize(conn, frame)
			continue
		case protocol.MethodShellClose:
			h.handleShellClose(conn, frame)
			continue
		}

		// JSON-based methods — parse into Request
		req, err := protocol.ParseRequest(frame)
		if err != nil {
			log.Printf("agent: parse request MsgID %d: %v", frame.MsgID, err)
			_ = h.writeFrame(conn, protocol.NewError(frame.MsgID, err.Error()))
			continue
		}

		go h.dispatch(conn, req)
	}
}

// dispatch parses JSON params and routes to the appropriate handler.
func (h *handler) dispatch(conn net.Conn, req *protocol.Request) {
	var err error
	switch req.Method {
	case protocol.MethodRead:
		err = handleRead(h, conn, req)
	case protocol.MethodWrite:
		err = handleWrite(h, conn, req)
	case protocol.MethodEdit:
		err = handleEdit(h, conn, req)
	case protocol.MethodBash:
		err = handleBash(h, conn, req)
	case protocol.MethodShellOpen:
		err = h.handleShellOpen(conn, req)
	default:
		_ = h.writeFrame(conn, protocol.NewError(req.MsgID, fmt.Sprintf("unknown method: %s", req.MethodString())))
		return
	}

	if err != nil {
		log.Printf("agent: MsgID %d method %s error: %v", req.MsgID, req.MethodString(), err)
		_ = h.writeFrame(conn, protocol.NewError(req.MsgID, err.Error()))
	}
}

// --- Shell binary method handlers ---

func (h *handler) handleShellIO(conn net.Conn, frame *protocol.Frame) {
	params, err := protocol.ParseShellIOParams(frame)
	if err != nil {
		log.Printf("agent: shell_io parse error: %v", err)
		return
	}

	h.mu.Lock()
	sess, ok := h.sessions[params.SessionID]
	h.mu.Unlock()
	if !ok {
		return // session already closed
	}

	if len(params.Input) > 0 {
		sess.write(params.Input)
	}
}

func (h *handler) handleShellResize(conn net.Conn, frame *protocol.Frame) {
	params, err := protocol.ParseShellResizeParams(frame)
	if err != nil {
		log.Printf("agent: shell_resize parse error: %v", err)
		return
	}

	h.mu.Lock()
	sess, ok := h.sessions[params.SessionID]
	h.mu.Unlock()
	if !ok {
		return
	}
	sess.resize(params.Rows, params.Cols)
}

func (h *handler) handleShellClose(conn net.Conn, frame *protocol.Frame) {
	params, err := protocol.ParseShellCloseParams(frame)
	if err != nil {
		log.Printf("agent: shell_close parse error: %v", err)
		return
	}

	h.mu.Lock()
	sess, ok := h.sessions[params.SessionID]
	if ok {
		delete(h.sessions, params.SessionID)
	}
	h.mu.Unlock()
	if ok {
		sess.close()
		log.Printf("agent: shell session %d closed", params.SessionID)
	}
}
