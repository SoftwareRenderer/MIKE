package exec

import (
	"strings"
	"testing"

	"mike/backend/types"
)

func tailCall(id, name, args string) types.ToolCallMsg {
	tc := types.ToolCallMsg{ID: id, Type: "function"}
	tc.Function.Name = name
	tc.Function.Arguments = args
	return tc
}

// A long conversation with one superseded summary in the middle, so what the reproduction picks up
// comes from the turns the model is about to lose and from nowhere older.
func tailConversation() []types.ChatMessage {
	return []types.ChatMessage{
		{Role: "system", Content: "THE-PROMPT"},
		{Role: "user", Content: "OLDEST-ASK"},
		{Role: "assistant", Content: "TURN-ONE", ToolCalls: []types.ToolCallMsg{tailCall("c1", types.ToolReadFile, `{"path":"a.go"}`)}},
		{Role: "tool", ToolCallID: "c1", Content: "RESULT-ONE"},
		{Role: "summarization", Content: "AN-OLDER-SUMMARY"},
		{Role: "assistant", Content: "TURN-TWO"},
		{Role: "user", Content: "MIDDLE-ASK"},
		{Role: "assistant", Content: "TURN-THREE"},
		{Role: "user", Content: "NEWEST-ASK"},
		{Role: "assistant", Content: "TURN-FOUR"},
	}
}

func TestRecentTurnWindowStopsBeforeADroppedCallsResult(t *testing.T) {
	t.Parallel()
	// buildLLMContext cuts at the summarization row, the same read the next call is built from.
	window := recentTurnWindow(buildLLMContext(tailConversation()))

	if len(window) == 0 || window[0].Content != "TURN-TWO" {
		t.Fatalf("window opens on %+v, want the third-to-last assistant turn", window[0])
	}
	text := renderRecentContext(buildLLMContext(tailConversation()))
	for _, want := range []string{"TURN-TWO", "MIDDLE-ASK", "TURN-THREE", "NEWEST-ASK", "TURN-FOUR"} {
		if !strings.Contains(text, want) {
			t.Errorf("reproduction is missing %q:\n%s", want, text)
		}
	}
	// RESULT-ONE belongs to the turn before the window; a result without its call is noise.
	for _, absent := range []string{"RESULT-ONE", "THE-PROMPT", "AN-OLDER-SUMMARY"} {
		if strings.Contains(text, absent) {
			t.Errorf("reproduction carries %q, which the model did not lose just now", absent)
		}
	}
}

func TestRecentTurnWindowIncludesTheMessageTheWorkAnswers(t *testing.T) {
	t.Parallel()
	conv := []types.ChatMessage{
		{Role: "system", Content: "prompt"},
		{Role: "user", Content: "THE-ASK"},
		{Role: "assistant", Content: "ONE"},
		{Role: "assistant", Content: "TWO"},
		{Role: "assistant", Content: "THREE"},
	}
	window := recentTurnWindow(conv)
	if window[0].Role != "user" || window[0].Content != "THE-ASK" {
		t.Errorf("window opens on role=%q %q, want the ask the reproduced work answers", window[0].Role, window[0].Content)
	}

	// A short conversation has nothing to drop: reproduce all of it, and still no system prompt.
	short := renderRecentContext([]types.ChatMessage{
		{Role: "system", Content: "THE-PROMPT"},
		{Role: "user", Content: "ask"},
		{Role: "assistant", Content: "answer"},
	})
	for _, want := range []string{"ask", "answer"} {
		if !strings.Contains(short, want) {
			t.Errorf("a two-message conversation reproduced without %q: %q", want, short)
		}
	}
	if strings.Contains(short, "THE-PROMPT") {
		t.Errorf("the system prompt was reproduced as if the run had lost it: %q", short)
	}
}

