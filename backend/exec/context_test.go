package exec

import (
	"fmt"
	"strings"
	"testing"

	"mike/backend/types"
)

// --- Checkpoint Label Prompt Tests ---

func TestBuildCheckpointLabelMessages(t *testing.T) {
	t.Parallel()
	msgs := buildCheckpointLabelMessages("src/main.go | 10 ++++++++---", "User: Fix the bug\nAssistant: I'll patch it")

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("expected system role, got %s", msgs[0].Role)
	}
	if msgs[0].Content != checkpointLabelPrompt {
		t.Error("the system turn must carry the checkpoint label instruction")
	}
	if msgs[1].Role != "user" {
		t.Errorf("expected user role, got %s", msgs[1].Role)
	}
	if !strings.Contains(msgs[1].Content, "Recent changes") {
		t.Error("user message should contain 'Recent changes' context header")
	}
	if !strings.Contains(msgs[1].Content, "Git diff --stat") {
		t.Error("the label prompt was sent without its diff-stat section")
	}
	if !strings.Contains(msgs[1].Content, "src/main.go | 10 ++++++++---") {
		t.Error("the label prompt was sent without the diff stat it labels")
	}
	if !strings.Contains(msgs[1].Content, "Assistant: I'll patch it") {
		t.Error("the label prompt was sent without the turn it labels")
	}
}

// --- ExtractCommitContext Tests ---

// reviewCall builds a request_review tool call. ExtractCommitContext selects the
// assistant messages to hand to the commit generator by Function.Name, so fixtures
// must set the name — an ID alone is just some other tool call.
func reviewCall(id string) types.ToolCallMsg {
	return toolCallMsg(id, "request_review")
}

// toolCallMsg builds a tool call named name (any tool).
func toolCallMsg(id, name string) types.ToolCallMsg {
	m := types.ToolCallMsg{ID: id}
	m.Function.Name = name
	m.Function.Arguments = `{}`
	return m
}

// contentOf returns the contents of the extracted messages, for set-style assertions.
func contentOf(msgs []types.ChatMessage) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Content
	}
	return out
}

func TestExtractCommitContext_FullConversation(t *testing.T) {
	t.Parallel()
	// Typical task: system + user (task prompt) + working turns + the assistant
	// that requested the review. Only the first user message and the request_review
	// turn (with its tool results) reach the commit generator.
	messages := []types.ChatMessage{
		{Role: "system", Content: "You are a coding assistant"},
		{Role: "user", Content: "Add rate limiting to API endpoints"},
		{Role: "assistant", Content: "I'll add rate limiting"},
		{Role: "user", Content: "Also add a config option"},
		{Role: "assistant", Content: "Done", ToolCalls: []types.ToolCallMsg{toolCallMsg("call_1", "edit_file")}},
		{Role: "tool", Content: "wrote 1234 bytes", ToolCallID: "call_1"},
		// The assistant that requested the review — the one we want
		{Role: "assistant", Content: "Rate limiting is implemented. Here's a summary of changes.", ToolCalls: []types.ToolCallMsg{reviewCall("call_2")}},
		{Role: "tool", Content: "review requested", ToolCallID: "call_2"},
	}

	result := ExtractCommitContext(messages)

	// Should return: first user + request_review assistant + tool result for call_2
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d: %v", len(result), contentOf(result))
	}

	// First message should be the original user request — not the follow-up user turn
	if result[0].Role != "user" {
		t.Errorf("first message role = %q, want %q", result[0].Role, "user")
	}
	if result[0].Content != "Add rate limiting to API endpoints" {
		t.Errorf("first message content = %q, want original task prompt", result[0].Content)
	}

	// Second message should be the request_review assistant message
	if result[1].Role != "assistant" {
		t.Errorf("second message role = %q, want %q", result[1].Role, "assistant")
	}
	if result[1].Content != "Rate limiting is implemented. Here's a summary of changes." {
		t.Errorf("second message content = %q, want final assistant content", result[1].Content)
	}
	if len(result[1].ToolCalls) != 1 || result[1].ToolCalls[0].ID != "call_2" {
		t.Errorf("second message tool calls = %+v, want [call_2]", result[1].ToolCalls)
	}

	// Third message should be the tool result for that assistant's tool call
	if result[2].Role != "tool" {
		t.Errorf("third message role = %q, want %q", result[2].Role, "tool")
	}
	if result[2].Content != "review requested" {
		t.Errorf("third message content = %q, want %q", result[2].Content, "review requested")
	}

	// Working turns are not commit context: neither the plain assistant, the second
	// user message, nor a non-review tool result may ride along.
	for _, m := range result[1:] {
		if m.Content == "Also add a config option" || m.Content == "wrote 1234 bytes" || m.Content == "I'll add rate limiting" {
			t.Errorf("non-request_review turn leaked into commit context: %q", m.Content)
		}
	}
}

