package exec

import (
	"fmt"
	"strings"
	"testing"

	"mike/backend/types"
)

// --- Checkpoint Label Prompt Tests ---

func TestCheckpointLabelPrompt_Exists(t *testing.T) {
	t.Parallel()
	if checkpointLabelPrompt == "" {
		t.Error("checkpointLabelPrompt should not be empty")
	}
	if !strings.Contains(checkpointLabelPrompt, "5 words") {
		t.Error("checkpointLabelPrompt should mention 5 words limit")
	}
}

func TestCheckpointLabelPrompt_DifferentFromCommitPrompt(t *testing.T) {
	t.Parallel()
	if checkpointLabelPrompt == commitMessagePrompt {
		t.Error("checkpointLabelPrompt should differ from commitMessagePrompt")
	}
}

func TestCheckpointLabelPrompt_UseDescription(t *testing.T) {
	t.Parallel()
	// Label is only generated at fork time, not during checkpoint creation.
	// See handleThreadFork -> generateCheckpointLabelForFork -> GenerateCheckpointLabel.
	if !strings.Contains(checkpointLabelPrompt, "imperative mood") {
		t.Error("checkpointLabelPrompt should specify imperative mood")
	}
}

func TestBuildCheckpointLabelMessages(t *testing.T) {
	t.Parallel()
	msgs := buildCheckpointLabelMessages("src/main.go | 10 ++++++++---", "User: Fix the bug\nAssistant: I'll patch it")

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("expected system role, got %s", msgs[0].Role)
	}
	if msgs[1].Role != "user" {
		t.Errorf("expected user role, got %s", msgs[1].Role)
	}
	if !strings.Contains(msgs[1].Content, "Recent changes") {
		t.Error("user message should contain 'Recent changes' context header")
	}
	if !strings.Contains(msgs[1].Content, "Git diff --stat") {
		t.Error("user message should contain diff stat")
	}
}

// --- Checkpoint Context Tests ---

func TestExtractLastTurns_FromSlicedMessages(t *testing.T) {
	t.Parallel()
	// The messages since the last checkpoint, as one turn's extraction sees them.

	allMessages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "Original request"},
		{Role: "assistant", Content: "Old response"},
		{Role: "tool", Content: "old result"},
		// --- checkpoint 1, checkpointIdx = 4 ---
		{Role: "assistant", Content: "New tool call", ToolCalls: []types.ToolCallMsg{{ID: "call_2"}}},
		{Role: "tool", Content: "new result", ToolCallID: "call_2"},
	}

	checkpointIdx := 4
	sinceCheckpoint := allMessages[checkpointIdx:]
	turns := ExtractLastTurns(sinceCheckpoint, 3)

	// Should only contain messages since checkpoint
	if len(turns) != 2 {
		t.Errorf("expected 2 messages in turns, got %d", len(turns))
	}
	for _, m := range turns {
		if m.Content == "Original request" {
			t.Error("original user request should not be in turns")
		}
		if m.Content == "Old response" {
			t.Error("old assistant response should not be in turns")
		}
	}
}

func TestExtractLastTurns_NoNewMessages(t *testing.T) {
	t.Parallel()
	// Edge case: checkpointIdx == len(messages), no new messages
	allMessages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "Request"},
	}

	checkpointIdx := len(allMessages)
	sinceCheckpoint := allMessages[checkpointIdx:]
	turns := ExtractLastTurns(sinceCheckpoint, 3)

	if len(turns) != 0 {
		t.Errorf("expected 0 turns when no new messages, got %d", len(turns))
	}
}

// --- estimatePromptTokens Tests ---

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

func TestExtractCommitContext_SingleTurn_NoRequestReview(t *testing.T) {
	t.Parallel()
	// Minimal conversation with no request_review turn: the task prompt is the only
	// thing the commit generator gets — an assistant message that never requested a
	// review says nothing the diff does not already say.
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "Fix the bug"},
		{Role: "assistant", Content: "Fixed it"},
	}

	result := ExtractCommitContext(messages)

	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d: %v", len(result), contentOf(result))
	}
	if result[0].Content != "Fix the bug" {
		t.Errorf("first = %q, want %q", result[0].Content, "Fix the bug")
	}
}

func TestExtractCommitContext_NoAssistant(t *testing.T) {
	t.Parallel()
	// Only a user message, no assistant response yet
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "Do something"},
	}

	result := ExtractCommitContext(messages)

	// Should return just the user message
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	if result[0].Role != "user" {
		t.Errorf("expected user role, got %q", result[0].Role)
	}
}

