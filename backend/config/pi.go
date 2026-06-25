package config

import (
	"net/http"
	"os"
	"os/exec"

	"kanban/backend/httputil"
	"kanban/backend/types"
)

// PiAvailabilityResponse reports whether the `pi` binary is available on PATH.
type PiAvailabilityResponse struct {
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
}

// CheckPiAvailable runs `pi --version` and returns whether Pi is installed.
func CheckPiAvailable() PiAvailabilityResponse {
	cmd := exec.Command("pi", "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return PiAvailabilityResponse{Available: false}
	}
	return PiAvailabilityResponse{
		Available: true,
		Version:   string(out),
	}
}

// HandlePiAvailability handles GET /api/config/pi-available.
func (cs *ConfigStore) HandlePiAvailability(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result := CheckPiAvailable()
	httputil.JSONResponse(w, http.StatusOK, result)
}

// HandleTestPiConnection handles POST /api/config/test-pi-connection.
// Runs `pi --version` to verify Pi is accessible.
func (cs *ConfigStore) HandleTestPiConnection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result := CheckPiAvailable()
	resp := TestConnectionResponse{
		Success: result.Available,
		Message: result.Version,
	}
	if !result.Available {
		resp.Message = "pi binary not found on PATH"
	}
	httputil.JSONResponse(w, http.StatusOK, resp)
}

// GetPiProvider returns the Pi provider from config, or nil if not configured.
func (cs *ConfigStore) GetPiProvider() *types.Provider {
	cfg, err := cs.GetConfig()
	if err != nil {
		return nil
	}
	for i := range cfg.Providers {
		if cfg.Providers[i].IsPi() {
			return &cfg.Providers[i]
		}
	}
	return nil
}

// HasPiProvider returns true if a Pi agent is configured.
func (cs *ConfigStore) HasPiProvider() bool {
	return cs.GetPiProvider() != nil
}

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
// For Pi providers without an explicit config dir, defaults to ~/.pi/agent.
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
		// Default for Pi providers without explicit config dir
		if path == "" && cfg.Providers[i].IsPi() {
			path = "~/.pi/agent"
		}
		if path == "" {
			continue
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
