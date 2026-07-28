package pi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"kanban/pkg/agent"
)

func TestAdapter_Init(t *testing.T) {
	a := NewAdapter()

	// Valid config
	cfg, _ := json.Marshal(map[string]any{
		"binary":       "pi",
		"mode":         "rpc",
		"session_name": "test-session",
		"display_name": "Test Agent",
		"env":          []string{"PI_DIR=/tmp"},
	})
	if err := a.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if a.config.Binary != "pi" {
		t.Errorf("Binary = %q, want %q", a.config.Binary, "pi")
	}
	if a.config.DisplayName != "Test Agent" {
		t.Errorf("DisplayName = %q, want %q", a.config.DisplayName, "Test Agent")
	}

	// Invalid config
	if err := a.Init(context.Background(), []byte("not json")); err == nil {
		t.Error("expected error for invalid JSON")
	}

	// Empty config uses defaults
	a2 := NewAdapter()
	if err := a2.Init(context.Background(), []byte(`{}`)); err != nil {
		t.Fatalf("Init empty: %v", err)
	}
	if a2.config.Binary != "pi" {
		t.Errorf("default Binary = %q, want %q", a2.config.Binary, "pi")
	}
	if a2.config.DisplayName != "Pi Coding Agent" {
		t.Errorf("default DisplayName = %q", a2.config.DisplayName)
	}
}

