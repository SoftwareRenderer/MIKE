package llama

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mike/backend/types"
)

// TestChatStream_ReadIdleTimeout verifies ChatStream returns
// context.DeadlineExceeded when the server stops sending data.
func TestChatStream_ReadIdleTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("no flusher")
			return // unreachable, satisfies compiler
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n"))
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	c := NewLlamaClient(server.URL, "test-model", "off", "")
	c.readIdleTimeout = 100 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.ChatStream(ctx, testMessages(), nil, func(choice types.ChatChoice) {}, nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("expected deadline exceeded error, got: %v", err)
	}
}

// TestChatStream_TimedOutRace closes the connection as the idle timer fires.
func TestChatStream_TimedOutRace(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleRaceConn(t, conn)
		}
	}()

	c := NewLlamaClient("http://"+listener.Addr().String(), "test-model", "off", "")
	c.readIdleTimeout = 50 * time.Millisecond

	for range 20 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = c.ChatStream(ctx, testMessages(), nil, func(choice types.ChatChoice) {}, nil, nil)
		cancel()
	}

	listener.Close()
	wg.Wait()
}

func handleRaceConn(t *testing.T, conn net.Conn) {
	defer conn.Close()
	buf := make([]byte, 4096)
	_, _ = conn.Read(buf)
	conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\n"))
	conn.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"finish_reason\":null}]}\n\n"))
	time.Sleep(55 * time.Millisecond)
}

// TestChatStream_TimedOutDataRace exercises the timedOut race via io.Pipe.
func TestChatStream_TimedOutDataRace(t *testing.T) {
	for i := 0; i < 200; i++ {
		testTimedOutRaceOnce(t)
	}
}

func testTimedOutRaceOnce(_ *testing.T) {
	r, w := io.Pipe()
	go func() {
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"finish_reason\":null}]}\n\n"))
		time.Sleep(50 * time.Millisecond)
		w.Close()
	}()

	scanner := bufio.NewScanner(r)
	idleTimeout := 50 * time.Millisecond
	var timedOut atomic.Bool
	readTimer := time.AfterFunc(idleTimeout, func() {
		timedOut.Store(true)
		r.Close()
	})
	readTimer.Stop()

	for scanner.Scan() {
		_ = scanner.Text()
		readTimer.Stop()
		readTimer.Reset(idleTimeout)
	}
	readTimer.Stop()
	_ = timedOut.Load()
}

// TestChatStream_ProductionTimedOutRace calls the real ChatStream
// and triggers the timedOut race by closing the connection at the deadline.
func TestChatStream_ProductionTimedOutRace(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 8192)
				_, _ = c.Read(buf)
				c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nCache-Control: no-cache\r\n\r\n"))
				c.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"finish_reason\":null}]}\n\n"))
				time.Sleep(50 * time.Millisecond)
			}(conn)
		}
	}()

	c := NewLlamaClient("http://"+listener.Addr().String(), "test-model", "off", "")
	c.readIdleTimeout = 50 * time.Millisecond

	for range 200 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = c.ChatStream(ctx, testMessages(), nil, func(choice types.ChatChoice) {}, nil, nil)
		cancel()
	}

	listener.Close()
	wg.Wait()
}

