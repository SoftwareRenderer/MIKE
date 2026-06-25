package stream

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"kanban/backend/git"
	"kanban/backend/store"
	"kanban/backend/types"

	"github.com/coder/websocket"
)

const (
	// DefaultBufferSize is the default ring buffer size per connection.
	DefaultBufferSize = 500
	// MaxBufferSize is the maximum ring buffer size per connection.
	MaxBufferSize = 2000
)

// UserMessage carries a user message with optional model override.
type UserMessage struct {
	Content     string
	ModelName   string
	ServerURL   string
	Attachments string // JSON array of {mimeType, data} for images
}

// LazyStartHandler is called when a user message arrives for a task with no active executor.
type LazyStartHandler func(projectID, taskID, content, modelName, serverURL string)

// clientState holds the subscription state for a single WebSocket connection.
type clientState struct {
	conn       *websocket.Conn
	projectSub map[string]int // projectID -> subscriber count (for multi-tab support)
	taskSub    map[string]int // "projectID:taskID" -> subscriber count
	bufMu      sync.Mutex
	ringBuffer [][]byte // raw JSON frames for replay
	bufSize    int
}

// AppStreamMessage is the envelope for client-to-server messages.
type AppStreamMessage struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// AppStreamSubscription is the data for subscribe/unsubscribe messages.
type AppStreamSubscription struct {
	ProjectID string `json:"project_id"`
	TaskID    string `json:"task_id,omitempty"`
}

// AppStreamUserMessage is the data for user_message from the WS (fallback, primary is POST /send).
type AppStreamUserMessage struct {
	ProjectID     string `json:"project_id"`
	TaskID        string `json:"task_id"`
	Content       string `json:"content"`
	ModelName     string `json:"model_name,omitempty"`
	ServerURL     string `json:"server_url,omitempty"`
	Attachments   string `json:"attachments,omitempty"`
}

// diffSnapshot holds the last-known git state for a task, used to detect changes.
type diffSnapshot struct {
	raw        string
	shortStat  string
	untrackedN int
}

// branchSnapshot holds the last-known branch for a project.
type branchSnapshot struct {
	branch string
}

// AppStreamManager manages a single WebSocket endpoint for all app events.
// Clients connect once at /api/stream and subscribe to projects/tasks.
type AppStreamManager struct {
	mu      sync.RWMutex
	clients map[*websocket.Conn]*clientState
	writeMu sync.Mutex // serializes all writes to prevent concurrent Read/Write races
	store   *store.Store
	worktreeMgr *git.WorktreeManager

	// Diff pollers — one per project, starts when first client subscribes
	pollersMu sync.Mutex
	pollers   map[string]context.CancelFunc

	// User message handler — set by executor for forwarding WS user messages
	userMsgHandler func(AppStreamUserMessage)
	handlerMu      sync.RWMutex

	// Lazy start handler — called when a user message arrives for a task with no active executor
	lazyStartHandler LazyStartHandler
	lazyMu           sync.RWMutex
}

// NewAppStreamManager creates a new AppStreamManager.
func NewAppStreamManager(store *store.Store, worktreeMgr *git.WorktreeManager) *AppStreamManager {
	return &AppStreamManager{
		clients:     make(map[*websocket.Conn]*clientState),
		store:       store,
		worktreeMgr: worktreeMgr,
		pollers:     make(map[string]context.CancelFunc),
	}
}

// SetUserMessageHandler sets the callback for forwarding user messages.
func (asm *AppStreamManager) SetUserMessageHandler(h func(AppStreamUserMessage)) {
	asm.handlerMu.Lock()
	defer asm.handlerMu.Unlock()
	asm.userMsgHandler = h
}

// GetUserMessageHandler returns the current user message handler.
func (asm *AppStreamManager) GetUserMessageHandler() func(AppStreamUserMessage) {
	asm.handlerMu.RLock()
	defer asm.handlerMu.RUnlock()
	return asm.userMsgHandler
}

// SetLazyStartHandler sets the callback for lazy-starting an executor.
func (asm *AppStreamManager) SetLazyStartHandler(h LazyStartHandler) {
	asm.lazyMu.Lock()
	defer asm.lazyMu.Unlock()
	asm.lazyStartHandler = h
}