func TestExtractCommitContext_Empty(t *testing.T) {
	t.Parallel()
	result := ExtractCommitContext(nil)
	if len(result) != 0 {
		t.Errorf("expected 0 messages, got %d", len(result))
	}
}

func TestExtractCommitContext_SkipsSystemMessages(t *testing.T) {
	t.Parallel()
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt 1"},
		{Role: "system", Content: "system prompt 2"},
		{Role: "user", Content: "Task"},
		{Role: "assistant", Content: "Done", ToolCalls: []types.ToolCallMsg{reviewCall("call_r")}},
		{Role: "tool", Content: "reviewed", ToolCallID: "call_r"},
	}

	result := ExtractCommitContext(messages)

	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d: %v", len(result), contentOf(result))
	}
	// Neither message should be system
	if result[0].Role == "system" || result[1].Role == "system" {
		t.Error("should not include system messages")
	}
}

func TestExtractCommitContext_MultipleUserMessages(t *testing.T) {
	t.Parallel()
	// Multiple user messages — should pick the FIRST user message, not the last
	messages := []types.ChatMessage{
		{Role: "user", Content: "Add rate limiting to API endpoints"},
		{Role: "assistant", Content: "I'll do that"},
		{Role: "user", Content: "Also add logging"},
		{Role: "assistant", Content: "Done with both"},
		{Role: "user", Content: "Great thanks"},
		{Role: "assistant", Content: "You're welcome. Here's the summary.", ToolCalls: []types.ToolCallMsg{
			{ID: "call_1", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "request_review", Arguments: `{}`}},
		}},
		{Role: "tool", Content: "ok", ToolCallID: "call_1"},
	}

	result := ExtractCommitContext(messages)

	// Should include: first user + last assistant + tool result
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	// First user message (the task prompt), not "Great thanks"
	if result[0].Content != "Add rate limiting to API endpoints" {
		t.Errorf("first = %q, want original task prompt", result[0].Content)
	}
	// Last assistant message
	if result[1].Content != "You're welcome. Here's the summary." {
		t.Errorf("second = %q, want final assistant", result[1].Content)
	}
	if result[2].Role != "tool" {
		t.Errorf("third role = %q, want tool", result[2].Role)
	}
}

func TestExtractCommitContext_IncludesRequestReviewToolResult(t *testing.T) {
	t.Parallel()
	// Last assistant has request_review tool call — include its tool result
	messages := []types.ChatMessage{
		{Role: "user", Content: "Build feature"},
		{Role: "assistant", Content: "Feature built. Summary of changes.", ToolCalls: []types.ToolCallMsg{
			{ID: "call_1", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "request_review", Arguments: `{}`}},
		}},
		{Role: "tool", Content: "Task complete", ToolCallID: "call_1"},
	}

	result := ExtractCommitContext(messages)

	// Should include: first user + last assistant + tool result for request_review
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	if result[0].Role != "user" {
		t.Errorf("first role = %q, want user", result[0].Role)
	}
	if result[1].Role != "assistant" {
		t.Errorf("second role = %q, want assistant", result[1].Role)
	}
	if result[2].Role != "tool" {
		t.Errorf("third role = %q, want tool", result[2].Role)
	}
	if result[2].Content != "Task complete" {
		t.Errorf("tool result = %q, want %q", result[2].Content, "Task complete")
	}
}

func TestExtractCommitContext_LastAssistantNoRequestReview_NoEditsBetween(t *testing.T) {
	t.Parallel()
	// Last assistant has no request_review, and no edits between — skip last assistant
	messages := []types.ChatMessage{
		{Role: "user", Content: "Build feature"},
		{Role: "assistant", Content: "Feature built. Summary of changes.", ToolCalls: []types.ToolCallMsg{
			{ID: "call_review", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "request_review", Arguments: `{"summary":"Added new feature"}`}},
		}},
		{Role: "tool", Content: "Task complete", ToolCallID: "call_review"},
		// Follow-up with no tool calls — should be skipped
		{Role: "assistant", Content: "Let me know if you need anything else"},
	}

	result := ExtractCommitContext(messages)

	// Should include: first user + request_review assistant + tool result (last assistant skipped)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	if result[0].Role != "user" {
		t.Errorf("first role = %q, want user", result[0].Role)
	}
	if result[1].Content != "Feature built. Summary of changes." {
		t.Errorf("second = %q, want request_review assistant", result[1].Content)
	}
	if result[2].Role != "tool" {
		t.Errorf("third role = %q, want tool", result[2].Role)
	}
}

