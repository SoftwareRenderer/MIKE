// Package llama is the OpenAI-compatible chat client: streaming and non-streaming
// completion, tool definitions (standard, plan, explore) and usage stats. Everything the
// model understands is described here; backend/exec decides when to call it.
package llama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"mike/backend/llmadapter"
	"mike/backend/types"

	"mike/backend/httputil"
	"mike/pkg/logging"
)

// LlamaError is a structured error returned by the llama.cpp-compatible server
// when it responds with a non-200 status code. It carries the HTTP status code
// and any Retry-After header so callers can implement rate-limit-aware backoff.
type LlamaError struct {
	StatusCode int
	RetryAfter time.Duration // 0 when the server did not send a Retry-After header
	Body       string
}

// Error implements the error interface, preserving the original text format.
func (e *LlamaError) Error() string {
	return fmt.Sprintf("llama error %d: %s", e.StatusCode, e.Body)
}

// IsRateLimit reports whether this error is a 429 Too Many Requests response.
func (e *LlamaError) IsRateLimit() bool {
	return e.StatusCode == http.StatusTooManyRequests
}

func (e *LlamaError) IsRequestRejected() bool {
	return e.StatusCode >= 400 && e.StatusCode < 500 &&
		e.StatusCode != http.StatusRequestTimeout && e.StatusCode != http.StatusTooManyRequests
}

// ErrStreamAborted is a stream that ended before its terminal frame: `data: [DONE]`, or a
// non-empty finish_reason on the last choice. Reaching EOF is not an error, so without this a
// provider that died mid-request reads as one that answered with nothing.
var ErrStreamAborted = errors.New("llama stream ended before completion")

// errorBody is what a LlamaError carries for a non-200: the body, or the failure to hold it.
func errorBody(resp *http.Response) string {
	body, err := httputil.ReadUpstream(resp.Body)
	if err != nil {
		return err.Error()
	}
	return string(body)
}

// newLlamaError builds a LlamaError from a non-200 response, parsing the
// Retry-After header (integer-seconds or HTTP-date form) when present.
func newLlamaError(statusCode int, header http.Header, body string) *LlamaError {
	return &LlamaError{
		StatusCode: statusCode,
		RetryAfter: parseRetryAfter(header.Get("Retry-After")),
		Body:       body,
	}
}

// parseRetryAfter parses a Retry-After header value into a duration.
// It returns 0 when the value is empty or unparseable.
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return time.Until(t)
	}
	return 0
}

// reasoningEffortTokens: thinking budget per level ("none"/"off"/"default" send none).
var reasoningEffortTokens = map[string]int{
	"low":    512,
	"medium": 2048,
	"high":   8192,
}

// applyReasoningParams sets request fields for an effort level
func applyReasoningParams(reqBody map[string]any, effort string) {
	effort = strings.TrimSpace(effort)
	switch effort {
	case "", "default":
		// Server decides.
	case "none", "off":
		reqBody["thinking_budget_tokens"] = 0
		reqBody["chat_template_kwargs"] = map[string]any{"enable_thinking": false}
	default:
		if budget, ok := reasoningEffortTokens[effort]; ok {
			reqBody["thinking_budget_tokens"] = budget
		}
		reqBody["reasoning_control"] = true
		reqBody["reasoning_effort"] = effort
	}
}

// applySamplingParams writes a model's sampler overrides into the request body, and only those: a
// nil field is left out so the provider keeps its own value, and a zero (temperature 0) is typed.
func applySamplingParams(reqBody map[string]any, s *types.ModelSampling) {
	if s == nil {
		return
	}
	putNumber(reqBody, "temperature", s.Temperature)
	putNumber(reqBody, "top_p", s.TopP)
	putNumber(reqBody, "top_k", s.TopK)
	putNumber(reqBody, "min_p", s.MinP)
	putNumber(reqBody, "frequency_penalty", s.FrequencyPenalty)
	putNumber(reqBody, "presence_penalty", s.PresencePenalty)
	putNumber(reqBody, "repetition_penalty", s.RepetitionPenalty)
	putNumber(reqBody, "seed", s.Seed)
}

