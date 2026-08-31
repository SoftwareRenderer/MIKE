package extensions

import (
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"time"

	"kanban/backend/types"
)

// ExtensionType distinguishes between different kinds of extensions.
const (
	ExtensionTypeLSP   = "lsp"
	ExtensionTypeUI    = "ui"
	ExtensionTypeCSS   = "css"
	ExtensionTypeAgent = "agent"
	ExtensionTypeLLM   = "llm"
)

// maxExtensionValueBytes caps the size of a single storage value (KiB).
const maxExtensionValueBytes = 64 * 1024

// KVEntry is a single key/value pair in an extension's scoped storage.
type KVEntry struct {
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	UpdatedAt string          `json:"updated_at"`
}

// Extension represents an installed extension in the system.
type Extension struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Author          string            `json:"author,omitempty"`
	Version         string            `json:"version"`
	Type            string            `json:"type"`
	Manifest        ExtensionManifest `json:"manifest"`
	Config          map[string]any    `json:"config"`
	ContainerConfig ContainerConfig   `json:"container_config"`
	SandboxConfig   SandboxConfig     `json:"sandbox_config"`
	InstallSource   string            `json:"install_source"` // "registry", "local"
	ImageRef        string            `json:"image_ref,omitempty"`
	Enabled         bool              `json:"enabled"`
	InstalledAt     string            `json:"installed_at"`
	BuiltAt         string            `json:"built_at"`
	// Git source info for commit-pinned (self-hosted) extensions.
	// Empty for first-party/URL-based extensions.
	Repo   string `json:"repo,omitempty"`
	Commit string `json:"commit,omitempty"`
	Subdir string `json:"subdir,omitempty"`
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
	LSP     *LSPConfig     `json:"lsp,omitempty"`
	UI      *UIConfig      `json:"ui,omitempty"`
	CSS     *CSSConfig     `json:"css,omitempty"`
	Agent   *AgentConfig   `json:"agent,omitempty"`
	Network *NetworkConfig `json:"network,omitempty"` // server-side network capability
	LLM     *LLMConfig     `json:"llm,omitempty"`     // provider request adaptation
}

// GetLSP returns the LSP capability, or nil if not present.
func (c *Capabilities) GetLSP() *LSPConfig {
	if c == nil {
		return nil
	}
	return c.LSP
}

// GetAgent returns the Agent capability, or nil if not present.
func (c *Capabilities) GetAgent() *AgentConfig {
	if c == nil {
		return nil
	}
	return c.Agent
}

// GetNetwork returns the Network capability, or nil if not present.
func (c *Capabilities) GetNetwork() *NetworkConfig {
	if c == nil {
		return nil
	}
	return c.Network
}

// GetLLM returns the LLM capability, or nil if not present.
func (c *Capabilities) GetLLM() *LLMConfig {
	if c == nil {
		return nil
	}
	return c.LLM
}

// LLMConfig defines the request-adaptation capability: the extension ships
// provider mappings (in a separate file in the extension's directory,
// MappingsFile, default "mappings.json") that translate MIKE's
// OpenAI-compatible LLM requests to provider-specific formats.
type LLMConfig struct {
	MappingsFile string `json:"mappings_file,omitempty"`
}

// NetworkConfig defines server-side network settings for external API access.
// Extensions must declare this capability to use the proxy endpoint.
// Headers can use {{property_name}} placeholders which are resolved from
// the extension's configuration at request time.
type NetworkConfig struct {
	AllowedDomains []string          `json:"allowed_domains,omitempty"` // whitelist of domains the proxy may contact
	Headers        map[string]string `json:"headers,omitempty"`         // header templates (e.g. "Authorization": "token {{github_token}}")
}