// GetLazyStartHandler returns the current lazy start handler.
func (asm *AppStreamManager) GetLazyStartHandler() LazyStartHandler {
	asm.lazyMu.RLock()
	defer asm.lazyMu.RUnlock()
	return asm.lazyStartHandler
}

// -- Subscription management --

// subscribeProject adds a project subscription for a client.
func (asm *AppStreamManager) subscribeProject(conn *websocket.Conn, projectID string) {
	asm.mu.Lock()
	client := asm.clients[conn]
	if client == nil {
		asm.mu.Unlock()
		return
	}
	if client.projectSub == nil {
		client.projectSub = make(map[string]int)
	}
	client.projectSub[projectID]++
	wasEmpty := len(client.projectSub) == 1 // first project subscription
	asm.mu.Unlock()

	// Start diff poller if this is the first subscriber to this project
	if wasEmpty {
		asm.startPoller(projectID)
	}
}

// unsubscribeProject removes a project subscription for a client.
func (asm *AppStreamManager) unsubscribeProject(conn *websocket.Conn, projectID string) {
	asm.mu.Lock()
	client := asm.clients[conn]
	if client == nil {
		asm.mu.Unlock()
		return
	}
	if client.projectSub != nil {
		if count, ok := client.projectSub[projectID]; ok {
			if count <= 1 {
				delete(client.projectSub, projectID)
			} else {
				client.projectSub[projectID] = count - 1
			}
		}
	}
	hasSubs := len(client.projectSub) > 0
	asm.mu.Unlock()

	// Stop poller if no clients are subscribed to this project
	if !hasSubs {
		asm.stopPoller(projectID)
	}
}

// subscribeTask adds a task subscription for a client.
func (asm *AppStreamManager) subscribeTask(conn *websocket.Conn, projectID, taskID string) {
	asm.mu.Lock()
	client := asm.clients[conn]
	if client == nil {
		asm.mu.Unlock()
		return
	}
	if client.taskSub == nil {
		client.taskSub = make(map[string]int)
	}
	key := projectID + ":" + taskID
	client.taskSub[key]++
	asm.mu.Unlock()
}

// unsubscribeTask removes a task subscription for a client.
func (asm *AppStreamManager) unsubscribeTask(conn *websocket.Conn, projectID, taskID string) {
	asm.mu.Lock()
	client := asm.clients[conn]
	if client == nil {
		asm.mu.Unlock()
		return
	}
	if client.taskSub != nil {
		key := projectID + ":" + taskID
		if count, ok := client.taskSub[key]; ok {
			if count <= 1 {
				delete(client.taskSub, key)
			} else {
				client.taskSub[key] = count - 1
			}
		}
	}
	asm.mu.Unlock()
}

// hasProjectSubscribers returns true if any client is subscribed to the project.
func (asm *AppStreamManager) hasProjectSubscribers(projectID string) bool {
	asm.mu.RLock()
	defer asm.mu.RUnlock()
	for _, client := range asm.clients {
		if count, ok := client.projectSub[projectID]; ok && count > 0 {
			return true
		}
	}
	return false
}

// hasTaskSubscribers returns true if any client is subscribed to the task.
func (asm *AppStreamManager) hasTaskSubscribers(projectID, taskID string) bool {
	asm.mu.RLock()
	defer asm.mu.RUnlock()
	key := projectID + ":" + taskID
	for _, client := range asm.clients {
		if count, ok := client.taskSub[key]; ok && count > 0 {
			return true
		}
	}
	return false
}

// -- Broadcast methods --

// SendBoardEvent sends a board-level event to all clients subscribed to the project.
func (asm *AppStreamManager) SendBoardEvent(projectID string, event types.StreamEvent) {
	asm.sendBoardPayload(projectID, event.Data)
}

