package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"kanban/backend/llama"
	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/types"
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
	d := newPhraseDetector()

	// Feed unique sentences
	text := "The quick brown fox jumps over the lazy dog today. Then the cat sat on the mat and slept all day long."
	if d.check(text) {
		t.Error("should not detect repetition in unique text")
	}
}

func TestPhraseDetector_ExactRepetition(t *testing.T) {
	d := newPhraseDetector()

	if !repeatPhrase(d, testPhrase) {
		t.Error("should detect exact phrase repetition")
	}
}

func TestPhraseDetector_CaseInsensitive(t *testing.T) {
	d := newPhraseDetector()

	if !repeatPhrase(d, testPhrase) {
		t.Error("should detect case-insensitive repetition")
	}
}

func TestPhraseDetector_ShortPhraseIgnored(t *testing.T) {
	d := newPhraseDetector()

	// Short phrase (< 8 words) should be ignored
	short := "Hello world."
	d.check(short)
	if d.check(short) {
		t.Error("should not fire on short phrases")
	}
}

func TestPhraseDetector_WhitespaceNormalization(t *testing.T) {
	d := newPhraseDetector()

	if !repeatPhrase(d, testPhrase) {
		t.Error("should detect repetition despite extra whitespace")
	}
}

func TestPhraseDetector_FiresOnce(t *testing.T) {
	d := newPhraseDetector()

	repeatPhrase(d, testPhrase) // fires
	// After first fire, should still return false (already fired)
	if d.check(testPhrase) {
		t.Error("should not fire again after already fired")
	}
}

func TestPhraseDetector_Reset(t *testing.T) {
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
	d := newPhraseDetector()

	// Feed the phrase multiple times in one chunk to reach threshold
	text := strings.Repeat(testPhrase+" ", maxPhraseRepeats-1) + testPhrase
	if !d.check(text) {
		t.Error("should detect repetition when repeated sentence appears later in same chunk")
	}
}

func TestPhraseDetector_NewlineBoundary(t *testing.T) {
	d := newPhraseDetector()

	if !repeatPhrase(d, testPhrase+"\n") {
		t.Error("should detect repetition across newline boundary")
	}
}

func TestPhraseDetector_DoubleNewlineBoundary(t *testing.T) {
	d := newPhraseDetector()

	if !repeatPhrase(d, testPhrase+"\n\n") {
		t.Error("should detect repetition across double newline boundary")
	}
}

func TestPhraseDetector_CodeBlockNotTriggered(t *testing.T) {
	d := newPhraseDetector()

	// Code-like content without sentence boundaries
	code := "func main() { fmt.Println(\"hello\") }"
	d.check(code)
	if d.check(code) {
		t.Error("should not trigger on code without sentence boundaries")
	}
}

func TestPhraseDetector_ExclamationBoundary(t *testing.T) {
	d := newPhraseDetector()

	if !repeatPhrase(d, "Wow this is really amazing and incredible stuff!") {
		t.Error("should detect repetition with exclamation boundary")
	}
}

func TestPhraseDetector_QuestionBoundary(t *testing.T) {
	d := newPhraseDetector()

	if !repeatPhrase(d, "What do you think about this approach and implementation?") {
		t.Error("should detect repetition with question boundary")
	}
}

func TestPhraseDetector_IncompleteSentenceAccumulates(t *testing.T) {
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
	d := newLoopDetector()

	// First read of file
	args1 := map[string]string{"path": "src/main.go"}
	args1JSON, _ := json.Marshal(args1)
	tc1 := []types.ChatToolCall{makeToolCall("read_file", string(args1JSON))}
	if loopType, _, _ := d.recordTurn(tc1, "", "tool_calls"); loopType != LoopNone {
		t.Error("first read should not trigger loop")
	}

	// Second read of same file
	tc2 := []types.ChatToolCall{makeToolCall("read_file", string(args1JSON))}
	if loopType, _, _ := d.recordTurn(tc2, "", "tool_calls"); loopType != LoopTool {
		t.Error("second read of same file should trigger LoopTool")
	}
}

// --- Rolling Window Tests ---

func TestLoopDetector_LoopStreakIncrementsOnConsecutiveLoops(t *testing.T) {
	d := newLoopDetector()

	// Simulate handleLoopDetected: increments loopStreak, reset preserves it
	simulateHint := func() {
		d.loopStreak++
		streak := d.loopStreak
		d.reset()
		d.loopStreak = streak
	}

	// 3 consecutive loops (no valid turns in between)
	for i := 0; i < 3; i++ {
		simulateHint()
	}

	// loopStreak should be 3
	if d.loopStreak != 3 {
		t.Errorf("expected loopStreak=3, got %d", d.loopStreak)
	}
}

func TestLoopDetector_ValidTurnResetsLoopStreak(t *testing.T) {
	d := newLoopDetector()

	// Simulate 2 consecutive loop hints
	d.loopStreak = 2

	// A valid turn resets loopStreak to 0
	args := map[string]string{"path": "src/other.go"}
	argsJSON, _ := json.Marshal(args)
	tc := []types.ChatToolCall{makeToolCall("read_file", string(argsJSON))}
	loopType, _, _ := d.recordTurn(tc, "", "tool_calls")
	if loopType != LoopNone {
		t.Fatalf("expected LoopNone, got %v", loopType)
	}
	if d.loopStreak != 0 {
		t.Errorf("expected loopStreak=0 after valid turn, got %d", d.loopStreak)
	}
}

func TestLoopDetector_LoopStreakResetsOnValidTurn(t *testing.T) {
	d := newLoopDetector()

	// Simulate loop streak
	d.loopStreak = 2

	// Valid turn resets streak
	args := map[string]string{"path": "src/other.go"}
	argsJSON, _ := json.Marshal(args)
	tc := []types.ChatToolCall{makeToolCall("read_file", string(argsJSON))}
	loopType, _, _ := d.recordTurn(tc, "", "tool_calls")
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
	d.recordTurn(tc, "", "tool_calls")                  // first read, valid (loopStreak stays 0)
	loopType, _, _ = d.recordTurn(tc, "", "tool_calls") // second read, loop
	if loopType != LoopTool {
		t.Fatal("expected LoopTool")
	}
	// handleLoopDetected increments
	d.loopStreak++
	if d.loopStreak != 1 {
		t.Errorf("expected loopStreak=1 (fresh streak), got %d", d.loopStreak)
	}
}

func TestLoopDetector_ResetClearsLoopStreak(t *testing.T) {
	d := newLoopDetector()

	// Manually set loopStreak
	d.loopStreak = 2

	// reset() clears loopStreak
	d.reset()
	if d.loopStreak != 0 {
		t.Errorf("expected loopStreak=0 after reset, got %d", d.loopStreak)
	}
}

func TestLoopDetector_LoopStreakWindowTextOnly(t *testing.T) {
	d := newLoopDetector()

	// Simulate 2 consecutive loop hints
	d.loopStreak = 2

	// Text-only responses now trigger LoopStatement (not LoopNone)
	// This means the streak is NOT reset by text-only responses
	texts := []string{
		"The database migration completed successfully and all tables are now created with the correct schema definitions",
		"I wrote comprehensive unit tests covering edge cases and boundary conditions for the authentication module",
		"The deployment pipeline is configured with proper error handling and notification hooks for the staging environment",
	}
	for _, text := range texts {
		loopType, _, _ := d.recordTurn(nil, text, "stop")
		if loopType != LoopStatement {
			t.Fatalf("expected LoopStatement, got %v", loopType)
		}
	}
	// loopStreak is preserved (not reset) because LoopStatement is a loop detection
	// The streak counter is managed by handleLoopDetected (increment + preserve across reset)
	if d.loopStreak != 2 {
		t.Errorf("expected loopStreak=2 (preserved across LoopStatement), got %d", d.loopStreak)
	}
}

