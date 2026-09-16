package config

import "mike/backend/types"

// GetAgentProvider returns the first configured agent provider
func (cs *ConfigStore) GetAgentProvider() *types.Provider {
	cfg, err := cs.GetConfig()
	if err == nil {
		for i := range cfg.Providers {
			if cfg.Providers[i].IsAgent() {
				return &cfg.Providers[i]
			}
		}
	}
	if providers := cs.agentProviders(); len(providers) > 0 {
		return &providers[0]
	}
	return nil
}
