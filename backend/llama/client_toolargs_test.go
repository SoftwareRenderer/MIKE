package llama

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mike/backend/types"
)

// truncatedToolCallArgs is what a model really sent in task ridge-meteor-anvil: the object cut short
// by a stray escaped quote, followed by the tool-call tag it was being written inside. The provider
// decodes `arguments` to render the conversation, so replaying this text makes it refuse every later
// request for the thread — 400 "Expecting value: line 1 column 111 (char 110)".
const truncatedToolCallArgs = `{"command": "cd /home/sandbox/MIKE/frontend && npm run check 2>&1 | tail -8", "timeout\": 300}` +
	"\n" + `</parameter": `

func assistantCall(args string) types.ChatMessage {
	m := types.ChatMessage{Role: "assistant"}
	m.ToolCalls = []types.ToolCallMsg{{ID: "call-1", Type: "function"}}
	m.ToolCalls[0].Function.Name = "bash"
	m.ToolCalls[0].Function.Arguments = args
	return m
}

func toolAnswerFor(id, content string) types.ChatMessage {
	return types.ChatMessage{Role: "tool", Content: content, ToolCallID: id}
}

// sentArguments is the arguments string an assistant message goes out with.
func sentArguments(t *testing.T, m types.ChatMessage) string {
	t.Helper()
	calls, ok := toAPIMessage(m)["tool_calls"].([]map[string]any)
	if !ok || len(calls) == 0 {
		t.Fatalf("no tool_calls in the converted message: %#v", m)
	}
	fn, _ := calls[0]["function"].(map[string]any)
	args, _ := fn["arguments"].(string)
	return args
}

// TestToAPIMessage_ToolCallArgumentsAlwaysDecodeAsAnObject: `arguments` is parsed by the provider to
// render the conversation, so anything that is not a JSON object goes out as {} — including the
// truncated text a model really produced. The caller's own copy keeps the bytes, because the
// transcript and the reviewer's card show what the model said.
func TestToAPIMessage_ToolCallArgumentsAlwaysDecodeAsAnObject(t *testing.T) {
	const valid = `{"command": "go test ./...", "timeout": 300}`
	for _, tc := range []struct {
		name string
		args string
		want string
	}{
		{"a valid object is sent verbatim", valid, valid},
		{"empty", "", "{}"},
		{"blank", "   ", "{}"},
		{"prose", "not json", "{}"},
		{"unterminated", `{"unterminated": `, "{}"},
		{"an object inside a string", `"{}"`, "{}"},
		{"an array, which is not an object", `[1,2]`, "{}"},
		{"cut short by a stray escape", truncatedToolCallArgs, "{}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := assistantCall(tc.args)
			if got := sentArguments(t, raw); got != tc.want {
				t.Errorf("arguments = %q, want %q", got, tc.want)
			}
			if raw.ToolCalls[0].Function.Arguments != tc.args {
				t.Error("the conversation was mutated: the model's own text must stay stored and displayed as sent")
			}
		})
	}
}

func TestToAPIMessage_ToolCallWithoutIDOrNameIsNotSent(t *testing.T) {
	m := assistantCall(`{"command": "true"}`)
	m.ToolCalls[0].Function.Name = ""

	if _, ok := toAPIMessage(m)["tool_calls"]; ok {
		t.Error("a call with no name was sent: it cannot be answered and the provider refuses it")
	}
}

