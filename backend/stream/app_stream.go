package stream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
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

// toolCallMarker is the JSON substring used to identify tool_call events in buffered frames.
var toolCallMarker = []byte(`"event":"tool_call"`)

// maxFrameBufferSize is the maximum size of a single frame to buffer.
// Frames larger than this (e.g., tool_result with large output, full diffs)
// are skipped to prevent ring buffers from consuming excessive memory.
const maxFrameBufferSize = 256 * 1024 // 256KB

// bytesContains is a simple bytes.Contains that avoids []byte→string allocation.
func bytesContains(haystack, needle []byte) bool {
	return bytes.Contains(haystack, needle)
}

const (
	// DefaultBufferSize is the default ring buffer size per connection.
	// Large enough to hold a full LLM streaming session (tokens + reasoning) without evicting tool_call events.
	DefaultBufferSize = 5000
	// MaxBufferSize is the maximum ring buffer size per connection.
	MaxBufferSize = 20000
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
	ProjectID   string `json:"project_id"`
	TaskID      string `json:"task_id"`
	Content     string `json:"content"`
	ModelName   string `json:"model_name,omitempty"`
	ServerURL   string `json:"server_url,omitempty"`
	Attachments string `json:"attachments,omitempty"`
}

// branchSnapshot holds the last-known branch for a project.
type branchSnapshot struct {
	branch string
}

// AppStreamManager manages a single WebSocket endpoint for all app events.
// Clients connect once at /api/stream and subscribe to projects/tasks.
type AppStreamManager struct {
	mu          sync.RWMutex
	clients     map[*websocket.Conn]*clientState
	writeMu     sync.Mutex // serializes all writes to prevent concurrent Read/Write races
	store       *store.Store
	worktreeMgr *git.WorktreeManager

	// Filesystem watcher for worktree changes
	watcher *WorktreeWatcher

	// Filesystem watcher for project directory changes (create/delete)
	projectWatcher *ProjectWatcher

	// Branch pollers — one per project, for detecting branch switches
	branchPollersMu sync.Mutex
	branchPollers   map[string]context.CancelFunc

	// User message handler — set by executor for forwarding WS user messages
	userMsgHandler func(AppStreamUserMessage)
	handlerMu      sync.RWMutex

	// Lazy start handler — called when a user message arrives for a task with no active executor
	lazyStartHandler LazyStartHandler
	lazyMu           sync.RWMutex

	// Per-task ring buffer for reconnect sync.
	// When a client subscribes to a task, it gets the buffered events so it can
	// catch up on tokens/tool_calls that were streamed while it was disconnected.
	taskBufMu   sync.Mutex
	taskBuffers map[string][][]byte // task key ("projectID:taskID") -> ring buffer
	taskBufSize int
}