func putNumber[T float64 | int](reqBody map[string]any, key string, v *T) {
	if v != nil {
		reqBody[key] = *v
	}
}

// Silence phases of an open stream. A provider that answered without sending a token is in a
// different state from one that streamed tokens and stopped: the first is usually prompt
// processing, the second is usually output the provider is holding back.
const (
	StreamPhaseFirstToken  = "first_token"
	StreamPhaseMidResponse = "mid_response"
)

// When silence on a stream starts being worth reporting. Both are floors, not deadlines: nothing
// here ends a stream for being silent unless a caller sets a ceiling (SetSilencePolicy).
const (
	streamNoticeFirstToken  = 15 * time.Second // a cold prefill of 74k tokens measures ~10s
	streamNoticeMidResponse = 30 * time.Second // longer than any gap between two tokens
	streamWaitTick          = time.Second      // the clock's resolution; the caller throttles
)

// SilenceCallback reports silence on a stream that is still open. Called repeatedly while the
// silence grows, never for a stream that is merely slow to the point of the notice floor.
type SilenceCallback func(silent time.Duration, phase string)

// ErrStreamSilent says the provider answered and then stopped sending, and that a configured
// ceiling was reached. It exists because a quiet stream is not a dead one: a provider generating
// grammar-constrained tool calls has been measured holding 16,267 tokens behind 76-89 seconds of
// silence and then finishing normally, so this error names a ceiling, never a lost connection.
type ErrStreamSilent struct {
	Silent time.Duration
	Phase  string // StreamPhaseFirstToken or StreamPhaseMidResponse
}

func (e *ErrStreamSilent) Error() string {
	if e.Phase == StreamPhaseFirstToken {
		return fmt.Sprintf("the provider sent nothing for %s", e.Silent.Round(time.Second))
	}
	return fmt.Sprintf("the provider sent nothing for %s after streaming a response", e.Silent.Round(time.Second))
}

// LlamaClient handles communication with llama.cpp OpenAI-compatible API.
type LlamaClient struct {
	baseURL         string
	modelName       string
	reasoningEffort string
	apiKey          string
	httpClient      *http.Client
	sampling        *types.ModelSampling // the model's sampler overrides; nil sends none of them
	ceiling         time.Duration        // 0: silence alone never ends a stream
	onWait          SilenceCallback      // nil: silence is not reported
	noticeFirstTok  time.Duration        // 0 means streamNoticeFirstToken
	noticeMidResp   time.Duration        // 0 means streamNoticeMidResponse
	waitTick        time.Duration        // 0 means streamWaitTick
	adapter         *llmadapter.Adapter
}

// SetSampling attaches a model's sampler overrides. Nil keeps every one of them out of the request
// body, so a model whose fields were left blank is sampled the provider's way.
func (c *LlamaClient) SetSampling(s *types.ModelSampling) {
	c.sampling = s
}

// SetSilencePolicy decides what a quiet stream costs. onWait hears every second of silence past
// the notice floor while the stream is open; a non-zero ceiling ends the call with
// ErrStreamSilent at that much silence. The zero ceiling, which is the default, never ends one —
// only the peer closing, a failed keepalive or a cancelled context does.
func (c *LlamaClient) SetSilencePolicy(ceiling time.Duration, onWait SilenceCallback) {
	c.ceiling = ceiling
	c.onWait = onWait
}

// SetAdapter configures provider-dependent request adaptation (llm
// extensions). Matching mappings are applied AFTER the standard request fields
// are set, so they can rewrite or remove them. Nil = no adaptation.
func (c *LlamaClient) SetAdapter(a *llmadapter.Adapter) {
	c.adapter = a
}

