// Package agent defines the contract between MIKE and an agent adapter: the event stream and the
// adapter interface a WASM module implements. It has no server dependencies on purpose, so an adapter
// can be built and tested without importing the backend.
package agent

import "context"

// Event is one agent event, JSON-encoded across the WASM boundary. The adapter produces it and the
// runner streams it to the executor.
type Event struct {
	Type           string           `json:"type"`
	Text           string           `json:"text,omitempty"`            // for EventTypeToken
	Thinking       string           `json:"thinking,omitempty"`        // for EventTypeReasoning
	ToolCall       ToolCallData     `json:"tool_call,omitempty"`       // for EventTypeToolCall
	ToolResult     ToolResultData   `json:"tool_result,omitempty"`     // for EventTypeToolResult
	Status         StatusData       `json:"status,omitempty"`          // for EventTypeStatus
	Stats          SessionStatsData `json:"stats,omitempty"`           // for EventTypeStats
	PersistEntries []PersistEntry   `json:"persist_entries,omitempty"` // for EventTypePersist
	RawData        map[string]any   `json:"raw_data,omitempty"`        // adapter-specific extra fields
}

// ToolCallData describes a tool call from the agent.
type ToolCallData struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	DisplayName string         `json:"display_name"`
	Args        map[string]any `json:"args"`
	RawArgs     string         `json:"raw_args,omitempty"`
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
	Cost            float64 `json:"cost"`
}

// PersistEntry is a message the adapter hands over at turn-end for the executor to persist.
type PersistEntry struct {
	Role             string  `json:"role"` // "assistant" or "tool_call"
	Content          string  `json:"content,omitempty"`
	ReasoningContent string  `json:"reasoning_content,omitempty"`
	ModelName        string  `json:"model_name,omitempty"`
	ProviderName     string  `json:"provider_name,omitempty"`
	PromptTokens     int     `json:"prompt_tokens,omitempty"`
	PredictedTokens  int     `json:"predicted_tokens,omitempty"`
	PromptMs         int64   `json:"prompt_ms,omitempty"`        // prompt processing time (ms)
	PredictedMs      int64   `json:"predicted_ms,omitempty"`     // generation time (ms)
	Cost             float64 `json:"cost,omitempty"`             // per-message cost from usage.cost.total
	ToolCallsJSON    string  `json:"tool_calls_json,omitempty"`  // JSON of tool call data (role=tool_call)
	ToolResultJSON   string  `json:"tool_result_json,omitempty"` // JSON of tool result data (role=tool_call)
}

// Event type constants are a contract with the WASM adapters in ../MIKE-extensions/agent/*, a tree this
// repository cannot build. Before deleting one, grep that tree and check the shipped module:
// `strings ../MIKE-extensions/agent/pi/pi.wasm | grep <name>`.
const (
	EventTypeToken      = "token"
	EventTypeReasoning  = "reasoning"
	EventTypeToolCall   = "tool_call"
	EventTypeToolResult = "tool_result"
	EventTypeStatus     = "status"
	EventTypeStats      = "stats"
	EventTypeError      = "error"
	EventTypePersist    = "persist"
)

// Command type constants (passed to EncodeCommand).
const (
	CommandPrompt        = "prompt"
	CommandCompact       = "compact"
	CommandStats         = "stats"
	CommandGetState      = "get_state"
	CommandSetModel      = "set_model"
	CommandSwitchSession = "switch_session"
	CommandAbort         = "abort"
)

// SubprocessConfig names the agent process to spawn: the adapter returns it, the runner uses it.
type SubprocessConfig struct {
	Binary string   `json:"binary"`
	Args   []string `json:"args"`
	Env    []string `json:"env,omitempty"`
}

// AgentModel describes a model available from an agent.
type AgentModel struct {
	Name          string `json:"name"`
	ContextWindow *int   `json:"context_window,omitempty"`
}

// AgentAdapter is the boundary between the runner (backend) and a WASM adapter: the runner loads the
// module and wraps its exports, the adapter owns all protocol logic. Every ctx is the caller's and is
// used only to cancel the call in flight.
type AgentAdapter interface {
	// Init initializes the adapter with extension config JSON.
	Init(ctx context.Context, config []byte) error

	// SubprocessConfig returns the agent process the runner spawns, with its args and env.
	SubprocessConfig(ctx context.Context) SubprocessConfig

	// EncodeCommand encodes a high-level command into protocol bytes for agent stdin. cmd is one of the
	// Command constants and data is command-specific JSON.
	EncodeCommand(ctx context.Context, cmd string, data []byte) []byte

	// ParseOutput parses raw agent stdout bytes into events. The adapter keeps state across calls (line
	// buffering, turn state) and may return several events for one input.
	ParseOutput(ctx context.Context, data []byte) []Event

	// HandleUIRequest generates response bytes for an agent UI request (e.g., cancel a dialog), or nil
	// when no response should be sent.
	HandleUIRequest(ctx context.Context, id, method string) []byte

	// ListModels returns the models available from the agent, or an empty list when the agent cannot
	// discover them.
	ListModels(ctx context.Context) []AgentModel

	// Close cleans up adapter state.
	Close()
}

// AdapterFactory creates a new AgentAdapter instance.
type AdapterFactory func() AgentAdapter

// CompactResult holds the result of a compaction request.
type CompactResult struct {
	Summary string
	Err     error
}

// WASM function names the adapter module must export.
const (
	WasmFnInit             = "Init"
	WasmFnSubprocessConfig = "SubprocessConfig"
	WasmFnEncodeCommand    = "EncodeCommand"
	WasmFnParseOutput      = "ParseOutput"
	WasmFnHandleUIRequest  = "HandleUIRequest"
	WasmFnClose            = "Close"
	WasmFnAlloc            = "Alloc"
	WasmFnFree             = "Free"
)
