// Package pi implements the Pi Coding Agent protocol adapter.
// It parses Pi's JSONL RPC protocol and maps it to generic agent events.
//
// Core adapter logic (no backend imports — compiles to WASM).
package pi

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Event types (match backend/agent/interface.go by JSON wire format).
const (
	EventTypeToken        = "token"
	EventTypeReasoning    = "reasoning"
	EventTypeToolCall     = "tool_call"
	EventTypeToolResult   = "tool_result"
	EventTypeStatus       = "status"
	EventTypeStats        = "stats"
	EventTypeError        = "error"
	EventTypePersist      = "persist"
	EventTypeTurnComplete = "turn_complete"
)

// Event types and structs match backend/agent/interface.go by JSON wire format.
type Event struct {
	Type           string           `json:"type"`
	Text           string           `json:"text,omitempty"`
	Thinking       string           `json:"thinking,omitempty"`
	ToolCall       ToolCallData     `json:"tool_call,omitempty"`
	ToolResult     ToolResultData   `json:"tool_result,omitempty"`
	Status         StatusData       `json:"status,omitempty"`
	Stats          SessionStatsData `json:"stats,omitempty"`
	PersistEntries []PersistEntry   `json:"persist_entries,omitempty"`
	RawData        map[string]any   `json:"raw_data,omitempty"`
}

type ToolCallData struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	DisplayName string         `json:"display_name"`
	Args        map[string]any `json:"args"`
	RawArgs     string         `json:"raw_args,omitempty"`
}

type ToolResultData struct {
	ID      string `json:"id"`
	Output  string `json:"output"`
	Success bool   `json:"success"`
}

type StatusData struct {
	State    string `json:"state"`
	Activity string `json:"activity"`
}

type SessionStatsData struct {
	ContextTokens   int     `json:"context_tokens"`
	ContextWindow   int     `json:"context_window"`
	ContextPercent  float64 `json:"context_percent"`
	TotalInput      int     `json:"total_input"`
	TotalOutput     int     `json:"total_output"`
	TotalCacheRead  int     `json:"total_cache_read"`
	TotalCacheWrite int     `json:"total_cache_write"`
}

type PersistEntry struct {
	Role             string `json:"role"`
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
	ModelName        string `json:"model_name,omitempty"`
	ProviderName     string `json:"provider_name,omitempty"`
	PromptTokens     int    `json:"prompt_tokens,omitempty"`
	PredictedTokens  int    `json:"predicted_tokens,omitempty"`
	Duration         int64  `json:"duration,omitempty"`
	ToolCallsJSON    string `json:"tool_calls_json,omitempty"`
	ToolResultJSON   string `json:"tool_result_json,omitempty"`
}

// SubprocessConfig is the adapter's subprocess configuration type.
type SubprocessConfig struct {
	Binary string   `json:"binary"`
	Args   []string `json:"args"`
	Env    []string `json:"env,omitempty"`
}

// Adapter implements the agent.AgentAdapter interface for Pi's JSONL protocol.
type Adapter struct {
	config        piConfig
	lineBuffer    string
	cmdID         int
}

// envVar is a single environment variable key-value pair.
type envVar struct {
	Key   string
	Value string
}

// piConfig holds parsed adapter configuration.
type piConfig struct {
	Binary          string
	Mode            string
	SessionName     string
	DisplayName     string
	Env             []string
	SandboxMounts   []sandboxMount // resolved mounts (manifest default + runtime override)
	SandboxEnvs     []envVar       // resolved env key-value pairs (manifest default + runtime override)
}

type sandboxMount struct {
	HostPath      string
	ContainerPath string
	Mode          string
}

// toolDisplayNames maps tool names to human-readable display names.
var toolDisplayNames = map[string]string{
	"read_file":      "Read File",
	"write_file":     "Write File",
	"edit_file":      "Edit File",
	"bash":           "Bash",
	"request_review": "Request Review",
	"ask_question":   "Ask Question",
	"create_task":    "Create Task",
	"final_answer":   "Final Answer",
}

// piToolNameMap maps Pi's tool names to Kanban tool names.
var piToolNameMap = map[string]string{
	"read":           "read_file",
	"write":          "write_file",
	"edit":           "edit_file",
	"bash":           "bash",
	"request_review": "request_review",
	"final_answer":   "request_review",
	"ask_question":   "ask_question",
}

