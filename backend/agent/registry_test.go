package agent

import (
	"testing"

	ext "kanban/backend/extensions"
	"kanban/backend/types"
)

// mockExtensionStore implements a minimal ExtensionStore for testing.
type mockExtensionStore struct {
	extensions []ext.Extension
}

func (m *mockExtensionStore) GetByType(extType string) ([]ext.Extension, error) {
	var result []ext.Extension
	for _, ext := range m.extensions {
		if extType == "" || ext.Type == extType {
			result = append(result, ext)
		}
	}
	return result, nil
}

func (m *mockExtensionStore) List() ([]ext.Extension, error) {
	return m.extensions, nil
}

// TestRegistry_Lookup tests URL resolution.
func TestRegistry_Lookup(t *testing.T) {
	store := &mockExtensionStore{
		extensions: []ext.Extension{
			{
				Name:    "pi",
				Type:    ext.ExtensionTypeAgent,
				Enabled: true,
				Manifest: ext.ExtensionManifest{
					DisplayName: "Pi Coding Agent",
					Agent:       &ext.AgentConfig{WasmModule: "pi.wasm"},
				},
			},
			{
				Name:    "disabled-agent",
				Type:    ext.ExtensionTypeAgent,
				Enabled: false,
			},
			{
				Name: "lsp-server",
				Type: ext.ExtensionTypeLSP,
				Enabled: true,
			},
		},
	}

	registry := NewAgentRegistry(store)

	// Valid agent URL
	ext := registry.Lookup("agent://pi")
	if ext == nil {
		t.Fatal("Lookup(agent://pi) returned nil")
	}
	if ext.Name != "pi" {
		t.Errorf("Name = %q, want %q", ext.Name, "pi")
	}

	// Disabled agent
	ext = registry.Lookup("agent://disabled-agent")
	if ext != nil {
		t.Errorf("Lookup(agent://disabled-agent) = %v, want nil", ext)
	}

	// Non-agent URL
	ext = registry.Lookup("http://example.com")
	if ext != nil {
		t.Errorf("Lookup(http://...) = %v, want nil", ext)
	}

	// Non-existent agent
	ext = registry.Lookup("agent://nonexistent")
	if ext != nil {
		t.Errorf("Lookup(agent://nonexistent) = %v, want nil", ext)
	}

	// Agent URL with trailing slash
	ext = registry.Lookup("agent://pi/")
	if ext == nil {
		t.Fatal("Lookup(agent://pi/) returned nil")
	}
	if ext.Name != "pi" {
		t.Errorf("Name = %q, want %q", ext.Name, "pi")
	}
}

// TestRegistry_LookupByName tests name-based lookup.
func TestRegistry_LookupByName(t *testing.T) {
	store := &mockExtensionStore{
		extensions: []ext.Extension{
			{Name: "pi", Type: ext.ExtensionTypeAgent, Enabled: true},
			{Name: "other", Type: ext.ExtensionTypeAgent, Enabled: false},
		},
	}

	registry := NewAgentRegistry(store)

	ext := registry.LookupByName("pi")
	if ext == nil {
		t.Fatal("LookupByName(pi) returned nil")
	}

	ext = registry.LookupByName("other")
	if ext != nil {
		t.Errorf("LookupByName(other) = %v, want nil (disabled)", ext)
	}

	ext = registry.LookupByName("nonexistent")
	if ext != nil {
		t.Errorf("LookupByName(nonexistent) = %v, want nil", ext)
	}
}

// TestRegistry_List tests listing enabled agents.
func TestRegistry_List(t *testing.T) {
	store := &mockExtensionStore{
		extensions: []ext.Extension{
			{Name: "pi", Type: ext.ExtensionTypeAgent, Enabled: true},
			{Name: "agent2", Type: ext.ExtensionTypeAgent, Enabled: true},
			{Name: "disabled", Type: ext.ExtensionTypeAgent, Enabled: false},
			{Name: "lsp", Type: ext.ExtensionTypeLSP, Enabled: true},
		},
	}

	registry := NewAgentRegistry(store)
	agents := registry.List()

	if len(agents) != 2 {
		t.Fatalf("List() returned %d agents, want 2", len(agents))
	}

	names := make(map[string]bool)
	for _, a := range agents {
		names[a.Name] = true
	}
	if !names["pi"] || !names["agent2"] {
		t.Errorf("List() names = %v, want pi and agent2", names)
	}
}

// TestRegistry_ToProviders tests provider conversion.
func TestRegistry_ToProviders(t *testing.T) {
	store := &mockExtensionStore{
		extensions: []ext.Extension{
			{
				Name:    "pi",
				Type:    ext.ExtensionTypeAgent,
				Enabled: true,
				Manifest: ext.ExtensionManifest{
					DisplayName: "Pi Coding Agent",
				},
			},
			{
				Name:    "disabled",
				Type:    ext.ExtensionTypeAgent,
				Enabled: false,
			},
		},
	}

	registry := NewAgentRegistry(store)
	providers := registry.ToProviders()

	if len(providers) != 1 {
		t.Fatalf("ToProviders() returned %d providers, want 1", len(providers))
	}

	p := providers[0]
	if p.Name != "pi" {
		t.Errorf("Name = %q, want %q", p.Name, "pi")
	}
	if p.DisplayName != "Pi Coding Agent" {
		t.Errorf("DisplayName = %q, want %q", p.DisplayName, "Pi Coding Agent")
	}
	if p.Kind != types.ProviderKindAgent {
		t.Errorf("Kind = %q, want %q", p.Kind, types.ProviderKindAgent)
	}
	if p.URL != "agent://pi" {
		t.Errorf("URL = %q, want %q", p.URL, "agent://pi")
	}
	if p.Concurrency != 1 {
		t.Errorf("Concurrency = %d, want 1", p.Concurrency)
	}
}

// TestRegistry_Reload tests that reload picks up changes.
func TestRegistry_Reload(t *testing.T) {
	store := &mockExtensionStore{
		extensions: []ext.Extension{
			{Name: "pi", Type: ext.ExtensionTypeAgent, Enabled: true},
		},
	}

	registry := NewAgentRegistry(store)

	// Initially has pi
	if registry.Lookup("agent://pi") == nil {
		t.Fatal("expected pi to be found")
	}

	// Simulate disabling pi
	store.extensions[0].Enabled = false

	// Reload should pick up the change
	registry.Reload()

	if registry.Lookup("agent://pi") != nil {
		t.Error("expected pi to be nil after reload (disabled)")
	}

	// Simulate enabling a new agent
	store.extensions = append(store.extensions, ext.Extension{
		Name:   "new-agent",
		Type:   ext.ExtensionTypeAgent,
		Enabled: true,
	})

	registry.Reload()

	if registry.Lookup("agent://new-agent") == nil {
		t.Error("expected new-agent to be found after reload")
	}
}

// TestRegistry_EmptyStore tests behavior with no agent extensions.
func TestRegistry_EmptyStore(t *testing.T) {
	store := &mockExtensionStore{extensions: []ext.Extension{}}
	registry := NewAgentRegistry(store)

	if registry.Lookup("agent://anything") != nil {
		t.Error("expected nil lookup on empty store")
	}

	if agents := registry.List(); len(agents) != 0 {
		t.Errorf("expected empty list, got %d", len(agents))
	}

	if providers := registry.ToProviders(); len(providers) != 0 {
		t.Errorf("expected empty providers, got %d", len(providers))
	}
}