func TestExtractCommitContext_LaterEditsAfterRequestReview_AreNotIncluded(t *testing.T) {
	t.Parallel()
	// Work done after the review request is not commit context: the generator sees the
	// request_review turn, not the follow-up edits.
	messages := []types.ChatMessage{
		{Role: "user", Content: "Build feature"},
		{Role: "assistant", Content: "Feature built. Summary of changes.", ToolCalls: []types.ToolCallMsg{
			{ID: "call_review", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "request_review", Arguments: `{"summary":"Added new feature"}`}},
		}},
		{Role: "tool", Content: "Task complete", ToolCallID: "call_review"},
		// Another assistant with edits after request_review
		{Role: "assistant", Content: "Making a quick fix", ToolCalls: []types.ToolCallMsg{
			{ID: "call_edit", Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "edit_file", Arguments: `{"path":"a.go"}`}},
		}},
		{Role: "tool", Content: "edited", ToolCallID: "call_edit"},
	}

	result := ExtractCommitContext(messages)

	// Should include: first user + request_review assistant + its tool result only
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d: %v", len(result), contentOf(result))
	}
	if result[0].Role != "user" {
		t.Errorf("first role = %q, want user", result[0].Role)
	}
	if result[1].Content != "Feature built. Summary of changes." {
		t.Errorf("second = %q, want request_review assistant", result[1].Content)
	}
	if result[2].Role != "tool" || result[2].Content != "Task complete" {
		t.Errorf("third = %q (%s), want the request_review tool result", result[2].Content, result[2].Role)
	}
	for _, m := range result {
		if m.Content == "Making a quick fix" || m.Content == "edited" {
			t.Errorf("post-review edit leaked into commit context: %q", m.Content)
		}
	}
}