// TestHasContent verifies the hasContent helper detects meaningful token deltas.
func TestHasContent(t *testing.T) {
	tests := []struct {
		name   string
		choice types.ChatChoice
		want   bool
	}{
		{
			name: "reasoning content",
			choice: types.ChatChoice{
				Delta: types.ChatChoiceDelta{
					ReasoningContent: ptrStr("thinking..."),
				},
			},
			want: true,
		},
		{
			name: "text content",
			choice: types.ChatChoice{
				Delta: types.ChatChoiceDelta{
					Content: ptrStr("hello"),
				},
			},
			want: true,
		},
		{
			name: "tool call with id and name",
			choice: types.ChatChoice{
				Delta: types.ChatChoiceDelta{
					ToolCalls: []types.ChatToolCall{
						{Index: 0, ID: "call_abc", Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{Name: "read_file"}, Type: "function"},
					},
				},
			},
			want: true,
		},
		{
			name: "finish reason only",
			choice: types.ChatChoice{
				Delta:        types.ChatChoiceDelta{},
				FinishReason: "stop",
			},
			want: false,
		},
		{
			name: "empty choice",
			choice: types.ChatChoice{
				Delta: types.ChatChoiceDelta{},
			},
			want: false,
		},
		{
			name: "empty reasoning content",
			choice: types.ChatChoice{
				Delta: types.ChatChoiceDelta{
					ReasoningContent: ptrStr(""),
				},
			},
			want: false,
		},
		{
			name: "tool call with only id",
			choice: types.ChatChoice{
				Delta: types.ChatChoiceDelta{
					ToolCalls: []types.ChatToolCall{
						{Index: 0, ID: "call_abc", Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{}, Type: "function"},
					},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasContent(tt.choice)
			if got != tt.want {
				t.Errorf("hasContent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func ptrStr(s string) *string {
	return &s
}

// TestNormalizeReasoning covers the two reasoning field names. Precedence,
// never concatenation: when both fields are set, reasoning_content wins
// (concatenating would duplicate the text).
func TestNormalizeReasoning(t *testing.T) {
	tests := []struct {
		name        string
		choice      types.ChatChoice
		wantDelta   string // expected *Delta.ReasoningContent ("" = nil)
		wantMessage string // expected *Message.ReasoningContent ("" = nil)
	}{
		{
			name:      "vLLM delta reasoning folds into reasoning_content",
			choice:    types.ChatChoice{Delta: types.ChatChoiceDelta{Reasoning: ptrStr("thinking")}},
			wantDelta: "thinking",
		},
		{
			name:      "llama.cpp reasoning_content untouched",
			choice:    types.ChatChoice{Delta: types.ChatChoiceDelta{ReasoningContent: ptrStr("thinking")}},
			wantDelta: "thinking",
		},
		{
			name: "both fields: reasoning_content wins, no concatenation",
			choice: types.ChatChoice{Delta: types.ChatChoiceDelta{
				ReasoningContent: ptrStr("canonical"),
				Reasoning:        ptrStr("duplicate"),
			}},
			wantDelta: "canonical",
		},
		{
			name:      "empty reasoning_content is replaced",
			choice:    types.ChatChoice{Delta: types.ChatChoiceDelta{ReasoningContent: ptrStr(""), Reasoning: ptrStr("thinking")}},
			wantDelta: "thinking",
		},
		{
			name:      "empty reasoning is not folded",
			choice:    types.ChatChoice{Delta: types.ChatChoiceDelta{Reasoning: ptrStr("")}},
			wantDelta: "",
		},
		{
			name:   "neither field set stays nil",
			choice: types.ChatChoice{Delta: types.ChatChoiceDelta{Content: ptrStr("hi")}},
		},
		{
			name:        "non-streaming message reasoning folds (vLLM)",
			choice:      types.ChatChoice{Message: types.ChatChoiceMessage{Reasoning: ptrStr("thought")}},
			wantMessage: "thought",
		},
		{
			name:        "non-streaming message reasoning_content folds (llama.cpp)",
			choice:      types.ChatChoice{Message: types.ChatChoiceMessage{ReasoningContent: ptrStr("thought")}},
			wantMessage: "thought",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			choice := tt.choice
			normalizeReasoning(&choice)

			got := ""
			if choice.Delta.ReasoningContent != nil {
				got = *choice.Delta.ReasoningContent
			} else if tt.wantDelta != "" {
				t.Fatalf("Delta.ReasoningContent = nil, want %q", tt.wantDelta)
			}
			if got != tt.wantDelta {
				t.Errorf("Delta.ReasoningContent = %q, want %q", got, tt.wantDelta)
			}

			gotMsg := ""
			if choice.Message.ReasoningContent != nil {
				gotMsg = *choice.Message.ReasoningContent
			} else if tt.wantMessage != "" {
				t.Fatalf("Message.ReasoningContent = nil, want %q", tt.wantMessage)
			}
			if gotMsg != tt.wantMessage {
				t.Errorf("Message.ReasoningContent = %q, want %q", gotMsg, tt.wantMessage)
			}
		})
	}

	t.Run("nil choice is a no-op", func(t *testing.T) {
		normalizeReasoning(nil)
	})
}

// TestChatStream_ReasoningFieldVariants checks reasoning reaches the callback
// in Delta.ReasoningContent for both field names, and that either one marks
// first-token arrival — vLLM's "reasoning" previously left firstTokenTime
// unset, deadening the wall-clock fallback.
func TestChatStream_ReasoningFieldVariants(t *testing.T) {
	tests := []struct {
		name  string
		delta string // raw delta JSON, as the server would send it
		want  string
	}{
		{"vLLM reasoning", `{"reasoning":"thinking hard"}`, "thinking hard"},
		{"llama.cpp reasoning_content", `{"reasoning_content":"thinking hard"}`, "thinking hard"},
		{"both sends canonical only", `{"reasoning_content":"canonical","reasoning":"duplicate"}`, "canonical"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				// Prompt-processing delay before the first (reasoning-only) token.
				time.Sleep(100 * time.Millisecond)
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":%s,\"finish_reason\":null}]}\n\n", tt.delta)
				// Usage without timings (vLLM-style): forces the wall-clock fallback.
				w.Write([]byte("data: {\"usage\":{\"prompt_tokens\":50,\"completion_tokens\":3,\"total_tokens\":53}}\n\n"))
				w.Write([]byte("data: [DONE]\n\n"))
			}))
			defer server.Close()

			c := NewLlamaClient(server.URL, "test-model", "off", "")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			var reasoning strings.Builder
			usage, err := c.ChatStream(ctx, testMessages(), nil, func(choice types.ChatChoice) {
				if choice.Delta.ReasoningContent != nil {
					reasoning.WriteString(*choice.Delta.ReasoningContent)
				}
			}, nil, nil)
			if err != nil {
				t.Fatalf("ChatStream error: %v", err)
			}

			if got := reasoning.String(); got != tt.want {
				t.Errorf("reasoning delivered to callback = %q, want %q", got, tt.want)
			}
			if usage == nil {
				t.Fatal("expected usage, got nil")
			}
			// A reasoning delta must count as the first token: PromptMs comes
			// from firstTokenTime-callStart, so ~0 means the frame looked empty.
			if usage.PromptMs < 80 {
				t.Errorf("prompt_ms = %d, expected ~100ms — reasoning delta did not register as first token", usage.PromptMs)
			}
			if usage.CompletionTokens != 3 {
				t.Errorf("completion_tokens = %d, want 3", usage.CompletionTokens)
			}
		})
	}
}

