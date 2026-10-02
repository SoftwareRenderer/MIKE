package llama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"mike/backend/llmadapter"
	"mike/backend/types"
)

// The sampler keys, so a "none of them present" test names all eight.
var samplerKeys = []string{
	"temperature", "top_p", "top_k", "min_p",
	"frequency_penalty", "presence_penalty", "repetition_penalty", "seed",
}

// bodyRecorder answers a completion and keeps the last request body it was sent.
func bodyRecorder(t *testing.T, stream bool) (*httptest.Server, *map[string]any) {
	t.Helper()
	got := &map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, got)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n" +
				"data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func fullSampling() *types.ModelSampling {
	return &types.ModelSampling{
		Temperature:       ptrFloat(0.7),
		TopP:              ptrFloat(0.9),
		TopK:              ptrInt(40),
		MinP:              ptrFloat(0.05),
		FrequencyPenalty:  ptrFloat(-1.5),
		PresencePenalty:   ptrFloat(0.25),
		RepetitionPenalty: ptrFloat(1.1),
		Seed:              ptrInt(1234),
	}
}

func ptrFloat(v float64) *float64 { return &v }
func ptrInt(v int) *int           { return &v }

// TestSamplingOverridesReachTheBody: every value typed in Settings arrives at the provider under the
// key that provider reads.
func TestSamplingOverridesReachTheBody(t *testing.T) {
	t.Parallel()
	want := map[string]float64{
		"temperature": 0.7, "top_p": 0.9, "top_k": 40, "min_p": 0.05,
		"frequency_penalty": -1.5, "presence_penalty": 0.25, "repetition_penalty": 1.1, "seed": 1234,
	}

	for _, stream := range []bool{false, true} {
		name := "completion"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			srv, got := bodyRecorder(t, stream)
			c := NewLlamaClient(srv.URL, "test-model", "off", "")
			c.SetSampling(fullSampling())

			var err error
			if stream {
				_, err = c.ChatStream(context.Background(), testMessages(), nil, func(types.ChatChoice) {}, nil, nil)
			} else {
				_, _, err = c.ChatCompletion(context.Background(), testMessages(), nil)
			}
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}

			for key, value := range want {
				v, ok := (*got)[key]
				if !ok {
					t.Errorf("body carries no %q: %v", key, *got)
					continue
				}
				n, isNum := v.(float64)
				if !isNum || n != value {
					t.Errorf("%s = %v, want %v", key, v, value)
				}
			}
		})
	}
}

// TestNoSamplingSendsNoSamplerKeys: a blank field is the absence of the key, which is how the
// provider's own sampling survives.
func TestNoSamplingSendsNoSamplerKeys(t *testing.T) {
	t.Parallel()
	t.Run("never set", func(t *testing.T) {
		srv, got := bodyRecorder(t, false)
		c := NewLlamaClient(srv.URL, "test-model", "off", "")
		if _, _, err := c.ChatCompletion(context.Background(), testMessages(), nil); err != nil {
			t.Fatalf("ChatCompletion: %v", err)
		}
		assertNoSamplerKeys(t, *got)
	})

	t.Run("set to an empty override set", func(t *testing.T) {
		srv, got := bodyRecorder(t, false)
		c := NewLlamaClient(srv.URL, "test-model", "off", "")
		c.SetSampling(&types.ModelSampling{})
		if _, _, err := c.ChatCompletion(context.Background(), testMessages(), nil); err != nil {
			t.Fatalf("ChatCompletion: %v", err)
		}
		assertNoSamplerKeys(t, *got)
	})
}

func assertNoSamplerKeys(t *testing.T, body map[string]any) {
	t.Helper()
	for _, key := range samplerKeys {
		if v, ok := body[key]; ok {
			t.Errorf("body carries %s=%v; a model with no override sends nothing for it", key, v)
		}
	}
}

// TestTemperatureZeroIsASentValue: temperature 0 is greedy decoding somebody chose, and dropping it
// hands the turn back to the provider.
func TestTemperatureZeroIsASentValue(t *testing.T) {
	t.Parallel()
	srv, got := bodyRecorder(t, false)
	c := NewLlamaClient(srv.URL, "test-model", "off", "")
	c.SetSampling(&types.ModelSampling{Temperature: ptrFloat(0), TopK: ptrInt(0)})

	if _, _, err := c.ChatCompletion(context.Background(), testMessages(), nil); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	for _, key := range []string{"temperature", "top_k"} {
		v, ok := (*got)[key]
		if !ok {
			t.Errorf("body carries no %q: %v", key, *got)
			continue
		}
		if n, _ := v.(float64); n != 0 {
			t.Errorf("%s = %v, want 0", key, v)
		}
	}
	for _, key := range []string{"top_p", "min_p", "seed"} {
		if _, ok := (*got)[key]; ok {
			t.Errorf("body carries %q, which was never typed", key)
		}
	}
}

// TestSamplingIsAppliedBeforeTheAdapter: a mapping sees the sampler fields in the body, so it can
// rename or drop one for its provider.
func TestSamplingIsAppliedBeforeTheAdapter(t *testing.T) {
	t.Parallel()
	srv, got := bodyRecorder(t, false)

	a := llmadapter.NewAdapter()
	a.SetMappings([]llmadapter.Mapping{{
		Match: srv.URL,
		Body:  llmadapter.BodyRules{Remove: []string{"temperature"}},
	}})

	c := NewLlamaClient(srv.URL, "test-model", "off", "")
	c.SetAdapter(a)
	c.SetSampling(fullSampling())

	if _, _, err := c.ChatCompletion(context.Background(), testMessages(), nil); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if _, ok := (*got)["temperature"]; ok {
		t.Error("the mapping's Remove did not reach temperature: the overrides are applied after the adapter")
	}
	if _, ok := (*got)["seed"]; !ok {
		t.Error("a sampler field the mapping never named went missing")
	}
}
