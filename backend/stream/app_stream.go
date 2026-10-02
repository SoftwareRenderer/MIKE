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
	"strconv"
	"strings"
	"sync"
	"time"

	"mike/backend/fsutil"
	"mike/backend/git"
	"mike/backend/httputil"
	"mike/backend/store"
	"mike/backend/types"
	"mike/pkg/keylock"

	"github.com/coder/websocket"
	"mike/pkg/logging"
)

var toolCallMarker = []byte(`"event":"tool_call"`)

// toolResultMarker names the frame that reports a tool call's result.
var toolResultMarker = []byte(`"event":"tool_result"`)

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
	// DefaultBufferSize and DefaultBufferBytes bound what one task's replay buffer and one
	// connection's send queue hold, and both apply twice: a frame count bounds nothing on its
	// own, since a frame may be maxFrameBufferSize long.
	DefaultBufferSize  = 5000
	DefaultBufferBytes = 16 << 20

	// DefaultTaskBufferKeys is how many tasks may hold a replay buffer at once; the oldest
	// gives way.
	DefaultTaskBufferKeys = 64

	// DefaultMaxConnections bounds live /api/stream sockets. Each costs a read loop, a
	// pinger, a sender goroutine and a replay buffer.
	DefaultMaxConnections = 256

	// DefaultMaxTaskSubs and DefaultMaxProjectSubs cap what one connection may ask to be
	// told about; their keys arrive as text from the client.
	DefaultMaxTaskSubs    = 1024
	DefaultMaxProjectSubs = 256
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

type clientState struct {
	conn       *websocket.Conn
	projectSub map[string]struct{}
	taskSub    map[string]struct{}
	// noRowsTask is the subset of taskSub whose connections declared NoRows; see DurableThrough.
	noRowsTask map[string]struct{}
	// writeMu separates the two writers of conn: the sender goroutine draining out, and the
	// keepalive pinger, which takes it with TryLock (httputil.Keepalive).
	writeMu sync.Mutex
	out     *sendQueue
	// traffic counts what this connection was sent, by frame type, for the disconnect line.
	traffic *trafficLedger

	// A project's first-data pass is read by one worker per connection, in the order the
	// subscriptions arrived. initMu guards the passes waiting to run; initOrder is exactly the
	// keys of initPending, oldest first.
	initMu      sync.Mutex
	initPending map[string]AppStreamSubscription
	initOrder   []string
	initRunning bool
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
	// NoRows declares that this browser has no stored copy of the conversation — its read of the messages
	// window failed and it cached nothing — so the task's replay buffer is kept whole for it
	// (DurableThrough). A client drops the claim as soon as a read answers.
	NoRows bool `json:"no_rows,omitempty"`
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

	// Per-task ring buffer for reconnect sync.
	// When a client subscribes to a task, it gets the buffered events so it can
	// catch up on tokens/tool_calls that were streamed while it was disconnected.
	taskBufMu   sync.Mutex
	taskBuffers map[string][]taskFrame // task key -> ring buffer
	taskBufSize int
	// taskBufLoad is the bytes each key's buffer holds. taskBufOrder is exactly the keys of
	// taskBuffers, oldest opened first, which is the order they are evicted in.
	taskBufLoad  map[string]int
	taskBufOrder []string
	// convRev counts durable conversation writes per task key, guarded by taskBufMu. See ConversationRevision.
	convRev      map[string]uint64
	taskBufBound int // byte bound on one task's buffer
	taskBufKeys  int // how many tasks may hold one
	// queueFrames and queueBytes bound one connection's outbound queue; see sendQueue.
	queueFrames    int
	queueBytes     int
	maxConns       int
	maxTaskSubs    int
	maxProjectSubs int

	// Latest-known sandbox memory stats
	sandboxStatsProvider func(projectID, taskID string) *types.SandboxMemoryStats

	// writeTimeout bounds one frame's delivery to one client; see httputil.WriteTimeout.
	writeTimeout time.Duration
}

type taskFrame struct {
	data   []byte
	toolID string
	// settlesToolID names the call a frame reports the result of. The replay fold matches frames by
	// toolID, so a result's own id is held apart from it; see sendTaskPayload.
	settlesToolID string
	bucket        string
}