// TestToAPIMessage_ContentPartsAreSentOrDropped: a content array the provider can read, or the plain
// text — never an entry that decodes to null, and never the user's words lost behind a stale part.
func TestToAPIMessage_ContentPartsAreSentOrDropped(t *testing.T) {
	for _, tc := range []struct {
		name      string
		content   string
		parts     []types.ContentPart
		wantParts int    // > 0: content goes out as an array of this many parts
		wantText  string // otherwise: content goes out as this plain string
	}{
		{
			name:    "the text and the one image with data go out, the rest are dropped",
			content: "look",
			parts: []types.ContentPart{
				{Type: "text", Text: "look"},
				{Type: "audio", Text: "stale shape"},
				{Type: "image", ImageSource: &types.ImageSource{MediaType: "image/png", Data: ""}},
				{Type: "image", ImageSource: &types.ImageSource{MediaType: "image/png", Data: "aGVsbG8="}},
			},
			wantParts: 2,
		},
		{
			name:     "no usable part falls back to the plain text",
			content:  "the real text",
			parts:    []types.ContentPart{{Type: "audio", Text: "stale shape"}},
			wantText: "the real text",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := toAPIMessage(types.ChatMessage{Role: "user", Content: tc.content, ContentParts: tc.parts})["content"]
			if tc.wantText != "" {
				if got != tc.wantText {
					t.Errorf("content = %#v, want the plain-text content %q", got, tc.wantText)
				}
				return
			}
			parts, ok := got.([]any)
			if !ok || len(parts) != tc.wantParts {
				t.Fatalf("content = %#v, want %d parts", got, tc.wantParts)
			}
			body, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if hasNullEntry(t, body) {
				t.Errorf("content serialized with a null entry: %s", body)
			}
		})
	}
}

// hasNullEntry reports whether a marshaled array holds a null element.
func hasNullEntry(t *testing.T, body []byte) bool {
	t.Helper()
	var items []any
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, item := range items {
		if item == nil {
			return true
		}
	}
	return false
}

// TestToAPIMessage_ReasoningContentRidesItsOwnField: the thinking goes out beside the answer and
// never inside it, and only on an assistant message — the chat shape has no place for it anywhere
// else, so a stray value on another role must not reach the provider.
func TestToAPIMessage_ReasoningContentRidesItsOwnField(t *testing.T) {
	call := func() []types.ToolCallMsg {
		calls := []types.ToolCallMsg{{ID: "call_1", Type: "function"}}
		calls[0].Function.Name = types.ToolReadFile
		calls[0].Function.Arguments = `{"path":"foo.txt"}`
		return calls
	}
	for _, tc := range []struct {
		name        string
		role        string
		content     string
		reasoning   string
		calls       []types.ToolCallMsg
		toolCallID  string
		wantContent string
		wantReason  string // "" = the key must be absent
	}{
		{
			name: "an answer and its thinking go out as two fields", role: "assistant",
			content: "Here is the answer", reasoning: "Let me think step by step...",
			wantContent: "Here is the answer", wantReason: "Let me think step by step...",
		},
		{
			name: "thinking with no answer", role: "assistant",
			reasoning:  "I need to think about this",
			wantReason: "I need to think about this",
		},
		{
			name: "no thinking sends no key", role: "assistant",
			content: "Simple response", wantContent: "Simple response",
		},
		{
			name: "a tool call rides beside its thinking", role: "assistant",
			content: "Let me check", reasoning: "I should read the file first", calls: call(),
			wantContent: "Let me check", wantReason: "I should read the file first",
		},
		{
			name: "a user message's thinking is dropped", role: "user",
			content: "hello", reasoning: "should be ignored", wantContent: "hello",
		},
		{
			name: "a tool answer's thinking is dropped", role: "tool",
			content: "file contents", reasoning: "should be ignored", toolCallID: "call_1", wantContent: "file contents",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := types.ChatMessage{Role: tc.role, Content: tc.content, ReasoningContent: tc.reasoning,
				ToolCalls: tc.calls, ToolCallID: tc.toolCallID}
			got := toAPIMessage(m)

			if got["role"] != tc.role {
				t.Errorf("role = %v, want %q", got["role"], tc.role)
			}
			if got["content"] != tc.wantContent {
				t.Errorf("content = %v, want %q", got["content"], tc.wantContent)
			}
			reason, present := got["reasoning_content"]
			if tc.wantReason == "" {
				if present {
					t.Errorf("reasoning_content = %v, want the key absent", reason)
				}
				return
			}
			if reason != tc.wantReason {
				t.Errorf("reasoning_content = %v, want %q", reason, tc.wantReason)
			}
			if tc.calls != nil && got["tool_calls"] == nil {
				t.Error("the tool call was lost beside the reasoning")
			}
		})
	}
}