// TestChatStream_WallClockTimingFallback verifies that ChatStream calculates
// prompt_ms and predicted_ms from wall-clock when the provider doesn't supply Timings.
func TestChatStream_WallClockTimingFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("no flusher")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")

		// Simulate prompt processing delay (no Timings sent)
		time.Sleep(100 * time.Millisecond)

		// Send a few tokens with small delays
		for i := 0; i < 3; i++ {
			w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"finish_reason\":null}]}\n\n"))
			flusher.Flush()
			time.Sleep(20 * time.Millisecond)
		}

		// Send usage without timings (simulates a provider that doesn't send Timings)
		w.Write([]byte("data: {\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":3,\"total_tokens\":103}}\n\n"))
		flusher.Flush()

		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	c := NewLlamaClient(server.URL, "test-model", "off", "")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	usage, err := c.ChatStream(ctx, testMessages(), nil, func(choice types.ChatChoice) {}, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	if usage == nil {
		t.Fatal("expected usage, got nil")
	}

	// Token counts should come from Usage
	if usage.PromptTokens != 100 {
		t.Errorf("prompt_tokens = %d, want 100", usage.PromptTokens)
	}
	if usage.CompletionTokens != 3 {
		t.Errorf("completion_tokens = %d, want 3", usage.CompletionTokens)
	}

	// Timing should be calculated from wall-clock since provider didn't send Timings
	// Prompt time should be ~100ms (the initial delay before first token)
	if usage.PromptMs < 80 {
		t.Errorf("prompt_ms = %d, expected ~100ms (got too low)", usage.PromptMs)
	}
	if usage.PromptMs > 200 {
		t.Errorf("prompt_ms = %d, expected ~100ms (got too high)", usage.PromptMs)
	}

	// Gen time should be ~60ms (3 tokens * 20ms each)
	if usage.PredictedMs < 40 {
		t.Errorf("predicted_ms = %d, expected ~60ms (got too low)", usage.PredictedMs)
	}
	if usage.PredictedMs > 200 {
		t.Errorf("predicted_ms = %d, expected ~60ms (got too high)", usage.PredictedMs)
	}
}

// TestChatStream_TimingsFromProvider verifies that provider-supplied Timings
// take precedence over wall-clock estimation.
func TestChatStream_TimingsFromProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("no flusher")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")

		// Send a token with timings (llama.cpp format)
		w.Write([]byte("data: {\"timings\":{\"prompt_n\":100,\"prompt_ms\":50.0,\"predicted_n\":1,\"predicted_ms\":30.0,\"cache_n\":0},\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n"))
		flusher.Flush()

		// Send final usage with timings
		w.Write([]byte("data: {\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":1,\"total_tokens\":101},\"timings\":{\"prompt_n\":100,\"prompt_ms\":50.0,\"predicted_n\":1,\"predicted_ms\":30.0,\"cache_n\":0}}\n\n"))
		flusher.Flush()

		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	c := NewLlamaClient(server.URL, "test-model", "off", "")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	usage, err := c.ChatStream(ctx, testMessages(), nil, func(choice types.ChatChoice) {}, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	if usage == nil {
		t.Fatal("expected usage, got nil")
	}

	// Provider-supplied timings should be preserved (not overwritten by wall-clock)
	if usage.PromptMs != 50 {
		t.Errorf("prompt_ms = %d, want 50 (from provider Timings)", usage.PromptMs)
	}
	if usage.PredictedMs != 30 {
		t.Errorf("predicted_ms = %d, want 30 (from provider Timings)", usage.PredictedMs)
	}
}