// sendBoardPayload sends a payload directly to all clients subscribed to the project.
// The payload is JSON-marshaled and sent as the WebSocket frame.
func (asm *AppStreamManager) sendBoardPayload(projectID string, payload interface{}) {
	asm.mu.RLock()
	targetConns := make([]*websocket.Conn, 0)
	for conn, client := range asm.clients {
		if count, ok := client.projectSub[projectID]; ok && count > 0 {
			targetConns = append(targetConns, conn)
		}
	}
	asm.mu.RUnlock()

	frame, err := json.Marshal(payload)
	if err != nil {
		return
	}

	asm.writeMu.Lock()
	for _, conn := range targetConns {
		if err := conn.Write(context.Background(), websocket.MessageText, frame); err != nil {
			// Client disconnected, will be cleaned up
		}
	}
	asm.writeMu.Unlock()

	// Buffer for reconnect sync
	asm.bufferRaw(frame)
}

// SendTaskEvent sends a task-level event to all clients subscribed to the task.
// The event includes task_id for client-side routing.
func (asm *AppStreamManager) SendTaskEvent(projectID, taskID string, event types.StreamEvent) {
	// Build flat JSON: {"type":"task_event","task_id":"...","type":"token","data":{...}}
	payload := map[string]interface{}{
		"type":    "task_event",
		"task_id": taskID,
		"event":   string(event.Type),
		"data":    event.Data,
	}
	asm.sendTaskPayload(projectID, taskID, payload)
}

// sendTaskPayload sends a payload to all clients subscribed to a task.
func (asm *AppStreamManager) sendTaskPayload(projectID, taskID string, payload interface{}) {
	asm.mu.RLock()
	targetConns := make([]*websocket.Conn, 0)
	key := projectID + ":" + taskID
	for conn, client := range asm.clients {
		if count, ok := client.taskSub[key]; ok && count > 0 {
			targetConns = append(targetConns, conn)
		}
	}
	asm.mu.RUnlock()

	frame, err := json.Marshal(payload)
	if err != nil {
		return
	}

	asm.writeMu.Lock()
	for _, conn := range targetConns {
		if err := conn.Write(context.Background(), websocket.MessageText, frame); err != nil {
			// Client disconnected
		}
	}
	asm.writeMu.Unlock()

	// Buffer for reconnect sync
	asm.bufferRaw(frame)
}

// BroadcastAll sends a raw message to ALL connected clients.
func (asm *AppStreamManager) BroadcastAll(msg []byte) {
	asm.mu.RLock()
	allConns := make([]*websocket.Conn, 0, len(asm.clients))
	for conn := range asm.clients {
		allConns = append(allConns, conn)
	}
	asm.mu.RUnlock()

	asm.writeMu.Lock()
	for _, conn := range allConns {
		if err := conn.Write(context.Background(), websocket.MessageText, msg); err != nil {
			// Client disconnected
		}
	}
	asm.writeMu.Unlock()
}

// BroadcastProjectCounts computes and broadcasts task counts for all projects.
func (asm *AppStreamManager) BroadcastProjectCounts() {
	if asm.store == nil {
		return
	}
	counts := asm.store.GetAllProjectCounts()
	msg, _ := json.Marshal(map[string]interface{}{
		"type": "project_counts",
		"data": counts,
	})
	asm.BroadcastAll(msg)
}

// -- BoardStreamManager compatibility methods --

// Broadcast sends a board_refresh event to all clients of a project,
// and a project_counts event to ALL connected clients across all projects.
func (asm *AppStreamManager) Broadcast(projectID string) {
	asm.sendBoardPayload(projectID, map[string]string{"type": "board_refresh"})
	asm.BroadcastProjectCounts()
}

// BroadcastCommitStatus sends a commit_status event to all clients of a project.
func (asm *AppStreamManager) BroadcastCommitStatus(projectID, taskID, status string) {
	asm.sendBoardPayload(projectID, map[string]string{
		"type":    "commit_status",
		"task_id": taskID,
		"status":  status,
	})
}

// BroadcastBranch sends a project_branch event to all clients of a project.
func (asm *AppStreamManager) BroadcastBranch(projectID, branch string) {
	asm.sendBoardPayload(projectID, map[string]interface{}{
		"type":   "project_branch",
		"branch": branch,
	})
}