func TestHandleLoopDetected_IncrementsAndPreservesLoopStreak(t *testing.T) {
	e := newTestExecutor(t)
	now := time.Now().UTC()

	proj := &types.Project{ID: "proj-streak", Name: "Streak Test", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}
	if err := e.store.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &types.Task{
		ID: "task-streak", ProjectID: proj.ID, Title: "Test", Prompt: "Test",
		Status: "in_progress", ExecutionState: "running",
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
	e := newTestExecutor(t)
	now := time.Now().UTC()

	proj := &types.Project{ID: "proj-exploration", Name: "exploration", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}
	_ = e.store.CreateProject(proj)
	task := &types.Task{
		ID:            "task-exploration",
		ProjectID:     proj.ID,
		Title:         "Ideation task",
		Prompt:        "Brainstorm",
		Status:        "in_progress",
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
	taskAfter, _ := e.store.GetTaskByTaskID(task.ID)
	if taskAfter.ExecutionState == "failed" {
		t.Error("task should not be in failed state in explore mode")
	}
}

// TestHandleLoopDetected_HintBroadcastCarriesMessageID verifies the hint
// broadcast carries the persisted message ID (frontend dedupes on history reload).
func TestHandleLoopDetected_HintBroadcastCarriesMessageID(t *testing.T) {
	e := newTestExecutor(t)
	now := time.Now().UTC()

	proj := &types.Project{ID: "proj-hint-id", Name: "Hint ID Test", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}
	if err := e.store.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &types.Task{
		ID: "task-hint-id", ProjectID: proj.ID, Title: "Test", Prompt: "Test",
		Status: "in_progress", ExecutionState: "running",
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
	frames := e.appStreamMgr.GetTaskBuffer(proj.ID, task.ID)
	var found bool
	for _, frame := range frames {
		var payload struct {
			Type   string         `json:"type"`
			Event  string         `json:"event"`
			TaskID string         `json:"task_id"`
			Data   map[string]any `json:"data"`
		}
		if err := json.Unmarshal(frame, &payload); err != nil {
			continue
		}
		if payload.Event != "hint" {
			continue
		}
		found = true
		if payload.Data["id"] != hintMsg.ID {
			t.Errorf("hint broadcast id = %v, want %s", payload.Data["id"], hintMsg.ID)
		}
		if payload.Data["content"] != hintMsg.Content {
			t.Errorf("hint broadcast content = %v, want %s", payload.Data["content"], hintMsg.Content)
		}
	}
	if !found {
		t.Fatal("no hint broadcast frame found in task buffer")
	}
}

func TestLoopDetector_ToolLoopReturnsRepeatedArgs(t *testing.T) {
	d := newLoopDetector()

	// First read
	args1 := map[string]string{"path": "src/main.go"}
	args1JSON, _ := json.Marshal(args1)
	tc1 := []types.ChatToolCall{makeToolCall("read_file", string(args1JSON))}
	d.recordTurn(tc1, "", "tool_calls")

	// Second read of same file — returned args should match
	tc2 := []types.ChatToolCall{makeToolCall("read_file", string(args1JSON))}
	loopType, _, returnedArgs := d.recordTurn(tc2, "", "tool_calls")
	if loopType != LoopTool {
		t.Fatal("expected LoopTool")
	}
	if returnedArgs != string(args1JSON) {
		t.Errorf("returned args = %q, want %q", returnedArgs, string(args1JSON))
	}
}

func TestLoopDetector_ToolLoopMultiCall(t *testing.T) {
	d := newLoopDetector()

	// First turn: read_file then request_review (state-changing clears tracker)
	// So we need: first turn read_file, second turn request_review + read_file
	// The read_file on turn 2 should NOT trigger loop because request_review cleared it
	args1 := map[string]string{"path": "src/main.go"}
	args1JSON, _ := json.Marshal(args1)

	tc1 := []types.ChatToolCall{makeToolCall("read_file", string(args1JSON))}
	loopType, _, _ := d.recordTurn(tc1, "", "tool_calls")
	if loopType != LoopNone {
		t.Error("first read should not trigger loop")
	}

	// Second turn: same read_file — should trigger loop
	tc2 := []types.ChatToolCall{makeToolCall("read_file", string(args1JSON))}
	loopType, _, returnedArgs := d.recordTurn(tc2, "", "tool_calls")
	if loopType != LoopTool {
		t.Fatal("expected LoopTool on repeated read")
	}
	if returnedArgs != string(args1JSON) {
		t.Errorf("returned args = %q, want %q", returnedArgs, string(args1JSON))
	}
}

func TestLoopDetector_ToolLoopDifferentTargets(t *testing.T) {
	d := newLoopDetector()

	args1 := map[string]string{"path": "src/main.go"}
	args1JSON, _ := json.Marshal(args1)
	args2 := map[string]string{"path": "src/utils.go"}
	args2JSON, _ := json.Marshal(args2)

	tc1 := []types.ChatToolCall{makeToolCall("read_file", string(args1JSON))}
	tc2 := []types.ChatToolCall{makeToolCall("read_file", string(args2JSON))}

	if loopType, _, _ := d.recordTurn(tc1, "", "tool_calls"); loopType != LoopNone {
		t.Error("first read should not trigger loop")
	}
	if loopType, _, _ := d.recordTurn(tc2, "", "tool_calls"); loopType != LoopNone {
		t.Error("read of different file should not trigger loop")
	}
}

func TestLoopDetector_TextLoop(t *testing.T) {
	d := newLoopDetector()

	// Statements (no question mark) trigger LoopStatement
	text1 := "I need to carefully analyze the requirements and implement the solution step by step to make sure everything works correctly."
	text2 := "I must carefully analyze the requirements and implement the solution step by step to make sure everything works correctly."

	if loopType, _, _ := d.recordTurn(nil, text1, "stop"); loopType != LoopStatement {
		t.Errorf("expected LoopStatement for first text, got %v", loopType)
	}
	if loopType, _, _ := d.recordTurn(nil, text2, "stop"); loopType != LoopStatement {
		t.Errorf("expected LoopStatement for second text, got %v", loopType)
	}
}

func TestLoopDetector_StatementOnlyWhenLLMDone(t *testing.T) {
	d := newLoopDetector()

	// Text-only response with finish_reason="stop" (LLM chose to end) -> LoopStatement
	loopType, _, _ := d.recordTurn(nil, "I have analyzed the code.", "stop")
	if loopType != LoopStatement {
		t.Errorf("expected LoopStatement with finish_reason=stop, got %v", loopType)
	}

	d = newLoopDetector()

	// Text-only response with finish_reason="tool_calls" but no actual tool calls
	// -> LLM signaled it wanted tools but none were produced; don't penalize
	loopType, _, _ = d.recordTurn(nil, "I have analyzed the code.", "tool_calls")
	if loopType != LoopNone {
		t.Errorf("expected LoopNone with finish_reason=tool_calls, got %v", loopType)
	}

	d = newLoopDetector()

	// Text-only response with empty finish_reason (streaming not complete) -> don't penalize
	loopType, _, _ = d.recordTurn(nil, "I have analyzed the code.", "")
	if loopType != LoopNone {
		t.Errorf("expected LoopNone with empty finish_reason, got %v", loopType)
	}
}

func TestLoopDetector_QuestionDoesNotTriggerStatement(t *testing.T) {
	d := newLoopDetector()

	// Text containing a question mark should NOT trigger LoopStatement
	questions := []string{
		"Should I use React or Vue for this project?",
		"What framework would you recommend? Let me know your preference.",
		"Could you clarify the requirements?",
	}
	for _, q := range questions {
		loopType, _, _ := d.recordTurn(nil, q, "stop")
		if loopType != LoopNone {
			t.Errorf("expected LoopNone for question %q, got %v", q, loopType)
		}
	}
	// loopStreak should be reset to 0
	if d.loopStreak != 0 {
		t.Errorf("expected loopStreak=0 after question, got %d", d.loopStreak)
	}
}

func TestLoopDetector_PhraseLoopViaStreaming(t *testing.T) {
	d := newLoopDetector()

	// Simulate streaming chunks — repeat the same phrase.
	// Note: This will trigger a block loop (3 repeats) before a phrase loop (maxPhraseRepeats).
	chunk := "Let me think about this problem carefully and analyze the full requirements. "
	firedAt := -1
	for i := 0; i < maxPhraseRepeats; i++ {
		loopType := d.checkStreamingText(chunk)
		if loopType == LoopPhrase {
			firedAt = i
			break
		}
	}

	if firedAt == -1 {
		t.Error("expected loop to be detected")
	}
	t.Logf("loop detected at iteration %d", firedAt)
}

func TestLoopDetector_ResetClearsPhraseFlag(t *testing.T) {
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

func TestBuildHint_Phrase(t *testing.T) {
	hint := buildHint(LoopPhrase, "")
	if hint == "" {
		t.Error("LoopPhrase should have a hint message")
	}
	if hint != "You repeated yourself in your last response. Continue with a new action or tool call." {
		t.Errorf("unexpected hint: %s", hint)
	}
}

func TestBuildHint_Tool(t *testing.T) {
	hint := buildHint(LoopTool, "src/main.go")
	if hint == "" {
		t.Error("LoopTool should have a hint message")
	}
	if hint != "You've read/checked `src/main.go` twice in a row without making any changes in between. You already have the contents of this file. Summarize what you know and decide on the next action — edit the file, run a command, or mark the task complete." {
		t.Errorf("unexpected hint: %s", hint)
	}
}

func TestBuildHint_Text(t *testing.T) {
	hint := buildHint(LoopText, "")
	if hint == "" {
		t.Error("LoopText should have a hint message")
	}
}

func TestBuildHint_None(t *testing.T) {
	hint := buildHint(LoopNone, "")
	if hint != "" {
		t.Error("LoopNone should return empty hint")
	}
}

func TestBuildHint_ReasoningOnly(t *testing.T) {
	hint := buildHint(LoopReasoningOnly, "")
	if hint == "" {
		t.Fatal("LoopReasoningOnly should have a hint message")
	}
	// Verify it mentions reasoning
	if !strings.Contains(hint, "reasoning") {
		t.Error("hint should mention reasoning")
	}
	// Verify it lists available tools
	for _, tool := range []string{"read_file", "write_file", "edit_file", "bash", "ask_question", "request_review"} {
		if !strings.Contains(hint, tool) {
			t.Errorf("hint should mention tool %q, got: %s", tool, hint)
		}
	}
	// Verify it mentions function calling format
	if !strings.Contains(hint, "function calling") {
		t.Error("hint should mention function calling format")
	}
}

func TestBuildHint_Statement(t *testing.T) {
	hint := buildHint(LoopStatement, "")
	if hint == "" {
		t.Fatal("LoopStatement should have a hint message")
	}
	expected := "Is the task complete? Use `ask_question` if clarification/approval is needed, or `request_review` if the task is complete. Otherwise use `read_file`, `write_file`, `edit_file`, or `bash` to make progress."
	if hint != expected {
		t.Errorf("unexpected hint: %s", hint)
	}
}

// --- Text Analysis Helper Tests ---

func TestNormalizePhrase(t *testing.T) {
	result := normalizePhrase("  Hello   World  ")
	expected := "hello world"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestWordCount(t *testing.T) {
	count := wordCount("the quick brown fox jumps over the lazy dog")
	if count != 9 {
		t.Errorf("expected 9 words, got %d", count)
	}
}

func TestSplitSentences_Basic(t *testing.T) {
	segments := splitSentences("Hello world. How are you? I am fine!")
	if len(segments) != 3 {
		t.Errorf("expected 3 segments, got %d: %v", len(segments), segments)
	}
	if segments[0] != "Hello world." {
		t.Errorf("expected 'Hello world.', got %q", segments[0])
	}
	if segments[1] != "How are you?" {
		t.Errorf("expected 'How are you?', got %q", segments[1])
	}
	if segments[2] != "I am fine!" {
		t.Errorf("expected 'I am fine!', got %q", segments[2])
	}
}

func TestSplitSentences_NoBoundary(t *testing.T) {
	segments := splitSentences("No sentence boundary here at all")
	if len(segments) != 1 {
		t.Errorf("expected 1 segment, got %d: %v", len(segments), segments)
	}
}

func TestSplitSentences_DoubleNewline(t *testing.T) {
	segments := splitSentences("First paragraph here is long enough.\n\nSecond paragraph also long enough.")
	if len(segments) < 2 {
		t.Errorf("expected at least 2 segments, got %d: %v", len(segments), segments)
	}
}

func TestIsReadOnlyCommand(t *testing.T) {
	tests := []struct {
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
	}

	for _, tt := range tests {
		got := isReadOnlyCommand(tt.cmd)
		if got != tt.want {
			t.Errorf("isReadOnlyCommand(%q) = %v, want %v", tt.cmd, got, tt.want)
		}
	}
}

// --- Sandbox Prompt Path Tests ---

func TestPromptPath_SandboxEnabled(t *testing.T) {
	worktreePath := "/home/user/project/.worktrees/task/alpha-bravo-charlie"
	projectName := "my-project"
	sandboxEnabled := true

	// Simulate the prompt path logic from executeTask
	promptPath := worktreePath
	if sandboxEnabled {
		promptPath = "/home/sandbox/" + projectName
	}

	if promptPath != "/home/sandbox/my-project" {
		t.Errorf("sandbox enabled: promptPath = %q, want %q", promptPath, "/home/sandbox/my-project")
	}
}

func TestPromptPath_SandboxDisabled(t *testing.T) {
	worktreePath := "/home/user/project/.worktrees/task/alpha-bravo-charlie"
	projectName := "my-project"
	sandboxEnabled := false

	// Simulate the prompt path logic from executeTask
	promptPath := worktreePath
	if sandboxEnabled {
		promptPath = "/home/sandbox/" + projectName
	}

	if promptPath != worktreePath {
		t.Errorf("sandbox disabled: promptPath = %q, want %q", promptPath, worktreePath)
	}
}

// --- ExtractLastTurns Tests ---

func TestExtractLastTurns_Empty(t *testing.T) {
	result := ExtractLastTurns(nil, 3)
	if len(result) != 0 {
		t.Errorf("expected 0 messages, got %d", len(result))
	}
}

func TestExtractLastTurns_SystemOnly(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "You are a helpful assistant"},
	}
	result := ExtractLastTurns(messages, 3)
	if len(result) != 0 {
		t.Errorf("expected 0 messages (system skipped), got %d", len(result))
	}
}

func TestExtractLastTurns_SingleTurn(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there"},
	}
	result := ExtractLastTurns(messages, 3)
	if len(result) != 2 {
		t.Errorf("expected 2 messages, got %d", len(result))
	}
	if result[0].Role != "user" || result[1].Role != "assistant" {
		t.Errorf("expected user+assistant, got %s+%s", result[0].Role, result[1].Role)
	}
}

func TestExtractLastTurns_ThreeTurns(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "Turn 1 user"},
		{Role: "assistant", Content: "Turn 1 assistant"},
		{Role: "user", Content: "Turn 2 user"},
		{Role: "assistant", Content: "Turn 2 assistant"},
		{Role: "user", Content: "Turn 3 user"},
		{Role: "assistant", Content: "Turn 3 assistant"},
	}
	result := ExtractLastTurns(messages, 3)
	if len(result) != 6 {
		t.Errorf("expected 6 messages (3 turns), got %d", len(result))
	}
	// All 3 turns fit, so should start with turn 1
	if result[0].Content != "Turn 1 user" {
		t.Errorf("expected first msg to be 'Turn 1 user', got %q", result[0].Content)
	}
}

