//go:build !wasm

package pi

import (
	"kanban/backend/agent"
)

// Ensure nativeAdapter wraps Adapter and implements agent.AgentAdapter.
var _ agent.AgentAdapter = (*nativeAdapter)(nil)

// nativeAdapter wraps the core Adapter to implement agent.AgentAdapter.
// It translates between local types and agent package types.
type nativeAdapter struct {
	*Adapter
}

// NewNativeAdapter creates an adapter that implements agent.AgentAdapter.
func NewNativeAdapter() *nativeAdapter {
	return &nativeAdapter{Adapter: NewAdapter()}
}

// Init initializes the adapter with config JSON.
func (a *nativeAdapter) Init(config []byte) error {
	return a.Adapter.Init(config)
}

// SubprocessConfig returns the agent process configuration.
func (a *nativeAdapter) SubprocessConfig() agent.SubprocessConfig {
	cfg := a.Adapter.SubprocessConfig()
	return agent.SubprocessConfig{
		Binary: cfg.Binary,
		Args:   cfg.Args,
		Env:    cfg.Env,
	}
}

// EncodeCommand encodes a high-level command into Pi JSONL protocol bytes.
func (a *nativeAdapter) EncodeCommand(cmd string, data []byte) []byte {
	return a.Adapter.EncodeCommand(cmd, data)
}

// ParseOutput parses raw Pi JSONL stdout bytes into generic agent events.
func (a *nativeAdapter) ParseOutput(data []byte) []agent.Event {
	events := a.Adapter.ParseOutput(data)
	result := make([]agent.Event, len(events))
	for i, e := range events {
		result[i] = agent.Event{
			Type:           e.Type,
			Text:           e.Text,
			Thinking:       e.Thinking,
			ToolCall:       toAgentToolCall(e.ToolCall),
			ToolResult:     toAgentToolResult(e.ToolResult),
			Status:         toAgentStatus(e.Status),
			Stats:          toAgentStats(e.Stats),
			PersistEntries: toAgentPersistEntries(e.PersistEntries),
			RawData:        e.RawData,
		}
	}
	return result
}

// HandleUIRequest generates a response for an agent UI request.
func (a *nativeAdapter) HandleUIRequest(id, method string) []byte {
	return a.Adapter.HandleUIRequest(id, method)
}

// Close cleans up adapter state.
func (a *nativeAdapter) Close() {
	a.Adapter.Close()
}

// Type conversion helpers.

func toAgentToolCall(tc ToolCallData) agent.ToolCallData {
	return agent.ToolCallData{
		ID:          tc.ID,
		Name:        tc.Name,
		DisplayName: tc.DisplayName,
		Args:        tc.Args,
		RawArgs:     tc.RawArgs,
	}
}

func toAgentToolResult(tr ToolResultData) agent.ToolResultData {
	return agent.ToolResultData{
		ID:      tr.ID,
		Output:  tr.Output,
		Success: tr.Success,
	}
}

func toAgentStatus(s StatusData) agent.StatusData {
	return agent.StatusData{
		State:    s.State,
		Activity: s.Activity,
	}
}

func toAgentStats(s SessionStatsData) agent.SessionStatsData {
	return agent.SessionStatsData{
		ContextTokens:   s.ContextTokens,
		ContextWindow:   s.ContextWindow,
		ContextPercent:  s.ContextPercent,
		TotalInput:      s.TotalInput,
		TotalOutput:     s.TotalOutput,
		TotalCacheRead:  s.TotalCacheRead,
		TotalCacheWrite: s.TotalCacheWrite,
	}
}

func toAgentPersistEntries(entries []PersistEntry) []agent.PersistEntry {
	if entries == nil {
		return nil
	}
	result := make([]agent.PersistEntry, len(entries))
	for i, e := range entries {
		result[i] = agent.PersistEntry{
			Role:             e.Role,
			Content:          e.Content,
			ReasoningContent: e.ReasoningContent,
			ModelName:        e.ModelName,
			ProviderName:     e.ProviderName,
			PromptTokens:     e.PromptTokens,
			PredictedTokens:  e.PredictedTokens,
			Duration:         e.Duration,
			ToolCallsJSON:    e.ToolCallsJSON,
			ToolResultJSON:   e.ToolResultJSON,
		}
	}
	return result
}
