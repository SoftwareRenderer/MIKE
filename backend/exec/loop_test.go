package exec

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mike/backend/llama"
	"mike/backend/store"
	"mike/backend/stream"
	"mike/backend/types"

	"github.com/coder/websocket"
)

// --- Phrase Detector Tests ---

// testPhrase is the standard test sentence used across phrase detector tests.
const testPhrase = "The quick brown fox jumps over the lazy dog."

// repeatPhrase sends the same phrase to the detector enough times to reach the repeat threshold.
// Returns true if the detector fired.
func repeatPhrase(d *phraseDetector, phrase string) bool {
	fired := false
	for i := 0; i < maxPhraseRepeats; i++ {
		if d.check(phrase) {
			fired = true
			break
		}
	}
	return fired
}

func TestPhraseDetector_NoRepetition(t *testing.T) {
	t.Parallel()
	d := newPhraseDetector()

	// Feed unique sentences
	text := "The quick brown fox jumps over the lazy dog today. Then the cat sat on the mat and slept all day long."
	if d.check(text) {
		t.Error("should not detect repetition in unique text")
	}
}

func TestPhraseDetector_ExactRepetition(t *testing.T) {
	t.Parallel()
	d := newPhraseDetector()

	if !repeatPhrase(d, testPhrase) {
		t.Error("should detect exact phrase repetition")
	}
}

func TestPhraseDetector_ShortPhraseIgnored(t *testing.T) {
	t.Parallel()
	d := newPhraseDetector()

	// Short phrase (< 8 words) should be ignored
	short := "Hello world."
	d.check(short)
	if d.check(short) {
		t.Error("should not fire on short phrases")
	}
}

func TestPhraseDetector_FiresOnce(t *testing.T) {
	t.Parallel()
	d := newPhraseDetector()

	repeatPhrase(d, testPhrase) // fires
	// After first fire, should still return false (already fired)
	if d.check(testPhrase) {
		t.Error("should not fire again after already fired")
	}
}

func TestPhraseDetector_Reset(t *testing.T) {
	t.Parallel()
	d := newPhraseDetector()

	repeatPhrase(d, testPhrase) // fires

	d.reset()
	// After reset, same text should not fire (first occurrence)
	if d.check(testPhrase) {
		t.Error("should not fire after reset with same text")
	}
	// Now it should fire after enough repeats
	if !repeatPhrase(d, testPhrase) {
		t.Error("should fire on repeat after reset")
	}
}

func TestPhraseDetector_MultipleSentencesInOneChunk(t *testing.T) {
	t.Parallel()
	d := newPhraseDetector()

	// Feed the phrase multiple times in one chunk to reach threshold
	text := strings.Repeat(testPhrase+" ", maxPhraseRepeats-1) + testPhrase
	if !d.check(text) {
		t.Error("should detect repetition when repeated sentence appears later in same chunk")
	}
}

// TestPhraseDetector_SentenceBoundary: a repeated sentence is only caught once the splitter cuts it out,
// so every terminator the model actually writes is a shape the detector can fire on.
func TestPhraseDetector_SentenceBoundary(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		chunk string
	}{
		{"NewlineBoundary", testPhrase + "\n"},
		{"ExclamationBoundary", "Wow this is really amazing and incredible stuff!"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := newPhraseDetector()
			if !repeatPhrase(d, tt.chunk) {
				t.Errorf("repeating %q never fired", tt.chunk)
			}
		})
	}
}

func TestPhraseDetector_CodeBlockNotTriggered(t *testing.T) {
	t.Parallel()
	d := newPhraseDetector()

	// Code-like content without sentence boundaries
	code := "func main() { fmt.Println(\"hello\") }"
	d.check(code)
	if d.check(code) {
		t.Error("should not trigger on code without sentence boundaries")
	}
}

func TestPhraseDetector_IncompleteSentenceAccumulates(t *testing.T) {
	t.Parallel()
	d := newPhraseDetector()

	// Feed partial sentence, then complete it, then repeat enough times
	d.check("The quick brown fox jumps")
	d.check(" over the lazy dog.")
	// Repeat the full sentence to reach threshold
	if !repeatPhrase(d, "The quick brown fox jumps over the lazy dog.") {
		t.Error("should detect repetition when incomplete sentence is completed and then repeated")
	}
}

// --- Loop Detector Integration Tests ---

func makeToolCall(name, argsJSON string) types.ChatToolCall {
	return types.ChatToolCall{
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: name, Arguments: argsJSON},
	}
}

func TestLoopDetector_ToolLoop(t *testing.T) {
	t.Parallel()
	d := newLoopDetector()

	args1 := map[string]string{"path": "src/main.go"}
	args1JSON, _ := json.Marshal(args1)
	tc := []types.ChatToolCall{makeToolCall("read_file", string(args1JSON))}

	// First read of the file: nothing to repeat yet.
	if loopType, _ := d.recordTurn(tc, "", "tool_calls"); loopType != LoopNone {
		t.Errorf("first read should not trigger loop, got %v", loopType)
	}

	// Second read of the same target: the loop, and the args the hint is built from.
	loopType, returnedArgs := d.recordTurn(tc, "", "tool_calls")
	if loopType != LoopTool {
		t.Fatalf("second read of same file should trigger LoopTool, got %v", loopType)
	}
	if returnedArgs != string(args1JSON) {
		t.Errorf("returned args = %q, want %q", returnedArgs, string(args1JSON))
	}

	// A state-changing call clears the read tracker, so reading that file again after
	// writing it is progress, not a loop.
	write := []types.ChatToolCall{makeToolCall("write_file", `{"path":"src/main.go","content":"x"}`)}
	if loopType, _ := d.recordTurn(write, "", "tool_calls"); loopType != LoopNone {
		t.Errorf("a write should never be a loop, got %v", loopType)
	}
	if loopType, _ := d.recordTurn(tc, "", "tool_calls"); loopType != LoopNone {
		t.Errorf("re-reading after a write should not be a loop, got %v", loopType)
	}
}

// --- Rolling Window Tests ---

func TestLoopDetector_LoopStreakResetsOnValidTurn(t *testing.T) {
	t.Parallel()
	d := newLoopDetector()

	// Simulate loop streak
	d.loopStreak = 2

	// Valid turn resets streak
	args := map[string]string{"path": "src/other.go"}
	argsJSON, _ := json.Marshal(args)
	tc := []types.ChatToolCall{makeToolCall("read_file", string(argsJSON))}
	loopType, _ := d.recordTurn(tc, "", "tool_calls")
	if loopType != LoopNone {
		t.Fatalf("expected LoopNone, got %v", loopType)
	}
	if d.loopStreak != 0 {
		t.Errorf("expected loopStreak=0 after valid turn, got %d", d.loopStreak)
	}

	// A new loop starts fresh
	args = map[string]string{"path": "src/main.go"}
	argsJSON, _ = json.Marshal(args)
	tc = []types.ChatToolCall{makeToolCall("read_file", string(argsJSON))}
	d.recordTurn(tc, "", "tool_calls")               // first read, valid (loopStreak stays 0)
	loopType, _ = d.recordTurn(tc, "", "tool_calls") // second read, loop
	if loopType != LoopTool {
		t.Fatal("expected LoopTool")
	}
	// handleLoopDetected increments
	d.loopStreak++
	if d.loopStreak != 1 {
		t.Errorf("expected loopStreak=1 (fresh streak), got %d", d.loopStreak)
	}
}