// BroadcastDiff sends the full diff to all clients of a project.
func (asm *AppStreamManager) BroadcastDiff(projectID, taskID, diff string, shortStat string, untrackedN int, untracked []map[string]interface{}) {
	asm.sendBoardPayload(projectID, map[string]interface{}{
		"type":       "task_diff",
		"task_id":    taskID,
		"diff":       diff,
		"short_stat": shortStat,
		"untracked":  untracked,
	})
}

// BroadcastTaskUpdated sends a task_updated event with the full task object to all clients of a project,
// and a project_counts event to ALL connected clients across all projects.
func (asm *AppStreamManager) BroadcastTaskUpdated(projectID string, task interface{}) {
	asm.sendBoardPayload(projectID, map[string]interface{}{
		"type": "task_updated",
		"task": task,
	})
	asm.BroadcastProjectCounts()
}

// BroadcastTaskDeleted sends a task_deleted event with the task ID to all clients of a project,
// and a project_counts event to ALL connected clients across all projects.
func (asm *AppStreamManager) BroadcastTaskDeleted(projectID, taskID string) {
	asm.sendBoardPayload(projectID, map[string]interface{}{
		"type":    "task_deleted",
		"task_id": taskID,
	})
	asm.BroadcastProjectCounts()
}

// -- Ring buffer --

// bufferRaw adds a raw JSON frame to all clients' ring buffers.
func (asm *AppStreamManager) bufferRaw(frame []byte) {
	asm.mu.RLock()
	defer asm.mu.RUnlock()
	for _, client := range asm.clients {
		client.bufMu.Lock()
		if len(client.ringBuffer) >= client.bufSize {
			client.ringBuffer = client.ringBuffer[1:]
		}
		client.ringBuffer = append(client.ringBuffer, frame)
		client.bufMu.Unlock()
	}
}

// getBuffer returns the full ring buffer for a client.
func (asm *AppStreamManager) getBuffer(conn *websocket.Conn) [][]byte {
	asm.mu.RLock()
	client := asm.clients[conn]
	asm.mu.RUnlock()
	if client == nil {
		return nil
	}
	client.bufMu.Lock()
	defer client.bufMu.Unlock()
	return client.ringBuffer
}

// -- Diff poller --

// getDiffBaseline computes the effective diff baseline for a task.
func (asm *AppStreamManager) getDiffBaseline(task types.Task) string {
	if task.Branch == "" || asm.worktreeMgr == nil {
		return task.BaseRef
	}
	thread, _ := asm.store.GetThread(task.CurrentThreadID)
	forkBranch := ""
	if thread != nil {
		forkBranch = thread.GitBranch
	}
	baseRef, _ := asm.worktreeMgr.ResolveDiffBaseline(task.ID, task.BaseRef, task.Branch, forkBranch)
	return baseRef
}

// pollProjectDiffStats periodically checks git diff stats for tasks in in_progress/review
// and broadcasts updates when file/line counts change. Also monitors the project's current branch.
func (asm *AppStreamManager) pollProjectDiffStats(ctx context.Context, projectID string) {
	if asm.store == nil || asm.worktreeMgr == nil {
		return
	}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()

	snapshots := make(map[string]diffSnapshot)
	var lastBranch string

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			// Check for branch changes
			branch, _ := asm.worktreeMgr.GitRunInProjectOutput(projectID, "rev-parse", "--abbrev-ref", "HEAD")
			branch = strings.TrimSpace(branch)
			if branch != lastBranch {
				log.Printf("[board poll] project=%s branch changed: %s -> %s", projectID, lastBranch, branch)
				lastBranch = branch
				asm.BroadcastBranch(projectID, branch)
			}

			tasks, err := asm.store.GetTasksByProject(projectID)
			if err != nil {
				continue
			}
			for _, task := range tasks {
				if task.Status != "in_progress" && task.Status != "review" {
					continue
				}
				baseRef := asm.getDiffBaseline(task)
				raw, err := asm.worktreeMgr.GitDiffRaw(task.ID, baseRef)
				if err != nil {
					continue
				}
				prev, exists := snapshots[task.ID]
				if exists && prev.raw == raw {
					continue
				}
				diffResult, err := asm.worktreeMgr.GetTaskDiff(task.ID, baseRef, "")
				if err != nil {
					continue
				}
				shortStat := diffResult.ShortStat
				untrackedN := len(diffResult.Untracked)
				snapshots[task.ID] = diffSnapshot{raw: raw, shortStat: shortStat, untrackedN: untrackedN}

				if diffResult.Diff != "" || untrackedN > 0 {
					log.Printf("[board poll] project=%s task=%s diff changed: %s (untracked: %d files)",
						projectID, task.ID, shortStat, untrackedN)

					// Send task_diff to board subscribers
					untrackedMaps := make([]map[string]interface{}, len(diffResult.Untracked))
					for i, u := range diffResult.Untracked {
						untrackedMaps[i] = map[string]interface{}{"path": u.Path, "file_size": u.FileSize, "diff": u.Diff}
					}
					asm.BroadcastDiff(projectID, task.ID, diffResult.Diff, shortStat, untrackedN, untrackedMaps)

					// Send diff_changed to task subscribers
					if asm.hasTaskSubscribers(projectID, task.ID) {
						asm.SendTaskEvent(projectID, task.ID, types.StreamEvent{
							Type: types.WSTypeDiffChanged,
							Data: diffResult,
						})
					}
				} else {
					asm.BroadcastDiff(projectID, task.ID, "", "", 0, nil)
				}
			}
		}
	}
}

