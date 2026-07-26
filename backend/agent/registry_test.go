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
					DisplayName: "My Coding Agent",
					Agent:       &ext.AgentConfig{WasmModule: "pi.wasm"},
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

// TestRegistry_GetAgentConfigMount tests config mount resolution from manifest.
func TestRegistry_GetAgentConfigMount(t *testing.T) {
	store := &mockExtensionStore{
		extensions: []ext.Extension{
			{
				Name:    "pi",
				Type:    ext.ExtensionTypeAgent,
				Enabled: true,
				Config:  map[string]any{},
				Manifest: ext.ExtensionManifest{
					Agent: &ext.AgentConfig{
						SandboxMount: &types.AgentConfigMount{
							HostPath:      "~/.agent/config",
							ContainerPath: "/root/agent/config",
							Mode:          "ro",
							Envs:          []types.SandboxEnv{{Key: "AGENT_CONFIG_DIR", Value: "~/.agent/config"}},
						},
					},
				},
			},
		},
	}

	// Without config store: should return manifest defaults with ~ expanded
	registry := NewAgentRegistry(store, nil)
	mount := registry.GetAgentConfigMount("pi")
	if mount == nil {
		t.Fatal("expected non-nil mount")
	}
	expectedHost := ""
	if homeDir, err := os.UserHomeDir(); err == nil {
		expectedHost = homeDir + "/.agent/config"
	}
	if mount.HostPath != expectedHost {
		t.Errorf("HostPath = %q, want %q", mount.HostPath, expectedHost)
	}
	if mount.ContainerPath != "/root/agent/config" {
		t.Errorf("ContainerPath = %q, want /root/agent/config", mount.ContainerPath)
	}
	if mount.Mode != "ro" {
		t.Errorf("Mode = %q, want ro", mount.Mode)
	}
	if len(mount.Envs) != 1 || mount.Envs[0].Key != "AGENT_CONFIG_DIR" {
		t.Errorf("Envs = %+v, want key AGENT_CONFIG_DIR", mount.Envs)
	}

	// Disabled agent should return nil
	mount2 := registry.GetAgentConfigMount("disabled")
	if mount2 != nil {
		t.Error("expected nil for non-existent agent")
	}
}

// TestRegistry_GetAgentConfigMountWithOverrides tests config store + extension config overrides.
func TestRegistry_GetAgentConfigMountWithOverrides(t *testing.T) {
	store := &mockExtensionStore{
		extensions: []ext.Extension{
			{
				Name:    "pi",
				Type:    ext.ExtensionTypeAgent,
				Enabled: true,
				Config: map[string]any{
					"sandbox_mounts": []any{
						map[string]any{"host_path": "~/.agent/config", "container_path": "/root/agent/config", "mode": "rw"},
					},
				},
				Manifest: ext.ExtensionManifest{
					Agent: &ext.AgentConfig{
						SandboxMount: &types.AgentConfigMount{
							HostPath:      "~/.default/config",
							ContainerPath: "/root/default/config",
							Mode:          "ro",
						},
					},
				},
			},
		},
	}

	// With sandbox_mounts array override: mode should be "rw" and paths from config
	registry := NewAgentRegistry(store, nil)
	mount := registry.GetAgentConfigMount("pi")
	if mount == nil {
		t.Fatal("expected non-nil mount")
	}
	if mount.Mode != "rw" {
		t.Errorf("Mode = %q, want rw (from extension config)", mount.Mode)
	}
	if mount.HostPath != "/home/mwren/.agent/config" {
		t.Errorf("HostPath = %q, want expanded home path", mount.HostPath)
	}
}

