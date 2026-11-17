package llama

import (
	"context"
	"encoding/json/v2"
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

// TestChatStream_SilenceCeilingEndsStream a stream that goes quiet past the ceiling the caller set
// ends with ErrStreamSilent naming the silence. It must never read as "deadline exceeded": that
// borrowed error sent people looking for a timeout the product does not have.
func TestChatStream_SilenceCeilingEndsStream(t *testing.T) {
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
	c.waitTick = 10 * time.Millisecond
	c.SetSilencePolicy(100*time.Millisecond, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.ChatStream(ctx, testMessages(), nil, func(choice types.ChatChoice) {}, nil, nil)
	var silent *ErrStreamSilent
	if !errors.As(err, &silent) {
		t.Fatalf("expected ErrStreamSilent, got: %v", err)
	}
	if silent.Phase != StreamPhaseMidResponse {
		t.Errorf("phase = %q, want %q: a token had arrived", silent.Phase, StreamPhaseMidResponse)
	}
	if silent.Silent < 100*time.Millisecond {
		t.Errorf("silent = %v, want at least the 100ms ceiling", silent.Silent)
	}
	if strings.Contains(err.Error(), "deadline") {
		t.Errorf("the sentence still blames a deadline: %v", err)
	}
}

// TestChatStream_SilenceBeforeFirstTokenIsWaitedOut is the case a 74k prompt produces: the provider
// accepts the request and says nothing while it works. Silence is announced and the call is ended
// by the caller's context, never by the silence itself.
func TestChatStream_SilenceBeforeFirstTokenIsWaitedOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	c := NewLlamaClient(server.URL, "test-model", "off", "")
	c.noticeFirstTok = 30 * time.Millisecond
	c.waitTick = 10 * time.Millisecond
	var phases atomic.Value
	c.SetSilencePolicy(0, func(silent time.Duration, phase string) {
		phases.Store(phase)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	_, err := c.ChatStream(ctx, testMessages(), nil, func(types.ChatChoice) {}, nil, nil)
	if got, _ := phases.Load().(string); got != StreamPhaseFirstToken {
		t.Errorf("announced phase = %q, want %q", got, StreamPhaseFirstToken)
	}
	var silent *ErrStreamSilent
	if errors.As(err, &silent) {
		t.Errorf("a caller-less ceiling ended the call: %v", err)
	}
}

// TestChatStream_SilenceThatResumesCompletes: a provider that goes quiet and then finishes is a
// successful response, and the notice that fired while it was quiet is not an error.
func TestChatStream_SilenceThatResumesCompletes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("no flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		f.Flush()
		time.Sleep(90 * time.Millisecond) // longer than the notice floor, shorter than any ceiling
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n\ndata: [DONE]\n\n")
		f.Flush()
	}))
	defer server.Close()

	c := NewLlamaClient(server.URL, "test-model", "off", "")
	c.noticeMidResp = 30 * time.Millisecond
	c.waitTick = 10 * time.Millisecond
	var notices atomic.Int32 // the notice hears on the watcher goroutine, not on this one
	c.SetSilencePolicy(0, func(time.Duration, string) { notices.Add(1) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := c.ChatStream(ctx, testMessages(), nil, func(types.ChatChoice) {}, nil, nil); err != nil {
		t.Fatalf("a resumed stream must complete, got: %v", err)
	}
	if notices.Load() == 0 {
		t.Error("the silent stretch was never announced")
	}
}

// TestChatStream_TimedOutRace closes the connection as the idle timer fires. This is the whole
// concurrency surface of `backend/llama` that the gate sees: the repetition here is not what proves the
// race, the detector is, so the package belongs in RACE_PKGS and a small count is enough here.
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
	c.SetSilencePolicy(50*time.Millisecond, nil)

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
	conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nCache-Control: no-cache\r\n\r\n"))
	conn.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"finish_reason\":null}]}\n\n"))
	time.Sleep(55 * time.Millisecond)
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

