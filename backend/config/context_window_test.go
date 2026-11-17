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
// llama.cpp (meta.n_ctx), vLLM (max_model_len), and the runtime n_ctx winning over
// an advertised max_model_len.
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

// TestDiscoverContextWindowInventsNoWindowWhenNothingReportsOne holds the accepted cost of an
// unreported window (docs/context-window.md): the answer is nil, never a guess, because a guessed window
// trims output the model needed on a number nothing told us. A dead host costs nil too — not a panic and
// not a hung request. A case that hands back a counter also pins how many requests the probe may make: a
// direct provider has no upstream endpoint, so asking for one is a bug.
func TestDiscoverContextWindowInventsNoWindowWhenNothingReportsOne(t *testing.T) {
	for _, tc := range []struct {
		name string
		// probe returns the URL to discover from, and the request counter to check against 1, or nil
		// when the number of requests is not this case's point.
		probe func(t *testing.T) (url string, requests *int)
	}{
		{
			name: "the upstream reports neither field",
			probe: func(t *testing.T) (string, *int) {
				s := llamaSwapServer(t, `{"data":[{"id":"m","owned_by":"vllm"}]}`)
				t.Cleanup(s.Close)
				return s.URL + "/v1", nil
			},
		},
		{
			name: "the upstream reports no models",
			probe: func(t *testing.T) (string, *int) {
				s := llamaSwapServer(t, `{"data":[]}`)
				t.Cleanup(s.Close)
				return s.URL + "/v1", nil
			},
		},
		{
			// Non-llama-swap servers have no upstream endpoint, so the probe must be skipped: the one
			// request is the model list that says so.
			name: "a direct provider is not llama-swap",
			probe: func(t *testing.T) (string, *int) {
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
				t.Cleanup(server.Close)
				return server.URL + "/v1", &calls
			},
		},
		{
			name: "nothing is listening",
			probe: func(t *testing.T) (string, *int) {
				// Port 1 on localhost: connection refused.
				return "http://127.0.0.1:1/v1", nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := testConfigStore(t)
			url, requests := tc.probe(t)
			if got := cs.DiscoverContextWindow("Qwen3.8-Flash-Next", url); got != nil {
				t.Errorf("DiscoverContextWindow = %d, want nil: nothing reported a window", *got)
			}
			if requests != nil && *requests != 1 {
				t.Errorf("server calls = %d, want 1 (the upstream probe must be skipped)", *requests)
			}
		})
	}
}

func intPtr(n int) *int { return &n }
