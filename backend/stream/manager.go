package stream

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"kanban/backend/git"
	"kanban/backend/store"
	"kanban/backend/types"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// StreamManager manages broadcasters for all active tasks.
type StreamManager struct {
	mu               sync.RWMutex
	broadcasts       map[string]*Broadcaster
	store            *store.Store
	lazyStartHandler LazyStartHandler
	lazyMu           sync.RWMutex
	worktreeMgr      *git.WorktreeManager
	// pollers tracks the git status poller context per task (one poller per task, not per client)
	pollersMu sync.Mutex
	pollers   map[string]context.CancelFunc
}

// NewStreamManager creates a new StreamManager.
func NewStreamManager(store *store.Store, worktreeMgr *git.WorktreeManager) *StreamManager {
	return &StreamManager{
		broadcasts:  make(map[string]*Broadcaster),
		store:       store,
		worktreeMgr: worktreeMgr,
		pollers:     make(map[string]context.CancelFunc),
	}
}

// SetLazyStartHandler sets the callback for lazy-starting an executor.
func (sm *StreamManager) SetLazyStartHandler(h LazyStartHandler) {
	sm.lazyMu.Lock()
	defer sm.lazyMu.Unlock()
	sm.lazyStartHandler = h
}

// GetOrCreate returns an existing broadcaster or creates a new one.
func (sm *StreamManager) GetOrCreate(taskID string) *Broadcaster {
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
func (sm *StreamManager) Get(taskID string) *Broadcaster {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.broadcasts[taskID]
}

// pollGitStatus runs a polling loop that checks git diff HEAD --shortstat in the task's worktree.
// When the shortstat output changes (line counts differ), it sends a diff_changed event to all connected clients.
// Using --shortstat instead of status --porcelain ensures we detect content changes to the same file
// (e.g., "+5" becoming "+10" additions), not just new/modified file states.
func (sm *StreamManager) pollGitStatus(ctx context.Context, taskID string, b *Broadcaster) {
	var lastShortstat string
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()

	// Initial check to establish baseline
	if shortstat, err := sm.worktreeMgr.GitShortstat(taskID); err == nil {
		lastShortstat = shortstat
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			shortstat, err := sm.worktreeMgr.GitShortstat(taskID)
			if err != nil {
				log.Printf("[git poll] task=%s shortstat check failed: %v", taskID, err)
				continue
			}
			if shortstat != lastShortstat {
				log.Printf("[git poll] task=%s diff changed", taskID)
				lastShortstat = shortstat
				taskDiff, err := sm.worktreeMgr.GetTaskDiff(taskID)
				if err != nil {
					log.Printf("[git poll] task=%s GetTaskDiff failed: %v", taskID, err)
					continue
				}
				b.Send(types.StreamEvent{Type: types.WSTypeDiffChanged, Data: taskDiff})
			}
		}
	}
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

	// Set up git status poller lifecycle: starts when first client connects, stops when last disconnects
	if sm.worktreeMgr != nil {
		b.SetLifecycleCallback(func(hasClients bool) {
			if hasClients {
				// Don't poll for tasks that haven't started (no worktree exists)
				task, _ := sm.store.GetTaskByTaskID(taskID)
				if task != nil && task.Status == "backlog" {
					return
				}
				sm.pollersMu.Lock()
				if _, exists := sm.pollers[taskID]; !exists {
					ctx, cancel := context.WithCancel(context.Background())
					sm.pollers[taskID] = cancel
					sm.pollersMu.Unlock()
					go sm.pollGitStatus(ctx, taskID, b)
				} else {
					sm.pollersMu.Unlock()
				}
			} else {
				sm.pollersMu.Lock()
				if cancel, exists := sm.pollers[taskID]; exists {
					cancel()
					delete(sm.pollers, taskID)
				}
				sm.pollersMu.Unlock()
			}
		})
	}

	b.add(conn)
	defer b.remove(conn)

	// Ping/pong keepalive to prevent proxy timeouts
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := conn.Ping(context.Background()); err != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()

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
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.Type == "user_message" {
			var payload struct {
				Content        string `json:"content"`
				ModelName      string `json:"model_name"`
				LlamaServerURL string `json:"llama_server_url"`
			}
			if err := json.Unmarshal(msg.Data, &payload); err == nil && payload.Content != "" {
				log.Printf("[stream] task=%s user_message received (model=%s, url=%s)", taskID, payload.ModelName, payload.LlamaServerURL)
				// Get the task to find its current thread
				task, _ := sm.store.GetTaskByTaskID(taskID)
				threadID := ""
				if task != nil {
					threadID = task.CurrentThreadID
				}
				// Always persist the user message
				msgID := uuid.New().String()
				sm.store.CreateTaskMessage(&types.TaskMessage{
					ID:        msgID,
					TaskID:    taskID,
					ThreadID:  threadID,
					Role:      "user",
					Content:   payload.Content,
					CreatedAt: time.Now().UTC(),
				})
				// Broadcast back to all clients (include backend-generated id + thread_id)
				echoData := map[string]interface{}{"id": msgID, "role": "user", "content": payload.Content}
				if threadID != "" {
					echoData["thread_id"] = threadID
				}
				b.Send(types.StreamEvent{Type: types.WSTypeUserMessage, Data: echoData})
				// If executor is running, forward to it (with model info)
				handler := b.getUserMessageHandler()
				if handler != nil {
					log.Printf("[stream] task=%s forwarding to executor", taskID)
					handler(UserMessage{
						Content:   payload.Content,
						ModelName: payload.ModelName,
						ServerURL: payload.LlamaServerURL,
					})
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
						log.Printf("[stream] task=%s project=%s calling lazy-start (model=%s, url=%s)", taskID, projectID, payload.ModelName, payload.LlamaServerURL)
						go lazyHandler(projectID, taskID, payload.Content, payload.ModelName, payload.LlamaServerURL)
					} else {
						log.Printf("[stream] task=%s no lazy-start handler registered", taskID)
					}
				}
			}
		}
	}
}
