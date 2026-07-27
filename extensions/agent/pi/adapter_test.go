package pi

import (
	"encoding/json"
	"strings"
	"testing"
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
	if err := a.Init(cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if a.config.Binary != "pi" {
		t.Errorf("Binary = %q, want %q", a.config.Binary, "pi")
	}
	if a.config.DisplayName != "Test Agent" {
		t.Errorf("DisplayName = %q, want %q", a.config.DisplayName, "Test Agent")
	}

	// Invalid config
	if err := a.Init([]byte("not json")); err == nil {
		t.Error("expected error for invalid JSON")
	}

	// Empty config uses defaults
	a2 := NewAdapter()
	if err := a2.Init([]byte(`{}`)); err != nil {
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
	a.Init([]byte(`{"session_name":"test"}`))

	cfg := a.SubprocessConfig()
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
	a.Init([]byte(`{}`))

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
		result := a.EncodeCommand(tt.cmd, tt.data)
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

func TestAdapter_ParseOutput_TextDelta(t *testing.T) {
	a := NewAdapter()
	a.Init([]byte(`{}`))

	input := `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"hello world"}}
`
	events := a.ParseOutput([]byte(input))
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != EventTypeToken {
		t.Errorf("Type = %q, want %q", events[0].Type, EventTypeToken)
	}
	if events[0].Text != "hello world" {
		t.Errorf("Text = %q, want %q", events[0].Text, "hello world")
	}
}

func TestAdapter_ParseOutput_ThinkingDelta(t *testing.T) {
	a := NewAdapter()
	a.Init([]byte(`{}`))

	input := `{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"let me think"}}
`
	events := a.ParseOutput([]byte(input))
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != EventTypeReasoning {
		t.Errorf("Type = %q", events[0].Type)
	}
	if events[0].Thinking != "let me think" {
		t.Errorf("Thinking = %q", events[0].Thinking)
	}
}

func TestAdapter_ParseOutput_ToolExecution(t *testing.T) {
	a := NewAdapter()
	a.Init([]byte(`{}`))

	// tool_execution_start with Pi tool name "read"
	input := `{"type":"tool_execution_start","toolName":"read","toolCallId":"call-1","args":{"path":"README.md"}}
`
	events := a.ParseOutput([]byte(input))
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != EventTypeToolCall {
		t.Errorf("Type = %q, want %q", events[0].Type, EventTypeToolCall)
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
	events2 := a.ParseOutput([]byte(input2))
	if len(events2) != 1 {
		t.Fatalf("got %d events, want 1", len(events2))
	}
	if events2[0].Type != EventTypeToolResult {
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
	a.Init([]byte(`{"display_name":"Test Agent"}`))

	input := `{"type":"agent_start"}
`
	events := a.ParseOutput([]byte(input))
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != EventTypeStatus {
		t.Errorf("Type = %q", events[0].Type)
	}
	if events[0].Status.State != "running" {
		t.Errorf("Status.State = %q", events[0].Status.State)
	}
}

func TestAdapter_ParseOutput_AgentEnd(t *testing.T) {
	a := NewAdapter()
	a.Init([]byte(`{}`))

	input := `{"type":"agent_end","stopReason":"stop","messages":[{"role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input":100,"output":10}}]}
`
	events := a.ParseOutput([]byte(input))
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != EventTypePersist {
		t.Errorf("Type = %q, want %q", events[0].Type, EventTypePersist)
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
	a.Init([]byte(`{}`))

	input := `{"type":"response","command":"get_session_stats","success":true,"data":{"tokens":{"input":1000,"output":500,"cacheRead":100,"cacheWrite":50},"contextUsage":{"tokens":2000,"contextWindow":8000,"percent":25}}}
`
	events := a.ParseOutput([]byte(input))
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != EventTypeStats {
		t.Errorf("Type = %q, want %q", events[0].Type, EventTypeStats)
	}
	if events[0].Stats.TotalInput != 1000 {
		t.Errorf("TotalInput = %d", events[0].Stats.TotalInput)
	}
	if events[0].Stats.ContextPercent != 25 {
		t.Errorf("ContextPercent = %f", events[0].Stats.ContextPercent)
	}
}

func TestAdapter_ParseOutput_PartialLines(t *testing.T) {
	a := NewAdapter()
	a.Init([]byte(`{}`))

	// Send partial line
	partial := `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"hello`
	events1 := a.ParseOutput([]byte(partial))
	if len(events1) != 0 {
		t.Errorf("partial line should produce 0 events, got %d", len(events1))
	}

	// Complete the line
	rest := `"}}
`
	events2 := a.ParseOutput([]byte(rest))
	if len(events2) != 1 {
		t.Fatalf("completed line should produce 1 event, got %d", len(events2))
	}
	if events2[0].Text != "hello" {
		t.Errorf("Text = %q, want %q", events2[0].Text, "hello")
	}
}

func TestAdapter_ParseOutput_MultiLine(t *testing.T) {
	a := NewAdapter()
	a.Init([]byte(`{}`))

	input := `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"hello"}}
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":" world"}}
`
	events := a.ParseOutput([]byte(input))
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
	a.Init([]byte(`{}`))

	resp := a.HandleUIRequest("req-123", "confirm_action")
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
	a.Init([]byte(`{}`))
	a.lineBuffer = "some buffered data"
	a.Close()
	if a.lineBuffer != "" {
		t.Error("Close should clear line buffer")
	}
}

// --- Model name discovery and persistence tests ---

func TestAdapter_GetState_DiscoversModelName(t *testing.T) {
	a := NewAdapter()
	a.Init([]byte(`{}`))

	// Pi binary returns model info via get_state response with provider + name
	input := `{"type":"response","command":"get_state","success":true,"data":{"model":{"provider":"openai","name":"gpt-4o","modelId":"gpt-4o"}}}
`
	events := a.ParseOutput([]byte(input))

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != EventTypeStatus {
		t.Fatalf("Type = %q, want %q", events[0].Type, EventTypeStatus)
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
	a.Init([]byte(`{}`))

	// get_state response with modelId only (no provider)
	input := `{"type":"response","command":"get_state","success":true,"data":{"model":{"name":"claude-3"}}}
`
	events := a.ParseOutput([]byte(input))

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
	a.Init([]byte(`{}`))

	// get_state response with no model info
	input := `{"type":"response","command":"get_state","success":true,"data":{}}
`
	events := a.ParseOutput([]byte(input))

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
	a.Init([]byte(`{}`))

	// First, discover the model via get_state
	getStateInput := `{"type":"response","command":"get_state","success":true,"data":{"model":{"provider":"openai","name":"gpt-4o"}}}
`
	a.ParseOutput([]byte(getStateInput))

	// Now, agent_end should persist the discovered model name
	agentEndInput := `{"type":"agent_end","stopReason":"stop","messages":[{"role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input":100,"output":10}}]}
`
	events := a.ParseOutput([]byte(agentEndInput))

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != EventTypePersist {
		t.Fatalf("Type = %q, want %q", events[0].Type, EventTypePersist)
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
	a.Init([]byte(`{}`))

	// agent_end without prior get_state — should fall back to "pi"
	agentEndInput := `{"type":"agent_end","stopReason":"stop","messages":[{"role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input":50,"output":5}}]}
`
	events := a.ParseOutput([]byte(agentEndInput))

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != EventTypePersist {
		t.Fatalf("Type = %q, want %q", events[0].Type, EventTypePersist)
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
	a.Init([]byte(`{}`))

	// agent_end with a top-level model field
	agentEndInput := `{"type":"agent_end","stopReason":"stop","model":"anthropic/claude-3","messages":[{"role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input":50,"output":5}}]}
`
	events := a.ParseOutput([]byte(agentEndInput))

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != EventTypePersist {
		t.Fatalf("Type = %q, want %q", events[0].Type, EventTypePersist)
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
	a.Init([]byte(`{}`))

	// agent_end with per-message model field
	agentEndInput := `{"type":"agent_end","stopReason":"stop","messages":[{"role":"assistant","model":"bedrock/anthropic.claude-3","content":[{"type":"text","text":"done"}],"usage":{"input":50,"output":5}}]}
`
	events := a.ParseOutput([]byte(agentEndInput))

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Type != EventTypePersist {
		t.Fatalf("Type = %q, want %q", events[0].Type, EventTypePersist)
	}
	if len(events[0].PersistEntries) != 1 {
		t.Fatalf("PersistEntries len = %d, want 1", len(events[0].PersistEntries))
	}
	entry := events[0].PersistEntries[0]
	if entry.ModelName != "bedrock/anthropic.claude-3" {
		t.Errorf("PersistEntry.ModelName = %q, want %q", entry.ModelName, "bedrock/anthropic.claude-3")
	}
}
