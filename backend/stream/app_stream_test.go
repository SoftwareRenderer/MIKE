package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mike/backend/types"

	"github.com/coder/websocket"
)

// --- Live WebSocket frame-shape helpers ---
//
// Wire-format assertions belong here, on frames the production manager put on the
// wire. A test that marshals its own literal map and unmarshals it again only proves
// encoding/json works, so it stays green no matter what the server starts sending.

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

// serveStream serves asm on a throwaway server for the rest of the test and returns its
// ws URL, so a test can put several real clients on one manager.
func serveStream(t *testing.T, asm *AppStreamManager) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(asm.HandleStream))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// dialStream serves asm on a throwaway server, connects a real client and
// starts pumping its frames. Everything is torn down with the test.
func dialStream(t *testing.T, asm *AppStreamManager) *frameStream {
	t.Helper()
	return dialFrameStream(t, serveStream(t, asm))
}

// dialFrameStream connects a real client to the stream listening at wsURL and starts
// pumping its frames.
func dialFrameStream(t *testing.T, wsURL string) *frameStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial stream: %v", err)
	}
	// A test that fills a socket does it with frames larger than the library's 32 KiB default,
	// which the reader would answer by closing the connection it is meant to be watching.
	conn.SetReadLimit(-1)
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

// awaitFrame returns the first frame matching pred, failing after window. Use it when
// the assertion is about the frame's contents; await is enough for delivery counts.
func (fs *frameStream) awaitFrame(t *testing.T, window time.Duration, pred func(map[string]any) bool, what string) map[string]any {
	t.Helper()
	deadline := time.NewTimer(window)
	defer deadline.Stop()
	for {
		select {
		case frame, ok := <-fs.frames:
			if !ok {
				t.Fatalf("%s: connection closed", what)
			}
			if pred(frame) {
				return frame
			}
		case <-deadline.C:
			t.Fatalf("%s: nothing matching within %v", what, window)
		}
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

// tally drains this connection's frames for window and counts how many matched each predicate,
// keeping what the other predicates still need. A caller that awaits one frame first cannot then
// claim nothing more arrived: the await consumed whatever came before it.
func (fs *frameStream) tally(window time.Duration, preds ...func(map[string]any) bool) []int {
	deadline := time.NewTimer(window)
	defer deadline.Stop()
	counts := make([]int, len(preds))
	for {
		select {
		case frame, ok := <-fs.frames:
			if !ok {
				return counts // connection closed
			}
			for i, pred := range preds {
				if pred(frame) {
					counts[i]++
				}
			}
		case <-deadline.C:
			return counts
		}
	}
}

// flushSubscriptions makes the server acknowledge everything this client has sent so far:
// HandleStream's read loop is sequential, so a probe subscription whose replay we observe
// proves every earlier frame from this connection (an unsubscribe, say) has already been
// applied. Without it a negative assertion would race the server's read loop.
//
// Every probe takes a task key of its own, warmed here, because a connection replays nothing
// for a subscription it already holds, and a key shared with another connection's probe would
// deliver that connection's warm frame as a live event.
func (fs *frameStream) flushSubscriptions(t *testing.T, asm *AppStreamManager, projectID string) {
	t.Helper()
	taskID := fmt.Sprintf("flush-probe-%d", flushProbeSeq.Add(1))
	warmProbe(t, asm, projectID, taskID)
	fs.sendSubscription(t, "subscribe_task", projectID, taskID)
	fs.await(t, 5*time.Second, isTaskEventFor(taskID, "warm-"+taskID), 1, "subscription flush")
}

// flushProbeSeq numbers the flushSubscriptions probes, so no two of them share a task key.
var flushProbeSeq atomic.Uint64

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

// isType reports a frame predicate matching one frame type.
func isType(typ string) func(map[string]any) bool {
	return func(frame map[string]any) bool { return frame["type"] == typ }
}

// decodeFrame unmarshals a frame a production path produced and checks the one thing
// the client contract depends on: the type sits at the top level. Payload shape is
// then checked per frame with requireField.
func decodeFrame(t *testing.T, frame []byte) map[string]any {
	t.Helper()
	var msg map[string]any
	if err := json.Unmarshal(frame, &msg); err != nil {
		t.Fatalf("unmarshal frame: %v: %s", err, frame)
	}
	if msg["type"] == nil {
		t.Fatalf("frame has no top-level type: %s", frame)
	}
	return msg
}

// requireField fails unless field holds a JSON object. The client reads msg.data and
// msg.task as objects, which is exactly what a double-encoded payload (a JSON string
// in place of the object) breaks — the original bug these frames are tested against.
func requireField(t *testing.T, frame map[string]any, field string) map[string]any {
	t.Helper()
	obj, ok := frame[field].(map[string]any)
	if !ok {
		t.Fatalf("client reads msg.%s as an object, got %T: %v", field, frame[field], frame[field])
	}
	return obj
}

// TestBroadcastFramesAreFlat asserts the shape of the frames the manager actually
// writes to a subscribed client. Every frame carries its type at the top level and its
// payload as a JSON object (or flat sibling fields) — never an envelope whose data was
// marshalled a second time, which is what made the browser's msg.task / msg.task_id
// lookups come back undefined.
func TestBroadcastFramesAreFlat(t *testing.T) {
	_, s, _, wm, _ := setupDoneSnapshotTest(t)
	asm := NewAppStreamManager(s, wm)
	t.Cleanup(asm.Close)

	// These are setupDoneSnapshotTest's own IDs: the initial-data burst (used below as
	// the subscription sync point) resolves a real branch, so the project has to exist.
	const projectID, taskID = "proj-1", "task-1"

	c := dialStream(t, asm)
	c.sendSubscription(t, "subscribe_project", projectID, "")
	// subscribe_project ends with an initial-data burst whose last frame is
	// project_branch; reading up to it proves the server applied this subscription
	// (frames arrive in order per connection), so every broadcast below is delivered
	// to a subscriber that already exists — and cannot race the server's read loop.
	c.await(t, 5*time.Second, isType("project_branch"), 1, "project subscriber initial data")

	t.Run("board_refresh", func(t *testing.T) {
		asm.Broadcast(projectID)
		frame := c.awaitFrame(t, 5*time.Second, isType("board_refresh"), "board_refresh frame")
		if frame["data"] != nil {
			t.Errorf("board_refresh carries no payload, got data=%v", frame["data"])
		}
	})

	t.Run("task_updated", func(t *testing.T) {
		asm.BroadcastTaskUpdated(projectID, &types.Task{ID: taskID, ProjectID: projectID, Title: "Flat", Status: types.StatusBacklog})
		frame := c.awaitFrame(t, 5*time.Second, isType("task_updated"), "task_updated frame")
		task := requireField(t, frame, "task")
		if task["id"] != taskID {
			t.Errorf("msg.task.id = %v, want %s", task["id"], taskID)
		}
	})

	t.Run("project_counts", func(t *testing.T) {
		asm.BroadcastProjectCounts()
		frame := c.awaitFrame(t, 5*time.Second, isType("project_counts"), "project_counts frame")
		requireField(t, frame, "data")
	})

	t.Run("commit_status", func(t *testing.T) {
		asm.BroadcastCommitStatus(projectID, taskID, "committed")
		frame := c.awaitFrame(t, 5*time.Second, isType("commit_status"), "commit_status frame")
		if frame["task_id"] != taskID || frame["status"] != "committed" {
			t.Errorf("commit_status = task_id:%v status:%v, want %s/committed", frame["task_id"], frame["status"], taskID)
		}
	})

	t.Run("project_branch", func(t *testing.T) {
		asm.BroadcastBranch(projectID, "task/flat")
		frame := c.awaitFrame(t, 5*time.Second, isType("project_branch"), "project_branch frame")
		if frame["branch"] != "task/flat" {
			t.Errorf("msg.branch = %v, want task/flat", frame["branch"])
		}
	})

	t.Run("task_deleted", func(t *testing.T) {
		asm.BroadcastTaskDeleted(projectID, taskID)
		frame := c.awaitFrame(t, 5*time.Second, isType("task_deleted"), "task_deleted frame")
		if frame["task_id"] != taskID {
			t.Errorf("msg.task_id = %v, want %s", frame["task_id"], taskID)
		}
	})
}

// TestTaskEventDelivery verifies task event delivery is gated by the task
// subscription: project-only and other-task subscribers never see it, a task subscriber stops
// seeing it when the one unsubscribe it sent arrives, and sees it again when it re-subscribes.
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

	// 2. A task key is held once, so the single unsubscribe the browser sends for it ends
	// delivery even when the subscribe frame was sent twice.
	a.sendSubscription(t, "subscribe_task", projectID, taskID)
	a.sendSubscription(t, "unsubscribe_task", projectID, taskID)
	a.flushSubscriptions(t, asm, projectID)
	asm.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": "after-one-unsubscribe"}})
	a.quietWindow(t, time.Second, isTaskEventFor(taskID, "after-one-unsubscribe"), "delivery after one unsubscribe_task")

	// 3. Re-subscribing takes the key again, so delivery resumes.
	a.sendSubscription(t, "subscribe_task", projectID, taskID)
	a.flushSubscriptions(t, asm, projectID)
	asm.SendTaskEvent(projectID, taskID, types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": "resubscribed"}})
	a.await(t, 5*time.Second, isTaskEventFor(taskID, "resubscribed"), 1, "delivery after re-subscribing")
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

	msg := decodeFrame(t, frame)
	// What the client reads: msg.type / msg.task_id / msg.event beside msg.data.
	if msg["type"] != "task_event" || msg["task_id"] != "test-task" || msg["event"] != "sandbox_stats" {
		t.Fatalf("frame = %s, want a flat task_event for test-task", frame)
	}
	data := requireField(t, msg, "data")
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
		t.Fatalf("expected nil frame without provider, got %v", frame)
	}

	asm.SetSandboxStatsProvider(func(projectID, taskID string) *types.SandboxMemoryStats {
		return nil
	})
	if frame := asm.latestSandboxStatsFrame("p1", "t1"); frame != nil {
		t.Fatalf("expected nil frame when provider returns nil, got %v", frame)
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
	msg := decodeFrame(t, frame.data)
	if msg["event"] != "sandbox_stats" {
		t.Fatalf("expected event=sandbox_stats, got %v", msg["event"])
	}
	data := requireField(t, msg, "data")
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
	if got := len(replayedFrames(asm, "proj-1:task-1")); got != 3 {
		t.Fatalf("buffer before delete = %d frames, want 3", got)
	}

	asm.BroadcastTaskDeleted("proj-1", "task-1")
	if got := len(replayedFrames(asm, "proj-1:task-1")); got != 0 {
		t.Fatalf("buffer after delete = %d frames, want 0", got)
	}

	// Other tasks' buffers are untouched.
	asm.SendTaskEvent("proj-1", "task-2", types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": "y"}})
	if got := len(replayedFrames(asm, "proj-1:task-2")); got != 1 {
		t.Fatalf("other task buffer = %d frames, want 1", got)
	}
}

// TestTaskBufferClearedOnTerminalStatus: the buffer survives only while the turn can still
// write — a frame left behind by an ended turn is replayed to the next subscriber.
func TestTaskBufferClearedOnTerminalStatus(t *testing.T) {
	for _, state := range []string{"paused", "idle", "completed", "failed", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			asm := NewAppStreamManager(nil, nil)
			asm.SendTaskEvent("proj-1", "task-1", types.StreamEvent{Type: types.WSTypeToolCall, Data: map[string]string{"tool_id": "call-1", "tool_name": "bash"}})
			if got := len(replayedFrames(asm, "proj-1:task-1")); got != 1 {
				t.Fatalf("buffer before the status frame = %d frames, want 1", got)
			}

			asm.SendTaskEvent("proj-1", "task-1", types.TurnStatusEvent(state, "still going"))

			if got := len(replayedFrames(asm, "proj-1:task-1")); got != 0 {
				t.Fatalf("buffer after state %q = %d frames, want 0", state, got)
			}
		})
	}

	for _, state := range []string{"running", "pulling_image", "building_image", "starting_container"} {
		t.Run(state+"_keeps", func(t *testing.T) {
			asm := NewAppStreamManager(nil, nil)
			asm.SendTaskEvent("proj-1", "task-1", types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": "x"}})
			asm.SendTaskEvent("proj-1", "task-1", types.TurnStatusEvent(state, "still going"))
			if got := len(replayedFrames(asm, "proj-1:task-1")); got != 2 {
				t.Fatalf("buffer after state %q = %d frames, want 2", state, got)
			}
		})
	}
}

// TestToolCallReplayedToALateSubscriberCarriesWholeArgs: the live path sends a tool call's whole
// args and then only its growth, and the replay owes each call one frame — so that frame has to
// say everything the frames it joins said. A replay handing over only the last delta renders a
// tool call whose input starts mid-JSON.
func TestToolCallReplayedToALateSubscriberCarriesWholeArgs(t *testing.T) {
	asm := NewAppStreamManager(nil, nil)
	whole := `{"path":"a.txt"`
	first := `,"content":"hi"`
	second := `}`

	asm.SendTaskEvent("proj-1", "task-1", toolCall(map[string]any{"tool_id": "call_1", "tool_name": "write_file", "raw_args": whole}))
	asm.SendTaskEvent("proj-1", "task-1", toolCall(map[string]any{"tool_id": "call_1", "tool_name": "write_file", "raw_args_delta": first}))
	asm.SendTaskEvent("proj-1", "task-1", toolCall(map[string]any{"tool_id": "call_1", "tool_name": "write_file", "raw_args_delta": second}))

	buf := replayedFrames(asm, "proj-1:task-1")
	if len(buf) != 1 {
		t.Fatalf("replay holds %d frames for one tool call, want 1", len(buf))
	}
	data := toolCallArgs(buf[0])
	if got := data["raw_args"]; got != whole+first+second {
		t.Fatalf("replayed tool call carries raw_args %v, want the whole input %q", got, whole+first+second)
	}
	if _, still := data["raw_args_delta"]; still {
		t.Fatalf("replayed tool call still carries a delta its subscriber has nothing to append to: %v", data["raw_args_delta"])
	}
	// The envelope is re-marshalled with it: a payload without its task_id routes nowhere.
	var envelope struct {
		Type   string `json:"type"`
		TaskID string `json:"task_id"`
		Event  string `json:"event"`
	}
	if err := json.Unmarshal(buf[0], &envelope); err != nil {
		t.Fatalf("replayed frame does not read as JSON: %v", err)
	}
	if envelope.Type != string(types.WSTypeTaskEvent) || envelope.TaskID != "task-1" || envelope.Event != string(types.WSTypeToolCall) {
		t.Fatalf("replayed envelope became %+v", envelope)
	}
}

// TestToolCallDeltaSurvivesAReplayWithNothingToJoinItTo: when the frame holding a call's start has
// already been trimmed from the buffer, the delta is what there is. Dropping it would hide the
// call entirely, and rewriting it would invent input the model never sent.
func TestToolCallDeltaSurvivesAReplayWithNothingToJoinItTo(t *testing.T) {
	asm := NewAppStreamManager(nil, nil)
	asm.SendTaskEvent("proj-1", "task-1", toolCall(map[string]any{"tool_id": "call_9", "tool_name": "write_file", "raw_args_delta": `"tail"`}))

	buf := replayedFrames(asm, "proj-1:task-1")
	if len(buf) != 1 {
		t.Fatalf("a lone delta left the replay with %d frames, want 1", len(buf))
	}
	data := toolCallArgs(buf[0])
	if data["raw_args_delta"] != `"tail"` {
		t.Fatalf("a lone delta was rewritten: %v", data)
	}
	if _, invented := data["raw_args"]; invented {
		t.Fatalf("a delta with no start became a whole input: %v", data["raw_args"])
	}
}

func tokenText(text string) types.StreamEvent {
	return types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": text}}
}

func toolCall(data map[string]any) types.StreamEvent {
	return types.StreamEvent{Type: types.WSTypeToolCall, Data: data}
}

// replayedFrames is what a subscriber of the task is owed: the buffer with one frame per streaming
// tool call. It is the two steps subscribeTask takes, so a test reading the buffer reads the replay.
func replayedFrames(asm *AppStreamManager, taskKey string) [][]byte {
	queued := foldToolCallFrames(asm.taskBufferFrames(taskKey))
	frames := make([][]byte, 0, len(queued))
	for _, frame := range queued {
		frames = append(frames, frame.data)
	}
	return frames
}