func TestExtractLastTurns_MoreThanThreeTurns(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "Turn 1"},
		{Role: "assistant", Content: "A1"},
		{Role: "user", Content: "Turn 2"},
		{Role: "assistant", Content: "A2"},
		{Role: "user", Content: "Turn 3"},
		{Role: "assistant", Content: "A3"},
		{Role: "user", Content: "Turn 4"},
		{Role: "assistant", Content: "A4"},
		{Role: "user", Content: "Turn 5"},
		{Role: "assistant", Content: "A5"},
	}
	result := ExtractLastTurns(messages, 3)
	if len(result) != 6 {
		t.Errorf("expected 6 messages (last 3 turns), got %d", len(result))
	}
	// Should be turns 3, 4, 5
	if result[0].Content != "Turn 3" {
		t.Errorf("expected first msg to be 'Turn 3', got %q", result[0].Content)
	}
	if result[5].Content != "A5" {
		t.Errorf("expected last msg to be 'A5', got %q", result[5].Content)
	}
}

func TestExtractLastTurns_WithToolCalls(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "Read a file"},
		{Role: "assistant", Content: "", ToolCalls: []types.ToolCallMsg{{ID: "call_1", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "read_file", Arguments: `{"path":"foo.txt"}`}}}},
		{Role: "tool", Content: "file contents", ToolCallID: "call_1"},
		{Role: "assistant", Content: "Here is the content"},
		{Role: "user", Content: "Thanks"},
		{Role: "assistant", Content: "You're welcome"},
	}
	result := ExtractLastTurns(messages, 3)
	// Should have 2 turns (turn 1 with tool calls, turn 2)
	if len(result) < 4 {
		t.Errorf("expected at least 4 messages, got %d", len(result))
	}
}

func TestExtractLastTurns_IncompleteTurn(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "Turn 1"},
		{Role: "assistant", Content: "A1"},
		{Role: "user", Content: "Turn 2 (no response yet)"},
	}
	result := ExtractLastTurns(messages, 3)
	// Should include the incomplete turn
	if len(result) != 3 {
		t.Errorf("expected 3 messages (1 full turn + 1 incomplete), got %d", len(result))
	}
}

// --- FormatConversationTurns Tests ---

func TestFormatConversationTurns_Basic(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there"},
	}
	result := FormatConversationTurns(messages)
	if result != "User: Hello\n\nAssistant: Hi there\n\n" {
		t.Errorf("unexpected output: %q", result)
	}
}

func TestFormatConversationTurns_WithToolResult(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "assistant", Content: "", ToolCalls: []types.ToolCallMsg{{ID: "call_1", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "read_file", Arguments: `{"path":"foo.txt"}`}}}},
		{Role: "tool", Content: "file contents", ToolCallID: "call_1"},
	}
	result := FormatConversationTurns(messages)
	if result == "" {
		t.Error("expected non-empty output")
	}
	if !strings.Contains(result, "I used the read_file tool to") {
		t.Errorf("expected descriptive tool call in output, got: %s", result)
	}
	if !strings.Contains(result, "foo.txt") {
		t.Errorf("expected file path in output, got: %s", result)
	}
	// Tool result is incorporated into assistant line, not shown separately
	if strings.Contains(result, "[Tool result]") {
		t.Error("tool result should be incorporated into assistant line, not shown separately")
	}
}

func TestFormatConversationTurns_ToolResultSkipped(t *testing.T) {
	// Standalone tool messages (without preceding assistant with matching ToolCalls)
	// are skipped — they're already incorporated into the assistant line.
	longContent := strings.Repeat("x", 600)
	messages := []types.ChatMessage{
		{Role: "tool", Content: longContent},
	}
	result := FormatConversationTurns(messages)
	// Tool messages are skipped, so output should be empty
	if result != "" {
		t.Errorf("expected empty output for standalone tool message, got %d chars", len(result))
	}
}

func TestFormatConversationTurns_TrimsTrailingNewlines(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "user", Content: "Fix the bug\n\n"},
		{Role: "assistant", Content: "Let me check\n\n\n", ToolCalls: []types.ToolCallMsg{{ID: "call_1", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "read_file", Arguments: `{"path":"foo.go"}`}}}},
		{Role: "tool", Content: "file contents", ToolCallID: "call_1"},
		{Role: "assistant", Content: "Done!\n"},
	}
	result := FormatConversationTurns(messages)
	// Should not have stacked blank lines (more than 2 consecutive newlines)
	if strings.Contains(result, "\n\n\n") {
		t.Errorf("expected no stacked blank lines, got:\n%q", result)
	}
	// Each message block should end with exactly \n\n
	if !strings.HasSuffix(result, "\n\n") {
		t.Errorf("expected trailing double newline, got: %q", result[len(result)-5:])
	}
}

// --- isTaskID Tests ---

func TestIsTaskID_Valid(t *testing.T) {
	validIDs := []string{"frost-amber-quartz", "abc-def-ghi", "a-b-c"}
	for _, id := range validIDs {
		if !isTaskID(id) {
			t.Errorf("expected %q to be a valid task ID", id)
		}
	}
}

func TestIsTaskID_Invalid(t *testing.T) {
	invalidIDs := []string{"", "foo", "foo-bar", "foo-bar-baz-qux", "-foo-bar", "foo--bar", "foo-bar-"}
	for _, id := range invalidIDs {
		if isTaskID(id) {
			t.Errorf("expected %q to be invalid task ID", id)
		}
	}
}

// --- formatToolAction Tests ---

func TestFormatToolAction_CreateTask(t *testing.T) {
	result := formatToolAction("create_task", `{"title":"Test task"}`)
	if result != "create" {
		t.Errorf("expected 'create', got %q", result)
	}
}

func TestFormatToolAction_EditTask(t *testing.T) {
	result := formatToolAction("edit_task", `{"task_id":"frost-amber-quartz","title":"New title"}`)
	if result != "edit" {
		t.Errorf("expected 'edit', got %q", result)
	}
}

// --- formatToolResult Tests ---

func TestFormatToolResult_CreateTask_Success(t *testing.T) {
	toolResults := map[string]string{"call_1": `{"task_id":"frost-amber-quartz","title":"New task"}`}
	result := formatToolResult("create_task", "call_1", "successful", toolResults)
	if result != "task ID: frost-amber-quartz, title: New task" {
		t.Errorf("expected 'task ID: frost-amber-quartz, title: New task', got %q", result)
	}
}

func TestFormatToolResult_CreateTask_Success_NoTaskID(t *testing.T) {
	// Result is not valid JSON
	toolResults := map[string]string{"call_1": "Task created successfully"}
	result := formatToolResult("create_task", "call_1", "successful", toolResults)
	if result != "a task" {
		t.Errorf("expected 'a task', got %q", result)
	}
}

func TestFormatToolResult_CreateTask_Success_TaskIDOnly(t *testing.T) {
	// JSON result with task_id but no title
	toolResults := map[string]string{"call_1": `{"task_id":"frost-amber-quartz"}`}
	result := formatToolResult("create_task", "call_1", "successful", toolResults)
	if result != "task ID: frost-amber-quartz" {
		t.Errorf("expected 'task ID: frost-amber-quartz', got %q", result)
	}
}

func TestFormatToolResult_CreateTask_Failure(t *testing.T) {
	toolResults := map[string]string{"call_1": "error: project not found"}
	result := formatToolResult("create_task", "call_1", "unsuccessful", toolResults)
	if result != "but was unsuccessful. Error: error: project not found" {
		t.Errorf("expected 'but was unsuccessful. Error: error: project not found', got %q", result)
	}
}

func TestFormatToolResult_EditTask_Success(t *testing.T) {
	toolResults := map[string]string{"call_1": `{"task_id":"frost-amber-quartz","title":"New title","status":"updated"}`}
	result := formatToolResult("edit_task", "call_1", "successful", toolResults)
	if result != "task ID: frost-amber-quartz, title: New title" {
		t.Errorf("expected 'task ID: frost-amber-quartz, title: New title', got %q", result)
	}
}

func TestFormatToolResult_EditTask_Failure(t *testing.T) {
	toolResults := map[string]string{"call_1": "tool edit_task failed: task frost-amber-quartz not found"}
	result := formatToolResult("edit_task", "call_1", "unsuccessful", toolResults)
	if result != "but was unsuccessful. Error: tool edit_task failed: task frost-amber-quartz not found" {
		t.Errorf("expected error message, got %q", result)
	}
}

func TestFormatToolResult_CreateTask_Pending(t *testing.T) {
	toolResults := map[string]string{}
	result := formatToolResult("create_task", "call_1", "pending", toolResults)
	if result != "a task" {
		t.Errorf("expected 'a task', got %q", result)
	}
}

func TestFormatToolResult_OtherTool_Success(t *testing.T) {
	toolResults := map[string]string{"call_1": "file contents"}
	result := formatToolResult("read_file", "call_1", "successful", toolResults)
	if result != "which was successful" {
		t.Errorf("expected 'which was successful', got %q", result)
	}
}

func TestFormatToolResult_OtherTool_Failure(t *testing.T) {
	toolResults := map[string]string{"call_1": "error: file not found"}
	result := formatToolResult("read_file", "call_1", "unsuccessful", toolResults)
	if result != "which was unsuccessful" {
		t.Errorf("expected 'which was unsuccessful', got %q", result)
	}
}

// --- FormatConversationTurns with create_task ---

func TestFormatConversationTurns_CreateTask_Success(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "assistant", Content: "", ToolCalls: []types.ToolCallMsg{{ID: "call_1", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "create_task", Arguments: `{"title":"New task"}`}}}},
		{Role: "tool", Content: `{"task_id":"frost-amber-quartz","title":"New task"}`, ToolCallID: "call_1"},
	}
	result := FormatConversationTurns(messages)
	if !strings.Contains(result, "I used the create_task tool to create task ID: frost-amber-quartz, title: New task") {
		t.Errorf("expected 'create task ID: frost-amber-quartz, title: New task' in output, got: %s", result)
	}
	// Should NOT contain the generic "which was successful"
	if strings.Contains(result, "which was") {
		t.Errorf("should not contain 'which was' for create_task success, got: %s", result)
	}
}

func TestFormatConversationTurns_CreateTask_Failure(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "assistant", Content: "", ToolCalls: []types.ToolCallMsg{{ID: "call_1", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "create_task", Arguments: `{"title":"New task"}`}}}},
		{Role: "tool", Content: "error: project not found", ToolCallID: "call_1"},
	}
	result := FormatConversationTurns(messages)
	if !strings.Contains(result, "but was unsuccessful. Error: error: project not found") {
		t.Errorf("expected error message in output, got: %s", result)
	}
}