// applyAdapter applies the matched provider mapping to reqBody (in place) and
// returns the resolved headers to add. Nil when no mapping matches.
func (c *LlamaClient) applyAdapter(reqBody map[string]any) map[string]string {
	if c.adapter == nil {
		return nil
	}
	headers, match, ok := c.adapter.Adapt(c.baseURL, c.modelName, c.reasoningEffort, reqBody)
	if !ok {
		return nil
	}
	logging.For(logging.LLM).Infof("request adapted for provider %s (mapping %s)", c.baseURL, match)
	return headers
}

// NewLlamaClient creates a new LlamaClient.
func NewLlamaClient(baseURL, modelName, reasoningEffort, apiKey string) *LlamaClient {
	if reasoningEffort == "" {
		reasoningEffort = types.DefaultReasoningEffort
	}
	return &LlamaClient{
		baseURL:         strings.TrimRight(baseURL, "/"),
		modelName:       modelName,
		reasoningEffort: reasoningEffort,
		apiKey:          apiKey,
		httpClient:      httputil.NewStreamClient(), // a streaming completion is not bounded by a wall-clock timeout
	}
}

// setAuthHeader sets the Authorization header on a request if an API key is configured.
func (c *LlamaClient) setAuthHeader(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

const noResultRecorded = "No result was recorded for this tool call: the run ended before it finished."

// toAPIMessage converts a ChatMessage to the format expected by the LLM API.
// See requestMessages for the rule that spans messages (a tool call needs its answer).
func toAPIMessage(m types.ChatMessage) map[string]any {
	// An advisory is an environment note the model must see but the user never said
	role := m.Role
	if role == "advisory" {
		role = "user"
	}
	result := map[string]any{
		"role": role,
	}
	if parts := apiContentParts(m); len(parts) > 0 {
		result["content"] = parts
	} else {
		result["content"] = m.Content
	}
	if calls := apiToolCalls(m); len(calls) > 0 {
		result["tool_calls"] = calls
	}
	if m.ToolCallID != "" {
		result["tool_call_id"] = m.ToolCallID
	}
	if m.Role == "assistant" && m.ReasoningContent != "" {
		result["reasoning_content"] = m.ReasoningContent
	}
	return result
}

// apiContentParts builds a multi-modal content array in the OpenAI shape
// ({type: "image_url", image_url: {url: "data:..."}}). A part that is neither text nor an image with
// data would serialize as null, which the provider refuses, so it is left out; when nothing is left,
// the caller sends the message's plain text instead of an empty array.
func apiContentParts(m types.ChatMessage) []any {
	if len(m.ContentParts) == 0 {
		return nil
	}
	parts := make([]any, 0, len(m.ContentParts))
	for i, p := range m.ContentParts {
		switch {
		case p.Type == "text":
			parts = append(parts, map[string]any{"type": "text", "text": p.Text})
		case p.ImageSource != nil && p.ImageSource.Data != "":
			dataURL := "data:" + p.ImageSource.MediaType + ";base64," + p.ImageSource.Data
			parts = append(parts, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": dataURL},
			})
		default:
			logging.For(logging.LLM).Warnf("dropped content part %d of a %s message: neither text nor an image with data", i, m.Role)
		}
	}
	return parts
}

// apiToolCalls builds an assistant message's tool calls. `arguments` must hold a JSON object: the
// provider decodes it, so text the model truncated, double-encoded or left empty is replaced by {}.
// The tool's own result row is what tells the model what happened to that call.
func apiToolCalls(m types.ChatMessage) []map[string]any {
	var calls []map[string]any
	for _, tc := range m.ToolCalls {
		if tc.ID == "" || tc.Function.Name == "" {
			logging.For(logging.LLM).Warnf("dropped a tool call with no id or name (id=%q name=%q)", tc.ID, tc.Function.Name)
			continue
		}
		args := tc.Function.Arguments
		if !isJSONObject(args) {
			logging.For(logging.LLM).Warnf("tool call %s carries %d bytes of arguments that are not a JSON object; sending {} in their place", tc.Function.Name, len(args))
			args = "{}"
		}
		calls = append(calls, map[string]any{
			"id":       tc.ID,
			"type":     "function",
			"function": map[string]any{"name": tc.Function.Name, "arguments": args},
		})
	}
	return calls
}

