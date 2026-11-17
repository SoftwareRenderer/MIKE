package stream

// What one connection was sent, sorted by what it cost. Two things are held here: a frame is counted under
// the event that produced it, not under the envelope it travels in, and the byte count is what reached the
// socket.

import (
	"encoding/json/v2"
	"fmt"
	"strings"
	"testing"
	"time"

	"mike/backend/types"
)

// TestFrameBucketNamesTheEventInsideTheEnvelope: a frame is counted under the event that produced
// it, not under the envelope it travels in. The name comes from the map the sender built and never
// from the encoded bytes — a tool payload carries command output, which can hold the text `"type":"`.
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
		{"tool output carrying typed text", TaskEventPayload("task-1", types.StreamEvent{
			Type: types.WSTypeToolOutput,
			Data: map[string]string{"text": `{"type":"task_updated","task":"someone else's bytes"}`},
		}), "tool_output"},
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
