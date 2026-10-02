package config

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"mike/backend/types"
)

// samplingStore saves one provider whose one model carries the given overrides.
func floatPtr(v float64) *float64 { return &v }

func samplingStore(t *testing.T, sampling *types.ModelSampling) (*ConfigStore, error) {
	t.Helper()
	cs := testConfigStore(t)
	cfg := &types.Config{
		Providers: []types.Provider{{
			Kind:        types.ProviderKindLLM,
			Name:        "primary",
			URL:         "http://llm:8010/v1",
			Concurrency: 1,
			Models:      []types.ModelConfig{{Name: "model-a", ReasoningEffort: "default", Sampling: sampling}},
		}},
	}
	return cs, cs.UpdateConfig(cfg)
}

// TestSamplerBounds_EdgeAcceptedJustOutsideRefused: the edge of a range is saved, and a value one
// step outside it is refused naming the field.
func TestSamplerBounds_EdgeAcceptedJustOutsideRefused(t *testing.T) {
	t.Parallel()
	cases := []struct {
		field    string
		ok       []*types.ModelSampling
		rejected []*types.ModelSampling
	}{
		{"temperature",
			[]*types.ModelSampling{{Temperature: floatPtr(0)}, {Temperature: floatPtr(2)}},
			[]*types.ModelSampling{{Temperature: floatPtr(-0.01)}, {Temperature: floatPtr(2.01)}, {Temperature: floatPtr(900)}}},
		{"top_p",
			[]*types.ModelSampling{{TopP: floatPtr(0.001)}, {TopP: floatPtr(1)}},
			[]*types.ModelSampling{{TopP: floatPtr(0)}, {TopP: floatPtr(1.5)}}},
		{"min_p",
			[]*types.ModelSampling{{MinP: floatPtr(0)}, {MinP: floatPtr(1)}},
			[]*types.ModelSampling{{MinP: floatPtr(-0.1)}, {MinP: floatPtr(1.1)}}},
		{"frequency_penalty",
			[]*types.ModelSampling{{FrequencyPenalty: floatPtr(-2)}, {FrequencyPenalty: floatPtr(0)}, {FrequencyPenalty: floatPtr(2)}},
			[]*types.ModelSampling{{FrequencyPenalty: floatPtr(-2.5)}, {FrequencyPenalty: floatPtr(2.5)}}},
		{"presence_penalty",
			[]*types.ModelSampling{{PresencePenalty: floatPtr(-2)}, {PresencePenalty: floatPtr(2)}},
			[]*types.ModelSampling{{PresencePenalty: floatPtr(-2.1)}, {PresencePenalty: floatPtr(2.1)}}},
		{"repetition_penalty",
			[]*types.ModelSampling{{RepetitionPenalty: floatPtr(0.1)}, {RepetitionPenalty: floatPtr(2)}},
			[]*types.ModelSampling{{RepetitionPenalty: floatPtr(0)}, {RepetitionPenalty: floatPtr(2.5)}}},
		{"top_k",
			[]*types.ModelSampling{{TopK: intPtr(0)}, {TopK: intPtr(40)}},
			[]*types.ModelSampling{{TopK: intPtr(-1)}, {TopK: intPtr(-2)}}},
		{"seed",
			[]*types.ModelSampling{{Seed: intPtr(0)}, {Seed: intPtr(123456789)}},
			[]*types.ModelSampling{{Seed: intPtr(-1)}}},
	}

	for _, tc := range cases {
		for _, s := range tc.ok {
			if _, err := samplingStore(t, s); err != nil {
				t.Errorf("%s refused: %v", samplingValues(s), err)
			}
		}
		for _, s := range tc.rejected {
			_, err := samplingStore(t, s)
			if err == nil {
				t.Errorf("%s accepted; want a refusal", samplingValues(s))
				continue
			}
			if !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("%s: err = %v, want an ErrInvalidConfig", samplingValues(s), err)
			}
			for _, want := range []string{"primary", "model-a", tc.field} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%s refusal %q names no %q", tc.field, err, want)
				}
			}
		}
	}
}

// samplingValues renders the overrides for a failure message.
func samplingValues(s *types.ModelSampling) string {
	var parts []string
	for _, b := range samplerFloatBounds {
		if v := b.pick(s); v != nil {
			parts = append(parts, b.name+"="+trimNum(*v))
		}
	}
	for _, b := range samplerIntBounds {
		if v := b.pick(s); v != nil {
			parts = append(parts, b.name+"="+strconv.Itoa(*v))
		}
	}
	return strings.Join(parts, ",")
}

