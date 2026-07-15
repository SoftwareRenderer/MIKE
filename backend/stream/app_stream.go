package stream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
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

// isGitPath returns true if the path is inside a .git directory.
// Matches ".git" as a path component (e.g. ".git/HEAD", "sub/.git/config").
func isGitPath(path string) bool {
	if path == ".git" {
		return true
	}
	// Check if any path component is ".git"
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".git" {
			return true
		}
	}
	return false
}

// isInternalFile returns true for paths that should be suppressed from
// WebSocket broadcasts: .git directories and flight recorder trace files.
func isInternalFile(path string) bool {
	if isGitPath(path) {
		return true
	}
	base := filepath.Base(path)
	if base == "kanban.trace" || base == "kanban.trace.old" {
		return true
	}
	return false
}

// filterInternalPathsFromHashes removes entries with internal paths from a hash map.
func filterInternalPathsFromHashes(hashes map[string]string) map[string]string {
	if len(hashes) == 0 {
		return nil
	}
	filtered := make(map[string]string, len(hashes))
	for path, hash := range hashes {
		if !isInternalFile(path) {
			filtered[path] = hash
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

// filterInternalPathsFromList removes internal paths from a string slice.
func filterInternalPathsFromList(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	filtered := make([]string, 0, len(paths))
	for _, p := range paths {
		if !isInternalFile(p) {
			filtered = append(filtered, p)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

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
	Content         string
	ModelName       string
	ServerURL       string
	ReasoningEffort string
	Attachments     string // JSON array of {mimeType, data} for images
	Regenerate      bool   // true when re-sending a message to trigger regeneration
	ContextChanged  bool   // true when conversation context changed (e.g., message deleted); resets loop detector
}

// LazyStartHandler is called when a user message arrives for a task with no active executor.
type LazyStartHandler func(projectID, taskID, content, modelName, serverURL, reasoningEffort string, regenerate bool)

// clientState holds the subscription state for a single WebSocket connection.
type clientState struct {
	conn       *websocket.Conn
	projectSub map[string]int // projectID -> subscriber count (for multi-tab support)
	taskSub    map[string]int // "projectID:taskID" -> subscriber count
	writeMu    sync.Mutex     // serializes writes for this connection
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



// branchSnapshot holds the last-known branch for a project.
type branchSnapshot struct {
	branch string
}

// AppStreamManager manages a single WebSocket endpoint for all app events.
// Clients connect once at /api/stream and subscribe to projects/tasks.
type AppStreamManager struct {
	mu          sync.RWMutex
	clients     map[*websocket.Conn]*clientState
	store       *store.Store
	worktreeMgr *git.WorktreeManager

	// Filesystem watcher for worktree changes
	watcher *WorktreeWatcher

	// Filesystem watcher for project directory changes (create/delete)
	projectWatcher *ProjectWatcher

	// Branch pollers — one per project, for detecting branch switches
	branchPollersMu sync.Mutex
	branchPollers   map[string]context.CancelFunc

	// Lazy start handler — called when a user message arrives for a task with no active executor
	lazyStartHandler LazyStartHandler
	lazyMu           sync.RWMutex

	// Per-task ring buffer for reconnect sync.
	// When a client subscribes to a task, it gets the buffered events so it can
	// catch up on tokens/tool_calls that were streamed while it was disconnected.
	taskBufMu   sync.Mutex
	taskBuffers map[string][]taskFrame // task key -> ring buffer
	taskBufSize int
}

// taskFrame is a single entry in the per-task ring buffer.
type taskFrame struct {
	data   []byte
	toolID string
}

// NewAppStreamManager creates a new AppStreamManager.
func NewAppStreamManager(store *store.Store, worktreeMgr *git.WorktreeManager) *AppStreamManager {
	asm := &AppStreamManager{
		clients:       make(map[*websocket.Conn]*clientState),
		store:         store,
		worktreeMgr:   worktreeMgr,
		branchPollers: make(map[string]context.CancelFunc),
		taskBuffers:   make(map[string][]taskFrame),
		taskBufSize:   DefaultBufferSize,
	}
	// Initialize the filesystem watcher for task worktrees
	// Cache update callback is set later via SetDiffCacheUpdate (after GitHandler is created)
	var err error
	asm.watcher, err = NewWorktreeWatcher(worktreeMgr, asm.BroadcastDiffStats, nil)
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
			client.writeMu.Lock()
			defer client.writeMu.Unlock()
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
	targets := make([]*clientState, 0)
	for _, client := range asm.clients {
		if count, ok := client.projectSub[projectID]; ok && count > 0 {
			targets = append(targets, client)
		}
	}
	asm.mu.RUnlock()

	frame, err := json.Marshal(payload)
	if err != nil {
		return
	}

	for _, client := range targets {
		client.writeMu.Lock()
		if err := client.conn.Write(context.Background(), websocket.MessageText, frame); err != nil {
			// Client disconnected, will be cleaned up
		}
		client.writeMu.Unlock()
	}

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
	targets := make([]*clientState, 0)
	key := projectID + ":" + taskID
	for _, client := range asm.clients {
		if count, ok := client.taskSub[key]; ok && count > 0 {
			targets = append(targets, client)
		}
	}
	asm.mu.RUnlock()

	frame, err := json.Marshal(payload)
	if err != nil {
		return
	}

	for _, client := range targets {
		client.writeMu.Lock()
		if err := client.conn.Write(context.Background(), websocket.MessageText, frame); err != nil {
			// Client disconnected
		}
		client.writeMu.Unlock()
	}

	// Buffer for reconnect sync (per-client and per-task)
	asm.bufferRaw(frame)
	asm.bufferTask(key, frame)
}

// BroadcastAll sends a raw message to ALL connected clients.
func (asm *AppStreamManager) BroadcastAll(msg []byte) {
	asm.mu.RLock()
	allClients := make([]*clientState, 0, len(asm.clients))
	for _, client := range asm.clients {
		allClients = append(allClients, client)
	}
	asm.mu.RUnlock()

	for _, client := range allClients {
		client.writeMu.Lock()
		if err := client.conn.Write(context.Background(), websocket.MessageText, msg); err != nil {
			// Client disconnected
		}
		client.writeMu.Unlock()
	}
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
// Events for internal files (.git, flight recorder) are suppressed.
func (asm *AppStreamManager) BroadcastFsEvent(projectID, op, path string) {
	if isInternalFile(path) {
		return
	}
	asm.sendBoardPayload(projectID, map[string]any{
		"type": "fs_event",
		"op":   op,
		"path": path,
	})
}

// SetDiffCacheUpdate sets a callback to update the GitHandler's diff cache
// when the worktree watcher computes a new diff. Must be called before diff
// events are processed.
func (asm *AppStreamManager) SetDiffCacheUpdate(fn func(taskID string, taskDiff *git.TaskDiff)) {
	if asm.watcher != nil {
		asm.watcher.updateCache = fn
	}
}

// BroadcastDiffStats sends compact diff stats to all clients of a project.
func (asm *AppStreamManager) BroadcastDiffStats(projectID, taskID, diffHash, shortStat string, filesN, untrackedN, untrackedLines int, changedHashes map[string]string, removedFiles []string, stagedPaths []string) {
	asm.sendBoardPayload(projectID, map[string]any{
		"type":            "task_diff_stats",
		"task_id":         taskID,
		"hash":            diffHash,
		"short_stat":      shortStat,
		"files_n":         filesN,
		"untracked_n":     untrackedN,
		"untracked_lines": untrackedLines,
		"file_hashes":     filterInternalPathsFromHashes(changedHashes),
		"removed_files":   filterInternalPathsFromList(removedFiles),
		"staged_paths":    filterInternalPathsFromList(stagedPaths),
	})
}

// ComputeDiffHash computes a SHA-256 hash of the tracked diff + untracked files + staged state.
// Pass empty stagedHash when index state is irrelevant (e.g., worktree file watcher).
func ComputeDiffHash(diff string, untracked []git.UntrackedFile, stagedHash string) string {
	var hashInput strings.Builder
	hashInput.WriteString(diff)
	for _, u := range untracked {
		hashInput.WriteString(u.Path + strings.Join(u.Lines, "\n"))
	}
	if stagedHash != "" {
		hashInput.WriteString("\x00staged:" + stagedHash)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(hashInput.String())))
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

// BroadcastProjectPorts sends a project_ports event to all clients of a project.
func (asm *AppStreamManager) BroadcastProjectPorts(projectID string, ports []types.AssignedPort) {
	asm.sendBoardPayload(projectID, map[string]any{
		"type":  "project_ports",
		"ports": ports,
	})
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

	// Pre-extract tool_id for O(1) dedup
	var toolID string
	if bytesContains(frame, toolCallMarker) {
		toolID = extractToolID(frame)
	}

	asm.taskBufMu.Lock()
	defer asm.taskBufMu.Unlock()
	buf, ok := asm.taskBuffers[taskKey]
	if !ok {
		buf = make([]taskFrame, 0, asm.taskBufSize)
	}

	// For tool_call events, replace any existing frame with the same tool_id
	// so only the last delta is kept (avoids duplicate tool calls on replay).
	if toolID != "" {
		buf = removeFrameWithToolID(buf, toolID)
	}

	if len(buf) >= asm.taskBufSize {
		buf = buf[1:]
	}
	// Copy frame to avoid aliasing
	frameCopy := make([]byte, len(frame))
	copy(frameCopy, frame)
	buf = append(buf, taskFrame{data: frameCopy, toolID: toolID})
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

// removeFrameWithToolID removes any frame from buf that has the given tool_id.
// Uses pre-extracted toolID for O(1) comparison.
func removeFrameWithToolID(buf []taskFrame, toolID string) []taskFrame {
	filtered := make([]taskFrame, 0, len(buf))
	for _, f := range buf {
		if f.toolID != toolID {
			filtered = append(filtered, f)
		}
	}
	return filtered
}

// getTaskBuffer returns the per-task ring buffer without clearing it.
// The buffer is cleared only on complete/terminal status events (bufferTask).
// This allows multiple clients (or reconnections) to receive the buffer.
func (asm *AppStreamManager) getTaskBuffer(taskKey string) [][]byte {
	asm.taskBufMu.Lock()
	defer asm.taskBufMu.Unlock()
	buf := asm.taskBuffers[taskKey]
	// Extract raw data from frames
	result := make([][]byte, len(buf))
	for i, f := range buf {
		result[i] = f.data
	}
	return result
}

// clearTaskBuffer removes the per-task ring buffer.
func (asm *AppStreamManager) clearTaskBuffer(taskKey string) {
	asm.taskBufMu.Lock()
	defer asm.taskBufMu.Unlock()
	delete(asm.taskBuffers, taskKey)
}

// ClearTaskBuffer removes the per-task ring buffer.
func (asm *AppStreamManager) ClearTaskBuffer(projectID, taskID string) {
	asm.clearTaskBuffer(projectID + ":" + taskID)
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
	// If a ping fails, cancel the read context to unblock Read() and close the connection.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		ticker := time.NewTicker(27 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// Per-ping timeout so Ping() itself can't hang forever
				pingCtx, pingCancel := context.WithTimeout(ctx, 10*time.Second)
				client.writeMu.Lock()
				err := conn.Ping(pingCtx)
				if err == nil {
					// Also send a data frame so the browser's onmessage fires.
					// Control Ping/Pong frames are transparent to JS and don't reset
					// the client-side stale-detection timer.
					_ = conn.Write(pingCtx, websocket.MessageText, []byte(`{"type":"ping"}`))
				}
				client.writeMu.Unlock()
				pingCancel()
				if err != nil {
					cancel() // unblock Read() to tear down the connection
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Send buffered events on reconnect
	if r.URL.Query().Get("sync") == "1" {
		go func() {
			buf := asm.getBuffer(conn)
			client.writeMu.Lock()
			for _, frame := range buf {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if err := conn.Write(ctx, websocket.MessageText, frame); err != nil {
					return
				}
			}
			client.writeMu.Unlock()
		}()
	}

	// Read loop — handle client messages
	for {
		_, msg, err := conn.Read(ctx)
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

				// Send initial data for the project (background to not block the read loop)
				go asm.sendInitialProjectData(ctx, client, conn, sub.ProjectID)
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

		}
	}
}

// sendInitialProjectData sends initial diffs and branch info for a project on subscription.
// Runs in a background goroutine; ctx aborts on connection close.
func (asm *AppStreamManager) sendInitialProjectData(ctx context.Context, client *clientState, conn *websocket.Conn, projectID string) {
	if asm.store == nil || asm.worktreeMgr == nil {
		return
	}

	tasks, err := asm.store.GetTasksByProject(projectID)
	if err != nil {
		return
	}
	for _, task := range tasks {
		select {
		case <-ctx.Done():
			return
		default:
		}
		baseRef := asm.getDiffBaseline(task)
		// Watch worktrees for tasks in active states
		if task.Status == "in_progress" || task.Status == "review" {
			asm.WatchWorktree(task.ID, projectID, task.BaseRef)
		}
		if diffResult, err := asm.worktreeMgr.GetTaskDiff(task.ID, baseRef, ""); err == nil && (diffResult.Diff != "" || len(diffResult.Untracked) > 0) {
			hash := ComputeDiffHash(diffResult.Diff, diffResult.Untracked, diffResult.StagedHash)
			// Send compact stats for card UI
			untrackedLines := 0
			for _, u := range diffResult.Untracked {
				untrackedLines += len(u.Lines)
			}
			filesN := len(diffResult.Tracked) + len(diffResult.Untracked)
			statsMsg, _ := json.Marshal(map[string]any{
				"type":            "task_diff_stats",
				"task_id":         task.ID,
				"hash":            hash,
				"short_stat":      diffResult.ShortStat,
				"files_n":         filesN,
				"untracked_n":     len(diffResult.Untracked),
				"untracked_lines": untrackedLines,
				"file_hashes":     filterInternalPathsFromHashes(diffResult.GetPerFileHashes()),
				"staged_paths":    filterInternalPathsFromList(diffResult.StagedPaths),
			})
			client.writeMu.Lock()
			conn.Write(ctx, websocket.MessageText, statsMsg)
			client.writeMu.Unlock()

		// Send manifest for detail view (per-file metadata, no diff content)
		stagedSet := make(map[string]struct{}, len(diffResult.StagedPaths))
		for _, p := range diffResult.StagedPaths {
			stagedSet[p] = struct{}{}
		}
		files := make(map[string]map[string]any)
		for path, f := range diffResult.Tracked {
			if isInternalFile(path) {
				continue
			}
			_, isStaged := stagedSet[path]
			entry := map[string]any{
				"hash":        git.ComputeFileHash(f),
				"type":        "tracked",
				"added":       f.Added,
				"removed":     f.Removed,
				"truncated":   f.Truncated,
				"origAdded":   f.OrigAdded,
				"origRemoved": f.OrigRemoved,
				"staged":      isStaged,
			}
			if f.FileSize != nil {
				entry["file_size"] = *f.FileSize
			}
			if f.IsBinary {
				entry["is_binary"] = true
			}
			files[path] = entry
		}
		for _, u := range diffResult.Untracked {
			if isInternalFile(u.Path) {
				continue
			}
			// Skip untracked files already in the tracked section
			if _, exists := files[u.Path]; exists {
				continue
			}
			entry := map[string]any{
				"hash":       git.ComputeUntrackedHash(u),
				"type":       "untracked",
				"line_count": len(u.Lines),
				"file_size":  u.FileSize,
				"added":      len(u.Lines),
			}
			if u.IsBinary {
				entry["is_binary"] = true
			}
			files[u.Path] = entry
		}
		msg, _ := json.Marshal(map[string]any{
			"type":       "task_diff",
			"task_id":    task.ID,
			"hash":       hash,
			"files":      files,
			"short_stat": diffResult.ShortStat,
		})
		client.writeMu.Lock()
		conn.Write(ctx, websocket.MessageText, msg)
		client.writeMu.Unlock()
		}
	}

	branch, _ := asm.worktreeMgr.GitRunInProjectOutput(projectID, "rev-parse", "--abbrev-ref", "HEAD")
	branch = strings.TrimSpace(branch)
	if branch != "" {
		msg, _ := json.Marshal(map[string]any{
			"type":   "project_branch",
			"branch": branch,
		})
		client.writeMu.Lock()
		conn.Write(ctx, websocket.MessageText, msg)
		client.writeMu.Unlock()
	}
}