// NewAppStreamManager creates a new AppStreamManager.
func NewAppStreamManager(store *store.Store, worktreeMgr *git.WorktreeManager) *AppStreamManager {
	asm := &AppStreamManager{
		clients:       make(map[*websocket.Conn]*clientState),
		store:         store,
		worktreeMgr:   worktreeMgr,
		branchPollers: make(map[string]context.CancelFunc),
		taskBuffers:   make(map[string][][]byte),
		taskBufSize:   DefaultBufferSize,
	}
	// Initialize the filesystem watcher for task worktrees
	var err error
	asm.watcher, err = NewWorktreeWatcher(worktreeMgr, asm.BroadcastDiffStats)
	if err == nil {
		go asm.watcher.StartEventLoop()
	}

	// Initialize the project directory watcher
	asm.projectWatcher, err = NewProjectWatcher(asm.BroadcastFsEvent)
	if err == nil {
		go asm.projectWatcher.StartEventLoop()
	}

	return asm
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

// WatchWorktree starts filesystem monitoring for a task's worktree.
func (asm *AppStreamManager) WatchWorktree(taskID, projectID, baseRef string) {
	if asm.watcher != nil {
		_ = asm.watcher.Watch(taskID, projectID, baseRef)
	}
}

// UnwatchWorktree stops filesystem monitoring for a task's worktree.
func (asm *AppStreamManager) UnwatchWorktree(taskID string) {
	if asm.watcher != nil {
		asm.watcher.Unwatch(taskID)
	}
}

// Close stops all watchers and releases resources.
func (asm *AppStreamManager) Close() {
	if asm.watcher != nil {
		asm.watcher.Close()
	}
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

	// Start branch poller and project watcher if this is the first subscriber to this project
	if wasEmpty {
		asm.startBranchPoller(projectID)
		// Watch project directory for file create/delete events
		project, err := asm.store.GetProject(projectID)
		if err == nil && project != nil && project.Path != "" {
			_ = asm.projectWatcher.Watch(projectID, project.Path)
		}
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

	// Stop branch poller and project watcher if no clients are subscribed to this project
	if !hasSubs {
		asm.stopBranchPoller(projectID)
		asm.projectWatcher.Unwatch(projectID)
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

	// Send per-task buffer so the client can catch up on missed events
	// (e.g., tokens streamed while the page was reloading).
	buf := asm.getTaskBuffer(key)
	if len(buf) > 0 {
		go func() {
			asm.writeMu.Lock()
			defer asm.writeMu.Unlock()
			for _, frame := range buf {
				if err := conn.Write(context.Background(), websocket.MessageText, frame); err != nil {
					return
				}
			}
		}()
	}
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
func (asm *AppStreamManager) sendBoardPayload(projectID string, payload any) {
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
	payload := map[string]any{
		"type":    "task_event",
		"task_id": taskID,
		"event":   string(event.Type),
		"data":    event.Data,
	}
	asm.sendTaskPayload(projectID, taskID, payload)

	// Clear per-task buffer when a streaming turn ends, so reconnecting clients
	// don't get stale tokens from a previous turn.
	if event.Type == types.WSTypeStatus {
		if state, ok := event.Data.(map[string]string)["state"]; ok {
			if state == "stopped" || state == "failed" || state == "completed" ||
				state == "awaiting_review" || state == "idle" || state == "cancelled" {
				asm.clearTaskBuffer(projectID + ":" + taskID)
			}
		}
	}
	if event.Type == types.WSTypeComplete {
		asm.clearTaskBuffer(projectID + ":" + taskID)
	}
}

// sendTaskPayload sends a payload to all clients subscribed to a task.
func (asm *AppStreamManager) sendTaskPayload(projectID, taskID string, payload any) {
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

	// Buffer for reconnect sync (per-client and per-task)
	asm.bufferRaw(frame)
	asm.bufferTask(key, frame)
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
	msg, _ := json.Marshal(map[string]any{
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
	asm.sendBoardPayload(projectID, map[string]any{
		"type":   "project_branch",
		"branch": branch,
	})
}

// BroadcastFsEvent sends a fs_event to all clients of a project.
// op is "create" or "delete", path is the affected file/directory path.
func (asm *AppStreamManager) BroadcastFsEvent(projectID, op, path string) {
	asm.sendBoardPayload(projectID, map[string]any{
		"type": "fs_event",
		"op":   op,
		"path": path,
	})
}

// BroadcastDiffStats sends compact diff stats to all clients of a project.
// Used by the card UI to show file counts and the commit button.
// The hash lets the frontend detect content changes without receiving the full diff.
func (asm *AppStreamManager) BroadcastDiffStats(projectID, taskID, diffHash, shortStat string, untrackedN, untrackedLines int) {
	asm.sendBoardPayload(projectID, map[string]any{
		"type":            "task_diff_stats",
		"task_id":         taskID,
		"hash":            diffHash,
		"short_stat":      shortStat,
		"untracked_n":     untrackedN,
		"untracked_lines": untrackedLines,
	})
}

// BroadcastDiff sends the full diff to all clients of a project.
func (asm *AppStreamManager) BroadcastDiff(projectID, taskID, diff string, shortStat string, untrackedN int, untracked []map[string]any) {
	asm.sendBoardPayload(projectID, map[string]any{
		"type":       "task_diff",
		"task_id":    taskID,
		"diff":       diff,
		"short_stat": shortStat,
		"untracked":  untracked,
	})
}

// BroadcastTaskUpdated sends a task_updated event with the full task object to all clients of a project,
// and a project_counts event to ALL connected clients across all projects.
func (asm *AppStreamManager) BroadcastTaskUpdated(projectID string, task any) {
	asm.sendBoardPayload(projectID, map[string]any{
		"type": "task_updated",
		"task": task,
	})
	asm.BroadcastProjectCounts()
}

// BroadcastTaskDeleted sends a task_deleted event with the task ID to all clients of a project,
// and a project_counts event to ALL connected clients across all projects.
func (asm *AppStreamManager) BroadcastTaskDeleted(projectID, taskID string) {
	asm.sendBoardPayload(projectID, map[string]any{
		"type":    "task_deleted",
		"task_id": taskID,
	})
	asm.BroadcastProjectCounts()
}

// -- Ring buffer --

// bufferRaw adds a raw JSON frame to all clients' ring buffers.
// Skips frames larger than maxFrameBufferSize to prevent memory exhaustion.
func (asm *AppStreamManager) bufferRaw(frame []byte) {
	if len(frame) > maxFrameBufferSize {
		return
	}
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

// bufferTask adds a raw JSON frame to the per-task ring buffer.
// For tool_call events, keeps only the last delta per tool_id (replaces earlier
// deltas) so the reconnecting client gets the final tool_call state without
// duplicates. Non-tool_call events are appended normally.
func (asm *AppStreamManager) bufferTask(taskKey string, frame []byte) {
	// Skip large frames to prevent memory exhaustion
	if len(frame) > maxFrameBufferSize {
		return
	}

	asm.taskBufMu.Lock()
	defer asm.taskBufMu.Unlock()
	buf, ok := asm.taskBuffers[taskKey]
	if !ok {
		buf = make([][]byte, 0, asm.taskBufSize)
	}

	// For tool_call events, replace any existing frame with the same tool_id
	// so only the last delta is kept (avoids duplicate tool calls on replay).
	if bytesContains(frame, toolCallMarker) {
		if toolID := extractToolID(frame); toolID != "" {
			buf = removeFrameWithToolID(buf, toolID)
		}
	}

	if len(buf) >= asm.taskBufSize {
		buf = buf[1:]
	}
	// Copy frame to avoid aliasing
	frameCopy := make([]byte, len(frame))
	copy(frameCopy, frame)
	buf = append(buf, frameCopy)
	asm.taskBuffers[taskKey] = buf
}

// extractToolID extracts the tool_id from a task_event frame containing a tool_call event.
func extractToolID(frame []byte) string {
	var parsed struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(frame, &parsed); err != nil {
		return ""
	}
	var data struct {
		ToolID string `json:"tool_id"`
	}
	if err := json.Unmarshal(parsed.Data, &data); err != nil {
		return ""
	}
	return data.ToolID
}

// removeFrameWithToolID removes any frame from buf that contains the given tool_id.
func removeFrameWithToolID(buf [][]byte, toolID string) [][]byte {
	filtered := make([][]byte, 0, len(buf))
	for _, f := range buf {
		if !frameHasToolID(f, toolID) {
			filtered = append(filtered, f)
		}
	}
	return filtered
}

// frameHasToolID checks if a task_event frame contains a tool_call with the given tool_id.
func frameHasToolID(frame []byte, toolID string) bool {
	if toolID == "" {
		return false
	}
	// Fast path: check if the tool_id appears in the raw JSON
	return bytesContains(frame, []byte(`"tool_id":"`+toolID+`"`))
}

// getTaskBuffer returns the per-task ring buffer without clearing it.
// The buffer is cleared only on complete/terminal status events (bufferTask).
// This allows multiple clients (or reconnections) to receive the buffer.
func (asm *AppStreamManager) getTaskBuffer(taskKey string) [][]byte {
	asm.taskBufMu.Lock()
	defer asm.taskBufMu.Unlock()
	buf := asm.taskBuffers[taskKey]
	// Copy so concurrent access is safe
	result := make([][]byte, len(buf))
	copy(result, buf)
	return result
}

// clearTaskBuffer removes the per-task ring buffer.
func (asm *AppStreamManager) clearTaskBuffer(taskKey string) {
	asm.taskBufMu.Lock()
	defer asm.taskBufMu.Unlock()
	delete(asm.taskBuffers, taskKey)
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

// pollBranch checks the project's current branch and broadcasts if it changed.
func (asm *AppStreamManager) pollBranch(ctx context.Context, projectID string) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()

	var lastBranch string

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			branch, _ := asm.worktreeMgr.GitRunInProjectOutput(projectID, "rev-parse", "--abbrev-ref", "HEAD")
			branch = strings.TrimSpace(branch)
			if branch != lastBranch {
				log.Printf("[branch poll] project=%s branch changed: %s -> %s", projectID, lastBranch, branch)
				lastBranch = branch
				asm.BroadcastBranch(projectID, branch)
			}
		}
	}
}

// startBranchPoller starts the branch poller for a project if not already running.
func (asm *AppStreamManager) startBranchPoller(projectID string) {
	asm.branchPollersMu.Lock()
	defer asm.branchPollersMu.Unlock()
	if _, exists := asm.branchPollers[projectID]; exists {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	asm.branchPollers[projectID] = cancel
	go asm.pollBranch(ctx, projectID)
	log.Printf("[branch poller] project=%s started", projectID)
}

// stopBranchPoller stops the branch poller for a project.
func (asm *AppStreamManager) stopBranchPoller(projectID string) {
	asm.branchPollersMu.Lock()
	defer asm.branchPollersMu.Unlock()
	if cancel, exists := asm.branchPollers[projectID]; exists {
		cancel()
		delete(asm.branchPollers, projectID)
		log.Printf("[branch poller] project=%s stopped", projectID)
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

		// Stop branch pollers for projects no one is subscribed to
		for projectID := range client.projectSub {
			if !asm.hasProjectSubscribers(projectID) {
				asm.stopBranchPoller(projectID)
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
		// Watch worktrees for tasks in active states
		if task.Status == "in_progress" || task.Status == "review" {
			asm.WatchWorktree(task.ID, projectID, task.BaseRef)
		}
		if diffResult, err := asm.worktreeMgr.GetTaskDiff(task.ID, baseRef, ""); err == nil && (diffResult.Diff != "" || len(diffResult.Untracked) > 0) {
			// Send compact stats for card UI
			untrackedLines := 0
			for _, u := range diffResult.Untracked {
				untrackedLines += len(u.Lines)
			}
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(diffResult.Diff)))
			statsMsg, _ := json.Marshal(map[string]any{
				"type":            "task_diff_stats",
				"task_id":         task.ID,
				"hash":            hash,
				"short_stat":      diffResult.ShortStat,
				"untracked_n":     len(diffResult.Untracked),
				"untracked_lines": untrackedLines,
			})
			asm.writeMu.Lock()
			conn.Write(context.Background(), websocket.MessageText, statsMsg)
			asm.writeMu.Unlock()

			// Send full diff for detail view
			untrackedMaps := make([]map[string]any, len(diffResult.Untracked))
			for i, u := range diffResult.Untracked {
				untrackedMaps[i] = map[string]any{"path": u.Path, "lines": u.Lines, "file_size": u.FileSize, "truncated": u.Truncated, "orig_added": u.OrigAdded}
			}
			msg, _ := json.Marshal(map[string]any{
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
		msg, _ := json.Marshal(map[string]any{
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
	asm.branchPollersMu.Lock()
	for _, cancel := range asm.branchPollers {
		cancel()
	}
	asm.branchPollers = make(map[string]context.CancelFunc)
	asm.branchPollersMu.Unlock()
}
