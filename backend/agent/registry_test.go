package agent

import (
	"os"
	"path/filepath"
	"testing"

	ext "mike/backend/extensions"
	"mike/backend/types"
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
	if registry.LookupByName("pi") == nil {
		t.Fatal("expected pi to be found")
	}

	// Simulate disabling pi
	store.extensions[0].Enabled = false

	// Reload should pick up the change
	registry.Reload()

	if registry.LookupByName("pi") != nil {
		t.Error("expected pi to be nil after reload (disabled)")
	}

	// Simulate enabling a new agent
	store.extensions = append(store.extensions, ext.Extension{
		Name:    "new-agent",
		Type:    ext.ExtensionTypeAgent,
		Enabled: true,
	})

	registry.Reload()

	if registry.LookupByName("new-agent") == nil {
		t.Error("expected new-agent to be found after reload")
	}
}

// TestRegistry_EmptyStore tests behavior with no agent extensions.
func TestRegistry_EmptyStore(t *testing.T) {
	store := &mockExtensionStore{extensions: []ext.Extension{}}
	registry := NewAgentRegistry(store, nil)

	if registry.LookupByName("anything") != nil {
		t.Error("expected nil lookup on empty store")
	}

	if providers := registry.ToProviders(); len(providers) != 0 {
		t.Errorf("expected empty providers, got %d", len(providers))
	}
}