func TestFormatConversationTurns_EditTask_Success(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "assistant", Content: "", ToolCalls: []types.ToolCallMsg{{ID: "call_1", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "edit_task", Arguments: `{"task_id":"frost-amber-quartz","title":"New title"}`}}}},
		{Role: "tool", Content: `{"task_id":"frost-amber-quartz","title":"New title","status":"updated"}`, ToolCallID: "call_1"},
	}
	result := FormatConversationTurns(messages)
	if !strings.Contains(result, "I used the edit_task tool to edit task ID: frost-amber-quartz, title: New title") {
		t.Errorf("expected 'edit task ID: frost-amber-quartz, title: New title' in output, got: %s", result)
	}
}

func TestFormatConversationTurns_EditTask_Failure(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "assistant", Content: "", ToolCalls: []types.ToolCallMsg{{ID: "call_1", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "edit_task", Arguments: `{"task_id":"frost-amber-quartz","title":"New title"}`}}}},
		{Role: "tool", Content: "tool edit_task failed: task frost-amber-quartz not found", ToolCallID: "call_1"},
	}
	result := FormatConversationTurns(messages)
	if !strings.Contains(result, "I used the edit_task tool to edit but was unsuccessful. Error:") {
		t.Errorf("expected error message in output, got: %s", result)
	}
	if !strings.Contains(result, "task frost-amber-quartz not found") {
		t.Errorf("expected underlying error in output, got: %s", result)
	}
}

// --- formatChatHistory with create_task ---

func TestFormatChatHistory_CreateTask_Success(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "Create a new task"},
		{Role: "assistant", Content: "", ToolCalls: []types.ToolCallMsg{
			{ID: "call_1", Type: "function"},
		}},
		{Role: "tool", Content: `{"task_id":"frost-amber-quartz","title":"New task"}`, ToolCallID: "call_1"},
	}
	messages[2].ToolCalls[0].Function.Name = "create_task"
	messages[2].ToolCalls[0].Function.Arguments = `{"title":"New task"}`

	history := formatChatHistory(messages, false)

	if !strings.Contains(history, "I used the create_task tool to create task ID: frost-amber-quartz, title: New task") {
		t.Errorf("chat history missing task ID and title. Got:\n%s", history)
	}
	// Should NOT contain generic "which was successful"
	if strings.Contains(history, "which was") {
		t.Errorf("should not contain 'which was' for create_task success, got:\n%s", history)
	}
}

func TestFormatChatHistory_CreateTask_Failure(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "Create a task"},
		{Role: "assistant", Content: "", ToolCalls: []types.ToolCallMsg{
			{ID: "call_1", Type: "function"},
		}},
		{Role: "tool", Content: "error: invalid project", ToolCallID: "call_1"},
	}
	messages[2].ToolCalls[0].Function.Name = "create_task"
	messages[2].ToolCalls[0].Function.Arguments = `{"title":"Bad task"}`

	history := formatChatHistory(messages, false)

	if !strings.Contains(history, "but was unsuccessful. Error: error: invalid project") {
		t.Errorf("chat history missing error message. Got:\n%s", history)
	}
}

func TestFormatChatHistory_EditTask_Success(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "Edit a task"},
		{Role: "assistant", Content: "", ToolCalls: []types.ToolCallMsg{
			{ID: "call_1", Type: "function"},
		}},
		{Role: "tool", Content: `{"task_id":"frost-amber-quartz","title":"New title","status":"updated"}`, ToolCallID: "call_1"},
	}
	messages[2].ToolCalls[0].Function.Name = "edit_task"
	messages[2].ToolCalls[0].Function.Arguments = `{"task_id":"frost-amber-quartz","title":"New title"}`

	history := formatChatHistory(messages, false)

	if !strings.Contains(history, "I used the edit_task tool to edit task ID: frost-amber-quartz, title: New title") {
		t.Errorf("chat history missing task ID and title. Got:\n%s", history)
	}
}

func TestFormatChatHistory_EditTask_Failure(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "Edit a task"},
		{Role: "assistant", Content: "", ToolCalls: []types.ToolCallMsg{
			{ID: "call_1", Type: "function"},
		}},
		{Role: "tool", Content: "tool edit_task failed: task frost-amber-quartz not found", ToolCallID: "call_1"},
	}
	messages[2].ToolCalls[0].Function.Name = "edit_task"
	messages[2].ToolCalls[0].Function.Arguments = `{"task_id":"frost-amber-quartz","title":"New title"}`

	history := formatChatHistory(messages, false)

	if !strings.Contains(history, "I used the edit_task tool to edit but was unsuccessful. Error:") {
		t.Errorf("chat history missing error message. Got:\n%s", history)
	}
	if !strings.Contains(history, "task frost-amber-quartz not found") {
		t.Errorf("chat history missing underlying error. Got:\n%s", history)
	}
}

func TestBuildCommitConversation_MergesToolCallsIntoAssistant(t *testing.T) {
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

func TestBuildCommitConversation_SkipsToolCallsWithoutAssistant(t *testing.T) {
	// Edge case: tool_call at the start (no preceding assistant)
	taskMsgs := []types.TaskMessage{
		{Role: "tool_call", ToolCalls: `{"tool_id":"call_1","tool_name":"read_file","raw_args":"{}"}`, ToolResult: `{"output":"contents","success":true}`},
		{Role: "user", Content: "Hello"},
	}

	messages := BuildConversationFromTaskMessages(taskMsgs)

	// Should have: tool(result), user — tool_call with no assistant is skipped, tool result added
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if messages[0].Role != "tool" {
		t.Errorf("expected tool result, got role=%s", messages[0].Role)
	}
}

func TestFormatConversationTurns_FullPipeline(t *testing.T) {
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

// --- UTF-8 Tests (regression for CPU spike bug) ---

func TestSplitSentences_UTF8Characters(t *testing.T) {
	// Multi-byte UTF-8 characters should be preserved correctly
	segments := splitSentences("Café résumé naïve. Hello world!")
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d: %v", len(segments), segments)
	}
	if segments[0] != "Café résumé naïve." {
		t.Errorf("expected 'Café résumé naïve.', got %q", segments[0])
	}
	if segments[1] != "Hello world!" {
		t.Errorf("expected 'Hello world!', got %q", segments[1])
	}
}

func TestSplitSentences_CJKCharacters(t *testing.T) {
	// CJK characters are 3 bytes each — must not be corrupted.
	// Note: isSentenceEnd only recognizes ASCII .!? not CJK full-width 。！
	// So this tests that CJK chars pass through without corruption.
	segments := splitSentences("你好世界. 这是一个测试!")
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d: %v", len(segments), segments)
	}
	if segments[0] != "你好世界." {
		t.Errorf("expected '你好世界.', got %q", segments[0])
	}
	if segments[1] != "这是一个测试!" {
		t.Errorf("expected '这是一个测试!', got %q", segments[1])
	}
}

func TestSplitSentences_Emoji(t *testing.T) {
	// Emoji are 4-byte UTF-8 (surrogate pairs)
	segments := splitSentences("Hello 👋 world. How are you?")
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d: %v", len(segments), segments)
	}
	if segments[0] != "Hello 👋 world." {
		t.Errorf("expected 'Hello 👋 world.', got %q", segments[0])
	}
}

func TestSplitSentences_MixedUTF8(t *testing.T) {
	// Mix of ASCII, 2-byte, 3-byte, and 4-byte characters
	segments := splitSentences("café 日本語 🎉 done. Next!")
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d: %v", len(segments), segments)
	}
	if segments[0] != "café 日本語 🎉 done." {
		t.Errorf("expected 'café 日本語 🎉 done.', got %q", segments[0])
	}
}

func TestPhraseDetector_UTF8Streaming(t *testing.T) {
	// Simulate streaming chunks containing multi-byte UTF-8 characters.
	// This is the exact scenario that caused the CPU spike:
	// small chunks with UTF-8 chars that get split across calls.
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
	// Feed a very long text without sentence boundaries.
	// The pending buffer should be bounded to maxPendingSize.
	d := newPhraseDetector()

	// Generate text longer than maxPendingSize (16KB) without sentence boundaries
	longText := strings.Repeat("no sentence boundary here just words ", 1000)
	if len(longText) < maxPendingSize {
		t.Skipf("test text %d bytes is shorter than maxPendingSize %d", len(longText), maxPendingSize)
	}

	d.check(longText)
	// Pending should be bounded
	if len(d.pending) > maxPendingSize {
		t.Errorf("pending buffer %d bytes exceeds maxPendingSize %d", len(d.pending), maxPendingSize)
	}
}

func TestPhraseDetector_UTF8NoBufferBloat(t *testing.T) {
	// Regression test: ensure UTF-8 text doesn't cause exponential pending growth.
	// Before the fix, multi-byte chars were split into bytes, re-encoded as longer
	// sequences, and the pending buffer grew exponentially with each call.
	d := newPhraseDetector()

	// UTF-8 text with multi-byte characters, no sentence boundaries
	utf8Text := "café résumé naïve 日本語 🎉"

	// Feed the same text multiple times (no boundaries, so it accumulates in pending)
	for i := 0; i < 10; i++ {
		d.check(utf8Text)
	}

	// The pending buffer should not grow exponentially.
	// With the fix, pending is bounded to maxPendingSize.
	// Without the fix, it would be ~utf8Text * 10 * corruption_factor.
	if len(d.pending) > maxPendingSize {
		t.Errorf("pending buffer %d bytes exceeds maxPendingSize %d (UTF-8 bloat?)", len(d.pending), maxPendingSize)
	}
}

