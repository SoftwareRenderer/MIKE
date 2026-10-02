package types

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestModelConfigJSONRoundTrip verifies the per-model pricing/power and sampler override
// fields serialize and deserialize correctly, and that nil (inherit) fields are
// omitted from the JSON output.
func TestModelConfigJSONRoundTrip(t *testing.T) {
	inputP := 3.5
	outputP := 7.25
	cacheReadP := 1.0
	cacheWriteP := 2.5
	powerW := 420.0
	ctx := 32768
	temp := 0.7
	topP := 0.9
	topK := 40
	minP := 0.05
	freqPenalty := -1.5
	presPenalty := 0.25
	repPenalty := 1.1
	seed := 1234

	m := ModelConfig{
		Name:              "model-a",
		ReasoningEffort:   "high",
		ContextWindow:     &ctx,
		InputPrice:        &inputP,
		OutputPrice:       &outputP,
		CacheReadPrice:    &cacheReadP,
		CacheWritePrice:   &cacheWriteP,
		PowerConsumptionW: &powerW,
		Sampling: &ModelSampling{
			Temperature:       &temp,
			TopP:              &topP,
			TopK:              &topK,
			MinP:              &minP,
			FrequencyPenalty:  &freqPenalty,
			PresencePenalty:   &presPenalty,
			RepetitionPenalty: &repPenalty,
			Seed:              &seed,
		},
	}

	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	jsonStr := string(data)

	for _, key := range []string{
		`"name":"model-a"`,
		`"context_window":32768`,
		`"input_price":3.5`,
		`"output_price":7.25`,
		`"cache_read_price":1`,
		`"cache_write_price":2.5`,
		`"power_consumption_w":420`,
		`"sampling":{`,
		`"temperature":0.7`,
		`"top_p":0.9`,
		`"top_k":40`,
		`"min_p":0.05`,
		`"frequency_penalty":-1.5`,
		`"presence_penalty":0.25`,
		`"repetition_penalty":1.1`,
		`"seed":1234`,
	} {
		if !strings.Contains(jsonStr, key) {
			t.Fatalf("expected %s in JSON, got: %s", key, jsonStr)
		}
	}

	var out ModelConfig
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Name != m.Name || out.ReasoningEffort != m.ReasoningEffort {
		t.Fatalf("scalar fields mismatch: %+v", out)
	}
	if out.ContextWindow == nil || *out.ContextWindow != ctx {
		t.Fatalf("context_window mismatch: %v", out.ContextWindow)
	}
	if out.InputPrice == nil || *out.InputPrice != inputP {
		t.Fatalf("input_price mismatch: %v", out.InputPrice)
	}
	if out.OutputPrice == nil || *out.OutputPrice != outputP {
		t.Fatalf("output_price mismatch: %v", out.OutputPrice)
	}
	if out.CacheReadPrice == nil || *out.CacheReadPrice != cacheReadP {
		t.Fatalf("cache_read_price mismatch: %v", out.CacheReadPrice)
	}
	if out.CacheWritePrice == nil || *out.CacheWritePrice != cacheWriteP {
		t.Fatalf("cache_write_price mismatch: %v", out.CacheWritePrice)
	}
	if out.PowerConsumptionW == nil || *out.PowerConsumptionW != powerW {
		t.Fatalf("power_consumption_w mismatch: %v", out.PowerConsumptionW)
	}
	if out.Sampling == nil {
		t.Fatalf("sampling was dropped: %s", jsonStr)
	}
	for _, f := range []struct {
		name string
		got  *float64
		want float64
	}{
		{"temperature", out.Sampling.Temperature, temp},
		{"top_p", out.Sampling.TopP, topP},
		{"min_p", out.Sampling.MinP, minP},
		{"frequency_penalty", out.Sampling.FrequencyPenalty, freqPenalty},
		{"presence_penalty", out.Sampling.PresencePenalty, presPenalty},
		{"repetition_penalty", out.Sampling.RepetitionPenalty, repPenalty},
	} {
		if f.got == nil || *f.got != f.want {
			t.Errorf("sampling.%s mismatch: %v", f.name, f.got)
		}
	}
	for _, f := range []struct {
		name string
		got  *int
		want int
	}{
		{"top_k", out.Sampling.TopK, topK},
		{"seed", out.Sampling.Seed, seed},
	} {
		if f.got == nil || *f.got != f.want {
			t.Errorf("sampling.%s mismatch: %v", f.name, f.got)
		}
	}
}

// TestProviderUnmarshalNormalizesReasoningEffort: empty effort and legacy string
// model lists normalize to "default".
func TestProviderUnmarshalNormalizesReasoningEffort(t *testing.T) {
	var p Provider
	data := `{"name":"p","url":"http://x/v1","concurrency":1,"models":[{"name":"a"},{"name":"b","reasoning_effort":""},{"name":"c","reasoning_effort":"default"}]}`
	if err := json.Unmarshal([]byte(data), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, m := range p.Models {
		if m.ReasoningEffort != "default" {
			t.Errorf("model %s: expected default effort \"default\", got %q", m.Name, m.ReasoningEffort)
		}
	}

	// Legacy format: plain model-name strings normalize to the default level.
	var legacy Provider
	legacyData := `{"name":"p","url":"http://x/v1","concurrency":1,"models":["m1","m2"]}`
	if err := json.Unmarshal([]byte(legacyData), &legacy); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	for _, m := range legacy.Models {
		if m.ReasoningEffort != "default" {
			t.Errorf("legacy model %s: expected default effort \"default\", got %q", m.Name, m.ReasoningEffort)
		}
	}
}

// TestModelConfigNilFieldsOmitted verifies that unset (nil) override fields are
// omitted from JSON so they round-trip as "inherit provider default".
func TestModelConfigNilFieldsOmitted(t *testing.T) {
	m := ModelConfig{Name: "model-b", ReasoningEffort: "medium"}

	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	jsonStr := string(data)

	for _, key := range []string{
		"input_price", "output_price", "cache_read_price", "cache_write_price",
		"power_consumption_w", "context_window", "sampling",
	} {
		if strings.Contains(jsonStr, key) {
			t.Fatalf("expected %s to be omitted, got: %s", key, jsonStr)
		}
	}

	var out ModelConfig
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.InputPrice != nil || out.OutputPrice != nil || out.CacheReadPrice != nil ||
		out.CacheWritePrice != nil || out.PowerConsumptionW != nil || out.ContextWindow != nil ||
		out.Sampling != nil {
		t.Fatalf("expected all override fields nil, got: %+v", out)
	}
}
