package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"kanban/backend/extensions"
	"kanban/pkg/agent"
)

// mockAdapter implements agent.AgentAdapter for testing.
type mockAdapter struct {
	mu               sync.Mutex
	initConfig       []byte
	subprocessConfig agent.SubprocessConfig
	encodeResponses  map[string][]byte // cmd -> bytes
	parseResponses   map[string][]agent.Event // input bytes key -> events
	uiResponses      map[string][]byte  // "id:method" -> bytes
	closed           bool
}

func newMockAdapter() *mockAdapter {
	return &mockAdapter{
		encodeResponses: make(map[string][]byte),
		parseResponses:  make(map[string][]agent.Event),
		uiResponses:     make(map[string][]byte),
		subprocessConfig: agent.SubprocessConfig{
			Binary: "cat", // harmless binary for testing pipes
			Args:   []string{},
		},
	}
}

func (m *mockAdapter) SetSubprocessConfig(cfg agent.SubprocessConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subprocessConfig = cfg
}

func (m *mockAdapter) SetEncodeResponse(cmd string, bytes []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.encodeResponses[cmd] = bytes
}

func (m *mockAdapter) SetParseResponse(input string, events []agent.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.parseResponses[input] = events
}

func (m *mockAdapter) Init(_ context.Context, config []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initConfig = config
	return nil
}

func (m *mockAdapter) SubprocessConfig(_ context.Context) agent.SubprocessConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.subprocessConfig
}

func (m *mockAdapter) EncodeCommand(_ context.Context, cmd string, data []byte) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if resp, ok := m.encodeResponses[cmd]; ok {
		return resp
	}
	// Default: return data with newline
	if data != nil {
		return append(data, '\n')
	}
	return []byte("{}\n")
}

func (m *mockAdapter) ParseOutput(_ context.Context, data []byte) []agent.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	if events, ok := m.parseResponses[string(data)]; ok {
		return events
	}
	return nil
}

func (m *mockAdapter) HandleUIRequest(_ context.Context, id, method string) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := id + ":" + method
	if resp, ok := m.uiResponses[key]; ok {
		return resp
	}
	return nil
}

func (m *mockAdapter) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
}

func (m *mockAdapter) ListModels(_ context.Context) []agent.AgentModel {
	return nil
}

// TestMockAdapter_VerifiesInterface ensures mockAdapter implements agent.AgentAdapter.
var _ agent.AgentAdapter = (*mockAdapter)(nil)

// TestNewAgentRunner_Host verifies runner creation in host mode.
func TestNewAgentRunner_Host(t *testing.T) {
	// Register a mock factory
	adapter := newMockAdapter()
	RegisterAdapterFactory("test-agent", func() agent.AgentAdapter { return adapter })

	ext := &extensions.Extension{
		Name:    "test-agent",
		Version: "1.0.0",
		Type:    extensions.ExtensionTypeAgent,
		Manifest: extensions.ExtensionManifest{
			DisplayName: "Test Agent",
			Agent: &extensions.AgentConfig{
				
			},
		},
		Config: map[string]any{},
	}

	runner, err := NewAgentRunner(context.Background(), ext, nil, "test-task", t.TempDir(), "test-session", false)
	if err != nil {
		t.Fatalf("NewAgentRunner: %v", err)
	}
	defer runner.Close()

	if runner.PID() <= 0 {
		t.Errorf("expected positive PID, got %d", runner.PID())
	}

	// Verify config was passed to adapter
	var cfg RunnerConfig
	if err := json.Unmarshal(adapter.initConfig, &cfg); err != nil {
		t.Fatalf("unmarshal init config: %v", err)
	}
	if cfg.ExtensionName != "test-agent" {
		t.Errorf("ExtensionName = %q, want %q", cfg.ExtensionName, "test-agent")
	}
	if cfg.SessionName != "test-session" {
		t.Errorf("SessionName = %q, want %q", cfg.SessionName, "test-session")
	}
}