func TestRenderRecentContextEndsEachPieceWhereTheModelNeedsIt(t *testing.T) {
	t.Parallel()
	conv := []types.ChatMessage{
		{Role: "system", Content: "prompt"},
		{Role: "user", Content: "fix the flake"},
		{Role: "assistant",
			ReasoningContent: filler(3000, "weighing") + "\nSO THE FIX IS IN runner.go",
			Content:          filler(3000, "saying"),
			ToolCalls:        []types.ToolCallMsg{tailCall("c1", types.ToolEditFile, `{"path":"runner.go","edits":[`+filler(2000, "old")+`]}`)},
		},
		{Role: "tool", ToolCallID: "c1", Content: "EDIT-LANDED-HERE\n" + filler(3000, "output")},
	}
	text := renderRecentContext(conv)

	if !strings.Contains(text, "SO THE FIX IS IN runner.go") {
		t.Error("the end of the reasoning block was dropped; that is the part naming the next move")
	}
	if !strings.Contains(text, droppedEarlier) {
		t.Error("an abbreviated reasoning block does not say it was abbreviated")
	}
	if !strings.Contains(text, "EDIT-LANDED-HERE") || !strings.Contains(text, droppedLater) {
		t.Error("the tool result was not reproduced from its start, or the cut is unmarked")
	}
	if !strings.Contains(text, `{"path":"runner.go"`) {
		t.Error("the call's arguments lost the file they name")
	}
	if over := estimateTokensFromText(text) - recentContextCap; over > 0 {
		t.Errorf("the reproduction takes %d tokens over the cap", over)
	}
}

// A turn that made many calls is over the cap on its own even with every piece capped, so the oldest of
// several such turns has to go before the newest is squeezed.
func TestRenderRecentContextDropsTheOldestTurnFirst(t *testing.T) {
	t.Parallel()
	conv := []types.ChatMessage{{Role: "system", Content: "prompt"}, {Role: "user", Content: "the ask"}}
	for _, name := range []string{"ONE", "TWO", "THREE"} {
		var calls []types.ToolCallMsg
		var results []types.ChatMessage
		for i := 0; i < 12; i++ {
			id := name + string(rune('a'+i))
			calls = append(calls, tailCall(id, types.ToolBash, filler(400, "cmd"+name)))
			results = append(results, types.ChatMessage{Role: "tool", ToolCallID: id, Content: filler(400, "log"+name)})
		}
		conv = append(conv, types.ChatMessage{Role: "assistant", Content: "TURN-" + name, ToolCalls: calls})
		conv = append(conv, results...)
	}
	text := renderRecentContext(conv)

	if over := estimateTokensFromText(text) - recentContextCap; over > 0 {
		t.Errorf("the reproduction takes %d tokens over the cap", over)
	}
	if strings.Contains(text, "logONE") {
		t.Error("the oldest turn survived a cap it did not fit under")
	}
	if !strings.Contains(text, "logTHREE") {
		t.Error("the newest turn was dropped; the cap is meant to spend the oldest first")
	}
}

func TestRenderRecentContextMarksACallThatNeverAnswered(t *testing.T) {
	t.Parallel()
	text := renderRecentContext([]types.ChatMessage{
		{Role: "system", Content: "prompt"},
		{Role: "assistant", Content: "editing now", ToolCalls: []types.ToolCallMsg{tailCall("c9", types.ToolEditFile, `{"path":"x.go"}`)}},
	})
	if !strings.Contains(text, "cut off here") {
		t.Errorf("a call left waiting when the cut came is not reported:\n%s", text)
	}
}

func TestComposeContextCutNote(t *testing.T) {
	t.Parallel()
	if got := composeContextCutNote("   "); got != "" {
		t.Errorf("nothing was reproduced, yet a note was written: %q", got)
	}
	note := composeContextCutNote("Assistant: TURN-FOUR")
	for _, want := range []string{"context limit", "interrupted mid-task", "<recent_turns>", "TURN-FOUR"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note is missing %q:\n%s", want, note)
		}
	}
}