// TestSamplerFieldNeverTypedIsNeverRefused: an absent object and an all-nil one both mean "send
// nothing", which is every model row's default, so neither may be a refusal.
func TestSamplerFieldNeverTypedIsNeverRefused(t *testing.T) {
	t.Parallel()
	if _, err := samplingStore(t, nil); err != nil {
		t.Errorf("no sampling object refused: %v", err)
	}
	if _, err := samplingStore(t, &types.ModelSampling{}); err != nil {
		t.Errorf("an all-nil sampling object refused: %v", err)
	}
}

// TestRefusedSamplerWritesNothing: a refusal refuses the whole save — the stored row keeps its
// overrides and the offending row is not added beside it.
func TestRefusedSamplerWritesNothing(t *testing.T) {
	t.Parallel()
	cs, err := samplingStore(t, &types.ModelSampling{Temperature: floatPtr(0.3)})
	if err != nil {
		t.Fatalf("first save: %v", err)
	}

	bad := &types.Config{
		Providers: []types.Provider{{
			Kind:        types.ProviderKindLLM,
			Name:        "primary",
			URL:         "http://llm:8010/v1",
			Concurrency: 1,
			Models: []types.ModelConfig{
				{Name: "model-a", ReasoningEffort: "default", Sampling: &types.ModelSampling{Temperature: floatPtr(7)}},
				{Name: "model-b", ReasoningEffort: "default"},
			},
		}},
	}
	if err := cs.UpdateConfig(bad); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("bad save err = %v, want an ErrInvalidConfig", err)
	}

	cfg, err := cs.GetConfig()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	models := cfg.Providers[0].Models
	if len(models) != 1 || models[0].Name != "model-a" {
		t.Fatalf("stored models = %+v, want only the model the accepted save wrote", models)
	}
	if models[0].Sampling == nil || models[0].Sampling.Temperature == nil || *models[0].Sampling.Temperature != 0.3 {
		t.Fatalf("stored overrides = %+v, want the temperature the accepted save wrote", models[0].Sampling)
	}
}

// TestPUTConfig_SamplerOutOfRangeIs400NamingTheField: whoever typed the value gets the sentence as a
// 400, not a 500 with the cause sitting in the log.
func TestPUTConfig_SamplerOutOfRangeIs400NamingTheField(t *testing.T) {
	t.Parallel()
	cs, _ := testConfigStoreWithStore(t)
	body := `{"providers":[{"kind":"llm","name":"primary","url":"http://llm:8010/v1","concurrency":1,` +
		`"models":[{"name":"model-a","reasoning_effort":"default","sampling":{"top_p":3}}]}]}`
	w := putConfigRaw(t, cs, body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body %s, want 400", w.Code, w.Body.String())
	}
	for _, want := range []string{"top_p", "model-a"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("the 400 body %q names no %q", w.Body.String(), want)
		}
	}
}

// TestGetModelSampling: what was saved is what a request gets, and anything the config does not hold
// answers nil.
func TestGetModelSampling(t *testing.T) {
	t.Parallel()
	cs, err := samplingStore(t, &types.ModelSampling{Temperature: floatPtr(0), TopK: intPtr(40), Seed: intPtr(7)})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	got := cs.GetModelSampling("model-a", "http://llm:8010/v1")
	if got == nil {
		t.Fatal("GetModelSampling = nil, want the saved overrides")
	}
	if got.Temperature == nil || *got.Temperature != 0 {
		t.Errorf("temperature = %v, want the 0 that was stored (a zero is a value)", got.Temperature)
	}
	if got.TopK == nil || *got.TopK != 40 {
		t.Errorf("top_k = %v, want 40", got.TopK)
	}
	if got.Seed == nil || *got.Seed != 7 {
		t.Errorf("seed = %v, want 7", got.Seed)
	}
	if got.TopP != nil || got.MinP != nil {
		t.Errorf("untouched fields came back set: %+v", got)
	}

	// The URL is compared ignoring a trailing slash, like every other provider lookup.
	if cs.GetModelSampling("model-a", "http://llm:8010/v1/") == nil {
		t.Error("a trailing slash on the provider URL lost the overrides")
	}
	if got := cs.GetModelSampling("model-z", "http://llm:8010/v1"); got != nil {
		t.Errorf("an unknown model answered %+v, want nil", got)
	}
	if got := cs.GetModelSampling("model-a", "http://elsewhere:9/v1"); got != nil {
		t.Errorf("an unknown provider answered %+v, want nil", got)
	}
}
