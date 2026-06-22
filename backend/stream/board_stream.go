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

// diffSnapshot holds the last-known git state for a task, used to detect changes.
// Uses git diff --raw output (blob hashes) for reliable change detection.
type diffSnapshot struct {
	raw        string // git diff --raw output + untracked snapshot
	shortStat  string
	untrackedN int
}

// branchSnapshot holds the last-known branch for a project.
type branchSnapshot struct {
	branch string
}

// BoardStreamManager manages per-project WebSocket connections for board updates.
type BoardStreamManager struct {
	mu      sync.RWMutex
	clients map[string]map[*websocket.Conn]bool // projectID -> set of connections
	writeMu sync.Mutex // serializes all writes to prevent concurrent Read/Write races
	store   *store.Store
	worktreeMgr *git.WorktreeManager
	// pollers tracks the git diff stats poller context per project (one poller per project, not per client)
	pollersMu sync.Mutex
	pollers   map[string]context.CancelFunc
}

// NewBoardStreamManager creates a new BoardStreamManager.
func NewBoardStreamManager(store *store.Store, worktreeMgr *git.WorktreeManager) *BoardStreamManager {
	return &BoardStreamManager{
		clients:     make(map[string]map[*websocket.Conn]bool),
		store:       store,
		worktreeMgr: worktreeMgr,
		pollers:     make(map[string]context.CancelFunc),
	}
}

// getProjectClients returns the set of WebSocket connections for a project.
func (bsm *BoardStreamManager) getProjectClients(projectID string) map[*websocket.Conn]bool {
	bsm.mu.RLock()
	defer bsm.mu.RUnlock()
	return bsm.clients[projectID]
}

// getDiffBaseline computes the effective diff baseline for a task.
func (bsm *BoardStreamManager) getDiffBaseline(task types.Task) string {
	if task.Branch == "" || bsm.worktreeMgr == nil {
		return task.BaseRef
	}
	thread, _ := bsm.store.GetThread(task.CurrentThreadID)
	forkBranch := ""
	if thread != nil {
		forkBranch = thread.GitBranch
	}
	baseRef, _ := bsm.worktreeMgr.ResolveDiffBaseline(task.ID, task.BaseRef, task.Branch, forkBranch)
	return baseRef
}

// add registers a new client for a project.
func (bsm *BoardStreamManager) add(projectID string, conn *websocket.Conn) {
	bsm.mu.Lock()
	defer bsm.mu.Unlock()
	if bsm.clients[projectID] == nil {
		bsm.clients[projectID] = make(map[*websocket.Conn]bool)
	}
	bsm.clients[projectID][conn] = true
}

// remove unregisters a client from a project.
func (bsm *BoardStreamManager) remove(projectID string, conn *websocket.Conn) {
	bsm.mu.Lock()
	defer bsm.mu.Unlock()
	if clients, ok := bsm.clients[projectID]; ok {
		delete(clients, conn)
		if len(clients) == 0 {
			delete(bsm.clients, projectID)
		}
	}
}

// Broadcast sends a board_refresh event to all clients of a project,
// and a project_counts event to ALL connected clients across all projects.
func (bsm *BoardStreamManager) Broadcast(projectID string) {
	bsm.mu.RLock()
	clients := bsm.clients[projectID]
	bsm.mu.RUnlock()

	msg, _ := json.Marshal(map[string]string{"type": "board_refresh"})
	bsm.writeMu.Lock()
	for conn := range clients {
		if err := conn.Write(context.Background(), websocket.MessageText, msg); err != nil {
			// Client disconnected, will be cleaned up on read loop exit
		}
	}
	bsm.writeMu.Unlock()

	bsm.BroadcastProjectCounts()
}

// BroadcastAll sends a raw message to ALL connected WebSocket clients across all projects.
func (bsm *BoardStreamManager) BroadcastAll(msg []byte) {
	bsm.mu.RLock()
	allConns := make([]*websocket.Conn, 0)
	for _, conns := range bsm.clients {
		for conn := range conns {
			allConns = append(allConns, conn)
		}
	}
	bsm.mu.RUnlock()

	bsm.writeMu.Lock()
	for _, conn := range allConns {
		if err := conn.Write(context.Background(), websocket.MessageText, msg); err != nil {
			// Client disconnected
		}
	}
	bsm.writeMu.Unlock()
}

// BroadcastCommitStatus sends a commit_status event to all clients of a project.
// Status values: "queued" (waiting in queue), "processing" (commit in progress).
func (bsm *BoardStreamManager) BroadcastCommitStatus(projectID, taskID, status string) {
	bsm.mu.RLock()
	clients := bsm.clients[projectID]
	bsm.mu.RUnlock()

	msg, _ := json.Marshal(map[string]string{
		"type":    "commit_status",
		"task_id": taskID,
		"status":  status,
	})
	bsm.writeMu.Lock()
	for conn := range clients {
		if err := conn.Write(context.Background(), websocket.MessageText, msg); err != nil {
			// Client disconnected, will be cleaned up on read loop exit
		}
	}
	bsm.writeMu.Unlock()
}