func TestExtractCommitContext_NonReviewToolResultsAreDropped(t *testing.T) {
	t.Parallel()
	// No request_review at all: not even a successful write_file result is kept.
	// Tool results only ride along with an included assistant message.
	messages := []types.ChatMessage{
		{Role: "user", Content: "Build feature"},
		{Role: "assistant", Content: "Building...", ToolCalls: []types.ToolCallMsg{toolCallMsg("call_1", "write_file")}},
		{Role: "tool", Content: "wrote 100 bytes", ToolCallID: "call_1"},
		{Role: "assistant", Content: "Still working on it"},
	}

	result := ExtractCommitContext(messages)

	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d: %v", len(result), contentOf(result))
	}
	if result[0].Content != "Build feature" {
		t.Errorf("first = %q, want %q", result[0].Content, "Build feature")
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

func TestFormatToolAction_RequestReviewWithSummary(t *testing.T) {
	t.Parallel()
	args := `{"summary":"Added rate limiting middleware to all API endpoints with configurable max requests per minute"}`
	action := formatToolAction("request_review", args)
	expected := "request a review: Added rate limiting middleware to all API endpoints with configurable max requests per minute"
	if action != expected {
		t.Errorf("got %q, want %q", action, expected)
	}
}

func TestFormatToolAction_RequestReviewNoSummary(t *testing.T) {
	t.Parallel()
	args := `{}`
	action := formatToolAction("request_review", args)
	if action != "request a review" {
		t.Errorf("got %q, want %q", action, "request a review")
	}
}

func TestFormatToolAction_RequestReviewEmptySummary(t *testing.T) {
	t.Parallel()
	args := `{"summary":""}`
	action := formatToolAction("request_review", args)
	if action != "request a review" {
		t.Errorf("got %q, want %q", action, "request a review")
	}
}

// reviewCallWithSummary builds a request_review call carrying a summary.
func reviewCallWithSummary(id, summary string) types.ToolCallMsg {
	tc := toolCallMsg(id, "request_review")
	tc.Function.Arguments = `{"summary":"` + summary + `"}`
	return tc
}

func TestCommitContextText_ConsecutiveReviewsShareOneAssistantBlock(t *testing.T) {
	t.Parallel()
	// Both summaries must reach the generator under one Assistant label: a second label reads
	// as a second turn.
	conversation := []types.ChatMessage{
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
	}

	want := "User: Add retry with backoff to the LLM client\n\n" +
		"Assistant: Retry is in place.\nAdded bounded retry with exponential backoff\n\n" +
		"Per-provider ceiling is wired up.\nMade the ceiling per-provider\n\n"

	got := commitContextText(conversation)
	if got != want {
		t.Errorf("commit context =\n%q\nwant\n%q", got, want)
	}
	if strings.Contains(got, "request a review") {
		t.Errorf("consolidated text still narrates the review call:\n%s", got)
	}
}

func TestCommitContextText_SummaryWithoutTextOpensTheBlock(t *testing.T) {
	t.Parallel()
	conversation := []types.ChatMessage{
		{Role: "user", Content: "Build feature"},
		{Role: "assistant", ToolCalls: []types.ToolCallMsg{reviewCallWithSummary("c1", "Fixed the ceiling")}},
		{Role: "tool", Content: "review requested", ToolCallID: "c1"},
	}

	want := "User: Build feature\n\nAssistant: Fixed the ceiling\n\n"
	if got := commitContextText(conversation); got != want {
		t.Errorf("commit context = %q, want %q", got, want)
	}
}

func TestCommitContextText_ReviewWithoutSummaryAddsNothing(t *testing.T) {
	t.Parallel()
	conversation := []types.ChatMessage{
		{Role: "user", Content: "Build feature"},
		{Role: "assistant", ToolCalls: []types.ToolCallMsg{reviewCall("c1")}},
		{Role: "tool", Content: "review requested", ToolCallID: "c1"},
	}

	want := "User: Build feature\n\n"
	if got := commitContextText(conversation); got != want {
		t.Errorf("commit context = %q, want %q", got, want)
	}
}

func TestCommitContextText_OtherToolCallKeepsItsOwnLabel(t *testing.T) {
	t.Parallel()
	// A review call can share an assistant message with working calls, whose lines keep their
	// own label.
	edit := toolCallMsg("c2", "edit_file")
	edit.Function.Arguments = `{"path":"a.go"}`
	conversation := []types.ChatMessage{
		{Role: "user", Content: "Build feature"},
		{Role: "assistant", Content: "Ran it down.", ToolCalls: []types.ToolCallMsg{reviewCallWithSummary("c1", "Fixed the ceiling"), edit}},
		{Role: "tool", Content: "review requested", ToolCallID: "c1"},
		{Role: "tool", Content: "edited a.go", ToolCallID: "c2"},
	}

	want := "User: Build feature\n\n" +
		"Assistant: Ran it down.\nFixed the ceiling\n" +
		"Assistant: (I used the edit_file tool to edit `a.go` which was successful)\n\n"
	if got := commitContextText(conversation); got != want {
		t.Errorf("commit context =\n%q\nwant\n%q", got, want)
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

// --- estimatePromptTokens Tests ---

func TestEstimatePromptTokens_PlainContent(t *testing.T) {
	t.Parallel()
	messages := []types.ChatMessage{
		{Role: "user", Content: "hello world this is a test"},
	}
	tokens := estimatePromptTokens(messages)
	// "hello world this is a test" = 6 words * 1.3 = ~8
	if tokens < 5 || tokens > 15 {
		t.Errorf("expected ~8 tokens, got %d", tokens)
	}
}

func TestEstimatePromptTokens_WithReasoning(t *testing.T) {
	t.Parallel()
	messages := []types.ChatMessage{
		{Role: "assistant", Content: "answer", ReasoningContent: "let me think about this carefully step by step"},
	}
	tokens := estimatePromptTokens(messages)
	// "answer" (1 word) + "let me think about this carefully step by step" (9 words) = 10 * 1.3 = ~13
	if tokens < 10 || tokens > 20 {
		t.Errorf("expected ~13 tokens, got %d", tokens)
	}
}

func TestEstimatePromptTokens_ContentParts(t *testing.T) {
	t.Parallel()
	messages := []types.ChatMessage{
		{Role: "user", ContentParts: []types.ContentPart{
			{Type: "text", Text: "check this image for me please"},
			{Type: "image", ImageSource: &types.ImageSource{Type: "base64", MediaType: "image/png", Data: "abc123"}},
		}},
	}
	tokens := estimatePromptTokens(messages)
	// "check this image for me please" = 6 words * 1.3 = ~8
	// Content is empty, so only ContentParts text should be counted
	if tokens < 5 || tokens > 15 {
		t.Errorf("expected ~8 tokens from ContentParts text, got %d", tokens)
	}
}

func TestEstimatePromptTokens_ContentPartsOverridesContent(t *testing.T) {
	t.Parallel()
	messages := []types.ChatMessage{
		{Role: "user", Content: "this should be ignored", ContentParts: []types.ContentPart{
			{Type: "text", Text: "this should be counted"},
		}},
	}
	tokens := estimatePromptTokens(messages)
	// ContentParts takes priority — "this should be counted" = 4 words * 1.3 = ~5
	// NOT "this should be ignored" (4 words) + "this should be counted" (4 words)
	if tokens > 10 {
		t.Errorf("expected only ContentParts text (~5 tokens), got %d (Content was also counted?)", tokens)
	}
}