func TestHandleLoopDetected_IncrementsAndPreservesLoopStreak(t *testing.T) {
	t.Parallel()
	e := newTestExecutor(t)
	now := time.Now().UTC()

	proj := &types.Project{ID: "proj-streak", Name: "Streak Test", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}
	if err := e.store.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &types.Task{
		ID: "task-streak", ProjectID: proj.ID, Title: "Test", Prompt: "Test",
		Status: types.StatusInProgress, ExecutionState: types.ExecStateRunning,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	detector := newLoopDetector()
	messages := []types.ChatMessage{{Role: "system", Content: "system prompt"}}

	// First call — increments streak to 1, does NOT terminate, preserves streak
	terminated := handleLoopDetected(e, task, proj.ID, task.ID, detector, LoopTool, "foo.go", &messages, false)
	if terminated {
		t.Fatal("first loop should not terminate")
	}
	if detector.loopStreak != 1 {
		t.Errorf("expected loopStreak=1, got %d", detector.loopStreak)
	}

	// Second call — increments streak to 2, does NOT terminate, preserves streak
	terminated = handleLoopDetected(e, task, proj.ID, task.ID, detector, LoopTool, "foo.go", &messages, false)
	if terminated {
		t.Fatal("second loop should not terminate")
	}
	if detector.loopStreak != 2 {
		t.Errorf("expected loopStreak=2, got %d", detector.loopStreak)
	}

	// Third call — increments streak to 3, terminates (>= maxLoopHints)
	terminated = handleLoopDetected(e, task, proj.ID, task.ID, detector, LoopTool, "foo.go", &messages, false)
	if !terminated {
		t.Fatal("third loop should terminate (>= maxLoopHints)")
	}
}

func TestHandleLoopDetected_IdeationModeSuppressesHints(t *testing.T) {
	t.Parallel()
	e := newTestExecutor(t)
	now := time.Now().UTC()

	proj := &types.Project{ID: "proj-exploration", Name: "exploration", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}
	_ = e.store.CreateProject(proj)
	task := &types.Task{
		ID:            "task-exploration",
		ProjectID:     proj.ID,
		Title:         "Ideation task",
		Prompt:        "Brainstorm",
		Status:        types.StatusInProgress,
		OperatingMode: "explore",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := e.store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	detector := newLoopDetector()
	messages := []types.ChatMessage{{Role: "system", Content: "system prompt"}}

	// With explore mode, handleLoopDetected should NOT inject hints or terminate
	for i := 0; i < 5; i++ {
		terminated := handleLoopDetected(e, task, proj.ID, task.ID, detector, LoopTool, "foo.go", &messages, true)
		if terminated {
			t.Fatalf("explore mode should never terminate (iteration %d)", i+1)
		}
	}
	// No hint messages should have been injected
	if len(messages) != 1 {
		t.Errorf("expected 1 message (system only), got %d", len(messages))
	}
	// Loop streak should be 0 (reset on each call in explore mode)
	if detector.loopStreak != 0 {
		t.Errorf("expected loopStreak=0 in explore mode, got %d", detector.loopStreak)
	}
	// Task should still be running
	taskAfter, _ := e.store.GetTaskByID(task.ID)
	if taskAfter.ExecutionState == types.ExecStateFailed {
		t.Error("task should not be in failed state in explore mode")
	}
}

// TestHandleLoopDetected_HintBroadcastCarriesMessageID verifies the hint
// broadcast carries the persisted message ID (frontend dedupes on history reload).
func TestHandleLoopDetected_HintBroadcastCarriesMessageID(t *testing.T) {
	t.Parallel()
	e := newTestExecutor(t)
	now := time.Now().UTC()

	proj := &types.Project{ID: "proj-hint-id", Name: "Hint ID Test", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}
	if err := e.store.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &types.Task{
		ID: "task-hint-id", ProjectID: proj.ID, Title: "Test", Prompt: "Test",
		Status: types.StatusInProgress, ExecutionState: types.ExecStateRunning,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	detector := newLoopDetector()
	messages := []types.ChatMessage{{Role: "system", Content: "system prompt"}}

	terminated := handleLoopDetected(e, task, proj.ID, task.ID, detector, LoopStatement, "", &messages, false)
	if terminated {
		t.Fatal("loop hint should not terminate on first detection")
	}

	// The hint must be persisted with IsLoopHint set.
	hintMsgs, err := e.store.GetTaskMessages(task.ID, task.CurrentThreadID)
	if err != nil {
		t.Fatalf("get task messages: %v", err)
	}
	if len(hintMsgs) != 1 {
		t.Fatalf("expected 1 persisted hint message, got %d", len(hintMsgs))
	}
	hintMsg := hintMsgs[0]
	if !hintMsg.IsLoopHint {
		t.Error("expected persisted hint message to have IsLoopHint=true")
	}
	if hintMsg.Role != "user" {
		t.Errorf("expected hint role=user, got %q", hintMsg.Role)
	}
	if hintMsg.Content != buildHint(LoopStatement, "") {
		t.Errorf("unexpected hint content: %q", hintMsg.Content)
	}

	// The broadcast frame must carry the same message ID so the frontend can
	// dedupe the real-time entry against the DB entry on history reload.
	hintFrame, found := findTaskStreamEvent(taskStreamFrames(t, e.appStreamMgr, proj.ID, task.ID), "hint")
	if !found {
		t.Fatal("no hint broadcast frame received")
	}
	if hintFrame.Data["id"] != hintMsg.ID {
		t.Errorf("hint broadcast id = %v, want %s", hintFrame.Data["id"], hintMsg.ID)
	}
	if hintFrame.Data["content"] != hintMsg.Content {
		t.Errorf("hint broadcast content = %v, want %s", hintFrame.Data["content"], hintMsg.Content)
	}
}

// TestSendRetryHint_SystemRole: retry notification persists as a system message
// and the hint broadcast carries role "system".
// taskStreamFrame is one task_event frame as a subscriber receives it on the
// wire (the flat envelope built by stream.TaskEventPayload).
type taskStreamFrame struct {
	Type   string         `json:"type"`
	Event  string         `json:"event"`
	TaskID string         `json:"task_id"`
	Data   map[string]any `json:"data"`
}

// taskStreamFrames captures what a task subscriber receives, using the real
// stream WebSocket: subscribe_task makes the server replay the task's frame
// buffer through the same path a reconnecting browser takes. A flush event is
// sent after subscribing so the read has a definite end — its arrival proves the
// subscription was applied — plus a short drain for the replayed frames.
func taskStreamFrames(t *testing.T, asm *stream.AppStreamManager, projectID, taskID string) []taskStreamFrame {
	t.Helper()
	return taskStreamFramesDuring(t, asm, projectID, taskID, nil)
}

// taskStreamFramesDuring captures what a task subscriber receives while act runs.
// act starts only once the subscription is known to be live, and frames replayed
// before it are dropped, so the result is exactly what the task emitted during
// act. A nil act captures only the buffer replay.
func taskStreamFramesDuring(t *testing.T, asm *stream.AppStreamManager, projectID, taskID string, act func()) []taskStreamFrame {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(asm.HandleStream))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial stream: %v", err)
	}
	defer conn.CloseNow()

	sub, _ := json.Marshal(map[string]any{
		"type": "subscribe_task",
		"data": map[string]string{"project_id": projectID, "task_id": taskID},
	})
	if err := conn.Write(ctx, websocket.MessageText, sub); err != nil {
		t.Fatalf("subscribe_task: %v", err)
	}

	const flushText = "__stream_capture_flush__"
	sendFlush := func() {
		asm.SendTaskEvent(projectID, taskID, types.StreamEvent{
			Type: types.WSTypeToken,
			Data: map[string]string{"text": flushText},
		})
	}

	// readOne classifies the next frame. A read whose context expired closes the
	// connection (coder/websocket semantics), so it ends the capture.
	type readOutcome int
	const (
		readClosed readOutcome = iota
		readSkip
		readFrame
		readFlush
	)
	readOne := func(rctx context.Context) (taskStreamFrame, readOutcome) {
		_, raw, err := conn.Read(rctx)
		if err != nil {
			return taskStreamFrame{}, readClosed
		}
		var frame taskStreamFrame
		if json.Unmarshal(raw, &frame) != nil || frame.Type != "task_event" {
			return taskStreamFrame{}, readSkip // project_counts and friends
		}
		if frame.Event == string(types.WSTypeToken) && frame.Data["text"] == flushText {
			return taskStreamFrame{}, readFlush
		}
		return frame, readFrame
	}

	capture := func(drain bool) []taskStreamFrame {
		var frames []taskStreamFrame
		for {
			frame, outcome := readOne(ctx)
			switch outcome {
			case readFrame:
				frames = append(frames, frame)
			case readSkip:
				continue
			case readClosed:
				return frames
			case readFlush:
				if !drain {
					return frames
				}
				// A turn's frames outlive the flush that ends the capture, so keep
				// reading until the connection goes quiet. The drain read runs on a
				// short deadline, which closes the connection, so it is the last read.
				for {
					drainCtx, cancelDrain := context.WithTimeout(ctx, 500*time.Millisecond)
					late, lateOutcome := readOne(drainCtx)
					cancelDrain()
					if lateOutcome != readFrame {
						return frames
					}
					frames = append(frames, late)
				}
			}
		}
	}

	sendFlush()
	if act == nil {
		return capture(true)
	}
	capture(false) // the flush arriving proves the subscription is live
	act()
	sendFlush()
	return capture(true)
}

// findTaskStreamEvent returns the first captured frame for the named event.
func findTaskStreamEvent(frames []taskStreamFrame, event string) (taskStreamFrame, bool) {
	for _, f := range frames {
		if f.Event == event {
			return f, true
		}
	}
	return taskStreamFrame{}, false
}

func TestSendRetryHint_SystemRole(t *testing.T) {
	t.Parallel()
	e := newTestExecutor(t)
	now := time.Now().UTC()

	proj := &types.Project{ID: "proj-retry-hint", Name: "Retry Hint Test", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}
	if err := e.store.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &types.Task{
		ID: "task-retry-hint", ProjectID: proj.ID, Title: "Test", Prompt: "Test",
		Status: types.StatusInProgress, ExecutionState: types.ExecStateRunning,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	e.sendRetryHint(proj.ID, task.ID, task, errors.New("EOF"))

	// The retry message must be persisted with role=system.
	msgs, err := e.store.GetTaskMessages(task.ID, task.CurrentThreadID)
	if err != nil {
		t.Fatalf("get task messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 persisted retry message, got %d", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("expected persisted retry role=system, got %q", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "Connection interrupted") {
		t.Errorf("unexpected retry content: %q", msgs[0].Content)
	}
	// One notification covers the whole retry sequence, so it names no attempt number.
	if strings.Contains(msgs[0].Content, "/3") || strings.Contains(msgs[0].Content, "attempt") {
		t.Errorf("retry notification reads as one attempt (%q); it is sent once per call, not once per attempt", msgs[0].Content)
	}

	// The broadcast frame must carry the same message ID plus role "system".
	hintFrame, found := findTaskStreamEvent(taskStreamFrames(t, e.appStreamMgr, proj.ID, task.ID), "hint")
	if !found {
		t.Fatal("no hint broadcast frame received")
	}
	if hintFrame.Data["id"] != msgs[0].ID {
		t.Errorf("hint broadcast id = %v, want %s", hintFrame.Data["id"], msgs[0].ID)
	}
	if hintFrame.Data["role"] != "system" {
		t.Errorf("hint broadcast role = %v, want \"system\"", hintFrame.Data["role"])
	}
	if hintFrame.Data["content"] != msgs[0].Content {
		t.Errorf("hint broadcast content = %v, want %s", hintFrame.Data["content"], msgs[0].Content)
	}
}

func TestLoopDetector_ToolLoopDifferentTargets(t *testing.T) {
	t.Parallel()
	d := newLoopDetector()

	args1 := map[string]string{"path": "src/main.go"}
	args1JSON, _ := json.Marshal(args1)
	args2 := map[string]string{"path": "src/utils.go"}
	args2JSON, _ := json.Marshal(args2)

	tc1 := []types.ChatToolCall{makeToolCall("read_file", string(args1JSON))}
	tc2 := []types.ChatToolCall{makeToolCall("read_file", string(args2JSON))}

	if loopType, _ := d.recordTurn(tc1, "", "tool_calls"); loopType != LoopNone {
		t.Error("first read should not trigger loop")
	}
	if loopType, _ := d.recordTurn(tc2, "", "tool_calls"); loopType != LoopNone {
		t.Error("read of different file should not trigger loop")
	}
}

// TestLoopDetector_TextOnlyTurn: a text-only turn that ends in a statement is LoopStatement, and being a
// loop it leaves the streak alone — only a turn that asks resets it.
func TestLoopDetector_TextOnlyTurn(t *testing.T) {
	t.Parallel()
	statements := []string{
		"I need to carefully analyze the requirements and implement the solution step by step to make sure everything works correctly.",
		"I must carefully analyze the requirements and implement the solution step by step to make sure everything works correctly.",
		"The database migration completed successfully and all tables are now created with the correct schema definitions",
		"I wrote comprehensive unit tests covering edge cases and boundary conditions for the authentication module",
		"The deployment pipeline is configured with proper error handling and notification hooks for the staging environment",
	}
	for _, text := range statements {
		d := newLoopDetector()
		if loopType, _ := d.recordTurn(nil, text, "stop"); loopType != LoopStatement {
			t.Errorf("expected LoopStatement for %q, got %v", text, loopType)
		}
	}

	d := newLoopDetector()
	d.loopStreak = 2
	for _, text := range statements {
		if loopType, _ := d.recordTurn(nil, text, "stop"); loopType != LoopStatement {
			t.Fatalf("expected LoopStatement, got %v", loopType)
		}
	}
	if d.loopStreak != 2 {
		t.Errorf("expected loopStreak=2 (preserved across LoopStatement), got %d", d.loopStreak)
	}
}

// TestLoopDetector_StatementOnlyWhenLLMDone holds the finish-reason gate that precedes the paragraph
// rule: a response that did not end by choice is not penalised however it ends, and the paragraph is only
// read once the model said it was done.
func TestLoopDetector_StatementOnlyWhenLLMDone(t *testing.T) {
	t.Parallel()
	const (
		oneParagraph  = "I have analyzed the code."
		twoParagraphs = "Analyzing the module.\n\nNext I'll rewrite it."
	)
	for _, tc := range []struct {
		name   string
		text   string
		reason string
		want   LoopType
	}{
		{"done, ending in a statement", oneParagraph, "stop", LoopStatement},
		{"done, two paragraphs, still a statement", twoParagraphs, "stop", LoopStatement},
		{"asked for tools and produced none", oneParagraph, "tool_calls", LoopNone},
		{"cut off by length", oneParagraph, "length", LoopNone},
		{"the stream has not ended", oneParagraph, "", LoopNone},
		{"two paragraphs, tools asked for and absent", twoParagraphs, "tool_calls", LoopNone},
		{"two paragraphs, cut off by length", twoParagraphs, "length", LoopNone},
		{"two paragraphs, stream not ended", twoParagraphs, "", LoopNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newLoopDetector()
			if loopType, _ := d.recordTurn(nil, tc.text, tc.reason); loopType != tc.want {
				t.Errorf("recordTurn(finish_reason=%q) = %v, want %v", tc.reason, loopType, tc.want)
			}
		})
	}
}

func TestLoopDetector_QuestionDoesNotTriggerStatement(t *testing.T) {
	t.Parallel()
	d := newLoopDetector()

	// Text whose final paragraph asks should NOT trigger LoopStatement. Every code point
	// isQuestionMark recognises is here: a turn that asks in another script waits for the
	// reader just the same, and an ASCII-only list would pass with that table cut back to '?'.
	questions := []string{
		"Should I use React or Vue for this project?",
		"What framework would you recommend? Let me know your preference.",
		"Could you clarify the requirements?",
	}
	for _, q := range questions {
		loopType, _ := d.recordTurn(nil, q, "stop")
		if loopType != LoopNone {
			t.Errorf("expected LoopNone for question %q, got %v", q, loopType)
		}
	}
	for _, mark := range []rune{'\u00BF', '\u037E', '\u055E', '\u061F', '\u109E',
		'\u2047', '\u2048', '\u2049', '\uFE16', '\uFE56', '\uFF1F'} {
		asks := "Should I merge this now" + string(mark)
		if loopType, _ := d.recordTurn(nil, asks, "stop"); loopType != LoopNone {
			t.Errorf("expected LoopNone for %q, got %v", asks, loopType)
		}
	}
	// loopStreak should be reset to 0
	if d.loopStreak != 0 {
		t.Errorf("expected loopStreak=0 after question, got %d", d.loopStreak)
	}
}

func TestLoopDetector_QuestionInEarlierParagraphStillStatement(t *testing.T) {
	t.Parallel()
	d := newLoopDetector()
	d.loopStreak = 2

	// Only the closing paragraph can be waiting for the user, so an earlier question does
	// not spare a response that ends in a statement.
	ended := "Should I use React or Vue for this project?\n\nI'll go with React and wire up the router."
	if loopType, _ := d.recordTurn(nil, ended, "stop"); loopType != LoopStatement {
		t.Errorf("expected LoopStatement when only an earlier paragraph asks, got %v", loopType)
	}
	if d.loopStreak != 2 {
		t.Errorf("expected loopStreak=2 (preserved by LoopStatement), got %d", d.loopStreak)
	}

	asks := "I'll go with React and wire up the router.\n\nWant me to add tests too?"
	if loopType, _ := d.recordTurn(nil, asks, "stop"); loopType != LoopNone {
		t.Errorf("expected LoopNone when the last paragraph asks, got %v", loopType)
	}
	if d.loopStreak != 0 {
		t.Errorf("expected loopStreak=0 after a closing question, got %d", d.loopStreak)
	}
}

func TestLoopDetector_ResetClearsPhraseFlag(t *testing.T) {
	t.Parallel()
	d := newLoopDetector()

	chunk := "Let me think about this problem carefully and analyze the full requirements. "
	for i := 0; i < maxPhraseRepeats; i++ {
		d.checkStreamingText(chunk)
	}

	d.reset()
	if d.phraseDetected() {
		t.Error("phraseDetected should be false after reset")
	}
}

func TestLoopDetector_ToolCallResetsPhraseTracker(t *testing.T) {
	t.Parallel()
	d := newLoopDetector()

	// Feed phrase multiple times (not quite enough to fire)
	chunk := "Let me think about this problem carefully and analyze the full requirements. "
	for i := 0; i < maxPhraseRepeats-1; i++ {
		d.checkStreamingText(chunk)
	}

	// Tool call resets phrase tracker
	args := map[string]string{"path": "src/main.go"}
	argsJSON, _ := json.Marshal(args)
	tc := []types.ChatToolCall{makeToolCall("read_file", string(argsJSON))}
	d.recordTurn(tc, "", "tool_calls")

	// Same phrase again — tracker was reset, so it won't fire
	if d.checkStreamingText(chunk) == LoopPhrase {
		t.Error("phrase should not fire after tool call reset")
	}
}

func TestLoopDetector_RemoveHints(t *testing.T) {
	t.Parallel()
	d := newLoopDetector()

	// Build a conversation with hints injected at indices 2 and 4
	messages := []types.ChatMessage{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there"},
		{Role: "user", Content: "LOOP HINT 1"}, // index 2
		{Role: "assistant", Content: "Ok, calling tool"},
		{Role: "user", Content: "LOOP HINT 2"}, // index 4
		{Role: "assistant", Content: "Done"},
	}
	d.recordHintIndex(2)
	d.recordHintIndex(4)

	// Remove hints
	cleaned := d.removeHints(messages)

	// Should have 4 messages (hints at indices 2 and 4 removed)
	if len(cleaned) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(cleaned))
	}
	expected := []struct {
		role, content string
	}{
		{"user", "Hello"},
		{"assistant", "Hi there"},
		{"assistant", "Ok, calling tool"},
		{"assistant", "Done"},
	}
	for i, e := range expected {
		if cleaned[i].Role != e.role || cleaned[i].Content != e.content {
			t.Errorf("message[%d] = {Role: %q, Content: %q}, want {Role: %q, Content: %q}",
				i, cleaned[i].Role, cleaned[i].Content, e.role, e.content)
		}
	}
	// hintIndices should be cleared
	if len(d.hintIndices) != 0 {
		t.Errorf("expected hintIndices to be cleared, got %v", d.hintIndices)
	}
}

func TestLoopDetector_RemoveHints_Noop(t *testing.T) {
	t.Parallel()
	d := newLoopDetector()
	messages := []types.ChatMessage{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi"},
	}
	// No hints recorded — should return unchanged
	cleaned := d.removeHints(messages)
	if len(cleaned) != 2 {
		t.Errorf("expected 2 messages, got %d", len(cleaned))
	}
}

// TestBuildHint covers the loop-hint text the model is sent for each loop kind: the two
// hints whose exact wording is load-bearing are pinned verbatim, LoopReasoningOnly is
// checked for the things it must offer (a reason to act, and the tools to act with),
// LoopNone must produce nothing at all, and the rest must not come back empty — an empty
// hint means the loop is detected but the model is never told.
func TestBuildHint(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		kind      LoopType
		arg       string
		want      string
		contains  []string
		wantEmpty bool
	}{
		{
			name: "repeated phrase",
			kind: LoopPhrase,
			want: "You repeated yourself in your last response. Continue with a new action or tool call.",
		},
		{
			name: "repeated tool target",
			kind: LoopTool,
			arg:  "src/main.go",
			want: "You've read/checked `src/main.go` twice in a row without making any changes in between. You already have the contents of this file. Summarize what you know and decide on the next action — edit the file, run a command, or mark the task complete.",
		},
		{name: "repeated text", kind: LoopText},
		{name: "no loop", kind: LoopNone, wantEmpty: true},
		{
			name: "reasoning only",
			kind: LoopReasoningOnly,
			contains: []string{"reasoning", "function calling",
				"read_file", "write_file", "edit_file", "bash", "ask_question", "request_review"},
		},
		{
			name: "statement without action",
			kind: LoopStatement,
			want: "Is the task complete? Use `ask_question` if clarification/approval is needed, or `request_review` if the task is complete. Otherwise use `read_file`, `write_file`, `edit_file`, or `bash` to make progress.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hint := buildHint(tc.kind, tc.arg)

			if tc.wantEmpty {
				if hint != "" {
					t.Errorf("kind %v should produce no hint, got %q", tc.kind, hint)
				}
				return
			}
			if hint == "" {
				t.Fatalf("kind %v produced an empty hint", tc.kind)
			}
			if tc.want != "" && hint != tc.want {
				t.Errorf("hint = %q, want %q", hint, tc.want)
			}
			for _, sub := range tc.contains {
				if !strings.Contains(hint, sub) {
					t.Errorf("hint should mention %q, got: %s", sub, hint)
				}
			}
		})
	}
}

