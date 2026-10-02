package exec

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"mike/backend/types"
)

// samplingProvider answers a non-streaming completion and keeps the last request body, the only
// place an override is observable from here.
func samplingProvider(t *testing.T) (*httptest.Server, *map[string]any) {
	t.Helper()
	got := &map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

// storeProviderWithSampling persists one provider whose single model carries the overrides.
func storeProviderWithSampling(t *testing.T, te *TaskExecutor, url, model string, sampling *types.ModelSampling) {
	t.Helper()
	cfg := &types.Config{
		Providers: []types.Provider{{
			Kind:        types.ProviderKindLLM,
			URL:         url,
			Concurrency: 1,
			Models:      []types.ModelConfig{{Name: model, ReasoningEffort: "default", Sampling: sampling}},
		}},
	}
	if err := te.configStore.UpdateConfig(cfg); err != nil {
		t.Fatalf("store provider: %v", err)
	}
}

// TestEveryCallCarriesTheModelSamplingOverrides: the overrides are resolved where the client is
// built, so a title, a summary or a commit label samples the way the turn that asked for it does.
func TestEveryCallCarriesTheModelSamplingOverrides(t *testing.T) {
	srv, got := samplingProvider(t)
	te, _, _, _ := setupExecutorWithGit(t)
	temp, topK := 0.2, 0
	storeProviderWithSampling(t, te, srv.URL, "model-a", &types.ModelSampling{
		Temperature: &temp,
		TopK:        &topK,
	})

	msgs := []types.ChatMessage{{Role: "user", Content: "hi"}}
	if _, _, err := te.ChatCompletion(context.Background(), srv.URL, "model-a", msgs, nil); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	if v, ok := (*got)["temperature"].(float64); !ok || v != 0.2 {
		t.Errorf("temperature = %v, want the 0.2 the model row holds", (*got)["temperature"])
	}
	if v, ok := (*got)["top_k"].(float64); !ok || v != 0 {
		t.Errorf("top_k = %v, want the 0 the model row holds (a zero is a value)", (*got)["top_k"])
	}
	for _, key := range []string{"top_p", "min_p", "seed", "frequency_penalty"} {
		if v, present := (*got)[key]; present {
			t.Errorf("body carries %s=%v; nobody typed that override", key, v)
		}
	}
}

// TestModelWithoutOverridesSendsNoSamplerKeys: a row that never mentions sampling must not gain one
// from the wiring.
func TestModelWithoutOverridesSendsNoSamplerKeys(t *testing.T) {
	srv, got := samplingProvider(t)
	te, _, _, _ := setupExecutorWithGit(t)
	storeProviderWithSampling(t, te, srv.URL, "model-bare", nil)

	msgs := []types.ChatMessage{{Role: "user", Content: "hi"}}
	if _, _, err := te.ChatCompletion(context.Background(), srv.URL, "model-bare", msgs, nil); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}

	for _, key := range []string{"temperature", "top_p", "top_k", "min_p", "frequency_penalty", "presence_penalty", "repetition_penalty", "seed"} {
		if v, present := (*got)[key]; present {
			t.Errorf("body carries %s=%v for a model with no overrides", key, v)
		}
	}
}
