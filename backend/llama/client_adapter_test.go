package llama

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"mike/backend/llmadapter"
	"mike/backend/types"
)

// openRouterStyleMapping is the shape a provider mapping ships: rename what its provider reads, delete
// what llama.cpp invented, and attach the headers that provider asks for. The effort→reasoning.enabled
// branch of a real mapping is proved in backend/llmadapter, whose own fixture carries it.
func openRouterStyleMapping(serverURL string) llmadapter.Mapping {
	return llmadapter.Mapping{
		Match:   serverURL,
		Headers: map[string]string{"X-Provider": "test", "X-Model": "{{model}}"},
		Body: llmadapter.BodyRules{
			Remove:   []string{"thinking_budget_tokens", "reasoning_control", "reasoning_effort", "chat_template_kwargs", "return_progress"},
			RemoveIf: map[string]string{"tool_choice": "tools"},
			Set: map[string]llmadapter.SetRule{
				"reasoning.effort": {Value: "{{reasoning_effort}}", When: []string{"low", "medium", "high"}},
			},
		},
	}
}

// capturedRequest holds what the fake provider was sent.
type capturedRequest struct {
	body   map[string]any
	header http.Header
}

// recordRequest answers one completion, streaming or not, and keeps its body and headers.
func recordRequest(t *testing.T, stream bool) (*httptest.Server, *capturedRequest) {
	t.Helper()
	got := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.header = r.Header.Clone()
		data, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(data, &got.body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

// send runs one completion of the requested kind against the recorded server.
func send(t *testing.T, c *LlamaClient, stream bool) {
	t.Helper()
	ctx := context.Background()
	if stream {
		var content string
		if _, err := c.ChatStream(ctx, testMessages(), nil, func(choice types.ChatChoice) {
			if choice.Delta.Content != nil {
				content += *choice.Delta.Content
			}
		}, nil, nil); err != nil {
			t.Fatalf("ChatStream: %v", err)
		}
		if content != "hello" {
			t.Errorf("content = %q, want hello", content)
		}
		return
	}
	if _, text, err := c.ChatCompletion(ctx, testMessages(), nil); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	} else if text != "hi" {
		t.Errorf("text = %q, want hi", text)
	}
}

// TestClientAppliesTheProviderMapping: the mapping runs on the body before it is marshalled, on both
// paths, and only the mapping whose prefix matches the server's URL is the one applied.
func TestClientAppliesTheProviderMapping(t *testing.T) {
	t.Run("a matching mapping rewrites the completion body and its headers", func(t *testing.T) {
		srv, got := recordRequest(t, false)
		a := llmadapter.NewAdapter()
		a.SetMappings([]llmadapter.Mapping{openRouterStyleMapping(srv.URL)})

		c := NewLlamaClient(srv.URL, "test-model", "high", "secret-key")
		c.SetAdapter(a)
		send(t, c, false)

		reasoning, _ := got.body["reasoning"].(map[string]any)
		if reasoning == nil || reasoning["effort"] != "high" {
			t.Errorf("reasoning = %v, want {effort: high}", got.body["reasoning"])
		}
		if _, exists := reasoning["enabled"]; exists {
			t.Errorf("reasoning.enabled should not be set for effort=high: %v", reasoning)
		}
		for _, k := range []string{"thinking_budget_tokens", "reasoning_control", "reasoning_effort", "chat_template_kwargs", "tool_choice"} {
			if _, exists := got.body[k]; exists {
				t.Errorf("body still has %q", k)
			}
		}
		if got.header.Get("X-Provider") != "test" || got.header.Get("X-Model") != "test-model" {
			t.Errorf("headers = %v", got.header)
		}
		if got.header.Get("Authorization") != "Bearer secret-key" {
			t.Errorf("Authorization = %q", got.header.Get("Authorization"))
		}
	})

	t.Run("the streaming path is adapted too", func(t *testing.T) {
		srv, got := recordRequest(t, true)
		a := llmadapter.NewAdapter()
		a.SetMappings([]llmadapter.Mapping{openRouterStyleMapping(srv.URL)})

		c := NewLlamaClient(srv.URL, "m", "high", "")
		c.SetAdapter(a)
		send(t, c, true)

		if got.body["stream"] != true {
			t.Errorf("stream = %v, want true", got.body["stream"])
		}
		if _, exists := got.body["return_progress"]; exists {
			t.Error("return_progress should be removed")
		}
		reasoning, _ := got.body["reasoning"].(map[string]any)
		if reasoning == nil || reasoning["effort"] != "high" {
			t.Errorf("reasoning = %v, want {effort: high}", got.body["reasoning"])
		}
		if got.header.Get("X-Model") != "m" {
			t.Errorf("headers = %v", got.header)
		}
	})

	t.Run("an unmatched mapping leaves the llama.cpp request alone", func(t *testing.T) {
		srv, got := recordRequest(t, false)
		a := llmadapter.NewAdapter()
		a.SetMappings([]llmadapter.Mapping{{Match: "https://other.example"}})

		c := NewLlamaClient(srv.URL, "m", "off", "")
		c.SetAdapter(a)
		send(t, c, false)

		if got.body["tool_choice"] != "none" {
			t.Errorf("tool_choice = %v, want none (unchanged)", got.body["tool_choice"])
		}
		if _, exists := got.body["chat_template_kwargs"]; !exists {
			t.Error("chat_template_kwargs should be present (llama.cpp format, unchanged)")
		}
		if got.header.Get("X-Provider") != "" {
			t.Errorf("adapter header leaked into an unmatched request: %v", got.header)
		}
	})
}
