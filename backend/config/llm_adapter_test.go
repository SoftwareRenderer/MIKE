package config

import (
	"encoding/json"
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

// TestTestConnection_AdapterApplied verifies the test-connection request goes
// out in the provider's expected format (body rules + headers) when a mapping
// matches the provider URL.
func TestTestConnection_AdapterApplied(t *testing.T) {
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
	cs.SetLLMAdapter(adapterForTests(server.URL))

	res := cs.TestConnection(TestConnectionRequest{LlamaServerURL: server.URL, ModelName: "test-model"})
	if !res.Success {
		t.Fatalf("TestConnection failed: %s", res.Message)
	}
	if gotBody["model"] != "test-model" {
		t.Errorf("model = %v, want test-model", gotBody["model"])
	}
	if _, exists := gotBody["max_tokens"]; exists {
		t.Error("max_tokens should be removed by the mapping")
	}
	reasoning, _ := gotBody["reasoning"].(map[string]any)
	if reasoning == nil || reasoning["enabled"] != true {
		t.Errorf("reasoning = %v, want {enabled: true}", gotBody["reasoning"])
	}
	if gotHeader.Get("X-Test-Header") != "adapter-value" {
		t.Errorf("headers = %v", gotHeader)
	}
}

// TestTestConnection_NoAdapterUnchanged verifies the default request is
// unchanged when no adapter is wired.
func TestTestConnection_NoAdapterUnchanged(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	cs := testConfigStore(t)

	res := cs.TestConnection(TestConnectionRequest{LlamaServerURL: server.URL, ModelName: "test-model"})
	if !res.Success {
		t.Fatalf("TestConnection failed: %s", res.Message)
	}
	if gotBody["max_tokens"] != float64(1) {
		t.Errorf("max_tokens = %v, want 1 (unchanged)", gotBody["max_tokens"])
	}
	if _, exists := gotBody["reasoning"]; exists {
		t.Errorf("reasoning should not be present without an adapter: %v", gotBody["reasoning"])
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