// TestChatStream_WallClockTimingFallback verifies that ChatStream splits prompt_ms and predicted_ms
// from wall-clock when the provider supplies no Timings, and that a delta carrying only reasoning is a
// token: either field name must reach the callback in Delta.ReasoningContent and start the clock, or a
// thinking model reads as a turn that never began.
func TestChatStream_WallClockTimingFallback(t *testing.T) {
	tests := []struct {
		name  string
		delta string // raw delta JSON, as the server would send it
		want  string // what one delta must deliver to the callback
	}{
		{"content delta", `{"content":"x"}`, "x"},
		{"vLLM reasoning", `{"reasoning":"thinking hard"}`, "thinking hard"},
		{"llama.cpp reasoning_content", `{"reasoning_content":"thinking hard"}`, "thinking hard"},
		{"both sends canonical only", `{"reasoning_content":"canonical","reasoning":"duplicate"}`, "canonical"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				flusher, ok := w.(http.Flusher)
				if !ok {
					t.Fatal("no flusher")
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")

				// Prompt processing, with no Timings block: the wall-clock split is the only source.
				time.Sleep(100 * time.Millisecond)
				for i := 0; i < 3; i++ {
					fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":%s,\"finish_reason\":null}]}\n\n", tt.delta)
					flusher.Flush()
					time.Sleep(20 * time.Millisecond)
				}
				w.Write([]byte(`data: {"usage":{"prompt_tokens":100,"completion_tokens":3,"total_tokens":103}}` + "\n\n"))
				flusher.Flush()
				w.Write([]byte("data: [DONE]\n\n"))
				flusher.Flush()
				<-r.Context().Done()
			}))
			defer server.Close()

			c := NewLlamaClient(server.URL, "test-model", "off", "")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			var delivered strings.Builder
			usage, err := c.ChatStream(ctx, testMessages(), nil, func(choice types.ChatChoice) {
				if choice.Delta.Content != nil {
					delivered.WriteString(*choice.Delta.Content)
				}
				if choice.Delta.ReasoningContent != nil {
					delivered.WriteString(*choice.Delta.ReasoningContent)
				}
			}, nil, nil)
			if err != nil {
				t.Fatalf("ChatStream error: %v", err)
			}
			if got := delivered.String(); got != strings.Repeat(tt.want, 3) {
				t.Errorf("callback received %q, want %q three times", got, tt.want)
			}
			if usage == nil {
				t.Fatal("expected usage, got nil")
			}
			if usage.PromptTokens != 100 || usage.CompletionTokens != 3 {
				t.Errorf("usage = %d prompt / %d completion, want 100/3 from the usage frame",
					usage.PromptTokens, usage.CompletionTokens)
			}

			// Prompt time is firstTokenTime-callStart, so ~0 means a frame looked empty to the client.
			if usage.PromptMs < 80 || usage.PromptMs > 200 {
				t.Errorf("prompt_ms = %d, expected ~100ms (the delay before the first token)", usage.PromptMs)
			}
			// Gen time is streamEnd-firstTokenTime: ~60ms for three tokens paced at 20ms.
			if usage.PredictedMs < 40 || usage.PredictedMs > 200 {
				t.Errorf("predicted_ms = %d, expected ~60ms", usage.PredictedMs)
			}
		})
	}
}

