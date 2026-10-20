package stream

// Who writes a connection, and what a client that stopped reading costs everyone else: the
// producer of a frame — an executor turn, a watcher, a broadcast — queues it and returns, so a
// browser that stopped reading may not set the pace for that turn, for the other subscribers of the
// same task, or for the pinger holding the same writeMu.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mike/backend/types"
	"mike/pkg/logging"

	"github.com/coder/websocket"
)

// tokenEvent builds a token frame of roughly the size a streaming turn writes.
func tokenEvent(text string) types.StreamEvent {
	return types.StreamEvent{Type: types.WSTypeToken, Data: map[string]string{"text": text}}
}

// subscribeSilentConn subscribes a connection that will never read. The write direction is its
// own, so the server applies the subscription while everything sent to that peer piles up unread.
func subscribeSilentConn(t *testing.T, conn *websocket.Conn, action, projectID, taskID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	body, err := json.Marshal(map[string]any{
		"type": action,
		"data": map[string]string{"project_id": projectID, "task_id": taskID},
	})
	if err != nil {
		t.Fatalf("marshal %s: %v", action, err)
	}
	if err := conn.Write(ctx, websocket.MessageText, body); err != nil {
		t.Fatalf("write %s: %v", action, err)
	}
}

// awaitTaskSub waits for the server to apply a task subscription. The peer is silent, so no frame
// of its own can prove it was understood.
func awaitTaskSub(t *testing.T, asm *AppStreamManager, client *clientState, projectID, taskID string) {
	t.Helper()
	key := projectID + ":" + taskID
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		asm.mu.RLock()
		_, subscribed := client.taskSub[key]
		asm.mu.RUnlock()
		if subscribed {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("task=%s was never subscribed on this connection", taskID)
		case <-time.After(time.Millisecond):
		}
	}
}

// TestStalledSubscriberDoesNotHoldTheTurnBroadcastingToIt: the caller of SendTaskEvent is an
// executor goroutine. One subscriber whose socket is full must not stop it, nor the other
// subscriber waiting behind that write.
func TestStalledSubscriberDoesNotHoldTheTurnBroadcastingToIt(t *testing.T) {
	asm := NewAppStreamManager(writeTestStore(t), nil)
	asm.writeTimeout = 5 * time.Second
	t.Cleanup(asm.Close)
	srv := httptest.NewServer(http.HandlerFunc(asm.HandleStream))
	t.Cleanup(srv.Close)

	const projectID, taskID = "proj-stall", "task-stall"
	stalled := dialSilent(t, srv.URL, "")
	stalledClient := clientAlone(t, asm)
	subscribeSilentConn(t, stalled, "subscribe_task", projectID, taskID)
	awaitTaskSub(t, asm, stalledClient, projectID, taskID)

	live := dialStream(t, asm)
	liveClient := clientOtherThan(t, asm, stalledClient)
	live.sendSubscription(t, "subscribe_task", projectID, taskID)
	awaitTaskSub(t, asm, liveClient, projectID, taskID)

	// Fill that peer's socket from the same path a turn uses, until its sender is inside a write.
	chunk := strings.Repeat("x", 64*1024)
	awaitStuckSender(t, stalledClient, func() {
		asm.SendTaskEvent(projectID, taskID, tokenEvent(chunk))
	})
	// The turn's next frame, sent by the goroutine that runs the turn, with a stalled subscriber
	// still inside the fan-out.
	started := time.Now()
	asm.SendTaskEvent(projectID, taskID, tokenEvent("after-the-stall"))
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("SendTaskEvent spent %v writing to a subscriber that stopped reading", elapsed)
	}
	live.awaitFrame(t, 5*time.Second, isTaskEventFor(taskID, "after-the-stall"),
		"the frame the live subscriber was waiting behind")
}

