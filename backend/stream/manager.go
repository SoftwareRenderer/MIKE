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
)

// StreamManager manages broadcasters for all active tasks.
type StreamManager struct {
	mu               sync.RWMutex
	broadcasts       map[string]*Broadcaster
	store            *store.Store
	lazyStartHandler LazyStartHandler
	lazyMu           sync.RWMutex
	worktreeMgr      *git.WorktreeManager
	boardStreamMgr   *BoardStreamManager
	// pollers tracks the git status poller context per task (one poller per task, not per client)
	pollersMu sync.Mutex
	pollers   map[string]context.CancelFunc
}

// NewStreamManager creates a new StreamManager.
func NewStreamManager(store *store.Store, worktreeMgr *git.WorktreeManager, boardStreamMgr *BoardStreamManager) *StreamManager {
	return &StreamManager{
		broadcasts:     make(map[string]*Broadcaster),
		store:          store,
		worktreeMgr:    worktreeMgr,
		boardStreamMgr: boardStreamMgr,
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

// pollGitStatus runs a polling loop that checks git diff --raw in the task's worktree.
// Diffs are computed relative to the task's base_ref (or HEAD if not set).
// Uses --raw output (per-file blob hashes) for change detection — any content change
// produces a new hash, even same-line same-character-count edits.
// When a change is detected, it fetches the full diff and sends a diff_changed event.
// If the worktree doesn't exist yet (task in backlog), polling is skipped until it appears.
func (sm *StreamManager) pollGitStatus(ctx context.Context, taskID string, b *Broadcaster) {
	var lastRaw string
	var baselineSet bool
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			baseRef := sm.getDiffBaseline(taskID)
			raw, err := sm.worktreeMgr.GitDiffRaw(taskID, baseRef)
			if err != nil {
				// Worktree may not exist yet (task still in backlog) — skip silently
				continue
			}
			// First successful check — establish baseline, don't send event
			if !baselineSet {
				lastRaw = raw
				baselineSet = true
				continue
			}
			if raw != lastRaw {
				log.Printf("[git poll] task=%s diff changed", taskID)
				lastRaw = raw
				taskDiff, err := sm.worktreeMgr.GetTaskDiff(taskID, baseRef, "")
				if err != nil {
					log.Printf("[git poll] task=%s GetTaskDiff failed: %v", taskID, err)
					continue
				}
				b.Send(types.StreamEvent{Type: types.WSTypeDiffChanged, Data: taskDiff})
			}
		}
	}
}

// getDiffBaseline computes the effective diff baseline for a task.
func (sm *StreamManager) getDiffBaseline(taskID string) string {
	task, err := sm.store.GetTaskByTaskID(taskID)
	if err != nil || task.Branch == "" {
		return ""
	}
	thread, _ := sm.store.GetThread(task.CurrentThreadID)
	forkBranch := ""
	if thread != nil {
		forkBranch = thread.GitBranch
	}
	baseRef, _ := sm.worktreeMgr.ResolveDiffBaseline(taskID, task.BaseRef, task.Branch, forkBranch)
	return baseRef
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
		CompressionMode:    websocket.CompressionContextTakeover,
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	log.Printf("[stream] task=%s WS connection accepted", taskID)

	b := sm.GetOrCreate(taskID)

	// Set up git status poller lifecycle: starts when first client connects, stops when last disconnects
	if sm.worktreeMgr != nil {
		b.SetLifecycleCallback(func(hasClients bool) {
			if hasClients {
				sm.pollersMu.Lock()
				if _, exists := sm.pollers[taskID]; !exists {
					ctx, cancel := context.WithCancel(context.Background())
					sm.pollers[taskID] = cancel
					sm.pollersMu.Unlock()
					log.Printf("[git poll] task=%s starting poller", taskID)
					go sm.pollGitStatus(ctx, taskID, b)
				} else {
					sm.pollersMu.Unlock()
				}
			} else {
				sm.pollersMu.Lock()
				if cancel, exists := sm.pollers[taskID]; exists {
					cancel()
					delete(sm.pollers, taskID)
					sm.pollersMu.Unlock()
					log.Printf("[git poll] task=%s stopping poller (no clients)", taskID)
				} else {
					sm.pollersMu.Unlock()
				}
			}
		})
	}

	b.add(conn)
	defer b.remove(conn)

	// Ping/pong keepalive to prevent proxy timeouts.
	// done channel signals both the pinger and the sync-buffer goroutine to stop.
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(27 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := b.PingConn(conn); err != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()

	// Send buffered events on reconnect (async so read loop starts immediately).
	if msg := r.URL.Query().Get("sync"); msg != "" {
		go func() {
			buf := b.getBuffer(0)
			for _, m := range buf {
				select {
				case <-done:
					return
				default:
				}
				frame, _ := json.Marshal(m)
				if err := conn.Write(context.Background(), websocket.MessageText, frame); err != nil {
					return
				}
			}
		}()
	}

	// Keep reading to detect client close.
	// User messages are now sent via POST /send endpoint.
	for {
		_, _, err := conn.Read(context.Background())
		if err != nil {
			break
		}
	}
}
