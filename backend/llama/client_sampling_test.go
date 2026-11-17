package llama

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"mike/backend/llmadapter"
	"mike/backend/types"
)

// The sampler keys, so a "none of them present" case names all eight.
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

// everythingExcept is the blank half of an override set: a key nobody typed must not ride along.
func everythingExcept(typed ...string) []string {
	skip := make(map[string]bool, len(typed))
	for _, k := range typed {
		skip[k] = true
	}
	var absent []string
	for _, key := range samplerKeys {
		if !skip[key] {
			absent = append(absent, key)
		}
	}
	return absent
}

// TestSamplerKeysInTheBody: a value typed in Settings arrives under the key that provider reads, and a
// blank field is the absence of that key — which is how the provider's own sampling survives. A zero is
// a value somebody chose (greedy decoding), not a blank.
func TestSamplerKeysInTheBody(t *testing.T) {
	t.Parallel()
	everything := map[string]float64{
		"temperature": 0.7, "top_p": 0.9, "top_k": 40, "min_p": 0.05,
		"frequency_penalty": -1.5, "presence_penalty": 0.25, "repetition_penalty": 1.1, "seed": 1234,
	}

	for _, tc := range []struct {
		name       string
		stream     bool
		sampling   *types.ModelSampling
		want       map[string]float64
		wantAbsent []string
	}{
		{name: "completion sends every override", sampling: fullSampling(), want: everything},
		{name: "stream sends every override", stream: true, sampling: fullSampling(), want: everything},
		{name: "a model with no overrides sends none of the keys", wantAbsent: samplerKeys},
		{name: "an empty override set sends none of the keys", sampling: &types.ModelSampling{}, wantAbsent: samplerKeys},
		{
			name:       "a zero is a decision, not a blank",
			sampling:   &types.ModelSampling{Temperature: ptrFloat(0), TopK: ptrInt(0)},
			want:       map[string]float64{"temperature": 0, "top_k": 0},
			wantAbsent: everythingExcept("temperature", "top_k"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, got := bodyRecorder(t, tc.stream)
			c := NewLlamaClient(srv.URL, "test-model", "off", "")
			c.SetSampling(tc.sampling)

			var err error
			if tc.stream {
				_, err = c.ChatStream(context.Background(), testMessages(), nil, func(types.ChatChoice) {}, nil, nil)
			} else {
				_, _, err = c.ChatCompletion(context.Background(), testMessages(), nil)
			}
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			for key, value := range tc.want {
				v, ok := (*got)[key]
				if !ok {
					t.Errorf("body carries no %q: %v", key, *got)
					continue
				}
				if n, isNum := v.(float64); !isNum || n != value {
					t.Errorf("%s = %v, want %v", key, v, value)
				}
			}
			for _, key := range tc.wantAbsent {
				if v, ok := (*got)[key]; ok {
					t.Errorf("body carries %s=%v; a model with no override sends nothing for it", key, v)
				}
			}
		})
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
