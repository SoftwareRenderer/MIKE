package exec

import (
	"strings"
	"testing"

	"kanban/backend/types"
)

// --- Checkpoint Label Prompt Tests ---

func TestCheckpointLabelPrompt_Exists(t *testing.T) {
	if checkpointLabelPrompt == "" {
		t.Error("checkpointLabelPrompt should not be empty")
	}
	if !strings.Contains(checkpointLabelPrompt, "5 words") {
		t.Error("checkpointLabelPrompt should mention 5 words limit")
	}
}

func TestCheckpointLabelPrompt_DifferentFromCommitPrompt(t *testing.T) {
	if checkpointLabelPrompt == commitMessagePrompt {
		t.Error("checkpointLabelPrompt should differ from commitMessagePrompt")
	}
}

func TestCheckpointLabelPrompt_UseDescription(t *testing.T) {
	// Label is only generated at fork time, not during checkpoint creation.
	// See handleThreadFork -> generateCheckpointLabelForFork -> GenerateCheckpointLabel.
	if !strings.Contains(checkpointLabelPrompt, "imperative mood") {
		t.Error("checkpointLabelPrompt should specify imperative mood")
	}
}

func TestBuildCheckpointLabelMessages(t *testing.T) {
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

func TestExtractNewMessages_SinceStart(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "Build a feature"},
		{Role: "assistant", Content: "I'll do that", ToolCalls: []types.ToolCallMsg{{ID: "call_1"}}},
		{Role: "tool", Content: "done", ToolCallID: "call_1"},
	}

	// First checkpoint: checkpointIdx = 0, so all messages are "new"
	newMsgs := messages[0:]
	if len(newMsgs) != len(messages) {
		t.Errorf("expected %d new messages, got %d", len(messages), len(newMsgs))
	}
}

func TestExtractNewMessages_SinceCheckpoint(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "Build a feature"},
		{Role: "assistant", Content: "I'll do that", ToolCalls: []types.ToolCallMsg{{ID: "call_1"}}},
		{Role: "tool", Content: "done", ToolCallID: "call_1"},
		// --- checkpoint 1 happened here, checkpointIdx = 4 ---
		{Role: "assistant", Content: "Now reading file", ToolCalls: []types.ToolCallMsg{{ID: "call_2"}}},
		{Role: "tool", Content: "file contents", ToolCallID: "call_2"},
	}

	checkpointIdx := 4
	newMsgs := messages[checkpointIdx:]

	// Only messages since last checkpoint
	if len(newMsgs) != 2 {
		t.Errorf("expected 2 new messages since checkpoint, got %d", len(newMsgs))
	}
	if newMsgs[0].Role != "assistant" {
		t.Errorf("expected first new msg to be assistant, got %s", newMsgs[0].Role)
	}
	if newMsgs[1].Role != "tool" {
		t.Errorf("expected second new msg to be tool, got %s", newMsgs[1].Role)
	}
	// System prompt and original user message should NOT be included
	for _, m := range newMsgs {
		if m.Role == "system" {
			t.Error("system prompt should not be in new messages")
		}
		if m.Content == "Build a feature" {
			t.Error("original user message should not be in new messages")
		}
	}
}