// TestRegistry_GetAgentConfigMountProviderOverride tests provider-level host_path override.
func TestRegistry_GetAgentConfigMountProviderOverride(t *testing.T) {
	store := &mockExtensionStore{
		extensions: []ext.Extension{
			{
				Name:    "pi",
				Type:    ext.ExtensionTypeAgent,
				Enabled: true,
				Config:  map[string]any{},
				Manifest: ext.ExtensionManifest{
					Agent: &ext.AgentConfig{
						SandboxMount: &types.AgentConfigMount{
							HostPath:      "~/.agent/config",
							ContainerPath: "/root/agent/config",
							Mode:          "ro",
						},
					},
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
	mount := registry.GetAgentConfigMount("pi")
	if mount == nil {
		t.Fatal("expected non-nil mount")
	}
	if mount.HostPath != "/custom/pi/config" {
		t.Errorf("HostPath = %q, want /custom/pi/config (from provider override)", mount.HostPath)
	}
}

// TestRegistry_GetAgentConfigMountNoManifest tests when extension has no sandbox_mount.
func TestRegistry_GetAgentConfigMountNoManifest(t *testing.T) {
	store := &mockExtensionStore{
		extensions: []ext.Extension{
			{
				Name:    "no-mount-agent",
				Type:    ext.ExtensionTypeAgent,
				Enabled: true,
				Manifest: ext.ExtensionManifest{
					Agent: &ext.AgentConfig{
						WasmModule: "test.wasm",
					},
				},
			},
		},
	}

	registry := NewAgentRegistry(store, nil)
	mount := registry.GetAgentConfigMount("no-mount-agent")
	if mount != nil {
		t.Error("expected nil when no sandbox_mount in manifest")
	}
}

// TestRegistry_GetConfigMount tests getting mount for first enabled agent.
func TestRegistry_GetConfigMount(t *testing.T) {
	store := &mockExtensionStore{
		extensions: []ext.Extension{
			{
				Name:    "pi",
				Type:    ext.ExtensionTypeAgent,
				Enabled: true,
				Manifest: ext.ExtensionManifest{
					Agent: &ext.AgentConfig{
						SandboxMount: &types.AgentConfigMount{
							HostPath: "~/.agent/config",
							Mode:     "ro",
						},
					},
				},
			},
		},
	}

	registry := NewAgentRegistry(store, nil)
	mount := registry.GetConfigMount()
	if mount == nil {
		t.Fatal("expected non-nil mount")
	}
	if mount.Mode != "ro" {
		t.Errorf("Mode = %q, want ro", mount.Mode)
	}
}

// TestRegistry_GetAgentConfigMountTemplates tests that {{property}} template
// references in sandbox_mount are resolved using config_schema defaults.
func TestRegistry_GetAgentConfigMountTemplates(t *testing.T) {
	store := &mockExtensionStore{
		extensions: []ext.Extension{
			{
				Name:    "pi",
				Type:    ext.ExtensionTypeAgent,
				Enabled: true,
				Config:  map[string]any{},
				Manifest: ext.ExtensionManifest{
					ConfigSchema: &ext.ConfigSchema{
						Properties: []ext.SchemaProp{
							{Key: "sandbox_mounts", Type: "mount", Default: []any{
								map[string]any{"host_path": "~/.pi/agent", "container_path": "/root/.pi/agent", "mode": "rw"},
							}},
							{Key: "sandbox_envs", Type: "env", Default: []any{
								map[string]any{"key": "HOME", "value": "~/.pi/agent"},
							}},
						},
					},
					Agent: &ext.AgentConfig{
						SandboxMount: &types.AgentConfigMount{
							HostPath:      "~/.pi/agent",
							ContainerPath: "/root/.pi/agent",
							Mode:          "rw",
							Envs:          []types.SandboxEnv{{Key: "HOME", Value: "~/.pi/agent"}},
						},
					},
				},
			},
		},
	}

	registry := NewAgentRegistry(store, nil)
	mount := registry.GetAgentConfigMount("pi")
	if mount == nil {
		t.Fatal("expected non-nil mount")
	}
	// ~ is expanded to home directory by GetAgentConfigMount
	expectedHost := "~/.pi/agent"
	if homeDir, err := os.UserHomeDir(); err == nil {
		expectedHost = homeDir + "/.pi/agent"
	}
	if mount.HostPath != expectedHost {
		t.Errorf("HostPath = %q, want %q", mount.HostPath, expectedHost)
	}
	if mount.ContainerPath != "/root/.pi/agent" {
		t.Errorf("ContainerPath = %q, want /root/.pi/agent", mount.ContainerPath)
	}
	if mount.Mode != "rw" {
		t.Errorf("Mode = %q, want rw", mount.Mode)
	}
	if len(mount.Envs) != 1 || mount.Envs[0].Key != "HOME" {
		t.Errorf("Envs = %+v, want key HOME", mount.Envs)
	}
}

// TestRegistry_GetAgentConfigMountTemplatesWithOverride tests that runtime config
// overrides take precedence over resolved template values.
func TestRegistry_GetAgentConfigMountTemplatesWithOverride(t *testing.T) {
	store := &mockExtensionStore{
		extensions: []ext.Extension{
			{
				Name:    "pi",
				Type:    ext.ExtensionTypeAgent,
				Enabled: true,
				Config: map[string]any{
					"sandbox_mounts": []any{
						map[string]any{"host_path": "/custom/path", "container_path": "/root/.pi/agent", "mode": "ro"},
					},
				},
				Manifest: ext.ExtensionManifest{
					ConfigSchema: &ext.ConfigSchema{
						Properties: []ext.SchemaProp{
							{Key: "sandbox_mounts", Type: "mount", Default: []any{
								map[string]any{"host_path": "~/.pi/agent", "container_path": "/root/.pi/agent", "mode": "rw"},
							}},
						},
					},
					Agent: &ext.AgentConfig{
						SandboxMount: &types.AgentConfigMount{
							HostPath:      "~/.pi/agent",
							ContainerPath: "/root/.pi/agent",
							Mode:          "rw",
							Envs:          nil,
						},
					},
				},
			},
		},
	}

	registry := NewAgentRegistry(store, nil)
	mount := registry.GetAgentConfigMount("pi")
	if mount == nil {
		t.Fatal("expected non-nil mount")
	}
	// Runtime override should take precedence over resolved template
	if mount.HostPath != "/custom/path" {
		t.Errorf("HostPath = %q, want /custom/path (from runtime override)", mount.HostPath)
	}
	if mount.Mode != "ro" {
		t.Errorf("Mode = %q, want ro (from runtime override)", mount.Mode)
	}
	// Container path comes from sandbox_mounts array config
	if mount.ContainerPath != "/root/.pi/agent" {
		t.Errorf("ContainerPath = %q, want /root/.pi/agent (from config)", mount.ContainerPath)
	}
}

// TestRegistry_GetAgentConfigMountProviderOverride tests provider-level host_path override.