// TestChatCompletion_WallClockTimingFallback verifies that ChatCompletion calculates
// prompt_ms and predicted_ms from wall-clock when the provider doesn't supply Timings.
func TestChatCompletion_WallClockTimingFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate processing delay
		time.Sleep(100 * time.Millisecond)

		w.Header().Set("Content-Type", "application/json")
		// Response with usage but NO timings (simulates provider without Timings)
		w.Write([]byte(`{
			"choices": [{"message": {"content": "hello world"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 50, "completion_tokens": 2, "total_tokens": 52}
		}`))
	}))
	defer server.Close()

	c := NewLlamaClient(server.URL, "test-model", "off", "")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	usage, content, err := c.ChatCompletion(ctx, testMessages(), nil)
	if err != nil {
		t.Fatalf("ChatCompletion error: %v", err)
	}
	if content != "hello world" {
		t.Errorf("content = %q, want %q", content, "hello world")
	}
	if usage == nil {
		t.Fatal("expected usage, got nil")
	}

	// Token counts should come from Usage
	if usage.PromptTokens != 50 {
		t.Errorf("prompt_tokens = %d, want 50", usage.PromptTokens)
	}
	if usage.CompletionTokens != 2 {
		t.Errorf("completion_tokens = %d, want 2", usage.CompletionTokens)
	}

	// Timing should be estimated from wall-clock (~100ms total)
	// With 5:1 ratio: prompt ~50/(50+2*5) * 100 = ~83ms, gen ~17ms
	if usage.PromptMs < 50 {
		t.Errorf("prompt_ms = %d, expected ~80ms (got too low)", usage.PromptMs)
	}
	if usage.PredictedMs < 5 {
		t.Errorf("predicted_ms = %d, expected ~15ms (got too low)", usage.PredictedMs)
	}
	// Total should be close to 100ms
	totalMs := usage.PromptMs + usage.PredictedMs
	if totalMs < 80 || totalMs > 200 {
		t.Errorf("total_ms = %d, expected ~100ms", totalMs)
	}
}

// --- empty-content Tests ---

// TestChatCompletion_EmptyContentIsNotAnError: a completion whose budget all went
// to reasoning returns empty text plus usage, not an error. vLLM sends
// content:null here, which used to fail every small-max_tokens utility call.
func TestChatCompletion_EmptyContentIsNotAnError(t *testing.T) {
	tests := []struct {
		name    string
		content string // raw JSON value for message.content
	}{
		{"vLLM null content", "null"},
		{"llama.cpp empty string", `""`},
		{"whitespace only", `"   "`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":` + tt.content +
					`,"reasoning":"User request: write a commit message"},"finish_reason":"length"}],` +
					"\"usage\":{\"prompt_tokens\":54,\"completion_tokens\":6,\"total_tokens\":60}}"))
			}))
			defer server.Close()

			c := NewLlamaClient(server.URL, "test-model", "off", "")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			usage, content, err := c.ChatCompletion(ctx, testMessages(), nil)
			if err != nil {
				t.Fatalf("ChatCompletion returned error %v, want empty content with no error", err)
			}
			if content != "" {
				t.Errorf("content = %q, want empty", content)
			}
			// The usage must still be accounted even though there is no text.
			if usage == nil {
				t.Fatal("usage = nil, want the reported token counts")
			}
			if usage.PromptTokens != 54 || usage.CompletionTokens != 6 {
				t.Errorf("usage = %+v, want prompt=54 completion=6", usage)
			}
		})
	}
}

// --- in-flight usage callback Tests ---

