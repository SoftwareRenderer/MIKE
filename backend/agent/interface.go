package agent

// Event represents a generic agent event, JSON-serialized across the WASM boundary.
// The adapter (extension) produces Events; the runner (backend) streams them to the executor.
type Event struct {
	Type           string            `json:"type"`
	Text           string            `json:"text,omitempty"`           // for EventTypeToken
	Thinking       string            `json:"thinking,omitempty"`       // for EventTypeReasoning
	ToolCall       ToolCallData      `json:"tool_call,omitempty"`      // for EventTypeToolCall
	ToolResult     ToolResultData    `json:"tool_result,omitempty"`    // for EventTypeToolResult
	Status         StatusData        `json:"status,omitempty"`         // for EventTypeStatus
	Stats          SessionStatsData  `json:"stats,omitempty"`          // for EventTypeStats
	PersistEntries []PersistEntry    `json:"persist_entries,omitempty"` // for EventTypePersist
	RawData        map[string]any    `json:"raw_data,omitempty"`       // adapter-specific extra fields
}

// ToolCallData describes a tool call from the agent.
type ToolCallData struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	DisplayName  string         `json:"display_name"`
	Args         map[string]any `json:"args"`
	RawArgs      string         `json:"raw_args,omitempty"`
}

// ToolResultData describes a tool execution result.
type ToolResultData struct {
	ID      string `json:"id"`
	Output  string `json:"output"`
	Success bool   `json:"success"`
}

// StatusData describes an agent status change.
type StatusData struct {
	State    string `json:"state"`    // "running", "idle", "failed", etc.
	Activity string `json:"activity"` // human-readable activity description
}

// SessionStatsData describes agent session/token usage stats.
type SessionStatsData struct {
	ContextTokens   int     `json:"context_tokens"`
	ContextWindow   int     `json:"context_window"`
	ContextPercent  float64 `json:"context_percent"`
	TotalInput      int     `json:"total_input"`
	TotalOutput     int     `json:"total_output"`
	TotalCacheRead  int     `json:"total_cache_read"`
	TotalCacheWrite int     `json:"total_cache_write"`
}

// PersistEntry is a pre-parsed message for DB persistence.
// The adapter fills these at turn-end; the executor persists them generically.
type PersistEntry struct {
	Role             string `json:"role"`               // "assistant" or "tool_call"
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
	ModelName        string `json:"model_name,omitempty"`
	ProviderName     string `json:"provider_name,omitempty"`
	PromptTokens     int    `json:"prompt_tokens,omitempty"`
	PredictedTokens  int    `json:"predicted_tokens,omitempty"`
	Duration         int64  `json:"duration,omitempty"`
	ToolCallsJSON    string `json:"tool_calls_json,omitempty"`    // JSON of tool call data (role=tool_call)
	ToolResultJSON   string `json:"tool_result_json,omitempty"`   // JSON of tool result data (role=tool_call)
}

// Event type constants.
const (
	EventTypeToken     = "token"
	EventTypeReasoning = "reasoning"
	EventTypeToolCall  = "tool_call"
	EventTypeToolResult = "tool_result"
	EventTypeStatus    = "status"
	EventTypeStats     = "stats"
	EventTypeError     = "error"
	EventTypePersist   = "persist"
	EventTypeTurnComplete = "turn_complete"
)

// Command type constants (passed to EncodeCommand).
const (
	CommandPrompt   = "prompt"
	CommandCompact  = "compact"
	CommandStats    = "stats"
	CommandGetState = "get_state"
	CommandAbort    = "abort"
)

// SubprocessConfig describes the agent process to spawn.
// Returned by the adapter; consumed by the runner.
type SubprocessConfig struct {
	Binary string   `json:"binary"`
	Args   []string `json:"args"`
	Env    []string `json:"env,omitempty"`
}

// CompactResult holds the result of a compaction request.
type CompactResult struct {
	Summary string
	Err     error
}

// AgentAdapter is the interface between the runner (backend) and the adapter (WASM extension).
// The runner loads a WASM module and wraps its exports behind this interface.
// The adapter owns ALL protocol logic: command encoding, output parsing, state management.
type AgentAdapter interface {
	// Init initializes the adapter with extension config JSON.
	Init(config []byte) error

	// SubprocessConfig returns the agent process configuration.
	// The runner spawns this binary with these args/env.
	SubprocessConfig() SubprocessConfig

	// EncodeCommand encodes a high-level command into protocol bytes for agent stdin.
	// cmd is one of CommandPrompt, CommandCompact, CommandStats, CommandGetState, CommandAbort.
	// data is command-specific JSON (e.g., {"message":"...","behavior":"..."} for prompt).
	EncodeCommand(cmd string, data []byte) []byte

	// ParseOutput parses raw agent stdout bytes into structured events.
	// The adapter maintains state across calls (e.g., line buffering, turn state).
	// Returns multiple events if multiple protocol messages are in the input.
	ParseOutput(data []byte) []Event

	// HandleUIRequest generates response bytes for an agent UI request (e.g., cancel a dialog).
	// Returns nil if no response should be sent.
	HandleUIRequest(id, method string) []byte

	// Close cleans up adapter state.
	Close()
}

// WASM function names that the adapter module must export.
const (
	WasmFnInit             = "Init"
	WasmFnSubprocessConfig = "SubprocessConfig"
	WasmFnEncodeCommand    = "EncodeCommand"
	WasmFnParseOutput      = "ParseOutput"
	WasmFnHandleUIRequest  = "HandleUIRequest"
	WasmFnClose            = "Close"
)
