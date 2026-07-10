package pi

import (
	"encoding/json"
	"testing"

	"kanban/backend/types"
)

// TestPromptCommandFormat verifies that the Prompt command JSON includes
// the streamingBehavior field when provided, and omits it when not.
func TestPromptCommandFormat(t *testing.T) {
	tests := []struct {
		name              string
		message           string
		streamingBehavior []string
		wantFields        map[string]bool // field name -> should be present
	}{
		{
			name:    "no streaming behavior",
			message: "hello world",
			wantFields: map[string]bool{
				"type":    true,
				"message": true,
				"id":      true,
			},
		},
		{
			name:              "with steer behavior",
			message:           "follow up message",
			streamingBehavior: []string{"steer"},
			wantFields: map[string]bool{
				"type":              true,
				"message":           true,
				"id":                true,
				"streamingBehavior": true,
			},
		},
		{
			name:              "with followUp behavior",
			message:           "do this after",
			streamingBehavior: []string{"followUp"},
			wantFields: map[string]bool{
				"type":              true,
				"message":           true,
				"id":                true,
				"streamingBehavior": true,
			},
		},
		{
			name:              "empty streaming behavior omitted",
			message:           "test message",
			streamingBehavior: []string{""},
			wantFields: map[string]bool{
				"type":    true,
				"message": true,
				"id":      true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := map[string]any{
				"type":    "prompt",
				"message": tt.message,
				"id":      "test-id",
			}
			if len(tt.streamingBehavior) > 0 && tt.streamingBehavior[0] != "" {
				cmd["streamingBehavior"] = tt.streamingBehavior[0]
			}

			data, err := json.Marshal(cmd)
			if err != nil {
				t.Fatalf("marshal failed: %v", err)
			}

			var parsed map[string]any
			if err := json.Unmarshal(data, &parsed); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}

			// Check expected fields are present
			for field, wantPresent := range tt.wantFields {
				_, hasField := parsed[field]
				if hasField != wantPresent {
					t.Errorf("field %q: present=%v, want=%v", field, hasField, wantPresent)
				}
			}

			// Check no unexpected fields
			for field := range parsed {
				if _, wanted := tt.wantFields[field]; !wanted {
					t.Errorf("unexpected field %q in command", field)
				}
			}

			// Verify specific values
			if parsed["type"] != "prompt" {
				t.Errorf("type = %v, want prompt", parsed["type"])
			}
			if parsed["message"] != tt.message {
				t.Errorf("message = %v, want %v", parsed["message"], tt.message)
			}
			if len(tt.streamingBehavior) > 0 && tt.streamingBehavior[0] != "" {
				if parsed["streamingBehavior"] != tt.streamingBehavior[0] {
					t.Errorf("streamingBehavior = %v, want %v", parsed["streamingBehavior"], tt.streamingBehavior[0])
				}
			}
		})
	}
}

// TestToStreamEvent_MessageUpdate verifies that message_update events with
// different assistantMessageEvent types are handled correctly.
func TestToStreamEvent_MessageUpdate(t *testing.T) {
	tests := []struct {
		name        string
		event       PiEvent
		wantType    types.WSMessageType
		wantHasData bool
	}{
		{
			name: "text_delta produces token event",
			event: PiEvent{
				Type:               "message_update",
				AssistantEventType: "text_delta",
				TextDelta:          "hello",
			},
			wantType:    types.WSTypeToken,
			wantHasData: true,
		},
		{
			name: "thinking_delta produces reasoning event",
			event: PiEvent{
				Type:               "message_update",
				AssistantEventType: "thinking_delta",
				ThinkingDelta:      "let me think",
			},
			wantType:    types.WSTypeReasoning,
			wantHasData: true,
		},
		{
			name: "unknown assistant event type returns empty",
			event: PiEvent{
				Type:               "message_update",
				AssistantEventType: "text_start",
			},
			wantType:    "",
			wantHasData: false,
		},
		{
			name: "no assistant event type returns empty",
			event: PiEvent{
				Type: "message_update",
			},
			wantType:    "",
			wantHasData: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ToStreamEvent(tt.event, false)
			if result.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", result.Type, tt.wantType)
			}
			if tt.wantHasData && result.Data == nil {
				t.Error("expected Data to be non-nil")
			}
			if !tt.wantHasData && result.Data != nil {
				t.Error("expected Data to be nil")
			}
		})
	}
}

// TestToStreamEvent_ToolExecution verifies tool execution events are mapped correctly.
func TestToStreamEvent_ToolExecution(t *testing.T) {
	// tool_execution_start with Pi tool name "read" should map to "read_file"
	startEvent := PiEvent{
		Type:       "tool_execution_start",
		ToolName:   "read",
		ToolCallID: "call_123",
		ToolArgs:   map[string]any{"path": "README.md"},
	}
	result := ToStreamEvent(startEvent, false)
	if result.Type != types.WSTypeToolCall {
		t.Errorf("tool_execution_start Type = %q, want tool_call", result.Type)
	}
	data := result.Data.(map[string]any)
	if data["tool_name"] != "read_file" {
		t.Errorf("tool_name = %v, want read_file", data["tool_name"])
	}

	// tool_execution_end
	endEvent := PiEvent{
		Type:        "tool_execution_end",
		ToolName:    "read",
		ToolCallID:  "call_123",
		ToolResult:  "# README\nContent here",
		ToolIsError: false,
	}
	result = ToStreamEvent(endEvent, false)
	if result.Type != types.WSTypeToolResult {
		t.Errorf("tool_execution_end Type = %q, want tool_result", result.Type)
	}
}

// TestToStreamEvent_AgentEvents verifies agent_start and agent_end mapping.
func TestToStreamEvent_AgentEvents(t *testing.T) {
	// agent_start
	start := PiEvent{Type: "agent_start"}
	result := ToStreamEvent(start, false)
	if result.Type != types.WSTypeStatus {
		t.Errorf("agent_start Type = %q, want status", result.Type)
	}
	data := result.Data.(map[string]string)
	if data["state"] != "running" {
		t.Errorf("agent_start state = %q, want running", data["state"])
	}

	// agent_end with stopReason "stop"
	end := PiEvent{Type: "agent_end", StopReason: "stop"}
	result = ToStreamEvent(end, false)
	if result.Type != types.WSTypeStatus {
		t.Errorf("agent_end Type = %q, want status", result.Type)
	}
	data = result.Data.(map[string]string)
	if data["state"] != "idle" {
		t.Errorf("agent_end (stop) state = %q, want idle", data["state"])
	}

	// agent_end with stopReason "toolUse"
	endTool := PiEvent{Type: "agent_end", StopReason: "toolUse"}
	result = ToStreamEvent(endTool, false)
	data = result.Data.(map[string]string)
	if data["state"] != "running" {
		t.Errorf("agent_end (toolUse) state = %q, want running", data["state"])
	}
}

// TestMapPiToolName verifies Pi tool name to Kanban tool name mapping.
func TestMapPiToolName(t *testing.T) {
	tests := []struct {
		piName     string
		wantKanban string
	}{
		{"read", "read_file"},
		{"write", "write_file"},
		{"edit", "edit_file"},
		{"bash", "bash"},
		{"request_review", "request_review"},
		{"final_answer", "request_review"},
		{"unknown_tool", "unknown_tool"},
	}

	for _, tt := range tests {
		got := MapPiToolName(tt.piName)
		if got != tt.wantKanban {
			t.Errorf("MapPiToolName(%q) = %q, want %q", tt.piName, got, tt.wantKanban)
		}
	}
}