// NewAppStreamManager creates a new AppStreamManager with filesystem watchers.
func NewAppStreamManager(store *store.Store, worktreeMgr *git.WorktreeManager) *AppStreamManager {
	asm := &AppStreamManager{
		clients:        make(map[*websocket.Conn]*clientState),
		store:          store,
		worktreeMgr:    worktreeMgr,
		taskBuffers:    make(map[string][]taskFrame),
		taskBufLoad:    make(map[string]int),
		taskBufSize:    DefaultBufferSize,
		taskBufBound:   DefaultBufferBytes,
		taskBufKeys:    DefaultTaskBufferKeys,
		queueFrames:    DefaultBufferSize,
		queueBytes:     DefaultBufferBytes,
		maxConns:       DefaultMaxConnections,
		maxTaskSubs:    DefaultMaxTaskSubs,
		maxProjectSubs: DefaultMaxProjectSubs,
		writeTimeout:   httputil.WriteTimeout,
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

// WatchWorktree starts filesystem monitoring for a task's worktree.
func (asm *AppStreamManager) WatchWorktree(taskID, projectID string) {
	if asm.watcher != nil {
		_ = asm.watcher.Watch(taskID, projectID)
	}
}

// UnwatchWorktree stops filesystem monitoring for a task's worktree.
func (asm *AppStreamManager) UnwatchWorktree(taskID string) {
	if asm.watcher != nil {
		asm.watcher.Unwatch(taskID)
	}
}

// Close stops all watchers and releases resources; it is the whole "websocket watchers" stage.
func (asm *AppStreamManager) Close() {
	if asm.watcher != nil {
		asm.watcher.Close()
	}
	if asm.projectWatcher != nil {
		asm.projectWatcher.Close()
	}
	if asm.branchWatcher != nil {
		asm.branchWatcher.Close()
	}
}

// -- Subscription management --

// subscribeProject takes a project for this connection and reports whether the key is newly
// held. A connection holds a key once: the browser keeps the set of what it subscribed to and
// sends one unsubscribe per key, so a repeated subscribe frame changes nothing, and a reference
// count here could only drift upward and leave a project's watchers armed forever. Everything a
// subscription starts — the watchers below, the initial-data pass, a task buffer replay — runs
// on the frame that newly takes the key and on no later one.
func (asm *AppStreamManager) subscribeProject(conn *websocket.Conn, projectID string) bool {
	asm.mu.Lock()
	client := asm.clients[conn]
	if client == nil {
		asm.mu.Unlock()
		return false
	}
	if client.projectSub == nil {
		client.projectSub = make(map[string]struct{})
	}
	if _, held := client.projectSub[projectID]; held {
		asm.mu.Unlock()
		return false
	}
	if len(client.projectSub) >= asm.maxProjectSubs {
		asm.mu.Unlock()
		logging.For(logging.Stream).With(logging.ProjectID, projectID).Warnf("refusing a project subscription: this connection already holds %d", asm.maxProjectSubs)
		return false
	}
	client.projectSub[projectID] = struct{}{}
	asm.mu.Unlock()

	// Watch the project directory for branch switches and file create/delete events.
	project, err := asm.store.GetProject(projectID)
	if err == nil && project != nil && project.Path != "" {
		if asm.branchWatcher != nil {
			_ = asm.branchWatcher.Watch(projectID, project.Path)
		}
		if asm.projectWatcher != nil {
			_ = asm.projectWatcher.Watch(projectID, project.Path)
		}
	}
	return true
}

// unsubscribeProject releases the project; see subscribeProject for what a connection holds.
func (asm *AppStreamManager) unsubscribeProject(conn *websocket.Conn, projectID string) {
	asm.mu.Lock()
	client := asm.clients[conn]
	if client != nil {
		delete(client.projectSub, projectID)
	}
	asm.mu.Unlock()

	asm.releaseProjectWatchers(projectID)
}

// subscribeTask takes a task for this connection, held once per key exactly as subscribeProject
// holds a project, and replays only on the frame that newly takes it. noRows: AppStreamSubscription.NoRows.
func (asm *AppStreamManager) subscribeTask(conn *websocket.Conn, projectID, taskID string, noRows bool) bool {
	key := projectID + ":" + taskID
	asm.mu.Lock()
	client := asm.clients[conn]
	if client == nil {
		asm.mu.Unlock()
		return false
	}
	if client.taskSub == nil {
		client.taskSub = make(map[string]struct{})
	}
	if _, held := client.taskSub[key]; held {
		// A second take of a key already held changes the claim and earns no replay.
		asm.settleTaskClaim(client, projectID, taskID, noRows)
		asm.mu.Unlock()
		return false
	}
	if len(client.taskSub) >= asm.maxTaskSubs {
		asm.mu.Unlock()
		logging.For(logging.Stream).With(logging.TaskID, taskID).With(logging.ProjectID, projectID).
			Warnf("refusing a task subscription: this connection already holds %d", asm.maxTaskSubs)
		return false
	}
	client.taskSub[key] = struct{}{}
	asm.settleTaskClaim(client, projectID, taskID, noRows)
	// Read the buffer before releasing the lock that made this connection a subscriber, so a frame
	// being sent right now is either already in it or will be delivered to this socket.
	frames := asm.taskBufferFrames(key)
	asm.mu.Unlock()

	// Joining a call's frames is a pass over the snapshot with JSON in it, so it runs once the hub
	// lock is gone; what the connection is owed was settled by the snapshot, not by the fold.
	buf := foldToolCallFrames(frames)
	// Then the latest sandbox stats, which the task view needs even when that buffer has been
	// cleared. One batch, so the stats land after the replay they extend and share its budget.
	if statsFrame := asm.latestSandboxStatsFrame(projectID, taskID); statsFrame != nil {
		buf = append(buf, *statsFrame)
	}
	if refused := client.out.pushReplay(buf); refused > 0 {
		logging.For(logging.Stream).With(logging.TaskID, taskID).With(logging.ProjectID, projectID).
			Warnf("replay trimmed to the connection's budget: %d frame(s) not sent", refused)
	}
	return true
}

// latestSandboxStatsFrame returns the task_event frame carrying the
// provider's latest known sandbox memory stats for the task, or nil when no
// provider is registered or it reports no known stats.
func (asm *AppStreamManager) latestSandboxStatsFrame(projectID, taskID string) *outboundFrame {
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
	event := types.StreamEvent{Type: types.WSTypeSandboxStats, Data: stats}
	frame := TaskEventPayload(taskID, event)
	msg, err := json.Marshal(frame)
	if err != nil {
		return nil
	}
	return &outboundFrame{data: msg, bucket: string(event.Type)}
}

// SetSandboxStatsProvider registers the source of latest-known sandbox memory
// stats. The function is called on every subscribe_task; returning nil
// (unknown task) sends nothing.
func (asm *AppStreamManager) SetSandboxStatsProvider(fn func(projectID, taskID string) *types.SandboxMemoryStats) {
	asm.mu.Lock()
	asm.sandboxStatsProvider = fn
	asm.mu.Unlock()
}

// settleTaskClaim records or drops one connection's NoRows claim on a task it holds. The caller holds
// asm.mu for writing.
func (asm *AppStreamManager) settleTaskClaim(client *clientState, projectID, taskID string, noRows bool) {
	key := projectID + ":" + taskID
	_, claimed := client.noRowsTask[key]
	if noRows == claimed {
		return
	}
	if !noRows {
		delete(client.noRowsTask, key)
		return
	}
	client.noRowsTask[key] = struct{}{}
	logging.For(logging.Stream).With(logging.TaskID, taskID).With(logging.ProjectID, projectID).
		Warn("client has no conversation rows: owing it the whole replay buffer")
}

// unsubscribeTask releases the task; see subscribeTask for what a connection holds.
func (asm *AppStreamManager) unsubscribeTask(conn *websocket.Conn, projectID, taskID string) {
	asm.mu.Lock()
	client := asm.clients[conn]
	if client != nil {
		key := projectID + ":" + taskID
		delete(client.taskSub, key)
		delete(client.noRowsTask, key)
	}
	asm.mu.Unlock()
}

func (asm *AppStreamManager) hasProjectSubscribers(projectID string) bool {
	asm.mu.RLock()
	defer asm.mu.RUnlock()
	return len(asm.subscribers(projectSubs, projectID)) > 0
}

// releaseProjectWatchers stops a project's branch and filesystem watchers once no live
// connection holds the project, which is every client's count and not the caller's. The
// caller holds no lock and, when it is a connection leaving, has already removed its own
// entry: hasProjectSubscribers takes asm.mu as a reader.
func (asm *AppStreamManager) releaseProjectWatchers(projectID string) {
	if asm.hasProjectSubscribers(projectID) {
		return
	}
	if asm.branchWatcher != nil {
		asm.branchWatcher.Unwatch(projectID)
	}
	if asm.projectWatcher != nil {
		asm.projectWatcher.Unwatch(projectID)
	}
}

// subField selects one of a client's subscription sets (project- or
// task-scoped), so the fan-out helpers below stay single-purpose.
type subField func(*clientState) map[string]struct{}

var (
	projectSubs subField = func(c *clientState) map[string]struct{} { return c.projectSub }
	taskSubs    subField = func(c *clientState) map[string]struct{} { return c.taskSub }
)

// subscribers returns the connected clients holding key in the chosen subscription set, which
// is the whole of what "holding" means. Callers must hold at least asm.mu.RLock().
func (asm *AppStreamManager) subscribers(field subField, key string) []*clientState {
	targets := make([]*clientState, 0, len(asm.clients))
	for _, client := range asm.clients {
		if _, held := field(client)[key]; held {
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

// -- Broadcast methods --

func (asm *AppStreamManager) sendBoardPayload(projectID string, frame map[string]any) {
	asm.mu.RLock()
	targets := asm.subscribers(projectSubs, projectID)
	asm.mu.RUnlock()

	msg, err := json.Marshal(frame)
	if err != nil {
		return
	}

	asm.deliver(targets, msg, frameBucket(frame))
}

// appFrame builds one app-stream frame. The "type" key and the event name are written here and nowhere
// else in this package: a name spelled inline is a second spelling of the types.WSType constant, and the
// parity rule that compares the vocabulary against what the browser reads only sees the constants. Keys
// are marshalled sorted, so this changes no byte on the wire.
func appFrame(event types.WSMessageType, fields map[string]any) map[string]any {
	frame := make(map[string]any, len(fields)+1)
	frame["type"] = string(event)
	for k, v := range fields {
		frame[k] = v
	}
	return frame
}

// TaskEventPayload builds the flat task_event envelope shared by SendTaskEvent
// and on-subscribe replays (e.g. the latest sandbox stats).
func TaskEventPayload(taskID string, event types.StreamEvent) map[string]any {
	return appFrame(types.WSTypeTaskEvent, map[string]any{
		"task_id": taskID,
		"event":   string(event.Type),
		"data":    event.Data,
	})
}

// LockWaitNoticeEvery is how often a waiting caller repeats itself: three seconds is where "still going"
// stops looking like a pause and starts looking like a wait someone is deciding about.
const LockWaitNoticeEvery = 3 * time.Second

// LockWaitNotifier returns the keylock notice that tells a task's clients why they are still waiting and
// clears it when the lock is taken. The table already logs a slow wait for the operator; this is the half
// for the person who clicked, so it belongs only on an acquisition a click is waiting on.
func (asm *AppStreamManager) LockWaitNotifier(projectID, taskID, domain string) keylock.WaitNotice {
	return func(waited time.Duration, acquired bool) {
		asm.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeLockWait, Data: map[string]any{
			"domain":         domain,
			"waited_seconds": int(waited.Seconds()),
			"acquired":       acquired,
		}})
	}
}

// SendTaskEvent sends a task-level event to all clients subscribed to the task.
func (asm *AppStreamManager) SendTaskEvent(projectID, taskID string, event types.StreamEvent) {
	asm.sendTaskPayload(projectID, taskID, TaskEventPayload(taskID, event))

	// Clear per-task buffer when a streaming turn ends, so reconnecting clients
	// don't get stale tokens from a previous turn.
	switch event.Type {
	case types.WSTypeStatus:
		status, ok := event.Data.(types.TurnStatus)
		if !ok {
			// A status frame nobody built with types.TurnStatusEvent: the frame still goes out,
			// and whether the turn ended is a question nobody answered.
			logging.For(logging.Stream).With(logging.TaskID, taskID).Warnf("status frame carrying %T instead of types.TurnStatus", event.Data)
			return
		}
		if terminalTurnStates[status.State] {
			asm.clearTaskBuffer(projectID + ":" + taskID)
		}
	case types.WSTypeComplete:
		asm.clearTaskBuffer(projectID + ":" + taskID)
	}
}

var terminalTurnStates = map[string]bool{
	"failed":    true,
	"completed": true,
	"idle":      true,
	"cancelled": true, // wire-only: a response replaced by a new message, not an execution state
	"paused":    true,
}

func (asm *AppStreamManager) sendTaskPayload(projectID, taskID string, frame map[string]any) {
	key := projectID + ":" + taskID
	msg, err := json.Marshal(frame)
	if err != nil {
		return
	}
	// tool_id rides the buffered frame so a replay can collapse one call's frames; it is read here
	// because the hub lock taken below must not be held across JSON parsing. A result frame's id is held
	// apart from toolID because the fold would otherwise let it replace the call's own frame.
	var toolID, settles string
	switch {
	case bytes.Contains(msg, toolCallMarker):
		toolID = extractToolID(msg)
	case bytes.Contains(msg, toolResultMarker):
		settles = extractToolID(msg)
	}

	// Handing the frame to the subscribers and adding it to the replay buffer are one step, and so
	// is taking the task (see subscribeTask). A frame therefore reaches a connection exactly once:
	// either it was buffered before that connection held the task, so the replay carries it, or it
	// arrives over the socket. Split apart, a subscribe landing between the two steps got the frame
	// twice or never, and never is a hole the client cannot see.
	asm.mu.Lock()
	asm.deliver(asm.subscribers(taskSubs, key), msg, frameBucket(frame))
	asm.bufferTaskLocked(key, msg, toolID, settles, frameBucket(frame))
	asm.mu.Unlock()
}

// BroadcastAll sends a raw message to all connected clients.
func (asm *AppStreamManager) BroadcastAll(msg []byte, bucket string) {
	asm.mu.RLock()
	targets := asm.allClients()
	asm.mu.RUnlock()
	asm.deliver(targets, msg, bucket)
}

// BroadcastProjectCounts computes and broadcasts task counts for all projects.
func (asm *AppStreamManager) BroadcastProjectCounts() {
	if asm.store == nil {
		return
	}
	counts := asm.store.GetAllProjectCounts()
	msg, _ := json.Marshal(appFrame(types.WSTypeProjectCounts, map[string]any{"data": counts}))
	asm.BroadcastAll(msg, string(types.WSTypeProjectCounts))
}

// -- One-shot event broadcasts: board, explorer, diff, extension --

// Broadcast sends a board_refresh event to a project and project_counts to all clients.
func (asm *AppStreamManager) Broadcast(projectID string) {
	asm.sendBoardPayload(projectID, appFrame(types.WSTypeBoardRefresh, nil))
	asm.BroadcastProjectCounts()
}

// BroadcastCommitStatus sends a commit_status event to all clients of a project.
func (asm *AppStreamManager) BroadcastCommitStatus(projectID, taskID, status string) {
	asm.sendBoardPayload(projectID, appFrame(types.WSTypeCommitStatus, map[string]any{
		"task_id": taskID,
		"status":  status,
	}))
}

// BroadcastBranch sends a project_branch event to all clients of a project.
func (asm *AppStreamManager) BroadcastBranch(projectID, branch string) {
	asm.sendBoardPayload(projectID, appFrame(types.WSTypeProjectBranch, map[string]any{
		"branch": branch,
	}))
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
	asm.sendBoardPayload(projectID, appFrame(types.WSTypeFsEvent, map[string]any{
		"op":   op,
		"path": path,
	}))
}

// SetDiffCacheUpdate sets a callback to update the GitHandler's diff cache.
func (asm *AppStreamManager) SetDiffCacheUpdate(fn func(taskID string, taskDiff *git.TaskDiff)) {
	if asm.watcher != nil {
		asm.watcher.updateCache = fn
	}
}

// BroadcastDiffStats sends compact diff stats to all clients of a project.
//
// Three producers write these keys: here, the per-connection initial load below, and the stored snapshot
// in done_snapshot.go. All three build them from a git.TaskDiff, so one diff answers one hash
// (DiffHash) and one has_committed_changes (git.TaskDiff.HasCommittedWork) whichever of them spoke.
func (asm *AppStreamManager) BroadcastDiffStats(event DiffStatsEvent) {
	asm.sendBoardPayload(event.ProjectID, appFrame(types.WSTypeTaskDiffStats, map[string]any{
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
	}))
}

// DiffHash is the hash a task_diff_stats frame carries for a computed diff: the diff, its committed half,
// the untracked files and the staged state. It takes the whole TaskDiff so no producer picks its own answer
// to what counts as committed.
func DiffHash(d *git.TaskDiff) string {
	var hashInput strings.Builder
	hashInput.WriteString(d.Diff)
	hashInput.WriteString("\x00committed:" + d.CommittedHalf())
	for _, u := range d.Untracked {
		hashInput.WriteString(u.Path + strings.Join(u.Lines, "\n"))
	}
	if d.StagedHash != "" {
		hashInput.WriteString("\x00staged:" + d.StagedHash)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(hashInput.String())))
}

// BroadcastTaskUpdated sends a task_updated event and project_counts to all clients.
//
// The task is typed rather than `any` because the frame's whole contract is "the browser may take this row
// at face value": with `any`, a caller could hand it a dereferenced row, a DTO, or a map and every one of
// them marshals into something the client reads as a task.
func (asm *AppStreamManager) BroadcastTaskUpdated(projectID string, task *types.Task) {
	asm.sendBoardPayload(projectID, appFrame(types.WSTypeTaskUpdated, map[string]any{
		"task": TaskForBrowser(task),
	}))
	asm.BroadcastProjectCounts()
}

// BroadcastTaskDeleted sends a task_deleted event and project_counts to all clients.
func (asm *AppStreamManager) BroadcastTaskDeleted(projectID, taskID string) {
	// Drop the task's replay buffer: a deleted task's frames (tokens, stats,
	// …) must never be replayed to a late subscriber. The buffer is normally
	// cleared by terminal status/complete events, but deletion bypasses the
	// executor lifecycle.
	asm.clearTaskBuffer(projectID + ":" + taskID)
	asm.forgetConversationRevision(projectID, taskID)
	asm.sendBoardPayload(projectID, appFrame(types.WSTypeTaskDeleted, map[string]any{
		"task_id": taskID,
	}))
	asm.BroadcastProjectCounts()
}

// SendProjectEvent sends one typed event to every client subscribed to the project. For work that
// belongs to the project rather than to a task — building its sandbox image while the file explorer
// waits, where there is no task chat to carry the progress.
func (asm *AppStreamManager) SendProjectEvent(projectID string, event types.StreamEvent) {
	asm.sendBoardPayload(projectID, appFrame(event.Type, map[string]any{
		"data": event.Data,
	}))
}

// BroadcastProjectPorts sends a project_ports event to all clients of a project.
func (asm *AppStreamManager) BroadcastProjectPorts(projectID string, ports []types.AssignedPort) {
	asm.sendBoardPayload(projectID, appFrame(types.WSTypeProjectPorts, map[string]any{
		"ports": ports,
	}))
}

// BroadcastExtensionChange sends an extension_change event to all connected clients.
// This triggers the frontend to reload extensions and refresh UI injection slots.
func (asm *AppStreamManager) BroadcastExtensionChange() {
	frame := appFrame(types.WSTypeExtensionChange, nil)
	msg, _ := json.Marshal(frame)
	asm.BroadcastAll(msg, frameBucket(frame))
}

// -- Task replay buffer --

// bufferContentFrames are the buffered events whose content a conversation row carries once the turn has
// written it, and a trim drops them. Every other event on the task channel is a current value — turn
// state, token count, queue position — and a trim keeps the newest of each.
var bufferContentFrames = map[string]bool{
	string(types.WSTypeToken):         true,
	string(types.WSTypeReasoning):     true,
	string(types.WSTypeToolCall):      true,
	string(types.WSTypeToolProgress):  true,
	string(types.WSTypeToolOutput):    true,
	string(types.WSTypeToolResult):    true,
	string(types.WSTypeUserMessage):   true,
	string(types.WSTypeSummarization): true,
	string(types.WSTypeError):         true,
}

// trimTaskBufferToUndurable keeps what the conversation rows cannot answer: every frame of a call with no
// result row yet, and the newest value of each state event. The window read ends where durability ends and
// the replay starts there, which is why a mid-turn open is owed seconds of stream rather than the turn.
func (asm *AppStreamManager) trimTaskBufferToUndurable(taskKey string) {
	asm.taskBufMu.Lock()
	defer asm.taskBufMu.Unlock()
	buf, held := asm.taskBuffers[taskKey]
	if !held {
		return
	}

	settled := make(map[string]bool)
	newestState := map[string]int{}
	for i, f := range buf {
		if f.settlesToolID != "" {
			settled[f.settlesToolID] = true
		}
		if !bufferContentFrames[f.bucket] {
			newestState[f.bucket] = i
		}
	}
	kept := make([]taskFrame, 0, len(buf))
	loaded := 0
	for i, f := range buf {
		running := f.toolID != "" && !settled[f.toolID]
		latestState := !bufferContentFrames[f.bucket] && newestState[f.bucket] == i
		if !running && !latestState {
			continue
		}
		kept = append(kept, f)
		loaded += len(f.data)
	}
	if len(kept) == 0 {
		// Nothing is owed but the rows, so this key gives up its buffer.
		asm.dropTaskBufferLocked(taskKey)
		return
	}
	asm.taskBuffers[taskKey] = kept
	asm.taskBufLoad[taskKey] = loaded
}

// holdsTaskWithoutRowsLocked reports whether any live connection holds this task while declaring it has no
// rows, which is what stops a trim. The caller holds at least asm.mu.
func (asm *AppStreamManager) holdsTaskWithoutRowsLocked(taskKey string) bool {
	for _, client := range asm.clients {
		if _, ok := client.noRowsTask[taskKey]; ok {
			return true
		}
	}
	return false
}

// conversationEpoch names this process. A revision restarts at zero with it, so the epoch is what turns "a
// process that forgot" into one re-read rather than a stale screen.
var conversationEpoch = strconv.FormatInt(time.Now().UnixNano(), 36)

// ConversationRevision counts the durable conversation writes this task has had in this process, which a client
// compares against the read it applied. It is a count, not a version: it restarts with the process and is
// dropped when the task is deleted, and a client that cannot match its cursor re-reads.
func (asm *AppStreamManager) ConversationRevision(projectID, taskID string) uint64 {
	key := projectID + ":" + taskID
	asm.taskBufMu.Lock()
	defer asm.taskBufMu.Unlock()
	return asm.convRev[key]
}

// ConversationEpoch is the value that tells a client whether the revisions it holds came from this process.
func (asm *AppStreamManager) ConversationEpoch() string { return conversationEpoch }

// TaskDiffDigest is what this process last learned about a task's diff. ok=false means it never computed one,
// which is an unknown and not "nothing changed".
func (asm *AppStreamManager) TaskDiffDigest(taskID string) (hash string, staged int, ok bool) {
	if asm.watcher == nil {
		return "", 0, false
	}
	return asm.watcher.DiffDigest(taskID)
}

// DurableThrough tells the hub that everything the task has said up to now is a conversation row, so its
// buffer keeps only what no row answers. The executor calls it where a row lands, never at the frame that
// precedes the row: a `tool_result` frame arrives before its row is written. A path that removes rows calls it
// too.
func (asm *AppStreamManager) DurableThrough(projectID, taskID string) {
	key := projectID + ":" + taskID
	// The conversation moved whether or not the buffer is trimmed next.
	asm.taskBufMu.Lock()
	if asm.convRev == nil {
		asm.convRev = make(map[string]uint64)
	}
	asm.convRev[key]++
	asm.taskBufMu.Unlock()
	asm.mu.RLock()
	owed := asm.holdsTaskWithoutRowsLocked(key)
	asm.mu.RUnlock()
	if owed {
		return
	}
	asm.trimTaskBufferToUndurable(key)
}

func (asm *AppStreamManager) bufferTaskLocked(taskKey string, frame []byte, toolID, settlesToolID, bucket string) {
	// Skip large frames to prevent memory exhaustion
	if len(frame) > maxFrameBufferSize {
		return
	}

	asm.taskBufMu.Lock()
	defer asm.taskBufMu.Unlock()
	if _, fresh := asm.taskBuffers[taskKey]; !fresh {
		asm.makeRoomForTaskBufferLocked()
		asm.taskBufOrder = append(asm.taskBufOrder, taskKey)
	}

	// Copy frame to avoid aliasing
	frameCopy := make([]byte, len(frame))
	copy(frameCopy, frame)
	buf := append(asm.taskBuffers[taskKey], taskFrame{data: frameCopy, toolID: toolID, settlesToolID: settlesToolID, bucket: bucket})

	loaded := asm.taskBufLoad[taskKey] + len(frameCopy)
	for len(buf) > asm.taskBufSize || (loaded > asm.taskBufBound && len(buf) > 1) {
		loaded -= len(buf[0].data)
		buf = buf[1:]
	}
	asm.taskBuffers[taskKey] = buf
	asm.taskBufLoad[taskKey] = loaded
}

// makeRoomForTaskBufferLocked closes the oldest task buffer so an incoming task may open one.
// Reaching the cap means tasks streamed without a terminal event or a deletion, and the
// freshest keys are the ones someone may be looking at.
func (asm *AppStreamManager) makeRoomForTaskBufferLocked() {
	if len(asm.taskBufOrder) == 0 || len(asm.taskBufOrder) < asm.taskBufKeys {
		return
	}
	oldest := asm.taskBufOrder[0]
	asm.taskBufOrder = asm.taskBufOrder[1:]
	logging.For(logging.Stream).Warnf("task replay buffers at their limit of %d, dropping the oldest (task=%s)", asm.taskBufKeys, oldest)
	delete(asm.taskBuffers, oldest)
	delete(asm.taskBufLoad, oldest)
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

// foldToolCallFrames collapses the frames of one streaming tool call into the single frame a
// subscriber is owed. The live path sends a call's whole args and then only its growth, and two
// frames for one call replay as two cards — so the frames are joined where the replay is read, which
// costs one pass per subscribe rather than one per frame as it is buffered, and keeps the JSON out of
// the hub lock. A full buffer of one call's deltas measured 26 ms; the same pass over 5000 frames
// with nothing to join measured 0.9 ms.
//
// A frame carrying the whole args supersedes what was collected (args only grow), and a delta is
// appended to the whole held so far. A delta with no whole ahead of it — the head has been trimmed
// from the buffer — stays a delta: there is nothing to join it to, and dropping it would hide the call.
func foldToolCallFrames(buf []taskFrame) []outboundFrame {
	out := make([]outboundFrame, 0, len(buf))
	heldAt := map[string]int{}
	for _, f := range buf {
		if f.toolID == "" {
			out = append(out, outboundFrame{data: f.data, bucket: f.bucket})
			continue
		}
		at, seen := heldAt[f.toolID]
		if !seen {
			heldAt[f.toolID] = len(out)
			out = append(out, outboundFrame{data: f.data, bucket: f.bucket})
			continue
		}
		out[at].data = foldToolCall(out[at].data, f.data)
	}
	return out
}

func foldToolCall(held, next []byte) []byte {
	var envelope map[string]any
	if err := json.Unmarshal(next, &envelope); err != nil {
		return next
	}
	data, _ := envelope["data"].(map[string]any)
	delta, isDelta := data["raw_args_delta"].(string)
	if !isDelta {
		return next
	}
	whole, ok := toolCallArgs(held)["raw_args"].(string)
	if !ok {
		return next
	}
	data["raw_args"] = whole + delta
	delete(data, "raw_args_delta")
	merged, err := json.Marshal(envelope)
	if err != nil {
		return next
	}
	return merged
}

// toolCallArgs reads the payload of a buffered task_event frame, or nil when it does not read as
// an object payload.
func toolCallArgs(frame []byte) map[string]any {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(frame, &envelope); err != nil || len(envelope.Data) == 0 {
		return nil
	}
	var data map[string]any
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		return nil
	}
	return data
}

// taskBufferFrames is the task's replay buffer as it stands. The caller holds at least asm.mu, so a
// frame arriving now is either in the copy or about to reach that caller's connection over the socket.
// The frames are shared, not copied: the buffer only ever appends past what this copy holds or trims
// from its front, and a fold builds new frames rather than writing into these.
func (asm *AppStreamManager) taskBufferFrames(taskKey string) []taskFrame {
	asm.taskBufMu.Lock()
	defer asm.taskBufMu.Unlock()
	buf := asm.taskBuffers[taskKey]
	frames := make([]taskFrame, len(buf))
	copy(frames, buf)
	return frames
}

// dropTaskBufferLocked retires one task's buffer and removes it from the eviction order, whose keys are
// always exactly the buffers held. The caller holds asm.taskBufMu.
func (asm *AppStreamManager) dropTaskBufferLocked(taskKey string) {
	delete(asm.taskBuffers, taskKey)
	delete(asm.taskBufLoad, taskKey)
	for i, key := range asm.taskBufOrder {
		if key == taskKey {
			asm.taskBufOrder = append(asm.taskBufOrder[:i], asm.taskBufOrder[i+1:]...)
			break
		}
	}
}

func (asm *AppStreamManager) clearTaskBuffer(taskKey string) {
	asm.taskBufMu.Lock()
	defer asm.taskBufMu.Unlock()
	asm.dropTaskBufferLocked(taskKey)
}

// forgetConversationRevision retires a deleted task's count. A key reused by a new task starts at zero, which
// costs a client one re-read.
func (asm *AppStreamManager) forgetConversationRevision(projectID, taskID string) {
	key := projectID + ":" + taskID
	asm.taskBufMu.Lock()
	defer asm.taskBufMu.Unlock()
	delete(asm.convRev, key)
}

// ClearTaskBuffer removes the per-task ring buffer.
func (asm *AppStreamManager) ClearTaskBuffer(projectID, taskID string) {
	asm.clearTaskBuffer(projectID + ":" + taskID)
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
func (asm *AppStreamManager) sendDoneTaskInitialDiff(client *clientState, projectID string, task types.Task, sub AppStreamSubscription) {
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
	frame := appFrame(types.WSTypeTaskDiffStats, map[string]any{"task_id": task.ID})
	for k, v := range statsFields {
		frame[k] = v
	}
	msg, _ := json.Marshal(frame)
	client.out.push(msg, frameBucket(frame))
}

// HandleStream is the HTTP handler for the /api/stream WebSocket endpoint.
func (asm *AppStreamManager) HandleStream(w http.ResponseWriter, r *http.Request) {
	asm.mu.RLock()
	live := len(asm.clients)
	asm.mu.RUnlock()
	if live >= asm.maxConns {
		// Answered before the upgrade, so the browser sees a refusal rather than a connection
		// it has to work out is dead. This reads a count only: connections arrive during the gap
		// before the slot is taken at registration below, so that is the authoritative bound.
		logging.For(logging.Stream).Warnf("refusing a connection: %d already live (limit %d)", live, asm.maxConns)
		http.Error(w, "too many live stream connections", http.StatusServiceUnavailable)
		return
	}

	conn, ok := httputil.AcceptWS(w, r, "app stream")
	if !ok {
		return
	}

	client := newClientState(conn, asm)

	asm.mu.Lock()
	// Counting and joining the map are one decision, so two handshakes that each read a free
	// slot cannot both take one.
	pastCap := len(asm.clients) >= asm.maxConns
	if !pastCap {
		asm.clients[conn] = client
	}
	total := len(asm.clients)
	asm.mu.Unlock()

	if pastCap {
		// The upgrade already happened, so the refusal is a close with a code that says retry.
		logging.For(logging.Stream).Warnf("refusing a connection after its upgrade: %d already live (limit %d)", total, asm.maxConns)
		_ = conn.Close(websocket.StatusTryAgainLater, "too many live stream connections")
		return
	}

	defer func() {
		asm.mu.Lock()
		delete(asm.clients, conn)
		asm.mu.Unlock()
		conn.Close(websocket.StatusNormalClosure, "closed")

		for projectID := range client.projectSub {
			asm.releaseProjectWatchers(projectID)
		}
		if frames, _ := client.traffic.totals(); frames > 0 {
			logging.For(logging.Stream).Debugf("connection closed: %s", client.traffic.report())
		}
	}()

	logging.For(logging.Stream).Infof("connection accepted (total: %d)", total)

	// A failed ping cancels the read context, unblocking the Read loop below. The write
	// lock covers the ping because the sender goroutine writes frames to this connection too.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Only this endpoint's client measures silence from onmessage, so only it gets the app frame.
	httputil.Keepalive(ctx, conn, &client.writeMu, cancel, httputil.WithAppFrame())
	go asm.runSender(ctx, client)

	// Send initial project counts
	asm.BroadcastProjectCounts()

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
				if asm.subscribeProject(conn, sub.ProjectID) {
					logging.For(logging.Stream).With(logging.ProjectID, sub.ProjectID).Info("subscribed")
					// Queued rather than run here: the pass must not block the read loop.
					asm.queueInitialProjectData(ctx, client, sub)
				}
			}

		case "unsubscribe_project":
			var sub AppStreamSubscription
			if err := json.Unmarshal(input.Data, &sub); err == nil && sub.ProjectID != "" {
				asm.unsubscribeProject(conn, sub.ProjectID)
				logging.For(logging.Stream).With(logging.ProjectID, sub.ProjectID).Info("unsubscribed")
			}

		case "subscribe_task":
			var sub AppStreamSubscription
			if err := json.Unmarshal(input.Data, &sub); err == nil && sub.ProjectID != "" && sub.TaskID != "" {
				if asm.subscribeTask(conn, sub.ProjectID, sub.TaskID, sub.NoRows) {
					logging.For(logging.Stream).With(logging.TaskID, sub.TaskID).With(logging.ProjectID, sub.ProjectID).
						Info("subscribed")
				}
			}

		case "unsubscribe_task":
			var sub AppStreamSubscription
			if err := json.Unmarshal(input.Data, &sub); err == nil && sub.ProjectID != "" && sub.TaskID != "" {
				asm.unsubscribeTask(conn, sub.ProjectID, sub.TaskID)
				logging.For(logging.Stream).With(logging.TaskID, sub.TaskID).With(logging.ProjectID, sub.ProjectID).
					Info("unsubscribed")
			}

		}
	}
}

