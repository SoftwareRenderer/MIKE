package types

// ChatMessage represents a message in the conversation.
type ChatMessage struct {
	Role             string      `json:"role"`
	Content          string      `json:"content"`
	ReasoningContent string      `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCallMsg `json:"tool_calls,omitempty"` // assistant's tool calls (for message history)
	ToolCallID       string      `json:"tool_call_id,omitempty"` // tool result's reference to assistant's call
}

// ToolCallMsg is a tool call in an assistant message (non-streaming format).
type ToolCallMsg struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
	Type string `json:"type"`
}

// ChatTool represents a tool definition.
type ChatTool struct {
	Type     string `json:"type"`
	Function *ToolDef `json:"function"`
}

// ToolDef defines a tool's schema.
type ToolDef struct {
	Name        string                 `json:"name"`
	DisplayName string                 // UI-only, not sent to LLM
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ChatToolCall represents a tool call (or delta) from the model.
// In streaming responses, each chunk has an Index field to identify
// which tool call the delta belongs to. The client must merge deltas
// by index to reconstruct complete tool calls.
type ChatToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
	Type string `json:"type"`
}

// ChatChoiceDelta is the delta in a streaming response.
type ChatChoiceDelta struct {
	Content          *string        `json:"content"`
	ReasoningContent *string        `json:"reasoning_content,omitempty"`
	ToolCalls        []ChatToolCall `json:"tool_calls,omitempty"`
}

// ChatChoiceMessage is the full message in a non-streaming response.
type ChatChoiceMessage struct {
	Content *string `json:"content"`
}

// ChatChoice is a single choice in the response.
type ChatChoice struct {
	Delta        ChatChoiceDelta   `json:"delta"`
	Message      ChatChoiceMessage `json:"message"`
	FinishReason string            `json:"finish_reason"`
}

// PromptTokensDetails holds breakdown of prompt tokens.
type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

// ChatUsage token usage stats from the LLM response.
type ChatUsage struct {
	PromptTokens        int                 `json:"prompt_tokens"`
	PromptTokensDetails PromptTokensDetails `json:"prompt_tokens_details"`
	CompletionTokens    int                 `json:"completion_tokens"`
	TotalTokens         int                 `json:"total_tokens"`
	PromptMs            int64               `json:"-"` // populated from timings after parse
	PredictedMs         int64               `json:"-"` // populated from timings after parse
}

// ChatTimings processing timings from the LLM response.
type ChatTimings struct {
	CacheN        int     `json:"cache_n"`        // tokens served from KV cache
	PromptN       int     `json:"prompt_n"`        // tokens actually processed
	PromptMs      float64 `json:"prompt_ms"`
	PredictedN    int     `json:"predicted_n"`     // tokens generated
	PredictedMs   float64 `json:"predicted_ms"`
}

// ChatResponse is a full streaming response frame.
type ChatResponse struct {
	Choices  []ChatChoice `json:"choices"`
	Usage    *ChatUsage   `json:"usage,omitempty"`
	Timings  *ChatTimings `json:"timings,omitempty"`
}

// StreamCallback is called for each streaming token/tool_call.
type StreamCallback func(choice ChatChoice)
