// Package stream owns the WebSocket side of the app: the per-connection hub, per-task event
// channels, and the outbound frames (task events, diff stats, build progress) the browser
// renders. A turn's output goes here, never to an HTTP response.
package stream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"kanban/backend/fsutil"
	"kanban/backend/git"
	"kanban/backend/httputil"
	"kanban/backend/store"
	"kanban/backend/types"

	"github.com/coder/websocket"
	"kanban/pkg/logging"
)

var toolCallMarker = []byte(`"event":"tool_call"`)

func filterInternalPathsFromHashes(hashes map[string]string) map[string]string {
	if len(hashes) == 0 {
		return nil
	}
	filtered := make(map[string]string, len(hashes))
	for path, hash := range hashes {
		if !git.IsInternalFile(path) {
			filtered[path] = hash
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func filterInternalPathsFromList(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	filtered := make([]string, 0, len(paths))
	for _, p := range paths {
		if !git.IsInternalFile(p) {
			filtered = append(filtered, p)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

const maxFrameBufferSize = 256 * 1024 // 256KB

const (
	DefaultBufferSize = 5000
)

// UserMessage carries a user message with optional model override.
type UserMessage struct {
	Content         string
	ModelName       string
	ServerURL       string
	ReasoningEffort string
	Attachments     string
	Regenerate      bool
	ContextChanged  bool
}

// LazyStartHandler is called when a user message arrives for a task with no active executor.
type LazyStartHandler func(projectID, taskID, content, modelName, serverURL, reasoningEffort string, regenerate bool)

type clientState struct {
	conn       *websocket.Conn
	projectSub map[string]int
	taskSub    map[string]int
	writeMu    sync.Mutex
	bufMu      sync.Mutex
	ringBuffer [][]byte
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
	// KnownStats declares, per done task, the task updated_at of the diff
	// stats the client already has cached. When the value matches the
	// server-side task.UpdatedAt, the initial task_diff_stats frame for that
	// task is skipped (the client renders from its own cache — the done-task
	// diff is immutable, so identical updated_at means identical stats).
	KnownStats map[string]string `json:"known_stats,omitempty"`
}

// AppStreamManager manages the /api/stream WebSocket for all app events.
type AppStreamManager struct {
	mu          sync.RWMutex
	clients     map[*websocket.Conn]*clientState
	store       *store.Store
	worktreeMgr *git.WorktreeManager

	// Filesystem watcher for worktree changes
	watcher *WorktreeWatcher

	// Filesystem watcher for project directory changes (create/delete)
	projectWatcher *ProjectWatcher

	// Event-driven branch watcher — watches .git/HEAD for branch switches
	branchWatcher *BranchWatcher

	// Lazy start handler — called when a user message arrives for a task with no active executor
	lazyStartHandler LazyStartHandler
	lazyMu           sync.RWMutex

	// Per-task ring buffer for reconnect sync.
	// When a client subscribes to a task, it gets the buffered events so it can
	// catch up on tokens/tool_calls that were streamed while it was disconnected.
	taskBufMu   sync.Mutex
	taskBuffers map[string][]taskFrame // task key -> ring buffer
	taskBufSize int

	// Latest-known sandbox memory stats
	sandboxStatsProvider func(projectID, taskID string) *types.SandboxMemoryStats
}

type taskFrame struct {
	data   []byte
	toolID string
}

// NewAppStreamManager creates a new AppStreamManager with filesystem watchers.
func NewAppStreamManager(store *store.Store, worktreeMgr *git.WorktreeManager) *AppStreamManager {
	asm := &AppStreamManager{
		clients:     make(map[*websocket.Conn]*clientState),
		store:       store,
		worktreeMgr: worktreeMgr,
		taskBuffers: make(map[string][]taskFrame),
		taskBufSize: DefaultBufferSize,
	}
	// The diff cache callback is set later via SetDiffCacheUpdate, once GitHandler exists.
	var err error
	asm.watcher, err = NewWorktreeWatcher(worktreeMgr, asm.BroadcastDiffStats, nil, asm.BroadcastFsEvent)
	if err == nil {
		go asm.watcher.StartEventLoop()
	}

	asm.projectWatcher, err = NewProjectWatcher(asm.BroadcastFsEvent)
	if err == nil {
		go asm.projectWatcher.StartEventLoop()
	}

	asm.branchWatcher, err = NewBranchWatcher(asm.BroadcastBranch)
	if err == nil {
		go asm.branchWatcher.StartEventLoop()
	}

	return asm
}

// SetLazyStartHandler sets the callback for lazy-starting an executor.
func (asm *AppStreamManager) SetLazyStartHandler(h LazyStartHandler) {
	asm.lazyMu.Lock()
	defer asm.lazyMu.Unlock()
	asm.lazyStartHandler = h
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
	if asm.branchWatcher != nil {
		asm.branchWatcher.Close()
	}
}

// -- Subscription management --

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

	// Start branch watcher and project watcher if this is the first subscriber to this project
	if wasEmpty {
		// Watch project directory for branch switches and file create/delete events
		project, err := asm.store.GetProject(projectID)
		if err == nil && project != nil && project.Path != "" {
			if asm.branchWatcher != nil {
				_ = asm.branchWatcher.Watch(projectID, project.Path)
			}
			if asm.projectWatcher != nil {
				_ = asm.projectWatcher.Watch(projectID, project.Path)
			}
		}
	}
}

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

	// Stop branch watcher and project watcher if no clients are subscribed to this project
	if !hasSubs {
		if asm.branchWatcher != nil {
			asm.branchWatcher.Unwatch(projectID)
		}
		if asm.projectWatcher != nil {
			asm.projectWatcher.Unwatch(projectID)
		}
	}
}

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

	// Done tasks are never watched: their canonical diff (pinned
	// BaseCommit..BranchTip) is immutable and served from the persistent
	// snapshot, so a watcher could only burn git CPU (its broadcasts are
	// hash-deduped no-ops). If a done task is moved back to active work, the
	// executor start path re-registers its watcher.

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

	// Deliver the latest known sandbox memory stats after the buffer replay so
	// clients opening the task view see the most recent usage even when the
	// task is idle (the buffer above is cleared when execution completes).
	if frame := asm.latestSandboxStatsFrame(projectID, taskID); frame != nil {
		go func() {
			client.writeMu.Lock()
			defer client.writeMu.Unlock()
			_ = conn.Write(context.Background(), websocket.MessageText, frame)
		}()
	}
}

// latestSandboxStatsFrame returns the task_event frame carrying the
// provider's latest known sandbox memory stats for the task, or nil when no
// provider is registered or it reports no known stats.
func (asm *AppStreamManager) latestSandboxStatsFrame(projectID, taskID string) []byte {
	asm.mu.RLock()
	provider := asm.sandboxStatsProvider
	asm.mu.RUnlock()
	if provider == nil {
		return nil
	}
	stats := provider(projectID, taskID)
	if stats == nil {
		return nil
	}
	frame, err := json.Marshal(TaskEventPayload(taskID, types.StreamEvent{Type: types.WSTypeSandboxStats, Data: stats}))
	if err != nil {
		return nil
	}
	return frame
}

// SetSandboxStatsProvider registers the source of latest-known sandbox memory
// stats. The function is called on every subscribe_task; returning nil
// (unknown task) sends nothing.
func (asm *AppStreamManager) SetSandboxStatsProvider(fn func(projectID, taskID string) *types.SandboxMemoryStats) {
	asm.mu.Lock()
	asm.sandboxStatsProvider = fn
	asm.mu.Unlock()
}

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

func (asm *AppStreamManager) hasProjectSubscribers(projectID string) bool {
	asm.mu.RLock()
	defer asm.mu.RUnlock()
	return len(asm.subscribers(projectSubs, projectID)) > 0
}

// subField selects one of a client's subscription maps (project- or
// task-scoped), so the fan-out helpers below stay single-purpose.
type subField func(*clientState) map[string]int

var (
	projectSubs subField = func(c *clientState) map[string]int { return c.projectSub }
	taskSubs    subField = func(c *clientState) map[string]int { return c.taskSub }
)

// subscribers returns the connected clients holding an active (>0) count for
// key in the chosen subscription map. Callers must hold at least asm.mu.RLock().
func (asm *AppStreamManager) subscribers(field subField, key string) []*clientState {
	targets := make([]*clientState, 0, len(asm.clients))
	for _, client := range asm.clients {
		if count, ok := field(client)[key]; ok && count > 0 {
			targets = append(targets, client)
		}
	}
	return targets
}

// allClients snapshots the connected clients. Callers must hold at least
// asm.mu.RLock(); the snapshot lets per-client writes run lock-free.
func (asm *AppStreamManager) allClients() []*clientState {
	targets := make([]*clientState, 0, len(asm.clients))
	for _, client := range asm.clients {
		targets = append(targets, client)
	}
	return targets
}

// writeFrame delivers a marshalled frame to each target. Per-client writeMu
// serialises this with the keepalive pinger and the replay goroutines.
func (asm *AppStreamManager) writeFrame(targets []*clientState, frame []byte) {
	for _, client := range targets {
		client.writeMu.Lock()
		// A write error means the client disconnected; its read loop removes it
		// from asm.clients, so there is nothing to do here.
		_ = client.conn.Write(context.Background(), websocket.MessageText, frame)
		client.writeMu.Unlock()
	}
}

// -- Broadcast methods --

func (asm *AppStreamManager) sendBoardPayload(projectID string, payload any) {
	asm.mu.RLock()
	targets := asm.subscribers(projectSubs, projectID)
	asm.mu.RUnlock()

	frame, err := json.Marshal(payload)
	if err != nil {
		return
	}

	asm.writeFrame(targets, frame)

	// Buffer for reconnect sync
	asm.bufferRaw(frame)
}

// TaskEventPayload builds the flat task_event envelope shared by SendTaskEvent
// and on-subscribe replays (e.g. the latest sandbox stats).
func TaskEventPayload(taskID string, event types.StreamEvent) map[string]any {
	return map[string]any{
		"type":    "task_event",
		"task_id": taskID,
		"event":   string(event.Type),
		"data":    event.Data,
	}
}

// SendTaskEvent sends a task-level event to all clients subscribed to the task.
func (asm *AppStreamManager) SendTaskEvent(projectID, taskID string, event types.StreamEvent) {
	asm.sendTaskPayload(projectID, taskID, TaskEventPayload(taskID, event))

	// Clear per-task buffer when a streaming turn ends, so reconnecting clients
	// don't get stale tokens from a previous turn.
	if event.Type == types.WSTypeStatus {
		if state, ok := event.Data.(map[string]string)["state"]; ok {
			if state == "failed" || state == "completed" ||
				state == "idle" || state == "cancelled" {
				asm.clearTaskBuffer(projectID + ":" + taskID)
			}
		}
	}
	if event.Type == types.WSTypeComplete {
		asm.clearTaskBuffer(projectID + ":" + taskID)
	}
}

func (asm *AppStreamManager) sendTaskPayload(projectID, taskID string, payload any) {
	key := projectID + ":" + taskID
	asm.mu.RLock()
	targets := asm.subscribers(taskSubs, key)
	asm.mu.RUnlock()

	frame, err := json.Marshal(payload)
	if err != nil {
		return
	}

	asm.writeFrame(targets, frame)

	// Buffer for reconnect sync (per-client and per-task)
	asm.bufferRaw(frame)
	asm.bufferTask(key, frame)
}

// BroadcastAll sends a raw message to all connected clients.
func (asm *AppStreamManager) BroadcastAll(msg []byte) {
	asm.mu.RLock()
	targets := asm.allClients()
	asm.mu.RUnlock()
	asm.writeFrame(targets, msg)
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

// -- BoardStreamManager compatibility --

// Broadcast sends a board_refresh event to a project and project_counts to all clients.
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

// shouldSkipFsEvent reports whether an fs_event for the given path should not be
// broadcast: internal files (.git, flight recorder), the temporary file an atomic
// write renames into place, and paths ignored by git.
func shouldSkipFsEvent(repoRoot, path string) bool {
	if git.IsInternalFile(path) {
		return true
	}
	if fsutil.IsAtomicTemp(path) {
		return true
	}
	return git.IsGitIgnored(repoRoot, path)
}

// BroadcastFsEvent sends an fs_event to all clients of a project. Events for
// internal files (.git, flight recorder) or paths ignored by .gitignore are
// dropped — they should not trigger explorer refreshes.
func (asm *AppStreamManager) BroadcastFsEvent(projectID, repoRoot, op, path string) {
	if shouldSkipFsEvent(repoRoot, path) {
		return
	}
	asm.sendBoardPayload(projectID, map[string]any{
		"type": "fs_event",
		"op":   op,
		"path": path,
	})
}

// SetDiffCacheUpdate sets a callback to update the GitHandler's diff cache.
func (asm *AppStreamManager) SetDiffCacheUpdate(fn func(taskID string, taskDiff *git.TaskDiff)) {
	if asm.watcher != nil {
		asm.watcher.updateCache = fn
	}
}

// BroadcastDiffStats sends compact diff stats to all clients of a project.
func (asm *AppStreamManager) BroadcastDiffStats(event DiffStatsEvent) {
	asm.sendBoardPayload(event.ProjectID, map[string]any{
		"type":                  "task_diff_stats",
		"task_id":               event.TaskID,
		"hash":                  event.DiffHash,
		"short_stat":            event.ShortStat,
		"files_n":               event.FilesN,
		"untracked_n":           event.UntrackedN,
		"untracked_lines":       event.UntrackedLines,
		"tracked_add":           event.TrackedAdd,
		"tracked_del":           event.TrackedDel,
		"file_hashes":           filterInternalPathsFromHashes(event.ChangedHashes),
		"removed_files":         filterInternalPathsFromList(event.RemovedFiles),
		"staged_paths":          filterInternalPathsFromList(event.StagedPaths),
		"has_committed_changes": event.HasCommittedChanges,
		"already_merged":        event.AlreadyMerged,
	})
}

// ComputeDiffHash computes a SHA-256 hash of the diff, committed diff, untracked files, and staged state.
func ComputeDiffHash(diff string, committedDiff string, untracked []git.UntrackedFile, stagedHash string) string {
	var hashInput strings.Builder
	hashInput.WriteString(diff)
	hashInput.WriteString("\x00committed:" + committedDiff)
	for _, u := range untracked {
		hashInput.WriteString(u.Path + strings.Join(u.Lines, "\n"))
	}
	if stagedHash != "" {
		hashInput.WriteString("\x00staged:" + stagedHash)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(hashInput.String())))
}

// BroadcastTaskUpdated sends a task_updated event and project_counts to all clients.
func (asm *AppStreamManager) BroadcastTaskUpdated(projectID string, task any) {
	asm.sendBoardPayload(projectID, map[string]any{
		"type": "task_updated",
		"task": task,
	})
	asm.BroadcastProjectCounts()
}

// BroadcastTaskDeleted sends a task_deleted event and project_counts to all clients.
func (asm *AppStreamManager) BroadcastTaskDeleted(projectID, taskID string) {
	// Drop the task's replay buffer: a deleted task's frames (tokens, stats,
	// …) must never be replayed to a late subscriber. The buffer is normally
	// cleared by terminal status/complete events, but deletion bypasses the
	// executor lifecycle.
	asm.clearTaskBuffer(projectID + ":" + taskID)
	asm.sendBoardPayload(projectID, map[string]any{
		"type":    "task_deleted",
		"task_id": taskID,
	})
	asm.BroadcastProjectCounts()
}

// SendProjectEvent sends one typed event to every client subscribed to the project. For work that
// belongs to the project rather than to a task — building its sandbox image while the file explorer
// waits, where there is no task chat to carry the progress.
func (asm *AppStreamManager) SendProjectEvent(projectID string, event types.StreamEvent) {
	asm.sendBoardPayload(projectID, map[string]any{
		"type": string(event.Type),
		"data": event.Data,
	})
}

// BroadcastProjectPorts sends a project_ports event to all clients of a project.
func (asm *AppStreamManager) BroadcastProjectPorts(projectID string, ports []types.AssignedPort) {
	asm.sendBoardPayload(projectID, map[string]any{
		"type":  "project_ports",
		"ports": ports,
	})
}

// BroadcastExtensionChange sends an extension_change event to all connected clients.
// This triggers the frontend to reload extensions and refresh UI injection slots.
func (asm *AppStreamManager) BroadcastExtensionChange() {
	msg, _ := json.Marshal(map[string]any{
		"type": string(types.WSTypeExtensionChange),
	})
	asm.BroadcastAll(msg)
}

// -- Ring buffer --

func (asm *AppStreamManager) bufferRaw(frame []byte) {
	if len(frame) > maxFrameBufferSize {
		return
	}
	// Collect clients under global lock, then release before per-client writes.
	asm.mu.RLock()
	targets := asm.allClients()
	asm.mu.RUnlock()

	for _, client := range targets {
		client.bufMu.Lock()
		if len(client.ringBuffer) >= client.bufSize {
			client.ringBuffer = client.ringBuffer[1:]
		}
		client.ringBuffer = append(client.ringBuffer, frame)
		client.bufMu.Unlock()
	}
}

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

func (asm *AppStreamManager) bufferTask(taskKey string, frame []byte) {
	// Skip large frames to prevent memory exhaustion
	if len(frame) > maxFrameBufferSize {
		return
	}

	// Pre-extract tool_id for O(1) dedup
	var toolID string
	if bytes.Contains(frame, toolCallMarker) {
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

func removeFrameWithToolID(buf []taskFrame, toolID string) []taskFrame {
	filtered := make([]taskFrame, 0, len(buf))
	for _, f := range buf {
		if f.toolID != toolID {
			filtered = append(filtered, f)
		}
	}
	return filtered
}

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

func (asm *AppStreamManager) clearTaskBuffer(taskKey string) {
	asm.taskBufMu.Lock()
	defer asm.taskBufMu.Unlock()
	delete(asm.taskBuffers, taskKey)
}

// ClearTaskBuffer removes the per-task ring buffer.
func (asm *AppStreamManager) ClearTaskBuffer(projectID, taskID string) {
	asm.clearTaskBuffer(projectID + ":" + taskID)
}

// resolveTaskDiffBaseline resolves a task's diff baseline the way every path
// does: pinned BaseCommit/BaseRef (via ResolveTaskBaseline), divergence
// fallback for diverged forks, fork-thread merge-base resolution.
func resolveTaskDiffBaseline(st *store.Store, wm *git.WorktreeManager, projectID string, task *types.Task) (string, error) {
	thread, _ := st.GetThread(task.CurrentThreadID)
	forkBranch := ""
	if thread != nil {
		forkBranch = thread.GitBranch
	}
	projectPath, originBranch := "", ""
	if project, err := st.GetProject(projectID); err == nil && project != nil {
		projectPath, originBranch = project.Path, project.OriginBranch
	}
	return wm.ResolveDiffBaseline(task.ID, wm.ResolveTaskBaseline(projectPath, originBranch, task), task.Branch, forkBranch)
}

func (asm *AppStreamManager) getDiffBaseline(task types.Task) string {
	if task.Branch == "" || asm.worktreeMgr == nil {
		return task.BaseRef
	}
	baseRef, _ := resolveTaskDiffBaseline(asm.store, asm.worktreeMgr, task.ProjectID, &task)
	return baseRef
}

// computeAlreadyMerged reports whether the task's branch commits are already
// merged into the target branch — a single git merge-base call in the project
// repo, no worktree required.
func (asm *AppStreamManager) computeAlreadyMerged(projectID string, task types.Task) bool {
	targetBranch, ok := CheckAlreadyMerged(task)
	if !ok {
		return false
	}
	project, err := asm.store.GetProject(projectID)
	if err != nil || project.Path == "" {
		return false
	}
	return asm.worktreeMgr.IsMergedInto(project.Path, task.Branch, targetBranch)
}

// sendDoneTaskInitialDiff sends a done task's task_diff_stats frame from the
// persistent snapshot — no per-connection `git diff` runs. When the client
// declared (via known_stats) that it already has stats for this exact task
// state, the frame is skipped entirely.
func (asm *AppStreamManager) sendDoneTaskInitialDiff(ctx context.Context, client *clientState, conn *websocket.Conn, projectID string, task types.Task, sub AppStreamSubscription) {
	d, err := MaterializeDoneTaskDiff(asm.store, asm.worktreeMgr, projectID, &task)
	if err != nil || d == nil || !d.HasChanges {
		return
	}
	// The client already has stats for this exact task state (updated_at
	// unchanged) — identical refs, identical immutable diff.
	if known, ok := sub.KnownStats[task.ID]; ok && known != "" && known == task.UpdatedAt.Format(time.RFC3339) {
		return
	}
	var statsFields map[string]any
	if err := json.Unmarshal([]byte(d.StatsJSON), &statsFields); err != nil {
		return
	}
	// already_merged is deliberately not snapshotted: it can flip (target
	// branch advances) without the diff changing, so it's recomputed fresh.
	statsFields["already_merged"] = asm.computeAlreadyMerged(projectID, task)
	frame := map[string]any{"type": "task_diff_stats", "task_id": task.ID}
	for k, v := range statsFields {
		frame[k] = v
	}
	msg, _ := json.Marshal(frame)
	client.writeMu.Lock()
	conn.Write(ctx, websocket.MessageText, msg)
	client.writeMu.Unlock()
}

// HandleStream is the HTTP handler for the /api/stream WebSocket endpoint.
func (asm *AppStreamManager) HandleStream(w http.ResponseWriter, r *http.Request) {
	conn, ok := httputil.AcceptWS(w, r, "app stream")
	if !ok {
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

		// Stop watchers for projects no one is subscribed to
		for projectID := range client.projectSub {
			if !asm.hasProjectSubscribers(projectID) {
				if asm.branchWatcher != nil {
					asm.branchWatcher.Unwatch(projectID)
				}
				if asm.projectWatcher != nil {
					asm.projectWatcher.Unwatch(projectID)
				}
			}
		}
	}()

	logging.For(logging.Stream).Infof("connection accepted (total: %d)", len(asm.clients))

	// Send initial project counts
	asm.BroadcastProjectCounts()

	// A failed ping cancels the read context, unblocking the Read loop below. The write
	// lock covers the ping because frames are broadcast to this connection elsewhere.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	httputil.Keepalive(ctx, conn, &client.writeMu, cancel)

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
				logging.For(logging.Stream).Infof("subscribed to project=%s", sub.ProjectID)

				// Send initial data for the project (background to not block the read loop)
				go asm.sendInitialProjectData(ctx, client, conn, sub)
			}

		case "unsubscribe_project":
			var sub AppStreamSubscription
			if err := json.Unmarshal(input.Data, &sub); err == nil && sub.ProjectID != "" {
				asm.unsubscribeProject(conn, sub.ProjectID)
				logging.For(logging.Stream).Infof("unsubscribed from project=%s", sub.ProjectID)
			}

		case "subscribe_task":
			var sub AppStreamSubscription
			if err := json.Unmarshal(input.Data, &sub); err == nil && sub.ProjectID != "" && sub.TaskID != "" {
				asm.subscribeTask(conn, sub.ProjectID, sub.TaskID)
				logging.For(logging.Stream).Infof("subscribed to task=%s in project=%s", sub.TaskID, sub.ProjectID)
			}

		case "unsubscribe_task":
			var sub AppStreamSubscription
			if err := json.Unmarshal(input.Data, &sub); err == nil && sub.ProjectID != "" && sub.TaskID != "" {
				asm.unsubscribeTask(conn, sub.ProjectID, sub.TaskID)
				logging.For(logging.Stream).Infof("unsubscribed from task=%s in project=%s", sub.TaskID, sub.ProjectID)
			}

		}
	}
}

func (asm *AppStreamManager) sendInitialProjectData(ctx context.Context, client *clientState, conn *websocket.Conn, sub AppStreamSubscription) {
	projectID := sub.ProjectID
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
		if task.Status == types.StatusDone {
			// Done tasks are never watched and never hit the worktree watcher's
			// git baseline resolution — their canonical diff (pinned
			// BaseCommit..BranchTip) is immutable, so the stored snapshot is the
			// source of truth (re-watched normally if the task leaves Done).
			asm.sendDoneTaskInitialDiff(ctx, client, conn, projectID, task, sub)
			continue
		}
		baseRef := asm.getDiffBaseline(task)
		// Watch worktrees for tasks in active states or with an initialized worktree.
		// Use resolved baseline (same as panel) so card stats match panel display.
		if task.Status == types.StatusInProgress || task.Status == types.StatusReview || task.Branch != "" {
			asm.WatchWorktree(task.ID, projectID, baseRef)
		}
		if diffResult, err := asm.worktreeMgr.GetTaskDiff(task.ID, baseRef, ""); err == nil && (diffResult.Diff != "" || len(diffResult.Untracked) > 0 || diffResult.CommittedDiff != "") {
			hash := ComputeDiffHash(diffResult.Diff, diffResult.CommittedDiff, diffResult.Untracked, diffResult.StagedHash)
			stats := ComputeDiffStats(diffResult, task)
			filesN := stats.VisibleTracked + stats.VisibleUntracked
			// Check if task branch commits are already merged into the target branch.
			stats.AlreadyMerged = asm.computeAlreadyMerged(projectID, task)
			statsMsg, _ := json.Marshal(map[string]any{
				"type":                  "task_diff_stats",
				"task_id":               task.ID,
				"hash":                  hash,
				"short_stat":            diffResult.ShortStat,
				"files_n":               filesN,
				"untracked_n":           stats.VisibleUntracked,
				"untracked_lines":       stats.UntrackedLines,
				"tracked_add":           stats.TrackedAdd,
				"tracked_del":           stats.TrackedDel,
				"file_hashes":           filterInternalPathsFromHashes(diffResult.GetPerFileHashes()),
				"staged_paths":          filterInternalPathsFromList(diffResult.StagedPaths),
				"has_committed_changes": diffResult.CommittedDiff != "",
				"already_merged":        stats.AlreadyMerged,
			})
			client.writeMu.Lock()
			conn.Write(ctx, websocket.MessageText, statsMsg)
			client.writeMu.Unlock()
			// No per-file task_diff manifest frame is broadcast: no component reads it, and the
			// task detail view fetches its data from the REST diff endpoint.
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
