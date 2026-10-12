package agent

import (
	"fmt"
	"strings"
	"sync"

	ext "mike/backend/extensions"
	"mike/backend/types"
	"mike/pkg/logging"
)

// ConfigStore interface for user-configured agent settings.
type ConfigStore interface {
	GetAgentProvider() *types.Provider
}

// ExtensionStore interface for extension discovery.
type ExtensionStore interface {
	GetByType(extType string) ([]ext.Extension, error)
}

// AgentRegistry discovers enabled agent extensions and maps agent://<name> URLs.
type AgentRegistry struct {
	mu          sync.RWMutex
	store       ExtensionStore
	configStore ConfigStore
	agents      map[string]*ext.Extension // name -> extension
	enabled     []string                  // names of enabled agents
}

func NewAgentRegistry(store ExtensionStore, configStore ConfigStore) *AgentRegistry {
	r := &AgentRegistry{
		store:       store,
		configStore: configStore,
		agents:      make(map[string]*ext.Extension),
	}
	r.reload()
	return r
}

func (r *AgentRegistry) LookupByName(name string) *ext.Extension {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ext, ok := r.agents[name]
	if !ok || !ext.Enabled {
		return nil
	}
	return ext
}

// ToProviders converts agents to provider entries for the UI.
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

// GetAgentSandbox returns sandbox config if mounts are approved in Settings > Extensions.
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

	// provider config directory replaces host path of an approved mount.
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

	// manifest environment variables are returned only if mounts are approved.
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

// refuseMount validates mount and checks if host path is approved.
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

// parseEnvArray parses JSON array of env objects.
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

// Reload re-scans store for agent extensions.
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
