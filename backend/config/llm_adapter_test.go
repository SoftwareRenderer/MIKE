package config

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"mike/backend/llmadapter"
)

func adapterForTests(serverURL string) *llmadapter.Adapter {
	a := llmadapter.NewAdapter()
	a.SetMappings([]llmadapter.Mapping{{
		Match:   serverURL,
		Headers: map[string]string{"X-Test-Header": "adapter-value"},
		Body: llmadapter.BodyRules{
			Remove: []string{"max_tokens"},
			Set: map[string]llmadapter.SetRule{
				"reasoning.enabled": {From: "reasoning_effort", Value: true, When: []string{"", "default"}},
			},
		},
	}})
	return a
}

// TestTestConnectionAppliesTheMatchingMapping covers the one decision from both sides: a mapping whose
// match is the provider URL rewrites the test-connection request (headers, removed keys, derived body
// rules), and no mapping leaves the default request exactly as it was before an adapter existed.
func TestTestConnectionAppliesTheMatchingMapping(t *testing.T) {
	for _, tc := range []struct {
		name    string
		adapter bool
	}{
		{name: "a mapping matches the provider URL", adapter: true},
		{name: "no adapter is wired", adapter: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody map[string]any
			var gotHeader http.Header
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotHeader = r.Header.Clone()
				data, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(data, &gotBody)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
			}))
			defer server.Close()

			cs := testConfigStore(t)
			if tc.adapter {
				cs.SetLLMAdapter(adapterForTests(server.URL))
			}

			res := cs.TestConnection(TestConnectionRequest{LlamaServerURL: server.URL, ModelName: "test-model"})
			if !res.Success {
				t.Fatalf("TestConnection failed: %s", res.Message)
			}
			if gotBody["model"] != "test-model" {
				t.Errorf("model = %v, want test-model", gotBody["model"])
			}

			_, hasMaxTokens := gotBody["max_tokens"]
			reasoning, _ := gotBody["reasoning"].(map[string]any)
			if tc.adapter {
				if hasMaxTokens {
					t.Error("max_tokens should be removed by the mapping")
				}
				if reasoning == nil || reasoning["enabled"] != true {
					t.Errorf("reasoning = %v, want {enabled: true}", gotBody["reasoning"])
				}
				if gotHeader.Get("X-Test-Header") != "adapter-value" {
					t.Errorf("headers = %v", gotHeader)
				}
				return
			}
			if !hasMaxTokens || gotBody["max_tokens"] != float64(1) {
				t.Errorf("max_tokens = %v, want 1 (unchanged)", gotBody["max_tokens"])
			}
			if reasoning != nil {
				t.Errorf("reasoning should not be present without an adapter: %v", gotBody["reasoning"])
			}
			if gotHeader.Get("X-Test-Header") != "" {
				t.Errorf("an adapter's header reached a request with no adapter: %v", gotHeader)
			}
		})
	}
}

// TestFetchModels_AdapterHeaders verifies /models requests carry the
// mapping's headers.
func TestFetchModels_AdapterHeaders(t *testing.T) {
	var gotHeader http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"model-one"}]}`))
	}))
	defer server.Close()

	cs := testConfigStore(t)
	cs.SetLLMAdapter(adapterForTests(server.URL))

	res := cs.FetchModels(FetchModelsRequest{LlamaServerURL: server.URL})
	if res.Error != "" {
		t.Fatalf("FetchModels error: %s", res.Error)
	}
	if len(res.Models) != 1 || res.Models[0].Name != "model-one" {
		t.Errorf("models = %+v, want [model-one]", res.Models)
	}
	if gotHeader.Get("X-Test-Header") != "adapter-value" {
		t.Errorf("headers = %v", gotHeader)
	}
}
