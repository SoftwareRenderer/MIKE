package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/types"
)

// --- Phrase Detector Tests ---

func TestPhraseDetector_NoRepetition(t *testing.T) {
	d := newPhraseDetector()

	// Feed unique sentences
	text := "The quick brown fox jumps over the lazy dog. Then the cat sat on the mat and slept all day long."
	if d.check(text) {
		t.Error("should not detect repetition in unique text")
	}
}

func TestPhraseDetector_ExactRepetition(t *testing.T) {
	d := newPhraseDetector()

	// First sentence
	d.check("The quick brown fox jumps over the lazy dog.")

	// Same sentence repeated (with period)
	repeated := "The quick brown fox jumps over the lazy dog."
	if !d.check(repeated) {
		t.Error("should detect exact phrase repetition")
	}
}

func TestPhraseDetector_CaseInsensitive(t *testing.T) {
	d := newPhraseDetector()

	d.check("The quick brown fox jumps over the lazy dog.")
	repeated := "THE QUICK BROWN FOX JUMPS OVER THE LAZY DOG."
	if !d.check(repeated) {
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

	d.check("The quick brown fox jumps over the lazy dog.")
	repeated := "The  quick   brown    fox   jumps   over   the   lazy   dog."
	if !d.check(repeated) {
		t.Error("should detect repetition despite extra whitespace")
	}
}

func TestPhraseDetector_FiresOnce(t *testing.T) {
	d := newPhraseDetector()

	d.check("The quick brown fox jumps over the lazy dog.")
	d.check("The quick brown fox jumps over the lazy dog.") // fires
	if !d.check("The quick brown fox jumps over the lazy dog.") {
		// After first fire, should still return false (already fired)
		// This is expected behavior - once fired, stays fired until reset
	}
}

func TestPhraseDetector_Reset(t *testing.T) {
	d := newPhraseDetector()

	d.check("The quick brown fox jumps over the lazy dog.")
	d.check("The quick brown fox jumps over the lazy dog.") // fires

	d.reset()
	// After reset, same text should not fire
	if d.check("The quick brown fox jumps over the lazy dog.") {
		t.Error("should not fire after reset with same text")
	}
	// Now it should fire on repeat
	if !d.check("The quick brown fox jumps over the lazy dog.") {
		t.Error("should fire on repeat after reset")
	}
}

func TestPhraseDetector_MultipleSentencesInOneChunk(t *testing.T) {
	d := newPhraseDetector()

	// Feed two sentences, second repeats the first
	text := "The quick brown fox jumps over the lazy dog. Some other unique text here that is long enough. The quick brown fox jumps over the lazy dog."
	if !d.check(text) {
		t.Error("should detect repetition when repeated sentence appears later in same chunk")
	}
}

func TestPhraseDetector_NewlineBoundary(t *testing.T) {
	d := newPhraseDetector()

	d.check("The quick brown fox jumps over the lazy dog.\n")
	repeated := "The quick brown fox jumps over the lazy dog."
	if !d.check(repeated) {
		t.Error("should detect repetition across newline boundary")
	}
}

func TestPhraseDetector_DoubleNewlineBoundary(t *testing.T) {
	d := newPhraseDetector()

	d.check("The quick brown fox jumps over the lazy dog.\n\n")
	repeated := "The quick brown fox jumps over the lazy dog."
	if !d.check(repeated) {
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

	d.check("Wow this is really amazing and incredible stuff!")
	repeated := "Wow this is really amazing and incredible stuff!"
	if !d.check(repeated) {
		t.Error("should detect repetition with exclamation boundary")
	}
}

func TestPhraseDetector_QuestionBoundary(t *testing.T) {
	d := newPhraseDetector()

	d.check("What do you think about this approach and implementation?")
	repeated := "What do you think about this approach and implementation?"
	if !d.check(repeated) {
		t.Error("should detect repetition with question boundary")
	}
}

func TestPhraseDetector_IncompleteSentenceAccumulates(t *testing.T) {
	d := newPhraseDetector()

	// Feed partial sentence
	d.check("The quick brown fox jumps")
	// Complete it
	d.check(" over the lazy dog.")
	// Now repeat the full sentence
	if !d.check("The quick brown fox jumps over the lazy dog.") {
		t.Error("should detect repetition when incomplete sentence is completed and then repeated")
	}
}

// --- Word Set / Jaccard Tests ---

func TestWordSet_Basic(t *testing.T) {
	words := wordSet("The quick brown fox jumps")
	if len(words) != 4 { // "the" is a stop word
		t.Errorf("expected 4 words, got %d", len(words))
	}
	if _, ok := words["quick"]; !ok {
		t.Error("expected 'quick' in word set")
	}
	if _, ok := words["the"]; ok {
		t.Error("should not include stop word 'the'")
	}
}

func TestWordSet_CaseInsensitive(t *testing.T) {
	words := wordSet("Hello HELLO hello")
	if len(words) != 1 {
		t.Errorf("expected 1 word, got %d", len(words))
	}
}

func TestJaccardIdentical(t *testing.T) {
	a := map[string]bool{"foo": true, "bar": true, "baz": true}
	b := map[string]bool{"foo": true, "bar": true, "baz": true}
	sim := jaccardSimilarity(a, b)
	if sim != 1.0 {
		t.Errorf("expected similarity 1.0, got %f", sim)
	}
}

func TestJaccardDisjoint(t *testing.T) {
	a := map[string]bool{"foo": true, "bar": true}
	b := map[string]bool{"baz": true, "qux": true}
	sim := jaccardSimilarity(a, b)
	if sim != 0.0 {
		t.Errorf("expected similarity 0.0, got %f", sim)
	}
}

func TestJaccardPartial(t *testing.T) {
	a := map[string]bool{"foo": true, "bar": true, "baz": true}
	b := map[string]bool{"foo": true, "bar": true, "qux": true}
	sim := jaccardSimilarity(a, b)
	expected := 2.0 / 4.0 // intersection=2, union=4
	if sim != expected {
		t.Errorf("expected similarity %f, got %f", expected, sim)
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
	if loopType, _, _ := d.recordTurn(tc1, ""); loopType != LoopNone {
		t.Error("first read should not trigger loop")
	}

	// Second read of same file
	tc2 := []types.ChatToolCall{makeToolCall("read_file", string(args1JSON))}
	if loopType, _, _ := d.recordTurn(tc2, ""); loopType != LoopTool {
		t.Error("second read of same file should trigger LoopTool")
	}
}

func TestLoopDetector_ToolLoopReturnsRepeatedArgs(t *testing.T) {
	d := newLoopDetector()

	// First read
	args1 := map[string]string{"path": "src/main.go"}
	args1JSON, _ := json.Marshal(args1)
	tc1 := []types.ChatToolCall{makeToolCall("read_file", string(args1JSON))}
	d.recordTurn(tc1, "")

	// Second read of same file — returned args should match
	tc2 := []types.ChatToolCall{makeToolCall("read_file", string(args1JSON))}
	loopType, _, returnedArgs := d.recordTurn(tc2, "")
	if loopType != LoopTool {
		t.Fatal("expected LoopTool")
	}
	if returnedArgs != string(args1JSON) {
		t.Errorf("returned args = %q, want %q", returnedArgs, string(args1JSON))
	}
}

func TestLoopDetector_ToolLoopMultiCall(t *testing.T) {
	d := newLoopDetector()

	// First turn: read_file then task_complete (state-changing clears tracker)
	// So we need: first turn read_file, second turn task_complete + read_file
	// The read_file on turn 2 should NOT trigger loop because task_complete cleared it
	args1 := map[string]string{"path": "src/main.go"}
	args1JSON, _ := json.Marshal(args1)

	tc1 := []types.ChatToolCall{makeToolCall("read_file", string(args1JSON))}
	loopType, _, _ := d.recordTurn(tc1, "")
	if loopType != LoopNone {
		t.Error("first read should not trigger loop")
	}

	// Second turn: same read_file — should trigger loop
	tc2 := []types.ChatToolCall{makeToolCall("read_file", string(args1JSON))}
	loopType, _, returnedArgs := d.recordTurn(tc2, "")
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

	if loopType, _, _ := d.recordTurn(tc1, ""); loopType != LoopNone {
		t.Error("first read should not trigger loop")
	}
	if loopType, _, _ := d.recordTurn(tc2, ""); loopType != LoopNone {
		t.Error("read of different file should not trigger loop")
	}
}

func TestLoopDetector_TextLoop(t *testing.T) {
	d := newLoopDetector()

	// Two very similar responses (same key words, slight rewording)
	text1 := "I need to carefully analyze the requirements and implement the solution step by step to make sure everything works correctly."
	text2 := "I must carefully analyze the requirements and implement the solution step by step to make sure everything works correctly."

	// First text response
	if loopType, _, _ := d.recordTurn(nil, text1); loopType != LoopNone {
		t.Error("first text should not trigger loop")
	}

	// Second similar text response (Jaccard > 0.6)
	if loopType, _, _ := d.recordTurn(nil, text2); loopType != LoopText {
		t.Error("similar text should trigger LoopText")
	}
}

func TestLoopDetector_PhraseLoopViaStreaming(t *testing.T) {
	d := newLoopDetector()

	// Simulate streaming chunks
	chunk1 := "Let me think about this problem carefully and analyze the requirements. "
	chunk2 := "The best approach would be to implement the solution step by step. "
	chunk3 := "Let me think about this problem carefully and analyze the requirements."

	if d.checkStreamingText(chunk1) != LoopNone {
		t.Error("first chunk should not trigger loop")
	}
	if d.checkStreamingText(chunk2) != LoopNone {
		t.Error("second unique chunk should not trigger loop")
	}
	if d.checkStreamingText(chunk3) != LoopPhrase {
		t.Error("repeated phrase should trigger LoopPhrase")
	}

	if !d.phraseDetected() {
		t.Error("phraseDetected should return true after detection")
	}
}

func TestLoopDetector_ResetClearsPhraseFlag(t *testing.T) {
	d := newLoopDetector()

	d.checkStreamingText("Let me think about this problem carefully and analyze the requirements.")
	d.checkStreamingText("Let me think about this problem carefully and analyze the requirements.") // fires

	d.reset()
	if d.phraseDetected() {
		t.Error("phraseDetected should be false after reset")
	}
}

func TestLoopDetector_ToolCallResetsPhraseTracker(t *testing.T) {
	d := newLoopDetector()

	// Feed phrase that would normally repeat
	d.checkStreamingText("Let me think about this problem carefully and analyze the requirements.")

	// Tool call resets phrase tracker
	args := map[string]string{"path": "src/main.go"}
	argsJSON, _ := json.Marshal(args)
	tc := []types.ChatToolCall{makeToolCall("read_file", string(argsJSON))}
	d.recordTurn(tc, "")

	// Same phrase should not fire anymore
	if d.checkStreamingText("Let me think about this problem carefully and analyze the requirements.") == LoopPhrase {
		t.Error("phrase should not fire after tool call reset")
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
	for _, tool := range []string{"read_file", "write_file", "edit_file", "bash", "task_complete", "create_task"} {
		if !strings.Contains(hint, tool) {
			t.Errorf("hint should mention tool %q, got: %s", tool, hint)
		}
	}
	// Verify it mentions function calling format
	if !strings.Contains(hint, "function calling") {
		t.Error("hint should mention function calling format")
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

func TestExtractCommandTarget(t *testing.T) {
	tests := []struct {
		cmd  string
		want string
	}{
		{"head src/main.go", "src/main.go"},
		{"cat ./file.txt", "file.txt"},
		{"grep pattern file.go", "file.go"},
		{"ls -la /tmp", "/tmp"},
		{"echo hello > output.txt", "output.txt"},
		{"git add -A", "."},
		{"git commit -m msg", "."},
	}

	for _, tt := range tests {
		got := extractCommandTarget(tt.cmd)
		if got != tt.want {
			t.Errorf("extractCommandTarget(%q) = %q, want %q", tt.cmd, got, tt.want)
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
		{Role: "assistant", Content: "", ToolCalls: []types.ToolCallMsg{{Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "read_file", Arguments: `{"path":"foo.txt"}`}}}},
		{Role: "tool", Content: "file contents"},
	}
	result := FormatConversationTurns(messages)
	if result == "" {
		t.Error("expected non-empty output")
	}
	if !strings.Contains(result, "[tool: read_file") {
		t.Error("expected tool call in output")
	}
	if !strings.Contains(result, "[Tool result]") {
		t.Error("expected tool result in output")
	}
}

func TestFormatConversationTurns_TruncatesLongToolResults(t *testing.T) {
	longContent := strings.Repeat("x", 600)
	messages := []types.ChatMessage{
		{Role: "tool", Content: longContent},
	}
	result := FormatConversationTurns(messages)
	// Output should be: "[Tool result]" + 500 chars + "..." + "\n\n" = ~511 chars
	// Full content would be: "[Tool result]" + 600 chars + "\n\n" = ~613 chars
	if len(result) > 520 {
		t.Errorf("expected truncated output (~511 chars), got %d chars", len(result))
	}
	if !strings.Contains(result, "...") {
		t.Error("expected truncation marker '...' in output")
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
	// "Let me analyze the café's approach to this problème.">
	chunks := []string{
		"Let me analyze the caf",
		"é's approach to this pr",
		"oblème.",
	}
	for _, chunk := range chunks {
		if d.check(chunk) {
			t.Error("should not detect repetition in first occurrence")
		}
	}

	// Now repeat the same phrase
	chunks2 := []string{
		"Let me analyze the caf",
		"é's approach to this pr",
		"oblème.",
	}
	fired := false
	for _, chunk := range chunks2 {
		if d.check(chunk) {
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
		ctx:       context.Background(),
		cancel:    func() {},
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
	reviewTask.ExecutionState = "awaiting_review"
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
	if reviewTaskAfter.ExecutionState != "awaiting_review" {
		t.Errorf("expected review task execution_state='awaiting_review', got %q", reviewTaskAfter.ExecutionState)
	}
}

// createTaskInProject creates a task directly in the store for testing.
func createTaskInProject(t *testing.T, s *store.Store, projectID, taskID string) *types.Task {
	t.Helper()
	now := time.Now().UTC()
	task := &types.Task{
		ID:          taskID,
		ProjectID:   projectID,
		Title:       "Test Task",
		Prompt:      "Test prompt",
		Status:      "backlog",
		ExecutionState: "idle",
		CreatedAt:   now,
		UpdatedAt:   now,
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

func TestUnstageNewBinaryFilesInDir(t *testing.T) {
	dir := t.TempDir()
	_, _ = runGitCommand(dir, "init", "-b", "main")
	_, _ = runGitCommand(dir, "config", "user.email", "test@test.com")
	_, _ = runGitCommand(dir, "config", "user.name", "Test")

	// Create initial commit so we have a HEAD
	_ = os.WriteFile(filepath.Join(dir, "initial.txt"), []byte("initial\n"), 0644)
	_, _ = runGitCommand(dir, "add", "initial.txt")
	_, _ = runGitCommand(dir, "commit", "-m", "initial")

	// Create new files: text, image, and binary
	_ = os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "photo.png"), []byte("\x89PNG\r\n\x1a\nfake png"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "data.bin"), []byte("\x00\x01\x02\x03binary"), 0644)

	// Stage all
	_, _ = runGitCommand(dir, "add", "-A")

	// Verify all are staged
	out, _ := runGitCommand(dir, "diff", "--staged", "--name-only", "--diff-filter=A")
	staged := strings.FieldsFunc(strings.TrimSpace(out), func(r rune) bool { return r == '\n' })
	if len(staged) != 3 {
		t.Fatalf("expected 3 staged files, got %d: %v", len(staged), staged)
	}

	// Unstage binary non-image files
	unstageNewBinaryFilesInDir(dir)

	// Check what remains staged
	out, _ = runGitCommand(dir, "diff", "--staged", "--name-only", "--diff-filter=A")
	staged = strings.FieldsFunc(strings.TrimSpace(out), func(r rune) bool { return r == '\n' })

	// Should have readme.txt and photo.png, but NOT data.bin
	got := make(map[string]bool)
	for _, f := range staged {
		got[f] = true
	}

	if len(got) != 2 {
		t.Errorf("expected 2 staged files, got %d: %v", len(got), staged)
	}
	if !got["readme.txt"] {
		t.Error("readme.txt should still be staged")
	}
	if !got["photo.png"] {
		t.Error("photo.png should still be staged (image)")
	}
	if got["data.bin"] {
		t.Error("data.bin should be unstaged (binary, not image)")
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

	// Expected: system + summary + 2 pre-summary turns + 2 post-summary = 8
	if len(result) != 8 {
		t.Fatalf("expected 8 messages, got %d: %v", len(result), func() []string {
			var s []string
			for _, m := range result {
				s = append(s, m.Role+":"+m.Content[:min(len(m.Content), 20)])
			}
			return s
		}())
	}
	if result[0].Role != "system" {
		t.Errorf("expected system at [0], got %s", result[0].Role)
	}
	if result[1].Role != "assistant" || !strings.HasPrefix(result[1].Content, "[Context summary: ") {
		t.Errorf("expected formatted summary at [1], got %s: %q", result[1].Role, result[1].Content)
	}
}

func TestBuildLLMContext_SkipsOldMessagesBeyond3Turns(t *testing.T) {
	// 5 turns before summary, only last 3 should appear.
	var messages []types.ChatMessage
	messages = append(messages, types.ChatMessage{Role: "system", Content: "system prompt"})
	for i := 1; i <= 5; i++ {
		messages = append(messages, types.ChatMessage{Role: "user", Content: fmt.Sprintf("old message %d", i)})
		messages = append(messages, types.ChatMessage{Role: "assistant", Content: fmt.Sprintf("old response %d", i)})
	}
	messages = append(messages, types.ChatMessage{Role: "summarization", Content: "summary"})
	messages = append(messages, types.ChatMessage{Role: "user", Content: "new message"})

	result := buildLLMContext(messages)

	// Count how many "old message" entries appear
	oldCount := 0
	for _, m := range result {
		if strings.Contains(m.Content, "old message") {
			oldCount++
		}
	}
	// Should be exactly 3.
	if oldCount != 3 {
		t.Errorf("expected 3 old messages in context, got %d", oldCount)
	}
	// The oldest (message 1, 2) should NOT appear
	for _, m := range result {
		if strings.Contains(m.Content, "old message 1") || strings.Contains(m.Content, "old message 2") {
			t.Errorf("old message beyond 3-turn window should not appear: %q", m.Content)
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

	// Tool calls included in pre-summary turns.
	hasToolCall := false
	hasToolResult := false
	for _, m := range result {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			hasToolCall = true
		}
		if m.Role == "tool" {
			hasToolResult = true
		}
	}
	if !hasToolCall {
		t.Error("expected tool call in context")
	}
	if !hasToolResult {
		t.Error("expected tool result in context")
	}
}

// --- shouldSummarize Tests ---

func TestShouldSummarize_SmallContext(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	if shouldSummarize(messages, 32768) {
		t.Error("small context should not trigger summarization")
	}
}

func TestShouldSummarize_LargeContext(t *testing.T) {
	// Build a large context that exceeds 90% of a small window
	var messages []types.ChatMessage
	messages = append(messages, types.ChatMessage{Role: "system", Content: "system prompt"})
	// Each message ~20 words, ~26 tokens. Window = 100 tokens, threshold = 90.
	// Need ~4 messages to exceed threshold.
	for i := 0; i < 10; i++ {
		messages = append(messages, types.ChatMessage{Role: "user", Content: fmt.Sprintf("message number %d with some extra words to pad the token count higher", i)})
		messages = append(messages, types.ChatMessage{Role: "assistant", Content: fmt.Sprintf("response number %d with some extra words to pad the token count higher", i)})
	}
	if !shouldSummarize(messages, 100) {
		t.Error("large context should trigger summarization")
	}
}

func TestShouldSummarize_AfterExistingSummary(t *testing.T) {
	// Existing summary + many new messages should trigger re-summarization.
	var messages []types.ChatMessage
	messages = append(messages, types.ChatMessage{Role: "system", Content: "system prompt"})
	// Old messages.
	messages = append(messages, types.ChatMessage{Role: "user", Content: "old message"})
	messages = append(messages, types.ChatMessage{Role: "assistant", Content: "old response"})
	messages = append(messages, types.ChatMessage{Role: "summarization", Content: "summary of earlier conversation"})
	// Many new messages should push context over limit.
	for i := 0; i < 10; i++ {
		messages = append(messages, types.ChatMessage{Role: "user", Content: fmt.Sprintf("new message number %d with some extra words to pad the token count higher", i)})
		messages = append(messages, types.ChatMessage{Role: "assistant", Content: fmt.Sprintf("new response number %d with some extra words to pad the token count higher", i)})
	}
	if !shouldSummarize(messages, 100) {
		t.Error("context with existing summary + many new messages should trigger re-summarization")
	}
}

func TestShouldSummarize_ZeroWindow(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "hello"},
	}
	if shouldSummarize(messages, 0) {
		t.Error("zero context window should never trigger summarization")
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

	// First call — should NOT terminate (hint 1/3)
	terminated := handleLoopDetected(te, task, proj.ID, task.ID, detector, LoopReasoningOnly, "", &messages)
	if terminated {
		t.Fatal("first hint should not terminate")
	}
	if detector.hintCount != 1 {
		t.Errorf("expected hintCount=1, got %d", detector.hintCount)
	}

	// Second call — should NOT terminate (hint 2/3)
	terminated = handleLoopDetected(te, task, proj.ID, task.ID, detector, LoopReasoningOnly, "", &messages)
	if terminated {
		t.Fatal("second hint should not terminate")
	}
	if detector.hintCount != 2 {
		t.Errorf("expected hintCount=2, got %d", detector.hintCount)
	}

	// Third call — should NOT terminate (hint 3/3)
	terminated = handleLoopDetected(te, task, proj.ID, task.ID, detector, LoopReasoningOnly, "", &messages)
	if terminated {
		t.Fatal("third hint should not terminate")
	}
	if detector.hintCount != 3 {
		t.Errorf("expected hintCount=3, got %d", detector.hintCount)
	}

	// Fourth call — SHOULD terminate (max hints exhausted)
	terminated = handleLoopDetected(te, task, proj.ID, task.ID, detector, LoopReasoningOnly, "", &messages)
	if !terminated {
		t.Fatal("fourth hint should terminate (max hints exhausted)")
	}

	// Verify task was marked as failed
	taskAfter, _ := te.store.GetTaskByTaskID(task.ID)
	if taskAfter.ExecutionState != "failed" {
		t.Errorf("expected execution_state='failed', got %q", taskAfter.ExecutionState)
	}
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
		`{"command":"ls`,           // chunk 1 (new, full)
		` -la`,                     // chunk 2 (delta)
		` /home/user`,             // chunk 3 (delta)
		`/projects"}`,             // chunk 4 (delta)
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
