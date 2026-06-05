package backend

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// WSMessageType is the type of a WebSocket message.
type WSMessageType string

const (
	WSTypeToken       WSMessageType = "token"
	WSTypeReasoning   WSMessageType = "reasoning"
	WSTypeReasoningEnd WSMessageType = "reasoning_end"
	WSTypeToolCall    WSMessageType = "tool_call"
	WSTypeToolResult  WSMessageType = "tool_result"
	WSTypeStatus      WSMessageType = "status"
	WSTypeQueue       WSMessageType = "queue"
	WSTypeComplete    WSMessageType = "complete"
	WSTypeError       WSMessageType = "error"
	WSTypeUserMessage WSMessageType = "user_message"
	WSTypeStats       WSMessageType = "stats"
)

// StreamEvent carries a typed payload to broadcast.
type StreamEvent struct {
	Type WSMessageType
	Data interface{}
}

// LazyStartHandler is called when a user message arrives for a task with no active executor.
type LazyStartHandler func(projectID, taskID, content string)

// broadcaster fans out events to multiple WebSocket clients.
type broadcaster struct {
	mu          sync.RWMutex
	clients     map[*websocket.Conn]bool
	bufMu       sync.Mutex
	ringBuffer  []WSMessage
	bufSize     int
	seqCounter  atomic.Int64
	closed      atomic.Bool
	userMsgHandler func(string)
	handlerMu    sync.RWMutex
}

func newBroadcaster(bufSize int) *broadcaster {
	if bufSize == 0 {
		bufSize = 100
	}
	return &broadcaster{
		clients:    make(map[*websocket.Conn]bool),
		ringBuffer: make([]WSMessage, 0, bufSize),
		bufSize:    bufSize,
	}
}

// clearBuffer clears the ring buffer. Called when a new executor starts
// to discard events from a previous executor.
func (b *broadcaster) clearBuffer() {
	b.bufMu.Lock()
	defer b.bufMu.Unlock()
	b.ringBuffer = b.ringBuffer[:0]
}

// SetUserMessageHandler sets the callback for forwarding user messages to the executor.
func (b *broadcaster) SetUserMessageHandler(h func(string)) {
	b.handlerMu.Lock()
	defer b.handlerMu.Unlock()
	b.userMsgHandler = h
}

// getUserMessageHandler returns the current user message handler.
func (b *broadcaster) getUserMessageHandler() func(string) {
	b.handlerMu.RLock()
	defer b.handlerMu.RUnlock()
	return b.userMsgHandler
}

// add registers a new client.
func (b *broadcaster) add(conn *websocket.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed.Load() {
		b.clients[conn] = true
	}
}

// remove unregisters a client.
func (b *broadcaster) remove(conn *websocket.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.clients, conn)
}

// send broadcasts an event to all connected clients and buffers it.
func (b *broadcaster) send(event StreamEvent) {
	msg, err := json.Marshal(event.Data)
	if err != nil {
		return
	}

	wsMsg := WSMessage{Type: string(event.Type), Data: msg}
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
func (b *broadcaster) getBuffer(fromSeq int64) []WSMessage {
	b.bufMu.Lock()
	defer b.bufMu.Unlock()
	if fromSeq <= 0 {
		return b.ringBuffer
	}
	return b.ringBuffer
}

// close stops the broadcaster.
func (b *broadcaster) close() {
	b.closed.Store(true)
	b.mu.Lock()
	defer b.mu.Unlock()
	for conn := range b.clients {
		conn.CloseNow()
	}
	b.clients = nil
}

// StreamManager manages broadcasters for all active tasks.
type StreamManager struct {
	mu             sync.RWMutex
	broadcasts     map[string]*broadcaster
	store          *Store
	lazyStartHandler LazyStartHandler
	lazyMu         sync.RWMutex
}

// NewStreamManager creates a new StreamManager.
func NewStreamManager(store *Store) *StreamManager {
	return &StreamManager{
		broadcasts: make(map[string]*broadcaster),
		store:      store,
	}
}

// SetLazyStartHandler sets the callback for lazy-starting an executor.
func (sm *StreamManager) SetLazyStartHandler(h LazyStartHandler) {
	sm.lazyMu.Lock()
	defer sm.lazyMu.Unlock()
	sm.lazyStartHandler = h
}

// GetOrCreate returns an existing broadcaster or creates a new one.
func (sm *StreamManager) GetOrCreate(taskID string) *broadcaster {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	b, ok := sm.broadcasts[taskID]
	if !ok {
		b = newBroadcaster(100)
		sm.broadcasts[taskID] = b
	}
	return b
}

// Remove removes and closes a broadcaster.
func (sm *StreamManager) Remove(taskID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if b, ok := sm.broadcasts[taskID]; ok {
		b.close()
		delete(sm.broadcasts, taskID)
	}
}

// Get returns a broadcaster if it exists.
func (sm *StreamManager) Get(taskID string) *broadcaster {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.broadcasts[taskID]
}

// ResetForTest closes all broadcasters and clears state. Used by e2e tests.
func (sm *StreamManager) ResetForTest() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for taskID, b := range sm.broadcasts {
		b.close()
		delete(sm.broadcasts, taskID)
	}
}

// HandleStream upgrades a WebSocket connection and subscribes to a task stream.
func (sm *StreamManager) HandleStream(w http.ResponseWriter, r *http.Request, taskID string) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()

	b := sm.GetOrCreate(taskID)
	b.add(conn)
	defer b.remove(conn)

	// Send buffered events on reconnect
	if msg := r.URL.Query().Get("sync"); msg != "" {
		buf := b.getBuffer(0)
		for _, m := range buf {
			frame, _ := json.Marshal(m)
			if err := conn.Write(context.Background(), websocket.MessageText, frame); err != nil {
				return
			}
		}
	}

	// Keep reading to detect client close and handle user messages
	for {
		_, data, err := conn.Read(context.Background())
		if err != nil {
			break
		}
		var msg struct {
			Type string `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.Type == "user_message" {
			var payload struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal(msg.Data, &payload); err == nil && payload.Content != "" {
				log.Printf("[stream] task=%s user_message received", taskID)
				// Always persist the user message
				sm.store.CreateTaskMessage(&TaskMessage{
					ID:        generateUUID(),
					TaskID:    taskID,
					Role:      "user",
					Content:   payload.Content,
					CreatedAt: time.Now().UTC(),
				})
				// Always broadcast back to all clients
				b.send(StreamEvent{Type: WSTypeUserMessage, Data: map[string]string{"role": "user", "content": payload.Content}})
				// If executor is running, forward to it
				handler := b.getUserMessageHandler()
				if handler != nil {
					log.Printf("[stream] task=%s forwarding to executor", taskID)
					handler(payload.Content)
				} else {
					// No executor running — lazy-start one
					log.Printf("[stream] task=%s no executor, trying lazy-start", taskID)
					sm.lazyMu.RLock()
					lazyHandler := sm.lazyStartHandler
					sm.lazyMu.RUnlock()
					if lazyHandler != nil {
						task, err := sm.store.GetTaskByTaskID(taskID)
						var projectID string
						if err == nil && task != nil {
							projectID = task.ProjectID
						}
						log.Printf("[stream] task=%s project=%s calling lazy-start", taskID, projectID)
						go lazyHandler(projectID, taskID, payload.Content)
					} else {
						log.Printf("[stream] task=%s no lazy-start handler registered", taskID)
					}
				}
			}
		}
	}
}
