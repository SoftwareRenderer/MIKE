package agent

import (
	"fmt"
	"os"
	"sync"

	ext "mike/backend/extensions"
	"mike/backend/types"
	"mike/pkg/logging"
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
	name, ok := types.ParseAgentURL(url)
	if !ok {
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
			URL:         types.AgentProviderURL(ext.Name),
			Concurrency: 1, // agents are single-concurrency by default
			Models:      []types.ModelConfig{},
		}
		providers = append(providers, provider)
	}
	return providers
}

// GetAgentSandbox returns the sandbox config for a specific agent extension.
// It reads sandbox_mount and sandbox_env arrays from extension config,
// falling back to manifest defaults. Provider config (AgentConfigDir) can
// override the host_path of the first mount.
//
// Host paths are expanded for leading ~.
// Returns nil if no mounts are configured for the extension.
func (r *AgentRegistry) GetAgentSandbox(agentName string) *types.AgentSandbox {
	r.mu.RLock()
	ext, ok := r.agents[agentName]
	r.mu.RUnlock()

	if !ok || !ext.Enabled {
		return nil
	}

	def := ext.Manifest.Capabilities.GetAgent()
	if def == nil {
		return nil
	}

	// Start with resolved manifest defaults
	var sb types.AgentSandbox
	if def.Sandbox != nil {
		resolved := def.ResolveSandboxTemplates(ext.Manifest.ConfigSchema)
		if resolved != nil {
			sb = *resolved
		}
	}

	// Read sandbox_mount array from extension config (overrides manifest)
	if mountsRaw, ok := ext.Config["sandbox_mount"]; ok {
		if mounts, err := parseMountArray(mountsRaw); err == nil && len(mounts) > 0 {
			sb.Mounts = mounts
		}
	}

	// Read sandbox_env array from extension config (overrides manifest)
	if envsRaw, ok := ext.Config["sandbox_env"]; ok {
		if envs, err := parseEnvArray(envsRaw); err == nil {
			sb.Envs = envs
		}
	}

	// Provider-level override of host_path on first mount (AgentConfigDir)
	if r.configStore != nil && len(sb.Mounts) > 0 {
		if provider := r.configStore.GetAgentProvider(); provider != nil && provider.AgentConfigDir != "" {
			sb.Mounts[0].HostPath = provider.AgentConfigDir
		}
	}

	// Expand leading ~ to home directory
	for i := range sb.Mounts {
		if len(sb.Mounts[i].HostPath) >= 2 && sb.Mounts[i].HostPath[0] == '~' && sb.Mounts[i].HostPath[1] == '/' {
			if homeDir, err := os.UserHomeDir(); err == nil {
				sb.Mounts[i].HostPath = homeDir + sb.Mounts[i].HostPath[1:]
			}
		}
		if sb.Mounts[i].Mode != "ro" && sb.Mounts[i].Mode != "rw" {
			sb.Mounts[i].Mode = "ro"
		}
	}

	if len(sb.Mounts) == 0 {
		return nil
	}

	return &sb
}

// parseObjectArray parses a JSON array of objects from extension config
func parseObjectArray[T any](v any, parse func(any) (T, error)) ([]T, error) {
	src, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("expected array, got %T", v)
	}
	items := make([]T, 0, len(src))
	for _, item := range src {
		parsed, err := parse(item)
		if err != nil {
			return nil, err
		}
		items = append(items, parsed)
	}
	return items, nil
}

// parseMountArray parses a JSON array of mount objects from extension config.
func parseMountArray(v any) ([]types.SandboxMount, error) {
	return parseObjectArray(v, parseMountObject)
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
	return parseObjectArray(v, parseEnvObject)
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

// GetAnyAgentSandbox returns the sandbox config for the first enabled agent extension.
// Used by sandbox terminals and FS operations that need to mount agent config
// for any configured agent (not a specific task's agent).
// Returns nil if no agent extension with a sandbox config is enabled, or if r is nil.
func (r *AgentRegistry) GetAnyAgentSandbox() *types.AgentSandbox {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, name := range r.enabled {
		if sb := r.GetAgentSandbox(name); sb != nil {
			return sb
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
		logging.For(logging.Agent).Warnf("failed to query agent extensions: %v", err)
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
