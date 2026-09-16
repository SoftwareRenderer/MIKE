package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mike/backend/extensions"
	"mike/pkg/agent"
)

// withProjectRoot changes the working directory to the project root so
// that resolveWasmPath can find extensions/agent/pi/pi.wasm. It restores
// the original directory on test cleanup.
func withProjectRoot(t *testing.T) {
	t.Helper()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	projectRoot := filepath.Join("..", "..")
	if err := os.Chdir(projectRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(oldWd) })
}

// loadPiWasm loads the pi.wasm module for integration tests. It changes
// to the project root directory so resolveWasmPath can find the wasm file.
// The adapter is automatically closed on test cleanup.
func loadPiWasm(t *testing.T) agent.AgentAdapter {
	t.Helper()
	withProjectRoot(t)
	ext := &extensions.Extension{
		Name: "pi",
		Type: extensions.ExtensionTypeAgent,
		Manifest: extensions.ExtensionManifest{
			Version: "1.0.0",
			Capabilities: &extensions.Capabilities{
				Agent: &extensions.AgentConfig{
					WasmModule: "pi.wasm",
				},
			},
		},
	}
	adapter, err := loadWasmAdapter(ext)
	if err != nil {
		t.Skipf("cannot load pi.wasm (run build.sh first): %v", err)
	}
	t.Cleanup(func() { adapter.Close() })
	return adapter
}

// TestWasmPiAdapter_Integration loads the actual pi.wasm module via
// loadWasmAdapter and verifies all exported functions work end-to-end.
// This exercises the wazero allocation pattern (Alloc → Write → Call → Free)
// and the store_result callback (WASM → host) as described in the
// wazero allocation/tinygo/greet.go guide.
func TestWasmPiAdapter_Integration(t *testing.T) {
	adapter := loadPiWasm(t)

	// --- Init ---
	cfg := RunnerConfig{
		ExtensionName: "pi",
		SessionName:   "test-session",
		WorktreePath:  t.TempDir(),
		Sandbox:       false,
		Config:        map[string]any{},
	}
	cfgBytes, _ := json.Marshal(cfg)

	if err := adapter.Init(context.Background(), cfgBytes); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// --- SubprocessConfig ---
	spCfg := adapter.SubprocessConfig(context.Background())
	if spCfg.Binary != "pi" {
		t.Errorf("Binary = %q, want %q", spCfg.Binary, "pi")
	}
	hasMode := false
	for _, arg := range spCfg.Args {
		if arg == "--mode" {
			hasMode = true
		}
	}
	if !hasMode {
		t.Errorf("expected --mode in args, got %v", spCfg.Args)
	}

	// --- EncodeCommand: compact ---
	compactCmd := adapter.EncodeCommand(context.Background(), agent.CommandCompact, nil)
	if len(compactCmd) == 0 {
		t.Fatal("EncodeCommand(compact) returned empty")
	}
	if !strings.Contains(string(compactCmd), `"type":"compact"`) {
		t.Errorf("compact: expected type compact, got %s", string(compactCmd))
	}

	// --- EncodeCommand: prompt with behavior ---
	promptData, _ := json.Marshal(map[string]any{"message": "hello", "behavior": "steer"})
	promptCmd := adapter.EncodeCommand(context.Background(), agent.CommandPrompt, promptData)
	if !strings.Contains(string(promptCmd), `"type":"prompt"`) {
		t.Errorf("prompt: expected type prompt, got %s", string(promptCmd))
	}
	if !strings.Contains(string(promptCmd), `"message":"hello"`) {
		t.Errorf("prompt: expected message, got %s", string(promptCmd))
	}
	if !strings.Contains(string(promptCmd), `"streamingBehavior":"steer"`) {
		t.Errorf("prompt: expected streamingBehavior, got %s", string(promptCmd))
	}

	// --- EncodeCommand: get_state ---
	stateCmd := adapter.EncodeCommand(context.Background(), agent.CommandGetState, nil)
	if !strings.Contains(string(stateCmd), `"type":"get_state"`) {
		t.Errorf("get_state: expected type, got %s", string(stateCmd))
	}

	// --- ParseOutput: text delta ---
	textInput := []byte(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"hello world"}}