func TestNormalizePhrase_UTF8(t *testing.T) {
	// Ensure normalizePhrase preserves UTF-8 characters
	result := normalizePhrase("  Café Résumé  ")
	expected := "café résumé"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestNormalizePhrase_TrailingPunctuationOnlyWord(t *testing.T) {
	// Edge case: a single word that is entirely punctuation after trim
	result := normalizePhrase("....")
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestNormalizePhrase_WordWithTrailingPunctuation(t *testing.T) {
	// Word followed by punctuation should have punctuation stripped
	result := normalizePhrase("hello world!!!")
	expected := "hello world"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// --- StopProjectTasks Tests ---

func TestStopProjectTasks(t *testing.T) {
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
	t1, _ := te.store.GetTaskByTaskID(task1.ID)
	if t1.ExecutionState != "idle" {
		t.Errorf("expected task1 execution_state='idle', got %q", t1.ExecutionState)
	}
	t2, _ := te.store.GetTaskByTaskID(task2.ID)
	if t2.ExecutionState != "idle" {
		t.Errorf("expected task2 execution_state='idle', got %q", t2.ExecutionState)
	}
}

func TestStopProjectTasks_NoRunningTasks(t *testing.T) {
	te := newTestExecutor(t)
	now := time.Now().UTC()
	proj := &types.Project{ID: "proj-empty", Name: "Empty Project", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}
	if err := te.store.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	createTaskInProject(t, te.store, proj.ID, "idle-task")

	// No running executors
	stopped := te.StopProjectTasks(proj.ID)
	if stopped != 0 {
		t.Fatalf("expected 0 tasks stopped, got %d", stopped)
	}
}

func TestStopProjectTasks_DoesNotAffectNonRunningTasks(t *testing.T) {
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
	reviewTask.Status = "review"
	reviewTask.ExecutionState = "idle"
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
	runningTaskAfter, _ := te.store.GetTaskByTaskID(runningTask.ID)
	if runningTaskAfter.ExecutionState != "idle" {
		t.Errorf("expected running task execution_state='idle', got %q", runningTaskAfter.ExecutionState)
	}

	// Verify review task was NOT modified
	reviewTaskAfter, _ := te.store.GetTaskByTaskID(reviewTask.ID)
	if reviewTaskAfter.Status != "review" {
		t.Errorf("expected review task status='review', got %q", reviewTaskAfter.Status)
	}
	if reviewTaskAfter.ExecutionState != "idle" {
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
		Status:         "backlog",
		ExecutionState: "idle",
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

func TestUserMessage_ReasoningEffortField(t *testing.T) {
	// Verify the UserMessage struct carries ReasoningEffort
	um := stream.UserMessage{
		Content:         "test message",
		ModelName:       "test-model",
		ServerURL:       "http://localhost:8080/v1",
		ReasoningEffort: "high",
		Attachments:     "",
	}

	if um.ReasoningEffort != "high" {
		t.Errorf("expected ReasoningEffort='high', got %q", um.ReasoningEffort)
	}
	if um.ModelName != "test-model" {
		t.Errorf("expected ModelName='test-model', got %q", um.ModelName)
	}
	if um.ServerURL != "http://localhost:8080/v1" {
		t.Errorf("expected ServerURL='http://localhost:8080/v1', got %q", um.ServerURL)
	}
}

func TestUserMessage_ReasoningEffortEmpty(t *testing.T) {
	// Verify empty ReasoningEffort is handled (falls back to config default)
	um := stream.UserMessage{
		Content:         "test message",
		ModelName:       "test-model",
		ServerURL:       "http://localhost:8080/v1",
		ReasoningEffort: "", // empty — should use config default
	}

	if um.ReasoningEffort != "" {
		t.Errorf("expected empty ReasoningEffort, got %q", um.ReasoningEffort)
	}
}

func TestModelConfig_SetAndGet(t *testing.T) {
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

func TestUserMessageToChatMessage_PlainText(t *testing.T) {
	um := stream.UserMessage{Content: "hello world"}
	msg := userMessageToChatMessage(um)

	if msg.Role != "user" {
		t.Errorf("expected role 'user', got %q", msg.Role)
	}
	if msg.Content != "hello world" {
		t.Errorf("expected content 'hello world', got %q", msg.Content)
	}
	if len(msg.ContentParts) > 0 {
		t.Error("expected no ContentParts for plain text message")
	}
}

func TestUserMessageToChatMessage_WithAttachments(t *testing.T) {
	atts, _ := json.Marshal([]struct {
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
	}{
		{MimeType: "image/png", Data: "iVBORw0KGgo="},
	})
	um := stream.UserMessage{Content: "check this image", Attachments: string(atts)}
	msg := userMessageToChatMessage(um)

	if msg.Role != "user" {
		t.Errorf("expected role 'user', got %q", msg.Role)
	}
	if len(msg.ContentParts) != 2 {
		t.Fatalf("expected 2 ContentParts (text + image), got %d", len(msg.ContentParts))
	}
	if msg.ContentParts[0].Type != "text" {
		t.Errorf("expected first part to be text, got %q", msg.ContentParts[0].Type)
	}
	if msg.ContentParts[0].Text != "check this image" {
		t.Errorf("expected text 'check this image', got %q", msg.ContentParts[0].Text)
	}
	if msg.ContentParts[1].Type != "image" {
		t.Errorf("expected second part to be image, got %q", msg.ContentParts[1].Type)
	}
}

func TestUserMessageToChatMessage_StripsDataPrefix(t *testing.T) {
	atts, _ := json.Marshal([]struct {
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
	}{
		{MimeType: "image/png", Data: "data:image/png;base64,abc123"},
	})
	um := stream.UserMessage{Content: "test", Attachments: string(atts)}
	msg := userMessageToChatMessage(um)

	if msg.ContentParts[1].ImageSource == nil {
		t.Fatal("expected ImageSource to be set")
	}
	if msg.ContentParts[1].ImageSource.Data != "abc123" {
		t.Errorf("expected data prefix stripped, got %q", msg.ContentParts[1].ImageSource.Data)
	}
}

func TestUserMessageToChatMessage_DefaultMimeType(t *testing.T) {
	atts, _ := json.Marshal([]struct {
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
	}{
		{MimeType: "", Data: "base64data"},
	})
	um := stream.UserMessage{Content: "test", Attachments: string(atts)}
	msg := userMessageToChatMessage(um)

	if msg.ContentParts[1].ImageSource.MediaType != "image/png" {
		t.Errorf("expected default mimeType 'image/png', got %q", msg.ContentParts[1].ImageSource.MediaType)
	}
}

func TestUserMessageToChatMessage_InvalidJSON(t *testing.T) {
	um := stream.UserMessage{Content: "test", Attachments: "not-json"}
	msg := userMessageToChatMessage(um)

	// Should fall back to plain text message
	if msg.Content != "test" {
		t.Errorf("expected fallback to plain text, got %q", msg.Content)
	}
	if len(msg.ContentParts) > 0 {
		t.Error("expected no ContentParts for invalid JSON attachments")
	}
}

func TestExtractPrevSummary_NoSummary(t *testing.T) {
	messages := []types.ChatMessage{{Role: "system", Content: "you are a helper"}}
	got := extractPrevSummary(messages)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestExtractPrevSummary_WithSummary(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "you are a helper"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
		{Role: "summarization", Content: "The user asked about Go generics"},
		{Role: "user", Content: "tell me more"},
	}
	got := extractPrevSummary(messages)
	if got != "The user asked about Go generics" {
		t.Errorf("expected 'The user asked about Go generics', got %q", got)
	}
}

func TestExtractPrevSummary_MultipleSummaries(t *testing.T) {
	// Should return the most recent summary.
	messages := []types.ChatMessage{
		{Role: "system", Content: "you are a helper"},
		{Role: "summarization", Content: "first summary"},
		{Role: "user", Content: "more stuff"},
		{Role: "summarization", Content: "second summary"},
	}
	got := extractPrevSummary(messages)
	if got != "second summary" {
		t.Errorf("expected 'second summary', got %q", got)
	}
}

func TestExtractPrevSummary_OnlyAssistantMessages(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "you are a helper"},
		{Role: "assistant", Content: "not a summary"},
	}
	got := extractPrevSummary(messages)
	if got != "" {
		t.Errorf("expected empty string for no summarization role, got %q", got)
	}
}

// --- buildLLMContext Tests ---

func TestBuildLLMContext_NoSummary(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	result := buildLLMContext(messages)
	// No summary: passthrough.
	if len(result) != len(messages) {
		t.Fatalf("expected %d messages, got %d", len(messages), len(result))
	}
	for i := range messages {
		if result[i].Role != messages[i].Role || result[i].Content != messages[i].Content {
			t.Errorf("message[%d] mismatch: got %v, want %v", i, result[i], messages[i])
		}
	}
}

func TestBuildLLMContext_WithSummary(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "old message 1"},
		{Role: "assistant", Content: "old response 1"},
		{Role: "user", Content: "old message 2"},
		{Role: "assistant", Content: "old response 2"},
		{Role: "summarization", Content: "summary of conversation"},
		{Role: "user", Content: "new message"},
		{Role: "assistant", Content: "new response"},
	}
	result := buildLLMContext(messages)

	// Summary is in system prompt; summarization message and all pre-summary
	// messages are dropped. System prompt + post-summary messages remain: 3
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	// No summarization role in output
	for _, m := range result {
		if m.Role == "summarization" {
			t.Error("summarization messages should be filtered out")
		}
	}
	// System prompt + only post-summary messages preserved
	expected := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "new message"},
		{Role: "assistant", Content: "new response"},
	}
	for i, exp := range expected {
		if result[i].Role != exp.Role || result[i].Content != exp.Content {
			t.Errorf("message[%d] mismatch: got %s:%q, want %s:%q", i, result[i].Role, result[i].Content, exp.Role, exp.Content)
		}
	}
}

func TestBuildLLMContext_DropsPreSummaryMessages(t *testing.T) {
	// Pre-summary messages are dropped (their content is in the system prompt summary).
	var messages []types.ChatMessage
	messages = append(messages, types.ChatMessage{Role: "system", Content: "system prompt"})
	for i := 1; i <= 5; i++ {
		messages = append(messages, types.ChatMessage{Role: "user", Content: fmt.Sprintf("old message %d", i)})
		messages = append(messages, types.ChatMessage{Role: "assistant", Content: fmt.Sprintf("old response %d", i)})
	}
	messages = append(messages, types.ChatMessage{Role: "summarization", Content: "summary"})
	messages = append(messages, types.ChatMessage{Role: "user", Content: "new message"})

	result := buildLLMContext(messages)

	// Only system prompt + post-summary messages: 2
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	// System prompt preserved
	if result[0].Role != "system" {
		t.Errorf("expected system message, got %s", result[0].Role)
	}
	// All old messages should be absent
	for i := 1; i <= 5; i++ {
		for _, m := range result {
			if m.Content == fmt.Sprintf("old message %d", i) {
				t.Errorf("pre-summary message 'old message %d' should be dropped", i)
			}
		}
	}
	// Post-summary message preserved
	if result[1].Content != "new message" {
		t.Errorf("expected 'new message', got %q", result[1].Content)
	}
	// No summarization in output
	for _, m := range result {
		if m.Role == "summarization" {
			t.Error("summarization messages should be filtered out")
		}
	}
}

func TestBuildLLMContext_MultipleSummaries(t *testing.T) {
	// With re-summarization, only messages after the LAST summary are kept.
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "very old message"},
		{Role: "assistant", Content: "very old response"},
		{Role: "summarization", Content: "first summary"},
		{Role: "user", Content: "mid message"},
		{Role: "assistant", Content: "mid response"},
		{Role: "summarization", Content: "second summary (includes first)"},
		{Role: "user", Content: "latest message"},
	}
	result := buildLLMContext(messages)

	// System + only post-last-summary messages: 2
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if result[0].Role != "system" {
		t.Errorf("expected system, got %s", result[0].Role)
	}
	if result[1].Content != "latest message" {
		t.Errorf("expected 'latest message', got %q", result[1].Content)
	}
	// Mid messages (between summaries) should also be dropped
	for _, m := range result {
		if m.Content == "mid message" || m.Content == "mid response" {
			t.Error("messages between summaries should be dropped")
		}
	}
}

func TestBuildLLMContext_WithToolCalls(t *testing.T) {
	assistantWithTool := types.ChatMessage{
		Role:    "assistant",
		Content: "ok",
		ToolCalls: []types.ToolCallMsg{
			{ID: "call1", Type: "function"},
		},
	}
	assistantWithTool.ToolCalls[0].Function.Name = "bash"
	assistantWithTool.ToolCalls[0].Function.Arguments = "{}"

	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "do something"},
		assistantWithTool,
		{Role: "tool", Content: "output", ToolCallID: "call1"},
		{Role: "summarization", Content: "summary"},
		{Role: "user", Content: "done"},
	}
	result := buildLLMContext(messages)

	// Pre-summary tool calls are dropped (captured by summary in system prompt).
	// Expected: system + user = 2
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if result[0].Role != "system" {
		t.Errorf("expected system, got %s", result[0].Role)
	}
	if result[1].Content != "done" {
		t.Errorf("expected 'done', got %q", result[1].Content)
	}
}