// queueInitialProjectData asks for a project's initial data. One worker per connection takes
// the pending projects in arrival order, and a project already pending is not queued twice: a
// second pass over a project would re-read the diffs the first is reading and re-watch the
// worktrees it is already watching, so it buys nothing but git processes — and two passes
// writing at once would interleave their frames.
func (asm *AppStreamManager) queueInitialProjectData(ctx context.Context, client *clientState, sub AppStreamSubscription) {
	client.initMu.Lock()
	if _, pending := client.initPending[sub.ProjectID]; !pending {
		client.initOrder = append(client.initOrder, sub.ProjectID)
	}
	// The newest frame decides what the client still needs, so its known_stats wins.
	client.initPending[sub.ProjectID] = sub
	startWorker := !client.initRunning
	client.initRunning = true
	client.initMu.Unlock()

	if startWorker {
		go asm.initialProjectWorker(ctx, client)
	}
}

// initialProjectWorker runs the connection's queued initial-data passes, one at a time. It ends
// with the read loop's context, which is cancelled when the connection goes away.
func (asm *AppStreamManager) initialProjectWorker(ctx context.Context, client *clientState) {
	for {
		client.initMu.Lock()
		if len(client.initOrder) == 0 {
			client.initRunning = false
			client.initMu.Unlock()
			return
		}
		projectID := client.initOrder[0]
		client.initOrder = client.initOrder[1:]
		sub, pending := client.initPending[projectID]
		delete(client.initPending, projectID)
		client.initMu.Unlock()

		// A project the connection left while its pass waited is not worth reading: the
		// frames would answer a subscription that no longer exists.
		asm.mu.RLock()
		_, held := client.projectSub[projectID]
		asm.mu.RUnlock()

		if pending && held {
			asm.sendInitialProjectData(ctx, client, sub)
		}
	}
}