// --- Text Analysis Helper Tests ---

// TestNormalizePhrase covers the comparison key behind phrase-loop detection. Everything
// here is a detection miss if it goes wrong: case and run-of-whitespace differences must
// collapse (so the same sentence typed twice matches), UTF-8 must survive untouched, and
// trailing punctuation must drop without eating the word it follows — or "hello world!!!"
// stops matching "hello world".
func TestNormalizePhrase(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "collapses case and whitespace", in: "  Hello   World  ", want: "hello world"},
		{name: "preserves UTF-8", in: "  Café Résumé  ", want: "café résumé"},
		{name: "punctuation only", in: "....", want: ""},
		{name: "strips trailing punctuation", in: "hello world!!!", want: "hello world"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizePhrase(tc.in); got != tc.want {
				t.Errorf("normalizePhrase(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSplitSentences covers the sentence splitter that feeds phrase detection. Exact
// segment lists are asserted wherever the boundaries are defined, because a segment that
// loses or gains a character corrupts the phrase comparison downstream. The UTF-8 rows are
// the point of the table: splitSentences walks bytes, so a boundary scan that lands
// mid-rune silently corrupts the text it is supposed to be only cutting.
func TestSplitSentences(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		// want is the exact segment list; nil means only min is asserted.
		want []string
		min  int
	}{
		{
			name: "punctuation boundaries",
			in:   "Hello world. How are you? I am fine!",
			want: []string{"Hello world.", "How are you?", "I am fine!"},
		},
		{name: "no boundary", in: "No sentence boundary here at all", want: []string{"No sentence boundary here at all"}},
		{
			name: "paragraph break",
			in:   "First paragraph here is long enough.\n\nSecond paragraph also long enough.",
			min:  2,
		},
		{
			name: "multi-byte Latin",
			in:   "Café résumé naïve. Hello world!",
			want: []string{"Café résumé naïve.", "Hello world!"},
		},
		{
			// isSentenceEnd only knows ASCII .!?, so this pins that CJK passes through intact.
			name: "CJK is not corrupted",
			in:   "你好世界. 这是一个测试!",
			want: []string{"你好世界.", "这是一个测试!"},
		},
		{
			name: "4-byte emoji",
			in:   "Hello 👋 world. How are you?",
			want: []string{"Hello 👋 world.", "How are you?"},
		},
		{
			name: "mixed 2-, 3- and 4-byte runes",
			in:   "café 日本語 🎉 done. Next!",
			want: []string{"café 日本語 🎉 done.", "Next!"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitSentences(tc.in)

			if tc.want == nil {
				if len(got) < tc.min {
					t.Fatalf("expected at least %d segments, got %d: %v", tc.min, len(got), got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("segments = %v (%d), want %v (%d)", got, len(got), tc.want, len(tc.want))
			}
			for i, want := range tc.want {
				if got[i] != want {
					t.Errorf("segment %d = %q, want %q", i, got[i], want)
				}
			}
		})
	}
}

// crashingBashCommand is the command that ended a real review turn: the task's tool call stored both
// results, then the loop detector's read-only classification panicked and the turn unwound.
const crashingBashCommand = `cd /home/sandbox/MIKE && grep -rn --include="*.go" -E "Sprintf\(\"[^\"]*(\\|\\|\\||&&|; *\\$|\\becho\\b|\\bgrep\\b|\\brg\\b|\\bls\\b|\\bcat\\b)" backend/ pkg/ | grep -v _test | head -20; echo "=== search exec ==="; grep -rn "func (s \*Server) runSearch\|func.*search" backend/server/handler_fs.go | head -20`

// readOnlyCommandCases pins the classifier's verdicts. A separator inside quotes is text, not a separator,
// and a segment with no words decides nothing — the two rules every shape below turns on.
var readOnlyCommandCases = []struct {
	cmd  string
	want bool
}{
	{"head src/main.go", true},
	{"cat file.txt", true},
	{"ls -la", true},
	{"grep foo bar.txt", true},
	{"echo hello > file.txt", false}, // redirect
	{"python script.py", false},
	{"git commit -m msg", false},
	{"sed -i 's/a/b/' file", false},

	// A quoted separator is an argument: `&&` and `|` inside quotes start no new segment.
	{`grep "a&&|b" f`, true},
	{`cat "a && b"`, true},
	{`grep -E "s\"&&|x" f`, true},
	{crashingBashCommand, true},

	// Nothing to classify: the verdict is read-only, the same answer an unparseable command gets.
	{"", true},
	{"   ", true},
	{"| grep x", true},

	// Every segment decides: `;` separates commands the way `&&` and a pipe do.
	{"echo hi; rm -f x", false},
	{"echo hi | rm -f x", false},
	{"grep x f | wc -l && git status", false},
	{"echo a && echo b > out", false},
}

func TestIsReadOnlyCommand(t *testing.T) {
	t.Parallel()
	for _, tt := range readOnlyCommandCases {
		got := isReadOnlyCommand(tt.cmd)
		if got != tt.want {
			t.Errorf("isReadOnlyCommand(%q) = %v, want %v", tt.cmd, got, tt.want)
		}
	}
}

// FuzzIsReadOnlyCommand holds the invariant the classifier exists under: its verdict chooses which
// loop hint to show and grants nothing, so no command text may end the turn that asked for it. The
// table cases run through that verdict in TestIsReadOnlyCommand().
func FuzzIsReadOnlyCommand(f *testing.F) {
	f.Add(crashingBashCommand)
	f.Add(`grep "a&&|b" f`)
	f.Add(`| grep x`)
	f.Add(`echo hi; rm -f x`)
	f.Add(`"`)
	f.Add(`'`)
	f.Add(`\\`)
	f.Fuzz(func(t *testing.T, cmd string) {
		isReadOnlyCommand(cmd)
	})
}

// --- Sandbox Prompt Path Tests ---

// --- Turn Rendering Tests ---

// chatMsg is one message of a conversation fixture.
func chatMsg(role, content string) types.ChatMessage {
	return types.ChatMessage{Role: role, Content: content}
}

// asstWithCall is the assistant row a tool call belongs to; toolAnswer is the tool row that answers it.
// The renderers see a call only through that pair, so every fixture below needs both.
func asstWithCall(id, name, args string) types.ChatMessage {
	return types.ChatMessage{Role: "assistant", ToolCalls: []types.ToolCallMsg{{
		ID: id, Type: "function",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: name, Arguments: args},
	}}}
}

func toolAnswer(callID, content string) types.ChatMessage {
	return types.ChatMessage{Role: "tool", Content: content, ToolCallID: callID}
}

// TestExtractLastTurns covers what a commit-message prompt is built from: turns, not messages — the
// system prompt never appears, at most n turns are kept and they are the last n, and a turn the model
// never answered still counts as a turn, or the question it asks disappears.
func TestExtractLastTurns(t *testing.T) {
	t.Parallel()
	threeTurns := []types.ChatMessage{
		chatMsg("system", "system prompt"),
		chatMsg("user", "Turn 1 user"), chatMsg("assistant", "Turn 1 assistant"),
		chatMsg("user", "Turn 2 user"), chatMsg("assistant", "Turn 2 assistant"),
		chatMsg("user", "Turn 3 user"), chatMsg("assistant", "Turn 3 assistant"),
	}
	fiveTurns := []types.ChatMessage{chatMsg("system", "system prompt")}
	for i := 1; i <= 5; i++ {
		fiveTurns = append(fiveTurns, chatMsg("user", fmt.Sprintf("Turn %d", i)), chatMsg("assistant", fmt.Sprintf("A%d", i)))
	}

	for _, tc := range []struct {
		name     string
		messages []types.ChatMessage
		want     []types.ChatMessage
		min      int
	}{
		{name: "nothing to take", messages: nil},
		{name: "a system prompt is not a turn", messages: []types.ChatMessage{chatMsg("system", "You are a helpful assistant")}},
		{
			name:     "one turn",
			messages: []types.ChatMessage{chatMsg("system", "system prompt"), chatMsg("user", "Hello"), chatMsg("assistant", "Hi there")},
			want:     []types.ChatMessage{chatMsg("user", "Hello"), chatMsg("assistant", "Hi there")},
		},
		{
			name:     "three turns all fit",
			messages: threeTurns,
			want:     threeTurns[1:],
		},
		{
			name:     "only the last three turns",
			messages: fiveTurns,
			want:     []types.ChatMessage{chatMsg("user", "Turn 3"), chatMsg("assistant", "A3"), chatMsg("user", "Turn 4"), chatMsg("assistant", "A4"), chatMsg("user", "Turn 5"), chatMsg("assistant", "A5")},
		},
		{
			name: "a turn keeps the rows its tool calls made",
			messages: []types.ChatMessage{
				chatMsg("system", "system prompt"),
				chatMsg("user", "Read a file"),
				asstWithCall("call_1", "read_file", `{"path":"foo.txt"}`),
				toolAnswer("call_1", "file contents"),
				chatMsg("assistant", "Here is the content"),
				chatMsg("user", "Thanks"),
				chatMsg("assistant", "You're welcome"),
			},
			min: 4,
		},
		{
			name:     "an unanswered turn is still a turn",
			messages: []types.ChatMessage{chatMsg("system", "system prompt"), chatMsg("user", "Turn 1"), chatMsg("assistant", "A1"), chatMsg("user", "Turn 2 (no response yet)")},
			want:     []types.ChatMessage{chatMsg("user", "Turn 1"), chatMsg("assistant", "A1"), chatMsg("user", "Turn 2 (no response yet)")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractLastTurns(tc.messages, 3)
			if tc.min > 0 {
				if len(got) < tc.min {
					t.Fatalf("expected at least %d messages, got %d", tc.min, len(got))
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d messages, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, want := range tc.want {
				if got[i].Role != want.Role || got[i].Content != want.Content {
					t.Errorf("message[%d] = %s:%q, want %s:%q", i, got[i].Role, got[i].Content, want.Role, want.Content)
				}
			}
		})
	}
}

// TestFormatConversationTurns covers the prompt text a commit label or a summary reads: a tool call is
// described in its assistant line and its result row never stands alone, a create_task/edit_task line
// names what the call produced (and its failure quotes the error), and no call with no recorded result
// may read as a success.
func TestFormatConversationTurns(t *testing.T) {
	t.Parallel()
	const createdJSON = `{"task_id":"frost-amber-quartz","title":"New task"}`
	const editedJSON = `{"task_id":"frost-amber-quartz","title":"New title","status":"updated"}`

	for _, tc := range []struct {
		name       string
		messages   []types.ChatMessage
		want       string
		wantSuffix string
		contains   []string
		absent     []string
	}{
		{
			name:     "a plain exchange",
			messages: []types.ChatMessage{chatMsg("user", "Hello"), chatMsg("assistant", "Hi there")},
			want:     "User: Hello\n\nAssistant: Hi there\n\n",
		},
		{
			name: "a tool call is described, its result row is not echoed",
			messages: []types.ChatMessage{
				asstWithCall("call_1", "read_file", `{"path":"foo.txt"}`),
				toolAnswer("call_1", "file contents"),
			},
			contains: []string{"I used the read_file tool to", "foo.txt"},
			absent:   []string{"[Tool result]"},
		},
		{
			name:     "a standalone tool row is skipped",
			messages: []types.ChatMessage{{Role: "tool", Content: strings.Repeat("x", 600)}},
			want:     "",
		},
		{
			name: "blank lines never stack, and every block ends with one",
			messages: []types.ChatMessage{
				chatMsg("user", "Fix the bug\n\n"),
				asstWithCall("call_1", "read_file", `{"path":"foo.go"}`),
				toolAnswer("call_1", "file contents"),
				chatMsg("assistant", "Done!\n"),
			},
			absent:     []string{"\n\n\n"},
			wantSuffix: "\n\n",
		},
		{
			name: "a created task is named by id and title",
			messages: []types.ChatMessage{
				asstWithCall("call_1", "create_task", `{"title":"New task"}`),
				toolAnswer("call_1", createdJSON),
			},
			contains: []string{"I used the create_task tool to create task ID: frost-amber-quartz, title: New task"},
			absent:   []string{"which was"},
		},
		{
			name: "a failed create quotes the error",
			messages: []types.ChatMessage{
				asstWithCall("call_1", "create_task", `{"title":"New task"}`),
				toolAnswer("call_1", "error: project not found"),
			},
			contains: []string{"but was unsuccessful. Error: error: project not found"},
		},
		{
			name: "an edited task is named by id and title",
			messages: []types.ChatMessage{
				asstWithCall("call_1", "edit_task", `{"task_id":"frost-amber-quartz","title":"New title"}`),
				toolAnswer("call_1", editedJSON),
			},
			contains: []string{"I used the edit_task tool to edit task ID: frost-amber-quartz, title: New title"},
		},
		{
			name: "a failed edit quotes the error",
			messages: []types.ChatMessage{
				asstWithCall("call_1", "edit_task", `{"task_id":"frost-amber-quartz","title":"New title"}`),
				toolAnswer("call_1", "tool edit_task failed: task frost-amber-quartz not found"),
			},
			contains: []string{
				"I used the edit_task tool to edit but was unsuccessful. Error:",
				"task frost-amber-quartz not found",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatConversationTurns(tc.messages)
			if tc.want != "" || len(tc.contains) == 0 && len(tc.absent) == 0 {
				if got != tc.want {
					t.Errorf("output = %q, want %q", got, tc.want)
				}
			}
			if tc.wantSuffix != "" && !strings.HasSuffix(got, tc.wantSuffix) {
				t.Errorf("expected trailing %q, got: %q", tc.wantSuffix, got[len(got)-5:])
			}
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("output missing %q, got:\n%s", want, got)
				}
			}
			for _, notWant := range tc.absent {
				if strings.Contains(got, notWant) {
					t.Errorf("output contains %q, got:\n%s", notWant, got)
				}
			}
		})
	}
}

// TestFormatChatHistory_ToolCallLines covers the history a summarizer is handed, which renders a tool
// call through the same wording but its own prefixes: the line names what the call produced, and a
// failure carries the provider's error rather than a bare "unsuccessful".
func TestFormatChatHistory_ToolCallLines(t *testing.T) {
	t.Parallel()
	const taskArgs = `{"task_id":"frost-amber-quartz","title":"New title"}`

	for _, tc := range []struct {
		name     string
		tool     string
		args     string
		result   string
		contains []string
		absent   []string
	}{
		{
			name: "a created task is named by id and title", tool: "create_task", args: `{"title":"New task"}`,
			result:   `{"task_id":"frost-amber-quartz","title":"New task"}`,
			contains: []string{"I used the create_task tool to create task ID: frost-amber-quartz, title: New task"},
			absent:   []string{"which was"},
		},
		{
			name: "a failed create quotes the error", tool: "create_task", args: `{"title":"Bad task"}`,
			result:   "error: invalid project",
			contains: []string{"but was unsuccessful. Error: error: invalid project"},
		},
		{
			name: "an edited task is named by id and title", tool: "edit_task", args: taskArgs,
			result:   `{"task_id":"frost-amber-quartz","title":"New title","status":"updated"}`,
			contains: []string{"I used the edit_task tool to edit task ID: frost-amber-quartz, title: New title"},
		},
		{
			name: "a failed edit quotes the error", tool: "edit_task", args: taskArgs,
			result: "tool edit_task failed: task frost-amber-quartz not found",
			contains: []string{
				"I used the edit_task tool to edit but was unsuccessful. Error:",
				"task frost-amber-quartz not found",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := []types.ChatMessage{
				chatMsg("system", "system prompt"),
				chatMsg("user", "do the thing"),
				asstWithCall("call_1", tc.tool, tc.args),
				toolAnswer("call_1", tc.result),
			}
			history := formatChatHistory(messages, false)
			for _, want := range tc.contains {
				if !strings.Contains(history, want) {
					t.Errorf("chat history missing %q. Got:\n%s", want, history)
				}
			}
			for _, notWant := range tc.absent {
				if strings.Contains(history, notWant) {
					t.Errorf("chat history contains %q. Got:\n%s", notWant, history)
				}
			}
		})
	}
}

// --- isTaskID Tests ---

// TestIsTaskID pins the shape of a task ID (three dictionary words) at the one place that
// decides whether a token in a command line is an ID. The invalid rows are the interesting
// ones: two words, four words, an empty string and the dash-adjacent forms must all read as
// prose, or a model's sentence starts being parsed as a task reference.
func TestIsTaskID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		id   string
		want bool
	}{
		{id: "frost-amber-quartz", want: true},
		{id: "abc-def-ghi", want: true},
		{id: "a-b-c", want: true},
		{id: "", want: false},
		{id: "foo", want: false},
		{id: "foo-bar", want: false},
		{id: "foo-bar-baz-qux", want: false},
		{id: "-foo-bar", want: false},
		{id: "foo--bar", want: false},
		{id: "foo-bar-", want: false},
	}

	for _, tc := range cases {
		name := tc.id
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			if got := isTaskID(tc.id); got != tc.want {
				t.Errorf("isTaskID(%q) = %v, want %v", tc.id, got, tc.want)
			}
		})
	}
}