// isJSONObject reports whether s is JSON that decodes to an object, which is what `arguments` holds.
func isJSONObject(s string) bool {
	var probe map[string]any
	return json.Unmarshal([]byte(s), &probe) == nil
}

// requestMessages parses a tool call's arguments to render the chat template
func requestMessages(messages []types.ChatMessage) []map[string]any {
	converted := make([]map[string]any, len(messages))
	answered := make(map[string]bool)
	for i, m := range messages {
		converted[i] = toAPIMessage(m)
		if m.Role == "tool" && m.ToolCallID != "" {
			answered[m.ToolCallID] = true
		}
	}

	result := make([]map[string]any, 0, len(messages))
	for i, m := range messages {
		if m.Role == "tool" && m.ToolCallID == "" {
			logging.For(logging.LLM).Warnf("dropped a tool answer that names no tool call")
			continue
		}
		result = append(result, converted[i])
		for _, tc := range m.ToolCalls {
			if tc.ID == "" || tc.Function.Name == "" || answered[tc.ID] {
				continue
			}
			logging.For(logging.LLM).Warnf("answered an unfinished tool call (tool_call_id=%s) so the request stays well-formed", tc.ID)
			result = append(result, map[string]any{
				"role": "tool", "content": noResultRecorded, "tool_call_id": tc.ID,
			})
		}
	}
	return result
}

// hasUserMessage reports whether the message list contains at least one user
// message. Some chat templates raise an error for system-only requests.
func hasUserMessage(messages []types.ChatMessage) bool {
	for _, m := range messages {
		if m.Role == "user" {
			return true
		}
	}
	return false
}

