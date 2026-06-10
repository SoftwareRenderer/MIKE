package stream

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"

	"kanban/backend/types"

	"github.com/coder/websocket"
)

// UserMessage carries a user message with optional model override.
type UserMessage struct {
	Content   string
	ModelName string
	ServerURL string
}

// LazyStartHandler is called when a user message arrives for a task with no active executor.
type LazyStartHandler func(projectID, taskID, content, modelName, serverURL string)

// Broadcaster fans out events to multiple WebSocket clients.
type Broadcaster struct {
	mu             sync.RWMutex
	clients        map[*websocket.Conn]bool
	bufMu          sync.Mutex
	ringBuffer     []types.WSMessage
	bufSize        int
	seqCounter     atomic.Int64
	closed         atomic.Bool
	userMsgHandler func(UserMessage)
	handlerMu      sync.RWMutex
}

func newBroadcaster(bufSize int) *Broadcaster {
	if bufSize == 0 {
		bufSize = 100
	}
	return &Broadcaster{
		clients:    make(map[*websocket.Conn]bool),
		ringBuffer: make([]types.WSMessage, 0, bufSize),
		bufSize:    bufSize,
	}
}

// clearBuffer clears the ring buffer.
func (b *Broadcaster) clearBuffer() {
	b.bufMu.Lock()
	defer b.bufMu.Unlock()
	b.ringBuffer = b.ringBuffer[:0]
}

// SetUserMessageHandler sets the callback for forwarding user messages to the executor.
func (b *Broadcaster) SetUserMessageHandler(h func(UserMessage)) {
	b.handlerMu.Lock()
	defer b.handlerMu.Unlock()
	b.userMsgHandler = h
}

// getUserMessageHandler returns the current user message handler.
func (b *Broadcaster) getUserMessageHandler() func(UserMessage) {
	b.handlerMu.RLock()
	defer b.handlerMu.RUnlock()
	return b.userMsgHandler
}

// add registers a new client.
func (b *Broadcaster) add(conn *websocket.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed.Load() {
		b.clients[conn] = true
	}
}

// remove unregisters a client.
func (b *Broadcaster) remove(conn *websocket.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.clients, conn)
}

// Send broadcasts an event to all connected clients and buffers it.
func (b *Broadcaster) Send(event types.StreamEvent) {
	msg, err := json.Marshal(event.Data)
	if err != nil {
		return
	}

	wsMsg := types.WSMessage{Type: string(event.Type), Data: msg}
	b.seqCounter.Add(1)

	// Buffer the message
	b.bufMu.Lock()
	if len(b.ringBuffer) >= b.bufSize {
		b.ringBuffer = b.ringBuffer[1:]
	}
	b.ringBuffer = append(b.ringBuffer, wsMsg)
	b.bufMu.Unlock()

	// Send to all connected clients
	b.mu.RLock()
	defer b.mu.RUnlock()
	for conn := range b.clients {
		if b.closed.Load() {
			break
		}
		frame, err := json.Marshal(wsMsg)
		if err != nil {
			continue
		}
		if err := conn.Write(context.Background(), websocket.MessageText, frame); err != nil {
			// Client disconnected, will be cleaned up
		}
	}
}

// getBuffer returns the last N buffered messages.
func (b *Broadcaster) getBuffer(fromSeq int64) []types.WSMessage {
	b.bufMu.Lock()
	defer b.bufMu.Unlock()
	if fromSeq <= 0 {
		return b.ringBuffer
	}
	return b.ringBuffer
}

// close stops the broadcaster.
func (b *Broadcaster) close() {
	b.closed.Store(true)
	b.mu.Lock()
	defer b.mu.Unlock()
	for conn := range b.clients {
		conn.CloseNow()
	}
	b.clients = nil
}
