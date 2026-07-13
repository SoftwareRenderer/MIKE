package llama

import (
	"testing"

	"kanban/backend/types"
)

func TestGetPlanModeToolDefinitions(t *testing.T) {
	tools := GetPlanModeToolDefinitions()

	// Should have exactly 7 tools: read_file, bash, create_task, view_tasks, edit_task, ask_question, request_review
	if len(tools) != 7 {
		t.Fatalf("expected 7 tools in plan mode, got %d", len(tools))
	}

	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.Function.Name] = true
	}

	// Should have read_file, bash, create_task, view_tasks, edit_task, ask_question, request_review
	expectedTools := []string{"read_file", "bash", "create_task", "view_tasks", "edit_task", "ask_question", "request_review"}
	for _, name := range expectedTools {
		if !toolNames[name] {
			t.Errorf("expected tool %q in plan mode, not found", name)
		}
	}

	// Should NOT have write_file, edit_file
	forbiddenTools := []string{"write_file", "edit_file"}
	for _, name := range forbiddenTools {
		if toolNames[name] {
			t.Errorf("tool %q should NOT be available in plan mode", name)
		}
	}
}

func TestGetToolDefinitions_Standard(t *testing.T) {
	tools := GetToolDefinitions()

	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.Function.Name] = true
	}

	// Standard mode should have all tools
	expectedTools := []string{"read_file", "write_file", "edit_file", "bash", "ask_question", "request_review"}
	for _, name := range expectedTools {
		if !toolNames[name] {
			t.Errorf("expected tool %q in standard mode, not found", name)
		}
	}
}

func TestGetIdeationModeToolDefinitions(t *testing.T) {
	tools := GetIdeationModeToolDefinitions()

	// Should have 9 tools: standard (6) + task management (3)
	if len(tools) != 9 {
		t.Fatalf("expected 9 tools in ideation mode, got %d", len(tools))
	}

	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.Function.Name] = true
	}

	// Should have all standard tools plus task management tools
	expectedTools := []string{"read_file", "write_file", "edit_file", "bash", "ask_question", "request_review", "create_task", "view_tasks", "edit_task"}
	for _, name := range expectedTools {
		if !toolNames[name] {
			t.Errorf("expected tool %q in ideation mode, not found", name)
		}
	}
}

func TestToAPIMessage_ReasoningContentSeparate(t *testing.T) {
	// When ReasoningContent is set, it should be sent as a separate
	// reasoning_content field — NOT concatenated into content.
	msg := types.ChatMessage{
		Role:             "assistant",
		Content:          "Here is the answer",
		ReasoningContent: "Let me think step by step...",
	}

	result := toAPIMessage(msg)

	if result["role"] != "assistant" {
		t.Errorf("expected role='assistant', got %v", result["role"])
	}
	if result["content"] != "Here is the answer" {
		t.Errorf("expected content='Here is the answer', got %v", result["content"])
	}
	if result["reasoning_content"] != "Let me think step by step..." {
		t.Errorf("expected reasoning_content='Let me think step by step...', got %v", result["reasoning_content"])
	}
}

func TestToAPIMessage_ReasoningContentOnly(t *testing.T) {
	// Reasoning-only message (empty content, reasoning present)
	msg := types.ChatMessage{
		Role:             "assistant",
		Content:          "",
		ReasoningContent: "I need to think about this",
	}

	result := toAPIMessage(msg)

	if result["content"] != "" {
		t.Errorf("expected empty content, got %v", result["content"])
	}
	if result["reasoning_content"] != "I need to think about this" {
		t.Errorf("expected reasoning_content, got %v", result["reasoning_content"])
	}
}

func TestToAPIMessage_NoReasoning(t *testing.T) {
	// Message without reasoning should not have reasoning_content key
	msg := types.ChatMessage{
		Role:    "assistant",
		Content: "Simple response",
	}

	result := toAPIMessage(msg)

	if _, ok := result["reasoning_content"]; ok {
		t.Error("expected no reasoning_content key when reasoning is empty")
	}
}

func TestToAPIMessage_ToolCallWithReasoning(t *testing.T) {
	// Assistant message with tool calls and reasoning
	msg := types.ChatMessage{
		Role:             "assistant",
		Content:          "Let me check",
		ReasoningContent: "I should read the file first",
		ToolCalls: []types.ToolCallMsg{
			{ID: "call_1", Type: "function"},
		},
	}
	msg.ToolCalls[0].Function.Name = "read_file"
	msg.ToolCalls[0].Function.Arguments = `{"path":"foo.txt"}`

	result := toAPIMessage(msg)

	if result["reasoning_content"] != "I should read the file first" {
		t.Errorf("expected reasoning_content with tool call, got %v", result["reasoning_content"])
	}
	if result["content"] != "Let me check" {
		t.Errorf("expected content='Let me check', got %v", result["content"])
	}
	if result["tool_calls"] == nil {
		t.Error("expected tool_calls to be present")
	}
}

func TestToAPIMessage_ReasoningOnlyOnAssistantRole(t *testing.T) {
	// reasoning_content should only be sent for assistant role per OpenAI spec
	// Even if ReasoningContent is set on other roles, it should be suppressed.

	// User message with ReasoningContent — should NOT have reasoning_content in output
	userMsg := types.ChatMessage{
		Role:             "user",
		Content:          "hello",
		ReasoningContent: "should be ignored",
	}
	result := toAPIMessage(userMsg)
	if _, ok := result["reasoning_content"]; ok {
		t.Error("user message should NOT have reasoning_content field")
	}

	// Tool message with ReasoningContent — should NOT have reasoning_content in output
	toolMsg := types.ChatMessage{
		Role:             "tool",
		Content:          "file contents",
		ToolCallID:       "call_1",
		ReasoningContent: "should be ignored",
	}
	result = toAPIMessage(toolMsg)
	if _, ok := result["reasoning_content"]; ok {
		t.Error("tool message should NOT have reasoning_content field")
	}

	// Assistant message — SHOULD have reasoning_content
	assistantMsg := types.ChatMessage{
		Role:             "assistant",
		Content:          "answer",
		ReasoningContent: "thinking...",
	}
	result = toAPIMessage(assistantMsg)
	if result["reasoning_content"] != "thinking..." {
		t.Errorf("assistant message should have reasoning_content, got %v", result["reasoning_content"])
	}
}