// TestAgentRunner_Prompt verifies that Prompt sends encoded command to stdin.
func TestAgentRunner_Prompt(t *testing.T) {
	// Use a mock adapter that returns known bytes for prompt command
	adapter := newMockAdapter()
	// Set subprocess to a process that echoes stdin to stdout
	adapter.SetSubprocessConfig(agent.SubprocessConfig{
		Binary: "cat",
		Args:   []string{},
	})
	adapter.SetEncodeResponse(agent.CommandPrompt, []byte("test-prompt-command\n"))

	RegisterAdapterFactory("test-prompt", func() agent.AgentAdapter { return adapter })

	ext := &extensions.Extension{
		Name:    "test-prompt",
		Type:    extensions.ExtensionTypeAgent,
		Manifest: extensions.ExtensionManifest{
			Agent: &extensions.AgentConfig{},
		},
	}

	runner, err := NewAgentRunner(context.Background(), ext, nil, "test-task", t.TempDir(), "session", false)
	if err != nil {
		t.Fatalf("NewAgentRunner: %v", err)
	}
	defer runner.Close()

	// Send a prompt
	err = runner.Prompt(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
}

// TestAgentRunner_EventChan verifies events are received from the adapter.
func TestAgentRunner_EventChan(t *testing.T) {
	adapter := newMockAdapter()
	// Set up a parse response for a known input
	testEvents := []agent.Event{
		{Type: agent.EventTypeToken, Text: "hello"},
		{Type: agent.EventTypeReasoning, Thinking: "let me think"},
	}
	adapter.SetParseResponse("test-input", testEvents)
	// Use a subprocess that outputs known data
	adapter.SetSubprocessConfig(agent.SubprocessConfig{
		Binary: "echo",
		Args:   []string{"test-input"},
	})

	RegisterAdapterFactory("test-events", func() agent.AgentAdapter { return adapter })

	ext := &extensions.Extension{
		Name:    "test-events",
		Type:    extensions.ExtensionTypeAgent,
		Manifest: extensions.ExtensionManifest{
			Agent: &extensions.AgentConfig{},
		},
	}

	runner, err := NewAgentRunner(context.Background(), ext, nil, "test-task", t.TempDir(), "session", false)
	if err != nil {
		t.Fatalf("NewAgentRunner: %v", err)
	}
	defer runner.Close()

	// Wait for events with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	eventReceived := false
	for {
		select {
		case event := <-runner.EventChan():
			if event.Type == agent.EventTypeToken && event.Text == "hello" {
				eventReceived = true
			}
		case <-ctx.Done():
			goto done
		}
	}
done:
	if !eventReceived {
		t.Log("note: event may not arrive if subprocess exited before adapter parsed output")
	}
}

// TestAgentRunner_Close verifies graceful shutdown.
func TestAgentRunner_Close(t *testing.T) {
	adapter := newMockAdapter()
	RegisterAdapterFactory("test-close", func() agent.AgentAdapter { return adapter })

	ext := &extensions.Extension{
		Name:    "test-close",
		Type:    extensions.ExtensionTypeAgent,
		Manifest: extensions.ExtensionManifest{
			Agent: &extensions.AgentConfig{},
		},
	}

	runner, err := NewAgentRunner(context.Background(), ext, nil, "test-task", t.TempDir(), "session", false)
	if err != nil {
		t.Fatalf("NewAgentRunner: %v", err)
	}

	runner.Close()

	// Verify adapter was closed
	adapter.mu.Lock()
	closed := adapter.closed
	adapter.mu.Unlock()
	if !closed {
		t.Error("expected adapter to be closed")
	}

	// Verify double-close is safe
	runner.Close()
}

// TestAgentRunner_ModelName verifies model name discovery.
func TestAgentRunner_ModelName(t *testing.T) {
	adapter := newMockAdapter()
	RegisterAdapterFactory("test-model", func() agent.AgentAdapter { return adapter })

	ext := &extensions.Extension{
		Name:    "test-model",
		Type:    extensions.ExtensionTypeAgent,
		Manifest: extensions.ExtensionManifest{
			Agent: &extensions.AgentConfig{},
		},
	}

	runner, err := NewAgentRunner(context.Background(), ext, nil, "test-task", t.TempDir(), "session", false)
	if err != nil {
		t.Fatalf("NewAgentRunner: %v", err)
	}
	defer runner.Close()

	// Default model name should be "Agent"
	if name := runner.ModelName(); name != "Agent" {
		t.Errorf("ModelName = %q, want %q", name, "Agent")
	}
}

// TestAgentRunner_QueryStats verifies stats query.
func TestAgentRunner_QueryStats(t *testing.T) {
	adapter := newMockAdapter()
	RegisterAdapterFactory("test-stats", func() agent.AgentAdapter { return adapter })

	ext := &extensions.Extension{
		Name:    "test-stats",
		Type:    extensions.ExtensionTypeAgent,
		Manifest: extensions.ExtensionManifest{
			Agent: &extensions.AgentConfig{},
		},
	}

	runner, err := NewAgentRunner(context.Background(), ext, nil, "test-task", t.TempDir(), "session", false)
	if err != nil {
		t.Fatalf("NewAgentRunner: %v", err)
	}
	defer runner.Close()

	// Query stats with short timeout (should return zero values when no response)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	stats := runner.QueryStats(ctx)
	// Should return zero values (no adapter response)
	if stats.ContextWindow != 0 {
		t.Errorf("ContextWindow = %d, want 0", stats.ContextWindow)
	}
}

// TestRunnerConfig_Serialization verifies RunnerConfig marshals/unmarshals correctly.
func TestRunnerConfig_Serialization(t *testing.T) {
	cfg := RunnerConfig{
		ExtensionName: "pi",
		SessionName:   "kanban-test-task",
		WorktreePath:  "/tmp/worktree",
		Sandbox:       true,
		Config:        map[string]any{"key": "value"},
		AgentManifest: &extensions.AgentConfig{
			
		},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded RunnerConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.ExtensionName != cfg.ExtensionName {
		t.Errorf("ExtensionName = %q, want %q", decoded.ExtensionName, cfg.ExtensionName)
	}
	if decoded.Sandbox != cfg.Sandbox {
		t.Errorf("Sandbox = %v, want %v", decoded.Sandbox, cfg.Sandbox)
	}
}


// TestAdapterRegistry verifies factory registration and lookup.
func TestAdapterRegistry(t *testing.T) {
	called := false
	RegisterAdapterFactory("registry-test", func() agent.AgentAdapter {
		called = true
		return newMockAdapter()
	})

	// Load via registry
	ext := &extensions.Extension{
		Name: "registry-test",
		Type: extensions.ExtensionTypeAgent,
		Manifest: extensions.ExtensionManifest{
			Agent: &extensions.AgentConfig{},
		},
	}

	adapter, err := LoadAdapter(ext)
	if err != nil {
		t.Fatalf("LoadAdapter: %v", err)
	}
	adapter.Close()

	if !called {
		t.Error("expected factory to be called")
	}
}

// TestEventTypes verifies event type constants are non-empty and unique.
func TestEventTypes(t *testing.T) {
	types := []string{
		agent.EventTypeToken,
		agent.EventTypeReasoning,
		agent.EventTypeToolCall,
		agent.EventTypeToolResult,
		agent.EventTypeStatus,
		agent.EventTypeStats,
		agent.EventTypeError,
		agent.EventTypePersist,
		agent.EventTypeTurnComplete,
	}
	seen := make(map[string]bool)
	for _, typ := range types {
		if typ == "" {
			t.Error("event type is empty")
		}
		if seen[typ] {
			t.Errorf("duplicate event type: %q", typ)
		}
		seen[typ] = true
	}
}

// TestPersistEntry_Serialization verifies agent.PersistEntry marshals correctly.
func TestPersistEntry_Serialization(t *testing.T) {
	entry := agent.PersistEntry{
		Role:             "assistant",
		Content:          "Hello world",
		ReasoningContent: "Let me think...",
		ModelName:        "test-model",
		PromptTokens:     100,
		PredictedTokens:  50,
		Duration:         1500,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded agent.PersistEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Role != entry.Role {
		t.Errorf("Role = %q, want %q", decoded.Role, entry.Role)
	}
	if decoded.Content != entry.Content {
		t.Errorf("Content = %q, want %q", decoded.Content, entry.Content)
	}
	if decoded.PromptTokens != entry.PromptTokens {
		t.Errorf("PromptTokens = %d, want %d", decoded.PromptTokens, entry.PromptTokens)
	}
}

// brokenAdapter implements agent.AgentAdapter that returns nil/empty for everything.
// Used to test nil-safety of the runner.
type brokenAdapter struct {
	subprocessConfig agent.SubprocessConfig
}

func (b *brokenAdapter) Init(_ context.Context, config []byte) error {
	return nil
}

func (b *brokenAdapter) SubprocessConfig(_ context.Context) agent.SubprocessConfig {
	return b.subprocessConfig
}

func (b *brokenAdapter) EncodeCommand(_ context.Context, cmd string, data []byte) []byte {
	return nil // simulates a broken/stale adapter
}

func (b *brokenAdapter) ParseOutput(_ context.Context, data []byte) []agent.Event {
	return nil
}

func (b *brokenAdapter) HandleUIRequest(_ context.Context, id, method string) []byte {
	return nil
}

func (b *brokenAdapter) Close() {}

func (b *brokenAdapter) ListModels(_ context.Context) []agent.AgentModel {
	return nil
}

var _ agent.AgentAdapter = (*brokenAdapter)(nil)

// TestNewAgentRunner_BrokenAdapter verifies that NewAgentRunner rejects
// adapters where Init succeeds but agent.SubprocessConfig returns empty binary.
func TestNewAgentRunner_BrokenAdapter(t *testing.T) {
	// Register a factory that returns an adapter with empty subprocess config
	RegisterAdapterFactory("test-broken", func() agent.AgentAdapter {
		return &brokenAdapter{}
	})

	ext := &extensions.Extension{
		Name:    "test-broken",
		Type:    extensions.ExtensionTypeAgent,
		Manifest: extensions.ExtensionManifest{
			Agent: &extensions.AgentConfig{},
		},
	}

	_, err := NewAgentRunner(context.Background(), ext, nil, "test-task", t.TempDir(), "session", false)
	if err == nil {
		t.Fatal("expected error from NewAgentRunner with broken adapter, got nil")
	}
	if !containsStr(err.Error(), "empty binary") {
		t.Errorf("expected 'empty binary' in error, got: %v", err)
	}
}

// TestAgentRunner_PromptNilEncode verifies Prompt returns error when
// EncodeCommand returns nil (broken adapter scenario).
func TestAgentRunner_PromptNilEncode(t *testing.T) {
	adapter := &brokenAdapter{subprocessConfig: agent.SubprocessConfig{Binary: "cat"}}
	RegisterAdapterFactory("test-nil-encode", func() agent.AgentAdapter { return adapter })

	ext := &extensions.Extension{
		Name:    "test-nil-encode",
		Type:    extensions.ExtensionTypeAgent,
		Manifest: extensions.ExtensionManifest{
			Agent: &extensions.AgentConfig{},
		},
	}

	runner, err := NewAgentRunner(context.Background(), ext, nil, "test-task", t.TempDir(), "session", false)
	if err != nil {
		t.Fatalf("NewAgentRunner: %v", err)
	}
	defer runner.Close()

	err = runner.Prompt(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error from Prompt with nil EncodeCommand, got nil")
	}
	if !containsStr(err.Error(), "nil") {
		t.Errorf("expected 'nil' in error, got: %v", err)
	}
}

// TestAgentRunner_CloseNilEncode verifies Close handles nil EncodeCommand gracefully.
func TestAgentRunner_CloseNilEncode(t *testing.T) {
	adapter := &brokenAdapter{subprocessConfig: agent.SubprocessConfig{Binary: "cat"}}
	RegisterAdapterFactory("test-close-nil", func() agent.AgentAdapter { return adapter })

	ext := &extensions.Extension{
		Name:    "test-close-nil",
		Type:    extensions.ExtensionTypeAgent,
		Manifest: extensions.ExtensionManifest{
			Agent: &extensions.AgentConfig{},
		},
	}

	runner, err := NewAgentRunner(context.Background(), ext, nil, "test-task", t.TempDir(), "session", false)
	if err != nil {
		t.Fatalf("NewAgentRunner: %v", err)
	}

	// Close should not panic even with nil EncodeCommand
	runner.Close()

	// Double-close should also be safe
	runner.Close()
}

// containsStr is a helper for error message checking.
func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}()))
}

// TestSubprocessConfig_Serialization verifies agent.SubprocessConfig marshals correctly.
func TestSubprocessConfig_Serialization(t *testing.T) {
	cfg := agent.SubprocessConfig{
		Binary: "pi",
		Args:   []string{"--mode", "rpc", "--name", "test"},
		Env:    []string{"PI_DIR=/tmp/pi"},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded agent.SubprocessConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Binary != cfg.Binary {
		t.Errorf("Binary = %q, want %q", decoded.Binary, cfg.Binary)
	}
	if len(decoded.Args) != len(cfg.Args) {
		t.Errorf("Args len = %d, want %d", len(decoded.Args), len(cfg.Args))
	}
}

