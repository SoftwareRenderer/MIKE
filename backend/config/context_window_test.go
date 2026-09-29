package config

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// llamaSwapServer mimics llama-swap: its own /v1/models omits every
// context-window field (why the upstream probe exists), while
// /upstream/{model}/v1/models serves upstreamBody.
func llamaSwapServer(t *testing.T, upstreamBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/models":
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"Qwen3.8-Flash-Next","object":"model","owned_by":"llama-swap","meta":{"llamaswap":{"type":"model"}},"status":{"value":"loaded"}}]}`))
		default:
			// /upstream/{model}/v1/models
			_, _ = w.Write([]byte(upstreamBody))
		}
	}))
}

// TestDiscoverContextWindow covers the upstream shapes seen in practice:
// llama.cpp (meta.n_ctx), vLLM (max_model_len), and servers reporting neither.
func TestDiscoverContextWindow(t *testing.T) {
	tests := []struct {
		name         string
		upstreamBody string
		want         *int
	}{
		{
			name:         "vLLM upstream: max_model_len",
			upstreamBody: `{"object":"list","data":[{"id":"Qwen3.8-Flash-Next","owned_by":"vllm","max_model_len":262144}]}`,
			want:         intPtr(262144),
		},
		{
			name:         "llama.cpp upstream: meta.n_ctx",
			upstreamBody: `{"object":"list","data":[{"id":"m","owned_by":"llamacpp","meta":{"n_ctx":262144,"n_vocab":248320}}]}`,
			want:         intPtr(262144),
		},
		{
			name:         "runtime n_ctx wins over advertised max_model_len",
			upstreamBody: `{"data":[{"max_model_len":8192,"meta":{"n_ctx":4096}}]}`,
			want:         intPtr(4096),
		},
		{
			name:         "neither field reported",
			upstreamBody: `{"data":[{"id":"m","owned_by":"vllm"}]}`,
			want:         nil,
		},
		{
			name:         "empty data array",
			upstreamBody: `{"data":[]}`,
			want:         nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := testConfigStore(t)
			server := llamaSwapServer(t, tt.upstreamBody)
			defer server.Close()

			got := cs.DiscoverContextWindow("Qwen3.8-Flash-Next", server.URL+"/v1")
			switch {
			case tt.want == nil && got != nil:
				t.Errorf("DiscoverContextWindow = %d, want nil", *got)
			case tt.want != nil && got == nil:
				t.Error("DiscoverContextWindow = nil, want a context window")
			case tt.want != nil && *got != *tt.want:
				t.Errorf("DiscoverContextWindow = %d, want %d", *got, *tt.want)
			}
		})
	}
}

// TestDiscoverContextWindow_NotLlamaSwap: non-llama-swap servers have no
// upstream endpoint, so the probe must be skipped.
func TestDiscoverContextWindow_NotLlamaSwap(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" {
			// Direct vLLM: owned_by is vllm, so no upstream probe should happen.
			_, _ = w.Write([]byte(`{"data":[{"id":"m","owned_by":"vllm","max_model_len":262144}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"max_model_len":1}]}`))
	}))
	defer server.Close()

	cs := testConfigStore(t)
	if got := cs.DiscoverContextWindow("m", server.URL+"/v1"); got != nil {
		t.Errorf("DiscoverContextWindow = %d, want nil (not llama-swap)", *got)
	}
	if calls != 1 {
		t.Errorf("server calls = %d, want 1 (the upstream probe must be skipped)", calls)
	}
}

// TestDiscoverContextWindow_UnreachableServer: a dead host returns nil rather
// than panicking or blocking.
func TestDiscoverContextWindow_UnreachableServer(t *testing.T) {
	cs := testConfigStore(t)
	// Port 1 on localhost: connection refused.
	if got := cs.DiscoverContextWindow("m", "http://127.0.0.1:1/v1"); got != nil {
		t.Errorf("DiscoverContextWindow = %d, want nil for unreachable server", *got)
	}
}

func intPtr(n int) *int { return &n }