// TestChatStream_OnUsageCallback: in-flight usage snapshots reach the callback
// for both providers' frame shapes — the returned *ChatUsage only exists after
// the stream ends, too late to drive a live counter.
func TestChatStream_OnUsageCallback(t *testing.T) {
	t.Run("llama.cpp per-frame timings produce monotonic snapshots", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			// Three frames, each with a larger cumulative predicted_n, as
			// llama.cpp sends per-frame timings.
			w.Write([]byte(`data: {"choices":[{"delta":{"content":"a"}}],"timings":{"cache_n":0,"prompt_n":10,"prompt_ms":5,"predicted_n":1,"predicted_ms":1}}` + "\n\n"))
			w.Write([]byte(`data: {"choices":[{"delta":{"content":"b"}}],"timings":{"cache_n":0,"prompt_n":10,"prompt_ms":5,"predicted_n":2,"predicted_ms":2}}` + "\n\n"))
			w.Write([]byte(`data: {"choices":[{"delta":{"content":"c"}}],"timings":{"cache_n":0,"prompt_n":10,"prompt_ms":5,"predicted_n":3,"predicted_ms":3}}` + "\n\n"))
			w.Write([]byte("data: [DONE]\n\n"))
		}))
		defer server.Close()

		c := NewLlamaClient(server.URL, "test-model", "off", "")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var snaps []int
		_, err := c.ChatStream(ctx, testMessages(), nil, func(types.ChatChoice) {}, nil,
			func(u *types.ChatUsage) { snaps = append(snaps, u.CompletionTokens) })
		if err != nil {
			t.Fatalf("ChatStream error: %v", err)
		}
		if len(snaps) < 3 {
			t.Fatalf("onUsage fired %d times, want >=3 (one per timings frame)", len(snaps))
		}
		for i := 1; i < len(snaps); i++ {
			if snaps[i] < snaps[i-1] {
				t.Fatalf("completion tokens went backwards: %v", snaps)
			}
		}
		if snaps[len(snaps)-1] != 3 {
			t.Errorf("final snapshot = %d, want 3", snaps[len(snaps)-1])
		}
	})

	t.Run("vLLM empty-choices usage frame still reaches onUsage", func(t *testing.T) {
		// vLLM's usage arrives on a frame with "choices":[], which StreamCallback
		// can never deliver (cb fires per choice). onUsage must still see it.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte(`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"))
			w.Write([]byte(`data: {"choices":[],"usage":{"prompt_tokens":54,"completion_tokens":6,"total_tokens":60}}` + "\n\n"))
			w.Write([]byte("data: [DONE]\n\n"))
		}))
		defer server.Close()

		c := NewLlamaClient(server.URL, "test-model", "off", "")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var got *types.ChatUsage
		_, err := c.ChatStream(ctx, testMessages(), nil, func(types.ChatChoice) {}, nil,
			func(u *types.ChatUsage) { got = u })
		if err != nil {
			t.Fatalf("ChatStream error: %v", err)
		}
		if got == nil {
			t.Fatal("onUsage never fired for the empty-choices usage frame")
		}
		if got.PromptTokens != 54 || got.CompletionTokens != 6 {
			t.Errorf("onUsage usage = %+v, want prompt=54 completion=6", got)
		}
	})

	t.Run("snapshots are independent copies", func(t *testing.T) {
		// On the timings path the client mutates ONE usage struct in place across
		// frames, so a retained early pointer would otherwise observe the final
		// frame's values. onUsage must hand out copies.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte(`data: {"choices":[{"delta":{"content":"a"}}],"timings":{"cache_n":0,"prompt_n":1,"prompt_ms":1,"predicted_n":1,"predicted_ms":1}}` + "\n\n"))
			w.Write([]byte(`data: {"choices":[{"delta":{"content":"b"}}],"timings":{"cache_n":0,"prompt_n":1,"prompt_ms":1,"predicted_n":99,"predicted_ms":2}}` + "\n\n"))
			w.Write([]byte("data: [DONE]\n\n"))
		}))
		defer server.Close()

		c := NewLlamaClient(server.URL, "test-model", "off", "")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var first *types.ChatUsage
		_, err := c.ChatStream(ctx, testMessages(), nil, func(types.ChatChoice) {}, nil,
			func(u *types.ChatUsage) {
				if first == nil {
					first = u // retain the FIRST snapshot only
				}
			})
		if err != nil {
			t.Fatalf("ChatStream error: %v", err)
		}
		if first == nil {
			t.Fatal("onUsage never fired")
		}
		if first.CompletionTokens != 1 {
			t.Errorf("retained first snapshot reads %d, want 1 — onUsage handed out the live (in-place-mutated) pointer instead of a copy", first.CompletionTokens)
		}
	})

	t.Run("nil onUsage is accepted", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte(`data: {"choices":[{"delta":{"content":"hi"}}],"timings":{"prompt_n":1,"prompt_ms":1,"predicted_n":1,"predicted_ms":1}}` + "\n\n"))
			w.Write([]byte("data: [DONE]\n\n"))
		}))
		defer server.Close()

		c := NewLlamaClient(server.URL, "test-model", "off", "")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := c.ChatStream(ctx, testMessages(), nil, func(types.ChatChoice) {}, nil, nil); err != nil {
			t.Fatalf("ChatStream with nil onUsage error: %v", err)
		}
	})
}

// --- vLLM metrics Tests ---

// TestChatStream_VllmMetricsTiming verifies that vLLM's metrics block supplies
// prompt/gen times when no timings block exists (vLLM never sends timings).
func TestChatStream_VllmMetricsTiming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"hi"},"finish_reason":null}]}` + "\n\n"))
		// vLLM shape: usage + metrics on the final frame, no timings.
		w.Write([]byte(`data: {"choices":[],"usage":{"prompt_tokens":54,"completion_tokens":6,"total_tokens":60},"metrics":{"time_to_first_token_ms":132.7,"generation_time_ms":44.3,"tokens_per_second":43.4}}` + "\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	c := NewLlamaClient(server.URL, "test-model", "off", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	usage, err := c.ChatStream(ctx, testMessages(), nil, func(types.ChatChoice) {}, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	if usage == nil {
		t.Fatal("expected usage, got nil")
	}
	if usage.PromptMs != 132 {
		t.Errorf("prompt_ms = %d, want 132 (truncated from time_to_first_token_ms 132.7)", usage.PromptMs)
	}
	if usage.PredictedMs != 44 {
		t.Errorf("predicted_ms = %d, want 44 (truncated from generation_time_ms 44.3)", usage.PredictedMs)
	}
}

// TestChatStream_TimingsBeatVllmMetrics verifies that when a server sends both
// a timings block and a metrics block, timings wins.
func TestChatStream_TimingsBeatVllmMetrics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"hi"},"finish_reason":null}],"usage":{"prompt_tokens":100,"completion_tokens":3,"total_tokens":103},"timings":{"cache_n":0,"prompt_n":100,"prompt_ms":50.0,"predicted_n":3,"predicted_ms":30.0},"metrics":{"time_to_first_token_ms":999.0,"generation_time_ms":999.0}}` + "\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	c := NewLlamaClient(server.URL, "test-model", "off", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	usage, err := c.ChatStream(ctx, testMessages(), nil, func(types.ChatChoice) {}, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	if usage.PromptMs != 50 || usage.PredictedMs != 30 {
		t.Errorf("timings must win over metrics: got prompt_ms=%d predicted_ms=%d, want 50/30", usage.PromptMs, usage.PredictedMs)
	}
}