func TestExtractLastTurns_FromSlicedMessages(t *testing.T) {
	// Simulates what createCheckpoint does after slicing:
	// messages[checkpointIdx:] -> ExtractLastTurns -> FormatConversationTurns

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

func TestExtractCommitContext_FullConversation(t *testing.T) {
	// Typical task: system + user (task prompt) + many turns + final assistant with tool calls
	messages := []types.ChatMessage{
		{Role: "system", Content: "You are a coding assistant"},
		{Role: "user", Content: "Add rate limiting to API endpoints"},
		{Role: "assistant", Content: "I'll add rate limiting"},
		{Role: "user", Content: "Also add a config option"},
		{Role: "assistant", Content: "Done", ToolCalls: []types.ToolCallMsg{
			{ID: "call_1"},
		}},
		{Role: "tool", Content: "wrote 1234 bytes", ToolCallID: "call_1"},
		// Final assistant message — the one we want
		{Role: "assistant", Content: "Rate limiting is implemented. Here's a summary of changes.", ToolCalls: []types.ToolCallMsg{
			{ID: "call_2"},
		}},
		{Role: "tool", Content: "review requested", ToolCallID: "call_2"},
	}

	result := ExtractCommitContext(messages)

	// Should return: first user + last assistant + tool result for call_2
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}

	// First message should be the original user request
	if result[0].Role != "user" {
		t.Errorf("first message role = %q, want %q", result[0].Role, "user")
	}
	if result[0].Content != "Add rate limiting to API endpoints" {
		t.Errorf("first message content = %q, want original task prompt", result[0].Content)
	}

	// Second message should be the final assistant message
	if result[1].Role != "assistant" {
		t.Errorf("second message role = %q, want %q", result[1].Role, "assistant")
	}
	if result[1].Content != "Rate limiting is implemented. Here's a summary of changes." {
		t.Errorf("second message content = %q, want final assistant content", result[1].Content)
	}
	if len(result[1].ToolCalls) != 1 {
		t.Errorf("second message tool calls = %d, want 1", len(result[1].ToolCalls))
	}

	// Third message should be the tool result for the final assistant's tool call
	if result[2].Role != "tool" {
		t.Errorf("third message role = %q, want %q", result[2].Role, "tool")
	}
	if result[2].Content != "review requested" {
		t.Errorf("third message content = %q, want %q", result[2].Content, "review requested")
	}
}

func TestExtractCommitContext_SingleTurn(t *testing.T) {
	// Minimal conversation: just one user + one assistant
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "Fix the bug"},
		{Role: "assistant", Content: "Fixed it"},
	}

	result := ExtractCommitContext(messages)

	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if result[0].Content != "Fix the bug" {
		t.Errorf("first = %q, want %q", result[0].Content, "Fix the bug")
	}
	if result[1].Content != "Fixed it" {
		t.Errorf("second = %q, want %q", result[1].Content, "Fixed it")
	}
}

func TestExtractCommitContext_NoAssistant(t *testing.T) {
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
	result := ExtractCommitContext(nil)
	if len(result) != 0 {
		t.Errorf("expected 0 messages, got %d", len(result))
	}
}

func TestExtractCommitContext_SkipsSystemMessages(t *testing.T) {
	messages := []types.ChatMessage{
		{Role: "system", Content: "system prompt 1"},
		{Role: "system", Content: "system prompt 2"},
		{Role: "user", Content: "Task"},
		{Role: "assistant", Content: "Done"},
	}

	result := ExtractCommitContext(messages)

	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	// Neither message should be system
	if result[0].Role == "system" || result[1].Role == "system" {
		t.Error("should not include system messages")
	}
}

func TestExtractCommitContext_PreservesAssistantToolCalls(t *testing.T) {
	// The final assistant message has tool calls that should be preserved
	messages := []types.ChatMessage{
		{Role: "user", Content: "Build feature"},
		{Role: "assistant", Content: "Building..."},
		{Role: "assistant", Content: "All done", ToolCalls: []types.ToolCallMsg{
			{ID: "call_a"},
			{ID: "call_b"},
		}},
		{Role: "tool", Content: "ok", ToolCallID: "call_a"},
		{Role: "tool", Content: "reviewed", ToolCallID: "call_b"},
	}

	result := ExtractCommitContext(messages)

	// Should include: first user + last assistant + 2 tool results
	if len(result) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(result))
	}
	// The assistant message should have its tool calls
	if len(result[1].ToolCalls) != 2 {
		t.Errorf("expected 2 tool calls, got %d", len(result[1].ToolCalls))
	}
	// Tool results should be present
	toolCount := 0
	for _, m := range result {
		if m.Role == "tool" {
			toolCount++
		}
	}
	if toolCount != 2 {
		t.Errorf("expected 2 tool results, got %d", toolCount)
	}
}