// startPoller starts the diff stats poller for a project if not already running.
func (asm *AppStreamManager) startPoller(projectID string) {
	asm.pollersMu.Lock()
	defer asm.pollersMu.Unlock()
	if _, exists := asm.pollers[projectID]; exists {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	asm.pollers[projectID] = cancel
	go asm.pollProjectDiffStats(ctx, projectID)
	log.Printf("[poller] project=%s started", projectID)
}

// stopPoller stops the diff stats poller for a project.
func (asm *AppStreamManager) stopPoller(projectID string) {
	asm.pollersMu.Lock()
	defer asm.pollersMu.Unlock()
	if cancel, exists := asm.pollers[projectID]; exists {
		cancel()
		delete(asm.pollers, projectID)
		log.Printf("[poller] project=%s stopped", projectID)
	}
}

// -- WebSocket handler --

// HandleStream is the HTTP handler for the single app-level WebSocket at /api/stream.
func (asm *AppStreamManager) HandleStream(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionContextTakeover,
	})
	if err != nil {
		log.Printf("[app stream] accept error: %v", err)
		return
	}

	client := &clientState{
		conn:       conn,
		projectSub: make(map[string]int),
		taskSub:    make(map[string]int),
		ringBuffer: make([][]byte, 0, DefaultBufferSize),
		bufSize:    DefaultBufferSize,
	}

	asm.mu.Lock()
	asm.clients[conn] = client
	asm.mu.Unlock()

	defer func() {
		asm.mu.Lock()
		delete(asm.clients, conn)
		asm.mu.Unlock()
		conn.Close(websocket.StatusNormalClosure, "closed")

		// Stop pollers for projects no one is subscribed to
		for projectID := range client.projectSub {
			if !asm.hasProjectSubscribers(projectID) {
				asm.stopPoller(projectID)
			}
		}
	}()

	log.Printf("[app stream] connection accepted (total: %d)", len(asm.clients))

	// Send initial project counts
	asm.BroadcastProjectCounts()

	// Ping/pong keepalive
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				asm.writeMu.Lock()
				err := conn.Ping(context.Background())
				asm.writeMu.Unlock()
				if err != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()

	// Send buffered events on reconnect
	if r.URL.Query().Get("sync") == "1" {
		go func() {
			buf := asm.getBuffer(conn)
			for _, frame := range buf {
				select {
				case <-done:
					return
				default:
				}
				if err := conn.Write(context.Background(), websocket.MessageText, frame); err != nil {
					return
				}
			}
		}()
	}

	// Read loop — handle client messages
	for {
		_, msg, err := conn.Read(context.Background())
		if err != nil {
			break
		}

		var input AppStreamMessage
		if err := json.Unmarshal(msg, &input); err != nil {
			continue
		}

		switch input.Type {
		case "subscribe_project":
			var sub AppStreamSubscription
			if err := json.Unmarshal(input.Data, &sub); err == nil && sub.ProjectID != "" {
				asm.subscribeProject(conn, sub.ProjectID)
				log.Printf("[app stream] subscribed to project=%s", sub.ProjectID)

				// Send initial data for the project
				asm.sendInitialProjectData(conn, sub.ProjectID)
			}

		case "unsubscribe_project":
			var sub AppStreamSubscription
			if err := json.Unmarshal(input.Data, &sub); err == nil && sub.ProjectID != "" {
				asm.unsubscribeProject(conn, sub.ProjectID)
				log.Printf("[app stream] unsubscribed from project=%s", sub.ProjectID)
			}

		case "subscribe_task":
			var sub AppStreamSubscription
			if err := json.Unmarshal(input.Data, &sub); err == nil && sub.ProjectID != "" && sub.TaskID != "" {
				asm.subscribeTask(conn, sub.ProjectID, sub.TaskID)
				log.Printf("[app stream] subscribed to task=%s in project=%s", sub.TaskID, sub.ProjectID)
			}

		case "unsubscribe_task":
			var sub AppStreamSubscription
			if err := json.Unmarshal(input.Data, &sub); err == nil && sub.ProjectID != "" && sub.TaskID != "" {
				asm.unsubscribeTask(conn, sub.ProjectID, sub.TaskID)
				log.Printf("[app stream] unsubscribed from task=%s in project=%s", sub.TaskID, sub.ProjectID)
			}

		case "user_message":
			var um AppStreamUserMessage
			if err := json.Unmarshal(input.Data, &um); err == nil && um.Content != "" {
				asm.handlerMu.RLock()
				handler := asm.userMsgHandler
				asm.handlerMu.RUnlock()
				if handler != nil {
					handler(um)
				}
			}
		}
	}
}