`)
	events := adapter.ParseOutput(context.Background(), textInput)
	if len(events) != 1 {
		t.Fatalf("text delta: got %d events, want 1", len(events))
	}
	if events[0].Type != agent.EventTypeToken || events[0].Text != "hello world" {
		t.Errorf("text delta: unexpected event %+v", events[0])
	}

	// --- ParseOutput: thinking delta ---
	thinkInput := []byte(`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"let me think"}}
`)
	events = adapter.ParseOutput(context.Background(), thinkInput)
	if len(events) != 1 || events[0].Type != agent.EventTypeReasoning {
		t.Fatalf("thinking delta: unexpected events %+v", events)
	}
	if events[0].Thinking != "let me think" {
		t.Errorf("thinking delta: text = %q", events[0].Thinking)
	}

	// --- ParseOutput: tool execution start + end ---
	toolInput := []byte(`{"type":"tool_execution_start","toolName":"read","toolCallId":"call-1","args":{"path":"test.go"}}
{"type":"tool_execution_end","toolName":"read","toolCallId":"call-1","isError":false,"result":{"content":[{"type":"text","text":"# test.go"}]}}
`)
	events = adapter.ParseOutput(context.Background(), toolInput)
	if len(events) != 2 {
		t.Fatalf("tool execution: got %d events, want 2", len(events))
	}
	if events[0].Type != agent.EventTypeToolCall {
		t.Errorf("tool start: expected tool_call, got %s", events[0].Type)
	}
	if events[0].ToolCall.Name != "read_file" {
		t.Errorf("tool start: name = %q, want read_file", events[0].ToolCall.Name)
	}
	if events[0].ToolCall.ID != "call-1" {
		t.Errorf("tool start: id = %q", events[0].ToolCall.ID)
	}
	if events[1].Type != agent.EventTypeToolResult {
		t.Errorf("tool end: expected tool_result, got %s", events[1].Type)
	}
	if events[1].ToolResult.Output != "# test.go" {
		t.Errorf("tool end: output = %q", events[1].ToolResult.Output)
	}
	if !events[1].ToolResult.Success {
		t.Error("tool end: expected success=true")
	}

	// --- ParseOutput: agent_end with persistence ---
	agentEndInput := []byte(`{"type":"agent_end","stopReason":"stop","messages":[{"role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input":100,"output":10}}]}
