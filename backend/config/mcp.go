package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"mike/backend/types"
	"mike/pkg/logging"
)

// The MCP registry is one config row holding a JSON array, read the way providers are: the row keeps
// every secret sealed and the config view hands out none of them.

// EnvRestrictMCP names the operator switch that freezes the registry. This package owns the refusal
// sentence; backend/startup holds the copy the ladder reads, and startup.TestEnvNamesMatchTheirOwners
// holds the two together.
const EnvRestrictMCP = "MIKE_RESTRICT_MCP"

// ErrMCPLocked refuses writes that change the registry while frozen, and Test probes that dial
// user-typed values. The sentence names the variable and what remains writable.
var ErrMCPLocked = fmt.Errorf(
	"the MCP registry is frozen by %s: a server cannot be added, edited, removed or tested from Settings, "+
		"while a project can still enable or disable a server on the list under Project Defaults", EnvRestrictMCP)

// mcpKeyPurpose is the record kind an MCP header value is sealed under, so a value sealed for some
// other record kind cannot open here.
const mcpKeyPurpose = "mcp-header"

const mcpServersRow = "mcp_servers"

// parseMCPServers warns if the row is unreadable; a missing registry only disables MCP tools.
func parseMCPServers(raw string) []types.MCPServer {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var servers []types.MCPServer
	if err := json.Unmarshal([]byte(raw), &servers); err != nil {
		logging.For(logging.MCP).Warnf("the %s row is unreadable and is read as empty: %v", mcpServersRow, err)
		return nil
	}
	return servers
}

// copyMCPServers deep-copies the list so a caller that opens a header cannot write through to the
// snapshot's stored copy.
func copyMCPServers(src []types.MCPServer) []types.MCPServer {
	if src == nil {
		return nil
	}
	out := make([]types.MCPServer, len(src))
	copy(out, src)
	for i := range out {
		if out[i].Headers != nil {
			out[i].Headers = append([]types.MCPHeader{}, out[i].Headers...)
		}
	}
	return out
}

// storedMCPServers is used for read-modify-write to avoid dropping values from GetConfig.
func (cs *ConfigStore) storedMCPServers() ([]types.MCPServer, error) {
	snap, err := cs.snapshot()
	if err != nil {
		return nil, err
	}
	return copyMCPServers(snap.mcpServers), nil
}

// OpenMCPServers is the only place a stored MCP secret is opened; it never hands out placeholders.
func (cs *ConfigStore) OpenMCPServers() ([]types.MCPServer, error) {
	servers, err := cs.storedMCPServers()
	if err != nil {
		return nil, err
	}
	for i := range servers {
		for j := range servers[i].Headers {
			h := &servers[i].Headers[j]
			if h.Value == nil {
				continue
			}
			open, err := cs.openMCPHeader(servers[i].ID, h.Name, *h.Value)
			if err != nil {
				// An unopenable value is no value: the server is dialled without that header and the run
				// continues.
				logging.For(logging.MCP).Warnf("mcp header %q for server %s did not open: %s", h.Name, servers[i].ID, err)
				h.Value = nil
				continue
			}
			value := open
			h.Value = &value
		}
	}
	return servers, nil
}

// openMCPHeader returns the value as-is if it is not sealed.
func (cs *ConfigStore) openMCPHeader(serverID, name, sealed string) (string, error) {
	if cs.box == nil {
		return "", fmt.Errorf("no encryption key configured")
	}
	record := mcpRecord(serverID, name)
	if !cs.box.Opens(mcpKeyPurpose, record, sealed) {
		return sealed, nil
	}
	return cs.box.Decrypt(mcpKeyPurpose, record, sealed)
}

// mcpRecord is the key a sealed header is bound to, and the key every lookup of a stored header uses. A
// value copied to another server's row or another header's slot does not open.
func mcpRecord(serverID, name string) string {
	return serverID + "\x00" + strings.ToLower(name)
}

// maskMCPServerSecrets allows Settings to show a token is set without receiving it. The mask list
// comes from the stored row, not the list being masked.
func maskMCPServerSecrets(servers []types.MCPServer, stored []types.MCPServer) {
	if len(servers) == 0 {
		return
	}
	set := make(map[string]bool, len(stored))
	for _, s := range stored {
		for _, h := range s.Headers {
			if h.Value != nil && *h.Value != "" {
				set[mcpRecord(s.ID, h.Name)] = true
			}
		}
	}
	for i := range servers {
		for j := range servers[i].Headers {
			h := &servers[i].Headers[j]
			if !set[mcpRecord(servers[i].ID, h.Name)] {
				continue
			}
			masked := maskedAPIKey
			h.Value = &masked
		}
	}
}