func TestAdapter_SubprocessConfig(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{"session_name":"test"}`))

	cfg := a.SubprocessConfig(context.Background())
	if cfg.Binary != "pi" {
		t.Errorf("Binary = %q, want %q", cfg.Binary, "pi")
	}
	if !strings.Contains(strings.Join(cfg.Args, " "), "--mode rpc") {
		t.Errorf("Args = %v, want --mode rpc", cfg.Args)
	}
	if !strings.Contains(strings.Join(cfg.Args, " "), "--name test") {
		t.Errorf("Args = %v, want --name test", cfg.Args)
	}
}

func TestAdapter_EncodeCommand(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	tests := []struct {
		cmd  string
		data []byte
		want string // substring to check
	}{
		{"prompt", []byte(`{"message":"hello"}`), `"type":"prompt"`},
		{"prompt", []byte(`{"message":"hello","behavior":"steer"}`), `"streamingBehavior":"steer"`},
		{"compact", nil, `"type":"compact"`},
		{"stats", nil, `"type":"get_session_stats"`},
		{"get_state", nil, `"type":"get_state"`},
		{"abort", nil, `"type":"abort"`},
	}

	for _, tt := range tests {
		result := a.EncodeCommand(context.Background(), tt.cmd, tt.data)
		resultStr := string(result)
		if !strings.Contains(resultStr, tt.want) {
			t.Errorf("EncodeCommand(%q) = %s, want to contain %q", tt.cmd, resultStr, tt.want)
		}
		// Should end with newline
		if !strings.HasSuffix(resultStr, "\n") {
			t.Errorf("EncodeCommand(%q) should end with newline", tt.cmd)
		}
	}
}

func TestAdapter_EncodeCommand_SetModel(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	// Test with provider/model format
	result := a.EncodeCommand(context.Background(), agent.CommandSetModel, []byte(`{"model_name":"anthropic/claude-sonnet-4"}`))
	resultStr := string(result)
	if !strings.Contains(resultStr, `"type":"set_model"`) {
		t.Errorf("missing set_model type: %s", resultStr)
	}
	if !strings.Contains(resultStr, `"provider":"anthropic"`) {
		t.Errorf("missing provider: %s", resultStr)
	}
	if !strings.Contains(resultStr, `"modelId":"claude-sonnet-4"`) {
		t.Errorf("missing modelId: %s", resultStr)
	}

	// Test with model name only (no provider prefix)
	a2 := NewAdapter()
	a2.Init(context.Background(), []byte(`{}`))
	result2 := a2.EncodeCommand(context.Background(), agent.CommandSetModel, []byte(`{"model_name":"claude-sonnet-4"}`))
	result2Str := string(result2)
	if !strings.Contains(result2Str, `"type":"set_model"`) {
		t.Errorf("missing set_model type: %s", result2Str)
	}
	if !strings.Contains(result2Str, `"modelId":"claude-sonnet-4"`) {
		t.Errorf("missing modelId: %s", result2Str)
	}

	// Test with explicit provider in data
	a3 := NewAdapter()
	a3.Init(context.Background(), []byte(`{}`))
	result3 := a3.EncodeCommand(context.Background(), agent.CommandSetModel, []byte(`{"model_name":"gpt-4o","provider":"openai"}`))
	result3Str := string(result3)
	if !strings.Contains(result3Str, `"provider":"openai"`) {
		t.Errorf("missing explicit provider: %s", result3Str)
	}
	if !strings.Contains(result3Str, `"modelId":"gpt-4o"`) {
		t.Errorf("missing modelId: %s", result3Str)
	}

	// Test with nil data (should produce empty set_model)
	a4 := NewAdapter()
	a4.Init(context.Background(), []byte(`{}`))
	result4 := a4.EncodeCommand(context.Background(), agent.CommandSetModel, nil)
	result4Str := string(result4)
	if !strings.Contains(result4Str, `"type":"set_model"`) {
		t.Errorf("missing set_model type with nil data: %s", result4Str)
	}
}

func TestAdapter_ParseOutput_SetModelResponse(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	// set_model response with model info
	input := `{"type":"response","command":"set_model","success":true,"data":{"model":{"provider":"anthropic","name":"claude-sonnet-4","modelId":"claude-sonnet-4"}}}
`
	events := a.ParseOutput(context.Background(), []byte(input))

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != agent.EventTypeStatus {
		t.Fatalf("Type = %q, want %q", events[0].Type, agent.EventTypeStatus)
	}
	if mn, ok := events[0].RawData["model_name"].(string); !ok || mn != "anthropic/claude-sonnet-4" {
		t.Errorf("model_name = %q, want %q", mn, "anthropic/claude-sonnet-4")
	}
	// Adapter should have stored the model name
	if a.modelName != "anthropic/claude-sonnet-4" {
		t.Errorf("adapter.modelName = %q, want %q", a.modelName, "anthropic/claude-sonnet-4")
	}
}

func TestAdapter_ParseOutput_SetModelResponse_EmptyData(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	// set_model response with no model data
	input := `{"type":"response","command":"set_model","success":true,"data":{}}
`
	events := a.ParseOutput(context.Background(), []byte(input))

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if mn, ok := events[0].RawData["model_name"].(string); !ok || mn != "" {
		t.Errorf("model_name = %q, want empty", mn)
	}
}

func TestAdapter_ParseOutput_SetModelResponse_Error(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	// set_model response with error
	input := `{"type":"response","command":"set_model","success":false,"error":"Model not found: invalid/model"}
`
	events := a.ParseOutput(context.Background(), []byte(input))

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != agent.EventTypeError {
		t.Fatalf("Type = %q, want %q", events[0].Type, agent.EventTypeError)
	}
}

func TestAdapter_ParseOutput_TextDelta(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	input := `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"hello world"}}
`
	events := a.ParseOutput(context.Background(), []byte(input))
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != agent.EventTypeToken {
		t.Errorf("Type = %q, want %q", events[0].Type, agent.EventTypeToken)
	}
	if events[0].Text != "hello world" {
		t.Errorf("Text = %q, want %q", events[0].Text, "hello world")
	}
}

func TestAdapter_ParseOutput_ThinkingDelta(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	input := `{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"let me think"}}
`
	events := a.ParseOutput(context.Background(), []byte(input))
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != agent.EventTypeReasoning {
		t.Errorf("Type = %q", events[0].Type)
	}
	if events[0].Thinking != "let me think" {
		t.Errorf("Thinking = %q", events[0].Thinking)
	}
}

func TestAdapter_ParseOutput_ToolExecution(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	// tool_execution_start with Pi tool name "read"
	input := `{"type":"tool_execution_start","toolName":"read","toolCallId":"call-1","args":{"path":"README.md"}}
`
	events := a.ParseOutput(context.Background(), []byte(input))
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != agent.EventTypeToolCall {
		t.Errorf("Type = %q, want %q", events[0].Type, agent.EventTypeToolCall)
	}
	if events[0].ToolCall.Name != "read_file" {
		t.Errorf("ToolCall.Name = %q, want read_file (mapped from read)", events[0].ToolCall.Name)
	}
	if events[0].ToolCall.ID != "call-1" {
		t.Errorf("ToolCall.ID = %q", events[0].ToolCall.ID)
	}

	// tool_execution_end
	input2 := `{"type":"tool_execution_end","toolName":"read","toolCallId":"call-1","isError":false,"result":{"content":[{"type":"text","text":"# README"}]}}
