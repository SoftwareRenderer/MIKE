package exec

import (
	"strconv"
	"strings"
	"testing"

	"mike/backend/types"
)

type trimRow struct {
	id     string
	name   string
	output string
}

// filler is text estimating about tokens, ten words to a line, with word marking one result from another.
func filler(tokens int, word string) string {
	words := int(float64(tokens) / 1.3)
	sb := strings.Builder{}
	for i := 0; i < words; i++ {
		if i > 0 && i%10 == 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(word)
		sb.WriteString(" ")
	}
	return strings.TrimSpace(sb.String())
}

func convWith(rows ...trimRow) []types.ChatMessage {
	msgs := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "read the files please"},
	}
	var calls []types.ToolCallMsg
	var tools []types.ChatMessage
	for _, r := range rows {
		calls = append(calls, types.ToolCallMsg{ID: r.id, Type: "function"})
		calls[len(calls)-1].Function.Name = r.name
		tools = append(tools, types.ChatMessage{Role: "tool", Content: r.output, ToolCallID: r.id})
	}
	msgs = append(msgs, types.ChatMessage{Role: "assistant", ToolCalls: calls})
	return append(msgs, tools...)
}

func toolContents(msgs []types.ChatMessage) []string {
	var out []string
	for _, m := range msgs {
		if m.Role == "tool" {
			out = append(out, m.Content)
		}
	}
	return out
}

func TestTrimToFitLeavesAFittingRequestAlone(t *testing.T) {
	t.Parallel()
	conv := convWith(trimRow{id: "c1", name: types.ToolReadFile, output: filler(500, "aa")})
	budget := estimatePromptTokens(conv) + 1000

	kept, reports := trimToFit(conv, budget)

	if len(reports) != 0 {
		t.Errorf("%d results trimmed, want none: %v", len(reports), toolContents(kept))
	}
	if kept[3].Content != conv[3].Content {
		t.Error("a request inside its budget was rewritten")
	}
}

func TestTrimToFitCutsTheOldestResultFirst(t *testing.T) {
	t.Parallel()
	conv := convWith(
		trimRow{id: "c1", name: types.ToolReadFile, output: filler(2000, "aa")},
		trimRow{id: "c2", name: types.ToolReadFile, output: filler(2000, "bb")},
	)
	budget := estimatePromptTokens(conv) - 1000

	kept, reports := trimToFit(conv, budget)

	if len(reports) != 1 {
		t.Fatalf("%d results trimmed, want the oldest cut once: %+v", len(reports), reports)
	}
	if !strings.Contains(kept[3].Content, "trimmed") {
		t.Errorf("the oldest result carries no notice: %q", kept[3].Content[:80])
	}
	if kept[4].Content != conv[4].Content {
		t.Error("the newest result was cut while an older one still had room to give")
	}
	if over := estimatePromptTokens(kept) - budget; over > 0 {
		t.Errorf("the trimmed request is still %d tokens over its budget", over)
	}
}

// A tool message the model never sees leaves its assistant tool_call unanswered, and the provider refuses
// the request.
func TestTrimToFitNeverDropsAToolMessage(t *testing.T) {
	t.Parallel()
	conv := convWith(
		trimRow{id: "c1", name: types.ToolReadFile, output: filler(4000, "aa")},
		trimRow{id: "c2", name: types.ToolBash, output: filler(4000, "bb")},
	)

	kept, _ := trimToFit(conv, 100)

	if len(kept) != len(conv) {
		t.Fatalf("%d messages came back, want %d", len(kept), len(conv))
	}
	calls := map[string]bool{}
	for _, m := range kept {
		for _, tc := range m.ToolCalls {
			calls[tc.ID] = true
		}
	}
	answered := map[string]bool{}
	for _, m := range kept {
		if m.Role == "tool" {
			answered[m.ToolCallID] = true
		}
	}
	for id := range calls {
		if !answered[id] {
			t.Errorf("tool_call %s has no tool message answering it", id)
		}
	}
	for i, m := range kept {
		if m.Role != "tool" && m.Content != conv[i].Content {
			t.Errorf("message %d (%s) was rewritten; only tool output is ours to cut", i, m.Role)
		}
	}
}

func TestTrimToFitKeepsTheFloor(t *testing.T) {
	t.Parallel()
	conv := convWith(trimRow{id: "c1", name: types.ToolBash, output: filler(4000, "aa")})

	kept, reports := trimToFit(conv, 50)

	body, _, found := strings.Cut(kept[3].Content, "… (trimmed")
	if !found {
		t.Fatal("no trim notice on the cut result")
	}
	// The cut ends on a line boundary, which can cost the budget's last line.
	if got := estimateTokensFromText(body); got < minKeptTokens*3/4 {
		t.Errorf("result body is %d tokens, below the floor of %d", got, minKeptTokens)
	}
	if reports[0].keptTokens <= 0 || reports[0].droppedTokens <= 0 {
		t.Errorf("a cut must report both halves of what it did: %+v", reports[0])
	}
}

