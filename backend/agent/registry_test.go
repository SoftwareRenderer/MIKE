package agent

import (
	"os"
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

// mockConfigStore implements ConfigStore for testing.
type mockConfigStore struct {
	agentProvider *types.Provider
}

func (m *mockConfigStore) GetAgentProvider() *types.Provider {
	return m.agentProvider
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
					DisplayName:  "My Coding Agent",
					Version:      "1.0.0",
					Capabilities: &ext.Capabilities{Agent: &ext.AgentConfig{}},
				},
			},
			{
				Name:    "disabled-agent",
				Type:    ext.ExtensionTypeAgent,
				Enabled: false,
			},
			{
				Name:    "lsp-server",
				Type:    ext.ExtensionTypeLSP,
				Enabled: true,
			},
		},
	}

	registry := NewAgentRegistry(store, nil)

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

	registry := NewAgentRegistry(store, nil)

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

	registry := NewAgentRegistry(store, nil)
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
					DisplayName: "My Coding Agent",
				},
			},
			{
				Name:    "disabled",
				Type:    ext.ExtensionTypeAgent,
				Enabled: false,
			},
		},
	}

	registry := NewAgentRegistry(store, nil)
	providers := registry.ToProviders()

	if len(providers) != 1 {
		t.Fatalf("ToProviders() returned %d providers, want 1", len(providers))
	}

	p := providers[0]
	if p.Name != "pi" {
		t.Errorf("Name = %q, want %q", p.Name, "pi")
	}
	if p.DisplayName != "My Coding Agent" {
		t.Errorf("DisplayName = %q, want %q", p.DisplayName, "My Coding Agent")
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
	// Models must be non-nil so the config API serializes [] (not null).
	if p.Models == nil {
		t.Errorf("Models = nil, want non-nil empty slice")
	}
}

// TestRegistry_Reload tests that reload picks up changes.
func TestRegistry_Reload(t *testing.T) {
	store := &mockExtensionStore{
		extensions: []ext.Extension{
			{Name: "pi", Type: ext.ExtensionTypeAgent, Enabled: true},
		},
	}

	registry := NewAgentRegistry(store, nil)

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
		Name:    "new-agent",
		Type:    ext.ExtensionTypeAgent,
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
	registry := NewAgentRegistry(store, nil)

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

// TestRegistry_GetAgentSandbox tests sandbox config resolution from manifest.
func TestRegistry_GetAgentSandbox(t *testing.T) {
	store := &mockExtensionStore{
		extensions: []ext.Extension{
			{
				Name:    "pi",
				Type:    ext.ExtensionTypeAgent,
				Enabled: true,
				Config:  map[string]any{},
				Manifest: ext.ExtensionManifest{
					Version: "1.0.0",
					Capabilities: &ext.Capabilities{Agent: &ext.AgentConfig{
						Sandbox: &types.AgentSandbox{
							Mounts: []types.SandboxMount{
								{HostPath: "~/.agent/config", ContainerPath: "/root/agent/config", Mode: "ro"},
							},
							Envs: []types.SandboxEnv{{Key: "AGENT_CONFIG_DIR", Value: "~/.agent/config"}},
						},
					}},
				},
			},
		},
	}

	// Without config store: should return manifest defaults with ~ expanded
	registry := NewAgentRegistry(store, nil)
	sb := registry.GetAgentSandbox("pi")
	if sb == nil {
		t.Fatal("expected non-nil sandbox")
	}
	if len(sb.Mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(sb.Mounts))
	}
	expectedHost := ""
	if homeDir, err := os.UserHomeDir(); err == nil {
		expectedHost = homeDir + "/.agent/config"
	}
	if sb.Mounts[0].HostPath != expectedHost {
		t.Errorf("Mounts[0].HostPath = %q, want %q", sb.Mounts[0].HostPath, expectedHost)
	}
	if sb.Mounts[0].ContainerPath != "/root/agent/config" {
		t.Errorf("Mounts[0].ContainerPath = %q, want /root/agent/config", sb.Mounts[0].ContainerPath)
	}
	if sb.Mounts[0].Mode != "ro" {
		t.Errorf("Mounts[0].Mode = %q, want ro", sb.Mounts[0].Mode)
	}
	if len(sb.Envs) != 1 || sb.Envs[0].Key != "AGENT_CONFIG_DIR" {
		t.Errorf("Envs = %+v, want key AGENT_CONFIG_DIR", sb.Envs)
	}

	// Non-existent agent should return nil
	sb2 := registry.GetAgentSandbox("disabled")
	if sb2 != nil {
		t.Error("expected nil for non-existent agent")
	}
}

