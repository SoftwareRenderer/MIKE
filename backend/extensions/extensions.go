package extensions

import (
	"encoding/json"
	"sort"
	"strings"

	"kanban/backend/types"
)

// ExtensionType distinguishes between different kinds of extensions.
const (
	ExtensionTypeLSP   = "lsp"
	ExtensionTypeUI    = "ui"
	ExtensionTypeCSS   = "css"
	ExtensionTypeAgent = "agent"
)

// Extension represents an installed extension in the system.
type Extension struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Version         string            `json:"version"`
	Type            string            `json:"type"`
	Manifest        ExtensionManifest `json:"manifest"`
	Config          map[string]any    `json:"config"`
	ContainerConfig ContainerConfig   `json:"container_config"`
	SandboxConfig   SandboxConfig     `json:"sandbox_config"`
	InstallSource   string            `json:"install_source"` // "registry", "manual", "local"
	ImageRef        string            `json:"image_ref,omitempty"`
	Enabled         bool              `json:"enabled"`
	InstalledAt     string            `json:"installed_at"`
	BuiltAt         string            `json:"built_at"`
}

// ExtensionManifest defines the metadata and capabilities of an extension.
type ExtensionManifest struct {
	DisplayName  string        `json:"display_name"`
	Description  string        `json:"description"`
	Author       string        `json:"author"`
	Homepage     string        `json:"homepage,omitempty"`
	LicenseURL   string        `json:"license_url,omitempty"`
	Version      string        `json:"version"`
	Priority     int           `json:"priority"` // loading order: lower values load first (default 0)
	ConfigSchema *ConfigSchema `json:"config_schema,omitempty"`
	Capabilities *Capabilities `json:"capabilities,omitempty"`
}

// Capabilities groups the functional capabilities an extension provides.
type Capabilities struct {
	LSP   *LSPConfig   `json:"lsp,omitempty"`
	UI    *UIConfig    `json:"ui,omitempty"`
	CSS   *CSSConfig   `json:"css,omitempty"`
	Agent *AgentConfig `json:"agent,omitempty"`
	Proxy *ProxyConfig `json:"proxy,omitempty"` // server-side proxy capability
}

// GetLSP returns the LSP capability, or nil if not present.
func (c *Capabilities) GetLSP() *LSPConfig {
	if c == nil {
		return nil
	}
	return c.LSP
}

// GetUI returns the UI capability, or nil if not present.
func (c *Capabilities) GetUI() *UIConfig {
	if c == nil {
		return nil
	}
	return c.UI
}

// GetCSS returns the CSS capability, or nil if not present.
func (c *Capabilities) GetCSS() *CSSConfig {
	if c == nil {
		return nil
	}
	return c.CSS
}

// GetAgent returns the Agent capability, or nil if not present.
func (c *Capabilities) GetAgent() *AgentConfig {
	if c == nil {
		return nil
	}
	return c.Agent
}

// GetProxy returns the Proxy capability, or nil if not present.
func (c *Capabilities) GetProxy() *ProxyConfig {
	if c == nil {
		return nil
	}
	return c.Proxy
}

// ProxyConfig defines server-side proxy settings for external API access.
// Extensions must declare this capability to use the proxy endpoint.
// Headers can use {{property_name}} placeholders which are resolved from
// the extension's configuration at request time.
type ProxyConfig struct {
	AllowedDomains []string          `json:"allowed_domains"`   // whitelist of domains the proxy may contact
	Headers        map[string]string `json:"headers,omitempty"` // header templates (e.g. "Authorization": "token {{github_token}}")
}

// LSPConfig defines LSP-specific extension configuration.
type LSPConfig struct {
	Command            string         `json:"command"`
	Args               []string       `json:"args"`
	Extensions         []string       `json:"extensions"`
	Language           string         `json:"language"`
	InitParamsOverride map[string]any `json:"initParamsOverride,omitempty"`
	// EditorEnabled controls whether LSP features (diagnostics, completions,
	// go-to-definition) are available in the editor. Nil or true = enabled.
	EditorEnabled *bool `json:"editor_enabled,omitempty"`
	// ToolCallsEnabled controls whether LSP tools (references, definition,
	// code actions, hover) are available to agent tool calls. Nil or true = enabled.
	ToolCallsEnabled *bool `json:"tool_calls_enabled,omitempty"`
}

// IsEditorEnabled returns true if editor LSP features are enabled.
// Defaults to true when not explicitly set (nil).
func (c *LSPConfig) IsEditorEnabled() bool {
	if c == nil || c.EditorEnabled == nil {
		return true
	}
	return *c.EditorEnabled
}

// IsToolCallsEnabled returns true if LSP tool calls are enabled.
// Defaults to true when not explicitly set (nil).
func (c *LSPConfig) IsToolCallsEnabled() bool {
	if c == nil || c.ToolCallsEnabled == nil {
		return true
	}
	return *c.ToolCallsEnabled
}