// BroadcastBranch sends a project_branch event to all clients of a project.
func (bsm *BoardStreamManager) BroadcastBranch(projectID, branch string) {
	bsm.mu.RLock()
	clients := bsm.clients[projectID]
	bsm.mu.RUnlock()

	msg, _ := json.Marshal(map[string]interface{}{
		"type":   "project_branch",
		"branch": branch,
	})
	bsm.writeMu.Lock()
	for conn := range clients {
		if err := conn.Write(context.Background(), websocket.MessageText, msg); err != nil {
			// Client disconnected
		}
	}
	bsm.writeMu.Unlock()
}

// BroadcastDiff sends the full diff to all clients of a project.
func (bsm *BoardStreamManager) BroadcastDiff(projectID, taskID, diff string, shortStat string, untrackedN int, untracked []map[string]interface{}) {
	bsm.mu.RLock()
	clients := bsm.clients[projectID]
	bsm.mu.RUnlock()

	msg, _ := json.Marshal(map[string]interface{}{
		"type":       "task_diff",
		"task_id":    taskID,
		"diff":       diff,
		"short_stat": shortStat,
		"untracked":  untracked,
	})
	bsm.writeMu.Lock()
	for conn := range clients {
		if err := conn.Write(context.Background(), websocket.MessageText, msg); err != nil {
			// Client disconnected
		}
	}
	bsm.writeMu.Unlock()
}

// BroadcastTaskUpdated sends a task_updated event with the full task object to all clients of a project,
// and a project_counts event to ALL connected clients across all projects.
func (bsm *BoardStreamManager) BroadcastTaskUpdated(projectID string, task interface{}) {
	bsm.mu.RLock()
	clients := bsm.clients[projectID]
	bsm.mu.RUnlock()

	msg, _ := json.Marshal(map[string]interface{}{
		"type": "task_updated",
		"task": task,
	})
	bsm.writeMu.Lock()
	for conn := range clients {
		if err := conn.Write(context.Background(), websocket.MessageText, msg); err != nil {
			// Client disconnected, will be cleaned up on read loop exit
		}
	}
	bsm.writeMu.Unlock()

	bsm.BroadcastProjectCounts()
}

// BroadcastTaskDeleted sends a task_deleted event with the task ID to all clients of a project,
// and a project_counts event to ALL connected clients across all projects.
func (bsm *BoardStreamManager) BroadcastTaskDeleted(projectID, taskID string) {
	bsm.mu.RLock()
	clients := bsm.clients[projectID]
	bsm.mu.RUnlock()

	msg, _ := json.Marshal(map[string]interface{}{
		"type":    "task_deleted",
		"task_id": taskID,
	})
	bsm.writeMu.Lock()
	for conn := range clients {
		if err := conn.Write(context.Background(), websocket.MessageText, msg); err != nil {
			// Client disconnected, will be cleaned up on read loop exit
		}
	}
	bsm.writeMu.Unlock()

	bsm.BroadcastProjectCounts()
}

// BroadcastProjectCounts computes and broadcasts task counts for all projects.
func (bsm *BoardStreamManager) BroadcastProjectCounts() {
	if bsm.store == nil {
		return
	}
	counts := bsm.store.GetAllProjectCounts()
	msg, _ := json.Marshal(map[string]interface{}{
		"type": "project_counts",
		"data": counts,
	})
	bsm.BroadcastAll(msg)
}

// pollProjectDiffStats periodically checks git diff stats for tasks in in_progress/review
// and broadcasts updates when file/line counts change. Also monitors the project's current branch.
func (bsm *BoardStreamManager) pollProjectDiffStats(ctx context.Context, projectID string) {
	if bsm.store == nil || bsm.worktreeMgr == nil {
		return
	}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()

	// Track last-known diff per task to detect changes
	snapshots := make(map[string]diffSnapshot)
	var lastBranch string

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			// Check for branch changes
			branch, _ := bsm.worktreeMgr.GitRunInProjectOutput(projectID, "rev-parse", "--abbrev-ref", "HEAD")
			branch = strings.TrimSpace(branch)
			if branch != lastBranch {
				log.Printf("[board poll] project=%s branch changed: %s -> %s", projectID, lastBranch, branch)
				lastBranch = branch
				bsm.BroadcastBranch(projectID, branch)
			}

			tasks, err := bsm.store.GetTasksByProject(projectID)
			if err != nil {
				continue
			}
			for _, task := range tasks {
				if task.Status != "in_progress" && task.Status != "review" {
					continue
				}
				baseRef := bsm.getDiffBaseline(task)
				// Use --raw for cheap, reliable change detection (blob hashes)
				raw, err := bsm.worktreeMgr.GitDiffRaw(task.ID, baseRef)
				if err != nil {
					continue
				}
				// Only generate full diff when a change is actually detected
				prev, exists := snapshots[task.ID]
				if exists && prev.raw == raw {
					continue
				}
				// Change detected — fetch full diff for broadcast
				diffResult, err := bsm.worktreeMgr.GetTaskDiff(task.ID, baseRef, "")
				if err != nil {
					continue
				}
				shortStat := diffResult.ShortStat
				untrackedN := len(diffResult.Untracked)
				snapshots[task.ID] = diffSnapshot{raw: raw, shortStat: shortStat, untrackedN: untrackedN}
				untrackedMaps := make([]map[string]interface{}, len(diffResult.Untracked))
				for i, u := range diffResult.Untracked {
					untrackedMaps[i] = map[string]interface{}{"path": u.Path, "file_size": u.FileSize, "diff": u.Diff}
				}
				if diffResult.Diff != "" || untrackedN > 0 {
					log.Printf("[board poll] project=%s task=%s diff changed: %s (untracked: %d files)",
						projectID, task.ID, shortStat, untrackedN)
					bsm.BroadcastDiff(projectID, task.ID, diffResult.Diff, shortStat, untrackedN, untrackedMaps)
				} else {
					bsm.BroadcastDiff(projectID, task.ID, "", "", 0, nil)
				}
			}
		}
	}
}