// NewAdapter creates a new Pi protocol adapter.
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
func (a *Adapter) Init(config []byte) error {
	var cfg struct {
		Binary      string   `json:"binary"`
		Mode        string   `json:"mode"`
		SessionName string   `json:"session_name"`
		DisplayName string   `json:"display_name"`
		Env         []string         `json:"env"`
		Config      map[string]any   `json:"config,omitempty"`
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

	cmMounts := []sandboxMount{}
	cmEnvs := []envVar{}
	if cfg.AgentManifest != nil && cfg.AgentManifest.Sandbox != nil {
		for _, m := range cfg.AgentManifest.Sandbox.Mount {
			cmMounts = append(cmMounts, sandboxMount{HostPath: m.HostPath, ContainerPath: m.ContainerPath, Mode: m.Mode})
		}
		for _, e := range cfg.AgentManifest.Sandbox.Env {
			cmEnvs = append(cmEnvs, envVar{Key: e.Key, Value: e.Value})
		}
	}

	a.config = piConfig{
		Binary:        defaultString(cfg.Binary, "pi"),
		Mode:          defaultString(cfg.Mode, "rpc"),
		SessionName:   cfg.SessionName,
		DisplayName:   defaultString(cfg.DisplayName, "Pi Coding Agent"),
		Env:           cfg.Env,
		SandboxMounts: cmMounts,
		SandboxEnvs:   cmEnvs,
	}
	a.lineBuffer = ""
	a.cmdID = 0
	return nil
}

// SubprocessConfig returns the agent process configuration.
func (a *Adapter) SubprocessConfig() SubprocessConfig {
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
	return SubprocessConfig{
		Binary: a.config.Binary,
		Args:   args,
		Env:    env,
	}
}

// EncodeCommand encodes a high-level command into Pi JSONL protocol bytes.
func (a *Adapter) EncodeCommand(cmd string, data []byte) []byte {
	a.cmdID++
	c := map[string]any{"id": fmt.Sprintf("%s-%d", cmd, a.cmdID)}

	switch cmd {
	case "prompt":
		var d struct {
			Message  string `json:"message"`
			Behavior string `json:"behavior"`
		}
		if data != nil {
			json.Unmarshal(data, &d)
		}
		c["type"] = "prompt"
		c["message"] = d.Message
		if d.Behavior != "" {
			c["streamingBehavior"] = d.Behavior
		}
	case "compact":
		c["type"] = "compact"
	case "stats":
		c["type"] = "get_session_stats"
	case "get_state":
		c["type"] = "get_state"
	case "abort":
		c["type"] = "abort"
	}

	result, _ := json.Marshal(c)
	return append(result, '\n')
}

// ParseOutput parses raw Pi JSONL stdout bytes into generic agent events.
func (a *Adapter) ParseOutput(data []byte) []Event {
	a.lineBuffer += string(data)
	var events []Event

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
func (a *Adapter) HandleUIRequest(id, method string) []byte {
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
func (a *Adapter) parseEvent(eventType string, raw map[string]any) *Event {
	switch eventType {
	case "message_update":
		return a.parseMessageUpdate(raw)
	case "tool_execution_start":
		return a.parseToolStart(raw)
	case "tool_execution_end":
		return a.parseToolEnd(raw)
	case "agent_start":
		return &Event{Type: EventTypeStatus, Status: StatusData{State: "running", Activity: a.config.DisplayName + " started"}}
	case "agent_end":
		return a.parseAgentEnd(raw)
	case "response":
		return a.parseResponse(raw)
	case "extension_ui_request":
		return a.parseUIRequest(raw)
	default:
		return &Event{Type: EventTypeStatus, Status: StatusData{State: "running", Activity: fmt.Sprintf("Pi: %s", eventType)}}
	}
}

func (a *Adapter) parseMessageUpdate(raw map[string]any) *Event {
	ame, ok := raw["assistantMessageEvent"].(map[string]any)
	if !ok {
		return nil
	}
	etype, _ := ame["type"].(string)
	delta, _ := ame["delta"].(string)
	switch etype {
	case "text_delta":
		return &Event{Type: EventTypeToken, Text: delta}
	case "thinking_delta":
		return &Event{Type: EventTypeReasoning, Thinking: delta}
	}
	return nil
}

func (a *Adapter) parseToolStart(raw map[string]any) *Event {
	toolName, _ := raw["toolName"].(string)
	toolCallID, _ := raw["toolCallId"].(string)
	args, _ := raw["args"].(map[string]any)
	argsCopy := make(map[string]any, len(args))
	for k, v := range args {
		argsCopy[k] = v
	}
	kanbanName := mapPiToolName(toolName)
	rawArgs, _ := json.Marshal(argsCopy)
	return &Event{
		Type: EventTypeToolCall,
		ToolCall: ToolCallData{
			ID: toolCallID, Name: kanbanName, DisplayName: getToolDisplayName(kanbanName),
			Args: argsCopy, RawArgs: string(rawArgs),
		},
	}
}

func (a *Adapter) parseToolEnd(raw map[string]any) *Event {
	toolCallID, _ := raw["toolCallId"].(string)
	isError, _ := raw["isError"].(bool)
	var output string
	if result, ok := raw["result"].(map[string]any); ok {
		if content, ok := result["content"].([]any); ok {
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
	return &Event{
		Type: EventTypeToolResult,
		ToolResult: ToolResultData{ID: toolCallID, Output: output, Success: !isError},
	}
}

func (a *Adapter) parseAgentEnd(raw map[string]any) *Event {
	messages, _ := raw["messages"].([]any)
	var msgs []map[string]any
	for _, m := range messages {
		if mm, ok := m.(map[string]any); ok {
			msgs = append(msgs, mm)
		}
	}
	entries := buildPersistEntries(msgs)
	return &Event{
		Type: EventTypePersist,
		Status: StatusData{State: "idle", Activity: a.config.DisplayName + " finished"},
		PersistEntries: entries,
	}
}

func (a *Adapter) parseResponse(raw map[string]any) *Event {
	command, _ := raw["command"].(string)
	success, _ := raw["success"].(bool)
	errMsg, _ := raw["error"].(string)
	if !success && errMsg != "" {
		return &Event{Type: EventTypeError, RawData: map[string]any{"message": errMsg, "code": "pi_rpc_error", "command": command}}
	}
	if command == "get_session_stats" {
		return &Event{Type: EventTypeStats, Stats: parseSessionStats(raw)}
	}
	if command == "compact" {
		summary := ""
		if data, ok := raw["data"].(map[string]any); ok {
			if s, ok := data["summary"].(string); ok {
				summary = s
			}
		}
		return &Event{Type: EventTypeStatus, RawData: map[string]any{"command": "compact", "summary": summary, "success": success}}
	}
	if command == "get_state" {
		var modelName string
		if data, ok := raw["data"].(map[string]any); ok {
			if model, ok := data["model"].(map[string]any); ok {
				provider, _ := model["provider"].(string)
				// Pi binary returns model name in "name" and "id" fields (not "modelId")
				modelID, _ := model["modelId"].(string)
				if modelID == "" {
					modelID, _ = model["name"].(string)
				}
				if modelID == "" {
					modelID, _ = model["id"].(string)
				}
				if provider != "" && modelID != "" {
					modelName = provider + "/" + modelID
				} else if modelID != "" {
					modelName = modelID
				}
			}
		}
		return &Event{Type: EventTypeStatus, RawData: map[string]any{"model_name": modelName}}
	}
	return nil
}

func (a *Adapter) parseUIRequest(raw map[string]any) *Event {
	method, _ := raw["method"].(string)
	extID, _ := raw["id"].(string)
	return &Event{
		Type:   EventTypeStatus,
		Status: StatusData{State: "running", Activity: fmt.Sprintf("Pi requested: %s", method)},
		RawData: map[string]any{"ui_request_method": method, "ui_request_id": extID},
	}
}

// buildPersistEntries parses Pi assistant messages into PersistEntry objects.
func buildPersistEntries(messages []map[string]any) []PersistEntry {
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

	var entries []PersistEntry
	for _, msg := range messages {
		role, _ := msg["role"].(string)
		if role != "assistant" {
			continue
		}
		text, reasoning, toolCalls, pt, ptok := parseAssistantMessage(msg)
		entries = append(entries, PersistEntry{
			Role: "assistant", Content: text, ReasoningContent: reasoning,
			ModelName: "pi", ProviderName: "Pi Coding Agent",
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
				"args": targs, "raw_args": string(argsJSON),
			}
			tcJSON, _ := json.Marshal(tcData)
			rj := []byte(`{"output":"","success":true}`)
			if r, ok := toolResults[tid]; ok {
				rj, _ = json.Marshal(r)
			}
			entries = append(entries, PersistEntry{
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

func parseSessionStats(raw map[string]any) SessionStatsData {
	var s SessionStatsData
	data, _ := raw["data"].(map[string]any)
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
	if k, ok := piToolNameMap[piName]; ok {
		return k
	}
	return piName
}

func getToolDisplayName(name string) string {
	if d, ok := toolDisplayNames[name]; ok {
		return d
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
