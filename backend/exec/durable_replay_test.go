package exec

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mike/backend/stream"
	"mike/backend/types"

	"github.com/coder/websocket"
)

// replayedEvents names the task events a live subscriber is sent within the window. Reads are pumped in a
// goroutine because a read whose context expires closes the connection.
func replayedEvents(t *testing.T, asm *stream.AppStreamManager, projectID, taskID string, window time.Duration) []string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(asm.HandleStream))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial the task stream: %v", err)
	}
	conn.SetReadLimit(-1)
	t.Cleanup(func() { conn.CloseNow() })

	frames := make(chan map[string]any, 256)
	go func() {
		defer close(frames)
		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var frame map[string]any
			if json.Unmarshal(raw, &frame) == nil {
				frames <- frame
			}
		}
	}()

	body, err := json.Marshal(map[string]any{"type": "subscribe_task",
		"data": map[string]string{"project_id": projectID, "task_id": taskID}})
	if err != nil {
		t.Fatalf("marshal subscribe: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, body); err != nil {
		t.Fatalf("subscribe to the task: %v", err)
	}

	var events []string
	deadline := time.After(window)
	for {
		select {
		case frame, open := <-frames:
			if !open {
				return events
			}
			if frame["type"] != string(types.WSTypeTaskEvent) {
				continue
			}
			if event, _ := frame["event"].(string); event != "" {
				events = append(events, event)
			}
		case <-deadline:
			return events
		}
	}
}

// TestAFreshSubscriberAfterAResultRowIsOwedTheTail pins where the executor marks the conversation durable:
// the write of a tool call's result, not the frame that announces it.
func TestAFreshSubscriberAfterAResultRowIsOwedTheTail(t *testing.T) {
	// The second answer goes quiet for longer than the replay window, so the turn is still open when the
	// replay is read.
	srv, _ := crashLLM(t,
		func(w http.ResponseWriter) { sseBashCall(w, "call_1", `{"command":"echo hi"}`) },
		streamsATokenThenGoesQuiet(6*time.Second),
	)
	te, _, projectID, initialSHA := setupExecutorWithGit(t)
	const taskID = "trim-at-the-result-row"
	worktreePath := crashTask(t, te, projectID, initialSHA, taskID, srv.URL)
	startExecRun(t, te, projectID, taskID, srv.URL, worktreePath)

	threadID := "thread-" + taskID
	deadline := time.Now().Add(60 * time.Second)
	for toolCallRowWithResult(t, mustMessages(t, te, taskID, threadID)) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("no tool_call row with a result within 60s; roles = %v", rolesInOrder(t, te, taskID, threadID))
		}
		time.Sleep(20 * time.Millisecond)
	}

	events := replayedEvents(t, te.appStreamMgr, projectID, taskID, 400*time.Millisecond)
	for _, event := range events {
		if event == string(types.WSTypeToolCall) || event == string(types.WSTypeToolResult) {
			t.Fatalf("a subscriber arriving after the result row is owed %v: the finished iteration is still in the replay buffer", events)
		}
	}
}
