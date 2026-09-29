package llama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"mike/backend/llmadapter"
	"mike/backend/types"
)

func openRouterStyleMapping(serverURL string) llmadapter.Mapping {
	return llmadapter.Mapping{
		Match:   serverURL,
		Headers: map[string]string{"X-Provider": "test", "X-Model": "{{model}}"},
		Body: llmadapter.BodyRules{
			Remove:   []string{"thinking_budget_tokens", "reasoning_control", "reasoning_effort", "chat_template_kwargs", "return_progress"},
			RemoveIf: map[string]string{"tool_choice": "tools"},
			Set: map[string]llmadapter.SetRule{
				"reasoning.effort":  {Value: "{{reasoning_effort}}", When: []string{"low", "medium", "high"}},
				"reasoning.enabled": {From: "reasoning_effort", Value: true, When: []string{"", "default"}},
			},
		},
	}
}

// TestChatCompletion_AdapterApplied verifies a matching mapping rewrites the
// request body (OpenRouter-style) and adds headers, for non-streaming calls.
func TestChatCompletion_AdapterApplied(t *testing.T) {
	var gotBody map[string]any
	var gotHeader http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	a := llmadapter.NewAdapter()
	a.SetMappings([]llmadapter.Mapping{openRouterStyleMapping(server.URL)})

	c := NewLlamaClient(server.URL, "test-model", "high", "secret-key")
	c.SetAdapter(a)

	_, text, err := c.ChatCompletion(context.Background(), testMessages(), nil)
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if text != "hi" {
		t.Errorf("text = %q, want %q", text, "hi")
	}

	reasoning, _ := gotBody["reasoning"].(map[string]any)
	if reasoning == nil || reasoning["effort"] != "high" {
		t.Errorf("reasoning = %v, want {effort: high}", gotBody["reasoning"])
	}
	if _, exists := reasoning["enabled"]; exists {
		t.Errorf("reasoning.enabled should not be set for effort=high: %v", reasoning)
	}
	for _, k := range []string{"thinking_budget_tokens", "reasoning_control", "reasoning_effort", "chat_template_kwargs", "tool_choice"} {
		if _, exists := gotBody[k]; exists {
			t.Errorf("body still has %q", k)
		}
	}
	if gotHeader.Get("X-Provider") != "test" || gotHeader.Get("X-Model") != "test-model" {
		t.Errorf("headers = %v", gotHeader)
	}
	if gotHeader.Get("Authorization") != "Bearer secret-key" {
		t.Errorf("Authorization = %q", gotHeader.Get("Authorization"))
	}
}

// TestChatCompletion_DefaultEffortSendsEnabled verifies effort "default" maps
// to reasoning.enabled=true (provider decides).
func TestChatCompletion_DefaultEffortSendsEnabled(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	a := llmadapter.NewAdapter()
	a.SetMappings([]llmadapter.Mapping{openRouterStyleMapping(server.URL)})
	c := NewLlamaClient(server.URL, "m", "default", "")
	c.SetAdapter(a)

	if _, _, err := c.ChatCompletion(context.Background(), testMessages(), nil); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	reasoning, _ := gotBody["reasoning"].(map[string]any)
	if reasoning == nil || reasoning["enabled"] != true {
		t.Errorf("reasoning = %v, want {enabled: true}", gotBody["reasoning"])
	}
	if _, exists := reasoning["effort"]; exists {
		t.Errorf("reasoning.effort should not be set for effort=default: %v", reasoning)
	}
}

// TestChatCompletion_NoMappingPassthrough verifies the original
// llama.cpp-flavored request goes out unchanged when nothing matches.
func TestChatCompletion_NoMappingPassthrough(t *testing.T) {
	var gotBody map[string]any
	var gotHeader http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	a := llmadapter.NewAdapter()
	a.SetMappings([]llmadapter.Mapping{{Match: "https://other.example"}})
	c := NewLlamaClient(server.URL, "m", "off", "")
	c.SetAdapter(a)

	if _, _, err := c.ChatCompletion(context.Background(), testMessages(), nil); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if gotBody["tool_choice"] != "none" {
		t.Errorf("tool_choice = %v, want none (unchanged)", gotBody["tool_choice"])
	}
	if _, exists := gotBody["chat_template_kwargs"]; !exists {
		t.Error("chat_template_kwargs should be present (llama.cpp format, unchanged)")
	}
	if gotHeader.Get("X-Provider") != "" {
		t.Errorf("adapter header leaked into unmatched request: %v", gotHeader)
	}
}

// TestChatStream_AdapterApplied verifies the same adaptation on the streaming
// path (which additionally sends return_progress).
func TestChatStream_AdapterApplied(t *testing.T) {
	var gotBody map[string]any
	var gotHeader http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	a := llmadapter.NewAdapter()
	a.SetMappings([]llmadapter.Mapping{openRouterStyleMapping(server.URL)})
	c := NewLlamaClient(server.URL, "m", "high", "")
	c.SetAdapter(a)

	var gotContent string
	_, err := c.ChatStream(context.Background(), testMessages(), nil, func(choice types.ChatChoice) {
		if choice.Delta.Content != nil {
			gotContent += *choice.Delta.Content
		}
	}, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if gotContent != "hello" {
		t.Errorf("content = %q, want hello", gotContent)
	}
	if gotBody["stream"] != true {
		t.Errorf("stream = %v, want true", gotBody["stream"])
	}
	if _, exists := gotBody["return_progress"]; exists {
		t.Error("return_progress should be removed")
	}
	reasoning, _ := gotBody["reasoning"].(map[string]any)
	if reasoning == nil || reasoning["effort"] != "high" {
		t.Errorf("reasoning = %v, want {effort: high}", gotBody["reasoning"])
	}
	if gotHeader.Get("X-Model") != "m" {
		t.Errorf("headers = %v", gotHeader)
	}
}