// TestRegistry_GetAgentSandboxNeedsGrant: a manifest that names a folder does not give the agent that
// folder.
func TestRegistry_GetAgentSandboxNeedsGrant(t *testing.T) {
	declares := ext.Extension{
		Name:    "pi",
		Type:    ext.ExtensionTypeAgent,
		Enabled: true,
		Config:  map[string]any{},
		Manifest: ext.ExtensionManifest{
			Version: "1.0.0",
			Capabilities: &ext.Capabilities{Agent: &ext.AgentConfig{
				Sandbox: &types.AgentSandbox{
					Mounts: []types.SandboxMount{
						{HostPath: "~/.agent/config", ContainerPath: "/root/agent/config", Mode: "rw"},
					},
					Envs: []types.SandboxEnv{{Key: "AGENT_CONFIG_DIR", Value: "/root/agent/config"}},
				},
			}},
		},
	}

	registry := NewAgentRegistry(&mockExtensionStore{extensions: []ext.Extension{declares}}, nil)
	if sb := registry.GetAgentSandbox("pi"); sb != nil {
		t.Errorf("GetAgentSandbox = %+v, want nil: the manifest's mount is a proposal, not a grant", sb)
	}

	// The same extension after the user approved the folder.
	granted := declares
	granted.Config = map[string]any{
		"sandbox_mount": []any{
			map[string]any{"host_path": "~/.agent/config", "container_path": "/root/agent/config", "mode": "rw"},
		},
	}
	registry = NewAgentRegistry(&mockExtensionStore{extensions: []ext.Extension{granted}}, nil)
	sb := registry.GetAgentSandbox("pi")
	if sb == nil {
		t.Fatal("expected the saved grant to be honoured")
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
	if sb.Mounts[0].Mode != "rw" {
		t.Errorf("Mounts[0].Mode = %q, want rw as the person chose it", sb.Mounts[0].Mode)
	}
	if len(sb.Envs) != 1 || sb.Envs[0].Key != "AGENT_CONFIG_DIR" {
		t.Errorf("Envs = %+v, want the manifest's AGENT_CONFIG_DIR: an env grants no host access", sb.Envs)
	}
}

// TestRegistry_GetAgentSandboxRefusesUnshapelyGrant: a saved approval is checked again before podman sees
// it. A container path containing ":" would turn a read-only mount into a read-write one.
func TestRegistry_GetAgentSandboxRefusesUnshapelyGrant(t *testing.T) {
	store := &mockExtensionStore{extensions: []ext.Extension{{
		Name:    "pi",
		Type:    ext.ExtensionTypeAgent,
		Enabled: true,
		Config: map[string]any{"sandbox_mount": []any{
			map[string]any{"host_path": t.TempDir(), "container_path": "/mnt/x:rw", "mode": "ro"},
			map[string]any{"host_path": "relative/path", "container_path": "/mnt/ok", "mode": "ro"},
			map[string]any{"host_path": t.TempDir(), "container_path": "/mnt/ok2", "mode": "ro"},
		}},
		Manifest: ext.ExtensionManifest{Version: "1.0.0", Capabilities: &ext.Capabilities{Agent: &ext.AgentConfig{}}},
	}}}

	sb := NewAgentRegistry(store, nil).GetAgentSandbox("pi")
	if sb == nil || len(sb.Mounts) != 1 {
		t.Fatalf("GetAgentSandbox = %+v, want only the well-shaped mount", sb)
	}
	if sb.Mounts[0].ContainerPath != "/mnt/ok2" {
		t.Errorf("kept %q, want /mnt/ok2", sb.Mounts[0].ContainerPath)
	}
}

// TestRegistry_GetAgentSandboxRefusesADangerousHostPath: an approval saved before today's checks existed is
// still refused.
func TestRegistry_GetAgentSandboxRefusesADangerousHostPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory to point a grant at")
	}
	newStore := func(hostPath string) *mockExtensionStore {
		return &mockExtensionStore{extensions: []ext.Extension{{
			Name: "pi", Type: ext.ExtensionTypeAgent, Enabled: true,
			Config: map[string]any{"sandbox_mount": []any{
				map[string]any{"host_path": hostPath, "container_path": "/mnt/keys", "mode": "rw"},
			}},
			Manifest: ext.ExtensionManifest{Version: "1.0.0", Capabilities: &ext.Capabilities{Agent: &ext.AgentConfig{}}},
		}}}
	}

	if sb := NewAgentRegistry(newStore("~/.ssh"), nil).GetAgentSandbox("pi"); sb != nil {
		t.Errorf("GetAgentSandbox = %+v, want nil: a credential store is not grantable", sb)
	}
	if sb := NewAgentRegistry(newStore(home), nil).GetAgentSandbox("pi"); sb != nil {
		t.Errorf("GetAgentSandbox = %+v, want nil: a home directory is not grantable", sb)
	}

	// The provider's config folder points at a folder of keys, so the replacement is checked like any other
	// host folder.
	granted := newStore("~/.agent/config")
	cs := &mockConfigStore{agentProvider: &types.Provider{Name: "pi", AgentConfigDir: filepath.Join(home, ".ssh")}}
	if sb := NewAgentRegistry(granted, cs).GetAgentSandbox("pi"); sb != nil {
		t.Errorf("GetAgentSandbox = %+v, want nil: agent_config_dir cannot smuggle a credential store in", sb)
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

// TestRegistry_GetAgentSandboxProviderOverride: the provider's config folder replaces a folder the user has
// already approved, and cannot create an approval of its own.
func TestRegistry_GetAgentSandboxProviderOverride(t *testing.T) {
	grants := map[string]any{
		"sandbox_mount": []any{
			map[string]any{"host_path": "~/.agent/config", "container_path": "/root/agent/config", "mode": "ro"},
		},
	}
	newStore := func(config map[string]any) *mockExtensionStore {
		return &mockExtensionStore{extensions: []ext.Extension{{
			Name: "pi", Type: ext.ExtensionTypeAgent, Enabled: true, Config: config,
			Manifest: ext.ExtensionManifest{Version: "1.0.0", Capabilities: &ext.Capabilities{Agent: &ext.AgentConfig{
				Sandbox: &types.AgentSandbox{Mounts: []types.SandboxMount{
					{HostPath: "~/.agent/config", ContainerPath: "/root/agent/config", Mode: "ro"},
				}},
			}}},
		}}}
	}
	configStore := &mockConfigStore{agentProvider: &types.Provider{
		Name: "pi", AgentConfigDir: "/custom/pi/config",
	}}

	sb := NewAgentRegistry(newStore(grants), configStore).GetAgentSandbox("pi")
	if sb == nil || len(sb.Mounts) != 1 {
		t.Fatalf("GetAgentSandbox = %+v, want the one granted mount", sb)
	}
	if sb.Mounts[0].HostPath != "/custom/pi/config" {
		t.Errorf("Mounts[0].HostPath = %q, want /custom/pi/config (from provider override)", sb.Mounts[0].HostPath)
	}

	// The same config folder, with nothing approved, mounts nothing.
	if sb := NewAgentRegistry(newStore(map[string]any{}), configStore).GetAgentSandbox("pi"); sb != nil {
		t.Errorf("GetAgentSandbox = %+v, want nil: agent_config_dir cannot create a mount", sb)
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

// TestRegistry_AgentSandboxForURL: a task gets the folders approved for the agent it runs, and none from
// any other extension.
func TestRegistry_AgentSandboxForURL(t *testing.T) {
	store := &mockExtensionStore{
		extensions: []ext.Extension{
			{
				Name:    "pi",
				Type:    ext.ExtensionTypeAgent,
				Enabled: true,
				Config: map[string]any{"sandbox_mount": []any{
					map[string]any{"host_path": "~/.agent/config", "container_path": "/x", "mode": "ro"},
				}},
				Manifest: ext.ExtensionManifest{Version: "1.0.0",
					Capabilities: &ext.Capabilities{Agent: &ext.AgentConfig{}}},
			},
		},
	}
	registry := NewAgentRegistry(store, nil)

	if sb := registry.AgentSandboxForURL("agent://pi"); sb == nil || len(sb.Mounts) != 1 {
		t.Errorf("AgentSandboxForURL(agent://pi) = %+v, want the granted mount", sb)
	}
	if sb := registry.AgentSandboxForURL("http://localhost:8080/v1"); sb != nil {
		t.Errorf("AgentSandboxForURL(http://…) = %+v, want nil", sb)
	}
	if sb := registry.AgentSandboxForURL("agent://other"); sb != nil {
		t.Errorf("AgentSandboxForURL(agent://other) = %+v, want nil for an agent that is not installed", sb)
	}
}

// TestRegistry_GetAgentSandboxTemplates: the shipped manifest writes its sandbox block with {{property}}
// placeholders, and filling those in still gives the agent no folder.
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
	if sb := registry.GetAgentSandbox("pi"); sb != nil {
		t.Errorf("GetAgentSandbox = %+v, want nil: the resolved template is the proposal the settings form shows, not a mount",
			sb)
	}

	// The user saves what the form offered. The environment variables still come from the manifest's
	// placeholders, because that is how the agent finds its own folder.
	store.extensions[0].Config = map[string]any{"sandbox_mount": []any{
		map[string]any{"host_path": "~/.pi/agent", "container_path": "/root/.pi/agent", "mode": "rw"},
	}}
	sb := NewAgentRegistry(store, nil).GetAgentSandbox("pi")
	if sb == nil {
		t.Fatal("expected the saved grant to be honoured")
	}
	if len(sb.Mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(sb.Mounts))
	}
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
		t.Errorf("Envs = %+v, want the template-resolved key HOME", sb.Envs)
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

// TestRegistry_EmittedProviderURLsParseBack holds the URL scheme together in both directions:
// every provider URL ToProviders emits parses back through types.ParseAgentURL to the extension it
// came from, and every enabled agent extension is emitted. A URL the parser refuses is a provider
// the UI can select and no task can run, and a missing row is an installed agent nobody can pick.
func TestRegistry_EmittedProviderURLsParseBack(t *testing.T) {
	store := &mockExtensionStore{
		extensions: []ext.Extension{
			{Name: "pi", Type: ext.ExtensionTypeAgent, Enabled: true},
			{Name: "claude-code", Type: ext.ExtensionTypeAgent, Enabled: true},
			{Name: "disabled-agent", Type: ext.ExtensionTypeAgent, Enabled: false},
			{Name: "gopls", Type: ext.ExtensionTypeLSP, Enabled: true},
		},
	}
	registry := NewAgentRegistry(store, nil)

	providers := registry.ToProviders()
	emitted := make(map[string]bool, len(providers))
	for _, p := range providers {
		emitted[p.Name] = true
		name, ok := types.ParseAgentURL(p.URL)
		if !ok {
			t.Errorf("provider %q has URL %q that ParseAgentURL refuses", p.Name, p.URL)
			continue
		}
		if name != p.Name {
			t.Errorf("ParseAgentURL(%q) = %q, want the provider's own name %q", p.URL, name, p.Name)
		}
		if got := registry.LookupByName(name); got == nil || got.Name != p.Name {
			t.Errorf("%q named extension %v, want %q", p.URL, got, p.Name)
		}
	}

	// Expected straight from the fixture, so the check does not lean on the registry to describe
	// itself: every enabled agent row of any kind is the input a provider row must come from.
	for _, e := range store.extensions {
		if e.Type != ext.ExtensionTypeAgent || !e.Enabled {
			continue
		}
		if !emitted[e.Name] {
			t.Errorf("enabled agent %q has no provider row", e.Name)
		}
	}
	for _, forbidden := range []string{"disabled-agent", "gopls"} {
		if emitted[forbidden] {
			t.Errorf("provider list emitted %q, which is not an enabled agent extension", forbidden)
		}
	}
}
