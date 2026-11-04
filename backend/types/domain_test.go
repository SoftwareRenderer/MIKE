package types

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestModelConfigJSONTagsAreWhatTheStoreHolds pins the names a stored model row is written with; a rename
// leaves every existing config's pricing and sampler quietly empty. TestModelConfigNilFieldsOmitted is the
// other half.
func TestModelConfigJSONTagsAreWhatTheStoreHolds(t *testing.T) {
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