// --- formatToolAction Tests ---

// TestFormatToolAction covers the one-line label the commit-message generator sees instead
// of a tool call's full arguments.
func TestFormatToolAction(t *testing.T) {
	t.Parallel()
	cases := []struct {
		tool string
		args string
		want string
	}{
		{tool: "create_task", args: `{"title":"Test task"}`, want: "create"},
		{tool: "edit_task", args: `{"task_id":"frost-amber-quartz","title":"New title"}`, want: "edit"},
	}

	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			if got := formatToolAction(tc.tool, tc.args); got != tc.want {
				t.Errorf("formatToolAction(%q, …) = %q, want %q", tc.tool, got, tc.want)
			}
		})
	}
}

// --- formatToolResult Tests ---

// TestFormatToolResult covers how a tool call's result is rendered back into the commit
// message context. Two things are pinned per row: what a SUCCESS names (the task id and
// title, falling back to "a task" when the result is not JSON or carries no id) and what a
// FAILURE leaks (create_task/edit_task quote the error, other tools only say it failed).
// The pending row matters too — a call with no recorded result must not claim success.
func TestFormatToolResult(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		tool    string
		status  string
		results map[string]string
		want    string
	}{
		{
			name: "create_task with id and title", tool: "create_task", status: "successful",
			results: map[string]string{"call_1": `{"task_id":"frost-amber-quartz","title":"New task"}`},
			want:    "task ID: frost-amber-quartz, title: New task",
		},
		{
			name: "create_task with a non-JSON result", tool: "create_task", status: "successful",
			results: map[string]string{"call_1": "Task created successfully"},
			want:    "a task",
		},
		{
			name: "create_task with an id only", tool: "create_task", status: "successful",
			results: map[string]string{"call_1": `{"task_id":"frost-amber-quartz"}`},
			want:    "task ID: frost-amber-quartz",
		},
		{
			name: "create_task failure quotes the error", tool: "create_task", status: "unsuccessful",
			results: map[string]string{"call_1": "error: project not found"},
			want:    "but was unsuccessful. Error: error: project not found",
		},
		{
			// No recorded result at all must not read as success.
			name: "create_task still pending", tool: "create_task", status: "pending",
			results: map[string]string{},
			want:    "a task",
		},
		{
			name: "edit_task with id and title", tool: "edit_task", status: "successful",
			results: map[string]string{"call_1": `{"task_id":"frost-amber-quartz","title":"New title","status":"updated"}`},
			want:    "task ID: frost-amber-quartz, title: New title",
		},
		{
			name: "edit_task failure quotes the error", tool: "edit_task", status: "unsuccessful",
			results: map[string]string{"call_1": "tool edit_task failed: task frost-amber-quartz not found"},
			want:    "but was unsuccessful. Error: tool edit_task failed: task frost-amber-quartz not found",
		},
		{
			name: "other tool success", tool: "read_file", status: "successful",
			results: map[string]string{"call_1": "file contents"},
			want:    "which was successful",
		},
		{
			// Unlike create_task/edit_task, an unnamed tool's error text is not echoed.
			name: "other tool failure", tool: "read_file", status: "unsuccessful",
			results: map[string]string{"call_1": "error: file not found"},
			want:    "which was unsuccessful",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatToolResult(tc.tool, "call_1", tc.status, tc.results)
			if got != tc.want {
				t.Errorf("formatToolResult(%q, %s) = %q, want %q", tc.tool, tc.status, got, tc.want)
			}
		})
	}
}

func TestBuildCommitConversation_MergesToolCallsIntoAssistant(t *testing.T) {
	t.Parallel()
	taskMsgs := []types.TaskMessage{
		{Role: "user", Content: "Fix the bug"},
		{Role: "assistant", Content: "Let me check the file"},
		{Role: "tool_call", ToolCalls: `{"tool_id":"call_1","tool_name":"read_file","args":{"path":"foo.go"},"raw_args":"{\"path\":\"foo.go\"}"}`, ToolResult: `{"output":"file contents","success":true,"duration":10}`},
		{Role: "assistant", Content: "I see the issue"},
		{Role: "tool_call", ToolCalls: `{"tool_id":"call_2","tool_name":"edit_file","args":{"path":"foo.go"},"raw_args":"{\"path\":\"foo.go\",\"edits\":[...]}"}`, ToolResult: `{"output":"Edited foo.go (1 edit(s) applied)","success":true,"duration":5}`},
		{Role: "assistant", Content: "Done!"},
	}

	messages := BuildConversationFromTaskMessages(taskMsgs)

	// Should have: user, assistant(with tool_call), tool(result), assistant(with tool_call), tool(result), assistant
	if len(messages) != 6 {
		t.Fatalf("expected 6 messages, got %d", len(messages))
	}

	// First assistant should have the read_file tool call merged
	if messages[1].Role != "assistant" || len(messages[1].ToolCalls) != 1 {
		t.Error("expected first assistant to have merged tool call")
	}
	if messages[1].ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("expected read_file, got %s", messages[1].ToolCalls[0].Function.Name)
	}

	// Tool result should be a separate "tool" message
	if messages[2].Role != "tool" || messages[2].ToolCallID != "call_1" {
		t.Errorf("expected tool result message with call_1, got role=%s toolCallID=%s", messages[2].Role, messages[2].ToolCallID)
	}

	// Second assistant should have the edit_file tool call merged
	if messages[3].Role != "assistant" || len(messages[3].ToolCalls) != 1 {
		t.Error("expected second assistant to have merged tool call")
	}
	if messages[3].ToolCalls[0].Function.Name != "edit_file" {
		t.Errorf("expected edit_file, got %s", messages[3].ToolCalls[0].Function.Name)
	}
}

func TestBuildCommitConversation_OrphanToolRowsIgnored(t *testing.T) {
	t.Parallel()
	// A tool_call row with no assistant row to own it belongs to nothing. The utility
	// conversation is built by the same groupTaskMessages the LLM context uses, so the row
	// contributes neither a call nor a result here either — an orphan result with no call is
	// noise a commit-message prompt cannot act on.
	taskMsgs := []types.TaskMessage{
		{Role: "tool_call", ToolCalls: `{"tool_id":"call_1","tool_name":"read_file","raw_args":"{}"}`, ToolResult: `{"output":"contents","success":true}`},
		{Role: "user", Content: "Hello"},
	}

	messages := BuildConversationFromTaskMessages(taskMsgs)

	if len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("expected only the user message, got %+v", messages)
	}
}

