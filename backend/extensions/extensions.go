package extensions

import (
	"kanban/backend/types"
)

// ExtensionType distinguishes between different kinds of extensions.
const (
	ExtensionTypeLSP = "lsp"
	ExtensionTypeUI  = "ui"
	ExtensionTypeCSS = "css"
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
	Deprecated   bool          `json:"deprecated"`
	ConfigSchema *ConfigSchema `json:"config_schema,omitempty"`
	LSP          *LSPConfig    `json:"lsp,omitempty"`
	UI           *UIConfig     `json:"ui,omitempty"`
	CSS          *CSSConfig    `json:"css,omitempty"`
}

// LSPConfig defines LSP-specific extension configuration.
type LSPConfig struct {
	Command            string                 `json:"command"`
	Args               []string               `json:"args"`
	Extensions         []string               `json:"extensions"`
	Language           string                 `json:"language"`
	InitParamsOverride map[string]any         `json:"initParamsOverride,omitempty"`
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

// ConfigSchema is a JSON Schema subset for dynamic form rendering.
type ConfigSchema struct {
	Type       string                 `json:"type"`
	Properties map[string]SchemaProp  `json:"properties,omitempty"`
	Required   []string               `json:"required,omitempty"`
}

// SchemaProp describes a single property in a config schema.
type SchemaProp struct {
	Type        string  `json:"type"`
	Title       string  `json:"title,omitempty"`
	Description string  `json:"description,omitempty"`
	Default     any     `json:"default,omitempty"`
	Minimum     *float64 `json:"minimum,omitempty"`
	Maximum     *float64 `json:"maximum,omitempty"`
	Enum        []any   `json:"enum,omitempty"`
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
	NetworkIsolation bool                   `json:"network_isolation"`
	MemoryLimit      int                    `json:"memory_limit"`
	ForwardPorts     []types.PortForward    `json:"forward_ports,omitempty"`
	Mounts           []types.SandboxMount   `json:"mounts,omitempty"`
	Envs             []types.SandboxEnv     `json:"envs,omitempty"`
	Capabilities     []string               `json:"capabilities,omitempty"`
}

// RegistryIndex is the top-level registry format.
type RegistryIndex []RegistryEntry

// RegistryEntry describes a single extension available in the registry.
type RegistryEntry struct {
	Name           string `json:"name"`
	Version        string `json:"version"`
	Type           string `json:"type"`
	DisplayName    string `json:"display_name"`
	Description    string `json:"description"`
	Author         string `json:"author"`
	Homepage       string `json:"homepage,omitempty"`
	LicenseURL     string `json:"license_url,omitempty"`
	Image          string `json:"image,omitempty"`          // OCI image reference
	DockerfileURL  string `json:"dockerfile_url,omitempty"` // URL to Dockerfile
	ManifestURL    string `json:"manifest_url,omitempty"`   // URL to manifest.json
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
	return e.IsLSP() && e.Manifest.LSP != nil
}

// HasUIConfig returns true if this extension has UI configuration.
func (e *Extension) HasUIConfig() bool {
	return e.IsUI() && e.Manifest.UI != nil
}

// HasCSSConfig returns true if this extension has CSS configuration.
func (e *Extension) HasCSSConfig() bool {
	return e.IsCSS() && e.Manifest.CSS != nil
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