// ChatStream sends a chat completion request with streaming.
// onUsage (may be nil) gets an in-flight usage snapshot whenever it changes;
// see types.UsageCallback.
func (c *LlamaClient) ChatStream(ctx context.Context, messages []types.ChatMessage, tools []types.ChatTool, cb types.StreamCallback, onProgress types.PromptProgressCallback, onUsage types.UsageCallback) (*types.ChatUsage, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("llama server URL not configured")
	}
	if !hasUserMessage(messages) {
		return nil, fmt.Errorf("no user message in chat request; chat templates require at least one")
	}

	url := c.baseURL + "/chat/completions"

	reqBody := map[string]any{
		"model":           c.modelName,
		"messages":        requestMessages(messages),
		"stream":          true,
		"return_progress": true,
		// Without this, vLLM omits usage from the stream and all token
		// stats for the turn are lost. llama.cpp accepts it too (additive).
		"stream_options": map[string]any{"include_usage": true},
	}
	if len(tools) > 0 {
		reqBody["tools"] = tools
		reqBody["tool_choice"] = "auto"
	} else {
		reqBody["tool_choice"] = "none"
	}
	applyReasoningParams(reqBody, c.reasoningEffort)
	applySamplingParams(reqBody, c.sampling)
	adapterHeaders := c.applyAdapter(reqBody)

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuthHeader(req)
	for k, v := range adapterHeaders {
		req.Header.Set(k, v)
	}

	callStart := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post to llama: %w", err)
	}
	defer resp.Body.Close()

	// Close body on context cancellation to unblock the scanner.
	go func() {
		<-ctx.Done()
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, newLlamaError(resp.StatusCode, resp.Header, errorBody(resp))
	}

	var usage *types.ChatUsage
	var metrics *types.VllmMetrics
	var usageChanged bool // usage or timings updated on this frame
	var firstTokenTime time.Time
	var sawDone, sawFinish bool
	var dataFrames int
	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	// One clock for both phases, running beside the scanner. Silence is measured from the last line
	// read and reported as it grows; only a configured ceiling ends the call, and closing resp.Body
	// (the only way to unblock scanner.Scan()) records why before it closes.
	var lastByte atomic.Int64
	lastByte.Store(time.Now().UnixNano())
	var streaming atomic.Bool // a token has arrived, so the phase is mid-response
	var silenced atomic.Pointer[ErrStreamSilent]
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		noticeFirst, noticeMid, tick := c.noticeFirstTok, c.noticeMidResp, c.waitTick
		if noticeFirst == 0 {
			noticeFirst = streamNoticeFirstToken
		}
		if noticeMid == 0 {
			noticeMid = streamNoticeMidResponse
		}
		if tick == 0 {
			tick = streamWaitTick
		}
		timer := time.NewTicker(tick)
		defer timer.Stop()
		for {
			select {
			case <-watchDone:
				return
			case <-timer.C:
			}
			silent := time.Since(time.Unix(0, lastByte.Load()))
			notice, phase := noticeFirst, StreamPhaseFirstToken
			if streaming.Load() {
				notice, phase = noticeMid, StreamPhaseMidResponse
			}
			// The ceiling stands on its own: a caller who set one below the notice floor still
			// gets the stream ended, and one above it still gets the notices first.
			if c.ceiling > 0 && silent >= c.ceiling {
				silenced.Store(&ErrStreamSilent{Silent: silent, Phase: phase})
				_ = resp.Body.Close()
				return
			}
			if silent >= notice && c.onWait != nil {
				c.onWait(silent, phase)
			}
		}
	}()

	for scanner.Scan() {
		line := scanner.Text()
		lastByte.Store(time.Now().UnixNano())
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			sawDone = true
			break
		}
		dataFrames++

		var cr types.ChatResponse
		if err := json.Unmarshal([]byte(data), &cr); err != nil {
			continue
		}

		if cr.Usage != nil {
			usage = cr.Usage
			usageChanged = true
		}
		// vLLM's equivalent of timings; applied after the stream ends.
		if cr.Metrics != nil {
			metrics = cr.Metrics
		}
		// Always process Timings (per-frame for llama.cpp) to get intermediate token counts.
		// Timings.PredictedN gives cumulative completion tokens generated so far.
		// This drives real-time stats events during streaming.
		if cr.Timings != nil {
			if usage == nil {
				usage = &types.ChatUsage{}
			}
			// Use Timings for per-frame token counts (updates every frame during streaming)
			usage.PromptTokens = cr.Timings.PromptN + cr.Timings.CacheN
			usage.CompletionTokens = cr.Timings.PredictedN
			usage.PromptTokensDetails.CachedTokens = cr.Timings.CacheN
			usage.PromptMs = int64(cr.Timings.PromptMs)
			usage.PredictedMs = int64(cr.Timings.PredictedMs)
			usageChanged = true
		}
		// Hand out a copy: usage is mutated in place across frames.
		if usageChanged && onUsage != nil && usage != nil {
			snapshot := *usage
			onUsage(&snapshot)
		}
		// Forward prompt processing progress (from llama.cpp return_progress).
		// Sent once per frame, before choices, so caller can emit before first token.
		if cr.PromptProgress != nil && onProgress != nil {
			onProgress(*cr.PromptProgress)
		}

		for i := range cr.Choices {
			if cr.Choices[i].FinishReason != "" {
				sawFinish = true
			}
			normalizeReasoning(&cr.Choices[i])
			if hasContent(cr.Choices[i]) {
				streaming.Store(true)
				if firstTokenTime.IsZero() {
					firstTokenTime = time.Now()
				}
			}
			cb(cr.Choices[i])
		}
	}

	if e := silenced.Load(); e != nil {
		return usage, e
	}

	if err := scanner.Err(); err != nil {
		return usage, err
	}
	if !sawDone && !sawFinish {
		return usage, fmt.Errorf("%w: %d frame(s) read", ErrStreamAborted, dataFrames)
	}

	// Fill missing timings: provider metrics first, then wall-clock.
	if usage != nil {
		// == 0 guards keep timings priority when a server sends both.
		if metrics != nil {
			if usage.PromptMs == 0 {
				usage.PromptMs = int64(metrics.TimeToFirstTokenMs)
			}
			if usage.PredictedMs == 0 {
				usage.PredictedMs = int64(metrics.GenerationTimeMs)
			}
		}
		streamEnd := time.Now()
		if usage.PromptMs == 0 && !firstTokenTime.IsZero() {
			usage.PromptMs = firstTokenTime.Sub(callStart).Milliseconds()
		}
		if usage.PredictedMs == 0 && !firstTokenTime.IsZero() {
			usage.PredictedMs = streamEnd.Sub(firstTokenTime).Milliseconds()
		}
		logging.For(logging.LLM).Infof("final usage: prompt_tokens=%d, completion_tokens=%d, cache_tokens=%d, prompt_ms=%d, predicted_ms=%d", usage.PromptTokens, usage.CompletionTokens, usage.PromptTokensDetails.CachedTokens, usage.PromptMs, usage.PredictedMs)
	} else {
		logging.For(logging.LLM).Infof("no usage data received")
	}
	return usage, nil
}