// A result cut once keeps a notice that reports what the tool actually returned.
func TestTrimToFitCutsEachResultOnce(t *testing.T) {
	t.Parallel()
	conv := convWith(trimRow{id: "c1", name: types.ToolReadFile, output: filler(4000, "aa")})
	originalLines := strings.Count(conv[3].Content, "\n") + 1

	kept, _ := trimToFit(conv, 1)

	if got := strings.Count(kept[3].Content, "trimmed to "); got != 1 {
		t.Errorf("%d trim notices on one result, want one", got)
	}
	if !strings.Contains(kept[3].Content, " of "+strconv.Itoa(originalLines)+" lines") {
		t.Errorf("the notice lost the file's real line count: %q", kept[3].Content)
	}
}

func TestTrimMarkerNamesTheRoutePerTool(t *testing.T) {
	t.Parallel()
	read := trimMarker(types.ToolReadFile, "one\ntwo", strings.Repeat("x\n", 200))
	if !strings.Contains(read, "offset and limit") {
		t.Errorf("a trimmed read must say how to re-read it: %q", read)
	}
	bash := trimMarker(types.ToolBash, "out", strings.Repeat("y", 9000))
	if !strings.Contains(bash, "grep") || !strings.Contains(bash, "9000") {
		t.Errorf("a trimmed command must say how to narrow it and what was dropped: %q", bash)
	}
	other := trimMarker(types.ToolLSPHover, "res", strings.Repeat("z", 500))
	if !strings.Contains(other, "500") || strings.Contains(other, "grep") {
		t.Errorf("an unknown tool's notice must still report the cut, and not borrow another tool's route: %q", other)
	}
}

func TestTrimToFitDoesNotModifyTheConversation(t *testing.T) {
	t.Parallel()
	conv := convWith(trimRow{id: "c1", name: types.ToolReadFile, output: filler(3000, "aa")})
	before := toolContents(conv)

	trimToFit(conv, 100)

	for i, want := range toolContents(conv) {
		if want != before[i] {
			t.Errorf("tool result %d was rewritten in the conversation handed in", i)
		}
	}
}

func TestCutToTokensStaysUnderItsBudget(t *testing.T) {
	t.Parallel()
	text := filler(2000, "aa")
	for _, target := range []int{50, 512, 1000, 1999} {
		cut := cutToTokens(text, target)
		if got := estimateTokensFromText(cut); got > target {
			t.Errorf("target %d: cut estimates %d tokens", target, got)
		}
		if !strings.HasPrefix(text, cut) {
			t.Errorf("target %d: the cut is not a prefix of the result", target)
		}
	}
}

func TestCutToTokensStopsOnALineBoundary(t *testing.T) {
	t.Parallel()
	text := filler(2000, "aa")
	cut := cutToTokens(text, 1000)
	if strings.Count(text, "\n") == strings.Count(cut, "\n") {
		t.Fatal("the cut took every line, so it cut nothing")
	}
	if last := cut[strings.LastIndex(cut, "\n")+1:]; len(strings.Fields(last)) != 10 {
		t.Errorf("cut ends mid-line: %q", last)
	}
}

func TestCutToTokensCutsASingleLongLine(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("word ", 2000)
	cut := cutToTokens(text, 100)
	if cut == "" {
		t.Fatal("a result with no line breaks lost everything")
	}
	if got := estimateTokensFromText(cut); got > 100 {
		t.Errorf("cut estimates %d tokens, want at most 100", got)
	}
}

func TestCutToTokensLeavesAShortResultAlone(t *testing.T) {
	t.Parallel()
	text := filler(100, "aa")
	if got := cutToTokens(text, 500); got != text {
		t.Error("a result inside the budget was rewritten")
	}
}

// A cut that saves less than its own notice costs would make the request bigger.
func TestTrimToFitShrinksEvenATinyOverrun(t *testing.T) {
	t.Parallel()
	conv := convWith(trimRow{id: "c1", name: types.ToolBash, output: filler(2000, "aa")})
	budget := estimatePromptTokens(conv) - 1

	kept, reports := trimToFit(conv, budget)

	if len(reports) != 1 {
		t.Fatalf("%d reports, want one: %+v", len(reports), reports)
	}
	if reports[0].droppedTokens <= 0 {
		t.Errorf("the trimmed result is %d tokens, so the cut added tokens", -reports[0].droppedTokens)
	}
	if estimatePromptTokens(kept) > budget {
		t.Errorf("a one-token overrun was not fixed: %d over", estimatePromptTokens(kept)-budget)
	}
}