// --- buildLLMContext request_review filter Tests ---

func TestBuildLLMContext_FiltersTaskComplete(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "fix the bug"},
		{Role: "assistant", Content: "I'll fix it", ToolCalls: []types.ToolCallMsg{
			{ID: "call1", Type: "function"},
		}},
		{Role: "tool", Content: "file contents", ToolCallID: "call1"},
		{Role: "assistant", Content: "Task done!", ToolCalls: []types.ToolCallMsg{
			{ID: "call2", Type: "function"},
		}},
		{Role: "user", Content: "now do something else"},
		{Role: "assistant", Content: "working on it"},
	}
	// Set tool names
	messages[2].ToolCalls[0].Function.Name = "read_file"
	messages[2].ToolCalls[0].Function.Arguments = `{"path":"foo.go"}`
	messages[4].ToolCalls[0].Function.Name = "request_review"
	messages[4].ToolCalls[0].Function.Arguments = `{"summary":"fixed"}`

	result := buildLLMContext(messages)

	// request_review assistant (msg[4]) should be kept
	// system + user + assistant(read_file) + tool + assistant(request_review) + user + assistant = 7
	if len(result) != 7 {
		t.Fatalf("expected 7 messages, got %d", len(result))
		for i, m := range result {
			t.Logf("  [%d] role=%s content=%q toolCalls=%v", i, m.Role, m.Content, m.ToolCalls)
		}
	}

	// Verify request_review tool call is present in result
	foundRequestReview := false
	for _, m := range result {
		for _, tc := range m.ToolCalls {
			if tc.Function.Name == "request_review" {
				foundRequestReview = true
			}
		}
	}
	if !foundRequestReview {
		t.Error("request_review tool call should be present")
	}
}

func TestBuildLLMContext_FiltersTaskCompleteWithToolResult(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "fix the bug"},
		{Role: "assistant", Content: "done", ToolCalls: []types.ToolCallMsg{
			{ID: "call-tc", Type: "function"},
		}},
		{Role: "tool", Content: "task completed", ToolCallID: "call-tc"},
		{Role: "user", Content: "new request"},
	}
	messages[2].ToolCalls[0].Function.Name = "request_review"
	messages[2].ToolCalls[0].Function.Arguments = `{"summary":"done"}`

	result := buildLLMContext(messages)

	// request_review assistant (msg[2]) and its tool result (msg[3]) should be kept
	// system + user + assistant + tool + user = 5
	if len(result) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(result))
		for i, m := range result {
			t.Logf("  [%d] role=%s content=%q toolCallID=%q", i, m.Role, m.Content, m.ToolCallID)
		}
	}
}

func TestBuildLLMContext_KeepsOtherToolCalls(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "do stuff"},
		{Role: "assistant", Content: "", ToolCalls: []types.ToolCallMsg{
			{ID: "call1", Type: "function"},
			{ID: "call2", Type: "function"},
		}},
		{Role: "tool", Content: "result1", ToolCallID: "call1"},
		{Role: "tool", Content: "result2", ToolCallID: "call2"},
	}
	messages[2].ToolCalls[0].Function.Name = "bash"
	messages[2].ToolCalls[0].Function.Arguments = `{"command":"ls"}`
	messages[2].ToolCalls[1].Function.Name = "read_file"
	messages[2].ToolCalls[1].Function.Arguments = `{"path":"foo.go"}`

	result := buildLLMContext(messages)

	// No request_review, all messages pass through (except summarization which isn't present)
	if len(result) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(result))
	}
}

func TestBuildLLMContext_FiltersOnlyTaskCompleteToolCall(t *testing.T) {
	// Assistant message with mixed tool calls: bash + request_review
	// The entire assistant message should be filtered since it contains request_review
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "fix and complete"},
		{Role: "assistant", Content: "", ToolCalls: []types.ToolCallMsg{
			{ID: "call1", Type: "function"},
			{ID: "call2", Type: "function"},
		}},
		{Role: "tool", Content: "result", ToolCallID: "call1"},
		{Role: "tool", Content: "completed", ToolCallID: "call2"},
		{Role: "user", Content: "next"},
	}
	messages[2].ToolCalls[0].Function.Name = "bash"
	messages[2].ToolCalls[0].Function.Arguments = `{"command":"git status"}`
	messages[2].ToolCalls[1].Function.Name = "request_review"
	messages[2].ToolCalls[1].Function.Arguments = `{"summary":"done"}`

	result := buildLLMContext(messages)

	// Assistant with request_review + both tool results should be kept
	// system + user + assistant + tool + tool + user = 6
	if len(result) != 6 {
		t.Fatalf("expected 6 messages, got %d", len(result))
		for i, m := range result {
			t.Logf("  [%d] role=%s content=%q", i, m.Role, m.Content)
		}
	}

	// Verify request_review is present
	foundRequestReview := false
	for _, m := range result {
		for _, tc := range m.ToolCalls {
			if tc.Function.Name == "request_review" {
				foundRequestReview = true
			}
		}
	}
	if !foundRequestReview {
		t.Error("request_review tool call should be present")
	}
}

// --- shouldSummarize Tests ---

func TestShouldSummarize_BelowThreshold(t *testing.T) {
	if shouldSummarize(100, 32768, 90) {
		t.Error("usage below 90% of window should not trigger summarization")
	}
}

func TestShouldSummarize_AboveThreshold(t *testing.T) {
	// 92% of a 100-token window (>= 90) should trigger.
	if !shouldSummarize(92, 100, 90) {
		t.Error("usage at 92% of window should trigger summarization")
	}
}

func TestShouldSummarize_AtThreshold(t *testing.T) {
	// Exactly 90% of a 100-token window should trigger (>= 90).
	if !shouldSummarize(90, 100, 90) {
		t.Error("usage at 90% of window should trigger summarization")
	}
}

func TestShouldSummarize_ZeroWindow(t *testing.T) {
	if shouldSummarize(100, 0, 90) {
		t.Error("zero context window should never trigger summarization")
	}
}

func TestShouldSummarize_CustomThreshold(t *testing.T) {
	// With a 50% threshold, 60% of the window should trigger.
	if !shouldSummarize(60, 100, 50) {
		t.Error("usage at 60% of window with 50% threshold should trigger summarization")
	}
	// But below the custom threshold it should not trigger.
	if shouldSummarize(40, 100, 50) {
		t.Error("usage at 40% of window with 50% threshold should not trigger summarization")
	}
}

func TestShouldSummarize_InvalidThresholdFallsBackToDefault(t *testing.T) {
	// Invalid thresholds fall back to the 90% default.
	if !shouldSummarize(90, 100, 0) {
		t.Error("usage at 90% of window with invalid threshold should trigger summarization")
	}
	if !shouldSummarize(90, 100, 150) {
		t.Error("usage at 90% of window with out-of-range threshold should trigger summarization")
	}
}

func TestContextTokensUsed_RealUsagePreferred(t *testing.T) {
	lc := &loopContext{
		usage: &types.ChatUsage{PromptTokens: 8000, CompletionTokens: 2000},
	}
	if got := contextTokensUsed(lc); got != 10000 {
		t.Errorf("contextTokensUsed should prefer real usage, got %d", got)
	}
}

