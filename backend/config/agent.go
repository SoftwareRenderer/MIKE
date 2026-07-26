package config

import "kanban/backend/types"

// HasAgentProvider returns true if any coding agent is configured.
func (cs *ConfigStore) HasAgentProvider() bool {
	cfg, err := cs.GetConfig()
	if err != nil {
		return false
	}
	for i := range cfg.Providers {
		if cfg.Providers[i].IsAgent() {
			return true
		}
	}
	return false
}

// GetAgentProvider returns the first agent provider from config, or nil if not configured.
func (cs *ConfigStore) GetAgentProvider() *types.Provider {
	cfg, err := cs.GetConfig()
	if err != nil {
		return nil
	}
	for i := range cfg.Providers {
		if cfg.Providers[i].IsAgent() {
			return &cfg.Providers[i]
		}
	}
	return nil
}
