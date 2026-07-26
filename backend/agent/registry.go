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
// It uses the manifest's default sandbox_mount, with overrides from:
//   - Extension config (runtime override of mode/host_path/container_path/envs)
//   - Provider config (AgentConfigDir override of host_path)
//
// The host_path is expanded for leading ~.
// Returns nil if no sandbox_mount is defined for the extension.
func (r *AgentRegistry) GetAgentConfigMount(agentName string) *types.AgentConfigMount {
	r.mu.RLock()
	ext, ok := r.agents[agentName]
	r.mu.RUnlock()

	if !ok || !ext.Enabled {
		return nil
	}

	def := ext.Manifest.Agent
	if def == nil || def.SandboxMount == nil {
		return nil
	}

	// Resolve {{property}} template references in sandbox_mount using config_schema defaults
	mount := *def.ResolveSandboxMountTemplates(ext.Manifest.ConfigSchema) // copy

	// Override mode from extension runtime config
	if mode, ok := ext.Config["sandbox_mount_mode"].(string); ok && mode != "" {
		mount.Mode = mode
	}
	// Override host_path from extension runtime config
	if hp, ok := ext.Config["sandbox_mount_host_path"].(string); ok && hp != "" {
		mount.HostPath = hp
	}
	// Override container_path from extension runtime config
	if cp, ok := ext.Config["sandbox_mount_container_path"].(string); ok && cp != "" {
		mount.ContainerPath = cp
	}
	// Override envs from extension runtime config (empty key disables)
	if ek, ok := ext.Config["sandbox_mount_env_key"].(string); ok && ek != "" {
		if ev, ok := ext.Config["sandbox_mount_env_value"].(string); ok {
			mount.Envs = []types.SandboxEnv{{Key: ek, Value: ev}}
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

	return &mount
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