func TestContextTokensUsed_HeuristicFallback(t *testing.T) {
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

func TestMaybeAutoSummarize_TriggersAboveThreshold(t *testing.T) {
	// Summarization renders prompts/summarize.tmpl from the CWD — chdir to the
	// repo root so the template is found (mirrors how the running server resolves it).
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	oldwd, _ := os.Getwd()
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("chdir to repo root: %v", err)
	}
	defer func() { _ = os.Chdir(oldwd) }()

	te := newTestExecutor(t)
	te.gate = NewConcurrencyGate()
	s := te.store
	proj := createProject(t, s, "proj-sum-trigger", "Sum Trigger")

	server := mockSummarizeLLM()
	defer server.Close()

	now := time.Now().UTC()
	task := &types.Task{
		ID:              "sum-trigger-task",
		ProjectID:       proj.ID,
		Title:           "Sum task",
		Status:          "review",
		ExecutionState:  "idle",
		LlamaServerURL:  server.URL,
		ModelName:       "test-model",
		CurrentThreadID: "thread-1",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Context usage at 92% of a 10000-token window (> 90%) should trigger.
	cw := 10000
	lc := &loopContext{
		messages:      []types.ChatMessage{{Role: "user", Content: "hello"}},
		contextWindow: &cw,
		usage:         &types.ChatUsage{PromptTokens: 9200, CompletionTokens: 0},
		detector:      newLoopDetector(),
	}
	setup := &taskExecSetup{
		task:      task,
		projectID: proj.ID,
		ctx:       context.Background(),
		serverURL: server.URL,
		modelName: "test-model",
	}

	te.maybeAutoSummarize(setup, lc, "test-model")

	var hasSummary bool
	for _, m := range lc.messages {
		if m.Role == "summarization" {
			hasSummary = true
		}
	}
	if !hasSummary {
		t.Fatal("expected a summarization message to be appended when context exceeds 90%")
	}
	if task.Summarizing {
		t.Fatal("Summarizing flag should be cleared on the caller's task after summarization")
	}
}

func TestMaybeAutoSummarize_NoTriggerBelowThreshold(t *testing.T) {
	te := newTestExecutor(t)
	te.gate = NewConcurrencyGate()
	s := te.store
	proj := createProject(t, s, "proj-sum-notrigger", "Sum NoTrigger")

	server := mockSummarizeLLM()
	defer server.Close()

	now := time.Now().UTC()
	task := &types.Task{
		ID:              "sum-notrigger-task",
		ProjectID:       proj.ID,
		Status:          "review",
		ExecutionState:  "idle",
		LlamaServerURL:  server.URL,
		ModelName:       "test-model",
		CurrentThreadID: "thread-1",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Context usage at 50% — well below threshold — must NOT trigger.
	cw := 10000
	lc := &loopContext{
		messages:      []types.ChatMessage{{Role: "user", Content: "hello"}},
		contextWindow: &cw,
		usage:         &types.ChatUsage{PromptTokens: 5000, CompletionTokens: 0},
		detector:      newLoopDetector(),
	}
	setup := &taskExecSetup{
		task:      task,
		projectID: proj.ID,
		ctx:       context.Background(),
		serverURL: server.URL,
		modelName: "test-model",
	}

	te.maybeAutoSummarize(setup, lc, "test-model")

	for _, m := range lc.messages {
		if m.Role == "summarization" {
			t.Fatal("summarization should NOT trigger below 90% threshold")
		}
	}
}

// --- Reasoning-Only Loop Detection Tests ---

func TestLoopDetector_ReasoningOnlyIncrementsHintCount(t *testing.T) {
	te := newTestExecutor(t)
	now := time.Now().UTC()

	// Create project and task
	proj := &types.Project{ID: "proj-reasoning", Name: "Reasoning Test", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}
	if err := te.store.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &types.Task{
		ID: "task-reasoning", ProjectID: proj.ID, Title: "Test", Prompt: "Test",
		Status: "in_progress", ExecutionState: "running",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := te.store.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	detector := newLoopDetector()
	messages := []types.ChatMessage{{Role: "system", Content: "system prompt"}}

	// First call — should NOT terminate (streak 1/3)
	terminated := handleLoopDetected(te, task, proj.ID, task.ID, detector, LoopReasoningOnly, "", &messages, false)
	if terminated {
		t.Fatal("first hint should not terminate")
	}
	if detector.loopStreak != 1 {
		t.Errorf("expected loopStreak=1, got %d", detector.loopStreak)
	}

	// Second call — should NOT terminate (streak 2/3)
	terminated = handleLoopDetected(te, task, proj.ID, task.ID, detector, LoopReasoningOnly, "", &messages, false)
	if terminated {
		t.Fatal("second hint should not terminate")
	}
	if detector.loopStreak != 2 {
		t.Errorf("expected loopStreak=2, got %d", detector.loopStreak)
	}

	// Third call — SHOULD terminate (streak 3 >= maxLoopHints)
	terminated = handleLoopDetected(te, task, proj.ID, task.ID, detector, LoopReasoningOnly, "", &messages, false)
	if !terminated {
		t.Fatal("third hint should terminate (>= maxLoopHints)")
	}

	// Verify task was marked as failed
	taskAfter, _ := te.store.GetTaskByTaskID(task.ID)
	if taskAfter.ExecutionState != "failed" {
		t.Errorf("expected execution_state='failed', got %q", taskAfter.ExecutionState)
	}
}

// TestLoopDetector_ResetClearsStreak verifies that detector.reset() clears loopStreak,
// which is the mechanism used when a user action (e.g., message delete) changes context.
func TestLoopDetector_ResetClearsStreak(t *testing.T) {
	te := newTestExecutor(t)
	now := time.Now().UTC()

	proj := &types.Project{ID: "proj-reset-streak", Name: "Reset Streak", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}
	if err := te.store.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := &types.Task{
		ID: "task-reset-streak", ProjectID: proj.ID, Title: "Test", Prompt: "Test",
		Status: "in_progress", ExecutionState: "running",
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
	taskAfter, _ := te.store.GetTaskByTaskID(task.ID)
	if taskAfter.ExecutionState == "failed" {
		t.Error("task should not be failed — reset cleared the old streak")
	}
}

// TestNotifyContextChanged_SendsToChannel verifies that NotifyContextChanged
// sends a UserMessage with ContextChanged=true to the executor channel.
func TestNotifyContextChanged_SendsToChannel(t *testing.T) {
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
	te.NotifyContextChanged("proj", "test-task")

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
	te := newTestExecutor(t)

	// Should not panic when no task is running
	te.NotifyContextChanged("proj", "nonexistent-task")
}

// --- raw_args Delta Tests ---

// computeToolCallData builds the data map for a tool_call WS event,
// mirroring the logic in loop.go.
// Returns the data map with either raw_args (full) or raw_args_delta (incremental).
func computeToolCallData(args string, prevLen int, isNew bool) map[string]any {
	data := map[string]any{
		"tool_id": "call_test", "tool_name": "bash", "tool_display_name": "Run Command",
		"args": nil, "path": "", "sandbox": false,
	}
	if isNew {
		data["raw_args"] = args
	} else {
		data["raw_args_delta"] = args[prevLen:]
	}
	return data
}

func TestRawArgsDelta_NewToolCall(t *testing.T) {
	// New tool call: sends full raw_args
	args := `{"command":"ls -la"}`
	data := computeToolCallData(args, 0, true)

	if _, ok := data["raw_args"]; !ok {
		t.Fatal("new tool call should have raw_args field")
	}
	if data["raw_args"] != args {
		t.Errorf("expected raw_args=%q, got %q", args, data["raw_args"])
	}
	if _, ok := data["raw_args_delta"]; ok {
		t.Error("new tool call should NOT have raw_args_delta field")
	}
}

func TestRawArgsDelta_Update(t *testing.T) {
	// Update: sends only the delta (new characters)
	fullArgs := `{"command":"ls -la /home/user/projects"}`
	prevArgs := `{"command":"ls -la"}`
	prevLen := len(prevArgs)

	data := computeToolCallData(fullArgs, prevLen, false)

	if _, ok := data["raw_args"]; ok {
		t.Error("update should NOT have raw_args field")
	}
	if _, ok := data["raw_args_delta"]; !ok {
		t.Fatal("update should have raw_args_delta field")
	}
	expectedDelta := fullArgs[prevLen:] // " /home/user/projects"}
	if data["raw_args_delta"] != expectedDelta {
		t.Errorf("expected raw_args_delta=%q, got %q", expectedDelta, data["raw_args_delta"])
	}
}

func TestRawArgsDelta_Accumulation(t *testing.T) {
	// Simulate frontend accumulation of deltas
	chunks := []string{
		`{"command":"ls`, // chunk 1 (new, full)
		` -la`,           // chunk 2 (delta)
		` /home/user`,    // chunk 3 (delta)
		`/projects"}`,    // chunk 4 (delta)
	}

	// Simulate: first chunk is full raw_args, subsequent are deltas
	var accumulated string
	for i, chunk := range chunks {
		if i == 0 {
			accumulated = chunk // first = full raw_args
		} else {
			accumulated += chunk // subsequent = raw_args_delta appended
		}
	}

	expected := `{"command":"ls -la /home/user/projects"}`
	if accumulated != expected {
		t.Errorf("expected accumulated=%q, got %q", expected, accumulated)
	}
}

func TestRawArgsDelta_BandwidthSavings(t *testing.T) {
	// Verify that delta approach saves bandwidth for large write_file calls
	// Simulate a write_file with 10KB of content
	largeContent := strings.Repeat("x", 10000)
	fullArgs := `{"path":"./file.txt","content":"` + largeContent + `"}`

	// Simulate 5 streaming updates (each adding 2000 chars)
	var totalSent int
	prevLen := 0
	chunkSize := 2000

	// First update: full raw_args
	totalSent += len(fullArgs[0:chunkSize])
	prevLen = chunkSize

	// Subsequent updates: raw_args_delta only
	for prevLen < len(fullArgs) {
		nextLen := prevLen + chunkSize
		if nextLen > len(fullArgs) {
			nextLen = len(fullArgs)
		}
		totalSent += len(fullArgs[prevLen:nextLen]) // delta only
		prevLen = nextLen
	}

	// Old approach: would send full args on every update
	numUpdates := (len(fullArgs) + chunkSize - 1) / chunkSize
	oldTotal := len(fullArgs) * numUpdates

	// Delta approach should save significant bandwidth
	savings := float64(oldTotal-totalSent) / float64(oldTotal) * 100
	if savings < 50 {
		t.Errorf("expected >50%% bandwidth savings, got %.1f%%", savings)
	}
	t.Logf("bandwidth savings: %.1f%% (old=%d bytes, new=%d bytes)", savings, oldTotal, totalSent)
}

// --- streamFlusher Tests ---

func TestStreamFlusher_TokenBatching(t *testing.T) {
	var events []types.StreamEvent
	flusher := newStreamFlusher("proj", "task", false, func(_, _ string, e types.StreamEvent) {
		events = append(events, e)
	}, newLoopDetector())

	// Append multiple tokens
	flusher.appendToken("Hello")
	flusher.appendToken(" ")
	flusher.appendToken("world")

	// No events sent yet (not flushed)
	if len(events) != 0 {
		t.Fatalf("expected 0 events before flush, got %d", len(events))
	}

	// Flush — should send a single batched token event
	flusher.flush()
	if len(events) != 1 {
		t.Fatalf("expected 1 event after flush, got %d", len(events))
	}
	if events[0].Type != types.WSTypeToken {
		t.Fatalf("expected WSTypeToken, got %v", events[0].Type)
	}
	text := events[0].Data.(map[string]string)["text"]
	if text != "Hello world" {
		t.Errorf("expected 'Hello world', got %q", text)
	}
}

func TestStreamFlusher_ReasoningBatching(t *testing.T) {
	var events []types.StreamEvent
	flusher := newStreamFlusher("proj", "task", false, func(_, _ string, e types.StreamEvent) {
		events = append(events, e)
	}, newLoopDetector())

	flusher.appendReasoning("Thinking")
	flusher.appendReasoning(" about")
	flusher.appendReasoning(" the problem")

	flusher.flush()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != types.WSTypeReasoning {
		t.Fatalf("expected WSTypeReasoning, got %v", events[0].Type)
	}
	text := events[0].Data.(map[string]string)["text"]
	if text != "Thinking about the problem" {
		t.Errorf("expected 'Thinking about the problem', got %q", text)
	}
}

func TestStreamFlusher_ToolCallBatching(t *testing.T) {
	var events []types.StreamEvent
	flusher := newStreamFlusher("proj", "task", false, func(_, _ string, e types.StreamEvent) {
		events = append(events, e)
	}, newLoopDetector())

	// Simulate streaming tool call args: {"command":"ls -la"}
	// First chunk: new tool call with partial args
	flusher.appendToolCall("call_1", "bash", "Run Command", "", `{"command":"ls`, 14, 0, true, false)
	// Second chunk: args grew
	flusher.appendToolCall("call_1", "bash", "Run Command", "", `{"command":"ls -la"}`, 20, 14, false, false)

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
	var events []types.StreamEvent
	flusher := newStreamFlusher("proj", "task", false, func(_, _ string, e types.StreamEvent) {
		events = append(events, e)
	}, newLoopDetector())

	// Simulate existing tool call (prevLen > 0, so it's an update)
	flusher.appendToolCall("call_2", "bash", "Run Command", "", `{"command":"ls -la /home"}`, 26, 14, false, false)

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

func TestStreamFlusher_MixedEvents(t *testing.T) {
	var events []types.StreamEvent
	flusher := newStreamFlusher("proj", "task", false, func(_, _ string, e types.StreamEvent) {
		events = append(events, e)
	}, newLoopDetector())

	// Mix of token, reasoning, and tool_call
	flusher.appendToken("Hello")
	flusher.appendReasoning("thinking...")
	flusher.appendToolCall("call_1", "bash", "Run Command", "", `{"command":"ls"}`, 16, 0, true, false)

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
	var events []types.StreamEvent
	flusher := newStreamFlusher("proj", "task", false, func(_, _ string, e types.StreamEvent) {
		events = append(events, e)
	}, newLoopDetector())

	flusher.appendToken("Hello")
	flusher.appendReasoning("thinking")
	flusher.appendToolCall("call_1", "bash", "Run Command", "", `{"command":"ls"}`, 16, 0, true, false)

	// Reset should clear all buffers
	flusher.reset()

	// Flush should send nothing
	flusher.flush()
	if len(events) != 0 {
		t.Fatalf("expected 0 events after reset+flush, got %d", len(events))
	}
}

func TestStreamFlusher_MultipleToolCalls(t *testing.T) {
	var events []types.StreamEvent
	flusher := newStreamFlusher("proj", "task", false, func(_, _ string, e types.StreamEvent) {
		events = append(events, e)
	}, newLoopDetector())

	// Two separate tool calls
	flusher.appendToolCall("call_1", "bash", "Run Command", "", `{"command":"ls"}`, 16, 0, true, false)
	flusher.appendToolCall("call_2", "read_file", "Read File", "./foo.txt", `{"path":"./foo.txt"}`, 19, 0, true, false)

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
	var events []types.StreamEvent
	flusher := newStreamFlusher("proj", "task", false, func(_, _ string, e types.StreamEvent) {
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
	// Simulate DB messages with reasoning content
	taskMsgs := []types.TaskMessage{
		{Role: "user", Content: "Build a feature"},
		{Role: "assistant", Content: "I'll implement it", ReasoningContent: "Let me think about the approach..."},
		{Role: "assistant", Content: "Done!", ReasoningContent: ""},
		{Role: "assistant", Content: "", ReasoningContent: "I need to reason before acting"},
	}

	task := &types.Task{OperatingMode: "act"}
	messages := buildMessagesFromDB(task, taskMsgs, "/worktree", "proj", false)

	// Skip system prompt (index 0)
	if len(messages) != 5 {
		t.Fatalf("expected 5 messages (system + 4), got %d", len(messages))
	}

	// User message
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

	// Assistant without reasoning
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
	taskMsgs := []types.TaskMessage{
		{Role: "user", Content: "Build a feature"},
		{Role: "user", Content: "You repeated yourself in your last response.", IsLoopHint: true},
		{Role: "assistant", Content: "I'll implement it"},
	}

	task := &types.Task{OperatingMode: "act"}
	messages := buildMessagesFromDB(task, taskMsgs, "/worktree", "proj", false)

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
	taskMsgs := []types.TaskMessage{
		{Role: "user", Content: "Read a file"},
		{Role: "assistant", Content: "I'll read it", ReasoningContent: "Need to check the file first"},
		{Role: "tool_call", ToolCalls: `{"tool_id":"call_1","tool_name":"read_file","args":{"path":"foo.txt"}}`, ToolResult: `{"output":"file contents","success":true,"duration":10}`},
	}

	task := &types.Task{OperatingMode: "act"}
	messages := buildMessagesFromDB(task, taskMsgs, "/worktree", "proj", false)

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
	messages := buildMessagesFromDB(task, taskMsgs, "/worktree", "proj", false)

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

func TestRemoveDuplicateUserMessage_PlainText(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "prompt"},
		{Role: "user", Content: "hello"},
		{Role: "user", Content: "hello"}, // duplicate
		{Role: "assistant", Content: "hi"},
	}

	result := removeDuplicateUserMessage(messages)

	// Should have removed one of the duplicate user messages
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	// The newer user message (index 2 in original) should be kept
	if result[1].Role != "user" || result[1].Content != "hello" {
		t.Errorf("expected user 'hello', got role=%s content=%s", result[1].Role, result[1].Content)
	}
}

func TestRemoveDuplicateUserMessage_DifferentContent(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "prompt"},
		{Role: "user", Content: "hello"},
		{Role: "user", Content: "world"}, // different
		{Role: "assistant", Content: "hi"},
	}

	result := removeDuplicateUserMessage(messages)

	// Should NOT remove anything
	if len(result) != 4 {
		t.Fatalf("expected 4 messages (no duplicates), got %d", len(result))
	}
}

func TestRemoveDuplicateUserMessage_ContentPartsSameText(t *testing.T) {
	imgSource := &struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	}{Type: "base64", MediaType: "image/png", Data: "abc123"}

	messages := []types.ChatMessage{
		{Role: "system", Content: "prompt"},
		{Role: "user", ContentParts: []types.ContentPart{
			{Type: "text", Text: "check this"},
			{Type: "image", ImageSource: imgSource},
		}},
		{Role: "user", ContentParts: []types.ContentPart{
			{Type: "text", Text: "check this"},
			{Type: "image", ImageSource: imgSource},
		}}, // duplicate with same parts
	}

	result := removeDuplicateUserMessage(messages)

	// Should have removed one duplicate
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
}

func TestRemoveDuplicateUserMessage_ContentPartsDifferentText(t *testing.T) {
	imgSource := &struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	}{Type: "base64", MediaType: "image/png", Data: "abc123"}

	messages := []types.ChatMessage{
		{Role: "system", Content: "prompt"},
		{Role: "user", ContentParts: []types.ContentPart{
			{Type: "text", Text: "check this"},
			{Type: "image", ImageSource: imgSource},
		}},
		{Role: "user", ContentParts: []types.ContentPart{
			{Type: "text", Text: "check that"}, // different text
			{Type: "image", ImageSource: imgSource},
		}},
	}

	result := removeDuplicateUserMessage(messages)

	// Should NOT remove — text differs
	if len(result) != 3 {
		t.Fatalf("expected 3 messages (different text), got %d", len(result))
	}
}

func TestRemoveDuplicateUserMessage_ContentPartsDifferentCount(t *testing.T) {
	imgSource := &struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	}{Type: "base64", MediaType: "image/png", Data: "abc123"}

	messages := []types.ChatMessage{
		{Role: "system", Content: "prompt"},
		{Role: "user", ContentParts: []types.ContentPart{
			{Type: "text", Text: "check this"},
		}},
		{Role: "user", ContentParts: []types.ContentPart{
			{Type: "text", Text: "check this"},
			{Type: "image", ImageSource: imgSource}, // has image, other doesn't
		}},
	}

	result := removeDuplicateUserMessage(messages)

	// Should NOT remove — different part counts
	if len(result) != 3 {
		t.Fatalf("expected 3 messages (different part counts), got %d", len(result))
	}
}

func TestRemoveDuplicateUserMessage_MixedPlainAndContentParts(t *testing.T) {
	imgSource := &struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	}{Type: "base64", MediaType: "image/png", Data: "abc123"}

	messages := []types.ChatMessage{
		{Role: "system", Content: "prompt"},
		{Role: "user", Content: "hello"}, // plain text
		{Role: "user", ContentParts: []types.ContentPart{
			{Type: "text", Text: "hello"},
			{Type: "image", ImageSource: imgSource},
		}}, // has ContentParts
	}

	result := removeDuplicateUserMessage(messages)

	// Should NOT remove — one is plain, one has ContentParts
	if len(result) != 3 {
		t.Fatalf("expected 3 messages (mixed types), got %d", len(result))
	}
}

