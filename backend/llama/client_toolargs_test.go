package llama

import (
	"context"
	"encoding/json"
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

func TestToAPIMessage_TruncatedToolCallArgumentsCannotRideTheNextRequest(t *testing.T) {
	raw := assistantCall(truncatedToolCallArgs)

	got := sentArguments(t, raw)

	if got != "{}" {
		t.Errorf("arguments = %q, want {} so the provider can decode the message", got)
	}
	if raw.ToolCalls[0].Function.Arguments != truncatedToolCallArgs {
		t.Error("the conversation was mutated: the model's own text must stay stored and displayed as sent")
	}
}

func TestToAPIMessage_EmptyToolCallArgumentsMustStillDecodeAsAnObject(t *testing.T) {
	for _, args := range []string{"", "   ", "not json", `{"unterminated": `, `"{}"`, `[1,2]`} {
		if got := sentArguments(t, assistantCall(args)); got != "{}" {
			t.Errorf("arguments %q became %q, want {}", args, got)
		}
	}
}

func TestToAPIMessage_ValidToolCallArgumentsPassThroughUnchanged(t *testing.T) {
	const args = `{"command": "go test ./...", "timeout": 300}`

	if got := sentArguments(t, assistantCall(args)); got != args {
		t.Errorf("arguments = %q, want them sent verbatim", got)
	}
}

func TestToAPIMessage_ToolCallWithoutIDOrNameIsNotSent(t *testing.T) {
	m := assistantCall(`{"command": "true"}`)
	m.ToolCalls[0].Function.Name = ""

	if _, ok := toAPIMessage(m)["tool_calls"]; ok {
		t.Error("a call with no name was sent: it cannot be answered and the provider refuses it")
	}
}

func TestToAPIMessage_ContentPartThatIsNeitherTextNorImageIsNotSentAsNull(t *testing.T) {
	m := types.ChatMessage{Role: "user", Content: "look", ContentParts: []types.ContentPart{
		{Type: "text", Text: "look"},
		{Type: "audio", Text: "stale shape"},
		{Type: "image", ImageSource: &types.ImageSource{MediaType: "image/png", Data: ""}},
		{Type: "image", ImageSource: &types.ImageSource{MediaType: "image/png", Data: "aGVsbG8="}},
	}}

	got := toAPIMessage(m)["content"]
	parts, ok := got.([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("content = %#v, want the text part and the one image with data", got)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if hasNullEntry(t, body) {
		t.Errorf("content serialized with a null entry: %s", body)
	}
}

// TestToAPIMessage_MessageWithNoUsableContentPartsFallsBackToText: an empty content array is refused,
// and dropping the message would lose the user's words, which Content still holds.
func TestToAPIMessage_MessageWithNoUsableContentPartsFallsBackToText(t *testing.T) {
	m := types.ChatMessage{Role: "user", Content: "the real text", ContentParts: []types.ContentPart{
		{Type: "audio", Text: "stale shape"},
	}}

	if got := toAPIMessage(m)["content"]; got != "the real text" {
		t.Errorf("content = %#v, want the plain-text content", got)
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
