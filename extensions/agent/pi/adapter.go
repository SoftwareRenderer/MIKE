// Package pi implements the Pi Coding Agent protocol adapter.
// It parses Pi's JSONL RPC protocol and maps it to generic agent events.
//
// Core adapter logic (no backend imports — compiles to WASM).
package pi

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"kanban/pkg/agent"
)

// Pi Protocol Data Structs
type piMessageUpdate struct {
	AssistantMessageEvent struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	} `json:"assistantMessageEvent"`
}

type piToolExecution struct {
	ToolName   string         `json:"toolName"`
	ToolCallID string         `json:"toolCallId"`
	Args       map[string]any `json:"args"`
	IsError    bool           `json:"isError"`
	Result     map[string]any `json:"result"`
}

type piAgentEnd struct {
	Model     string           `json:"model"`
	ModelName string           `json:"modelName"`
	Messages  []map[string]any `json:"messages"`
}

type piResponse struct {
	Command string         `json:"command"`
	Success bool           `json:"success"`
	Error   string         `json:"error"`
	Data    map[string]any `json:"data"`
}

type piUIRequest struct {
	Method string `json:"method"`
	ID     string `json:"id"`
}

// Tool Definition
type toolDef struct {
	piName      string
	kanbanName  string
	displayName string
}

var piTools = []toolDef{
	{"read", "read_file", "Read File"},
	{"write", "write_file", "Write File"},
	{"edit", "edit_file", "Edit File"},
	{"bash", "bash", "Bash"},
	{"request_review", "request_review", "Request Review"},
	{"final_answer", "request_review", "Request Review"},
	{"ask_question", "ask_question", "Ask Question"},
	{"create_task", "create_task", "Create Task"},
}

// piConfig holds the resolved adapter configuration after Init parses
// the raw config JSON. It combines user-provided values with defaults
// and sandbox environment variables from the extension manifest.

type piConfig struct {
	Binary      string
	Mode        string
	SessionName string
	DisplayName string
	Env         []string
	SandboxEnvs []envVar // resolved env key-value pairs from manifest sandbox config
}

// envVar is a resolved key-value pair for sandbox environment variables.
type envVar struct {
	Key   string
	Value string
}

// Adapter implements the agent.AgentAdapter interface for the Pi Coding Agent.
// It maintains state across calls: a line buffer for JSONL parsing, a command
// counter for request IDs, and the discovered model name for persistence.

type Adapter struct {
	config     piConfig
	lineBuffer string // accumulates partial JSONL lines across ParseOutput calls
	cmdID      int    // monotonically increasing counter for command request IDs
	modelName  string // discovered via get_state or agent_end; used for persistence
}

// NewAdapter creates a new Pi protocol adapter with default configuration.
func NewAdapter() *Adapter {
	return &Adapter{
		config: piConfig{
			Binary:      "pi",
			Mode:        "rpc",
			DisplayName: "Pi Coding Agent",
		},
	}
}

