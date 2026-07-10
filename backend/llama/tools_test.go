package llama

import (
	"testing"

	"kanban/backend/types"
)

func TestGetPlanModeToolDefinitions(t *testing.T) {
	tools := GetPlanModeToolDefinitions()

	// Should have exactly 4 tools: read_file, bash, create_task, task_complete
	if len(tools) != 4 {
		t.Fatalf("expected 4 tools in plan mode, got %d", len(tools))
	}

	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.Function.Name] = true
	}

	// Should have read_file, bash, create_task, task_complete
	expectedTools := []string{"read_file", "bash", "create_task", "task_complete"}
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
	expectedTools := []string{"read_file", "write_file", "edit_file", "bash", "task_complete", "create_task"}
	for _, name := range expectedTools {
		if !toolNames[name] {
			t.Errorf("expected tool %q in standard mode, not found", name)
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