// TestReplayedFramesLandBeforeTheLiveFrameBehindThem: a task's replay and the live frame that
// follows it go to the same connection, which is the one place two delivery paths meet. The
// sandbox-stats frame rides at the end of the replay it extends.
func TestReplayedFramesLandBeforeTheLiveFrameBehindThem(t *testing.T) {
	asm := NewAppStreamManager(nil, nil)
	asm.SetSandboxStatsProvider(func(projectID, taskID string) *types.SandboxMemoryStats {
		return &types.SandboxMemoryStats{LimitMB: 512}
	})
	t.Cleanup(asm.Close)

	const projectID = "proj-replay"
	c := dialStream(t, asm)
	client := clientAlone(t, asm)

	for round := 0; round < 5; round++ {
		taskID := fmt.Sprintf("task-%d", round)
		for i := 0; i < 4; i++ {
			// Sent before the subscription exists, so these arrive as a replay.
			asm.SendTaskEvent(projectID, taskID, tokenEvent(fmt.Sprintf("warm-%d", i)))
		}
		c.sendSubscription(t, "subscribe_task", projectID, taskID)
		// Applied, so the frame below is a live one — and it may be queued while the replay of
		// this very task is still going out.
		awaitTaskSub(t, asm, client, projectID, taskID)
		asm.SendTaskEvent(projectID, taskID, tokenEvent("live"))

		want := []string{"warm", "warm", "warm", "warm", "stats", "live"}
		got := collectTaskFrames(t, c, taskID)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("round %d delivered %v, want %v — a replay must land ahead of the live frame queued behind it", round, got, want)
		}
	}
}

// collectTaskFrames reads this connection until its `live` frame arrives, naming each of the
// task's frames by what it was: replayed, the stats frame, or live.
func collectTaskFrames(t *testing.T, c *frameStream, taskID string) []string {
	t.Helper()
	var kinds []string
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		var frame map[string]any
		select {
		case f, ok := <-c.frames:
			if !ok {
				t.Fatalf("connection closed after %v", kinds)
			}
			frame = f
		case <-deadline.C:
			t.Fatalf("no live frame for task=%s after %v", taskID, kinds)
		}
		if frame["type"] != "task_event" || frame["task_id"] != taskID {
			continue
		}
		data, _ := frame["data"].(map[string]any)
		switch {
		case frame["event"] == string(types.WSTypeSandboxStats):
			kinds = append(kinds, "stats")
		case data["text"] == "live":
			return append(kinds, "live")
		default:
			kinds = append(kinds, "warm")
		}
	}
}

// TestSendQueueHoldsItsFrameAndByteBoundsByDroppingTheOldest: the queue is bounded twice, and the
// way out of both bounds is the same — a client that stopped reading is caught up on the tail.
func TestSendQueueHoldsItsFrameAndByteBoundsByDroppingTheOldest(t *testing.T) {
	const size = 1000
	q := newSendQueue(10, 5*size)
	marked := func(i int) []byte {
		frame := bytes.Repeat([]byte("x"), size)
		frame[0] = byte('A' + i)
		return frame
	}
	for i := 0; i < 30; i++ {
		q.push(marked(i), "board_refresh")
	}

	q.mu.Lock()
	held, frames := q.bytes, len(q.live)
	q.mu.Unlock()
	if held > 5*size {
		t.Fatalf("the queue holds %d bytes, over its %d bound", held, 5*size)
	}
	if frames > 10 {
		t.Fatalf("the queue holds %d frames, over its 10 bound", frames)
	}

	var survivors []byte
	for {
		frame, ok := q.pop()
		if !ok {
			break
		}
		survivors = append(survivors, frame.data[0])
	}
	if len(survivors) == 0 || len(survivors) >= 30 {
		t.Fatalf("%d frames survived 30 pushes into a bounded queue", len(survivors))
	}
	if survivors[len(survivors)-1] != 'A'+29 {
		t.Fatalf("the newest frame did not survive; the last one queued was %q", survivors[len(survivors)-1])
	}
	if first, last := survivors[0], survivors[len(survivors)-1]; first >= last {
		t.Fatalf("dropping the oldest left %q before %q", first, last)
	}
	if missed := q.droppedFrames(); missed != 30-len(survivors) {
		t.Fatalf("%d frames were dropped, but the connection is marked as having missed %d", 30-len(survivors), missed)
	}
}

