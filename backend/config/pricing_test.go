package config

import (
	"math"
	"testing"

	"mike/backend/types"
)

func TestEstimateCallCost_NoPricingConfigured(t *testing.T) {
	t.Parallel()
	cs := testConfigStore(t)
	cfg := &types.Config{
		Providers: []types.Provider{
			{Kind: "llm", URL: "http://llm:8010/v1", Concurrency: 1},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	usage := &types.ChatUsage{PromptTokens: 1000, CompletionTokens: 500}
	if got := cs.EstimateCallCost("http://llm:8010/v1", "model-a", usage); got != 0 {
		t.Errorf("EstimateCallCost = %v, want 0 (no per-token pricing)", got)
	}
}

func TestEstimateCallCost_ProviderPricing(t *testing.T) {
	t.Parallel()
	cs := testConfigStore(t)
	cfg := &types.Config{
		Providers: []types.Provider{
			{
				Kind: "llm", URL: "http://llm:8010/v1", Concurrency: 1,
				InputPrice: 1.0, CacheReadPrice: 0.1, OutputPrice: 2.0,
			},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	usage := &types.ChatUsage{PromptTokens: 1000, CompletionTokens: 500}
	usage.PromptTokensDetails.CachedTokens = 200

	// cost = (800 non-cached * $1.0 + 200 cached * $0.1 + 500 output * $2.0) / 1M
	want := (800*1.0 + 200*0.1 + 500*2.0) / 1_000_000
	got := cs.EstimateCallCost("http://llm:8010/v1", "model-a", usage)
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("EstimateCallCost = %v, want %v", got, want)
	}
}

func TestEstimateCallCost_ModelOverrideWins(t *testing.T) {
	t.Parallel()
	cs := testConfigStore(t)
	input := 5.0
	output := 10.0
	cfg := &types.Config{
		Providers: []types.Provider{
			{
				Kind: "llm", URL: "http://llm:8010/v1", Concurrency: 1,
				InputPrice: 1.0, OutputPrice: 2.0,
				Models: []types.ModelConfig{
					{Name: "pricey", InputPrice: &input, OutputPrice: &output},
				},
			},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	usage := &types.ChatUsage{PromptTokens: 1000, CompletionTokens: 300}

	// model override applies: (1000 * $5.0 + 300 * $10.0) / 1M
	want := (1000*5.0 + 300*10.0) / 1_000_000
	got := cs.EstimateCallCost("http://llm:8010/v1", "pricey", usage)
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("EstimateCallCost = %v, want %v", got, want)
	}

	// A different model on the same provider falls back to provider pricing.
	usage2 := &types.ChatUsage{PromptTokens: 1000, CompletionTokens: 300}
	want2 := (1000*1.0 + 300*2.0) / 1_000_000
	got2 := cs.EstimateCallCost("http://llm:8010/v1", "other-model", usage2)
	if math.Abs(got2-want2) > 1e-12 {
		t.Errorf("EstimateCallCost (fallback) = %v, want %v", got2, want2)
	}
}

func TestEstimateCallCost_CacheWriteSubstitutesInput(t *testing.T) {
	t.Parallel()
	cs := testConfigStore(t)
	cfg := &types.Config{
		Providers: []types.Provider{
			{
				Kind: "llm", URL: "http://llm:8010/v1", Concurrency: 1,
				InputPrice: 1.0, CacheWritePrice: 0.5,
			},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	usage := &types.ChatUsage{PromptTokens: 1000, CompletionTokens: 0}

	// cache_write_price (0.5) replaces input_price for non-cached input.
	want := 1000 * 0.5 / 1_000_000
	got := cs.EstimateCallCost("http://llm:8010/v1", "model-a", usage)
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("EstimateCallCost = %v, want %v", got, want)
	}
}

func TestEstimateCallCost_AgentProviderReturnsZero(t *testing.T) {
	t.Parallel()
	cs := testConfigStore(t)
	cfg := &types.Config{
		Providers: []types.Provider{
			{
				Name: "pi", Kind: "agent", URL: "agent://pi", Concurrency: 1,
				InputPrice: 1.0, OutputPrice: 2.0,
			},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	usage := &types.ChatUsage{PromptTokens: 1000, CompletionTokens: 500}
	// Agent providers report their own cost via session stats — never estimate.
	if got := cs.EstimateCallCost("agent://pi", "pi", usage); got != 0 {
		t.Errorf("EstimateCallCost (agent) = %v, want 0", got)
	}
}

func TestEstimateCallCost_UnknownProviderReturnsZero(t *testing.T) {
	t.Parallel()
	cs := testConfigStore(t)
	cfg := &types.Config{
		Providers: []types.Provider{
			{Kind: "llm", URL: "http://llm:8010/v1", Concurrency: 1, InputPrice: 1.0},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	usage := &types.ChatUsage{PromptTokens: 1000, CompletionTokens: 500}
	if got := cs.EstimateCallCost("http://unknown:9999/v1", "model-a", usage); got != 0 {
		t.Errorf("EstimateCallCost (unknown provider) = %v, want 0", got)
	}
}

func TestEstimateCallCost_NilUsage(t *testing.T) {
	t.Parallel()
	cs := testConfigStore(t)
	cfg := &types.Config{
		Providers: []types.Provider{
			{Kind: "llm", URL: "http://llm:8010/v1", Concurrency: 1, InputPrice: 1.0},
		},
	}
	if err := cs.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	if got := cs.EstimateCallCost("http://llm:8010/v1", "model-a", nil); got != 0 {
		t.Errorf("EstimateCallCost (nil usage) = %v, want 0", got)
	}
}
