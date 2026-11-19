package config

// These tests cover the two readings that make the worker pair no answer at all: a reference no provider
// answers, and a pair that names no model.

import (
	"testing"

	"mike/backend/types"
)

func TestGetWorkerTargetNeedsBothHalvesOfThePair(t *testing.T) {
	cs := testConfigStore(t)
	if _, _, ok := cs.GetWorkerTarget(); ok {
		t.Fatal("an unset worker is a target")
	}

	if err := cs.UpdateConfig(&types.Config{
		Providers: []types.Provider{
			{ID: 4, Name: "primary", Kind: types.ProviderKindLLM, URL: "https://api.example.invalid/v1",
				Models: []types.ModelConfig{{Name: "model-a"}}},
		},
		WorkerProvider: "4",
	}); err != nil {
		t.Fatalf("update config: %v", err)
	}
	if _, _, ok := cs.GetWorkerTarget(); ok {
		t.Fatal("a provider with no model named is a target")
	}

	// A stale worker_provider left by a deleted provider must not send a task to a URL nobody configured.
	if err := cs.UpdateConfig(&types.Config{
		Providers:      []types.Provider{{ID: 4, Name: "primary", Kind: types.ProviderKindLLM, URL: "https://api.example.invalid/v1"}},
		WorkerProvider: "99",
		WorkerModel:    "model-a",
	}); err != nil {
		t.Fatalf("update config: %v", err)
	}
	if _, _, ok := cs.GetWorkerTarget(); ok {
		t.Fatal("a worker reference no provider answers is a target")
	}
}

func TestGetWorkerTargetResolvesThePairByReference(t *testing.T) {
	cs := testConfigStore(t)
	if err := cs.UpdateConfig(&types.Config{
		Providers: []types.Provider{
			{ID: 4, Name: "primary", Kind: types.ProviderKindLLM, URL: "https://api.example.invalid/v1",
				Models: []types.ModelConfig{{Name: "model-a"}, {Name: "model-b"}}},
		},
		WorkerProvider: "4",
		WorkerModel:    "model-b",
	}); err != nil {
		t.Fatalf("update config: %v", err)
	}
	url, model, ok := cs.GetWorkerTarget()
	if !ok || url != "https://api.example.invalid/v1" || model != "model-b" {
		t.Errorf("GetWorkerTarget() = (%q, %q, %v), want the worker provider's URL and the worker model", url, model, ok)
	}

	// An old row stores the provider by name rather than by ID.
	if err := cs.UpdateConfig(&types.Config{
		Providers: []types.Provider{
			{ID: 4, Name: "primary", Kind: types.ProviderKindLLM, URL: "https://api.example.invalid/v1",
				Models: []types.ModelConfig{{Name: "model-a"}, {Name: "model-b"}}},
		},
		WorkerProvider: "primary",
		WorkerModel:    "model-a",
	}); err != nil {
		t.Fatalf("update config: %v", err)
	}
	url, model, ok = cs.GetWorkerTarget()
	if !ok || url != "https://api.example.invalid/v1" || model != "model-a" {
		t.Errorf("GetWorkerTarget() by name = (%q, %q, %v), want the same pair", url, model, ok)
	}
}

// An agent worker names no model in the config row, so the pair resolves from the registry row alone.
func TestGetWorkerTargetTakesTheAgentItselfAsTheModel(t *testing.T) {
	cs := testConfigStore(t)
	cs.SetAgentRegistry(&mockAgentLister{
		providers: []types.Provider{{Name: "pi", Kind: types.ProviderKindAgent, DisplayName: "Pi Coding Agent", URL: "agent://pi", Concurrency: 1}},
	})
	if err := cs.UpdateConfig(&types.Config{WorkerProvider: "pi"}); err != nil {
		t.Fatalf("update config: %v", err)
	}
	url, model, ok := cs.GetWorkerTarget()
	if !ok || url != "agent://pi" || model != "pi" {
		t.Errorf("GetWorkerTarget() = (%q, %q, %v), want agent://pi and pi", url, model, ok)
	}
}