// TestSendQueueKeepsOneOversizedFrameUntilTheNewerOnesNeedTheRoom: one frame larger than the byte
// bound still waits — the queue will not be the thing that loses a turn's output — and once newer
// frames arrive it is the oldest one that goes.
func TestSendQueueKeepsOneOversizedFrameUntilTheNewerOnesNeedTheRoom(t *testing.T) {
	q := newSendQueue(10, 100)
	q.push(bytes.Repeat([]byte("y"), 1000), "board_refresh")
	if frame, ok := q.pop(); !ok || len(frame.data) != 1000 {
		t.Fatalf("the queue refused to hold a frame over its byte bound: ok=%v len=%d", ok, len(frame.data))
	}

	q.push(bytes.Repeat([]byte("y"), 1000), "board_refresh")
	q.push([]byte(`{"type":"board_refresh"}`), "board_refresh")
	q.mu.Lock()
	frames, held := len(q.live), q.bytes
	q.mu.Unlock()
	if held > 100 {
		t.Fatalf("%d frame(s) holding %d bytes left in a queue bounded at 100 bytes", frames, held)
	}
	if frame, ok := q.pop(); !ok || len(frame.data) != 24 {
		t.Fatalf("dropping the oldest left a %d-byte frame, not the newest one", len(frame.data))
	}
	if missed := q.droppedFrames(); missed != 1 {
		t.Fatalf("the oversized frame was dropped, and the connection is marked as having missed %d", missed)
	}
}

// TestReplayGoesAheadOfWhatIsAlreadyQueued: a replay lands ahead of the live frames queued before
// it, replays keep the order their subscriptions were made in, and the frames an overflowing
// replay cannot keep are its own oldest ones.
func TestReplayGoesAheadOfWhatIsAlreadyQueued(t *testing.T) {
	q := newSendQueue(10, 1<<20)
	q.push([]byte("live-1"), "token")
	q.push([]byte("live-2"), "token")
	q.pushReplay([]outboundFrame{{data: []byte("replay-1"), bucket: "token"}, {data: []byte("replay-2"), bucket: "token"}})
	// A second task subscribed while the first replay is still queued: its own replay goes behind
	// that one, not in front of it.
	q.pushReplay([]outboundFrame{{data: []byte("replay-3"), bucket: "token"}})

	var order []string
	for {
		frame, ok := q.pop()
		if !ok {
			break
		}
		order = append(order, string(frame.data))
	}
	want := []string{"replay-1", "replay-2", "replay-3", "live-1", "live-2"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("queue drained %v, want %v", order, want)
	}

	q.pushReplay([]outboundFrame{{data: []byte("replay-a"), bucket: "token"}, {data: []byte("replay-b"), bucket: "token"}, {data: []byte("replay-c"), bucket: "token"}})
	q.frame = 2
	q.push([]byte("live-3"), "token")
	drained := ""
	for {
		frame, ok := q.pop()
		if !ok {
			break
		}
		drained += string(frame.data) + " "
	}
	if drained != "replay-c live-3 " {
		t.Fatalf("an overflowing replay drained %q, keeping the tail of the replay and the newest live frame", drained)
	}
}

// TestStatsFrameOnAFullReplayKeepsTheConnectionOpen: a replay buffer at the queue's whole bound, plus the
// sandbox_stats frame every subscriber is owed — the smallest replay that put its own queue over the bound,
// dropped the oldest frame, and ended the connection that asked for it.
func TestStatsFrameOnAFullReplayKeepsTheConnectionOpen(t *testing.T) {
	asm := NewAppStreamManager(nil, nil)
	t.Cleanup(asm.Close)
	// A task's replay buffer and a connection's queue share one bound.
	asm.taskBufSize = 40
	asm.queueFrames = 40
	asm.SetSandboxStatsProvider(func(_, _ string) *types.SandboxMemoryStats {
		return &types.SandboxMemoryStats{CurrentMB: 120, LimitMB: 512}
	})

	const projectID, taskID = "proj-full", "task-full"
	for i := 0; i < 50; i++ {
		asm.SendTaskEvent(projectID, taskID, tokenEvent(fmt.Sprintf("warm-%d", i)))
	}
	asm.taskBufMu.Lock()
	held := len(asm.taskBuffers[projectID+":"+taskID])
	asm.taskBufMu.Unlock()
	if held != 40 {
		t.Fatalf("the buffer holds %d frames, want the queue's whole bound of 40", held)
	}

	logs, restore := logging.TestSink()
	defer restore()

	c := dialStream(t, asm)
	c.sendSubscription(t, "subscribe_task", projectID, taskID)

	saw := map[string]bool{}
	deadline := time.After(2 * time.Second)
drain:
	for {
		select {
		case frame, ok := <-c.frames:
			if !ok {
				t.Fatalf("the connection that subscribed to a full task buffer was ended, having received %d frames", len(saw))
			}
			if data, _ := frame["data"].(map[string]any); data != nil {
				if text, _ := data["text"].(string); text != "" {
					saw[text] = true
				}
			}
		case <-deadline:
			break drain
		}
	}
	if !saw["warm-49"] {
		t.Error("the replay did not carry the newest frame of the buffer")
	}
	if saw["warm-0"] {
		t.Error("the replay carried the buffer's oldest frame, so it was trimmed at the wrong end")
	}
	if !strings.Contains(logs.String(), "replay trimmed") {
		t.Errorf("a truncated replay went unreported: %s", logs.String())
	}
}