// ChatCompletionOptions holds optional parameters for ChatCompletion.
type ChatCompletionOptions struct {
	MaxTokens *int // MaxTokens caps the response. nil = server default.
}

// ChatCompletion sends a non-streaming chat completion request and returns the usage data and full text response.
// Used for simple queries like title generation where streaming is not needed.
// Pass nil for opts to use all defaults.
func (c *LlamaClient) ChatCompletion(ctx context.Context, messages []types.ChatMessage, opts *ChatCompletionOptions) (*types.ChatUsage, string, error) {
	if c.baseURL == "" {
		return nil, "", fmt.Errorf("llama server URL not configured")
	}
	if !hasUserMessage(messages) {
		return nil, "", fmt.Errorf("no user message in chat request; chat templates require at least one")
	}

	var maxTokens int
	if opts != nil && opts.MaxTokens != nil {
		maxTokens = *opts.MaxTokens
	}

	url := c.baseURL + "/chat/completions"

	reqBody := map[string]any{
		"model":       c.modelName,
		"messages":    requestMessages(messages),
		"stream":      false,
		"tool_choice": "none",
	}
	if maxTokens > 0 {
		reqBody["max_tokens"] = maxTokens
	}
	applyReasoningParams(reqBody, c.reasoningEffort)
	applySamplingParams(reqBody, c.sampling)
	adapterHeaders := c.applyAdapter(reqBody)

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuthHeader(req)
	for k, v := range adapterHeaders {
		req.Header.Set(k, v)
	}

	callStart := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("post to llama: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", newLlamaError(resp.StatusCode, resp.Header, errorBody(resp))
	}

	var result types.ChatResponse
	payload, err := httputil.ReadUpstream(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read response: %w", err)
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, "", fmt.Errorf("decode response: %w", err)
	}

	if len(result.Choices) == 0 {
		return nil, "", fmt.Errorf("no choices in response")
	}

	normalizeReasoning(&result.Choices[0])

	// A model that spends its whole budget thinking returns no content (null on
	// vLLM, "" on llama.cpp). That is a valid completion with real usage, not an
	// error — callers handle empty text with their own fallbacks.
	var content string
	if result.Choices[0].Message.Content != nil {
		content = strings.TrimSpace(*result.Choices[0].Message.Content)
	}

	// Extract usage data
	var usage *types.ChatUsage
	if result.Usage != nil {
		usage = result.Usage
		if result.Timings != nil {
			usage.PromptMs = int64(result.Timings.PromptMs)
			usage.PredictedMs = int64(result.Timings.PredictedMs)
			if usage.PromptTokensDetails.CachedTokens == 0 && result.Timings.CacheN > 0 {
				usage.PromptTokensDetails.CachedTokens = result.Timings.CacheN
			}
		}
	} else if result.Timings != nil {
		usage = &types.ChatUsage{
			PromptTokens:     result.Timings.PromptN + result.Timings.CacheN,
			CompletionTokens: result.Timings.PredictedN,
			PromptMs:         int64(result.Timings.PromptMs),
			PredictedMs:      int64(result.Timings.PredictedMs),
		}
		usage.PromptTokensDetails.CachedTokens = result.Timings.CacheN
	}

	if usage != nil {
		// Provider metrics beat the wall-clock split below; == 0 keeps timings
		// priority when a server sends both.
		if result.Metrics != nil {
			if usage.PromptMs == 0 {
				usage.PromptMs = int64(result.Metrics.TimeToFirstTokenMs)
			}
			if usage.PredictedMs == 0 {
				usage.PredictedMs = int64(result.Metrics.GenerationTimeMs)
			}
		}
		// Estimate missing timing from wall-clock (gen ~5x slower per token than prompt).
		totalDuration := time.Since(callStart).Milliseconds()
		if usage.PromptMs == 0 && usage.PredictedMs == 0 && totalDuration > 0 {
			promptWeight := float64(usage.PromptTokens)
			genWeight := float64(usage.CompletionTokens) * 5
			if promptWeight+genWeight > 0 {
				usage.PromptMs = int64(float64(totalDuration) * promptWeight / (promptWeight + genWeight))
				usage.PredictedMs = totalDuration - usage.PromptMs
			}
		}
		logging.For(logging.LLM).Infof("non-streaming usage: prompt_tokens=%d, completion_tokens=%d, cache_tokens=%d, prompt_ms=%d, predicted_ms=%d", usage.PromptTokens, usage.CompletionTokens, usage.PromptTokensDetails.CachedTokens, usage.PromptMs, usage.PredictedMs)
	}

	return usage, content, nil
}