// TestRegistry_GetAgentSandboxWithOverrides tests extension config overrides.
func TestRegistry_GetAgentSandboxWithOverrides(t *testing.T) {
	store := &mockExtensionStore{
		extensions: []ext.Extension{
			{
				Name:    "pi",
				Type:    ext.ExtensionTypeAgent,
				Enabled: true,
				Config: map[string]any{
					"sandbox_mount": []any{
						map[string]any{"host_path": "~/.agent/config", "container_path": "/root/agent/config", "mode": "rw"},
					},
				},
				Manifest: ext.ExtensionManifest{
					Version: "1.0.0",
					Capabilities: &ext.Capabilities{Agent: &ext.AgentConfig{
						Sandbox: &types.AgentSandbox{
							Mounts: []types.SandboxMount{
								{HostPath: "~/.default/config", ContainerPath: "/root/default/config", Mode: "ro"},
							},
						},
					}},
				},
			},
		},
	}

	// With sandbox_mount array override: mode should be "rw" and paths from config
	registry := NewAgentRegistry(store, nil)
	sb := registry.GetAgentSandbox("pi")
	if sb == nil {
		t.Fatal("expected non-nil sandbox")
	}
	if len(sb.Mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(sb.Mounts))
	}
	if sb.Mounts[0].Mode != "rw" {
		t.Errorf("Mounts[0].Mode = %q, want rw (from extension config)", sb.Mounts[0].Mode)
	}
	expectedHost := ""
	if homeDir, err := os.UserHomeDir(); err == nil {
		expectedHost = homeDir + "/.agent/config"
	}
	if sb.Mounts[0].HostPath != expectedHost {
		t.Errorf("Mounts[0].HostPath = %q, want %q", sb.Mounts[0].HostPath, expectedHost)
	}
}

// TestRegistry_GetAgentSandboxProviderOverride tests provider-level host_path override.
func TestRegistry_GetAgentSandboxProviderOverride(t *testing.T) {
	store := &mockExtensionStore{
		extensions: []ext.Extension{
			{
				Name:    "pi",
				Type:    ext.ExtensionTypeAgent,
				Enabled: true,
				Config:  map[string]any{},
				Manifest: ext.ExtensionManifest{
					Version: "1.0.0",
					Capabilities: &ext.Capabilities{Agent: &ext.AgentConfig{
						Sandbox: &types.AgentSandbox{
							Mounts: []types.SandboxMount{
								{HostPath: "~/.agent/config", ContainerPath: "/root/agent/config", Mode: "ro"},
							},
						},
					}},
				},
			},
		},
	}

	// With config store that has AgentConfigDir override
	configStore := &mockConfigStore{
		agentProvider: &types.Provider{
			Name:           "pi",
			AgentConfigDir: "/custom/pi/config",
		},
	}
	registry := NewAgentRegistry(store, configStore)
	sb := registry.GetAgentSandbox("pi")
	if sb == nil {
		t.Fatal("expected non-nil sandbox")
	}
	if len(sb.Mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(sb.Mounts))
	}
	if sb.Mounts[0].HostPath != "/custom/pi/config" {
		t.Errorf("Mounts[0].HostPath = %q, want /custom/pi/config (from provider override)", sb.Mounts[0].HostPath)
	}
}

// TestRegistry_GetAgentSandboxNoManifest tests when extension has no sandbox.
func TestRegistry_GetAgentSandboxNoManifest(t *testing.T) {
	store := &mockExtensionStore{
		extensions: []ext.Extension{
			{
				Name:    "no-mount-agent",
				Type:    ext.ExtensionTypeAgent,
				Enabled: true,
				Manifest: ext.ExtensionManifest{
					Version:      "1.0.0",
					Capabilities: &ext.Capabilities{Agent: &ext.AgentConfig{}},
				},
			},
		},
	}

	registry := NewAgentRegistry(store, nil)
	sb := registry.GetAgentSandbox("no-mount-agent")
	if sb != nil {
		t.Error("expected nil when no sandbox in manifest")
	}
}

// TestRegistry_GetAnyAgentSandbox tests getting sandbox for first enabled agent.
func TestRegistry_GetAnyAgentSandbox(t *testing.T) {
	store := &mockExtensionStore{
		extensions: []ext.Extension{
			{
				Name:    "pi",
				Type:    ext.ExtensionTypeAgent,
				Enabled: true,
				Manifest: ext.ExtensionManifest{
					Version: "1.0.0",
					Capabilities: &ext.Capabilities{Agent: &ext.AgentConfig{
						Sandbox: &types.AgentSandbox{
							Mounts: []types.SandboxMount{{HostPath: "~/.agent/config", ContainerPath: "/x", Mode: "ro"}},
						},
					}},
				},
			},
		},
	}

	registry := NewAgentRegistry(store, nil)
	sb := registry.GetAnyAgentSandbox()
	if sb == nil {
		t.Fatal("expected non-nil sandbox")
	}
	if len(sb.Mounts) != 1 || sb.Mounts[0].Mode != "ro" {
		t.Errorf("Mounts[0].Mode = %q, want ro", sb.Mounts[0].Mode)
	}
}