func TestBuildCommitConversation_AdvisoryRowKeepsToolRows(t *testing.T) {
	t.Parallel()
	// A note written between an assistant row and its tool_call rows must not cost the commit message
	// that turn's calls and results.
	taskMsgs := []types.TaskMessage{
		{ID: "a1", Role: "user", Content: "Fix it"},
		{ID: "as-1", Role: "assistant", Content: "editing"},
		{Role: "tool_call", AssistantMsgID: "as-1", ToolCalls: `{"tool_id":"call_1","tool_name":"edit_file","raw_args":"{\"path\":\"a.go\"}"}`, ToolResult: `{"output":"Edited a.go","success":true}`},
		{Role: "advisory", Content: "LSP diagnostics for this turn's changes (1 file with findings):\n\nFix issues in a.go:\n1. line 3 error"},
		{Role: "tool_call", AssistantMsgID: "as-1", ToolCalls: `{"tool_id":"call_2","tool_name":"edit_file","raw_args":"{\"path\":\"b.go\"}"}`, ToolResult: `{"output":"Edited b.go","success":true}`},
	}

	messages := BuildConversationFromTaskMessages(taskMsgs)

	if len(messages) != 5 {
		t.Fatalf("expected user, assistant, 2 tool results, advisory, got %d: %+v", len(messages), messages)
	}
	if len(messages[1].ToolCalls) != 2 {
		t.Fatalf("assistant rebuilt with %d tool calls, want 2", len(messages[1].ToolCalls))
	}
	if messages[2].ToolCallID != "call_1" || messages[3].ToolCallID != "call_2" {
		t.Errorf("tool results = %q, %q, want call_1, call_2", messages[2].ToolCallID, messages[3].ToolCallID)
	}
	if messages[4].Role != "advisory" {
		t.Fatalf("last rebuilt message = %q, want advisory (it follows the work it comments on)", messages[4].Role)
	}

	// The note itself never reaches the prompt text — FormatConversationTurns has no case
	// for it — but it must not be attributed to the user either.
	turnText := FormatConversationTurns(messages)
	if strings.Contains(turnText, "User: LSP diagnostics") {
		t.Errorf("advisory attributed to the user:\n%s", turnText)
	}
}

func TestFormatConversationTurns_FullPipeline(t *testing.T) {
	t.Parallel()
	// Test the full pipeline: DB messages -> BuildConversationFromTaskMessages -> ExtractLastTurns -> FormatConversationTurns
	taskMsgs := []types.TaskMessage{
		{Role: "user", Content: "Fix the login bug"},
		{Role: "assistant", Content: "Let me check the auth file"},
		{Role: "tool_call", ToolCalls: `{"tool_id":"call_1","tool_name":"read_file","args":{"path":"src/auth.go"},"raw_args":"{\"path\":\"src/auth.go\"}"}`, ToolResult: `{"output":"package auth","success":true,"duration":10}`},
		{Role: "assistant", Content: "I see the issue. Let me fix it."},
		{Role: "tool_call", ToolCalls: `{"tool_id":"call_2","tool_name":"edit_file","args":{"path":"src/auth.go"},"raw_args":"{\"path\":\"src/auth.go\"}"}`, ToolResult: `{"output":"Edited src/auth.go (1 edit(s) applied)","success":true,"duration":5}`},
	}

	conversation := BuildConversationFromTaskMessages(taskMsgs)
	turns := ExtractLastTurns(conversation, 3)
	result := FormatConversationTurns(turns)

	// Should contain descriptive tool-use lines
	if !strings.Contains(result, "I used the read_file tool to") {
		t.Errorf("expected read_file description, got:\n%s", result)
	}
	if !strings.Contains(result, "src/auth.go") {
		t.Errorf("expected file path, got:\n%s", result)
	}
	if !strings.Contains(result, "I used the edit_file tool to") {
		t.Errorf("expected edit_file description, got:\n%s", result)
	}
	// Should NOT contain old format
	if strings.Contains(result, "[tool:") {
		t.Error("should not contain old [tool: format")
	}
	if strings.Contains(result, "[Tool result]") {
		t.Error("should not contain [Tool result] format")
	}
}

// --- UTF-8 streaming ---

func TestPhraseDetector_UTF8Streaming(t *testing.T) {
	t.Parallel()
	// Streaming chunks of multi-byte UTF-8 characters split across call boundaries.
	d := newPhraseDetector()

	// Feed chunks that contain UTF-8 characters
	// Feed a UTF-8 phrase with 10+ words via streaming chunks
	phrase := "Let me analyze the café's approach to this problème today."
	chunks := []string{
		"Let me analyze the caf",
		"é's approach to this pr",
		"oblème today.",
	}
	for _, chunk := range chunks {
		if d.check(chunk) {
			t.Error("should not detect repetition in first occurrence")
		}
	}

	// Now repeat enough times to reach threshold
	fired := false
	for i := 1; i < maxPhraseRepeats; i++ {
		if d.check(phrase) {
			fired = true
			break
		}
	}
	if !fired {
		t.Error("should detect repetition of UTF-8 phrase")
	}
}

func TestPhraseDetector_PendingBufferBound(t *testing.T) {
	t.Parallel()
	// Feed a very long text without sentence boundaries.
	// The pending buffer should be bounded to maxPendingSize.
	d := newPhraseDetector()

	// Generate text longer than maxPendingSize (16KB) without sentence boundaries
	longText := strings.Repeat("no sentence boundary here just words ", 1000)
	if len(longText) < maxPendingSize {
		t.Fatalf("the fixture text is %d bytes, under maxPendingSize %d, so the buffer was never over its bound",
			len(longText), maxPendingSize)
	}

	d.check(longText)
	// Pending should be bounded
	if len(d.pending) > maxPendingSize {
		t.Errorf("pending buffer %d bytes exceeds maxPendingSize %d", len(d.pending), maxPendingSize)
	}
}

// --- StopProjectTasks Tests ---

func TestStopProjectTasks(t *testing.T) {
	t.Parallel()
	te := newTestExecutor(t)
	now := time.Now().UTC()

	// Create project with unique path
	proj := &types.Project{ID: "proj-test", Name: "Test Project", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}
	if err := te.store.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create tasks in the project
	task1 := createTaskInProject(t, te.store, proj.ID, "task-1")
	task2 := createTaskInProject(t, te.store, proj.ID, "task-2")
	// Task in a different project
	otherProj := &types.Project{ID: "proj-other", Name: "Other Project", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}
	if err := te.store.CreateProject(otherProj); err != nil {
		t.Fatalf("create other project: %v", err)
	}
	_ = createTaskInProject(t, te.store, otherProj.ID, "task-other")

	// Simulate running executors for task1 and task2
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	te.taskMu.Lock()
	te.tasks[task1.ID] = &executionState{ctx: ctx1, cancel: cancel1, userMsgChan: make(chan stream.UserMessage, 16)}
	te.tasks[task2.ID] = &executionState{ctx: ctx2, cancel: cancel2, userMsgChan: make(chan stream.UserMessage, 16)}
	// Also add a non-project task (orphan)
	te.tasks["orphan-task"] = &executionState{
		ctx:         context.Background(),
		cancel:      func() {},
		userMsgChan: make(chan stream.UserMessage, 16),
	}
	te.taskMu.Unlock()

	// Call StopProjectTasks
	stopped := te.StopProjectTasks(proj.ID)
	if stopped != 2 {
		t.Fatalf("expected 2 tasks stopped, got %d", stopped)
	}

	// Verify task1 and task2 executors were removed
	te.taskMu.Lock()
	_, hasTask1 := te.tasks[task1.ID]
	_, hasTask2 := te.tasks[task2.ID]
	_, hasOrphan := te.tasks["orphan-task"]
	te.taskMu.Unlock()
	if hasTask1 {
		t.Error("expected task1 executor to be stopped")
	}
	if hasTask2 {
		t.Error("expected task2 executor to be stopped")
	}
	if !hasOrphan {
		t.Error("expected orphan task executor to still exist (different project)")
	}

	// Verify task statuses were reset to idle
	t1, _ := te.store.GetTaskByID(task1.ID)
	if t1.ExecutionState != types.ExecStateIdle {
		t.Errorf("expected task1 execution_state='idle', got %q", t1.ExecutionState)
	}
	t2, _ := te.store.GetTaskByID(task2.ID)
	if t2.ExecutionState != types.ExecStateIdle {
		t.Errorf("expected task2 execution_state='idle', got %q", t2.ExecutionState)
	}
}

func TestStopProjectTasks_DoesNotAffectNonRunningTasks(t *testing.T) {
	t.Parallel()
	te := newTestExecutor(t)
	now := time.Now().UTC()
	proj := &types.Project{ID: "proj-mixed", Name: "Mixed Project", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}
	if err := te.store.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create a running task and a review task
	runningTask := createTaskInProject(t, te.store, proj.ID, "running-task")
	reviewTask := createTaskInProject(t, te.store, proj.ID, "review-task")
	// Move review task to review status
	reviewTask.Status = types.StatusReview
	reviewTask.ExecutionState = types.ExecStateIdle
	_ = te.store.UpdateTask(reviewTask)

	// Only runningTask has an active executor
	ctx, cancel := context.WithCancel(context.Background())
	te.taskMu.Lock()
	te.tasks[runningTask.ID] = &executionState{ctx: ctx, cancel: cancel, userMsgChan: make(chan stream.UserMessage, 16)}
	te.taskMu.Unlock()

	// Call StopProjectTasks
	stopped := te.StopProjectTasks(proj.ID)
	if stopped != 1 {
		t.Fatalf("expected 1 task stopped, got %d", stopped)
	}

	// Verify running task was stopped and reset to idle
	runningTaskAfter, _ := te.store.GetTaskByID(runningTask.ID)
	if runningTaskAfter.ExecutionState != types.ExecStateIdle {
		t.Errorf("expected running task execution_state='idle', got %q", runningTaskAfter.ExecutionState)
	}

	// Verify review task was NOT modified
	reviewTaskAfter, _ := te.store.GetTaskByID(reviewTask.ID)
	if reviewTaskAfter.Status != types.StatusReview {
		t.Errorf("expected review task status='review', got %q", reviewTaskAfter.Status)
	}
	if reviewTaskAfter.ExecutionState != types.ExecStateIdle {
		t.Errorf("expected review task execution_state='idle', got %q", reviewTaskAfter.ExecutionState)
	}
}