func TestExtractCommitContext_NoRequestReview_KeepsEveryTurn(t *testing.T) {
	t.Parallel()
	// The review path keeps only the first user message, because a summary stands for the rest. The
	// fallback has no summary, so it keeps every turn, in order.
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "Add rate limiting"},
		{Role: "assistant", Content: "I'll start", ToolCalls: []types.ToolCallMsg{toolCallMsg("c1", "edit_file")}},
		{Role: "tool", Content: "edited a.go", ToolCallID: "c1"},
		{Role: "user", Content: "Also add a config option"},
		// An assistant message with no text of its own contributes no row.
		{Role: "assistant", ToolCalls: []types.ToolCallMsg{toolCallMsg("c2", "bash")}},
		{Role: "tool", Content: "ok", ToolCallID: "c2"},
		{Role: "assistant", Content: "Both are in"},
		{Role: "advisory", Content: "an advisory"},
		{Role: "summarization", Content: "a summary of older turns"},
	}

	result := ExtractCommitContext(messages)

	want := []string{"Add rate limiting", "I'll start", "Also add a config option", "Both are in"}
	if len(result) != len(want) {
		t.Fatalf("prose = %v, want %v", contentOf(result), want)
	}
	for i, w := range want {
		if result[i].Content != w {
			t.Errorf("message %d = %q, want %q", i, result[i].Content, w)
		}
	}
}

func TestExtractCommitContext_OneReviewDisablesTheProseFallback(t *testing.T) {
	t.Parallel()
	// One review anywhere selects the review path, and the working turns around it stay out.
	messages := []types.ChatMessage{
		{Role: "user", Content: "Build feature"},
		{Role: "assistant", Content: "Working", ToolCalls: []types.ToolCallMsg{toolCallMsg("c1", "edit_file")}},
		{Role: "tool", Content: "edited a.go", ToolCallID: "c1"},
		{Role: "assistant", Content: "Feature built.", ToolCalls: []types.ToolCallMsg{reviewCallWithSummary("c2", "Built the feature")}},
		{Role: "tool", Content: "review requested", ToolCallID: "c2"},
		{Role: "assistant", Content: "Anything else?"},
	}

	result := ExtractCommitContext(messages)

	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d: %v", len(result), contentOf(result))
	}
	if result[1].Content != "Feature built." {
		t.Errorf("second = %q, want the request_review assistant", result[1].Content)
	}
	for _, m := range result {
		switch m.Content {
		case "Working", "Anything else?", "edited a.go":
			t.Errorf("a working turn rode along with a review present: %q", m.Content)
		}
	}
}

func TestExtractCommitContext_MultipleRequestReviewsUsesAll(t *testing.T) {
	t.Parallel()
	// Multiple request_review calls — should include ALL of them
	messages := []types.ChatMessage{
		{Role: "user", Content: "Build feature"},
		// First request_review (intermediate)
		{Role: "assistant", Content: "Phase 1 done", ToolCalls: []types.ToolCallMsg{
			{ID: "call_r1", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "request_review", Arguments: `{}`}},
		}},
		{Role: "tool", Content: "Task complete phase 1", ToolCallID: "call_r1"},
		// More work
		{Role: "assistant", Content: "Continuing...", ToolCalls: []types.ToolCallMsg{
			{ID: "call_2", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "edit_file", Arguments: `{"path":"b.go"}`}},
		}},
		{Role: "tool", Content: "edited b.go", ToolCallID: "call_2"},
		// Final request_review
		{Role: "assistant", Content: "All done. Final summary.", ToolCalls: []types.ToolCallMsg{
			{ID: "call_r2", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "request_review", Arguments: `{}`}},
		}},
		{Role: "tool", Content: "Task complete", ToolCallID: "call_r2"},
	}

	result := ExtractCommitContext(messages)

	// Should include: first user + ALL request_review assistants + all their tool results
	if len(result) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(result))
	}
	if result[0].Role != "user" || result[0].Content != "Build feature" {
		t.Errorf("first = %q, want user 'Build feature'", result[0].Content)
	}
	if result[1].Role != "assistant" || result[1].Content != "Phase 1 done" {
		t.Errorf("second = %q, want first request_review assistant", result[1].Content)
	}
	if result[2].Role != "assistant" || result[2].Content != "All done. Final summary." {
		t.Errorf("third = %q, want final request_review assistant", result[2].Content)
	}
	// Both tool results present, in chronological order
	var toolContents []string
	for _, m := range result {
		if m.Role == "tool" {
			toolContents = append(toolContents, m.Content)
		}
	}
	if len(toolContents) != 2 || toolContents[0] != "Task complete phase 1" || toolContents[1] != "Task complete" {
		t.Errorf("tool results = %v, want [Task complete phase 1, Task complete]", toolContents)
	}
}