`
	events2 := a.ParseOutput(context.Background(), []byte(input2))
	if len(events2) != 1 {
		t.Fatalf("got %d events, want 1", len(events2))
	}
	if events2[0].Type != agent.EventTypeToolResult {
		t.Errorf("Type = %q", events2[0].Type)
	}
	if !events2[0].ToolResult.Success {
		t.Error("Success should be true")
	}
	if events2[0].ToolResult.Output != "# README" {
		t.Errorf("Output = %q", events2[0].ToolResult.Output)
	}
}

func TestAdapter_ParseOutput_AgentStart(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{"display_name":"Test Agent"}`))

	input := `{"type":"agent_start"}
`
	events := a.ParseOutput(context.Background(), []byte(input))
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != agent.EventTypeStatus {
		t.Errorf("Type = %q", events[0].Type)
	}
	if events[0].Status.State != "running" {
		t.Errorf("Status.State = %q", events[0].Status.State)
	}
}

func TestAdapter_ParseOutput_AgentEnd(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	input := `{"type":"agent_end","stopReason":"stop","messages":[{"role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input":100,"output":10}}]}
`
	events := a.ParseOutput(context.Background(), []byte(input))
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != agent.EventTypePersist {
		t.Errorf("Type = %q, want %q", events[0].Type, agent.EventTypePersist)
	}
	if len(events[0].PersistEntries) != 1 {
		t.Fatalf("PersistEntries len = %d, want 1", len(events[0].PersistEntries))
	}
	entry := events[0].PersistEntries[0]
	if entry.Role != "assistant" {
		t.Errorf("Role = %q", entry.Role)
	}
	if entry.Content != "done" {
		t.Errorf("Content = %q", entry.Content)
	}
	if entry.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d", entry.PromptTokens)
	}
	if entry.PredictedTokens != 10 {
		t.Errorf("PredictedTokens = %d", entry.PredictedTokens)
	}
}

func TestAdapter_ParseOutput_SessionStats(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	input := `{"type":"response","command":"get_session_stats","success":true,"data":{"cost":0.45,"tokens":{"input":1000,"output":500,"cacheRead":100,"cacheWrite":50},"contextUsage":{"tokens":2000,"contextWindow":8000,"percent":25}}}
`
	events := a.ParseOutput(context.Background(), []byte(input))
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != agent.EventTypeStats {
		t.Errorf("Type = %q, want %q", events[0].Type, agent.EventTypeStats)
	}
	if events[0].Stats.TotalInput != 1000 {
		t.Errorf("TotalInput = %d", events[0].Stats.TotalInput)
	}
	if events[0].Stats.ContextPercent != 25 {
		t.Errorf("ContextPercent = %f", events[0].Stats.ContextPercent)
	}
	if events[0].Stats.Cost != 0.45 {
		t.Errorf("Cost = %f, want 0.45", events[0].Stats.Cost)
	}
}

func TestAdapter_ParseOutput_SessionStats_NoCost(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	input := `{"type":"response","command":"get_session_stats","success":true,"data":{"tokens":{"input":500,"output":200}}}
`
	events := a.ParseOutput(context.Background(), []byte(input))
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Stats.Cost != 0 {
		t.Errorf("Cost = %f, want 0", events[0].Stats.Cost)
	}
}

func TestAdapter_ParseOutput_PartialLines(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	// Send partial line
	partial := `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"hello`
	events1 := a.ParseOutput(context.Background(), []byte(partial))
	if len(events1) != 0 {
		t.Errorf("partial line should produce 0 events, got %d", len(events1))
	}

	// Complete the line
	rest := `"}}
`
	events2 := a.ParseOutput(context.Background(), []byte(rest))
	if len(events2) != 1 {
		t.Fatalf("completed line should produce 1 event, got %d", len(events2))
	}
	if events2[0].Text != "hello" {
		t.Errorf("Text = %q, want %q", events2[0].Text, "hello")
	}
}