// createTaskInProject creates a task directly in the store for testing.
func createTaskInProject(t *testing.T, s *store.Store, projectID, taskID string) *types.Task {
	t.Helper()
	now := time.Now().UTC()
	task := &types.Task{
		ID:             taskID,
		ProjectID:      projectID,
		Title:          "Test Task",
		Prompt:         "Test prompt",
		Status:         types.StatusBacklog,
		ExecutionState: types.ExecStateIdle,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	// Create default thread
	threadID := types.GenerateThreadID()
	if err := s.CreateThread(&types.TaskThread{
		ID:        threadID,
		TaskID:    taskID,
		Name:      "Main",
		IsActive:  true,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	task.CurrentThreadID = threadID
	_ = s.UpdateTask(task)
	return task
}

// --- Reasoning Effort Tests ---

func TestModelConfig_SetAndGet(t *testing.T) {
	t.Parallel()
	mc := &modelConfig{
		serverURL: "http://old-server/v1",
		modelName: "old-model",
	}

	// Verify initial values
	url, model := mc.get()
	if url != "http://old-server/v1" {
		t.Errorf("expected initial URL 'http://old-server/v1', got %q", url)
	}
	if model != "old-model" {
		t.Errorf("expected initial model 'old-model', got %q", model)
	}

	// Update model config
	mc.set("http://new-server/v1", "new-model")

	url, model = mc.get()
	if url != "http://new-server/v1" {
		t.Errorf("expected URL 'http://new-server/v1', got %q", url)
	}
	if model != "new-model" {
		t.Errorf("expected model 'new-model', got %q", model)
	}
}

func TestModelConfig_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	mc := &modelConfig{
		serverURL: "http://initial/v1",
		modelName: "initial-model",
	}

	done := make(chan bool, 2)

	// Writer goroutine
	go func() {
		for i := 0; i < 100; i++ {
			mc.set("http://server/v1", fmt.Sprintf("model-%d", i))
		}
		done <- true
	}()

	// Reader goroutine
	go func() {
		for i := 0; i < 100; i++ {
			_, _ = mc.get()
		}
		done <- true
	}()

	// Wait for both goroutines
	<-done
	<-done

	// If we got here without data race, the test passes
	_, _ = mc.get()
}

// --- Unstage New Binary Files Tests ---

func TestIsImageFile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path string
		want bool
	}{
		{"photo.png", true},
		{"image.JPG", true},
		{"icon.jpg", true},
		{"logo.jpeg", true},
		{"anim.gif", true},
		{"pic.webp", true},
		{"sprite.bmp", true},
		{"icon.svg", true},
		{"favicon.ico", true},
		{"scan.tiff", true},
		{"scan.tif", true},
		{"photo.avif", true},
		{"anim.apng", true},
		{"subdir/image.PNG", true},
		{"data.bin", false},
		{"file.exe", false},
		{"font.woff2", false},
		{"archive.zip", false},
		{"readme.md", false},
		{"script.js", false},
		{"unknown.xyz", false},
		{"noext", false},
	}

	for _, tt := range tests {
		got := isImageFile(tt.path)
		if got != tt.want {
			t.Errorf("isImageFile(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

// --- Helper Function Tests ---

// TestUserMessageToChatMessage covers the frame a browser send becomes: plain text stays plain, attachments
// become a text part plus an image part, a data: prefix is stripped from the bytes, and an attachment string
// that is not JSON is dropped rather than sent as a half-formed part.
func TestUserMessageToChatMessage(t *testing.T) {
	t.Parallel()
	attach := func(t *testing.T, atts ...types.Attachment) string {
		t.Helper()
		b, err := json.Marshal(atts)
		if err != nil {
			t.Fatalf("marshal attachments: %v", err)
		}
		return string(b)
	}

	for _, tc := range []struct {
		name        string
		content     string
		attachments string
		wantContent string
		wantParts   []types.ContentPart
	}{
		{
			name: "plain text", content: "hello world",
			wantContent: "hello world",
		},
		{
			name: "an attachment becomes a second part", content: "check this image",
			attachments: attach(t, types.Attachment{MimeType: "image/png", Data: "iVBORw0KGgo="}),
			// A message with parts carries no plain content of its own: the text is its first part.
			wantParts: []types.ContentPart{
				{Type: "text", Text: "check this image"},
				{Type: "image", ImageSource: &types.ImageSource{MediaType: "image/png", Data: "iVBORw0KGgo="}},
			},
		},
		{
			name: "a data: prefix is stripped from the bytes", content: "test",
			attachments: attach(t, types.Attachment{MimeType: "image/png", Data: "data:image/png;base64,abc123"}),
			wantParts: []types.ContentPart{
				{Type: "text", Text: "test"},
				{Type: "image", ImageSource: &types.ImageSource{MediaType: "image/png", Data: "abc123"}},
			},
		},
		{
			name: "an unlabelled attachment is a png", content: "test",
			attachments: attach(t, types.Attachment{MimeType: "", Data: "base64data"}),
			wantParts: []types.ContentPart{
				{Type: "text", Text: "test"},
				{Type: "image", ImageSource: &types.ImageSource{MediaType: "image/png", Data: "base64data"}},
			},
		},
		{
			name: "attachments that are not JSON are dropped", content: "test",
			attachments: "not-json", wantContent: "test",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := userMessageToChatMessage(stream.UserMessage{Content: tc.content, Attachments: tc.attachments})
			if msg.Role != "user" {
				t.Errorf("role = %q, want user", msg.Role)
			}
			if msg.Content != tc.wantContent {
				t.Errorf("content = %q, want %q", msg.Content, tc.wantContent)
			}
			if len(msg.ContentParts) != len(tc.wantParts) {
				t.Fatalf("got %d content parts, want %d: %+v", len(msg.ContentParts), len(tc.wantParts), msg.ContentParts)
			}
			for i, want := range tc.wantParts {
				got := msg.ContentParts[i]
				if got.Type != want.Type || got.Text != want.Text {
					t.Errorf("part[%d] = %s:%q, want %s:%q", i, got.Type, got.Text, want.Type, want.Text)
				}
				if want.ImageSource == nil {
					if got.ImageSource != nil {
						t.Errorf("part[%d] carries an image source %+v, want none", i, got.ImageSource)
					}
					continue
				}
				if got.ImageSource == nil {
					t.Fatalf("part[%d] has no image source, want %+v", i, want.ImageSource)
				}
				if got.ImageSource.MediaType != want.ImageSource.MediaType || got.ImageSource.Data != want.ImageSource.Data {
					t.Errorf("part[%d] image source = %+v, want media type %q and data %q",
						i, *got.ImageSource, want.ImageSource.MediaType, want.ImageSource.Data)
				}
			}
		})
	}
}

func TestExtractPrevSummary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		messages []types.ChatMessage
		want     string
	}{
		{
			name:     "no summary",
			messages: []types.ChatMessage{{Role: "system", Content: "you are a helper"}},
			want:     "",
		},
		{
			name: "assistant messages are not summaries",
			messages: []types.ChatMessage{
				{Role: "system", Content: "you are a helper"},
				{Role: "assistant", Content: "not a summary"},
			},
			want: "",
		},
		{
			name: "picks the summary",
			messages: []types.ChatMessage{
				{Role: "system", Content: "you are a helper"},
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi there"},
				{Role: "summarization", Content: "The user asked about Go generics"},
				{Role: "user", Content: "tell me more"},
			},
			want: "The user asked about Go generics",
		},
		{
			name: "most recent summary wins",
			messages: []types.ChatMessage{
				{Role: "system", Content: "you are a helper"},
				{Role: "summarization", Content: "first summary"},
				{Role: "user", Content: "more stuff"},
				{Role: "summarization", Content: "second summary"},
			},
			want: "second summary",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractPrevSummary(tc.messages); got != tc.want {
				t.Errorf("extractPrevSummary() = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- buildLLMContext Tests ---

// callMsg is the tool call an assistant row carries.
func callMsg(id, name, args string) types.ToolCallMsg {
	return types.ToolCallMsg{
		ID: id, Type: "function",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: name, Arguments: args},
	}
}

// TestBuildLLMContext covers what a summarization request is priced from. The summary row is the cut:
// everything at or before it is dropped, because the summary replaces it in the system prompt, so a stale
// or an early boundary would either resend the history or drop the answer the model still needs. Nothing
// that never came from a summarization row counts as one, and a request_review turn stays whole — dropping
// it is what makes a run ask to be reviewed again.
func TestBuildLLMContext(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		messages  []types.ChatMessage
		want      []types.ChatMessage
		wantTools []string
	}{
		{
			name:     "no summary sends the conversation as it stands",
			messages: []types.ChatMessage{chatMsg("system", "system prompt"), chatMsg("user", "hello"), chatMsg("assistant", "hi")},
			want:     []types.ChatMessage{chatMsg("system", "system prompt"), chatMsg("user", "hello"), chatMsg("assistant", "hi")},
		},
		{
			name: "the summary and everything before it are dropped",
			messages: []types.ChatMessage{
				chatMsg("system", "system prompt"),
				chatMsg("user", "old message 1"), chatMsg("assistant", "old response 1"),
				chatMsg("user", "old message 2"), chatMsg("assistant", "old response 2"),
				chatMsg("summarization", "summary of conversation"),
				chatMsg("user", "new message"), chatMsg("assistant", "new response"),
			},
			want: []types.ChatMessage{chatMsg("system", "system prompt"), chatMsg("user", "new message"), chatMsg("assistant", "new response")},
		},
		{
			name: "only the newest summary cuts",
			messages: []types.ChatMessage{
				chatMsg("system", "system prompt"),
				chatMsg("user", "very old message"), chatMsg("assistant", "very old response"),
				chatMsg("summarization", "first summary"),
				chatMsg("user", "mid message"), chatMsg("assistant", "mid response"),
				chatMsg("summarization", "second summary (includes first)"),
				chatMsg("user", "latest message"),
			},
			want: []types.ChatMessage{chatMsg("system", "system prompt"), chatMsg("user", "latest message")},
		},
		{
			name: "a pre-summary tool call goes with the conversation the summary replaced",
			messages: []types.ChatMessage{
				chatMsg("system", "system prompt"),
				chatMsg("user", "do something"),
				{Role: "assistant", Content: "ok", ToolCalls: []types.ToolCallMsg{callMsg("call1", "bash", "{}")}},
				toolAnswer("call1", "output"),
				chatMsg("summarization", "summary"),
				chatMsg("user", "done"),
			},
			want: []types.ChatMessage{chatMsg("system", "system prompt"), chatMsg("user", "done")},
		},
		{
			name: "a request_review turn stays",
			messages: []types.ChatMessage{
				chatMsg("system", "system prompt"),
				chatMsg("user", "fix the bug"),
				{Role: "assistant", Content: "I'll fix it", ToolCalls: []types.ToolCallMsg{callMsg("call1", "read_file", `{"path":"foo.go"}`)}},
				toolAnswer("call1", "file contents"),
				{Role: "assistant", Content: "Task done!", ToolCalls: []types.ToolCallMsg{callMsg("call2", "request_review", `{"summary":"fixed"}`)}},
				chatMsg("user", "now do something else"),
				chatMsg("assistant", "working on it"),
			},
			want: []types.ChatMessage{
				chatMsg("system", "system prompt"), chatMsg("user", "fix the bug"),
				{Role: "assistant", Content: "I'll fix it"}, {Role: "tool", Content: "file contents"},
				{Role: "assistant", Content: "Task done!"}, chatMsg("user", "now do something else"),
				chatMsg("assistant", "working on it"),
			},
			wantTools: []string{"request_review"},
		},
		{
			name: "an assistant row mixing a command with request_review stays whole",
			messages: []types.ChatMessage{
				chatMsg("system", "system prompt"),
				chatMsg("user", "fix and complete"),
				{Role: "assistant", ToolCalls: []types.ToolCallMsg{
					callMsg("call1", "bash", `{"command":"git status"}`),
					callMsg("call2", "request_review", `{"summary":"done"}`),
				}},
				toolAnswer("call1", "result"),
				toolAnswer("call2", "completed"),
				chatMsg("user", "next"),
			},
			want: []types.ChatMessage{
				chatMsg("system", "system prompt"), chatMsg("user", "fix and complete"),
				{Role: "assistant", Content: ""}, {Role: "tool", Content: "result"},
				{Role: "tool", Content: "completed"}, chatMsg("user", "next"),
			},
			wantTools: []string{"bash", "request_review"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := buildLLMContext(tc.messages)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d messages, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, want := range tc.want {
				if got[i].Role != want.Role || got[i].Content != want.Content {
					t.Errorf("message[%d] = %s:%q, want %s:%q", i, got[i].Role, got[i].Content, want.Role, want.Content)
				}
			}
			present := map[string]bool{}
			for _, m := range got {
				if m.Role == "summarization" {
					t.Errorf("a summarization row reached the conversation: %q", m.Content)
				}
				for _, call := range m.ToolCalls {
					present[call.Function.Name] = true
				}
			}
			for _, name := range tc.wantTools {
				if !present[name] {
					t.Errorf("the %s tool call was dropped", name)
				}
			}
		})
	}
}

// --- shouldSummarize Tests ---

// TestShouldSummarize covers the auto-summarization trigger. The boundary rows are the
// point: the threshold is inclusive (>=), so exactly 90% must fire, and an unusable window
// or threshold must never fire OR silently disable itself — an out-of-range threshold falls
// back to the 90% default rather than being trusted.
func TestShouldSummarize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		tokens    int
		window    int
		threshold int
		want      bool
	}{
		{name: "below threshold", tokens: 100, window: 32768, threshold: 90, want: false},
		{name: "exactly at threshold", tokens: 90, window: 100, threshold: 90, want: true},
		{name: "above threshold", tokens: 92, window: 100, threshold: 90, want: true},
		{name: "zero window never triggers", tokens: 100, window: 0, threshold: 90, want: false},
		{name: "custom threshold, above", tokens: 60, window: 100, threshold: 50, want: true},
		{name: "custom threshold, below", tokens: 40, window: 100, threshold: 50, want: false},
		// An unset (0) or out-of-range threshold falls back to the 90% default, so the 90%
		// sample must still trigger.
		{name: "unset threshold uses the default", tokens: 90, window: 100, threshold: 0, want: true},
		{name: "out-of-range threshold uses the default", tokens: 90, window: 100, threshold: 150, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldSummarize(tc.tokens, tc.window, tc.threshold); got != tc.want {
				t.Errorf("shouldSummarize(%d tokens, %d window, %d%%) = %v, want %v",
					tc.tokens, tc.window, tc.threshold, got, tc.want)
			}
		})
	}
}

func TestContextTokensUsed_RealUsagePreferred(t *testing.T) {
	t.Parallel()
	lc := &loopContext{
		usage: &types.ChatUsage{PromptTokens: 8000, CompletionTokens: 2000},
	}
	if got := contextTokensUsed(lc); got != 10000 {
		t.Errorf("contextTokensUsed should prefer real usage, got %d", got)
	}
}

func TestContextTokensUsed_HeuristicFallback(t *testing.T) {
	t.Parallel()
	// No real usage available -> falls back to heuristic estimate of messages.
	lc := &loopContext{
		messages: []types.ChatMessage{
			{Role: "system", Content: "system prompt"},
			{Role: "user", Content: "hello"},
		},
	}
	got := contextTokensUsed(lc)
	if got <= 0 {
		t.Errorf("contextTokensUsed fallback should estimate > 0 tokens, got %d", got)
	}
}

// mockSummarizeLLM returns an httptest server that answers a non-streaming chat
// completion (used by summarization) with a fixed summary.
func mockSummarizeLLM() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"summary text"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
}

// TestMaybeAutoSummarize covers the usage trigger end to end against a summarizing provider: a
// conversation past the threshold of its reported window is compacted, the "Proceed" that feeds the
// compressed context back to the model is appended and stored only when the loop is about to ask again,
// and a conversation under the threshold is left exactly as it was.
func TestMaybeAutoSummarize(t *testing.T) {
	// Summarization renders prompts/summarize.tmpl from the CWD — chdir to the
	// repo root so the template is found (mirrors how the running server resolves it).
	chdirToRepoRoot(t)

	for _, tc := range []struct {
		name          string
		promptTokens  int
		injectProceed bool
		wantSummary   bool
		wantProceed   bool
	}{
		{"below the threshold nothing happens", 5000, false, false, false},
		{"above the threshold, not fed back", 9200, false, true, false},
		{"above the threshold, fed back to the loop", 9200, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			te := newTestExecutor(t)
			te.gate = NewConcurrencyGate()
			s := te.store
			proj := createProject(t, s, "proj-sum-"+strings.ReplaceAll(tc.name, " ", "-"), "Sum")

			server := mockSummarizeLLM()
			defer server.Close()

			now := time.Now().UTC()
			taskID := "sum-" + fmt.Sprint(tc.promptTokens) + "-" + fmt.Sprint(tc.injectProceed)
			task := &types.Task{
				ID:              taskID,
				ProjectID:       proj.ID,
				Title:           "Sum task",
				Status:          types.StatusReview,
				ExecutionState:  types.ExecStateIdle,
				LlamaServerURL:  server.URL,
				ModelName:       "test-model",
				CurrentThreadID: "thread-1",
				CreatedAt:       now,
				UpdatedAt:       now,
			}
			if err := s.CreateTask(task); err != nil {
				t.Fatalf("create task: %v", err)
			}

			// The provider's own count of the conversation, against a 10000-token window.
			cw := 10000
			lc := &loopContext{
				messages:      []types.ChatMessage{{Role: "user", Content: "hello"}},
				contextWindow: &cw,
				usage:         &types.ChatUsage{PromptTokens: tc.promptTokens, CompletionTokens: 0},
				detector:      newLoopDetector(),
				mc:            &modelConfig{serverURL: server.URL, modelName: "test-model"},
			}
			setup := &taskExecSetup{
				task:      task,
				projectID: proj.ID,
				ctx:       context.Background(),
				serverURL: server.URL,
				modelName: "test-model",
			}

			te.summarize.maybeAutoSummarize(setup, lc, "test-model", tc.injectProceed)

			summaryIdx, proceedIdx, proceedStored := -1, -1, false
			for i, m := range lc.messages {
				if m.Role == "summarization" {
					summaryIdx = i
				}
				if m.Role == "user" && m.Content == "Proceed" {
					proceedIdx = i
				}
			}
			msgs, err := s.GetTaskMessages(task.ID, "thread-1")
			if err != nil {
				t.Fatalf("get messages: %v", err)
			}
			for _, m := range msgs {
				if m.Role == "user" && m.Content == "Proceed" {
					proceedStored = true
				}
			}

			if tc.wantSummary != (summaryIdx >= 0) {
				t.Fatalf("summarization row = %t, want %t (prompt_tokens %d of %d)",
					summaryIdx >= 0, tc.wantSummary, tc.promptTokens, cw)
			}
			if !tc.wantSummary {
				return
			}
			// The Proceed, when there is one, is last: it follows the summary and the reproduction
			// of what the summary dropped.
			if tc.wantProceed != (proceedIdx >= 0) || tc.wantProceed != proceedStored {
				t.Errorf("the Proceed message: in memory = %t, stored = %t, want both = %t",
					proceedIdx >= 0, proceedStored, tc.wantProceed)
			}
			if tc.wantProceed && summaryIdx > len(lc.messages)-2 {
				t.Errorf("expected rows after the summary, got %d messages with summary at index %d",
					len(lc.messages), summaryIdx)
			}
			if task.Summarizing {
				t.Error("Summarizing flag should be cleared on the caller's task after summarization")
			}
		})
	}
}