// UIConfig defines UI-specific extension configuration.
type UIConfig struct {
	ModuleURL       string   `json:"module_url"`
	InjectionPoints []string `json:"injection_points"`
}

// CSSConfig defines CSS extension configuration (global stylesheet only).
// CSS extensions are a separate type from UI extensions — they have no
// module_url or injection_points, only a css_url.
type CSSConfig struct {
	CSSURL string `json:"css_url"` // global stylesheet URL (relative to server root)
}

// AgentConfig defines agent-specific extension configuration.
// The agent adapter (WASM module) handles all protocol details.
type AgentConfig struct {
	WasmModule   string              `json:"wasm_module"`             // path to WASM adapter module (relative to extension dir)
	Description  string              `json:"description,omitempty"`   // short description shown in provider card (markdown)
	SetupInfo    string              `json:"setup_info,omitempty"`    // setup instructions shown in provider card (markdown, newline-separated items)
	ConfigSchema *ConfigSchema       `json:"config_schema,omitempty"` // user-configurable options
	Sandbox      *types.AgentSandbox `json:"sandbox,omitempty"`       // default mounts and envs for sandbox containers
}

// ResolveSandboxTemplates resolves {{property_name}} template references
// in Sandbox using the default values from the provided ConfigSchema.
// This allows config_schema to hold the source-of-truth defaults while
// sandbox uses template references to the same values.
// Returns a nil sandbox if Sandbox is nil (no copy needed).
func (a *AgentConfig) ResolveSandboxTemplates(schema *ConfigSchema) *types.AgentSandbox {
	if a == nil || a.Sandbox == nil {
		return nil
	}
	sb := types.AgentSandbox{
		Mounts: make([]types.SandboxMount, len(a.Sandbox.Mounts)),
		Envs:   make([]types.SandboxEnv, len(a.Sandbox.Envs)),
	}
	copy(sb.Mounts, a.Sandbox.Mounts)
	copy(sb.Envs, a.Sandbox.Envs)
	if schema == nil {
		return &sb
	}
	for i := range sb.Mounts {
		sb.Mounts[i].HostPath = schema.resolveTemplate(sb.Mounts[i].HostPath)
		sb.Mounts[i].ContainerPath = schema.resolveTemplate(sb.Mounts[i].ContainerPath)
		sb.Mounts[i].Mode = schema.resolveTemplate(sb.Mounts[i].Mode)
	}
	for i := range sb.Envs {
		sb.Envs[i].Key = schema.resolveTemplate(sb.Envs[i].Key)
		sb.Envs[i].Value = schema.resolveTemplate(sb.Envs[i].Value)
	}
	return &sb
}

// resolveTemplate checks if value is a {{property_name[]}} template and resolves
// it to a config_schema array property's default value. The [] suffix signals
// the property is an array — value comes from the first element.
// Template names use the pattern {property_key}_{field}[] (e.g. {{sandbox_mount_host_path[]}}
// → sandbox_mount[0].host_path). If no match is found or the value is not a
// template, the original value is returned unchanged.
func (s *ConfigSchema) resolveTemplate(value string) string {
	if len(value) < 5 || value[0] != '{' || value[1] != '{' || value[len(value)-1] != '}' || value[len(value)-2] != '}' {
		return value
	}
	propName := value[2 : len(value)-2]
	// Strip trailing [] to get the property reference
	const arraySuffix = "[]"
	if !strings.HasSuffix(propName, arraySuffix) {
		return value
	}
	propName = strings.TrimSuffix(propName, arraySuffix)

	for _, prop := range s.Properties {
		arr, ok := prop.Default.([]any)
		if !ok || len(arr) == 0 {
			continue
		}
		first, ok := arr[0].(map[string]any)
		if !ok {
			continue
		}
		prefix := prop.Key + "_"
		if strings.HasPrefix(propName, prefix) {
			field := propName[len(prefix):]
			if v, ok := first[field]; ok {
				if str, ok := v.(string); ok {
					return str
				}
			}
		}
	}

	return value
}

// ConfigSchema is a JSON Schema subset for dynamic form rendering.
// Properties is an array to preserve insertion order from manifest.json.
type ConfigSchema struct {
	Type       string       `json:"type"`
	Properties []SchemaProp `json:"properties,omitempty"`
	Required   []string     `json:"required,omitempty"`
}

