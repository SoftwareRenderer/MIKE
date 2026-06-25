package types

import "encoding/json"

// WSMessageType is the type of a WebSocket message.
type WSMessageType string

const (
	WSTypeToken              WSMessageType = "token"
	WSTypeReasoning          WSMessageType = "reasoning"
	WSTypeReasoningEnd       WSMessageType = "reasoning_end"
	WSTypeToolCall           WSMessageType = "tool_call"
	WSTypeToolResult         WSMessageType = "tool_result"
	WSTypeStatus             WSMessageType = "status"
	WSTypeQueue              WSMessageType = "queue"
	WSTypeComplete           WSMessageType = "complete"
	WSTypeError              WSMessageType = "error"
	WSTypeUserMessage        WSMessageType = "user_message"
	WSTypeStats              WSMessageType = "stats"
	WSTypeDiffChanged        WSMessageType = "diff_changed"
	WSTypeThreadSwitch       WSMessageType = "thread_switch"
	WSTypeCheckpointPending  WSMessageType = "git_checkpoint_pending"
	WSTypeCheckpointComplete WSMessageType = "git_checkpoint_complete"
	WSTypeMergeResolution    WSMessageType = "merge_resolution"
	WSTypeBuildProgress      WSMessageType = "build_progress"
)

// StreamEvent carries a typed payload to broadcast.
type StreamEvent struct {
	Type WSMessageType
	Data interface{}
}

// WSMessage is the envelope for all WebSocket frames.
type WSMessage struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// StreamTokenEvent is an individual text token from the AI.
type StreamTokenEvent struct {
	Text string `json:"text"`
}

// StreamReasoningEvent is reasoning/thinking content.
type StreamReasoningEvent struct {
	Text string `json:"text"`
}

// StreamReasoningEndEvent marks the end of a reasoning block.
type StreamReasoningEndEvent struct {
	Summary string `json:"summary"`
}

// StreamToolCallEvent is an AI tool invocation request.
type StreamToolCallEvent struct {
	ToolID          string                 `json:"tool_id"`
	ToolName        string                 `json:"tool_name"`
	ToolDisplayName string                 `json:"tool_display_name"`
	Args            map[string]interface{} `json:"args"`
}

// StreamToolResultEvent is the result of a tool invocation.
type StreamToolResultEvent struct {
	ToolID   string `json:"tool_id"`
	Success  bool   `json:"success"`
	Output   string `json:"output"`
	Duration int64  `json:"duration"`
}

// StreamStatusEvent is a task status/activity update.
type StreamStatusEvent struct {
	State    string `json:"state"`
	Activity string `json:"activity"`
}

// StreamQueueEvent is a queue position update.
type StreamQueueEvent struct {
	Position      int `json:"position"`
	EstimatedWait int `json:"estimated_wait_seconds"`
}

// StreamCompleteEvent is sent when task execution finishes.
type StreamCompleteEvent struct {
	State   string `json:"state"`
	Summary string `json:"summary"`
	Tokens  struct {
		Prompt    int `json:"prompt"`
		Completed int `json:"completed"`
		Total     int `json:"total"`
	} `json:"tokens"`
	Duration int64 `json:"duration"`
}

// StreamErrorEvent is sent on errors during execution.
type StreamErrorEvent struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

// TerminalMessage is the envelope for terminal WebSocket frames.
type TerminalMessage struct {
	Type string `json:"type"`
	Data string `json:"data"`
	Rows int    `json:"rows,omitempty"`
	Cols int    `json:"cols,omitempty"`
}
