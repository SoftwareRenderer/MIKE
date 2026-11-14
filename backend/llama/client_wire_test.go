package llama

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"mike/backend/types"
)

// TestChatStreamSendsIdenticalBytesForTheSameConversation holds the rule a prefix cache runs on: the
// same conversation, sent twice, must reach the provider as the same bytes. A body that differs only
// in the order of two map keys is a different prompt to a provider hashing it for KV reuse, and the
// whole conversation is prefilled again. docs/api-contract.md → The JSON era this server speaks
func TestChatStreamSendsIdenticalBytesForTheSameConversation(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies [][]byte
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	c := NewLlamaClient(server.URL, "test-model", "off", "")

	// A conversation shaped like a real turn, so the body carries a tool_calls message, a tool answer
	// and the tool definitions with their JSON schemas — the maps whose order is unspecified.
	messages := []types.ChatMessage{
		{Role: "system", Content: "You are an assistant.\nCurrent date: 2026-11-14"},
		{Role: "user", Content: "read the file"},
		{Role: "assistant"},
		{Role: "tool", Content: "package main\n", ToolCallID: "call-1"},
	}
	messages[2].ToolCalls = []types.ToolCallMsg{{ID: "call-1", Type: "function"}}
	messages[2].ToolCalls[0].Function.Name = types.ToolReadFile
	messages[2].ToolCalls[0].Function.Arguments = `{"path":"main.go"}`

	for range 20 {
		if _, err := c.ChatStream(context.Background(), messages, GetToolDefinitions(), func(types.ChatChoice) {}, nil, nil); err != nil {
			t.Fatalf("ChatStream: %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 20 {
		t.Fatalf("server saw %d requests, want 20", len(bodies))
	}
	for i, body := range bodies[1:] {
		if string(body) != string(bodies[0]) {
			t.Errorf("request %d differs from request 0 (%d vs %d bytes): a provider hashing the body for "+
				"prefix reuse would re-prefill the whole conversation", i+1, len(body), len(bodies[0]))
			break
		}
	}
}