func TestBuildContextReplay_Empty(t *testing.T) {
	result := buildContextReplay(nil)
	if !strings.Contains(result, "<conversation_history>") {
		t.Error("expected conversation_history tags")
	}
	if !strings.Contains(result, "</conversation_history>") {
		t.Error("expected closing conversation_history tag")
	}
}

func TestBuildContextReplay_UserAndAssistant(t *testing.T) {
	msgs := []types.TaskMessage{
		{Role: "user", Content: "Build a todo app"},
		{Role: "assistant", Content: "Sure, let me create the files."},
		{Role: "user", Content: "Also add tests"},
	}
	result := buildContextReplay(msgs)
	if !strings.Contains(result, "**User**: Build a todo app") {
		t.Error("expected first user message")
	}
	if !strings.Contains(result, "**Assistant**: Sure, let me create the files.") {
		t.Error("expected assistant message")
	}
	if !strings.Contains(result, "**User**: Also add tests") {
		t.Error("expected second user message")
	}
}

func TestBuildContextReplay_WithReasoning(t *testing.T) {
	msgs := []types.TaskMessage{
		{Role: "assistant", ReasoningContent: "Let me think about this...", Content: "Here's the answer."},
	}
	result := buildContextReplay(msgs)
	if !strings.Contains(result, "**Assistant (thinking)**: Let me think about this...") {
		t.Error("expected reasoning content")
	}
	if !strings.Contains(result, "**Assistant**: Here's the answer.") {
		t.Error("expected assistant content")
	}
}

func TestBuildContextReplay_WithToolCalls(t *testing.T) {
	toolCalls := `[{"id":"call_1","name":"write_file","display_name":"Write File","args":{"path":"test.js","content":"hello"}}]`
	toolResult := `[{"id":"call_1","output":"File written","success":true}]`
	msgs := []types.TaskMessage{
		{Role: "tool_call", ToolCalls: toolCalls, ToolResult: toolResult},
	}
	result := buildContextReplay(msgs)
	if !strings.Contains(result, "**Tool calls**:") {
		t.Error("expected tool calls section")
	}
	if !strings.Contains(result, "Write File") {
		t.Error("expected tool display name")
	}
	if !strings.Contains(result, "**Tool results**:") {
		t.Error("expected tool results section")
	}
	if !strings.Contains(result, "File written") {
		t.Error("expected tool result output")
	}
}

func TestBuildContextReplay_WithSummarization(t *testing.T) {
	msgs := []types.TaskMessage{
		{Role: "user", Content: "First message"},
		{Role: "summarization", Content: "Summary of earlier conversation"},
		{Role: "user", Content: "After summary"},
	}
	result := buildContextReplay(msgs)
	if !strings.Contains(result, "**[Context was summarized]**") {
		t.Error("expected summarization marker")
	}
	if !strings.Contains(result, "Summary of earlier conversation") {
		t.Error("expected summary content")
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

func TestBackoffFor_RateLimitedWithRetryAfter(t *testing.T) {
	// Retry-After is honored: delay + 1s + 250-500ms jitter, on every attempt.
	err := &llama.LlamaError{StatusCode: 429, RetryAfter: 20 * time.Second}
	min := 20*time.Second + time.Second + 250*time.Millisecond
	max := 20*time.Second + time.Second + 500*time.Millisecond
	for attempt := 0; attempt < 3; attempt++ {
		assertBackoffWithin(t, fmt.Sprintf("attempt %d", attempt), backoffFor(attempt, err), min, max)
	}
}

func TestBackoffFor_RateLimitedRetryAfterUncapped(t *testing.T) {
	// A huge Retry-After is honored uncapped: 10h + 1s + 250-500ms jitter.
	err := &llama.LlamaError{StatusCode: 429, RetryAfter: 10 * time.Hour}
	min := 10*time.Hour + time.Second + 250*time.Millisecond
	max := 10*time.Hour + time.Second + 500*time.Millisecond
	assertBackoffWithin(t, "attempt 0", backoffFor(0, err), min, max)
}

func TestBackoffFor_RateLimitedExponential(t *testing.T) {
	// No Retry-After -> exponential 2s, 4s, 8s.
	err := &llama.LlamaError{StatusCode: 429}
	wants := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
	for attempt, want := range wants {
		if got := backoffFor(attempt, err); got != want {
			t.Errorf("attempt %d: backoff = %v, want %v", attempt, got, want)
		}
	}
}

func TestBackoffFor_GenericExponential(t *testing.T) {
	// Non-rate-limit errors use the same exponential 2s, 4s, 8s schedule.
	err := errors.New("connection reset")
	wants := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}
	for attempt, want := range wants {
		if got := backoffFor(attempt, err); got != want {
			t.Errorf("attempt %d: backoff = %v, want %v", attempt, got, want)
		}
	}
}

func TestBackoffFor_GenericLlamaErrorNotRateLimited(t *testing.T) {
	// A LlamaError with a non-429 code uses the exponential schedule (Retry-After ignored).
	err := &llama.LlamaError{StatusCode: 500, RetryAfter: 30 * time.Second}
	if got := backoffFor(0, err); got != 2*time.Second {
		t.Errorf("backoff = %v, want 2s", got)
	}
}
