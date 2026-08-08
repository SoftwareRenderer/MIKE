package llama

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kanban/backend/types"
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
	c.SetReadIdleTimeout(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.ChatStream(ctx, nil, nil, func(choice types.ChatChoice) {}, nil)
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
	c.SetReadIdleTimeout(50 * time.Millisecond)

	for range 20 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = c.ChatStream(ctx, nil, nil, func(choice types.ChatChoice) {}, nil)
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
	c.SetReadIdleTimeout(50 * time.Millisecond)

	for range 200 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = c.ChatStream(ctx, nil, nil, func(choice types.ChatChoice) {}, nil)
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

	usage, err := c.ChatStream(ctx, nil, nil, func(choice types.ChatChoice) {}, nil)
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

	usage, err := c.ChatStream(ctx, nil, nil, func(choice types.ChatChoice) {}, nil)
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

	usage, content, err := c.ChatCompletion(ctx, nil, nil)
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
	_, err := c.ChatStream(context.Background(), nil, nil, func(types.ChatChoice) {}, nil)
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