func TestAdapter_ParseOutput_MultiLine(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	input := `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"hello"}}
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":" world"}}
`
	events := a.ParseOutput(context.Background(), []byte(input))
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Text != "hello" {
		t.Errorf("events[0].Text = %q", events[0].Text)
	}
	if events[1].Text != " world" {
		t.Errorf("events[1].Text = %q", events[1].Text)
	}
}

func TestAdapter_HandleUIRequest(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	resp := a.HandleUIRequest(context.Background(), "req-123", "confirm_action")
	respStr := string(resp)
	if !strings.Contains(respStr, `"type":"extension_ui_response"`) {
		t.Errorf("response missing type: %s", respStr)
	}
	if !strings.Contains(respStr, `"id":"req-123"`) {
		t.Errorf("response missing id: %s", respStr)
	}
	if !strings.Contains(respStr, `"cancelled":true`) {
		t.Errorf("response should auto-cancel: %s", respStr)
	}
}

func TestAdapter_ToolNameMapping(t *testing.T) {
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
		got := mapPiToolName(tt.piName)
		if got != tt.wantKanban {
			t.Errorf("mapPiToolName(%q) = %q, want %q", tt.piName, got, tt.wantKanban)
		}
	}
}

func TestAdapter_DisplayNameMapping(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"read_file", "Read File"},
		{"bash", "Bash"},
		{"request_review", "Request Review"},
		{"unknown_tool", "Unknown_tool"},
	}

	for _, tt := range tests {
		got := getToolDisplayName(tt.name)
		if got != tt.want {
			t.Errorf("getToolDisplayName(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestAdapter_Close(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))
	a.lineBuffer = "some buffered data"
	a.Close()
	if a.lineBuffer != "" {
		t.Error("Close should clear line buffer")
	}
}

// --- Model name discovery and persistence tests ---

func TestAdapter_GetState_DiscoversModelName(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	// Pi binary returns model info via get_state response with provider + name
	input := `{"type":"response","command":"get_state","success":true,"data":{"model":{"provider":"openai","name":"gpt-4o","modelId":"gpt-4o"}}}
`
	events := a.ParseOutput(context.Background(), []byte(input))

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != agent.EventTypeStatus {
		t.Fatalf("Type = %q, want %q", events[0].Type, agent.EventTypeStatus)
	}
	if mn, ok := events[0].RawData["model_name"].(string); !ok || mn != "openai/gpt-4o" {
		t.Errorf("model_name = %q, want %q", mn, "openai/gpt-4o")
	}

	// The adapter should have stored the model name internally
	if a.modelName != "openai/gpt-4o" {
		t.Errorf("adapter.modelName = %q, want %q", a.modelName, "openai/gpt-4o")
	}
}

func TestAdapter_GetState_DiscoversModelName_NoProvider(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	// get_state response with modelId only (no provider)
	input := `{"type":"response","command":"get_state","success":true,"data":{"model":{"name":"claude-3"}}}
`
	events := a.ParseOutput(context.Background(), []byte(input))

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if mn, ok := events[0].RawData["model_name"].(string); !ok || mn != "claude-3" {
		t.Errorf("model_name = %q, want %q", mn, "claude-3")
	}
	if a.modelName != "claude-3" {
		t.Errorf("adapter.modelName = %q, want %q", a.modelName, "claude-3")
	}
}

func TestAdapter_GetState_EmptyModel(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	// get_state response with no model info
	input := `{"type":"response","command":"get_state","success":true,"data":{}}
`
	events := a.ParseOutput(context.Background(), []byte(input))

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	// model_name should be empty string in the event
	if mn, ok := events[0].RawData["model_name"].(string); !ok || mn != "" {
		t.Errorf("model_name = %q, want empty", mn)
	}
	// adapter.modelName should also be empty (will fall back to "pi")
	if a.modelName != "" {
		t.Errorf("adapter.modelName = %q, want empty", a.modelName)
	}
}

// TestAdapter_AgentEnd_PersistsDiscoveredModelName verifies that when get_state
// has already been called (model name discovered), agent_end persists that
// model name instead of hardcoding "pi".
func TestAdapter_AgentEnd_PersistsDiscoveredModelName(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	// First, discover the model via get_state
	getStateInput := `{"type":"response","command":"get_state","success":true,"data":{"model":{"provider":"openai","name":"gpt-4o"}}}
`
	a.ParseOutput(context.Background(), []byte(getStateInput))

	// Now, agent_end should persist the discovered model name
	agentEndInput := `{"type":"agent_end","stopReason":"stop","messages":[{"role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input":100,"output":10}}]}
`
	events := a.ParseOutput(context.Background(), []byte(agentEndInput))

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != agent.EventTypePersist {
		t.Fatalf("Type = %q, want %q", events[0].Type, agent.EventTypePersist)
	}
	if len(events[0].PersistEntries) != 1 {
		t.Fatalf("PersistEntries len = %d, want 1", len(events[0].PersistEntries))
	}
	entry := events[0].PersistEntries[0]
	if entry.ModelName != "openai/gpt-4o" {
		t.Errorf("PersistEntry.ModelName = %q, want %q", entry.ModelName, "openai/gpt-4o")
	}
	if entry.ProviderName != "Pi Coding Agent" {
		t.Errorf("PersistEntry.ProviderName = %q, want %q", entry.ProviderName, "Pi Coding Agent")
	}
}

// TestAdapter_AgentEnd_FallsBackToPiWhenNoModel verifies that when get_state
// hasn't been called, agent_end falls back to "pi" as the model name.
func TestAdapter_AgentEnd_FallsBackToPiWhenNoModel(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	// agent_end without prior get_state — should fall back to "pi"
	agentEndInput := `{"type":"agent_end","stopReason":"stop","messages":[{"role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input":50,"output":5}}]}
`
	events := a.ParseOutput(context.Background(), []byte(agentEndInput))

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != agent.EventTypePersist {
		t.Fatalf("Type = %q, want %q", events[0].Type, agent.EventTypePersist)
	}
	if len(events[0].PersistEntries) != 1 {
		t.Fatalf("PersistEntries len = %d, want 1", len(events[0].PersistEntries))
	}
	entry := events[0].PersistEntries[0]
	if entry.ModelName != "pi" {
		t.Errorf("PersistEntry.ModelName = %q, want %q (fallback)", entry.ModelName, "pi")
	}
}

// TestAdapter_AgentEnd_ExtractsModelFromEvent verifies that the adapter can
// extract model info from the agent_end event itself when get_state hasn't run.
func TestAdapter_AgentEnd_ExtractsModelFromEvent(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	// agent_end with a top-level model field
	agentEndInput := `{"type":"agent_end","stopReason":"stop","model":"anthropic/claude-3","messages":[{"role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input":50,"output":5}}]}
`
	events := a.ParseOutput(context.Background(), []byte(agentEndInput))

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != agent.EventTypePersist {
		t.Fatalf("Type = %q, want %q", events[0].Type, agent.EventTypePersist)
	}
	if len(events[0].PersistEntries) != 1 {
		t.Fatalf("PersistEntries len = %d, want 1", len(events[0].PersistEntries))
	}
	entry := events[0].PersistEntries[0]
	if entry.ModelName != "anthropic/claude-3" {
		t.Errorf("PersistEntry.ModelName = %q, want %q", entry.ModelName, "anthropic/claude-3")
	}
}

// TestAdapter_AgentEnd_ExtractsModelFromMessage verifies that the adapter can
// extract model info from per-message model field in agent_end.
func TestAdapter_AgentEnd_ExtractsModelFromMessage(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	// agent_end with per-message model field
	agentEndInput := `{"type":"agent_end","stopReason":"stop","messages":[{"role":"assistant","model":"bedrock/anthropic.claude-3","content":[{"type":"text","text":"done"}],"usage":{"input":50,"output":5}}]}
`
	events := a.ParseOutput(context.Background(), []byte(agentEndInput))

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != agent.EventTypePersist {
		t.Fatalf("Type = %q, want %q", events[0].Type, agent.EventTypePersist)
	}
	if len(events[0].PersistEntries) != 1 {
		t.Fatalf("PersistEntries len = %d, want 1", len(events[0].PersistEntries))
	}
	entry := events[0].PersistEntries[0]
	if entry.ModelName != "bedrock/anthropic.claude-3" {
		t.Errorf("PersistEntry.ModelName = %q, want %q", entry.ModelName, "bedrock/anthropic.claude-3")
	}
}

// TestAdapter_AgentEnd_MalformedReturnsPersistFallback verifies that when
// agent_end has corrupted data (e.g. messages is a string instead of array),
// the adapter returns a persist event with empty entries rather than nil.
// This ensures the turn completes instead of hanging the task indefinitely.
func TestAdapter_AgentEnd_MalformedReturnsPersistFallback(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	// Malformed agent_end: messages is a string instead of []map[string]any
	// This causes mapToStruct to fail → should return persist fallback event
	input := `{"type":"agent_end","stopReason":"stop","messages":"corrupted"}
`
	events := a.ParseOutput(context.Background(), []byte(input))

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (persist fallback)", len(events))
	}
	if events[0].Type != agent.EventTypePersist {
		t.Errorf("Type = %q, want %q (persist fallback)", events[0].Type, agent.EventTypePersist)
	}
	if len(events[0].PersistEntries) != 0 {
		t.Errorf("PersistEntries len = %d, want 0", len(events[0].PersistEntries))
	}
}

// TestAdapter_ParseOutput_MalformedJSONIsSkipped verifies that malformed JSON
// lines in the Pi JSONL stream are logged and skipped without crashing.
func TestAdapter_ParseOutput_MalformedJSONIsSkipped(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	input := `not valid json at all
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"hello"}}
`
	events := a.ParseOutput(context.Background(), []byte(input))

	// Only the valid line should produce an event
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (malformed line should be skipped)", len(events))
	}
	if events[0].Text != "hello" {
		t.Errorf("Text = %q, want %q", events[0].Text, "hello")
	}
}