// formatToolAction is how one tool call reads in the commit generator's context: a review says what it
// claimed to accomplish, and a review with nothing to say says only that it asked.
func TestFormatToolAction_RequestReviewIsLabelledByItsSummary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args string
		want string
	}{
		{
			name: "the summary becomes the label",
			args: `{"summary":"Added rate limiting middleware to all API endpoints with configurable max requests per minute"}`,
			want: "request a review: Added rate limiting middleware to all API endpoints with configurable max requests per minute",
		},
		{name: "no summary leaves the bare label", args: `{}`, want: "request a review"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatToolAction("request_review", tc.args); got != tc.want {
				t.Errorf("formatToolAction(request_review, %s) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

// reviewCallWithSummary builds a request_review call carrying a summary.
func reviewCallWithSummary(id, summary string) types.ToolCallMsg {
	tc := toolCallMsg(id, "request_review")
	tc.Function.Arguments = `{"summary":"` + summary + `"}`
	return tc
}

// editCall builds an edit_file call for a path, whose narration names the file.
func editCall(id, path string) types.ToolCallMsg {
	tc := toolCallMsg(id, "edit_file")
	tc.Function.Arguments = `{"path":"` + path + `"}`
	return tc
}

// TestCommitContextText renders the turns the commit message is written from. The exact comparison is the
// whole assertion: a review call narrated into the text, a working call swallowed by the review block, or
// a second Assistant label for one turn all change a row this table pins.
func TestCommitContextText(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name         string
		conversation []types.ChatMessage
		want         string
	}{
		{
			name: "ConsecutiveReviewsShareOneAssistantBlock",
			conversation: []types.ChatMessage{
				{Role: "system", Content: "You are a coding assistant"},
				{Role: "user", Content: "Add retry with backoff to the LLM client"},
				{Role: "assistant", Content: "I'll start with the retry loop.", ToolCalls: []types.ToolCallMsg{toolCallMsg("c1", "edit_file")}},
				{Role: "tool", Content: "edited client.go", ToolCallID: "c1"},
				{Role: "assistant", Content: "Retry is in place.", ToolCalls: []types.ToolCallMsg{reviewCallWithSummary("c2", "Added bounded retry with exponential backoff")}},
				{Role: "tool", Content: "review requested", ToolCallID: "c2"},
				{Role: "user", Content: "Make the ceiling per-provider"},
				{Role: "assistant", Content: "Now it comes from provider config.", ToolCalls: []types.ToolCallMsg{toolCallMsg("c3", "edit_file")}},
				{Role: "tool", Content: "error: no such file", ToolCallID: "c3"},
				{Role: "assistant", Content: "Per-provider ceiling is wired up.", ToolCalls: []types.ToolCallMsg{reviewCallWithSummary("c4", "Made the ceiling per-provider")}},
				{Role: "tool", Content: "review requested", ToolCallID: "c4"},
			},
			want: "User: Add retry with backoff to the LLM client\n\n" +
				"Assistant: Retry is in place.\nAdded bounded retry with exponential backoff\n\n" +
				"Per-provider ceiling is wired up.\nMade the ceiling per-provider\n\n",
		},
		{
			name: "SummaryWithoutTextOpensTheBlock",
			conversation: []types.ChatMessage{
				{Role: "user", Content: "Build feature"},
				{Role: "assistant", ToolCalls: []types.ToolCallMsg{reviewCallWithSummary("c1", "Fixed the ceiling")}},
				{Role: "tool", Content: "review requested", ToolCallID: "c1"},
			},
			want: "User: Build feature\n\nAssistant: Fixed the ceiling\n\n",
		},
		{
			name: "ReviewWithoutSummaryAddsNothing",
			conversation: []types.ChatMessage{
				{Role: "user", Content: "Build feature"},
				{Role: "assistant", ToolCalls: []types.ToolCallMsg{reviewCall("c1")}},
				{Role: "tool", Content: "review requested", ToolCallID: "c1"},
			},
			want: "User: Build feature\n\n",
		},
		{
			// A review call can share an assistant message with working calls, whose lines keep their
			// own label.
			name: "OtherToolCallKeepsItsOwnLabel",
			conversation: []types.ChatMessage{
				{Role: "user", Content: "Build feature"},
				{Role: "assistant", Content: "Ran it down.", ToolCalls: []types.ToolCallMsg{reviewCallWithSummary("c1", "Fixed the ceiling"), editCall("c2", "a.go")}},
				{Role: "tool", Content: "review requested", ToolCallID: "c1"},
				{Role: "tool", Content: "edited a.go", ToolCallID: "c2"},
			},
			want: "User: Build feature\n\n" +
				"Assistant: Ran it down.\nFixed the ceiling\n" +
				"Assistant: (I used the edit_file tool to edit `a.go` which was successful)\n\n",
		},
		{
			// The prose-only rule: formatTurns narrates any tool call left on a message, so a turn
			// with no review call must still render as the assistant's own words.
			name: "NoRequestReview_NarratesNoToolCall",
			conversation: []types.ChatMessage{
				{Role: "user", Content: "Build feature"},
				{Role: "assistant", Content: "Ran it down.", ToolCalls: []types.ToolCallMsg{editCall("c1", "a.go")}},
				{Role: "tool", Content: "edited a.go", ToolCallID: "c1"},
			},
			want: "User: Build feature\n\nAssistant: Ran it down.\n\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := commitContextText(tt.conversation); got != tt.want {
				t.Errorf("commit context =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestFormatConversationTurns_RequestReviewStaysALabelledTurn(t *testing.T) {
	t.Parallel()
	// The checkpoint-label prompt reads whole turns, unchanged by the commit context.
	messages := []types.ChatMessage{
		{Role: "assistant", Content: "Done", ToolCalls: []types.ToolCallMsg{reviewCallWithSummary("c1", "Fixed the ceiling")}},
	}

	want := "Assistant: Done\nAssistant: request a review: Fixed the ceiling\n\n"
	if got := FormatConversationTurns(messages); got != want {
		t.Errorf("turn text = %q, want %q", got, want)
	}
}

func TestExtractCommitContext_RequestReviewWithOtherToolCalls(t *testing.T) {
	t.Parallel()
	// The request_review turn also carries other tool calls — the assistant message is
	// included verbatim (all ToolCalls preserved) and all its tool results come along.
	messages := []types.ChatMessage{
		{Role: "user", Content: "Build feature"},
		{Role: "assistant", Content: "Done", ToolCalls: []types.ToolCallMsg{
			toolCallMsg("call_edit", "edit_file"),
			reviewCall("call_review"),
		}},
		{Role: "tool", Content: "edited", ToolCallID: "call_edit"},
		{Role: "tool", Content: "Task complete", ToolCallID: "call_review"},
	}

	result := ExtractCommitContext(messages)

	// Should include: first user + that assistant + both tool results
	if len(result) != 4 {
		t.Fatalf("expected 4 messages, got %d: %v", len(result), contentOf(result))
	}
	// The assistant message keeps both of its tool calls, in order
	if len(result[1].ToolCalls) != 2 || result[1].ToolCalls[0].ID != "call_edit" || result[1].ToolCalls[1].ID != "call_review" {
		t.Errorf("assistant tool calls = %+v, want [call_edit call_review]", result[1].ToolCalls)
	}
	// Tool results should be present
	toolRoles := 0
	for _, m := range result {
		if m.Role == "tool" {
			toolRoles++
		}
	}
	if toolRoles != 2 {
		t.Errorf("expected 2 tool results, got %d", toolRoles)
	}
}

// --- reviewSummaries Tests ---

// reviewRows builds the rows of a turn that asked for review: an assistant row and the tool_call
// row it owns.
func reviewRows(assistantID, callID, summary string) []types.TaskMessage {
	args := "{}"
	if summary != "" {
		args = fmt.Sprintf(`{"summary":%q}`, summary)
	}
	return []types.TaskMessage{
		{ID: assistantID, Role: "assistant", Content: "the turn's own prose"},
		{
			Role:           "tool_call",
			AssistantMsgID: assistantID,
			ToolCalls:      fmt.Sprintf(`{"tool_id":%q,"tool_name":"request_review","raw_args":%q}`, callID, args),
			ToolResult:     `{"output":"Task complete","success":true}`,
		},
	}
}

func TestReviewSummaries_EveryReviewInOrder(t *testing.T) {
	t.Parallel()
	// Two reviews changed two things: both summaries arrive, oldest first, and nothing else does.
	rows := []types.TaskMessage{{ID: "u1", Role: "user", Content: "the task prompt"}}
	rows = append(rows, reviewRows("a1", "c1", "Added the retry loop")...)
	rows = append(rows, types.TaskMessage{
		Role: "tool_call", AssistantMsgID: "a1",
		ToolCalls:  `{"tool_id":"c0","tool_name":"edit_file","raw_args":"{\"path\":\"client.go\"}"}`,
		ToolResult: `{"output":"edited client.go","success":true}`,
	})
	rows = append(rows, reviewRows("a2", "c2", "Made the ceiling per-provider")...)

	got := reviewSummaries(rows)
	want := "Added the retry loop\n\nMade the ceiling per-provider"
	if got != want {
		t.Errorf("reviewSummaries =\n%q\nwant\n%q", got, want)
	}
}

func TestReviewSummaries_NothingReviewed(t *testing.T) {
	t.Parallel()
	// Empty is what selects the caller's fallback.
	rows := []types.TaskMessage{
		{ID: "u1", Role: "user", Content: "the task prompt"},
		{ID: "a1", Role: "assistant", Content: "worked on it"},
	}
	if got := reviewSummaries(rows); got != "" {
		t.Errorf("reviewSummaries with no review = %q, want empty", got)
	}
	if got := reviewSummaries(reviewRows("a2", "c2", "")); got != "" {
		t.Errorf("reviewSummaries with a summary-less review = %q, want empty", got)
	}
}

func TestReviewSummaries_RepeatAppearsOnce(t *testing.T) {
	t.Parallel()
	rows := reviewRows("a1", "c1", "Fixed the parser")
	rows = append(rows, reviewRows("a2", "c2", "Fixed the parser")...)
	rows = append(rows, reviewRows("a3", "c3", "And the tests")...)

	if got, want := reviewSummaries(rows), "Fixed the parser\n\nAnd the tests"; got != want {
		t.Errorf("reviewSummaries = %q, want %q", got, want)
	}
}

// TestEstimatePromptTokens prices the request about to be sent. A row's ceiling is the load-bearing
// half: counting bytes the provider never receives (an attachment's base64, a Content the parts
// replaced) is the error that makes a turn look too big to send.
func TestEstimatePromptTokens(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name      string
		messages  []types.ChatMessage
		minTokens int
		maxTokens int // 0 leaves that side open
	}{
		{
			name:      "PlainContent",
			messages:  []types.ChatMessage{{Role: "user", Content: "hello world this is a test"}},
			minTokens: 5, maxTokens: 15, // 6 words * 1.3
		},
		{
			name:      "WithReasoning",
			messages:  []types.ChatMessage{{Role: "assistant", Content: "answer", ReasoningContent: "let me think about this carefully step by step"}},
			minTokens: 10, maxTokens: 20, // 1 + 9 words * 1.3
		},
		{
			name: "ContentParts",
			messages: []types.ChatMessage{{Role: "user", ContentParts: []types.ContentPart{
				{Type: "text", Text: "check this image for me please"},
				{Type: "image", ImageSource: &types.ImageSource{Type: "base64", MediaType: "image/png", Data: "abc123"}},
			}}},
			minTokens: 5, maxTokens: 15, // 6 words * 1.3, and the base64 stays uncounted
		},
		{
			name: "ContentPartsOverridesContent",
			messages: []types.ChatMessage{{Role: "user", Content: "this should be ignored", ContentParts: []types.ContentPart{
				{Type: "text", Text: "this should be counted"},
			}}},
			// Both texts are 4 words, so the ceiling is what proves Content was replaced, not appended.
			maxTokens: 10,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tokens := estimatePromptTokens(tt.messages)
			if tokens < tt.minTokens || tokens > tt.maxTokens {
				t.Errorf("estimatePromptTokens = %d, want %d..%d", tokens, tt.minTokens, tt.maxTokens)
			}
		})
	}
}
