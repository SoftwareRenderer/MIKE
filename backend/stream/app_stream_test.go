package stream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kanban/backend/types"

	"github.com/coder/websocket"
)

// assertFlatJSON verifies that a JSON frame is flat (no double-encoded data field).
// It checks that the given key exists and is an object (not a string).
func assertFlatJSON(t *testing.T, frame []byte, typeKey, dataKey string, expectedType string) map[string]any {
	t.Helper()
	var msg map[string]any
	if err := json.Unmarshal(frame, &msg); err != nil {
		t.Fatalf("unmarshal frame: %v: %s", err, string(frame))
	}
	if msg[typeKey] != expectedType {
		t.Fatalf("expected %s=%s, got %v: %s", typeKey, expectedType, msg[typeKey], string(frame))
	}
	if dataKey != "" {
		data, ok := msg[dataKey]
		if !ok {
			t.Fatalf("expected '%s' field in message: %s", dataKey, string(frame))
		}
		if _, ok := data.(map[string]any); !ok {
			t.Fatalf("expected '%s' to be an object (not double-encoded), got %T: %s", dataKey, data, string(frame))
		}
	}
	return msg
}

// TestTaskUpdatedWireFormat verifies that a task_updated event serializes as
// flat JSON so the client can access msg.task directly (not msg.data.task).
func TestTaskUpdatedWireFormat(t *testing.T) {
	task := &types.Task{
		ID:        "test-task",
		ProjectID: "test-project",
		Title:     "Test task",
		Status:    "backlog",
	}
	frame, err := json.Marshal(map[string]any{
		"type": "task_updated",
		"task": task,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	msg := assertFlatJSON(t, frame, "type", "task", "task_updated")
	taskData := msg["task"].(map[string]any)
	if taskData["id"] != "test-task" {
		t.Fatalf("expected task.id=test-task, got %v", taskData["id"])
	}
}

// TestTaskEventWireFormat verifies that a task_event serializes as flat JSON
// with type, task_id, event, and data at the top level.
func TestTaskEventWireFormat(t *testing.T) {
	payload := map[string]any{
		"type":    "task_event",
		"task_id": "test-task",
		"event":   "token",
		"data":    map[string]string{"text": "hello"},
	}
	frame, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	msg := assertFlatJSON(t, frame, "type", "data", "task_event")
	if msg["task_id"] != "test-task" {
		t.Fatalf("expected task_id=test-task, got %v", msg["task_id"])
	}
	if msg["event"] != "token" {
		t.Fatalf("expected event=token, got %v", msg["event"])
	}
	dataMap := msg["data"].(map[string]any)
	if dataMap["text"] != "hello" {
		t.Fatalf("expected data.text=hello, got %v", dataMap["text"])
	}
}

// TestCommitStatusWireFormat verifies commit_status is flat JSON.
func TestCommitStatusWireFormat(t *testing.T) {
	frame, err := json.Marshal(map[string]string{
		"type":    "commit_status",
		"task_id": "test-task",
		"status":  "queued",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	msg := assertFlatJSON(t, frame, "type", "", "commit_status")
	if msg["task_id"] != "test-task" {
		t.Fatalf("expected task_id=test-task, got %v", msg["task_id"])
	}
	if msg["status"] != "queued" {
		t.Fatalf("expected status=queued, got %v", msg["status"])
	}
}

// TestProjectCountsWireFormat verifies project_counts is flat JSON.
func TestProjectCountsWireFormat(t *testing.T) {
	counts := map[string]any{
		"test-project": map[string]any{
			"backlog":     1,
			"in_progress": 2,
			"review":      0,
			"completed":   3,
		},
	}
	frame, err := json.Marshal(map[string]any{
		"type": "project_counts",
		"data": counts,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	msg := assertFlatJSON(t, frame, "type", "data", "project_counts")
	dataMap := msg["data"].(map[string]any)
	projCounts := dataMap["test-project"].(map[string]any)
	if projCounts["backlog"].(float64) != 1 {
		t.Fatalf("expected backlog=1, got %v", projCounts["backlog"])
	}
}

// TestBranchWireFormat verifies project_branch is flat JSON.
func TestBranchWireFormat(t *testing.T) {
	frame, err := json.Marshal(map[string]any{
		"type":   "project_branch",
		"branch": "main",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	msg := assertFlatJSON(t, frame, "type", "", "project_branch")
	if msg["branch"] != "main" {
		t.Fatalf("expected branch=main, got %v", msg["branch"])
	}
}

// TestBoardRefreshWireFormat verifies board_refresh is flat JSON.
func TestBoardRefreshWireFormat(t *testing.T) {
	frame, err := json.Marshal(map[string]string{"type": "board_refresh"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	msg := assertFlatJSON(t, frame, "type", "", "board_refresh")
	_ = msg
}

// TestTaskDeletedWireFormat verifies task_deleted is flat JSON.
func TestTaskDeletedWireFormat(t *testing.T) {
	frame, err := json.Marshal(map[string]any{
		"type":    "task_deleted",
		"task_id": "test-task",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	msg := assertFlatJSON(t, frame, "type", "", "task_deleted")
	if msg["task_id"] != "test-task" {
		t.Fatalf("expected task_id=test-task, got %v", msg["task_id"])
	}
}

// TestOldStyleBroken verifies that the old WSMessage-wrapped approach
// placed task inside msg.data instead of msg.task (regression reference).
func TestOldStyleBroken(t *testing.T) {
	// This is what the OLD code did: wrap in {Type, Data: json.Marshal(payload)}.
	// The envelope is declared here on purpose — production sends flat frames,
	// so this shape exists only to pin down the regression it guards against.
	payload, _ := json.Marshal(map[string]any{
		"type": "task_updated",
		"task": map[string]string{"id": "test-task"},
	})
	oldStyle := struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}{Type: "task_updated", Data: payload}
	frame, _ := json.Marshal(oldStyle)

	// The old style produces: {"type":"task_updated","data":{"type":"task_updated","task":{...}}}
	// Client checks msg.task which is undefined (task is inside msg.data)
	var msg map[string]any
	json.Unmarshal(frame, &msg)

	// Old style: msg.task is undefined, task is at msg.data.task
	if _, ok := msg["task"]; ok {
		t.Fatal("old-style should NOT have top-level 'task' field")
	}
	data, ok := msg["data"].(map[string]any)
	if !ok {
		t.Fatal("old-style should have 'data' as object")
	}
	if _, ok := data["task"]; !ok {
		t.Fatal("old-style should have task nested inside data")
	}
	// This confirms the old approach was broken: client expected msg.task but got msg.data.task
}

// TestClientParseTaskUpdated verifies the client can parse a task_updated frame.
func TestClientParseTaskUpdated(t *testing.T) {
	frame, _ := json.Marshal(map[string]any{
		"type": "task_updated",
		"task": map[string]any{
			"id":         "test-task",
			"title":      "Test",
			"status":     "backlog",
			"project_id": "test-project",
		},
	})

	// Simulate client-side parsing (matches frontend api.ts logic)
	var msg map[string]any
	if err := json.Unmarshal(frame, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Client checks: if (msg.type === 'task_updated') callbacks.onTaskUpdated(msg.task)
	if msg["type"] != "task_updated" {
		t.Fatalf("expected type=task_updated, got %v", msg["type"])
	}
	task, ok := msg["task"]
	if !ok {
		t.Fatal("client expects msg.task to exist")
	}
	taskMap, ok := task.(map[string]any)
	if !ok {
		t.Fatalf("client expects msg.task to be an object, got %T", task)
	}
	if taskMap["id"] != "test-task" {
		t.Fatalf("client expects msg.task.id=test-task, got %v", taskMap["id"])
	}
}

// TestClientParseTaskEvent verifies the client can parse a task_event frame.
func TestClientParseTaskEvent(t *testing.T) {
	frame, _ := json.Marshal(map[string]any{
		"type":    "task_event",
		"task_id": "test-task",
		"event":   "token",
		"data":    map[string]string{"text": "hello"},
	})

	// Simulate client-side parsing (matches frontend api.ts logic)
	var msg map[string]any
	if err := json.Unmarshal(frame, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Client checks: if (msg.type === 'task_event') callbacks.onTaskEvent(msg.task_id, {type: msg.event, data: msg.data})
	if msg["type"] != "task_event" {
		t.Fatalf("expected type=task_event, got %v", msg["type"])
	}
	if msg["task_id"] != "test-task" {
		t.Fatalf("client expects msg.task_id=test-task, got %v", msg["task_id"])
	}
	if msg["event"] != "token" {
		t.Fatalf("client expects msg.event=token, got %v", msg["event"])
	}
	dataMap, ok := msg["data"].(map[string]any)
	if !ok {
		t.Fatalf("client expects msg.data to be an object, got %T", msg["data"])
	}
	if dataMap["text"] != "hello" {
		t.Fatalf("client expects msg.data.text=hello, got %v", dataMap["text"])
	}
}

// --- Live WebSocket subscription helpers ---
//
// These drive the real protocol (HandleStream on a throwaway server) so the
// production code registers the client, applies the subscription and performs
// the delivery — the test never reaches into AppStreamManager's maps.

// frameStream pumps every frame a client receives into a channel. Reading with
// a per-call context deadline is NOT an option here: coder/websocket closes the
// connection when a read's context expires, which would silently disconnect the
// client between assertions.
type frameStream struct {
	conn   *websocket.Conn
	ctx    context.Context
	frames chan map[string]any
}

// dialStream serves asm on a throwaway server, connects a real client and
// starts pumping its frames. Everything is torn down with the test.
func dialStream(t *testing.T, asm *AppStreamManager) *frameStream {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(asm.HandleStream))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial stream: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })

	fs := &frameStream{conn: conn, ctx: ctx, frames: make(chan map[string]any, 1024)}
	go func() {
		defer close(fs.frames)
		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var frame map[string]any
			if json.Unmarshal(raw, &frame) == nil {
				fs.frames <- frame
			}
		}
	}()
	return fs
}

// sendSubscription sends one subscribe_/unsubscribe_ frame over the real wire
// format. An empty taskID means the project-level variant.
func (fs *frameStream) sendSubscription(t *testing.T, action, projectID, taskID string) {
	t.Helper()
	body := map[string]any{"type": action, "data": map[string]string{"project_id": projectID, "task_id": taskID}}
	frame, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal %s: %v", action, err)
	}
	if err := fs.conn.Write(fs.ctx, websocket.MessageText, frame); err != nil {
		t.Fatalf("write %s: %v", action, err)
	}
}

// await blocks until want frames matching pred arrive, failing after window.
// Reads are ordered per connection, so anything seen here was written before
// what a later assertion on the same connection looks for.
func (fs *frameStream) await(t *testing.T, window time.Duration, pred func(map[string]any) bool, want int, what string) {
	t.Helper()
	if got := fs.collect(window, pred, want); got != want {
		t.Fatalf("%s: got %d frames within %v, want %d", what, got, window, want)
	}
}

// quietWindow fails if any frame matching pred arrives within window — the
// negative half of a delivery assertion. Callers must have proven the client is
// live beforehand, or "nothing arrived" proves nothing.
func (fs *frameStream) quietWindow(t *testing.T, window time.Duration, pred func(map[string]any) bool, what string) {
	t.Helper()
	if got := fs.collect(window, pred, 1); got != 0 {
		t.Fatalf("%s: got %d frames within %v, want none", what, got, window)
	}
}

// collect counts matching frames, stopping early at stopAt or after window.
func (fs *frameStream) collect(window time.Duration, pred func(map[string]any) bool, stopAt int) int {
	deadline := time.NewTimer(window)
	defer deadline.Stop()
	hits := 0
	for {
		select {
		case frame, ok := <-fs.frames:
			if !ok {
				return hits // connection closed
			}
			if pred(frame) {
				hits++
				if hits >= stopAt {
					return hits
				}
			}
		case <-deadline.C:
			return hits
		}
	}
}

// flushSubscriptions makes the server acknowledge everything this client has
// sent so far: HandleStream's read loop is sequential, so a probe subscription
// whose replay we observe proves every earlier frame from this connection (an
// unsubscribe, say) has already been applied. Without it a negative assertion
// would race the server's read loop.
func (fs *frameStream) flushSubscriptions(t *testing.T, projectID, probeTaskID string) {
	t.Helper()
	fs.sendSubscription(t, "subscribe_task", projectID, probeTaskID)
	fs.await(t, 5*time.Second, isTaskEventFor(probeTaskID, "warm-"+probeTaskID), 1, "subscription flush")
}

// isTaskEventFor reports a frame predicate for a task_event carrying text.
func isTaskEventFor(taskID, text string) func(map[string]any) bool {
	return func(frame map[string]any) bool {
		if frame["type"] != "task_event" || frame["task_id"] != taskID {
			return false
		}
		data, _ := frame["data"].(map[string]any)
		return data["text"] == text
	}
}

// TestTaskEventDelivery verifies task event delivery is gated by the task
// subscription: project-only and other-task subscribers never see it, and a
// task subscriber stops seeing it exactly when its subscription count drops to
// zero (multi-tab).
func TestTaskEventDelivery(t *testing.T) {
	_, s, _, wm, _ := setupDoneSnapshotTest(t)
	asm := NewAppStreamManager(s, wm)
	t.Cleanup(asm.Close)

	const projectID, taskID, probeTask = "proj-1", "task-1", "other-task"

	// Warm-up frames: buffered now (nobody is subscribed), replayed by the
	// production subscribe path, so their arrival proves the server processed
	// that client's subscribe_task.
	asm.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": "warm-" + taskID}})
	asm.SendTaskEvent(projectID, probeTask, types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": "warm-" + probeTask}})

	// A: subscribed to the task under test.
	a := dialStream(t, asm)
	a.sendSubscription(t, "subscribe_task", projectID, taskID)
	a.await(t, 5*time.Second, isTaskEventFor(taskID, "warm-"+taskID), 1, "task subscriber replay")

	// B: subscribed to the project only. Its liveness is proven by the
	// initial-data burst subscribe_project ends with (project_branch).
	b := dialStream(t, asm)
	b.sendSubscription(t, "subscribe_project", projectID, "")
	b.await(t, 5*time.Second, func(f map[string]any) bool { return f["type"] == "project_branch" }, 1, "project subscriber initial data")

	// C: subscribed to a different task in the same project.
	c := dialStream(t, asm)
	c.sendSubscription(t, "subscribe_task", projectID, probeTask)
	c.await(t, 5*time.Second, isTaskEventFor(probeTask, "warm-"+probeTask), 1, "other-task subscriber replay")

	// 1. Only the task subscriber receives the event.
	asm.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": "gated"}})
	a.await(t, 5*time.Second, isTaskEventFor(taskID, "gated"), 1, "task subscriber")
	b.quietWindow(t, time.Second, isTaskEventFor(taskID, "gated"), "project-only subscriber")
	c.quietWindow(t, time.Second, isTaskEventFor(taskID, "gated"), "other-task subscriber")

	// 2. Two tabs on one connection (subscribe_task twice): the first
	// unsubscribe drops the count to 1, so delivery must continue.
	a.sendSubscription(t, "subscribe_task", projectID, taskID)
	a.sendSubscription(t, "unsubscribe_task", projectID, taskID)
	a.flushSubscriptions(t, projectID, probeTask)
	asm.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": "still-subscribed"}})
	a.await(t, 5*time.Second, isTaskEventFor(taskID, "still-subscribed"), 1, "second tab still subscribed")

	// 3. The last unsubscribe removes the key, so delivery stops.
	a.sendSubscription(t, "unsubscribe_task", projectID, taskID)
	a.flushSubscriptions(t, projectID, probeTask)
	asm.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": "after-unsubscribe"}})
	a.quietWindow(t, time.Second, isTaskEventFor(taskID, "after-unsubscribe"), "unsubscribed client")
}

// TestSandboxStatsTaskEventPayload verifies the sandbox_stats task event
// serializes as flat JSON with the stats object under data.
func TestSandboxStatsTaskEventPayload(t *testing.T) {
	stats := &types.SandboxMemoryStats{
		Running:   true,
		CurrentMB: 412,
		LimitMB:   1024,
		PeakMB:    512,
		OOMKills:  1,
	}
	payload := TaskEventPayload("test-task", types.StreamEvent{Type: types.WSTypeSandboxStats, Data: stats})
	frame, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	msg := assertFlatJSON(t, frame, "type", "data", "task_event")
	if msg["task_id"] != "test-task" {
		t.Fatalf("expected task_id=test-task, got %v", msg["task_id"])
	}
	if msg["event"] != "sandbox_stats" {
		t.Fatalf("expected event=sandbox_stats, got %v", msg["event"])
	}
	data := msg["data"].(map[string]any)
	if data["running"] != true || data["memory_current_mb"] != float64(412) ||
		data["memory_limit_mb"] != float64(1024) || data["memory_peak_mb"] != float64(512) ||
		data["oom_kills"] != float64(1) {
		t.Fatalf("unexpected stats payload: %v", data)
	}
}

// TestLatestSandboxStatsFrame verifies the on-subscribe latest-stats seam:
// nil without a provider, nil when the provider reports unknown stats, and a
// well-formed frame otherwise.
func TestLatestSandboxStatsFrame(t *testing.T) {
	asm := NewAppStreamManager(nil, nil)

	if frame := asm.latestSandboxStatsFrame("p1", "t1"); frame != nil {
		t.Fatalf("expected nil frame without provider, got %s", frame)
	}

	asm.SetSandboxStatsProvider(func(projectID, taskID string) *types.SandboxMemoryStats {
		return nil
	})
	if frame := asm.latestSandboxStatsFrame("p1", "t1"); frame != nil {
		t.Fatalf("expected nil frame when provider returns nil, got %s", frame)
	}

	gotProject, gotTask := "", ""
	asm.SetSandboxStatsProvider(func(projectID, taskID string) *types.SandboxMemoryStats {
		gotProject, gotTask = projectID, taskID
		return &types.SandboxMemoryStats{Running: true, CurrentMB: 100, LimitMB: 512}
	})
	frame := asm.latestSandboxStatsFrame("proj-x", "task-y")
	if frame == nil {
		t.Fatal("expected frame from provider, got nil")
	}
	if gotProject != "proj-x" || gotTask != "task-y" {
		t.Fatalf("provider called with (%q, %q), want (\"proj-x\", \"task-y\")", gotProject, gotTask)
	}
	msg := assertFlatJSON(t, frame, "type", "data", "task_event")
	if msg["event"] != "sandbox_stats" {
		t.Fatalf("expected event=sandbox_stats, got %v", msg["event"])
	}
	data := msg["data"].(map[string]any)
	if data["running"] != true || data["memory_current_mb"] != float64(100) || data["memory_limit_mb"] != float64(512) {
		t.Fatalf("unexpected stats payload: %v", data)
	}
}

// TestTaskBufferClearedOnTaskDeleted verifies that deleting a task drops its
// replay buffer, so a late subscriber can never be replayed the deleted
// task's frames (deletion bypasses the terminal status/complete events that
// normally clear the buffer).
func TestTaskBufferClearedOnTaskDeleted(t *testing.T) {
	asm := NewAppStreamManager(nil, nil)

	// Accumulate a few non-terminal frames in the task buffer.
	for i := 0; i < 3; i++ {
		asm.SendTaskEvent("proj-1", "task-1", types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": "x"}})
	}
	if got := len(asm.getTaskBuffer("proj-1:task-1")); got != 3 {
		t.Fatalf("buffer before delete = %d frames, want 3", got)
	}

	asm.BroadcastTaskDeleted("proj-1", "task-1")
	if got := len(asm.getTaskBuffer("proj-1:task-1")); got != 0 {
		t.Fatalf("buffer after delete = %d frames, want 0", got)
	}

	// Other tasks' buffers are untouched.
	asm.SendTaskEvent("proj-1", "task-2", types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": "y"}})
	if got := len(asm.getTaskBuffer("proj-1:task-2")); got != 1 {
		t.Fatalf("other task buffer = %d frames, want 1", got)
	}
}