// UnmarshalJSON implements custom JSON unmarshaling for ConfigSchema.
// It accepts properties as either a JSON object (standard JSON Schema format
// where keys are property names) or a JSON array (internal representation).
func (s *ConfigSchema) UnmarshalJSON(data []byte) error {
	type alias ConfigSchema
	var raw struct {
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties,omitempty"`
		Required   []string        `json:"required,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	s.Type = raw.Type
	s.Required = raw.Required

	if len(raw.Properties) == 0 {
		return nil
	}

	// Try to unmarshal as JSON object (standard JSON Schema format)
	var propsMap map[string]SchemaProp
	if err := json.Unmarshal(raw.Properties, &propsMap); err == nil {
		s.Properties = make([]SchemaProp, 0, len(propsMap))
		for key, prop := range propsMap {
			prop.Key = key
			s.Properties = append(s.Properties, prop)
		}
		// Sort by key for deterministic order
		sort.Slice(s.Properties, func(i, j int) bool {
			return s.Properties[i].Key < s.Properties[j].Key
		})
		return nil
	}

	// Fall back to array format (internal representation)
	return json.Unmarshal(raw.Properties, &s.Properties)
}

// GetProperty returns the SchemaProp with the given key, or nil if not found.
func (s *ConfigSchema) GetProperty(key string) *SchemaProp {
	if s == nil {
		return nil
	}
	for i := range s.Properties {
		if s.Properties[i].Key == key {
			return &s.Properties[i]
		}
	}
	return nil
}

// SchemaProp describes a single property in a config schema.
type SchemaProp struct {
	Key         string   `json:"key"`
	Type        string   `json:"type"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Default     any      `json:"default,omitempty"`
	Minimum     *float64 `json:"minimum,omitempty"`
	Maximum     *float64 `json:"maximum,omitempty"`
	Enum        []any    `json:"enum,omitempty"`
	Sensitive   bool     `json:"sensitive,omitempty"` // if true, value is encrypted at rest
}

// ContainerConfig defines how the extension container is built/run.
type ContainerConfig struct {
	Image      string   `json:"image,omitempty"`      // OCI image reference (empty = build from Dockerfile)
	Dockerfile string   `json:"dockerfile,omitempty"` // path within package
	Entrypoint []string `json:"entrypoint,omitempty"`
	WorkingDir string   `json:"working_dir,omitempty"`
}

// SandboxConfig defines sandbox options for the extension container.
// Same shape as project SandboxOpts.
type SandboxConfig struct {
	NetworkIsolation bool                 `json:"network_isolation"`
	MemoryLimit      int                  `json:"memory_limit"`
	ForwardPorts     []types.PortForward  `json:"forward_ports,omitempty"`
	Mounts           []types.SandboxMount `json:"mounts,omitempty"`
	Envs             []types.SandboxEnv   `json:"envs,omitempty"`
	Capabilities     []string             `json:"capabilities,omitempty"`
}

// RegistryIndex is the top-level registry format.
type RegistryIndex []RegistryEntry

// RegistryEntry describes a single extension available in the registry.
type RegistryEntry struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	Type          string `json:"type"`
	DisplayName   string `json:"display_name"`
	Description   string `json:"description"`
	Author        string `json:"author"`
	Homepage      string `json:"homepage,omitempty"`
	LicenseURL    string `json:"license_url,omitempty"`
	Image         string `json:"image,omitempty"`          // OCI image reference
	DockerfileURL string `json:"dockerfile_url,omitempty"` // URL to Dockerfile
	ManifestURL   string `json:"manifest_url,omitempty"`   // URL to manifest.json
}

// IsLSP returns true if this extension is an LSP extension.
func (e *Extension) IsLSP() bool {
	return e.Type == ExtensionTypeLSP
}

// IsUI returns true if this extension is a UI extension.
func (e *Extension) IsUI() bool {
	return e.Type == ExtensionTypeUI
}

// IsCSS returns true if this extension is a CSS extension.
func (e *Extension) IsCSS() bool {
	return e.Type == ExtensionTypeCSS
}

// HasLSPConfig returns true if this extension has LSP configuration.
func (e *Extension) HasLSPConfig() bool {
	return e.IsLSP() && e.Manifest.Capabilities.GetLSP() != nil
}

// HasUIConfig returns true if this extension has UI configuration.
func (e *Extension) HasUIConfig() bool {
	return e.IsUI() && e.Manifest.Capabilities.GetUI() != nil
}

// HasCSSConfig returns true if this extension has CSS configuration.
func (e *Extension) HasCSSConfig() bool {
	return e.IsCSS() && e.Manifest.Capabilities.GetCSS() != nil
}

// IsAgent returns true if this extension is an agent extension.
func (e *Extension) IsAgent() bool {
	return e.Type == ExtensionTypeAgent
}

// HasAgentConfig returns true if this extension has agent configuration.
func (e *Extension) HasAgentConfig() bool {
	return e.IsAgent() && e.Manifest.Capabilities.GetAgent() != nil
}

// IsRegistryInstalled returns true if this extension was installed from a registry.
func (e *Extension) IsRegistryInstalled() bool {
	return e.InstallSource == "registry"
}

// IsManualInstalled returns true if this extension was manually defined.
func (e *Extension) IsManualInstalled() bool {
	return e.InstallSource == "manual"
}

// IsLocalInstalled returns true if this extension was installed from a local Dockerfile.
func (e *Extension) IsLocalInstalled() bool {
	return e.InstallSource == "local"
}