// normalizeReasoning copies the alternate reasoning field (vLLM's "reasoning")
// into the canonical ReasoningContent. Without it, vLLM reasoning frames look
// empty: firstTokenTime never gets set, breaking the timing fallback, and the
// text never reaches the UI. reasoning_content wins when both are present —
// never concatenate, or servers that send both would double the text.
func normalizeReasoning(choice *types.ChatChoice) {
	if choice == nil {
		return
	}
	if (choice.Delta.ReasoningContent == nil || *choice.Delta.ReasoningContent == "") &&
		choice.Delta.Reasoning != nil && *choice.Delta.Reasoning != "" {
		choice.Delta.ReasoningContent = choice.Delta.Reasoning
	}
	if (choice.Message.ReasoningContent == nil || *choice.Message.ReasoningContent == "") &&
		choice.Message.Reasoning != nil && *choice.Message.Reasoning != "" {
		choice.Message.ReasoningContent = choice.Message.Reasoning
	}
}

// hasContent reports whether the delta carries output (text, reasoning, or tool calls).
func hasContent(choice types.ChatChoice) bool {
	if choice.Delta.ReasoningContent != nil && *choice.Delta.ReasoningContent != "" {
		return true
	}
	if choice.Delta.Content != nil && *choice.Delta.Content != "" {
		return true
	}
	for _, tc := range choice.Delta.ToolCalls {
		if tc.ID != "" || tc.Function.Name != "" {
			return true
		}
	}
	return false
}

// MergeToolCalls merges streaming tool call deltas by index into complete tool calls.
func MergeToolCalls(existing []types.ChatToolCall, deltas []types.ChatToolCall) []types.ChatToolCall {
	idxMap := make(map[int]int)
	for i, tc := range existing {
		idxMap[tc.Index] = i
	}

	for _, delta := range deltas {
		pos, ok := idxMap[delta.Index]
		isNew := !ok
		if isNew {
			pos = len(existing)
			idxMap[delta.Index] = pos
			existing = append(existing, delta)
		}
		if !isNew {
			if delta.ID != "" {
				existing[pos].ID = delta.ID
			}
			if delta.Function.Name != "" {
				existing[pos].Function.Name += delta.Function.Name
			}
			if delta.Function.Arguments != "" {
				existing[pos].Function.Arguments += delta.Function.Arguments
			}
			if delta.Type != "" {
				existing[pos].Type = delta.Type
			}
		}
	}

	return existing
}