// --- Reasoning-Only Loop Detection Tests ---

// TestLoopDetector_ResetClearsStreak verifies that detector.reset() clears loopStreak,
// which is the mechanism used when a user action (e.g., message delete) changes context.
func TestLoopDetector_ResetClearsStreak(t *testing.T) {
	t.Parallel()
	te := newTestExecutor(t)
	now := time.Now().UTC()

	proj := &types.Project{ID: "proj-reset-streak", Name: "Reset Streak", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}
	if err := te.store.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := &types.Task{
		ID: "task-reset-streak", ProjectID: proj.ID, Title: "Test", Prompt: "Test",
		Status: types.StatusInProgress, ExecutionState: types.ExecStateRunning,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := te.store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	detector := newLoopDetector()
	messages := []types.ChatMessage{{Role: "system", Content: "system prompt"}}

	// Build up a streak of 2
	handleLoopDetected(te, task, proj.ID, task.ID, detector, LoopReasoningOnly, "", &messages, false)
	handleLoopDetected(te, task, proj.ID, task.ID, detector, LoopReasoningOnly, "", &messages, false)
	if detector.loopStreak != 2 {
		t.Fatalf("expected loopStreak=2, got %d", detector.loopStreak)
	}

	// Simulate user action: reset detector (what ContextChanged path does)
	detector.reset()
	if detector.loopStreak != 0 {
		t.Errorf("expected loopStreak=0 after reset, got %d", detector.loopStreak)
	}

	// After reset, first 2 of 3 should NOT terminate (fresh streak)
	for i := 0; i < 2; i++ {
		terminated := handleLoopDetected(te, task, proj.ID, task.ID, detector, LoopReasoningOnly, "", &messages, false)
		if terminated {
			t.Fatalf("hint %d should not terminate (fresh streak, %d/3)", i+1, i+2)
		}
	}
	// Task should still be running (not failed from the old streak)
	taskAfter, _ := te.store.GetTaskByID(task.ID)
	if taskAfter.ExecutionState == types.ExecStateFailed {
		t.Error("task should not be failed — reset cleared the old streak")
	}
}

// TestNotifyContextChanged_SendsToChannel verifies that NotifyContextChanged
// sends a UserMessage with ContextChanged=true to the executor channel.
func TestNotifyContextChanged_SendsToChannel(t *testing.T) {
	t.Parallel()
	te := newTestExecutor(t)

	// Simulate an execution state with a userMsgChan
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	userMsgChan := make(chan stream.UserMessage, 16)

	te.taskMu.Lock()
	te.tasks["test-task"] = &executionState{
		ctx:         ctx,
		cancel:      cancel,
		userMsgChan: userMsgChan,
		summarizeCh: make(chan *summarizeRequest, 16),
	}
	te.taskMu.Unlock()

	// Call NotifyContextChanged
	te.NotifyContextChanged("test-task")

	// Verify the channel received the notification
	select {
	case um := <-userMsgChan:
		if !um.ContextChanged {
			t.Error("expected ContextChanged=true")
		}
		if um.Content != "" {
			t.Errorf("expected empty content, got %q", um.Content)
		}
	default:
		t.Fatal("no message sent to channel")
	}
}

// TestNotifyContextChanged_NoOpWhenNoTask verifies that NotifyContextChanged
// is a no-op when no executor is running for the task.
func TestNotifyContextChanged_NoOpWhenNoTask(t *testing.T) {
	t.Parallel()
	te := newTestExecutor(t)

	// Should not panic when no task is running
	te.NotifyContextChanged("nonexistent-task")

	te.taskMu.Lock()
	_, exists := te.tasks["nonexistent-task"]
	te.taskMu.Unlock()
	if exists {
		t.Error("a notification for an unstarted task created execution state for it")
	}
}

// --- streamFlusher Tests ---

// TestStreamFlusher_BatchesEachBufferIntoOneEvent: appended tokens and appended reasoning each reach the
// wire once per flush, joined in the order they arrived — batching per token would put a frame on the
// socket for every chunk the provider sent.
func TestStreamFlusher_BatchesEachBufferIntoOneEvent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		wantType types.WSMessageType
		append   func(*streamFlusher, string)
		chunks   []string
		want     string
	}{
		{"tokens", types.WSTypeToken, (*streamFlusher).appendToken, []string{"Hello", " ", "world"}, "Hello world"},
		{"reasoning", types.WSTypeReasoning, (*streamFlusher).appendReasoning, []string{"Thinking", " about", " the problem"}, "Thinking about the problem"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []types.StreamEvent
			flusher := newStreamFlusher("proj", "task", func(_, _ string, e types.StreamEvent) {
				events = append(events, e)
			}, newLoopDetector())

			for _, chunk := range tc.chunks {
				tc.append(flusher, chunk)
			}
			if len(events) != 0 {
				t.Fatalf("expected 0 events before flush, got %d", len(events))
			}

			flusher.flush()
			if len(events) != 1 {
				t.Fatalf("expected 1 event after flush, got %d", len(events))
			}
			if events[0].Type != tc.wantType {
				t.Fatalf("expected %v, got %v", tc.wantType, events[0].Type)
			}
			if text := events[0].Data.(map[string]string)["text"]; text != tc.want {
				t.Errorf("expected %q, got %q", tc.want, text)
			}
		})
	}
}

func TestStreamFlusher_ToolCallBatching(t *testing.T) {
	t.Parallel()
	var events []types.StreamEvent
	flusher := newStreamFlusher("proj", "task", func(_, _ string, e types.StreamEvent) {
		events = append(events, e)
	}, newLoopDetector())

	// Simulate streaming tool call args: {"command":"ls -la"}
	// First chunk: new tool call with partial args
	flusher.appendToolCall("call_1", "bash", "Run Command", "", `{"command":"ls`, 14, 0, false)
	// Second chunk: args grew
	flusher.appendToolCall("call_1", "bash", "Run Command", "", `{"command":"ls -la"}`, 20, 14, false)

	flusher.flush()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != types.WSTypeToolCall {
		t.Fatalf("expected WSTypeToolCall, got %v", events[0].Type)
	}
	data := events[0].Data.(map[string]any)
	// New tool call: sends full raw_args
	if data["raw_args"] != `{"command":"ls -la"}` {
		t.Errorf("expected full raw_args, got %v", data["raw_args"])
	}
	if _, ok := data["raw_args_delta"]; ok {
		t.Error("should NOT have raw_args_delta for new tool call")
	}
}

func TestStreamFlusher_ToolCallUpdateDelta(t *testing.T) {
	t.Parallel()
	var events []types.StreamEvent
	flusher := newStreamFlusher("proj", "task", func(_, _ string, e types.StreamEvent) {
		events = append(events, e)
	}, newLoopDetector())

	// Simulate existing tool call (prevLen > 0, so it's an update)
	flusher.appendToolCall("call_2", "bash", "Run Command", "", `{"command":"ls -la /home"}`, 26, 14, false)

	flusher.flush()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	data := events[0].Data.(map[string]any)
	// Update: sends delta from start position
	if data["raw_args_delta"] != ` -la /home"}` {
		t.Errorf("expected delta ' -la /home\"}', got %v", data["raw_args_delta"])
	}
	if _, ok := data["raw_args"]; ok {
		t.Error("should NOT have raw_args for update")
	}
}

// TestStreamFlusher_ToolCallDeltaBoundsFallBackToFullArgs covers the third branch of the flush:
// delta bounds that would slice out of range. The client gets the whole argument string rather than
// the turn dying on a slice panic.
func TestStreamFlusher_ToolCallDeltaBoundsFallBackToFullArgs(t *testing.T) {
	t.Parallel()
	const args = `{"command":"ls"}`
	cases := []struct {
		name           string
		argLen, prevLn int
	}{
		{"start past current", 14, 20},
		{"current past the recorded args", 99, 5},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var events []types.StreamEvent
			flusher := newStreamFlusher("proj", "task", func(_, _ string, e types.StreamEvent) {
				events = append(events, e)
			}, newLoopDetector())

			flusher.appendToolCall(fmt.Sprintf("call_bounds_%d", i), "bash", "Run Command", "", args, tc.argLen, tc.prevLn, false)
			flusher.flush()

			if len(events) != 1 {
				t.Fatalf("expected 1 event, got %d", len(events))
			}
			data := events[0].Data.(map[string]any)
			if data["raw_args"] != args {
				t.Errorf("raw_args = %v, want the whole %q", data["raw_args"], args)
			}
			if _, ok := data["raw_args_delta"]; ok {
				t.Errorf("raw_args_delta = %v, want no delta when the bounds are unusable", data["raw_args_delta"])
			}
		})
	}
}

func TestStreamFlusher_MixedEvents(t *testing.T) {
	t.Parallel()
	var events []types.StreamEvent
	flusher := newStreamFlusher("proj", "task", func(_, _ string, e types.StreamEvent) {
		events = append(events, e)
	}, newLoopDetector())

	// Mix of token, reasoning, and tool_call
	flusher.appendToken("Hello")
	flusher.appendReasoning("thinking...")
	flusher.appendToolCall("call_1", "bash", "Run Command", "", `{"command":"ls"}`, 16, 0, false)

	flusher.flush()
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	// Verify event types
	eventTypes := make(map[types.WSMessageType]bool)
	for _, e := range events {
		eventTypes[e.Type] = true
	}
	if !eventTypes[types.WSTypeToken] || !eventTypes[types.WSTypeReasoning] || !eventTypes[types.WSTypeToolCall] {
		t.Errorf("expected all three event types, got %v", eventTypes)
	}
}

func TestStreamFlusher_Reset(t *testing.T) {
	t.Parallel()
	var events []types.StreamEvent
	flusher := newStreamFlusher("proj", "task", func(_, _ string, e types.StreamEvent) {
		events = append(events, e)
	}, newLoopDetector())

	flusher.appendToken("Hello")
	flusher.appendReasoning("thinking")
	flusher.appendToolCall("call_1", "bash", "Run Command", "", `{"command":"ls"}`, 16, 0, false)

	// Reset should clear all buffers
	flusher.reset()

	// Flush should send nothing
	flusher.flush()
	if len(events) != 0 {
		t.Fatalf("expected 0 events after reset+flush, got %d", len(events))
	}
}

func TestStreamFlusher_MultipleToolCalls(t *testing.T) {
	t.Parallel()
	var events []types.StreamEvent
	flusher := newStreamFlusher("proj", "task", func(_, _ string, e types.StreamEvent) {
		events = append(events, e)
	}, newLoopDetector())

	// Two separate tool calls
	flusher.appendToolCall("call_1", "bash", "Run Command", "", `{"command":"ls"}`, 16, 0, false)
	flusher.appendToolCall("call_2", "read_file", "Read File", "./foo.txt", `{"path":"./foo.txt"}`, 19, 0, false)

	flusher.flush()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	// Verify both tool calls are present
	toolIDs := make(map[string]bool)
	for _, e := range events {
		data := e.Data.(map[string]any)
		toolIDs[data["tool_id"].(string)] = true
	}
	if !toolIDs["call_1"] || !toolIDs["call_2"] {
		t.Errorf("expected both tool IDs, got %v", toolIDs)
	}
}

func TestStreamFlusher_SecondFlush(t *testing.T) {
	t.Parallel()
	var events []types.StreamEvent
	flusher := newStreamFlusher("proj", "task", func(_, _ string, e types.StreamEvent) {
		events = append(events, e)
	}, newLoopDetector())

	// First batch
	flusher.appendToken("Hello ")
	flusher.flush()

	// Second batch
	flusher.appendToken("world")
	flusher.flush()

	if len(events) != 2 {
		t.Fatalf("expected 2 events (one per flush), got %d", len(events))
	}
	if events[0].Data.(map[string]string)["text"] != "Hello " {
		t.Errorf("expected first batch 'Hello ', got %q", events[0].Data.(map[string]string)["text"])
	}
	if events[1].Data.(map[string]string)["text"] != "world" {
		t.Errorf("expected second batch 'world', got %q", events[1].Data.(map[string]string)["text"])
	}
}

// --- Reasoning Content Tests ---

