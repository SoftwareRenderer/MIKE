package types

import (
	"encoding/json"
	"strings"
)

// Attachment is one file attached to a user message — the element type of the
// JSON array stored in TaskMessage.Attachments. The field names follow what the
// frontend sends (camelCase mimeType), which is why they differ from the usual
// snake_case JSON tags.
type Attachment struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"` // base64, optionally a data: URL
}

// ImageSource carries base64 image data for a multi-modal content part.
//
// It is a named type rather than an inline struct field so that every place
// building an image part does not have to re-declare the same anonymous shape.
// Internally it holds raw base64; llama serializes it into OpenAI's
// {type:"image_url", image_url:{url:"data:<media>;base64,..."}} wire format.
type ImageSource struct {
	Type      string `json:"type"` // always "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// ContentPart represents a part of a multi-modal message (text or image).
type ContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// ImageSource is set only for image parts; serialized to OpenAI image_url
	// format for the LLM API.
	ImageSource *ImageSource `json:"source,omitempty"`
}

// NewImagePart builds an image ContentPart from an attachment. An empty mime
// type falls back to image/png (what the frontend sends for clipboard images),
// and a data: URL is stripped down to its bare base64 payload.
func NewImagePart(att Attachment) ContentPart {
	mediaType := att.MimeType
	if mediaType == "" {
		mediaType = "image/png"
	}
	data := att.Data
	if idx := strings.Index(data, ","); idx >= 0 {
		data = data[idx+1:]
	}
	return ContentPart{
		Type:        "image",
		ImageSource: &ImageSource{Type: "base64", MediaType: mediaType, Data: data},
	}
}

// ContentParts builds the multi-modal content for a message: the text as the
// leading part, then one image part per attachment (attachments with no data are
// dropped). ok is false when attachmentsJSON is empty or unparseable, so the
// caller falls back to plain-text content instead of sending a content-part
// message the model may reject.
func ContentParts(text, attachmentsJSON string) (parts []ContentPart, ok bool) {
	if attachmentsJSON == "" {
		return nil, false
	}
	var atts []Attachment
	if err := json.Unmarshal([]byte(attachmentsJSON), &atts); err != nil {
		return nil, false
	}
	parts = make([]ContentPart, 0, len(atts)+1)
	parts = append(parts, ContentPart{Type: "text", Text: text})
	for _, att := range atts {
		if att.Data == "" {
			continue
		}
		parts = append(parts, NewImagePart(att))
	}
	return parts, true
}

// ChatMessage represents a message in the conversation.
type ChatMessage struct {
	Role             string        `json:"role"`
	Content          string        `json:"content"`
	ContentParts     []ContentPart // multi-modal content parts; when set, overrides Content in LLM API
	ReasoningContent string        `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCallMsg `json:"tool_calls,omitempty"`   // assistant's tool calls (for message history)
	ToolCallID       string        `json:"tool_call_id,omitempty"` // tool result's reference to assistant's call
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
	Type     string   `json:"type"`
	Function *ToolDef `json:"function"`
}

// ToolDef defines a tool's schema.
type ToolDef struct {
	Name        string         `json:"name"`
	DisplayName string         // UI-only, not sent to LLM
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
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
// Reasoning arrives as reasoning_content (llama.cpp, cloud providers) or
// reasoning (vLLM); the llama client folds the latter into ReasoningContent.
type ChatChoiceDelta struct {
	Content          *string        `json:"content"`
	ReasoningContent *string        `json:"reasoning_content,omitempty"`
	Reasoning        *string        `json:"reasoning,omitempty"`
	ToolCalls        []ChatToolCall `json:"tool_calls,omitempty"`
}

// ChatChoiceMessage is the full message in a non-streaming response.
// Reasoning follows the same two-name rule as ChatChoiceDelta.
type ChatChoiceMessage struct {
	Content          *string `json:"content"`
	ReasoningContent *string `json:"reasoning_content,omitempty"`
	Reasoning        *string `json:"reasoning,omitempty"`
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
	CacheN      int     `json:"cache_n"`  // tokens served from KV cache
	PromptN     int     `json:"prompt_n"` // tokens actually processed
	PromptMs    float64 `json:"prompt_ms"`
	PredictedN  int     `json:"predicted_n"` // tokens generated
	PredictedMs float64 `json:"predicted_ms"`
}

// PromptProgressData is the prompt processing progress from llama.cpp.
type PromptProgressData struct {
	Total     int `json:"total"`
	Cache     int `json:"cache"`
	Processed int `json:"processed"`
	TimeMs    int `json:"time_ms"`
}

// ChatResponse is a full streaming response frame.
type ChatResponse struct {
	Choices        []ChatChoice        `json:"choices"`
	Usage          *ChatUsage          `json:"usage,omitempty"`
	Timings        *ChatTimings        `json:"timings,omitempty"`
	PromptProgress *PromptProgressData `json:"prompt_progress,omitempty"`
	Metrics        *VllmMetrics        `json:"metrics,omitempty"`
}

// VllmMetrics carries the vLLM server-side timings the backend actually uses,
// sent on the final frame next to usage. vLLM sends no timings block, so this is
// its only timing source: time_to_first_token_ms is prompt processing,
// generation_time_ms is generation. vLLM reports more (queue_time_ms,
// mean_itl_ms, tokens_per_second); they are deliberately not captured — nothing
// reads them, and this struct is never re-serialised to the client.
type VllmMetrics struct {
	TimeToFirstTokenMs float64 `json:"time_to_first_token_ms"`
	GenerationTimeMs   float64 `json:"generation_time_ms"`
}

// StreamCallback is called for each streaming token/tool_call.
type StreamCallback func(choice ChatChoice)

// PromptProgressCallback is called when the LLM sends prompt processing progress.
type PromptProgressCallback func(progress PromptProgressData)

// UsageCallback receives a snapshot of accumulated usage during streaming,
// each time it changes (llama.cpp: per-frame timings; vLLM: the final usage
// frame). The pointer is a copy, safe to retain. ChatStream's returned usage is
// only available after the stream ends, which is too late for live stats.
type UsageCallback func(usage *ChatUsage)