// prepareMCPServers ensures every server is validated and secrets are sealed before writing.
func (cs *ConfigStore) prepareMCPServers(incoming []types.MCPServer) ([]types.MCPServer, error) {
	stored, err := cs.storedMCPServers()
	if err != nil {
		return nil, err
	}
	storedValues := make(map[string]string, len(stored)*2)
	for _, s := range stored {
		for _, h := range s.Headers {
			if h.Value != nil && *h.Value != "" {
				storedValues[mcpRecord(s.ID, h.Name)] = *h.Value
			}
		}
	}
	if err := validateMCPServers(incoming); err != nil {
		return nil, err
	}

	out := make([]types.MCPServer, len(incoming))
	for i := range incoming {
		s := incoming[i]
		if s.ID == "" {
			id, err := types.NewMCPServerID()
			if err != nil {
				return nil, fmt.Errorf("generate MCP server id: %w", err)
			}
			s.ID = id
		}
		s.Name = strings.TrimSpace(s.Name)
		s.URL = strings.TrimSpace(s.URL)
		s.URL = strings.TrimRight(s.URL, "/")

		headers := make([]types.MCPHeader, 0, len(s.Headers))
		for _, h := range s.Headers {
			name := strings.TrimSpace(h.Name)
			switch {
			case h.Value == nil || *h.Value == maskedAPIKey:
				keep, ok := storedValues[mcpRecord(s.ID, name)]
				if !ok {
					continue
				}
				value := keep
				h.Value = &value
			case *h.Value == "":
				continue
			default:
				if cs.box == nil {
					return nil, fmt.Errorf("no encryption key configured: cannot store the %q header for MCP server %q", name, s.Name)
				}
				sealed, err := cs.box.Encrypt(mcpKeyPurpose, mcpRecord(s.ID, name), *h.Value)
				if err != nil {
					return nil, fmt.Errorf("seal the %q header for MCP server %q: %w", name, s.Name, err)
				}
				value := sealed
				h.Value = &value
			}
			h.Name = name
			headers = append(headers, h)
		}
		s.Headers = headers
		out[i] = s
	}
	return out, nil
}

// mcpRegistryUnchanged compares the prepared list to the stored row to avoid redundant writes.
func mcpRegistryUnchanged(prepared, stored []types.MCPServer) (bool, error) {
	if len(prepared) == 0 && len(stored) == 0 {
		return true, nil
	}
	// Both sides marshal the same type, so only a value can make the bytes differ.
	now, err := json.Marshal(prepared)
	if err != nil {
		return false, err
	}
	before, err := json.Marshal(stored)
	if err != nil {
		return false, err
	}
	return string(now) == string(before), nil
}

// refuseMCPChange is the fence MIKE_RESTRICT_MCP puts on the registry.
func (cs *ConfigStore) refuseMCPChange(prepared []types.MCPServer) error {
	if !cs.mcpLocked {
		return nil
	}
	stored, err := cs.storedMCPServers()
	if err != nil {
		return err
	}
	unchanged, err := mcpRegistryUnchanged(prepared, stored)
	if err != nil || unchanged {
		return err
	}
	return ErrMCPLocked
}

// validateMCPServers refuses shapes that cannot be dialled safely.
func validateMCPServers(servers []types.MCPServer) error {
	if len(servers) > types.MaxMCPServers {
		return fmt.Errorf("too many MCP servers: %d, the most is %d", len(servers), types.MaxMCPServers)
	}
	seen := make(map[string]bool, len(servers))
	slugs := make(map[string]string, len(servers))
	for _, s := range servers {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			return fmt.Errorf("an MCP server needs a name")
		}
		key := strings.ToLower(name)
		if seen[key] {
			return fmt.Errorf("two MCP servers are named %q", name)
		}
		seen[key] = true
		// The offered tool name is built from this slug, so two names that slug alike would ask
		// for the same tool twice.
		slug := types.Slugify(name, types.MCPServerSlugLen)
		if other, ok := slugs[slug]; ok {
			return fmt.Errorf("MCP servers %q and %q would both name their tools %q", other, name, slug)
		}
		slugs[slug] = name

		if err := types.ValidateMCPServer(name, s.URL, s.TimeoutSeconds, s.Headers); err != nil {
			return err
		}
	}
	return nil
}
