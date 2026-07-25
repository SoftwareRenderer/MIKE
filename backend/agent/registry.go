package agent

import (
	"fmt"
	"strings"
	"sync"

	ext "kanban/backend/extensions"
	"kanban/backend/types"
)

// ExtensionStore is the interface the registry needs from the extension store.
type ExtensionStore interface {
	GetByType(extType string) ([]ext.Extension, error)
}

// AgentRegistry discovers enabled agent extensions and maps agent://<name> URLs to extensions.
// Agent extensions auto-register as providers via ToProviders().
type AgentRegistry struct {
	mu      sync.RWMutex
	store   ExtensionStore
	agents  map[string]*ext.Extension // name -> extension
	enabled []string                  // names of enabled agents
}

// NewAgentRegistry creates a new registry that discovers agent extensions from the store.
func NewAgentRegistry(store ExtensionStore) *AgentRegistry {
	r := &AgentRegistry{
		store:  store,
		agents: make(map[string]*ext.Extension),
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
