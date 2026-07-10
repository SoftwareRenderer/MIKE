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
			{Type: "image", ImageSource: &struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			}{Type: "base64", MediaType: "image/png", Data: "abc123"}},
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