// LSPConfig defines LSP-specific extension configuration.
type LSPConfig struct {
	Command            string         `json:"command,omitempty"`
	Args               []string       `json:"args,omitempty"`
	Extensions         []string       `json:"extensions,omitempty"`
	Language           string         `json:"language,omitempty"`
	InitParamsOverride map[string]any `json:"initParamsOverride,omitempty"`
	// EditorEnabled gates the editor's LSP features (diagnostics, completions,
	// go-to-definition). Nil or true = enabled. The gate is applied client-side
	// (extension-manager.getEditorLSPExtensions feeds the editor's LSP
	// connection), so the server only stores and serves it — there is no
	// server-side reader. Independent of ToolCallsEnabled, which the executor
	// checks server-side for the agent's LSP tools.
	EditorEnabled *bool `json:"editor_enabled,omitempty"`
	// ToolCallsEnabled controls whether LSP tools (references, definition,
	// rename, hover) are available to agent tool calls. Nil or true = enabled.
	ToolCallsEnabled *bool `json:"tool_calls_enabled,omitempty"`
	// Sandbox declares default sandbox options (writable mounts, envs, memory).
	Sandbox *SandboxConfig `json:"sandbox,omitempty"`
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
	// InjectionPoints lists the UI injection points the extension targets
	// (e.g. "createTask", "taskCard"). The entry page is always index.html
	// in the extension's own directory, so there is no module URL to declare.
	InjectionPoints []string `json:"injection_points,omitempty"`
	// Permissions lists the fully-qualified capability names (e.g.
	// "createTask.description.insertText") the extension requests. The host
	// grants only the intersection of these and the supported catalog.
	Permissions []string `json:"permissions,omitempty"`
}

// CSSConfig defines CSS extension configuration (global stylesheet only).
// CSS extensions are a separate type from UI extensions — they have no
// injection_points, only a css_url.
type CSSConfig struct {
	CSSURL string `json:"css_url,omitempty"` // global stylesheet URL (relative to server root)
}

// AgentConfig defines agent-specific extension configuration.
// The agent adapter (WASM module) handles all protocol details.
type AgentConfig struct {
	WasmModule   string              `json:"wasm_module,omitempty"`   // path to WASM adapter module (relative to extension dir)
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
	Properties []SchemaProp `json:"properties,omitempty"`
}

