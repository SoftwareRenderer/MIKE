package config

import (
	"math"
	"testing"

	"mike/backend/types"
)

// TestEstimateCallCost prices one call from the usage the provider reported: the provider's rates, a
// model's own rates over them, and cache_write_price standing in for input_price on the uncached part.
// Where there is no price to apply it answers zero and invents nothing — an unpriced provider, a
// provider that is not an LLM (they report their own cost through session stats), a provider the config
// does not hold, and a call that reported no usage all cost nothing to say so.
func TestEstimateCallCost(t *testing.T) {
	usage := func(prompt, completion, cached int) *types.ChatUsage {
		u := &types.ChatUsage{PromptTokens: prompt, CompletionTokens: completion}
		u.PromptTokensDetails.CachedTokens = cached
		return u
	}
	oneProvider := func(p types.Provider) []types.Provider {
		p.Kind = types.ProviderKindLLM
		p.URL = "http://llm:8010/v1"
		p.Concurrency = 1
		if p.Models == nil {
			p.Models = []types.ModelConfig{{Name: "model-a"}}
		}
		return []types.Provider{p}
	}

	for _, tc := range []struct {
		name      string
		providers []types.Provider
		url       string
		model     string
		usage     *types.ChatUsage
		want      float64
	}{
		{
			name:      "no per-token pricing",
			providers: oneProvider(types.Provider{}),
			url:       "http://llm:8010/v1",
			model:     "model-a",
			usage:     usage(1000, 500, 0),
			want:      0,
		},
		{
			name:      "the provider's rates",
			providers: oneProvider(types.Provider{InputPrice: 1.0, CacheReadPrice: 0.1, OutputPrice: 2.0}),
			url:       "http://llm:8010/v1",
			model:     "model-a",
			// (800 non-cached * $1.0 + 200 cached * $0.1 + 500 output * $2.0) / 1M
			usage: usage(1000, 500, 200),
			want:  (800*1.0 + 200*0.1 + 500*2.0) / 1_000_000,
		},
		{
			name: "the model's own rates win",
			providers: oneProvider(types.Provider{
				InputPrice: 1.0, OutputPrice: 2.0,
				Models: []types.ModelConfig{{Name: "pricey", InputPrice: floatPtr(5.0), OutputPrice: floatPtr(10.0)}},
			}),
			url:   "http://llm:8010/v1",
			model: "pricey",
			// (1000 * $5.0 + 300 * $10.0) / 1M
			usage: usage(1000, 300, 0),
			want:  (1000*5.0 + 300*10.0) / 1_000_000,
		},
		{
			name: "another model of that provider falls back to the provider's rates",
			providers: oneProvider(types.Provider{
				InputPrice: 1.0, OutputPrice: 2.0,
				Models: []types.ModelConfig{{Name: "pricey", InputPrice: floatPtr(5.0), OutputPrice: floatPtr(10.0)}},
			}),
			url:   "http://llm:8010/v1",
			model: "other-model",
			usage: usage(1000, 300, 0),
			want:  (1000*1.0 + 300*2.0) / 1_000_000,
		},
		{
			name:      "cache_write_price replaces input_price for the uncached part",
			providers: oneProvider(types.Provider{InputPrice: 1.0, CacheWritePrice: 0.5}),
			url:       "http://llm:8010/v1",
			model:     "model-a",
			usage:     usage(1000, 0, 0),
			want:      1000 * 0.5 / 1_000_000,
		},
		{
			name: "an agent provider reports its own cost",
			providers: func() []types.Provider {
				p := types.Provider{Name: "pi", Kind: types.ProviderKindAgent, URL: "agent://pi", Concurrency: 1,
					InputPrice: 1.0, OutputPrice: 2.0}
				return []types.Provider{p}
			}(),
			url:   "agent://pi",
			model: "pi",
			usage: usage(1000, 500, 0),
			want:  0,
		},
		{
			name:      "a provider the config does not hold",
			providers: oneProvider(types.Provider{InputPrice: 1.0}),
			url:       "http://unknown:9999/v1",
			model:     "model-a",
			usage:     usage(1000, 500, 0),
			want:      0,
		},
		{
			name:      "no usage reported",
			providers: oneProvider(types.Provider{InputPrice: 1.0}),
			url:       "http://llm:8010/v1",
			model:     "model-a",
			usage:     nil,
			want:      0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := testConfigStore(t)
			if err := cs.UpdateConfig(&types.Config{Providers: tc.providers}); err != nil {
				t.Fatalf("update config: %v", err)
			}
			if got := cs.EstimateCallCost(tc.url, tc.model, tc.usage); math.Abs(got-tc.want) > 1e-12 {
				t.Errorf("EstimateCallCost(%q, %q) = %v, want %v", tc.url, tc.model, got, tc.want)
			}
		})
	}
}
