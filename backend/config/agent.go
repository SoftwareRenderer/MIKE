package config

import (
	"os"

	"kanban/backend/types"
)

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

// GetAgentConfigDir returns the config directory path for the first configured
// coding agent. Expands leading ~ to the user's home directory.
// For agents without an explicit config dir, defaults to ~/.pi/agent.
// Returns empty string if no agent is configured.
func (cs *ConfigStore) GetAgentConfigDir() string {
	cfg, err := cs.GetConfig()
	if err != nil {
		return ""
	}
	for i := range cfg.Providers {
		if !cfg.Providers[i].IsAgent() {
			continue
		}
		path := cfg.Providers[i].AgentConfigDir
		// Default config dir for agents without explicit setting
		if path == "" {
			path = "~/.pi/agent"
		}
		// Expand leading ~ to home directory
		if len(path) >= 2 && path[0] == '~' && path[1] == '/' {
			if homeDir, err := os.UserHomeDir(); err == nil {
				path = homeDir + path[1:]
			}
		}
		return path
	}
	return ""
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