// TestAdapter_ParseOutput_PersistedModelNameAfterParseError verifies that
// a discovered model name survives a subsequent parse error on agent_end.
func TestAdapter_ParseOutput_PersistedModelNameAfterParseError(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	// Discover model via get_state
	getStateInput := `{"type":"response","command":"get_state","success":true,"data":{"model":{"provider":"openai","name":"gpt-4o"}}}
`
	a.ParseOutput(context.Background(), []byte(getStateInput))

	// Now send corrupted agent_end — should fall back to persist event,
	// but model name should still be the discovered one
	corruptedInput := `{"type":"agent_end","messages":"corrupted"}
`
	events := a.ParseOutput(context.Background(), []byte(corruptedInput))

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != agent.EventTypePersist {
		t.Fatalf("Type = %q, want %q", events[0].Type, agent.EventTypePersist)
	}
	// With empty persist entries, modelName fallback to "pi" won't be called
	// since buildPersistEntries isn't invoked (no messages to parse)
	if len(events[0].PersistEntries) != 0 {
		t.Errorf("PersistEntries len = %d, want 0", len(events[0].PersistEntries))
	}
}

// TestAdapter_EncodeCommand_MalformedData verifies that EncodeCommand with
// malformed JSON data still returns a valid command (logging the error).
func TestAdapter_EncodeCommand_MalformedData(t *testing.T) {
	a := NewAdapter()
	a.Init(context.Background(), []byte(`{}`))

	// Invalid JSON for prompt command — should still produce a command
	// with empty message (error logged internally, not returned)
	result := a.EncodeCommand(context.Background(), agent.CommandPrompt, []byte(`{invalid`))
	resultStr := string(result)

	if !strings.Contains(resultStr, `"type":"prompt"`) {
		t.Errorf("EncodeCommand should still produce prompt command: %s", resultStr)
	}
	// Message should be empty (default)
	if !strings.Contains(resultStr, `"message":""`) {
		t.Errorf("EncodeCommand with malformed data should have empty message: %s", resultStr)
	}
	// Should still end with newline
	if !strings.HasSuffix(resultStr, "\n") {
		t.Errorf("EncodeCommand should end with newline: %s", resultStr)
	}
}

// TestAdapter_ParseOutput_NilAdapterDoesNotCrash verifies that ParseOutput
// on an adapter not initialized doesn't panic.
func TestAdapter_ParseOutput_UninitializedDoesNotCrash(t *testing.T) {
	a := NewAdapter()
	// Skip Init — lineBuffer is already "" from NewAdapter

	// ParseOutput should not panic even if not initialized
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ParseOutput panicked on uninitialized adapter: %v", r)
		}
	}()

	input := `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"hello"}}
`
	events := a.ParseOutput(context.Background(), []byte(input))
	if len(events) != 1 {
		t.Errorf("got %d events, want 1", len(events))
	}
	if events[0].Text != "hello" {
		t.Errorf("Text = %q, want %q", events[0].Text, "hello")
	}
}
