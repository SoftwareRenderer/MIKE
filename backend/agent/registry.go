package agent

import (
	"fmt"
	"strings"
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

// LookupByName resolves an agent by extension name. A caller holding an agent://<name> provider URL
// parses it first: types.ParseAgentURL owns the grammar, this owns the enabled check.
func (r *AgentRegistry) LookupByName(name string) *ext.Extension {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ext, ok := r.agents[name]
	if !ok || !ext.Enabled {
		return nil
	}
	return ext
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

// GetAgentSandbox answers nil until the user approves a folder in Settings > Extensions: the mount
// list written in the manifest is only a suggestion and is never used here.
func (r *AgentRegistry) GetAgentSandbox(agentName string) *types.AgentSandbox {
	r.mu.RLock()
	installed, ok := r.agents[agentName]
	r.mu.RUnlock()

	if !ok || !installed.Enabled {
		return nil
	}

	def := installed.Manifest.Capabilities.GetAgent()
	if def == nil {
		return nil
	}

	mountsRaw, ok := installed.Config[ext.AgentMountConfigKey]
	if !ok {
		return nil
	}
	mounts, err := ext.ParseAgentMountGrant(mountsRaw)
	if err != nil {
		logging.For(logging.Agent).Warnf("agent=%s ignored its saved sandbox mounts: %v", agentName, err)
		return nil
	}
	var granted []types.SandboxMount
	for _, m := range mounts {
		m.HostPath = ext.ExpandHostPath(m.HostPath)
		if m.Mode != "ro" && m.Mode != "rw" {
			m.Mode = "ro"
		}
		granted = append(granted, m)
	}

	// The agent config folder from the provider settings replaces the host folder of a mount the user has
	// already approved. It never adds a mount of its own.
	if dir := r.agentConfigDir(); dir != "" {
		if len(granted) > 0 {
			logging.For(logging.Agent).Infof("agent=%s mount %q takes its host path from the provider's config directory %q",
				agentName, granted[0].ContainerPath, dir)
			granted[0].HostPath = dir
		} else {
			logging.For(logging.Agent).Warnf("agent=%s ignores agent_config_dir %q: mounting it needs a sandbox mount granted under Settings → Extensions",
				agentName, dir)
		}
	}

	var sb types.AgentSandbox
	for _, m := range granted {
		if err := refuseMount(m); err != nil {
			logging.For(logging.Agent).Warnf("agent=%s refused mount %q: %v", agentName, m.HostPath, err)
			continue
		}
		sb.Mounts = append(sb.Mounts, m)
	}
	if len(sb.Mounts) == 0 {
		return nil
	}

	// The environment variables from the manifest name folders inside the container that only exist once a
	// host folder has been approved, so they are returned with the mounts and never on their own.
	if def.Sandbox != nil {
		if resolved := def.ResolveSandboxTemplates(installed.Manifest.ConfigSchema); resolved != nil {
			sb.Envs = resolved.Envs
		}
	}
	if envsRaw, ok := installed.Config["sandbox_env"]; ok {
		if envs, err := parseEnvArray(envsRaw); err == nil {
			sb.Envs = envs
		}
	}
	return &sb
}

// A saved approval can be older than the checks that exist today, and the agent config folder from the
// provider settings is a second way a host path can end up in a mount.
func refuseMount(m types.SandboxMount) error {
	if err := types.ValidateSandboxMount(m); err != nil {
		return err
	}
	return ext.CheckGrantedHostPath(m.HostPath)
}

func (r *AgentRegistry) AgentSandboxForURL(providerURL string) *types.AgentSandbox {
	if r == nil {
		return nil
	}
	name, ok := types.ParseAgentURL(providerURL)
	if !ok {
		return nil
	}
	return r.GetAgentSandbox(name)
}

func (r *AgentRegistry) agentConfigDir() string {
	if r.configStore == nil {
		return ""
	}
	provider := r.configStore.GetAgentProvider()
	if provider == nil {
		return ""
	}
	return ext.ExpandHostPath(strings.TrimSpace(provider.AgentConfigDir))
}

// parseEnvArray parses a JSON array of env objects from extension config.
func parseEnvArray(v any) ([]types.SandboxEnv, error) {
	items, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("expected array, got %T", v)
	}
	envs := make([]types.SandboxEnv, 0, len(items))
	for _, item := range items {
		e, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("expected object, got %T", item)
		}
		key, _ := e["key"].(string)
		value, _ := e["value"].(string)
		envs = append(envs, types.SandboxEnv{Key: key, Value: value})
	}
	return envs, nil
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