// TestChatCompletion_VllmMetricsTiming: metrics also supply timing on the
// non-streaming path, which all utility calls use.
func TestChatCompletion_VllmMetricsTiming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// vLLM non-streaming shape: usage + metrics, prompt_tokens_details null.
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":54,"completion_tokens":6,"total_tokens":60,"prompt_tokens_details":null},"metrics":{"time_to_first_token_ms":93.9,"generation_time_ms":43.9,"tokens_per_second":43.4}}`))
	}))
	defer server.Close()

	c := NewLlamaClient(server.URL, "test-model", "off", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	usage, _, err := c.ChatCompletion(ctx, testMessages(), nil)
	if err != nil {
		t.Fatalf("ChatCompletion error: %v", err)
	}
	if usage.PromptMs != 93 || usage.PredictedMs != 43 {
		t.Errorf("prompt_ms=%d predicted_ms=%d, want 93/43 from metrics", usage.PromptMs, usage.PredictedMs)
	}
}

// --- LlamaError Tests ---

func TestLlamaError_ErrorText(t *testing.T) {
	e := &LlamaError{StatusCode: 429, Body: `{"error":"Rate limit exceeded"}`}
	got := e.Error()
	want := `llama error 429: {"error":"Rate limit exceeded"}`
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestLlamaError_IsRateLimit(t *testing.T) {
	if !(&LlamaError{StatusCode: 429}).IsRateLimit() {
		t.Error("IsRateLimit() = false for 429, want true")
	}
	if (&LlamaError{StatusCode: 500}).IsRateLimit() {
		t.Error("IsRateLimit() = true for 500, want false")
	}
	if (&LlamaError{StatusCode: 200}).IsRateLimit() {
		t.Error("IsRateLimit() = true for 200, want false")
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  time.Duration
	}{
		{"empty", "", 0},
		{"seconds", "120", 120 * time.Second},
		{"seconds with spaces", "  30  ", 30 * time.Second},
		{"zero", "0", 0},
		{"http date", time.Now().Add(5 * time.Minute).UTC().Format(http.TimeFormat), 5 * time.Minute},
		{"garbage", "not-a-duration", 0},
		{"negative", "-10", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRetryAfter(tc.input)
			// Allow small tolerance for the HTTP-date case (time.Until).
			if diff := got - tc.want; diff < -2*time.Second || diff > 2*time.Second {
				t.Errorf("parseRetryAfter(%q) = %v, want ~%v", tc.input, got, tc.want)
			}
		})
	}
}

func TestNewLlamaError_ParsesRetryAfter(t *testing.T) {
	hdr := http.Header{}
	hdr.Set("Retry-After", "45")
	e := newLlamaError(429, hdr, "rate limited")
	if e.StatusCode != 429 {
		t.Errorf("StatusCode = %d, want 429", e.StatusCode)
	}
	if e.RetryAfter != 45*time.Second {
		t.Errorf("RetryAfter = %v, want 45s", e.RetryAfter)
	}
	if !e.IsRateLimit() {
		t.Error("expected IsRateLimit() = true")
	}
}