// UnmarshalJSON implements custom JSON unmarshaling for ConfigSchema.
// It accepts properties as either a JSON object (standard JSON Schema format
// where keys are property names) or a JSON array (internal representation).
func (s *ConfigSchema) UnmarshalJSON(data []byte) error {
	var raw struct {
		Properties json.RawMessage `json:"properties,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

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

	// Detect asks the workspace's own sandbox where a language keeps the code it
	// resolves. Core runs the command and never learns what the answer means — it
	// only checks that the answer names a directory the user already configured.
	Detect []DetectProbe `json:"detect,omitempty"`
	// Environment says how to tell whether this server can answer *at all* in this
	// workspace (a dependency root that is missing, a cache that is cold). When it
	// cannot, the listed diagnostic codes are the server guessing, and core does not
	// forward them to the model.
	Environment *EnvironmentGate `json:"environment,omitempty"`
	// ReloadFiles are workspace-relative paths whose change means the language
	// server has to re-read the project (a go.mod, a lockfile). A server sees no
	// file-watch events through a bind mount, so the turn's changed-file list is
	// what tells us.
	ReloadFiles []string `json:"reload_files,omitempty"`
}

// CommandRef is a manifest-declared command. Core never runs it on the host: a
// probe runs in a sandbox, either the task's (Detect) or the extension's own
// (EnvironmentGate.Check).
type CommandRef struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	// TimeoutSeconds bounds one run. 0 means the default; every value is clamped
	// so a manifest cannot stall a turn.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// Timeout returns the declared bound, clamped to (0, max]. A manifest that
// declares nothing gets def, and nothing declared may outrank max.
func (c CommandRef) Timeout(def, max time.Duration) time.Duration {
	if c.TimeoutSeconds <= 0 {
		return def
	}
	t := time.Duration(c.TimeoutSeconds) * time.Second
	if t > max {
		return max
	}
	return t
}

// Argv is the command line to run, command first. Returns false when no command
// is declared, so a half-written manifest is skipped instead of run empty.
func (c CommandRef) Argv() ([]string, bool) {
	if strings.TrimSpace(c.Command) == "" {
		return nil, false
	}
	return append([]string{c.Command}, c.Args...), true
}

// DetectProbe is one detection question asked of the task sandbox, plus what to
// do with the answer. Envs values may use the {value} placeholder, replaced by
// the probe's answer (trimmed, single line) — the only way a detected path can
// reach a language server.
type DetectProbe struct {
	CommandRef `json:",inline"`
	// ID names the probe in logs. Optional; defaults to the command.
	ID string `json:"id,omitempty"`
	// Envs are the assignments the answer produces, e.g. GOMODCACHE={value}.
	Envs []types.SandboxEnv `json:"envs,omitempty"`
}

// LogID is the identifier to use in log lines for this probe.
func (d DetectProbe) LogID() string {
	if d.ID != "" {
		return d.ID
	}
	return d.Command
}

// EnvironmentGate describes the health check for a language server's workspace,
// and what to do with the noise it produces when the workspace cannot be
// resolved. Codes are opaque strings: core compares them to the diagnostic's
// `code` and knows nothing about what any of them mean.
type EnvironmentGate struct {
	// Check runs inside the extension's own sandbox, in the workspace it is
	// answering for.
	Check CommandRef `json:"check"`
	// SuppressCodes are diagnostic codes that only mean "I could not load the
	// project" — dropped when Check says the environment is unusable.
	SuppressCodes []string `json:"suppress_codes,omitempty"`
	// Note is one sentence shown to the user instead of the suppressed noise.
	Note string `json:"note,omitempty"`
}

// Suppresses reports whether a diagnostic code is one this gate hides while the
// environment is unhealthy.
func (g *EnvironmentGate) Suppresses(code string) bool {
	if g == nil || code == "" {
		return false
	}
	for _, c := range g.SuppressCodes {
		if c == code {
			return true
		}
	}
	return false
}

// lspSandboxFromManifest returns the LSP capability's sandbox config, or nil.
func lspSandboxFromManifest(m *ExtensionManifest) *SandboxConfig {
	if m == nil || m.Capabilities == nil || m.Capabilities.LSP == nil {
		return nil
	}
	return m.Capabilities.LSP.Sandbox
}

// sandboxConfigFromManifest overlays manifest-declared sandbox options onto defaults.
func sandboxConfigFromManifest(m *SandboxConfig) SandboxConfig {
	sc := SandboxConfig{
		NetworkIsolation: true,
		MemoryLimit:      DefaultExtensionMemory, // 0 = no limit
	}
	if m == nil {
		return sc
	}
	if m.MemoryLimit > 0 {
		sc.MemoryLimit = m.MemoryLimit
	}
	sc.Mounts = m.Mounts
	sc.Envs = m.Envs
	sc.ForwardPorts = m.ForwardPorts
	sc.Capabilities = m.Capabilities
	// Probe declarations are manifest data, not user config: they travel with the
	// stored record so the runtime can act on them without re-reading the manifest.
	sc.Detect = m.Detect
	sc.Environment = m.Environment
	sc.ReloadFiles = m.ReloadFiles
	return sc
}

// RegistryIndex is the top-level registry format.
type RegistryIndex []RegistryEntry

// RegistryEntry describes a single extension available in the registry.
type RegistryEntry struct {
	Name          string        `json:"name"`
	Version       string        `json:"version"`
	Type          string        `json:"type"`
	DisplayName   string        `json:"display_name"`
	Description   string        `json:"description"`
	Author        string        `json:"author"`
	Featured      bool          `json:"featured,omitempty"`
	Homepage      string        `json:"homepage,omitempty"`
	LicenseURL    string        `json:"license_url,omitempty"`
	Image         string        `json:"image,omitempty"`          // OCI image reference
	DockerfileURL string        `json:"dockerfile_url,omitempty"` // URL to Dockerfile
	Capabilities  *Capabilities `json:"capabilities,omitempty"`   // capability config (mirrors manifest)
	Repo          string        `json:"repo,omitempty"`
	Commit        string        `json:"commit,omitempty"`
	Subdir        string        `json:"subdir,omitempty"`
	// LocalPath is the absolute on-disk directory of a filesystem (unpublished)
	// extension, as returned by the local-extensions endpoint.
	LocalPath string `json:"local_path,omitempty"`
	localDir  string
}

// IsLSP returns true if this extension is an LSP extension.
func (e *Extension) IsLSP() bool {
	return e.Type == ExtensionTypeLSP
}

// IsAgent returns true if this extension is an agent extension.
func (e *Extension) IsAgent() bool {
	return e.Type == ExtensionTypeAgent
}

// IsLocalInstalled returns true if this extension was installed from a local Dockerfile.
func (e *Extension) IsLocalInstalled() bool {
	return e.InstallSource == "local"
}

// sanitizeURL validates that a URL uses an allowed scheme (http or https).
// Returns an empty string if the URL is empty or uses a disallowed scheme.
// This prevents javascript: and data: URL injection in href attributes.
func sanitizeURL(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	switch parsed.Scheme {
	case "http", "https":
		return raw
	default:
		return ""
	}
}

// sanitizeManifestURLs validates and sanitizes all URL fields in a manifest.
// Mutates the manifest in place, clearing any disallowed URLs.
func sanitizeManifestURLs(m *ExtensionManifest) {
	if m == nil {
		return
	}
	m.Homepage = sanitizeURL(m.Homepage)
	m.LicenseURL = sanitizeURL(m.LicenseURL)
}