// TestChatStream_TimingsComeFromWhatTheProviderSends pins the precedence between the sources of a
// turn's timings: a provider's own Timings block is used verbatim, vLLM's metrics block fills what no
// Timings sent, and a server that sends both is read by its Timings. Read the wrong one and every card
// prices the turn with somebody else's clock.
func TestChatStream_TimingsComeFromWhatTheProviderSends(t *testing.T) {
	for _, tc := range []struct {
		name          string
		frames        []string // SSE payloads, each without its "data: " prefix
		wantPromptMs  int64
		wantPredicted int64
	}{
		{
			name: "the provider's Timings block is used verbatim",
			frames: []string{
				`{"timings":{"prompt_n":100,"prompt_ms":50.0,"predicted_n":1,"predicted_ms":30.0,"cache_n":0},"choices":[{"delta":{"content":"hello"},"finish_reason":null}]}`,
				`{"usage":{"prompt_tokens":100,"completion_tokens":1,"total_tokens":101},"timings":{"prompt_n":100,"prompt_ms":50.0,"predicted_n":1,"predicted_ms":30.0,"cache_n":0}}`,
			},
			wantPromptMs: 50, wantPredicted: 30,
		},
		{
			name: "vLLM's metrics block supplies what no Timings sent",
			frames: []string{
				`{"choices":[{"delta":{"content":"hi"},"finish_reason":null}]}`,
				`{"choices":[],"usage":{"prompt_tokens":54,"completion_tokens":6,"total_tokens":60},"metrics":{"time_to_first_token_ms":132.7,"generation_time_ms":44.3,"tokens_per_second":43.4}}`,
			},
			wantPromptMs: 132, wantPredicted: 44,
		},
		{
			name: "Timings win when a server sends both",
			frames: []string{
				`{"choices":[{"delta":{"content":"hi"},"finish_reason":null}],"usage":{"prompt_tokens":100,"completion_tokens":3,"total_tokens":103},"timings":{"cache_n":0,"prompt_n":100,"prompt_ms":50.0,"predicted_n":3,"predicted_ms":30.0},"metrics":{"time_to_first_token_ms":999.0,"generation_time_ms":999.0}}`,
			},
			wantPromptMs: 50, wantPredicted: 30,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, frame := range tc.frames {
					w.Write([]byte("data: " + frame + "\n\n"))
					w.(http.Flusher).Flush()
				}
				w.Write([]byte("data: [DONE]\n\n"))
				w.(http.Flusher).Flush()
				<-r.Context().Done()
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
			if usage.PromptMs != tc.wantPromptMs {
				t.Errorf("prompt_ms = %d, want %d", usage.PromptMs, tc.wantPromptMs)
			}
			if usage.PredictedMs != tc.wantPredicted {
				t.Errorf("predicted_ms = %d, want %d", usage.PredictedMs, tc.wantPredicted)
			}
		})
	}
}