// TestToAPIMessage_AdvisoryGoesOutAsUser: an advisory is an environment note (LSP diagnostics,
// merge-conflict instructions). The chat API has no role for it, so it must leave as "user" while the
// transcript and the summarizer keep calling it "advisory".
func TestToAPIMessage_AdvisoryGoesOutAsUser(t *testing.T) {
	msg := types.ChatMessage{
		Role:    "advisory",
		Content: "LSP diagnostics for this turn's changes (1 file with findings):",
	}

	result := toAPIMessage(msg)

	if result["role"] != "user" {
		t.Errorf("expected role='user' on the wire, got %v", result["role"])
	}
	if result["content"] != msg.Content {
		t.Errorf("content changed on the way out: %v", result["content"])
	}
	// Other roles are untouched.
	if toAPIMessage(types.ChatMessage{Role: "system", Content: "x"})["role"] != "system" {
		t.Error("system role must pass through")
	}
}

func TestRequestMessages_UnansweredToolCallIsAnsweredWhereTheProviderLooks(t *testing.T) {
	msgs := []types.ChatMessage{
		{Role: "user", Content: "run it"},
		assistantCall(`{"command": "true"}`),
		{Role: "user", Content: "and now?"},
	}

	got := requestMessages(msgs)

	if len(got) != 4 {
		t.Fatalf("%d messages, want the three plus the answer: %#v", len(got), got)
	}
	answer := got[2]
	if answer["role"] != "tool" || answer["tool_call_id"] != "call-1" {
		t.Fatalf("message after the assistant is %#v, want the tool answer for call-1", answer)
	}
	if answer["content"] != noResultRecorded {
		t.Errorf("answer = %v, want it to say no result was recorded", answer["content"])
	}
}

// TestRequestMessages_ToolCallAnsweredLaterIsNotAnsweredTwice: the utility prompts emit the assistants
// first and their tool answers after, so "answered" means named somewhere in the request.
func TestRequestMessages_ToolCallAnsweredLaterIsNotAnsweredTwice(t *testing.T) {
	msgs := []types.ChatMessage{
		{Role: "user", Content: "run it"},
		assistantCall(`{"command": "true"}`),
		{Role: "user", Content: "and now?"},
		toolAnswerFor("call-1", "done"),
	}

	var answers int
	for _, m := range requestMessages(msgs) {
		if m["tool_call_id"] == "call-1" {
			answers++
		}
	}
	if answers != 1 {
		t.Errorf("%d answers for call-1, want 1: two answers for one call is a refusal", answers)
	}
}

func TestRequestMessages_ToolAnswerNamingNoCallIsNotSent(t *testing.T) {
	msgs := []types.ChatMessage{
		{Role: "user", Content: "hi"},
		{Role: "tool", Content: "output nobody can attribute"},
	}

	got := requestMessages(msgs)

	if len(got) != 1 {
		t.Fatalf("%d messages sent: %#v, want the unattributable tool answer dropped", len(got), got)
	}
}

// TestChatStreamPoisonedHistoryStillLeavesTheProviderWithValidJSON is the reason no stored row needs
// repairing: a conversation whose tool call carries the truncated arguments still goes out decodable,
// so the task can run again.
func TestChatStreamPoisonedHistoryStillLeavesTheProviderWithValidJSON(t *testing.T) {
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	c := NewLlamaClient(server.URL, "test-model", "off", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	msgs := []types.ChatMessage{
		{Role: "user", Content: "check it"},
		assistantCall(truncatedToolCallArgs),
		toolAnswerFor("call-1", "Failed: exit code 1"),
		{Role: "user", Content: "continue"},
	}
	if _, err := c.ChatStream(ctx, msgs, nil, func(types.ChatChoice) {}, nil, nil); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	var sent struct {
		Messages []struct {
			Role      string `json:"role"`
			ToolCalls []struct {
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("the provider could not decode the request: %v\n%s", err, body)
	}
	var args string
	for _, m := range sent.Messages {
		for _, tc := range m.ToolCalls {
			args = tc.Function.Arguments
		}
	}
	if args != "{}" {
		t.Errorf("arguments on the wire = %q, want {}", args)
	}
	if msgs[1].ToolCalls[0].Function.Arguments != truncatedToolCallArgs {
		t.Error("the conversation the caller owns was rewritten")
	}
}