func TestExtractCommitContext_MultipleUserMessages(t *testing.T) {
	// Multiple user messages — should pick the FIRST user message, not the last
	messages := []types.ChatMessage{
		{Role: "user", Content: "Add rate limiting to API endpoints"},
		{Role: "assistant", Content: "I'll do that"},
		{Role: "user", Content: "Also add logging"},
		{Role: "assistant", Content: "Done with both"},
		{Role: "user", Content: "Great thanks"},
		{Role: "assistant", Content: "You're welcome. Here's the summary.", ToolCalls: []types.ToolCallMsg{
			{ID: "call_1", Function: struct{ Name string `json:"name"`; Arguments string `json:"arguments"` }{Name: "request_review", Arguments: `{}`}},
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
	// Tool result
	if result[2].Role != "tool" {
		t.Errorf("third role = %q, want tool", result[2].Role)
	}
}

func TestExtractCommitContext_IncludesRequestReviewToolResult(t *testing.T) {
	// Last assistant has request_review tool call — include its tool result
	messages := []types.ChatMessage{
		{Role: "user", Content: "Build feature"},
		{Role: "assistant", Content: "Feature built. Summary of changes.", ToolCalls: []types.ToolCallMsg{
			{ID: "call_1", Function: struct{ Name string `json:"name"`; Arguments string `json:"arguments"` }{Name: "request_review", Arguments: `{}`}},
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
	// Last assistant has no request_review, and no edits between — skip last assistant
	messages := []types.ChatMessage{
		{Role: "user", Content: "Build feature"},
		{Role: "assistant", Content: "Feature built. Summary of changes.", ToolCalls: []types.ToolCallMsg{
			{ID: "call_review", Function: struct{ Name string `json:"name"`; Arguments string `json:"arguments"` }{Name: "request_review", Arguments: `{"summary":"Added new feature"}`}},
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

func TestExtractCommitContext_LastAssistantNoRequestReview_HasEditsBetween(t *testing.T) {
	// Last assistant has no request_review, but there were edits between — include last assistant
	messages := []types.ChatMessage{
		{Role: "user", Content: "Build feature"},
		{Role: "assistant", Content: "Feature built. Summary of changes.", ToolCalls: []types.ToolCallMsg{
			{ID: "call_review", Function: struct{ Name string `json:"name"`; Arguments string `json:"arguments"` }{Name: "request_review", Arguments: `{"summary":"Added new feature"}`}},
		}},
		{Role: "tool", Content: "Task complete", ToolCallID: "call_review"},
		// Another assistant with edits after request_review
		{Role: "assistant", Content: "Making a quick fix", ToolCalls: []types.ToolCallMsg{
			{ID: "call_edit", Function: struct{ Name string `json:"name"`; Arguments string `json:"arguments"` }{Name: "edit_file", Arguments: `{"path":"a.go"}`}},
		}},
		{Role: "tool", Content: "edited", ToolCallID: "call_edit"},
	}

	result := ExtractCommitContext(messages)

	// Should include: first user + request_review assistant + last assistant + tool results
	if len(result) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(result))
	}
	if result[0].Role != "user" {
		t.Errorf("first role = %q, want user", result[0].Role)
	}
	if result[1].Content != "Feature built. Summary of changes." {
		t.Errorf("second = %q, want request_review assistant", result[1].Content)
	}
	if result[2].Content != "Making a quick fix" {
		t.Errorf("third = %q, want last assistant", result[2].Content)
	}
}

func TestExtractCommitContext_LastAssistantNoRequestReviewNoneAtAll(t *testing.T) {
	// No request_review at all — just first user + last assistant
	messages := []types.ChatMessage{
		{Role: "user", Content: "Build feature"},
		{Role: "assistant", Content: "Building...", ToolCalls: []types.ToolCallMsg{
			{ID: "call_1", Function: struct{ Name string `json:"name"`; Arguments string `json:"arguments"` }{Name: "write_file", Arguments: `{"path":"a.go"}`}},
		}},
		{Role: "tool", Content: "wrote 100 bytes", ToolCallID: "call_1"},
		{Role: "assistant", Content: "Still working on it"},
	}

	result := ExtractCommitContext(messages)

	// No request_review — just first user + last assistant
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if result[0].Content != "Build feature" {
		t.Errorf("first = %q, want %q", result[0].Content, "Build feature")
	}
	if result[1].Content != "Still working on it" {
		t.Errorf("second = %q, want %q", result[1].Content, "Still working on it")
	}
}

func TestExtractCommitContext_MultipleRequestReviewsUsesAll(t *testing.T) {
	// Multiple request_review calls — should include ALL of them
	messages := []types.ChatMessage{
		{Role: "user", Content: "Build feature"},
		// First request_review (intermediate)
		{Role: "assistant", Content: "Phase 1 done", ToolCalls: []types.ToolCallMsg{
			{ID: "call_r1", Function: struct{ Name string `json:"name"`; Arguments string `json:"arguments"` }{Name: "request_review", Arguments: `{}`}},
		}},
		{Role: "tool", Content: "Task complete phase 1", ToolCallID: "call_r1"},
		// More work
		{Role: "assistant", Content: "Continuing...", ToolCalls: []types.ToolCallMsg{
			{ID: "call_2", Function: struct{ Name string `json:"name"`; Arguments string `json:"arguments"` }{Name: "edit_file", Arguments: `{"path":"b.go"}`}},
		}},
		{Role: "tool", Content: "edited b.go", ToolCallID: "call_2"},
		// Final request_review
		{Role: "assistant", Content: "All done. Final summary.", ToolCalls: []types.ToolCallMsg{
			{ID: "call_r2", Function: struct{ Name string `json:"name"`; Arguments string `json:"arguments"` }{Name: "request_review", Arguments: `{}`}},
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
	args := `{"summary":"Added rate limiting middleware to all API endpoints with configurable max requests per minute"}`
	action := formatToolAction("request_review", args)
	expected := "request a review: Added rate limiting middleware to all API endpoints with configurable max requests per minute"
	if action != expected {
		t.Errorf("got %q, want %q", action, expected)
	}
}

func TestFormatToolAction_RequestReviewNoSummary(t *testing.T) {
	args := `{}`
	action := formatToolAction("request_review", args)
	if action != "request a review" {
		t.Errorf("got %q, want %q", action, "request a review")
	}
}

func TestFormatToolAction_RequestReviewEmptySummary(t *testing.T) {
	args := `{"summary":""}`
	action := formatToolAction("request_review", args)
	if action != "request a review" {
		t.Errorf("got %q, want %q", action, "request a review")
	}
}

func TestExtractCommitContext_RequestReviewWithOtherToolCalls(t *testing.T) {
	// Last assistant has request_review + other tool calls — include all tool results
	messages := []types.ChatMessage{
		{Role: "user", Content: "Build feature"},
		{Role: "assistant", Content: "Done", ToolCalls: []types.ToolCallMsg{
			{ID: "call_edit", Function: struct{ Name string `json:"name"`; Arguments string `json:"arguments"` }{Name: "edit_file", Arguments: `{"path":"a.go"}`}},
			{ID: "call_review", Function: struct{ Name string `json:"name"`; Arguments string `json:"arguments"` }{Name: "request_review", Arguments: `{}`}},
		}},
		{Role: "tool", Content: "edited", ToolCallID: "call_edit"},
		{Role: "tool", Content: "Task complete", ToolCallID: "call_review"},
	}

	result := ExtractCommitContext(messages)

	// Should include: first user + last assistant + both tool results
	if len(result) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(result))
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

// --- estimatePromptTokens Tests ---

func TestEstimatePromptTokens_PlainContent(t *testing.T) {
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