// TestChatCompletion_TimingsComeFromWhatTheProviderSends is the same precedence on the non-streaming
// path every utility call takes: vLLM's metrics block fills an empty Timings, and with neither source
// the wall clock is split by the token weights — a generated token costs about five times a prompted
// one, and a card that reads 0 ms for both claims the model answered instantly.
func TestChatCompletion_TimingsComeFromWhatTheProviderSends(t *testing.T) {
	for _, tc := range []struct {
		name            string
		payload         string
		delay           time.Duration
		content         string
		wantPromptTok   int
		wantCompletion  int
		wantPromptMs    int64 // 0 = no provider timing: the wall-clock split is the source
		wantPredictedMs int64
	}{
		{
			name: "vLLM's metrics block supplies what no Timings sent",
			payload: `{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],` +
				`"usage":{"prompt_tokens":54,"completion_tokens":6,"total_tokens":60,"prompt_tokens_details":null},` +
				`"metrics":{"time_to_first_token_ms":93.9,"generation_time_ms":43.9,"tokens_per_second":43.4}}`,
			content: "hi", wantPromptTok: 54, wantCompletion: 6, wantPromptMs: 93, wantPredictedMs: 43,
		},
		{
			name: "the wall clock is split by the token weights",
			payload: `{"choices":[{"message":{"content":"hello world"},"finish_reason":"stop"}],` +
				`"usage":{"prompt_tokens":50,"completion_tokens":2,"total_tokens":52}}`,
			delay: 100 * time.Millisecond, content: "hello world", wantPromptTok: 50, wantCompletion: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.delay > 0 {
					time.Sleep(tc.delay)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.payload))
			}))
			defer server.Close()

			c := NewLlamaClient(server.URL, "test-model", "off", "")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			usage, content, err := c.ChatCompletion(ctx, testMessages(), nil)
			if err != nil {
				t.Fatalf("ChatCompletion error: %v", err)
			}
			if content != tc.content {
				t.Errorf("content = %q, want %q", content, tc.content)
			}
			if usage == nil {
				t.Fatal("expected usage, got nil")
			}
			if usage.PromptTokens != tc.wantPromptTok || usage.CompletionTokens != tc.wantCompletion {
				t.Errorf("usage = %d prompt / %d completion, want %d/%d from the usage block",
					usage.PromptTokens, usage.CompletionTokens, tc.wantPromptTok, tc.wantCompletion)
			}
			if tc.wantPromptMs == 0 {
				// ~100ms of wall clock split 50:(2*5) between prompt and generation.
				if usage.PromptMs < 50 {
					t.Errorf("prompt_ms = %d, expected ~80ms (got too low)", usage.PromptMs)
				}
				if usage.PredictedMs < 5 {
					t.Errorf("predicted_ms = %d, expected ~15ms (got too low)", usage.PredictedMs)
				}
				if total := usage.PromptMs + usage.PredictedMs; total < 80 || total > 200 {
					t.Errorf("total_ms = %d, expected ~100ms", total)
				}
				return
			}
			if usage.PromptMs != tc.wantPromptMs || usage.PredictedMs != tc.wantPredictedMs {
				t.Errorf("prompt_ms=%d predicted_ms=%d, want %d/%d from metrics",
					usage.PromptMs, usage.PredictedMs, tc.wantPromptMs, tc.wantPredictedMs)
			}
		})
	}
}

// --- empty-content Tests ---

