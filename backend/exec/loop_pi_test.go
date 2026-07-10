package exec

import (
	"testing"
)

// --- extractToolResultText Tests ---

func TestExtractToolResultText_PlainString(t *testing.T) {
	msg := map[string]any{
		"content": "file written successfully",
	}
	got := extractToolResultText(msg)
	if got != "file written successfully" {
		t.Errorf("got %q, want %q", got, "file written successfully")
	}
}

func TestExtractToolResultText_MultiPartContent(t *testing.T) {
	msg := map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": "line one"},
			map[string]any{"type": "text", "text": "line two"},
		},
	}
	got := extractToolResultText(msg)
	if got != "line one\nline two" {
		t.Errorf("got %q, want %q", got, "line one\nline two")
	}
}

func TestExtractToolResultText_MultiPartWithNonText(t *testing.T) {
	msg := map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": "result"},
			map[string]any{"type": "image", "url": "data:..."},
			map[string]any{"type": "text", "text": " done"},
		},
	}
	got := extractToolResultText(msg)
	if got != "result\n done" {
		t.Errorf("got %q, want %q", got, "result\n done")
	}
}

func TestExtractToolResultText_EmptyContent(t *testing.T) {
	msg := map[string]any{
		"content": "",
	}
	got := extractToolResultText(msg)
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

func TestExtractToolResultText_NilContent(t *testing.T) {
	msg := map[string]any{}
	got := extractToolResultText(msg)
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

func TestExtractToolResultText_WrongType(t *testing.T) {
	msg := map[string]any{
		"content": 12345,
	}
	got := extractToolResultText(msg)
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

// --- parseAssistantMessage Tests ---

func TestParseAssistantMessage_TextOnly(t *testing.T) {
	msg := map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "text", "text": "Hello"},
			map[string]any{"type": "text", "text": " world"},
		},
	}
	text, reasoning, toolCalls, pt, ptk := parseAssistantMessage(msg)
	if text != "Hello\n world" {
		t.Errorf("text = %q, want %q", text, "Hello\n world")
	}
	if reasoning != "" {
		t.Errorf("reasoning = %q, want empty", reasoning)
	}
	if len(toolCalls) != 0 {
		t.Errorf("toolCalls = %d, want 0", len(toolCalls))
	}
	if pt != 0 || ptk != 0 {
		t.Errorf("usage = %d/%d, want 0/0", pt, ptk)
	}
}

func TestParseAssistantMessage_WithThinking(t *testing.T) {
	msg := map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "thinking", "thinking": "Let me think..."},
			map[string]any{"type": "text", "text": "Done."},
		},
	}
	text, reasoning, toolCalls, _, _ := parseAssistantMessage(msg)
	if text != "Done." {
		t.Errorf("text = %q, want %q", text, "Done.")
	}
	if reasoning != "Let me think..." {
		t.Errorf("reasoning = %q, want %q", reasoning, "Let me think...")
	}
	if len(toolCalls) != 0 {
		t.Errorf("toolCalls = %d, want 0", len(toolCalls))
	}
}

func TestParseAssistantMessage_WithToolCalls(t *testing.T) {
	msg := map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{
				"type":      "toolCall",
				"id":        "call_1",
				"name":      "read_file",
				"arguments": map[string]any{"path": "foo.txt"},
			},
			map[string]any{
				"type":      "toolCall",
				"id":        "call_2",
				"name":      "write_file",
				"arguments": map[string]any{"path": "bar.txt", "content": "hello"},
			},
		},
	}
	_, _, toolCalls, _, _ := parseAssistantMessage(msg)
	if len(toolCalls) != 2 {
		t.Fatalf("toolCalls = %d, want 2", len(toolCalls))
	}
	if toolCalls[0]["id"] != "call_1" {
		t.Errorf("first toolCall id = %v, want call_1", toolCalls[0]["id"])
	}
	if toolCalls[1]["name"] != "write_file" {
		t.Errorf("second toolCall name = %v, want write_file", toolCalls[1]["name"])
	}
}

func TestParseAssistantMessage_WithUsage(t *testing.T) {
	msg := map[string]any{
		"role":    "assistant",
		"content": []any{map[string]any{"type": "text", "text": "ok"}},
		"usage": map[string]any{
			"input":  150.0,
			"output": 45.0,
		},
	}
	_, _, _, pt, ptk := parseAssistantMessage(msg)
	if pt != 150 {
		t.Errorf("promptTokens = %d, want 150", pt)
	}
	if ptk != 45 {
		t.Errorf("predictedTokens = %d, want 45", ptk)
	}
}

func TestParseAssistantMessage_FullMessage(t *testing.T) {
	msg := map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "thinking", "thinking": "Planning approach"},
			map[string]any{"type": "text", "text": "I'll read the file"},
			map[string]any{
				"type":      "toolCall",
				"id":        "call_abc",
				"name":      "ReadFile",
				"arguments": map[string]any{"path": "src/main.go"},
			},
		},
		"usage": map[string]any{
			"input":  500.0,
			"output": 120.0,
		},
	}
	text, reasoning, toolCalls, pt, ptk := parseAssistantMessage(msg)

	if text != "I'll read the file" {
		t.Errorf("text = %q, want %q", text, "I'll read the file")
	}
	if reasoning != "Planning approach" {
		t.Errorf("reasoning = %q, want %q", reasoning, "Planning approach")
	}
	if len(toolCalls) != 1 {
		t.Fatalf("toolCalls = %d, want 1", len(toolCalls))
	}
	if toolCalls[0]["name"] != "ReadFile" {
		t.Errorf("toolCall name = %v, want ReadFile", toolCalls[0]["name"])
	}
	if pt != 500 || ptk != 120 {
		t.Errorf("usage = %d/%d, want 500/120", pt, ptk)
	}
}

func TestParseAssistantMessage_EmptyContent(t *testing.T) {
	msg := map[string]any{
		"role":    "assistant",
		"content": []any{},
	}
	text, reasoning, toolCalls, pt, ptk := parseAssistantMessage(msg)
	if text != "" {
		t.Errorf("text = %q, want empty", text)
	}
	if reasoning != "" {
		t.Errorf("reasoning = %q, want empty", reasoning)
	}
	if len(toolCalls) != 0 {
		t.Errorf("toolCalls = %d, want 0", len(toolCalls))
	}
	if pt != 0 || ptk != 0 {
		t.Errorf("usage = %d/%d, want 0/0", pt, ptk)
	}
}

func TestParseAssistantMessage_NilContent(t *testing.T) {
	msg := map[string]any{
		"role": "assistant",
	}
	text, reasoning, toolCalls, pt, ptk := parseAssistantMessage(msg)
	if text != "" || reasoning != "" || len(toolCalls) != 0 || pt != 0 || ptk != 0 {
		t.Errorf("expected all zero values, got text=%q reasoning=%q toolCalls=%d pt=%d ptk=%d",
			text, reasoning, len(toolCalls), pt, ptk)
	}
}

func TestParseAssistantMessage_MissingUsageFields(t *testing.T) {
	msg := map[string]any{
		"role":    "assistant",
		"content": []any{map[string]any{"type": "text", "text": "ok"}},
		"usage":   map[string]any{}, // empty usage
	}
	_, _, _, pt, ptk := parseAssistantMessage(msg)
	if pt != 0 || ptk != 0 {
		t.Errorf("usage = %d/%d, want 0/0 for missing fields", pt, ptk)
	}
}