// sendInitialProjectData sends initial diffs and branch info for a project on subscription.
func (asm *AppStreamManager) sendInitialProjectData(conn *websocket.Conn, projectID string) {
	if asm.store == nil || asm.worktreeMgr == nil {
		return
	}

	tasks, err := asm.store.GetTasksByProject(projectID)
	if err != nil {
		return
	}
	for _, task := range tasks {
		baseRef := asm.getDiffBaseline(task)
		if diffResult, err := asm.worktreeMgr.GetTaskDiff(task.ID, baseRef, ""); err == nil && (diffResult.Diff != "" || len(diffResult.Untracked) > 0) {
			untrackedMaps := make([]map[string]interface{}, len(diffResult.Untracked))
			for i, u := range diffResult.Untracked {
				untrackedMaps[i] = map[string]interface{}{"path": u.Path, "file_size": u.FileSize, "diff": u.Diff}
			}
			msg, _ := json.Marshal(map[string]interface{}{
				"type":       "task_diff",
				"task_id":    task.ID,
				"diff":       diffResult.Diff,
				"short_stat": diffResult.ShortStat,
				"untracked":  untrackedMaps,
			})
			asm.writeMu.Lock()
			conn.Write(context.Background(), websocket.MessageText, msg)
			asm.writeMu.Unlock()
		}
	}

	branch, _ := asm.worktreeMgr.GitRunInProjectOutput(projectID, "rev-parse", "--abbrev-ref", "HEAD")
	branch = strings.TrimSpace(branch)
	if branch != "" {
		msg, _ := json.Marshal(map[string]interface{}{
			"type":   "project_branch",
			"branch": branch,
		})
		asm.writeMu.Lock()
		conn.Write(context.Background(), websocket.MessageText, msg)
		asm.writeMu.Unlock()
	}
}

// ResetForTest closes all connections and clears state. Used by e2e tests.
func (asm *AppStreamManager) ResetForTest() {
	asm.mu.Lock()
	defer asm.mu.Unlock()
	for conn := range asm.clients {
		conn.CloseNow()
	}
	asm.clients = make(map[*websocket.Conn]*clientState)
	asm.pollersMu.Lock()
	for _, cancel := range asm.pollers {
		cancel()
	}
	asm.pollers = make(map[string]context.CancelFunc)
	asm.pollersMu.Unlock()
}