`)
	events = adapter.ParseOutput(context.Background(), agentEndInput)
	if len(events) != 1 || events[0].Type != agent.EventTypePersist {
		t.Fatalf("agent_end: unexpected events %+v", events)
	}
	if len(events[0].PersistEntries) != 1 {
		t.Fatalf("persist entries: got %d, want 1", len(events[0].PersistEntries))
	}
	entry := events[0].PersistEntries[0]
	if entry.Role != "assistant" || entry.Content != "done" {
		t.Errorf("persist entry: role=%q content=%q", entry.Role, entry.Content)
	}
	if entry.PromptTokens != 100 || entry.PredictedTokens != 10 {
		t.Errorf("persist entry: pt=%d ptok=%d", entry.PromptTokens, entry.PredictedTokens)
	}
	if entry.ModelName != "pi" || entry.ProviderName != "Pi Coding Agent" {
		t.Errorf("persist entry: model=%q provider=%q", entry.ModelName, entry.ProviderName)
	}

	// --- ParseOutput: partial line buffering ---
	partial := []byte(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"hel`)
	events = adapter.ParseOutput(context.Background(), partial)
	if len(events) != 0 {
		t.Errorf("partial line: expected 0 events, got %d", len(events))
	}
	rest := []byte(`lo"}}
`)
	events = adapter.ParseOutput(context.Background(), rest)
	if len(events) != 1 {
		t.Fatalf("completed line: expected 1 event, got %d", len(events))
	}
	if events[0].Text != "hello" {
		t.Errorf("completed line: text = %q, want %q", events[0].Text, "hello")
	}

	// --- ParseOutput: multi-line (two text deltas in one chunk) ---
	multiInput := []byte(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"hello"}}
{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":" world"}}
`)
	events = adapter.ParseOutput(context.Background(), multiInput)
	if len(events) != 2 {
		t.Fatalf("multi-line: got %d events, want 2", len(events))
	}
	if events[0].Text != "hello" || events[1].Text != " world" {
		t.Errorf("multi-line: texts = %q %q", events[0].Text, events[1].Text)
	}

	// --- ParseOutput: session stats ---
	statsInput := []byte(`{"type":"response","command":"get_session_stats","success":true,"data":{"tokens":{"input":1000,"output":500,"cacheRead":100,"cacheWrite":50},"contextUsage":{"tokens":2000,"contextWindow":8000,"percent":25}}}` + "\n")
	events = adapter.ParseOutput(context.Background(), statsInput)
	if len(events) != 1 || events[0].Type != agent.EventTypeStats {
		t.Fatalf("stats: unexpected events %+v", events)
	}
	if events[0].Stats.TotalInput != 1000 || events[0].Stats.TotalOutput != 500 {
		t.Errorf("stats: input=%d output=%d", events[0].Stats.TotalInput, events[0].Stats.TotalOutput)
	}
	if events[0].Stats.ContextPercent != 25 {
		t.Errorf("stats: percent = %f", events[0].Stats.ContextPercent)
	}

	// --- HandleUIRequest ---
	resp := adapter.HandleUIRequest(context.Background(), "req-123", "confirm_action")
	if !strings.Contains(string(resp), `"extension_ui_response"`) {
		t.Errorf("ui response: missing type, got %s", string(resp))
	}
	if !strings.Contains(string(resp), `"id":"req-123"`) {
		t.Errorf("ui response: missing id, got %s", string(resp))
	}
	if !strings.Contains(string(resp), `"cancelled":true`) {
		t.Errorf("ui response: missing cancelled, got %s", string(resp))
	}
}

// TestWasmPiAdapter_ConcurrentAccess verifies that the wasmAdapter mutex
// prevents concurrent calls from corrupting the WASM module state.
// This simulates the race between readOutput's ParseOutput and Prompt's
// EncodeCommand that caused unsafe.Slice: len out of range crashes.
func TestWasmPiAdapter_ConcurrentAccess(t *testing.T) {
	adapter := loadPiWasm(t)

	cfg := RunnerConfig{ExtensionName: "pi", SessionName: "conc-test", WorktreePath: t.TempDir()}
	cfgBytes, _ := json.Marshal(cfg)
	if err := adapter.Init(context.Background(), cfgBytes); err != nil {
		t.Fatalf("Init: %v", err)
	}

	ctx := context.Background()
	var wg sync.WaitGroup
	errors := make(chan error, 20)

	// Simulate concurrent ParseOutput (from readOutput goroutine) and
	// EncodeCommand (from Prompt goroutine) calls
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			data := []byte(fmt.Sprintf(`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"turn-%d"}}`, i) + "\n")
			events := adapter.ParseOutput(ctx, data)
			if len(events) != 1 {
				errors <- fmt.Errorf("goroutine %d: ParseOutput got %d events, want 1", i, len(events))
				return
			}
			if events[0].Text != fmt.Sprintf("turn-%d", i) {
				errors <- fmt.Errorf("goroutine %d: text = %q, want %q", i, events[0].Text, fmt.Sprintf("turn-%d", i))
				return
			}
		}(i)
	}

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := adapter.EncodeCommand(ctx, agent.CommandPrompt, nil)
			if len(cmd) == 0 {
				errors <- fmt.Errorf("goroutine %d: EncodeCommand returned empty", i)
				return
			}
		}(i)
	}

	// Also interleave HandleUIRequest and SubprocessConfig calls
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			adapter.HandleUIRequest(ctx, fmt.Sprintf("req-%d", i), "confirm")
			adapter.SubprocessConfig(ctx)
		}(i)
	}

	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

// TestWasmPiAdapter_GetStateModelDiscovery verifies that the WASM module
// correctly discovers the model name via get_state response and persists it
// in subsequent agent_end events.
func TestWasmPiAdapter_GetStateModelDiscovery(t *testing.T) {
	adapter := loadPiWasm(t)

	cfg := RunnerConfig{ExtensionName: "pi", SessionName: "test", WorktreePath: t.TempDir()}
	cfgBytes, _ := json.Marshal(cfg)
	if err := adapter.Init(context.Background(), cfgBytes); err != nil {
		t.Fatalf("Init: %v", err)
	}

	getStateInput := []byte(`{"type":"response","command":"get_state","success":true,"data":{"model":{"provider":"openai","name":"gpt-4o"}}}