func TestChatStream_ReturnsLlamaError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "15")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"Rate limit exceeded"}`))
	}))
	defer server.Close()

	c := NewLlamaClient(server.URL, "test-model", "off", "")
	_, err := c.ChatStream(context.Background(), testMessages(), nil, func(types.ChatChoice) {}, nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var le *LlamaError
	if !errors.As(err, &le) {
		t.Fatalf("expected *LlamaError, got %T: %v", err, err)
	}
	if !le.IsRateLimit() {
		t.Error("expected IsRateLimit() = true for 429")
	}
	if le.RetryAfter != 15*time.Second {
		t.Errorf("RetryAfter = %v, want 15s", le.RetryAfter)
	}
	if !strings.Contains(err.Error(), "llama error 429") {
		t.Errorf("Error() = %q, want to contain llama error 429", err.Error())
	}
}

// --- User message guard tests ---

// testMessages returns a minimal message list that satisfies the user message guard.
func testMessages() []types.ChatMessage {
	return []types.ChatMessage{{Role: "user", Content: "hi"}}
}

// reasoningParamsCase: expected reasoning request fields for one effort level.
type reasoningParamsCase struct {
	effort       string
	budgetTokens float64
	budgetSent   bool
	controlSent  bool
	effortSent   string // "" = key absent
	enableThink  bool
}

var reasoningParamCases = []reasoningParamsCase{
	{effort: "default", budgetSent: false, controlSent: false, effortSent: "", enableThink: false},
	{effort: "none", budgetSent: true, budgetTokens: 0, controlSent: false, effortSent: "", enableThink: true},
	// "off" is the legacy name for "none".
	{effort: "off", budgetSent: true, budgetTokens: 0, controlSent: false, effortSent: "", enableThink: true},
	{effort: "low", budgetSent: true, budgetTokens: 512, controlSent: true, effortSent: "low"},
	{effort: "medium", budgetSent: true, budgetTokens: 2048, controlSent: true, effortSent: "medium"},
	{effort: "high", budgetSent: true, budgetTokens: 8192, controlSent: true, effortSent: "high"},
	// "max" is no longer a known level — it passes through verbatim like any custom value.
	{effort: "max", budgetSent: false, controlSent: true, effortSent: "max"},
	// Custom values pass through verbatim (no thinking budget).
	{effort: "xhigh", budgetSent: false, controlSent: true, effortSent: "xhigh"},
	{effort: "4096", budgetSent: false, controlSent: true, effortSent: "4096"},
	{effort: "  xhigh  ", budgetSent: false, controlSent: true, effortSent: "xhigh"},
}

func assertReasoningParams(t *testing.T, captured map[string]any, tc reasoningParamsCase) {
	t.Helper()

	if budget, ok := captured["thinking_budget_tokens"]; !ok {
		if tc.budgetSent {
			t.Errorf("effort=%s: thinking_budget_tokens missing, want %v", tc.effort, tc.budgetTokens)
		}
	} else {
		if !tc.budgetSent {
			t.Errorf("effort=%s: thinking_budget_tokens = %v, want absent", tc.effort, budget)
		} else if got, isNum := budget.(float64); !isNum || got != tc.budgetTokens {
			t.Errorf("effort=%s: thinking_budget_tokens = %v, want %v", tc.effort, budget, tc.budgetTokens)
		}
	}

	if control, ok := captured["reasoning_control"]; !ok {
		if tc.controlSent {
			t.Errorf("effort=%s: reasoning_control missing, want true", tc.effort)
		}
	} else if !tc.controlSent || control != true {
		t.Errorf("effort=%s: reasoning_control = %v, want %v", tc.effort, control, tc.controlSent)
	}

	if effort, ok := captured["reasoning_effort"]; !ok {
		if tc.effortSent != "" {
			t.Errorf("effort=%s: reasoning_effort missing, want %q", tc.effort, tc.effortSent)
		}
	} else if tc.effortSent == "" || effort != tc.effortSent {
		t.Errorf("effort=%s: reasoning_effort = %v, want %q", tc.effort, effort, tc.effortSent)
	}

	if tc.enableThink {
		kwargs, ok := captured["chat_template_kwargs"].(map[string]any)
		if !ok {
			t.Errorf("effort=%s: chat_template_kwargs missing", tc.effort)
		} else if kwargs["enable_thinking"] != false {
			t.Errorf("effort=%s: enable_thinking = %v, want false", tc.effort, kwargs["enable_thinking"])
		}
	} else if _, ok := captured["chat_template_kwargs"]; ok {
		t.Errorf("effort=%s: chat_template_kwargs present, want absent", tc.effort)
	}
}

// TestChatStream_ReasoningParams: reasoning request fields per effort level (streaming).
func TestChatStream_ReasoningParams(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	for _, tc := range reasoningParamCases {
		t.Run(tc.effort, func(t *testing.T) {
			captured = nil
			c := NewLlamaClient(server.URL, "test-model", tc.effort, "")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := c.ChatStream(ctx, testMessages(), nil, func(choice types.ChatChoice) {}, nil, nil); err != nil {
				t.Fatalf("ChatStream error: %v", err)
			}
			if captured == nil {
				t.Fatal("no request captured")
			}
			assertReasoningParams(t, captured, tc)
		})
	}
}

// TestChatStream_StreamOptionsRequestsUsage verifies the streaming request asks
// for the usage block. vLLM omits usage from streamed responses unless
// stream_options.include_usage is set, which silently zeroed all token stats,
// cost and timings for the turn.
func TestChatStream_StreamOptionsRequestsUsage(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	c := NewLlamaClient(server.URL, "test-model", "off", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.ChatStream(ctx, testMessages(), nil, func(types.ChatChoice) {}, nil, nil); err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}

	opts, ok := captured["stream_options"].(map[string]any)
	if !ok {
		t.Fatalf("stream_options = %#v, want an object", captured["stream_options"])
	}
	if opts["include_usage"] != true {
		t.Errorf("stream_options.include_usage = %#v, want true", opts["include_usage"])
	}
}

// TestChatStream_UsageFrameWithEmptyChoices verifies the final usage frame that
// include_usage produces (vLLM sends "choices":[] alongside usage) is consumed
// without invoking the token callback, and that its counts are returned.
func TestChatStream_UsageFrameWithEmptyChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n"))
		// vLLM's shape: empty choices array + usage on the last data frame.
		w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":54,\"completion_tokens\":6,\"total_tokens\":60,\"prompt_tokens_details\":{\"cached_tokens\":40}},\"metrics\":{\"time_to_first_token_ms\":132.7,\"generation_time_ms\":44.3}}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	c := NewLlamaClient(server.URL, "test-model", "off", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var callbackCalls int
	usage, err := c.ChatStream(ctx, testMessages(), nil, func(types.ChatChoice) { callbackCalls++ }, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	if usage == nil {
		t.Fatal("expected usage, got nil")
	}
	if usage.PromptTokens != 54 || usage.CompletionTokens != 6 {
		t.Errorf("usage = %+v, want prompt=54 completion=6", usage)
	}
	if usage.PromptTokensDetails.CachedTokens != 40 {
		t.Errorf("cached_tokens = %d, want 40", usage.PromptTokensDetails.CachedTokens)
	}
	if callbackCalls != 1 {
		t.Errorf("token callback calls = %d, want 1 (the empty-choices usage frame must not invoke it)", callbackCalls)
	}
}

// TestChatCompletion_ReasoningParams: same, on the non-streaming path.
func TestChatCompletion_ReasoningParams(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	for _, tc := range reasoningParamCases {
		t.Run(tc.effort, func(t *testing.T) {
			captured = nil
			c := NewLlamaClient(server.URL, "test-model", tc.effort, "")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, _, err := c.ChatCompletion(ctx, testMessages(), nil); err != nil {
				t.Fatalf("ChatCompletion error: %v", err)
			}
			if captured == nil {
				t.Fatal("no request captured")
			}
			assertReasoningParams(t, captured, tc)
		})
	}
}

// hitCountingServer counts requests so guard tests can assert the server is never hit.
func hitCountingServer(hits *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
}

func TestChatCompletion_NoUserMessage(t *testing.T) {
	var hits atomic.Int32
	server := hitCountingServer(&hits)
	defer server.Close()

	c := NewLlamaClient(server.URL, "test-model", "off", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// System-only messages must be rejected before any request is sent.
	_, _, err := c.ChatCompletion(ctx, []types.ChatMessage{{Role: "system", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error for system-only messages, got nil")
	}
	if !strings.Contains(err.Error(), "no user message") {
		t.Fatalf("unexpected error: %v", err)
	}

	// Empty/nil messages must also be rejected.
	if _, _, err := c.ChatCompletion(ctx, nil, nil); err == nil {
		t.Fatal("expected error for empty messages, got nil")
	}

	if got := hits.Load(); got != 0 {
		t.Errorf("server hit %d times, want 0", got)
	}
}

func TestChatStream_NoUserMessage(t *testing.T) {
	var hits atomic.Int32
	server := hitCountingServer(&hits)
	defer server.Close()

	c := NewLlamaClient(server.URL, "test-model", "off", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.ChatStream(ctx, []types.ChatMessage{{Role: "system", Content: "hi"}}, nil, func(types.ChatChoice) {}, nil, nil)
	if err == nil {
		t.Fatal("expected error for system-only messages, got nil")
	}
	if !strings.Contains(err.Error(), "no user message") {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := c.ChatStream(ctx, nil, nil, func(types.ChatChoice) {}, nil, nil); err == nil {
		t.Fatal("expected error for empty messages, got nil")
	}

	if got := hits.Load(); got != 0 {
		t.Errorf("server hit %d times, want 0", got)
	}
}

// A stream that ends without its terminal frame came from a provider that died, not one that
// finished. Reported as a successful empty completion, it is retried by nobody.
func TestChatStream_AbortVsCompletion(t *testing.T) {
	tests := []struct {
		name    string
		frames  []string // SSE payload of each frame, without the "data: " prefix
		wantErr bool
	}{
		{
			name:    "headers then nothing",
			frames:  nil,
			wantErr: true,
		},
		{
			name: "tokens then a quiet death",
			frames: []string{
				`{"choices":[{"delta":{"content":"half a sent"},"finish_reason":null}]}`,
				`{"choices":[{"delta":{"content":"ence"}}]}`,
			},
			wantErr: true,
		},
		{
			name: "DONE with no finish_reason",
			frames: []string{
				`{"choices":[{"delta":{"content":"done"},"finish_reason":null}]}`,
				`[DONE]`,
			},
			wantErr: false,
		},
		{
			name: "finish_reason with no DONE",
			frames: []string{
				`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, frame := range tt.frames {
					fmt.Fprintf(w, "data: %s\n\n", frame)
				}
			}))
			defer server.Close()

			c := NewLlamaClient(server.URL, "test-model", "off", "")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			_, err := c.ChatStream(ctx, testMessages(), nil, func(types.ChatChoice) {}, nil, nil)
			if tt.wantErr && !errors.Is(err, ErrStreamAborted) {
				t.Errorf("ChatStream error = %v, want ErrStreamAborted", err)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("ChatStream error = %v, want a completed stream", err)
			}
		})
	}
}

// A connection torn down mid-response stays an error of its own: the transport said something more
// useful than ErrStreamAborted can.
func TestChatStream_HardAbortMidStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("test server cannot hijack")
			return
		}
		conn, bw, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		bw.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
		bw.WriteString("27\r\ndata: {\"choices\":[{\"delta\":{\"content\":\"half\"}}]}\n\n\r\n")
		_ = bw.Flush()
		conn.Close()
	}))
	defer server.Close()

	c := NewLlamaClient(server.URL, "test-model", "off", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := c.ChatStream(ctx, testMessages(), nil, func(types.ChatChoice) {}, nil, nil); err == nil {
		t.Fatal("a connection closed mid-response returned no error")
	}
}
