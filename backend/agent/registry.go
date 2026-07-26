package agent

import (
	"fmt"
	"os"
	"strings"
	"sync"

	ext "kanban/backend/extensions"
	"kanban/backend/types"
)

// ConfigStore is the interface the registry needs for user-configured agent settings.
type ConfigStore interface {
	GetAgentProvider() *types.Provider
}

// ExtensionStore is the interface the registry needs from the extension store.
type ExtensionStore interface {
	GetByType(extType string) ([]ext.Extension, error)
}

// AgentRegistry discovers enabled agent extensions and maps agent://<name> URLs to extensions.
// Agent extensions auto-register as providers via ToProviders().
type AgentRegistry struct {
	mu          sync.RWMutex
	store       ExtensionStore
	configStore ConfigStore
	agents      map[string]*ext.Extension // name -> extension
	enabled     []string                  // names of enabled agents
}

// NewAgentRegistry creates a new registry that discovers agent extensions from the store.
func NewAgentRegistry(store ExtensionStore, configStore ConfigStore) *AgentRegistry {
	r := &AgentRegistry{
		store:       store,
		configStore: configStore,
		agents:      make(map[string]*ext.Extension),
	}
	r.reload()
	return r
}

// Lookup resolves an agent://<name> URL to the matching extension.
// Returns nil if no matching agent extension is found.
func (r *AgentRegistry) Lookup(url string) *ext.Extension {
	name := strings.TrimPrefix(url, "agent://")
	if name == url {
		return nil // not an agent URL
	}
	name = strings.TrimSuffix(name, "/")
	if name == "" {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	ext, ok := r.agents[name]
	if !ok || !ext.Enabled {
		return nil
	}
	return ext
}

// LookupByName resolves an agent by extension name.
func (r *AgentRegistry) LookupByName(name string) *ext.Extension {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ext, ok := r.agents[name]
	if !ok || !ext.Enabled {
		return nil
	}
	return ext
}

// List returns all enabled agent extensions.
func (r *AgentRegistry) List() []ext.Extension {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]ext.Extension, 0, len(r.enabled))
	for _, name := range r.enabled {
		if ext, ok := r.agents[name]; ok && ext.Enabled {
			result = append(result, *ext)
		}
	}
	return result
}

// ToProviders converts agent extensions to provider entries for the UI.
// Each agent extension becomes a provider with URL agent://<name>.
func (r *AgentRegistry) ToProviders() []types.Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	providers := make([]types.Provider, 0, len(r.enabled))
	for _, name := range r.enabled {
		ext, ok := r.agents[name]
		if !ok || !ext.Enabled {
			continue
		}

		provider := types.Provider{
			Name:        ext.Name,
			DisplayName: ext.Manifest.DisplayName,
			Kind:        types.ProviderKindAgent,
			URL:         fmt.Sprintf("agent://%s", ext.Name),
			Concurrency: 1, // agents are single-concurrency by default
		}
		providers = append(providers, provider)
	}
	return providers
}