// TestChatCompletion_EmptyContentIsNotAnError: a completion whose budget all went to reasoning
// returns empty text plus usage, not an error. vLLM sends content:null here.
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

	t.Run("the empty-choices usage frame is usage, not a token", func(t *testing.T) {
		// include_usage makes vLLM send its usage on a frame with "choices":[], which
		// StreamCallback can never deliver (cb fires per choice): onUsage must still see
		// the counts, and the token callback must not fire for a frame that carries no token.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte(`data: {"choices":[{"delta":{"content":"hi"},"finish_reason":null}]}` + "\n\n"))
			w.Write([]byte(`data: {"choices":[],"usage":{"prompt_tokens":54,"completion_tokens":6,"total_tokens":60,"prompt_tokens_details":{"cached_tokens":40}}}` + "\n\n"))
			w.Write([]byte("data: [DONE]\n\n"))
		}))
		defer server.Close()

		c := NewLlamaClient(server.URL, "test-model", "off", "")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var got *types.ChatUsage
		var tokens int
		usage, err := c.ChatStream(ctx, testMessages(), nil, func(types.ChatChoice) { tokens++ }, nil,
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
		if usage == nil {
			t.Fatal("expected usage, got nil")
		}
		if usage.PromptTokens != 54 || usage.CompletionTokens != 6 {
			t.Errorf("returned usage = %+v, want prompt=54 completion=6", usage)
		}
		if usage.PromptTokensDetails.CachedTokens != 40 {
			t.Errorf("cached_tokens = %d, want 40", usage.PromptTokensDetails.CachedTokens)
		}
		if tokens != 1 {
			t.Errorf("token callback calls = %d, want 1 (the empty-choices usage frame must not invoke it)", tokens)
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

// --- LlamaError Tests ---

// TestLlamaError_StatusCodeClasses pins which refusals end the run at once and which pay a rate-limit
// backoff. A 4xx a later call could get an answer from must stay retryable, or a rate-limited provider
// kills the task; a malformed request retried is the same refusal paid again.
func TestLlamaError_StatusCodeClasses(t *testing.T) {
	for _, tc := range []struct {
		code        int
		rejected    bool
		rateLimited bool
	}{
		{code: 200},
		{code: 400, rejected: true},
		{code: 401, rejected: true},
		{code: 403, rejected: true},
		{code: 404, rejected: true},
		{code: 408}, // a timeout a later call can answer
		{code: 413, rejected: true},
		{code: 422, rejected: true},
		{code: 429, rateLimited: true}, // retry, with the server's own wait
		{code: 500},
		{code: 502},
		{code: 503},
	} {
		e := &LlamaError{StatusCode: tc.code}
		if got := e.IsRequestRejected(); got != tc.rejected {
			t.Errorf("IsRequestRejected(%d) = %v, want %v", tc.code, got, tc.rejected)
		}
		if got := e.IsRateLimit(); got != tc.rateLimited {
			t.Errorf("IsRateLimit(%d) = %v, want %v", tc.code, got, tc.rateLimited)
		}
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

// TestRefusedResponsesComeBackAsLlamaErrors: the retry loop decides by the status code and the
// Retry-After header, so a refusal has to survive the client as those two plus MIKE's own sentence —
// both on the path a task runs and on the one every utility call takes.
func TestRefusedResponsesComeBackAsLlamaErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "15")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"Rate limit exceeded"}`))
	}))
	defer server.Close()

	const wantText = `llama error 429: {"error":"Rate limit exceeded"}`

	t.Run("stream", func(t *testing.T) {
		c := NewLlamaClient(server.URL, "test-model", "off", "")
		_, err := c.ChatStream(context.Background(), testMessages(), nil, func(types.ChatChoice) {}, nil, nil)
		assertRateLimited(t, err, wantText)
	})

	t.Run("completion", func(t *testing.T) {
		c := NewLlamaClient(server.URL, "test-model", "off", "")
		_, _, err := c.ChatCompletion(context.Background(), testMessages(), nil)
		assertRateLimited(t, err, wantText)
	})
}

func assertRateLimited(t *testing.T, err error, wantText string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var le *LlamaError
	if !errors.As(err, &le) {
		t.Fatalf("expected *LlamaError, got %T: %v", err, err)
	}
	if le.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want 429", le.StatusCode)
	}
	if !le.IsRateLimit() {
		t.Error("expected IsRateLimit() = true for 429")
	}
	if le.RetryAfter != 15*time.Second {
		t.Errorf("RetryAfter = %v, want 15s from the header", le.RetryAfter)
	}
	if le.Error() != wantText {
		t.Errorf("Error() = %q, want %q", le.Error(), wantText)
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

// TestNoUserMessageIsRefusedBeforeTheSocket: a chat template errors on a request with no user
// message, so each path refuses it itself — and a request that reaches the provider is a turn somebody
// pays for.
func TestNoUserMessageIsRefusedBeforeTheSocket(t *testing.T) {
	for _, path := range []string{"completion", "stream"} {
		t.Run(path, func(t *testing.T) {
			var hits atomic.Int32
			server := hitCountingServer(&hits)
			defer server.Close()

			c := NewLlamaClient(server.URL, "test-model", "off", "")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			send := func(messages []types.ChatMessage) error {
				if path == "stream" {
					_, err := c.ChatStream(ctx, messages, nil, func(types.ChatChoice) {}, nil, nil)
					return err
				}
				_, _, err := c.ChatCompletion(ctx, messages, nil)
				return err
			}

			// System-only messages must be rejected before any request is sent.
			err := send([]types.ChatMessage{{Role: "system", Content: "hi"}})
			if err == nil {
				t.Fatal("expected error for system-only messages, got nil")
			}
			if !strings.Contains(err.Error(), "no user message") {
				t.Fatalf("unexpected error: %v", err)
			}

			// Empty/nil messages must also be rejected.
			if err := send(nil); err == nil {
				t.Fatal("expected error for empty messages, got nil")
			}

			if got := hits.Load(); got != 0 {
				t.Errorf("server hit %d times, want 0", got)
			}
		})
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
