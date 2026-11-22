package exec

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mike/backend/stream"
	"mike/backend/types"

	"github.com/coder/websocket"
)

// replayedEvents names the task events a live subscriber is sent within the window.
func replayedEvents(t *testing.T, asm *stream.AppStreamManager, projectID, taskID string, window time.Duration) []string {
	t.Helper()
	var names []string
	for _, e := range replayedTaskEvents(t, asm, projectID, taskID, window) {
		names = append(names, e.name)
	}
	return names
}

// A task_event a subscriber was handed, read off the flat frame.
type replayedEvent struct {
	name string
	data map[string]any
}

// replayedTaskEvents is what a subscriber arriving now is handed for the task it just took, oldest first.
// Reads are pumped in a goroutine because a read whose context expires closes the connection.
func replayedTaskEvents(t *testing.T, asm *stream.AppStreamManager, projectID, taskID string, window time.Duration) []replayedEvent {
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

	var events []replayedEvent
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
			name, _ := frame["event"].(string)
			if name == "" {
				continue
			}
			data, _ := frame["data"].(map[string]any)
			events = append(events, replayedEvent{name: name, data: data})
		case <-deadline:
			return events
		}
	}
}

// TestAFreshSubscriberAfterAResultRowIsOwedTheTail pins where the executor marks the conversation durable:
// the write of a tool call's result, not the frame that announces it.
func TestAFreshSubscriberAfterAResultRowIsOwedTheTail(t *testing.T) {
	t.Parallel()
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

// unansweredToolCallRow is a call whose row is written and whose result has not landed: what a view arriving
// mid-tool-call reads, since the rows come before the calls run.
func unansweredToolCallRow(msgs []types.TaskMessage) *types.TaskMessage {
	for i := range msgs {
		if msgs[i].Role == "tool_call" && msgs[i].ToolResult == "" {
			return &msgs[i]
		}
	}
	return nil
}

// TestAFreshSubscriberOverARunningToolCallIsNotToldItIsThinking: the buffer keeps a collapsed call's newest
// count frame for whoever subscribes next, and the replay is applied in order, so that newest frame is what
// decides whether a view arriving mid-tool-call believes the model is still thinking.
func TestAFreshSubscriberOverARunningToolCallIsNotToldItIsThinking(t *testing.T) {
	t.Parallel()
	// The command sleeps, so the tool is still running while the replay is read, and the answer after it goes
	// quiet so the turn is still open and its buffer has not been cleared.
	srv, _ := crashLLM(t,
		func(w http.ResponseWriter) { sseReasoningThenBashCall(w, "sleep 5") },
		streamsATokenThenGoesQuiet(6*time.Second),
	)
	te, _, projectID, initialSHA := setupExecutorWithGit(t)
	collapseReasoning(t, te, true)
	const taskID = "count-closed-before-the-tool"
	worktreePath := crashTask(t, te, projectID, initialSHA, taskID, srv.URL)
	startExecRun(t, te, projectID, taskID, srv.URL, worktreePath)

	threadID := "thread-" + taskID
	deadline := time.Now().Add(60 * time.Second)
	for unansweredToolCallRow(mustMessages(t, te, taskID, threadID)) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("no unanswered tool_call row within 60s; roles = %v", rolesInOrder(t, te, taskID, threadID))
		}
		time.Sleep(20 * time.Millisecond)
	}

	var newest map[string]any
	counts := 0
	for _, e := range replayedTaskEvents(t, te.appStreamMgr, projectID, taskID, 400*time.Millisecond) {
		if e.name != string(types.WSTypeReasoningProgress) {
			continue
		}
		counts++
		newest = e.data
	}
	if counts == 0 {
		t.Fatal("no count frame came back: the call this test staged announced nothing to close")
	}
	if id, _ := newest["message_id"].(string); id == "" {
		t.Errorf("the newest count frame a subscriber is handed is %v: nothing closed it", newest)
	}
}
