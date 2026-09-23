package stream

// What one connection was sent, sorted by what it cost. Two things are held here: a frame is counted under
// the event that produced it, not under the envelope it travels in, and the byte count is what reached the
// socket.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"mike/backend/types"
)

func TestFrameBucketNamesTheEventInsideTheEnvelope(t *testing.T) {
	cases := []struct {
		name  string
		frame map[string]any
		want  string
	}{
		{"token", TaskEventPayload("task-1", tokenEvent("hello")), "token"},
		{"sandbox stats", TaskEventPayload("task-1", types.StreamEvent{
			Type: types.WSTypeSandboxStats,
			Data: types.SandboxMemoryStats{Running: true},
		}), "sandbox_stats"},
		{"board frame", appFrame(types.WSTypeTaskUpdated, map[string]any{"task": &types.Task{}}), "task_updated"},
		{"counts", appFrame(types.WSTypeProjectCounts, map[string]any{"data": nil}), "project_counts"},
		{"no type", map[string]any{"data": 1}, "untagged"},
	}
	for _, tc := range cases {
		if got := frameBucket(tc.frame); got != tc.want {
			t.Errorf("%s: bucket %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestFrameBucketComesFromTheEnvelopeNotItsPayload: a tool payload can contain the text `"type":"`, because
// it carries command output. The name comes from the map the sender built, so those frames stay counted
// under the event that sent them.
func TestFrameBucketComesFromTheEnvelopeNotItsPayload(t *testing.T) {
	frame := TaskEventPayload("task-1", types.StreamEvent{Type: types.WSTypeToolOutput, Data: map[string]string{
		"text": `{"type":"task_updated","task":"someone else's bytes"}`,
	}})
	if got := frameBucket(frame); got != "tool_output" {
		t.Fatalf("bucket %q, want tool_output", got)
	}
}

// TestFrameBucketSurvivesTheWire: the same name has to come out of the encoded bytes too, because a name
// lost between the map and the socket leaves a bandwidth line with nothing behind it.
func TestFrameBucketSurvivesTheWire(t *testing.T) {
	frame := TaskEventPayload("task-1", tokenEvent("hello"))
	msg, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got, want := frameBucket(frame), frameBucket(mustDecode(t, msg)); got != want {
		t.Fatalf("bucket before the wire is %q, after it %q", got, want)
	}
}

func TestTrafficLedgerReportNamesTheBiggestSpenderFirst(t *testing.T) {
	ledger := newTrafficLedger()
	ledger.record("token", 40)
	ledger.record("task_updated", 9000)
	ledger.record("token", 60)

	report := ledger.report()
	if frames, bytes := ledger.totals(); frames != 3 || bytes != 9100 {
		t.Fatalf("totals %d frames/%d bytes, want 3/9100", frames, bytes)
	}
	// One task_updated frame costs more than the two token frames together, so it leads the line.
	if strings.Index(report, "task_updated") > strings.Index(report, "token") {
		t.Fatalf("report %q does not lead with task_updated", report)
	}
}

// TestTrafficLedgerCountsWhatTheSenderWrote: every frame this connection was written is counted, under the
// event that produced it, at the byte length it had on the wire.
func TestTrafficLedgerCountsWhatTheSenderWrote(t *testing.T) {
	asm := NewAppStreamManager(writeTestStore(t), nil)
	t.Cleanup(asm.Close)

	live := dialStream(t, asm)
	client := clientAlone(t, asm)
	const projectID, taskID = "proj-traffic", "task-traffic"
	// Board frames go to a connection that holds the project, so a connection holding only the task would
	// never see the task_updated this counts.
	live.sendSubscription(t, "subscribe_project", projectID, "")
	live.sendSubscription(t, "subscribe_task", projectID, taskID)
	awaitTaskSub(t, asm, client, projectID, taskID)

	token := tokenEvent("hello")
	asm.SendTaskEvent(projectID, taskID, token)
	asm.SendTaskEvent(projectID, taskID, token)
	tokenFrame := mustMarshal(t, TaskEventPayload(taskID, token))
	live.await(t, 5*time.Second, func(f map[string]any) bool {
		return f["type"] == "task_event" && f["event"] == "token"
	}, 2, "two token frames on the wire")
	awaitLedger(t, client, "token", func(_, bytes int) bool { return bytes == 2*len(tokenFrame) },
		fmt.Sprintf("%d bytes", 2*len(tokenFrame)))

	task := &types.Task{ID: taskID, ProjectID: projectID, Title: "traffic"}
	asm.BroadcastTaskUpdated(projectID, task)
	live.await(t, 5*time.Second, func(f map[string]any) bool { return f["type"] == "task_updated" },
		1, "task_updated on the wire")
	awaitLedger(t, client, "task_updated", func(frames, _ int) bool { return frames == 1 }, "1 frame")

	// A frame the connection never received is not bandwidth. The counts frames are counted because they were
	// written: one when the connection opened, one with the update.
	awaitLedger(t, client, "project_counts", func(frames, _ int) bool { return frames >= 2 },
		"the connect frame and the update's")
	report := client.traffic.report()
	for _, want := range []string{"token 2/", "task_updated 1/", "project_counts"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report %q does not mention %q", report, want)
		}
	}
}

// awaitLedger waits for the sender's own count of one bucket to satisfy the test. A record lands just after
// its write, so a frame the client has read may not be counted yet.
func awaitLedger(t *testing.T, client *clientState, bucket string, settled func(frames, bytes int) bool, want string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		if settled(client.traffic.bucketFrames(bucket), client.traffic.bucketBytes(bucket)) {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("bucket %q never held %s (frames=%d bytes=%d)", bucket, want,
				client.traffic.bucketFrames(bucket), client.traffic.bucketBytes(bucket))
		case <-time.After(time.Millisecond):
		}
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	msg, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return msg
}

func mustDecode(t *testing.T, frame []byte) map[string]any {
	t.Helper()
	var frameMap map[string]any
	if err := json.Unmarshal(frame, &frameMap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return frameMap
}