func (asm *AppStreamManager) sendInitialProjectData(ctx context.Context, client *clientState, sub AppStreamSubscription) {
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
			asm.sendDoneTaskInitialDiff(client, projectID, task, sub)
			continue
		}
		baseRef := asm.worktreeMgr.TaskDiffBaseline(&task)
		// Watch worktrees for tasks in active states or with an initialized worktree.
		if task.Status == types.StatusInProgress || task.Status == types.StatusReview || task.Branch != "" {
			asm.WatchWorktree(task.ID, projectID)
		}
		if diffResult, err := asm.worktreeMgr.GetTaskDiff(task.ID, baseRef, ""); err == nil && diffResult.HasChanges() {
			hash := DiffHash(diffResult)
			stats := ComputeDiffStats(diffResult)
			filesN := stats.VisibleTracked + stats.VisibleUntracked
			// Check if task branch commits are already merged into the target branch.
			stats.AlreadyMerged = asm.computeAlreadyMerged(projectID, task)
			statsFrame := appFrame(types.WSTypeTaskDiffStats, map[string]any{ // the shape BroadcastDiffStats documents
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
				"has_committed_changes": diffResult.HasCommittedWork(),
				"already_merged":        stats.AlreadyMerged,
			})
			statsMsg, _ := json.Marshal(statsFrame)
			client.out.push(statsMsg, frameBucket(statsFrame))
			// No per-file task_diff manifest frame is broadcast: no component reads it, and the
			// task detail view fetches its data from the REST diff endpoint.
		}
	}

	branch, _ := asm.worktreeMgr.GitRunInProjectOutput(projectID, "rev-parse", "--abbrev-ref", "HEAD")
	branch = strings.TrimSpace(branch)
	if branch != "" {
		frame := appFrame(types.WSTypeProjectBranch, map[string]any{
			"branch": branch,
		})
		msg, _ := json.Marshal(frame)
		client.out.push(msg, frameBucket(frame))
	}
}