func TestBuildMessagesFromDB_KeepsReasoningSeparate(t *testing.T) {
	t.Parallel()
	// Simulate DB messages with reasoning content
	taskMsgs := []types.TaskMessage{
		{Role: "user", Content: "Build a feature"},
		{Role: "assistant", Content: "I'll implement it", ReasoningContent: "Let me think about the approach..."},
		{Role: "assistant", Content: "Done!", ReasoningContent: ""},
		{Role: "assistant", Content: "", ReasoningContent: "I need to reason before acting"},
	}

	task := &types.Task{OperatingMode: "act"}
	messages := buildMessagesFromDB(task, taskMsgs, "/worktree", promptScope{projectName: "proj"})

	// Skip system prompt (index 0)
	if len(messages) != 5 {
		t.Fatalf("expected 5 messages (system + 4), got %d", len(messages))
	}

	if messages[1].Role != "user" || messages[1].Content != "Build a feature" {
		t.Errorf("user message wrong: role=%s, content=%s", messages[1].Role, messages[1].Content)
	}

	// Assistant with reasoning — ReasoningContent must be separate, NOT concatenated into Content
	if messages[2].Role != "assistant" {
		t.Errorf("expected assistant, got %s", messages[2].Role)
	}
	if messages[2].Content != "I'll implement it" {
		t.Errorf("expected Content='I'll implement it', got %q", messages[2].Content)
	}
	if messages[2].ReasoningContent != "Let me think about the approach..." {
		t.Errorf("expected ReasoningContent='Let me think about the approach...', got %q", messages[2].ReasoningContent)
	}

	if messages[3].Content != "Done!" || messages[3].ReasoningContent != "" {
		t.Errorf("assistant without reasoning wrong: Content=%q, ReasoningContent=%q", messages[3].Content, messages[3].ReasoningContent)
	}

	// Reasoning-only assistant (empty content)
	if messages[4].Content != "" {
		t.Errorf("reasoning-only: expected empty Content, got %q", messages[4].Content)
	}
	if messages[4].ReasoningContent != "I need to reason before acting" {
		t.Errorf("reasoning-only: expected ReasoningContent='I need to reason before acting', got %q", messages[4].ReasoningContent)
	}
}

func TestBuildMessagesFromDB_ExcludesLoopHints(t *testing.T) {
	t.Parallel()
	taskMsgs := []types.TaskMessage{
		{Role: "user", Content: "Build a feature"},
		{Role: "user", Content: "You repeated yourself in your last response.", IsLoopHint: true},
		{Role: "assistant", Content: "I'll implement it"},
	}

	task := &types.Task{OperatingMode: "act"}
	messages := buildMessagesFromDB(task, taskMsgs, "/worktree", promptScope{projectName: "proj"})

	// system + user + assistant = 3 (hint excluded)
	if len(messages) != 3 {
		t.Fatalf("expected 3 messages (loop hint excluded), got %d", len(messages))
	}
	if messages[1].Role != "user" || messages[1].Content != "Build a feature" {
		t.Errorf("expected original user message first, got role=%s content=%q", messages[1].Role, messages[1].Content)
	}
	if messages[2].Content != "I'll implement it" {
		t.Errorf("expected assistant message, got %q", messages[2].Content)
	}
}

func TestBuildMessagesFromDB_ToolCallWithReasoning(t *testing.T) {
	t.Parallel()
	taskMsgs := []types.TaskMessage{
		{Role: "user", Content: "Read a file"},
		{Role: "assistant", Content: "I'll read it", ReasoningContent: "Need to check the file first"},
		{Role: "tool_call", ToolCalls: `{"tool_id":"call_1","tool_name":"read_file","args":{"path":"foo.txt"}}`, ToolResult: `{"output":"file contents","success":true,"duration":10}`},
	}

	task := &types.Task{OperatingMode: "act"}
	messages := buildMessagesFromDB(task, taskMsgs, "/worktree", promptScope{projectName: "proj"})

	// system + user + assistant + tool = 4
	if len(messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(messages))
	}

	// Assistant message: reasoning should be separate, tool calls merged
	if messages[2].ReasoningContent != "Need to check the file first" {
		t.Errorf("expected ReasoningContent='Need to check the file first', got %q", messages[2].ReasoningContent)
	}
	if messages[2].Content != "I'll read it" {
		t.Errorf("expected Content='I'll read it', got %q", messages[2].Content)
	}
	if len(messages[2].ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call on assistant message, got %d", len(messages[2].ToolCalls))
	}
	if messages[2].ToolCalls[0].ID != "call_1" {
		t.Errorf("expected tool call ID 'call_1', got %q", messages[2].ToolCalls[0].ID)
	}
	if messages[2].ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("expected tool name 'read_file', got %q", messages[2].ToolCalls[0].Function.Name)
	}

	// Tool result message
	if messages[3].Role != "tool" {
		t.Errorf("expected tool role, got %s", messages[3].Role)
	}
	if messages[3].ToolCallID != "call_1" {
		t.Errorf("expected ToolCallID='call_1', got %q", messages[3].ToolCallID)
	}
	if messages[3].Content != "file contents" {
		t.Errorf("expected Content='file contents', got %q", messages[3].Content)
	}
}

func TestBuildMessagesFromDB_ReasoningOnlyWithToolCalls(t *testing.T) {
	t.Parallel()
	// Bug: assistant with only reasoning (no text content) + tool calls was
	// invisible in chat history because ToolCalls were not merged from DB.
	taskMsgs := []types.TaskMessage{
		{Role: "user", Content: "Read config and fix it"},
		{Role: "assistant", Content: "", ReasoningContent: "I need to read the config first to understand the issue"},
		{Role: "tool_call", ToolCalls: `{"tool_id":"call_1","tool_name":"read_file","raw_args":"{\"path\":\"config.yaml\"}"}`, ToolResult: `{"output":"key: value","success":true,"duration":5}`},
		{Role: "assistant", Content: "", ReasoningContent: "Now I'll fix the config"},
		{Role: "tool_call", ToolCalls: `{"tool_id":"call_2","tool_name":"edit_file","raw_args":"{\"path\":\"config.yaml\"}"}`, ToolResult: `{"output":"edited","success":true,"duration":8}`},
	}

	task := &types.Task{OperatingMode: "act"}
	messages := buildMessagesFromDB(task, taskMsgs, "/worktree", promptScope{projectName: "proj"})

	// system + user + assistant(read_file) + tool + assistant(edit_file) + tool = 6
	if len(messages) != 6 {
		t.Fatalf("expected 6 messages, got %d", len(messages))
		for i, m := range messages {
			t.Logf("  [%d] role=%s content=%q reasoning=%q toolCalls=%d", i, m.Role, m.Content, m.ReasoningContent, len(m.ToolCalls))
		}
	}

	// First reasoning-only assistant should have tool call merged
	if len(messages[2].ToolCalls) != 1 {
		t.Errorf("expected 1 tool call on first reasoning-only assistant, got %d", len(messages[2].ToolCalls))
	}
	if messages[2].ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("expected tool 'read_file', got %q", messages[2].ToolCalls[0].Function.Name)
	}
	if messages[2].Content != "" {
		t.Errorf("expected empty content, got %q", messages[2].Content)
	}
	if messages[2].ReasoningContent != "I need to read the config first to understand the issue" {
		t.Errorf("unexpected reasoning: %q", messages[2].ReasoningContent)
	}

	// Second reasoning-only assistant should also have tool call merged
	if len(messages[4].ToolCalls) != 1 {
		t.Errorf("expected 1 tool call on second reasoning-only assistant, got %d", len(messages[4].ToolCalls))
	}
	if messages[4].ToolCalls[0].Function.Name != "edit_file" {
		t.Errorf("expected tool 'edit_file', got %q", messages[4].ToolCalls[0].Function.Name)
	}
}

func TestFormatChatHistory_ReasoningOnlyWithToolCalls(t *testing.T) {
	t.Parallel()
	// Verify that reasoning-only assistant messages with tool calls appear
	// in chat history as "I used the X tool to..." lines.
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "Read a file"},
		{Role: "assistant", Content: "", ReasoningContent: "Let me check the file", ToolCalls: []types.ToolCallMsg{
			{ID: "call_1", Type: "function"},
		}},
		{Role: "tool", Content: "file contents", ToolCallID: "call_1"},
	}
	messages[2].ToolCalls[0].Function.Name = "read_file"
	messages[2].ToolCalls[0].Function.Arguments = `{"path":"foo.txt"}`

	history := formatChatHistory(messages, false)

	// Should contain the tool use line even though assistant had no text content
	if !strings.Contains(history, "I used the read_file tool") {
		t.Errorf("chat history missing tool use line. Got:\n%s", history)
	}
	// Should NOT be empty/missing the assistant turn
	if !strings.Contains(history, "Assistant:") {
		t.Errorf("chat history missing Assistant prefix. Got:\n%s", history)
	}
	// Should contain user message
	if !strings.Contains(history, "User: Read a file") {
		t.Errorf("chat history missing user message. Got:\n%s", history)
	}
}

// --- removeDuplicateUserMessage Tests ---

// TestBuildContextReplay covers the transcript a context-change notice replays to the model: every role
// has its own label, reasoning is quoted as the model's thinking rather than its answer, a tool call is
// shown with what it was asked to do and what came back, and a summarization is marked as one so the model
// knows the conversation it is reading was compressed.
func TestBuildContextReplay(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		msgs     []types.TaskMessage
		contains []string
	}{
		{
			name:     "an empty thread still frames itself",
			msgs:     nil,
			contains: []string{"<conversation_history>", "</conversation_history>"},
		},
		{
			name: "user and assistant turns are labelled",
			msgs: []types.TaskMessage{
				{Role: "user", Content: "Build a todo app"},
				{Role: "assistant", Content: "Sure, let me create the files."},
				{Role: "user", Content: "Also add tests"},
			},
			contains: []string{"**User**: Build a todo app", "**Assistant**: Sure, let me create the files.", "**User**: Also add tests"},
		},
		{
			name:     "reasoning is replayed as thinking",
			msgs:     []types.TaskMessage{{Role: "assistant", ReasoningContent: "Let me think about this...", Content: "Here's the answer."}},
			contains: []string{"**Assistant (thinking)**: Let me think about this...", "**Assistant**: Here's the answer."},
		},
		{
			name: "a tool call is shown with its result",
			msgs: []types.TaskMessage{{
				Role:       "tool_call",
				ToolCalls:  `[{"id":"call_1","name":"write_file","display_name":"Write File","args":{"path":"test.js","content":"hello"}}]`,
				ToolResult: `[{"id":"call_1","output":"File written","success":true}]`,
			}},
			contains: []string{"**Tool calls**:", "Write File", "**Tool results**:", "File written"},
		},
		{
			name: "a summarization is marked as one",
			msgs: []types.TaskMessage{
				{Role: "user", Content: "First message"},
				{Role: "summarization", Content: "Summary of earlier conversation"},
				{Role: "user", Content: "After summary"},
			},
			contains: []string{"**[Context was summarized]**", "Summary of earlier conversation"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := buildContextReplay(tc.msgs)
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("replay missing %q, got:\n%s", want, got)
				}
			}
		})
	}
}

// --- Backoff Policy Tests ---

// assertBackoffWithin fails the test if got is outside [min, max].
func assertBackoffWithin(t *testing.T, name string, got, min, max time.Duration) {
	t.Helper()
	if got < min || got > max {
		t.Errorf("%s: backoff = %v, want within [%v, %v]", name, got, min, max)
	}
}

// TestBackoffFor covers the retry ladder. A Retry-After is honoured exactly, uncapped, on every attempt —
// a provider that says ten hours means ten hours — and only for a 429: a server error keeps the short
// exponential ladder however its body is worded, because nothing told us to wait.
func TestBackoffFor(t *testing.T) {
	t.Parallel()
	const (
		jitterMin = 250 * time.Millisecond
		jitterMax = 500 * time.Millisecond
	)
	for _, tc := range []struct {
		name    string
		err     error
		attempt int
		min     time.Duration
		max     time.Duration
	}{
		{"429 honours Retry-After on the first attempt", &llama.LlamaError{StatusCode: 429, RetryAfter: 20 * time.Second}, 0, 21*time.Second + jitterMin, 21*time.Second + jitterMax},
		{"429 honours Retry-After again later", &llama.LlamaError{StatusCode: 429, RetryAfter: 20 * time.Second}, 2, 21*time.Second + jitterMin, 21*time.Second + jitterMax},
		{"a huge Retry-After is not capped", &llama.LlamaError{StatusCode: 429, RetryAfter: 10 * time.Hour}, 0, 10*time.Hour + time.Second + jitterMin, 10*time.Hour + time.Second + jitterMax},
		{"429 without Retry-After escalates", &llama.LlamaError{StatusCode: 429}, 0, 2 * time.Second, 2 * time.Second},
		{"429 second attempt", &llama.LlamaError{StatusCode: 429}, 1, 4 * time.Second, 4 * time.Second},
		{"429 third attempt", &llama.LlamaError{StatusCode: 429}, 2, 8 * time.Second, 8 * time.Second},
		{"a transport fault escalates the same way", errors.New("connection reset"), 0, 2 * time.Second, 2 * time.Second},
		{"a transport fault second attempt", errors.New("connection reset"), 1, 4 * time.Second, 4 * time.Second},
		{"a transport fault third attempt", errors.New("connection reset"), 2, 8 * time.Second, 8 * time.Second},
		{"a server error ignores Retry-After", &llama.LlamaError{StatusCode: 500, RetryAfter: 30 * time.Second}, 0, 2 * time.Second, 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertBackoffWithin(t, "attempt "+fmt.Sprint(tc.attempt), backoffFor(tc.attempt, tc.err), tc.min, tc.max)
		})
	}
}