// Init initializes the adapter with config JSON.
// ctx is accepted for interface compliance; the pure adapter does no I/O
// during initialization, so it is not used.
func (a *Adapter) Init(_ context.Context, config []byte) error {
	var cfg struct {
		Binary        string         `json:"binary"`
		Mode          string         `json:"mode"`
		SessionName   string         `json:"session_name"`
		DisplayName   string         `json:"display_name"`
		Env           []string       `json:"env"`
		Config        map[string]any `json:"config,omitempty"`
		AgentManifest *struct {
			Sandbox *struct {
				Mount []struct {
					HostPath      string `json:"host_path"`
					ContainerPath string `json:"container_path"`
					Mode          string `json:"mode"`
				} `json:"mount,omitempty"`
				Env []struct {
					Key   string `json:"key"`
					Value string `json:"value"`
				} `json:"env,omitempty"`
			} `json:"sandbox,omitempty"`
		} `json:"agent_manifest,omitempty"`
	}
	if err := json.Unmarshal(config, &cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	cmEnvs := []envVar{}
	if cfg.AgentManifest != nil && cfg.AgentManifest.Sandbox != nil {
		for _, e := range cfg.AgentManifest.Sandbox.Env {
			cmEnvs = append(cmEnvs, envVar{Key: e.Key, Value: e.Value})
		}
	}

	a.config = piConfig{
		Binary:      defaultString(cfg.Binary, "pi"),
		Mode:        defaultString(cfg.Mode, "rpc"),
		SessionName: cfg.SessionName,
		DisplayName: defaultString(cfg.DisplayName, "Pi Coding Agent"),
		Env:         cfg.Env,
		SandboxEnvs: cmEnvs,
	}
	a.lineBuffer = ""
	a.cmdID = 0
	a.modelName = ""
	return nil
}

// SubprocessConfig returns the agent process configuration.
// ctx is accepted for interface compliance; the pure adapter does no I/O
// during config lookup, so it is not used.
func (a *Adapter) SubprocessConfig(_ context.Context) agent.SubprocessConfig {
	args := []string{"--mode", a.config.Mode}
	if a.config.SessionName != "" {
		args = append(args, "--name", a.config.SessionName)
	}
	env := a.config.Env
	// In non-sandbox mode the adapter process runs on the host and needs
	// the config directory env var so Pi can locate its config.
	for _, e := range a.config.SandboxEnvs {
		if e.Key != "" {
			env = append(env, e.Key+"="+e.Value)
		}
	}
	return agent.SubprocessConfig{
		Binary: a.config.Binary,
		Args:   args,
		Env:    env,
	}
}

// EncodeCommand encodes a high-level command into Pi JSONL protocol bytes.
// ctx is accepted for interface compliance; the pure adapter does no I/O
// during encoding, so it is not used.
func (a *Adapter) EncodeCommand(_ context.Context, cmd string, data []byte) []byte {
	a.cmdID++
	c := map[string]any{"id": fmt.Sprintf("%s-%d", cmd, a.cmdID)}

	switch cmd {
	case agent.CommandPrompt:
		var d struct {
			Message  string `json:"message"`
			Behavior string `json:"behavior"`
		}
		if data != nil {
			if err := json.Unmarshal(data, &d); err != nil {
				log.Printf("[pi-adapter] EncodeCommand: failed to unmarshal prompt data: %v", err)
			}
		}
		c["type"] = "prompt"
		c["message"] = d.Message
		if d.Behavior != "" {
			c["streamingBehavior"] = d.Behavior
		}
	case agent.CommandCompact:
		c["type"] = "compact"
	case agent.CommandStats:
		c["type"] = "get_session_stats"
	case agent.CommandGetState:
		c["type"] = "get_state"
	case agent.CommandGetAvailableModels:
		c["type"] = "get_available_models"
	case agent.CommandAbort:
		c["type"] = "abort"
	}

	result, _ := json.Marshal(c)
	return append(result, '\n')
}

// ParseOutput parses raw Pi JSONL stdout bytes into generic agent events.
// ctx is accepted for interface compliance; the pure adapter does no I/O
// during parsing, so it is not used.
func (a *Adapter) ParseOutput(_ context.Context, data []byte) []agent.Event {
	a.lineBuffer += string(data)
	var events []agent.Event

	for {
		idx := strings.IndexByte(a.lineBuffer, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimRight(a.lineBuffer[:idx], "\r")
		a.lineBuffer = a.lineBuffer[idx+1:]
		if line == "" {
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			log.Printf("[pi-adapter] ParseOutput: failed to parse JSON line (%d bytes): %v", len(line), err)
			continue
		}

		eventType, _ := raw["type"].(string)
		if ev := a.parseEvent(eventType, raw); ev != nil {
			events = append(events, *ev)
		}
	}
	return events
}

// HandleUIRequest generates a response for an agent UI request.
// ctx is accepted for interface compliance; the pure adapter does no I/O
// during handling, so it is not used.
func (a *Adapter) HandleUIRequest(_ context.Context, id, method string) []byte {
	resp := map[string]any{
		"type":      "extension_ui_response",
		"id":        id,
		"cancelled": true,
	}
	data, _ := json.Marshal(resp)
	return append(data, '\n')
}

// Close cleans up adapter state.
func (a *Adapter) Close() {
	a.lineBuffer = ""
}

// parseEvent dispatches a Pi JSONL event to the appropriate parser.
func (a *Adapter) parseEvent(eventType string, raw map[string]any) *agent.Event {
	switch eventType {
	case "message_update":
		return a.parseMessageUpdate(raw)
	case "tool_execution_start":
		return a.parseToolStart(raw)
	case "tool_execution_end":
		return a.parseToolEnd(raw)
	case "agent_start":
		return &agent.Event{Type: agent.EventTypeStatus, Status: agent.StatusData{State: "running", Activity: a.config.DisplayName + " started"}}
	case "agent_end":
		return a.parseAgentEnd(raw)
	case "response":
		return a.parseResponse(raw)
	case "extension_ui_request":
		return a.parseUIRequest(raw)
	default:
		return &agent.Event{Type: agent.EventTypeStatus, Status: agent.StatusData{State: "running", Activity: fmt.Sprintf("Pi: %s", eventType)}}
	}
}

func (a *Adapter) parseMessageUpdate(raw map[string]any) *agent.Event {
	var mu piMessageUpdate
	if err := mapToStruct(raw, &mu); err != nil {
		log.Printf("[pi-adapter] parseMessageUpdate: %v", err)
		return nil
	}
	etype := mu.AssistantMessageEvent.Type
	delta := mu.AssistantMessageEvent.Delta
	switch etype {
	case "text_delta":
		return &agent.Event{Type: agent.EventTypeToken, Text: delta}
	case "thinking_delta":
		return &agent.Event{Type: agent.EventTypeReasoning, Thinking: delta}
	}
	return nil
}

func (a *Adapter) parseToolStart(raw map[string]any) *agent.Event {
	var te piToolExecution
	if err := mapToStruct(raw, &te); err != nil {
		log.Printf("[pi-adapter] parseToolStart: %v", err)
		return nil
	}
	kanbanName := mapPiToolName(te.ToolName)
	rawArgs, _ := json.Marshal(te.Args)
	return &agent.Event{
		Type: agent.EventTypeToolCall,
		ToolCall: agent.ToolCallData{
			ID: te.ToolCallID, Name: kanbanName, DisplayName: getToolDisplayName(kanbanName),
			Args: te.Args, RawArgs: string(rawArgs),
		},
	}
}

func (a *Adapter) parseToolEnd(raw map[string]any) *agent.Event {
	var te piToolExecution
	if err := mapToStruct(raw, &te); err != nil {
		log.Printf("[pi-adapter] parseToolEnd: %v", err)
		return nil
	}
	var output string
	if te.Result != nil {
		if content, ok := te.Result["content"].([]any); ok {
			var texts []string
			for _, item := range content {
				if cm, ok := item.(map[string]any); ok && cm["type"] == "text" {
					if text, ok := cm["text"].(string); ok {
						texts = append(texts, text)
					}
				}
			}
			output = strings.Join(texts, "\n")
		}
	}
	return &agent.Event{
		Type:       agent.EventTypeToolResult,
		ToolResult: agent.ToolResultData{ID: te.ToolCallID, Output: output, Success: !te.IsError},
	}
}

func (a *Adapter) parseAgentEnd(raw map[string]any) *agent.Event {
	var ae piAgentEnd
	if err := mapToStruct(raw, &ae); err != nil {
		log.Printf("[pi-adapter] parseAgentEnd: %v", err)
		// Return a persist event with empty entries so the turn completes.
		// Without this fallback, the task would hang indefinitely waiting for a
		// persist event that never arrives (see agent_end handling in loop_agent.go).
		return &agent.Event{
			Type:   agent.EventTypePersist,
			Status: agent.StatusData{State: "idle", Activity: a.config.DisplayName + " finished (parse error)"},
		}
	}

	// The agent_end event may carry a top-level model field — use it if we
	// haven't already discovered a model name via get_state.
	if a.modelName == "" {
		if mn, ok := extractModelNameFromEvent(raw); ok {
			a.modelName = mn
		}
	}

	entries := a.buildPersistEntries(ae.Messages)
	return &agent.Event{
		Type:           agent.EventTypePersist,
		Status:         agent.StatusData{State: "idle", Activity: a.config.DisplayName + " finished"},
		PersistEntries: entries,
	}
}

func (a *Adapter) parseResponse(raw map[string]any) *agent.Event {
	var resp piResponse
	if err := mapToStruct(raw, &resp); err != nil {
		log.Printf("[pi-adapter] parseResponse: %v", err)
		return nil
	}
	if !resp.Success && resp.Error != "" {
		return &agent.Event{Type: agent.EventTypeError, RawData: map[string]any{"message": resp.Error, "code": "pi_rpc_error", "command": resp.Command}}
	}
	if resp.Command == "get_session_stats" {
		return &agent.Event{Type: agent.EventTypeStats, Stats: parseSessionStats(resp.Data)}
	}
	if resp.Command == "compact" {
		summary := ""
		if resp.Data != nil {
			if s, ok := resp.Data["summary"].(string); ok {
				summary = s
			}
		}
		return &agent.Event{Type: agent.EventTypeStatus, RawData: map[string]any{"command": "compact", "summary": summary, "success": resp.Success}}
	}
	if resp.Command == "get_available_models" {
		return &agent.Event{Type: agent.EventTypeModels, RawData: map[string]any{"models": resp.Data["models"]}}
	}
	if resp.Command == "get_state" {
		var modelName string
		if resp.Data != nil {
			if model, ok := resp.Data["model"].(map[string]any); ok {
				provider, _ := model["provider"].(string)
				modelID, _ := model["name"].(string)
				if modelID == "" {
					modelID, _ = model["id"].(string)
				}
				if modelID == "" {
					modelID, _ = model["modelId"].(string)
				}
				if provider != "" && modelID != "" {
					modelName = provider + "/" + modelID
				} else if modelID != "" {
					modelName = modelID
				}
			}
		}
		a.modelName = modelName
		return &agent.Event{Type: agent.EventTypeStatus, RawData: map[string]any{"model_name": modelName}}
	}
	return nil
}

func (a *Adapter) parseUIRequest(raw map[string]any) *agent.Event {
	var ur piUIRequest
	if err := mapToStruct(raw, &ur); err != nil {
		log.Printf("[pi-adapter] parseUIRequest: %v", err)
		return nil
	}
	return &agent.Event{
		Type:    agent.EventTypeStatus,
		Status:  agent.StatusData{State: "running", Activity: fmt.Sprintf("Pi requested: %s", ur.Method)},
		RawData: map[string]any{"ui_request_method": ur.Method, "ui_request_id": ur.ID},
	}
}

// extractModelNameFromEvent tries to find a model name in an agent_end event
// or an individual assistant message. Checks common field names that the Pi
// binary may populate.
func extractModelNameFromEvent(raw map[string]any) (string, bool) {
	if mn, ok := raw["model"].(string); ok && mn != "" {
		return mn, true
	}
	if mn, ok := raw["modelName"].(string); ok && mn != "" {
		return mn, true
	}
	msgs, _ := raw["messages"].([]any)
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := msg["role"].(string); role == "assistant" {
			if mn, ok := msg["model"].(string); ok && mn != "" {
				return mn, true
			}
		}
	}
	return "", false
}

// modelNameOrDefault returns the discovered model name, falling back to "pi"
// when the model name hasn't been discovered yet (e.g. get_state timed out).
func (a *Adapter) modelNameOrDefault() string {
	if a.modelName != "" {
		return a.modelName
	}
	return "pi"
}

// buildPersistEntries parses Pi assistant messages into PersistEntry objects.
func (a *Adapter) buildPersistEntries(messages []map[string]any) []agent.PersistEntry {
	modelName := a.modelNameOrDefault()
	toolResults := make(map[string]map[string]any)
	for _, msg := range messages {
		role, _ := msg["role"].(string)
		if role != "toolResult" {
			continue
		}
		tid, _ := msg["toolCallId"].(string)
		if tid == "" {
			continue
		}
		isError, _ := msg["isError"].(bool)
		toolResults[tid] = map[string]any{"output": extractToolResultText(msg), "success": !isError}
	}

	var entries []agent.PersistEntry
	for _, msg := range messages {
		role, _ := msg["role"].(string)
		if role != "assistant" {
			continue
		}
		text, reasoning, toolCalls, pt, ptok := parseAssistantMessage(msg)
		entries = append(entries, agent.PersistEntry{
			Role: "assistant", Content: text, ReasoningContent: reasoning,
			ModelName: modelName, ProviderName: "Pi Coding Agent",
			PromptTokens: pt, PredictedTokens: ptok,
		})
		for _, tc := range toolCalls {
			tid, _ := tc["id"].(string)
			tname, _ := tc["name"].(string)
			targs, _ := tc["arguments"].(map[string]any)
			if tid == "" {
				continue
			}
			kanbanName := mapPiToolName(tname)
			argsJSON, _ := json.Marshal(targs)
			tcData := map[string]any{
				"tool_id": tid, "tool_name": kanbanName,
				"tool_display_name": getToolDisplayName(kanbanName),
				"args":              targs, "raw_args": string(argsJSON),
			}
			tcJSON, _ := json.Marshal(tcData)
			rj := []byte(`{"output":"","success":true}`)
			if r, ok := toolResults[tid]; ok {
				rj, _ = json.Marshal(r)
			}
			entries = append(entries, agent.PersistEntry{
				Role: "tool_call", ToolCallsJSON: string(tcJSON), ToolResultJSON: string(rj),
			})
		}
	}
	return entries
}

func parseAssistantMessage(msg map[string]any) (text, reasoning string, toolCalls []map[string]any, pt, ptok int) {
	if content, ok := msg["content"].([]any); ok {
		var texts []string
		for _, c := range content {
			if cm, ok := c.(map[string]any); ok {
				switch cm["type"] {
				case "text":
					if t, ok := cm["text"].(string); ok {
						texts = append(texts, t)
					}
				case "thinking":
					if t, ok := cm["thinking"].(string); ok {
						reasoning = t
					}
				case "toolCall":
					toolCalls = append(toolCalls, cm)
				}
			}
		}
		text = strings.Join(texts, "\n")
	}
	if usage, ok := msg["usage"].(map[string]any); ok {
		if v, ok := usage["input"].(float64); ok {
			pt = int(v)
		}
		if v, ok := usage["output"].(float64); ok {
			ptok = int(v)
		}
	}
	return
}

func extractToolResultText(msg map[string]any) string {
	if c, ok := msg["content"].(string); ok {
		return c
	}
	if ca, ok := msg["content"].([]any); ok {
		var p []string
		for _, i := range ca {
			if m, ok := i.(map[string]any); ok && m["type"] == "text" {
				if t, ok := m["text"].(string); ok {
					p = append(p, t)
				}
			}
		}
		return strings.Join(p, "\n")
	}
	return ""
}

func parseSessionStats(data map[string]any) agent.SessionStatsData {
	var s agent.SessionStatsData
	if tokens, ok := data["tokens"].(map[string]any); ok {
		if v, ok := tokens["input"].(float64); ok {
			s.TotalInput = int(v)
		}
		if v, ok := tokens["output"].(float64); ok {
			s.TotalOutput = int(v)
		}
		if v, ok := tokens["cacheRead"].(float64); ok {
			s.TotalCacheRead = int(v)
		}
		if v, ok := tokens["cacheWrite"].(float64); ok {
			s.TotalCacheWrite = int(v)
		}
	}
	if cu, ok := data["contextUsage"].(map[string]any); ok {
		if v, ok := cu["tokens"].(float64); ok {
			s.ContextTokens = int(v)
		}
		if v, ok := cu["contextWindow"].(float64); ok {
			s.ContextWindow = int(v)
		}
		if v, ok := cu["percent"].(float64); ok {
			s.ContextPercent = v
		}
	}
	return s
}

func mapPiToolName(piName string) string {
	for _, t := range piTools {
		if t.piName == piName {
			return t.kanbanName
		}
	}
	return piName
}

func getToolDisplayName(name string) string {
	for _, t := range piTools {
		if t.kanbanName == name {
			return t.displayName
		}
	}
	if name == "" {
		return name
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

func defaultString(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// mapToStruct is a helper to convert map[string]any to a struct via JSON.
func mapToStruct(m map[string]any, target any) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}