// TestRegistry_GetAgentSandboxTemplates tests that {{property}} template
// references in sandbox are resolved using config_schema defaults.
func TestRegistry_GetAgentSandboxTemplates(t *testing.T) {
	store := &mockExtensionStore{
		extensions: []ext.Extension{
			{
				Name:    "pi",
				Type:    ext.ExtensionTypeAgent,
				Enabled: true,
				Config:  map[string]any{},
				Manifest: ext.ExtensionManifest{
					Version: "1.0.0",
					ConfigSchema: &ext.ConfigSchema{
						Properties: []ext.SchemaProp{
							{Key: "sandbox_mount", Type: "mount", Default: []any{
								map[string]any{"host_path": "~/.pi/agent", "container_path": "/root/.pi/agent", "mode": "rw"},
							}},
							{Key: "sandbox_env", Type: "env", Default: []any{
								map[string]any{"key": "HOME", "value": "~/.pi/agent"},
							}},
						},
					},
					Capabilities: &ext.Capabilities{Agent: &ext.AgentConfig{
						Sandbox: &types.AgentSandbox{
							Mounts: []types.SandboxMount{
								{HostPath: "{{sandbox_mount_host_path[]}}", ContainerPath: "{{sandbox_mount_container_path[]}}", Mode: "{{sandbox_mount_mode[]}}"},
							},
							Envs: []types.SandboxEnv{{Key: "{{sandbox_env_key[]}}", Value: "{{sandbox_env_value[]}}"}},
						},
					}},
				},
			},
		},
	}

	registry := NewAgentRegistry(store, nil)
	sb := registry.GetAgentSandbox("pi")
	if sb == nil {
		t.Fatal("expected non-nil sandbox")
	}
	if len(sb.Mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(sb.Mounts))
	}
	// ~ is expanded to home directory by GetAgentSandbox
	expectedHost := "~/.pi/agent"
	if homeDir, err := os.UserHomeDir(); err == nil {
		expectedHost = homeDir + "/.pi/agent"
	}
	if sb.Mounts[0].HostPath != expectedHost {
		t.Errorf("Mounts[0].HostPath = %q, want %q", sb.Mounts[0].HostPath, expectedHost)
	}
	if sb.Mounts[0].ContainerPath != "/root/.pi/agent" {
		t.Errorf("Mounts[0].ContainerPath = %q, want /root/.pi/agent", sb.Mounts[0].ContainerPath)
	}
	if sb.Mounts[0].Mode != "rw" {
		t.Errorf("Mounts[0].Mode = %q, want rw", sb.Mounts[0].Mode)
	}
	if len(sb.Envs) != 1 || sb.Envs[0].Key != "HOME" {
		t.Errorf("Envs = %+v, want key HOME", sb.Envs)
	}
}

// TestRegistry_GetAgentSandboxTemplatesWithOverride tests that runtime config
// overrides take precedence over resolved template values.
func TestRegistry_GetAgentSandboxTemplatesWithOverride(t *testing.T) {
	store := &mockExtensionStore{
		extensions: []ext.Extension{
			{
				Name:    "pi",
				Type:    ext.ExtensionTypeAgent,
				Enabled: true,
				Config: map[string]any{
					"sandbox_mount": []any{
						map[string]any{"host_path": "/custom/path", "container_path": "/root/.pi/agent", "mode": "ro"},
					},
				},
				Manifest: ext.ExtensionManifest{
					Version: "1.0.0",
					ConfigSchema: &ext.ConfigSchema{
						Properties: []ext.SchemaProp{
							{Key: "sandbox_mount", Type: "mount", Default: []any{
								map[string]any{"host_path": "~/.pi/agent", "container_path": "/root/.pi/agent", "mode": "rw"},
							}},
						},
					},
					Capabilities: &ext.Capabilities{Agent: &ext.AgentConfig{
						Sandbox: &types.AgentSandbox{
							Mounts: []types.SandboxMount{
								{HostPath: "{{sandbox_mount_host_path[]}}", ContainerPath: "{{sandbox_mount_container_path[]}}", Mode: "{{sandbox_mount_mode[]}}"},
							},
						},
					}},
				},
			},
		},
	}

	registry := NewAgentRegistry(store, nil)
	sb := registry.GetAgentSandbox("pi")
	if sb == nil {
		t.Fatal("expected non-nil sandbox")
	}
	if len(sb.Mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(sb.Mounts))
	}
	// Runtime override should take precedence over resolved template
	if sb.Mounts[0].HostPath != "/custom/path" {
		t.Errorf("Mounts[0].HostPath = %q, want /custom/path (from runtime override)", sb.Mounts[0].HostPath)
	}
	if sb.Mounts[0].Mode != "ro" {
		t.Errorf("Mounts[0].Mode = %q, want ro (from runtime override)", sb.Mounts[0].Mode)
	}
	// Container path comes from sandbox_mount array config
	if sb.Mounts[0].ContainerPath != "/root/.pi/agent" {
		t.Errorf("Mounts[0].ContainerPath = %q, want /root/.pi/agent (from config)", sb.Mounts[0].ContainerPath)
	}
}