`)
	events := adapter.ParseOutput(context.Background(), getStateInput)
	if len(events) != 1 || events[0].Type != agent.EventTypeStatus {
		t.Fatalf("get_state: unexpected events %+v", events)
	}
	if mn, ok := events[0].RawData["model_name"]; !ok || mn != "openai/gpt-4o" {
		t.Errorf("model_name = %v, want openai/gpt-4o", mn)
	}

	// After get_state, agent_end should persist the discovered model name
	agentEndInput := []byte(`{"type":"agent_end","stopReason":"stop","messages":[{"role":"assistant","content":[{"type":"text","text":"done"}],"usage":{"input":50,"output":5}}]}
`)
	events = adapter.ParseOutput(context.Background(), agentEndInput)
	if len(events) != 1 || events[0].Type != agent.EventTypePersist {
		t.Fatalf("agent_end: unexpected events %+v", events)
	}
	if len(events[0].PersistEntries) != 1 {
		t.Fatalf("persist entries: got %d, want 1", len(events[0].PersistEntries))
	}
	if events[0].PersistEntries[0].ModelName != "openai/gpt-4o" {
		t.Errorf("persist model_name = %q, want openai/gpt-4o",
			events[0].PersistEntries[0].ModelName)
	}
}

// TestWasmPiAdapter_ToolNameMapping verifies that Pi tool names are mapped
// to Kanban tool names through the actual WASM module.
func TestWasmPiAdapter_ToolNameMapping(t *testing.T) {
	adapter := loadPiWasm(t)

	cfg := RunnerConfig{ExtensionName: "pi", SessionName: "test", WorktreePath: t.TempDir()}
	cfgBytes, _ := json.Marshal(cfg)
	adapter.Init(context.Background(), cfgBytes)

	tests := []struct {
		piTool      string
		wantKanban  string
		wantDisplay string
	}{
		{"read", "read_file", "Read File"},
		{"write", "write_file", "Write File"},
		{"edit", "edit_file", "Edit File"},
		{"bash", "bash", "Bash"},
		{"request_review", "request_review", "Request Review"},
		{"final_answer", "request_review", "Request Review"},
		{"unknown_tool", "unknown_tool", "Unknown_tool"},
	}

	for _, tt := range tests {
		t.Run(tt.piTool, func(t *testing.T) {
			input := []byte(`{"type":"tool_execution_start","toolName":"` + tt.piTool + `","toolCallId":"cid","args":{}}
`)
			events := adapter.ParseOutput(context.Background(), input)
			if len(events) != 1 || events[0].Type != agent.EventTypeToolCall {
				t.Fatalf("unexpected events: %+v", events)
			}
			if events[0].ToolCall.Name != tt.wantKanban {
				t.Errorf("name = %q, want %q",
					events[0].ToolCall.Name, tt.wantKanban)
			}
			if events[0].ToolCall.DisplayName != tt.wantDisplay {
				t.Errorf("display_name = %q, want %q",
					events[0].ToolCall.DisplayName, tt.wantDisplay)
			}
		})
	}
}

// TestWasmPiAdapter_AllocPattern verifies the core wazero allocation pattern
// from the greet.go guide: Alloc returns valid linear memory offsets that
// the host can Write to and Read back correctly via the store_result callback.
func TestWasmPiAdapter_AllocPattern(t *testing.T) {
	adapter := loadPiWasm(t)

	// Init must succeed (exercises Alloc → Write → Call → Free for config)
	cfg := RunnerConfig{
		ExtensionName: "pi",
		SessionName:   "alloc-test",
		WorktreePath:  t.TempDir(),
	}
	cfgBytes, _ := json.Marshal(cfg)
	if err := adapter.Init(context.Background(), cfgBytes); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// SubprocessConfig exercises Alloc(0) → store_result callback
	spCfg := adapter.SubprocessConfig(context.Background())
	if spCfg.Binary != "pi" {
		t.Errorf("Binary = %q, want pi", spCfg.Binary)
	}
	if len(spCfg.Args) == 0 {
		t.Error("expected non-empty args from SubprocessConfig")
	}
}