// GetAgentConfigMount returns the sandbox mount for a specific agent extension.
// It reads sandbox_mounts and sandbox_envs arrays from extension config,
// falling back to manifest defaults. Provider config (AgentConfigDir) can
// override the host_path of the first mount.
//
// The host_path is expanded for leading ~.
// Returns nil if no mounts are configured for the extension.
func (r *AgentRegistry) GetAgentConfigMount(agentName string) *types.AgentConfigMount {
	r.mu.RLock()
	ext, ok := r.agents[agentName]
	r.mu.RUnlock()

	if !ok || !ext.Enabled {
		return nil
	}

	def := ext.Manifest.Agent
	if def == nil {
		return nil
	}

	// Start with manifest default mount
	var mount types.AgentConfigMount
	if def.SandboxMount != nil {
		mount = *def.ResolveSandboxMountTemplates(ext.Manifest.ConfigSchema)
	}

	// Read sandbox_mounts array from extension config
	if mountsRaw, ok := ext.Config["sandbox_mounts"]; ok {
		if mounts, err := parseMountArray(mountsRaw); err == nil && len(mounts) > 0 {
			m := mounts[0]
			mount.HostPath = m.HostPath
			mount.ContainerPath = m.ContainerPath
			mount.Mode = m.Mode
		}
	}

	// Read sandbox_envs array from extension config
	if envsRaw, ok := ext.Config["sandbox_envs"]; ok {
		if envs, err := parseEnvArray(envsRaw); err == nil {
			mount.Envs = envs
		}
	}

	// Provider-level override of host_path (AgentConfigDir)
	if r.configStore != nil {
		if provider := r.configStore.GetAgentProvider(); provider != nil && provider.AgentConfigDir != "" {
			mount.HostPath = provider.AgentConfigDir
		}
	}

	// Expand leading ~ to home directory
	if len(mount.HostPath) >= 2 && mount.HostPath[0] == '~' && mount.HostPath[1] == '/' {
		if homeDir, err := os.UserHomeDir(); err == nil {
			mount.HostPath = homeDir + mount.HostPath[1:]
		}
	}

	// Ensure mode has a value
	if mount.Mode != "ro" && mount.Mode != "rw" {
		mount.Mode = "ro"
	}

	// Return nil if no host path set
	if mount.HostPath == "" {
		return nil
	}

	return &mount
}

// parseMountArray parses a JSON array of mount objects from extension config.
func parseMountArray(v any) ([]types.SandboxMount, error) {
	switch src := v.(type) {
	case []any:
		var mounts []types.SandboxMount
		for _, item := range src {
			m, err := parseMountObject(item)
			if err != nil {
				return nil, err
			}
			mounts = append(mounts, m)
		}
		return mounts, nil
	default:
		return nil, fmt.Errorf("expected array, got %T", v)
	}
}

func parseMountObject(v any) (types.SandboxMount, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return types.SandboxMount{}, fmt.Errorf("expected object, got %T", v)
	}
	hp, _ := m["host_path"].(string)
	cp, _ := m["container_path"].(string)
	mode, _ := m["mode"].(string)
	if mode == "" {
		mode = "rw"
	}
	return types.SandboxMount{HostPath: hp, ContainerPath: cp, Mode: mode}, nil
}

// parseEnvArray parses a JSON array of env objects from extension config.
func parseEnvArray(v any) ([]types.SandboxEnv, error) {
	switch src := v.(type) {
	case []any:
		var envs []types.SandboxEnv
		for _, item := range src {
			e, err := parseEnvObject(item)
			if err != nil {
				return nil, err
			}
			envs = append(envs, e)
		}
		return envs, nil
	default:
		return nil, fmt.Errorf("expected array, got %T", v)
	}
}

func parseEnvObject(v any) (types.SandboxEnv, error) {
	e, ok := v.(map[string]any)
	if !ok {
		return types.SandboxEnv{}, fmt.Errorf("expected object, got %T", v)
	}
	key, _ := e["key"].(string)
	value, _ := e["value"].(string)
	return types.SandboxEnv{Key: key, Value: value}, nil
}

// GetConfigMount returns the config mount for the first enabled agent extension.
// Used by sandbox terminals and FS operations that need to mount agent config
// for any configured agent (not a specific task's agent).
// Returns nil if no agent extension with a sandbox_mount is enabled.
func (r *AgentRegistry) GetConfigMount() *types.AgentConfigMount {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, name := range r.enabled {
		if mount := r.GetAgentConfigMount(name); mount != nil {
			return mount
		}
	}
	return nil
}

// Reload re-scans the extension store for enabled agent extensions.
// Called after extension enable/disable/install/delete.
func (r *AgentRegistry) Reload() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reload()
}

// reload is the unlocked version of Reload.
func (r *AgentRegistry) reload() {
	// Must be called with r.mu held (write lock)
	extList, err := r.store.GetByType(ext.ExtensionTypeAgent)
	if err != nil {
		return
	}

	r.agents = make(map[string]*ext.Extension, len(extList))
	r.enabled = r.enabled[:0]

	for i := range extList {
		ext := &extList[i]
		r.agents[ext.Name] = ext
		if ext.Enabled {
			r.enabled = append(r.enabled, ext.Name)
		}
	}
}