// startPoller starts the diff stats poller for a project if not already running.
func (bsm *BoardStreamManager) startPoller(projectID string) {
	bsm.pollersMu.Lock()
	defer bsm.pollersMu.Unlock()
	if _, exists := bsm.pollers[projectID]; exists {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	bsm.pollers[projectID] = cancel
	go bsm.pollProjectDiffStats(ctx, projectID)
}

// stopPoller stops the diff stats poller for a project.
func (bsm *BoardStreamManager) stopPoller(projectID string) {
	bsm.pollersMu.Lock()
	defer bsm.pollersMu.Unlock()
	if cancel, exists := bsm.pollers[projectID]; exists {
		cancel()
		delete(bsm.pollers, projectID)
	}
}

// clientCount returns the number of connected clients for a project.
func (bsm *BoardStreamManager) clientCount(projectID string) int {
	bsm.mu.RLock()
	defer bsm.mu.RUnlock()
	return len(bsm.clients[projectID])
}

// HandleStream upgrades a WebSocket connection and subscribes to board updates.
func (bsm *BoardStreamManager) HandleStream(w http.ResponseWriter, r *http.Request, projectID string) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionContextTakeover,
	})
	if err != nil {
		log.Printf("board stream accept error: %v", err)
		return
	}

	// Track if this was the first client (to start the poller)
	wasEmpty := bsm.clientCount(projectID) == 0
	bsm.add(projectID, conn)
	defer func() {
		bsm.remove(projectID, conn)
		conn.Close(websocket.StatusNormalClosure, "closed")
		// Stop poller if this was the last client
		if bsm.clientCount(projectID) == 0 {
			bsm.stopPoller(projectID)
		}
	}()

	// Start poller when first client connects
	if wasEmpty {
		bsm.startPoller(projectID)
	}

	// Send initial diff for all tasks in this project
	if bsm.store != nil && bsm.worktreeMgr != nil {
		tasks, err := bsm.store.GetTasksByProject(projectID)
		if err == nil {
			for _, task := range tasks {
				baseRef := bsm.getDiffBaseline(task)
				if diffResult, err := bsm.worktreeMgr.GetTaskDiff(task.ID, baseRef, ""); err == nil && (diffResult.Diff != "" || len(diffResult.Untracked) > 0) {
					untrackedMaps := make([]map[string]interface{}, len(diffResult.Untracked))
					for i, u := range diffResult.Untracked {
						untrackedMaps[i] = map[string]interface{}{"path": u.Path, "file_size": u.FileSize, "diff": u.Diff}
					}
					bsm.BroadcastDiff(projectID, task.ID, diffResult.Diff, diffResult.ShortStat, len(diffResult.Untracked), untrackedMaps)
				}
			}
		}
	}

	// Send initial branch to this client
	if bsm.worktreeMgr != nil {
		branch, _ := bsm.worktreeMgr.GitRunInProjectOutput(projectID, "rev-parse", "--abbrev-ref", "HEAD")
		branch = strings.TrimSpace(branch)
		if branch != "" {
			msg, _ := json.Marshal(map[string]interface{}{
				"type":   "project_branch",
				"branch": branch,
			})
			bsm.writeMu.Lock()
			conn.Write(context.Background(), websocket.MessageText, msg)
			bsm.writeMu.Unlock()
		}
	}

	// Ping/pong keepalive to prevent proxy timeouts
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				bsm.writeMu.Lock()
				err := conn.Ping(context.Background())
				bsm.writeMu.Unlock()
				if err != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()

	for {
		_, _, err := conn.Read(context.Background())
		if err != nil {
			break
		}
	}
}