// TestAConnectionThatMissedFramesIsEndedSoTheClientResyncs: the app's one resync belongs to a new
// connection — onConnected(true) drives silentRefresh and TaskChat.loadHistory — so a connection
// with a hole in it is ended rather than left streaming a view that will never be right.
func TestAConnectionThatMissedFramesIsEndedSoTheClientResyncs(t *testing.T) {
	asm := NewAppStreamManager(nil, nil)
	asm.queueFrames = 2
	t.Cleanup(asm.Close)

	logs, restore := logging.TestSink()
	defer restore()

	c := dialStream(t, asm)
	c.sendSubscription(t, "subscribe_task", "proj-drop", "task-drop")
	// The flood has to reach a live subscription. A frame that arrives before the server applies
	// the subscription is buffered for its replay instead, and a replay trimmed to the connection's
	// budget is the other decided behaviour of this pair: reported, connection kept
	// (TestStatsFrameOnAFullReplayKeepsTheConnectionOpen). Flooding that path ends nothing.
	awaitTaskSub(t, asm, clientAlone(t, asm), "proj-drop", "task-drop")
	for i := 0; i < 200; i++ {
		asm.SendTaskEvent("proj-drop", "task-drop", tokenEvent(fmt.Sprintf("frame-%d", i)))
	}

	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case _, ok := <-c.frames:
			if !ok {
				if logs := logs.String(); !strings.Contains(logs, "component=stream") || !strings.Contains(logs, "resyncs") {
					t.Fatalf("the connection ended without saying why: %s", logs)
				}
				return
			}
		case <-deadline.C:
			t.Fatal("a connection that missed frames is still streaming a gap")
		}
	}
}

// TestTurnStatusFrameCarriesTheBytesItAlwaysDid: the payload is a type now, and the wire is not.
func TestTurnStatusFrameCarriesTheBytesItAlwaysDid(t *testing.T) {
	typed, err := json.Marshal(TaskEventPayload("task-1", types.TurnStatusEvent("running", "Starting task execution")))
	if err != nil {
		t.Fatalf("typed payload: %v", err)
	}
	// What every sender wrote before the payload had a type of its own.
	before, err := json.Marshal(TaskEventPayload("task-1", types.StreamEvent{
		Type: types.WSTypeStatus,
		Data: map[string]string{"state": "running", "activity": "Starting task execution"},
	}))
	if err != nil {
		t.Fatalf("map payload: %v", err)
	}
	if !bytes.Equal(typed, before) {
		t.Fatalf("the typed payload changed the wire:\n got %s\nwant %s", typed, before)
	}
}

// TestStatusFrameThatIsNotATurnStatusSaysSo: whether the turn ended is read off the payload, so a
// status frame nobody built with types.TurnStatusEvent has to be reported instead of leaving the
// task's frames replayable to the next subscriber.
func TestStatusFrameThatIsNotATurnStatusSaysSo(t *testing.T) {
	logs, restore := logging.TestSink()
	defer restore()

	asm := NewAppStreamManager(nil, nil)
	asm.SendTaskEvent("proj-1", "task-1", tokenEvent("x"))
	asm.SendTaskEvent("proj-1", "task-1", types.StreamEvent{
		Type: types.WSTypeStatus,
		Data: map[string]string{"state": "completed", "activity": "built by hand"},
	})

	line := logs.String()
	if !strings.Contains(line, "types.TurnStatus") {
		t.Fatalf("a status frame with an untyped payload passed without a word:\n%s", line)
	}
	if !strings.Contains(line, "task_id=task-1") {
		t.Fatalf("the report does not name the task it is about:\n%s", line)
	}
}
