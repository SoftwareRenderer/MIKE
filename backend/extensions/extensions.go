// Package extensions implements the extension system: agent, LSP and UI extensions -
// their manifests, SQLite records, the registry client that pins them, the gVisor sandbox
// they run in, and the REST API that drives them.
// Privileges come from the manifest and are checked twice: on save/install, and again when
// the sandbox is built (mount_policy.go).
package extensions

import (
	"cmp"
	"encoding/json"
	"mike/backend/types"
	"net/url"
	"slices"
	"strings"
)

// ExtensionType distinguishes between different kinds of extensions.
const (
	ExtensionTypeLSP   = "lsp"
	ExtensionTypeUI    = "ui"
	ExtensionTypeCSS   = "css"
	ExtensionTypeAgent = "agent"
	ExtensionTypeLLM   = "llm"
	// ExtensionTypeI18N is a translation pack: a catalog file the frontend loads to render the UI in the
	// pack's locale, plus optionally translated prompt templates. It executes nothing — static files
	// served from its own directory — so it gets no sandbox, no mounts, no network, no permissions.
	ExtensionTypeI18N = "i18n"
	// Its logic is data in the manifest, so the core holds no upstream knowledge. It runs in the server
	// process rather than a container, so it pairs with `network` and needs no image.
	ExtensionTypeTool = "tool"
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

// SensitiveProperty reports whether the manifest declares this config key to hold a secret.
func (e *Extension) SensitiveProperty(key string) bool {
	if e == nil {
		return false
	}
	return e.Manifest.ConfigSchema.IsSensitive(key)
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
	Image        string        `json:"image,omitempty"`
}

// Capabilities groups the functional capabilities an extension provides.
type Capabilities struct {
	LSP     *LSPConfig     `json:"lsp,omitempty"`
	UI      *UIConfig      `json:"ui,omitempty"`
	CSS     *CSSConfig     `json:"css,omitempty"`
	Agent   *AgentConfig   `json:"agent,omitempty"`
	Network *NetworkConfig `json:"network,omitempty"` // server-side network capability
	LLM     *LLMConfig     `json:"llm,omitempty"`     // provider request adaptation
	I18N    *I18NConfig    `json:"i18n,omitempty"`    // data-only translation pack (UI catalog + prompt templates)
	// Declaring tools makes the extension's type `tool`, and that column is how the executor finds the
	// row, so a manifest pairing tools with another executing capability is refused.
	Tools []ToolConfig `json:"tools,omitempty"`
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

// GetI18N returns the translation-pack capability, or nil if not present.
func (c *Capabilities) GetI18N() *I18NConfig {
	if c == nil {
		return nil
	}
	return c.I18N
}

// GetCSS returns the global-stylesheet capability, or nil if not present.
func (c *Capabilities) GetCSS() *CSSConfig {
	if c == nil {
		return nil
	}
	return c.CSS
}

// GetTools returns the contributed tool declarations, or nil when the manifest declares none.
func (c *Capabilities) GetTools() []ToolConfig {
	if c == nil {
		return nil
	}
	return c.Tools
}

// I18NConfig declares a translation pack: translated interface strings plus an optional overlay
// of translated prompt templates, both inside the extension's own directory. That is the whole
// security argument — a pack is read as data, so it gets no sandbox, no mounts, no network and
// no permissions, and a manifest pairing i18n with any other capability is refused
// (validateI18N). The active language is derived from which pack is enabled, so there is no
// language field to keep in sync.
type I18NConfig struct {
	// Locale is a bare BCP-47 language subtag — "zh", "fr", "ko". Not a full locale tag: a
	// pack is one language, and the spelling of that one language is what the registry index
	// and a browser's `navigator.language` have to agree on (validateLocale refuses "zh-CN"
	// rather than guessing which of zh-CN and zh-TW a reader wants). Core holds no language
	// names; this value is carried through verbatim.
	Locale string `json:"locale"`
	// MessagesURL is the message catalog, relative to the extension's own directory. Required:
	// the catalog is the pack. A URL with a scheme is refused — a translation file is the one
	// piece of the interface that reaches every screen, so it is not fetched from a host the
	// user never chose. The frontend resolves it the same way a css pack's `css_url` is.
	MessagesURL string `json:"messages_url"`
	// PromptsDir optionally names a directory of translated prompt templates inside the pack,
	// same containment rules as the catalog. Checked at install, read by ActiveI18N's consumer
	// (the server-side prompt locale), not by the UI.
	PromptsDir string `json:"prompts_dir,omitempty"`
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
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	// FileTypes says which files this extension owns and what its server calls each one. A
	// per-file language id is what the shape is for: tsserver parses .tsx differently from .ts
	// and mis-parses the file for good when handed the coarse id.
	FileTypes []LSPFileType `json:"file_types,omitempty"`
	// Syntax holds the named highlighting profiles the file types refer to — data, never code.
	Syntax             map[string]SyntaxSpec `json:"syntax,omitempty"`
	Language           string                `json:"language,omitempty"`
	InitParamsOverride map[string]any        `json:"initParamsOverride,omitempty"`
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
	// ChatDiagnosticsEnabled controls the turn-boundary pass that hands the files a turn
	// edited to their language server
	ChatDiagnosticsEnabled *bool `json:"chat_diagnostics_enabled,omitempty"`
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

// IsChatDiagnosticsEnabled returns true when the post-edit diagnostics pass may use this
// extension's server. Defaults to false: an extension that never declared the field is asked
// for nothing, and Settings writes the flag when a person opts in.
func (c *LSPConfig) IsChatDiagnosticsEnabled() bool {
	return c != nil && c.ChatDiagnosticsEnabled != nil && *c.ChatDiagnosticsEnabled
}

// LSPFileType is one file type an LSP extension owns.
type LSPFileType struct {
	// Extension is a bare lowercase suffix — "ts", not ".ts".
	Extension string `json:"extension"`
	// LanguageID is what `textDocument/didOpen` carries for this file type, which is not the
	// manifest language: "typescriptreact" for .tsx, "typescript" for .ts.
	LanguageID string `json:"language_id"`
	// Label is the human name for the editor's status bar ("TypeScript JSX").
	Label string `json:"label,omitempty"`
	// Syntax names the SyntaxSpec that highlights this file type; empty means fallback or plain text.
	Syntax string `json:"syntax,omitempty"`
}

// SyntaxSpec declares a grammar as data; the core turns it into a stream grammar and a bracket folder.
type SyntaxSpec struct {
	LineComment string `json:"line_comment,omitempty"`
	// LineComments are extra line-comment markers where a language has more than one (PHP: "//" and "#").
	LineComments []string      `json:"line_comments,omitempty"`
	BlockComment *CommentToken `json:"block_comment,omitempty"`
	Strings      []StringToken `json:"strings,omitempty"`
	// Pairs are the brackets, and the input to the generic fold provider.
	Pairs     []CommentToken `json:"pairs,omitempty"`
	Keywords  []string       `json:"keywords,omitempty"`
	Types     []string       `json:"types,omitempty"`
	Constants []string       `json:"constants,omitempty"`
	Builtins  []string       `json:"builtins,omitempty"`
	Numbers   bool           `json:"numbers,omitempty"`
	Region    *CommentToken  `json:"region,omitempty"`
	// CapitalizedTypes colors an identifier whose first letter is upper-case as a type name — Go's
	// exported `ChatMessage`, a JS class. Pointer because the answer defaults to true when the manifest
	// says nothing, and a plain bool with omitempty would drop an author's explicit false on the way
	// back out and hand the browser the default instead of the refusal.
	CapitalizedTypes *bool `json:"capitalized_types,omitempty"`
	// CallAsFunction colors a name applied to arguments (`name(`) as a function name. Same default.
	CallAsFunction *bool `json:"call_as_function,omitempty"`
}

// CommentToken is an open/close delimiter pair: a block comment, a bracket pair or a region marker.
type CommentToken struct {
	Open  string `json:"open"`
	Close string `json:"close"`
}

// StringToken is one string form: its quote (one character, or that character repeated), whether
// it honours backslash escapes, and whether it may span lines.
type StringToken struct {
	Quote     string `json:"quote"`
	Escapes   bool   `json:"escapes,omitempty"`
	Multiline bool   `json:"multiline,omitempty"`
}

// fileSuffix normalizes a path or file name to the bare lowercase suffix file types match on.
func fileSuffix(name string) string {
	base := name
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	dot := strings.LastIndexByte(base, '.')
	if dot <= 0 || dot == len(base)-1 {
		return ""
	}
	return strings.ToLower(base[dot+1:])
}

// fileTypeFor resolves the declared file type for a path; MatchesFile and LanguageIDFor both use it.
func (c *LSPConfig) fileTypeFor(name string) *LSPFileType {
	if c == nil {
		return nil
	}
	suffix := fileSuffix(name)
	for i := range c.FileTypes {
		if suffix != "" && c.FileTypes[i].Extension == suffix {
			return &c.FileTypes[i]
		}
	}
	// A file named after the language ("go") reaches its server.
	if c.Language != "" && strings.EqualFold(baseName(name), c.Language) {
		for i := range c.FileTypes {
			if c.FileTypes[i].LanguageID == c.Language {
				return &c.FileTypes[i]
			}
		}
	}
	return nil
}

// MatchesFile reports whether this extension serves the file.
func (c *LSPConfig) MatchesFile(name string) bool { return c.fileTypeFor(name) != nil }

// LanguageIDFor is the languageId to put on a document notification for this file, falling back to
// the manifest language when no file type claims it.
func (c *LSPConfig) LanguageIDFor(name string) string {
	if c == nil {
		return ""
	}
	if ft := c.fileTypeFor(name); ft != nil && ft.LanguageID != "" {
		return ft.LanguageID
	}
	return c.Language
}

func baseName(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[i+1:]
	}
	return name
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

// ToolConfig is one tool as an extension declares it. The shape is data and nothing here names a
// provider: the manifest spells out the path, the parameters and the answer field names.
type ToolConfig struct {
	// Never a core tool's name: the model would call it and never reach the core tool.
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	// The tool's only route to the model: a contributed tool ships no prompt text.
	Description string `json:"description"`
	// Only an object schema with declared properties: every `{{args.<name>}}` must name one.
	Parameters map[string]any `json:"parameters,omitempty"`
	Request    ToolRequest    `json:"request"`
	Response   ToolResponse   `json:"response"`
	// Withholds this tool from the agent while the extension stays installed.
	ToolCallsEnabled *bool `json:"tool_calls_enabled,omitempty"`
}

// IsToolCallsEnabled reports whether the tool is offered. Nil means offered.
func (t *ToolConfig) IsToolCallsEnabled() bool {
	if t == nil || t.ToolCallsEnabled == nil {
		return true
	}
	return *t.ToolCallsEnabled
}

// ToolRequest asks the upstream something. Its origin comes only from the configuration (see
// validateURLTemplate), so a person can point an extension at their own instance and the manifest cannot
// choose a different peer.
type ToolRequest struct {
	// GET (default) or POST; a POST sends the declared Query entries as a form body.
	Method string `json:"method,omitempty"`
	// A template beginning with the `{{config.<key>}}` of a property declared `type: "url"`.
	URL   string            `json:"url"`
	Query map[string]string `json:"query,omitempty"`
	// Bounds one upstream round trip. Zero is the default, not no timeout.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// ToolField is one line of a rendered result: the label the agent sees and the key it reads.
type ToolField struct {
	Label string `json:"label"`
	// A dotted path inside one result item ("title", "parsed_url").
	Key string `json:"key"`
}

// ToolResponse says where the results are in the upstream's JSON and what to show from each one.
type ToolResponse struct {
	Results string `json:"results"`
	// Rendered in declaration order, so an answer's shape never depends on map iteration.
	Fields []ToolField `json:"fields"`
	Total  string      `json:"total,omitempty"`
	// Always capped, whatever the manifest asks: the answer spends the model's context.
	MaxResults      int `json:"max_results,omitempty"`
	MaxSnippetChars int `json:"max_snippet_chars,omitempty"`
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

// ResolveSandboxTemplates resolves {{property_name}} templates in Sandbox using ConfigSchema defaults.
// Returns a nil sandbox if Sandbox is nil.
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

// resolveTemplate resolves {{property_name[]}} templates to the first element of a
// ConfigSchema array property. Templates follow the pattern {property_key}_{field}[]
// (e.g. {{sandbox_mount_host_path[]}} → sandbox_mount[0].host_path). Returns the
// original value if no match is found.
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

// The {{...}} placeholders are replaced only in this copy: the saved record keeps them.
func resolvedSandboxManifest(m ExtensionManifest) ExtensionManifest {
	agent := m.Capabilities.GetAgent()
	if agent == nil || agent.Sandbox == nil {
		return m
	}
	resolvedAgent := *agent
	resolvedAgent.Sandbox = agent.ResolveSandboxTemplates(m.ConfigSchema)
	caps := *m.Capabilities
	caps.Agent = &resolvedAgent
	m.Capabilities = &caps
	return m
}

// ConfigSchema is the JSON Schema subset rendered as a form. Properties is an array to keep
// manifest.json's insertion order.
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
		slices.SortFunc(s.Properties, func(a, b SchemaProp) int { return cmp.Compare(a.Key, b.Key) })
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

// IsSensitive reports whether this schema declares the key to hold a secret. A key it does not list,
// or no schema at all, declares none.
func (s *ConfigSchema) IsSensitive(key string) bool {
	prop := s.GetProperty(key)
	return prop != nil && prop.Sensitive
}

// SchemaTypeURL marks a config property whose value is an address a person typed. It is the only kind of
// value that grants a network peer: a contributed tool may contact only the origin stored in a property of
// this type (see validateURLTemplate), so such a property carries no default and the editor shows an empty
// field.
const SchemaTypeURL = "url"

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

	// Environment says how to tell whether this server can answer at all in this workspace
	// (a missing dependency root, a cold cache). When it cannot, the suppressed diagnostic
	// codes are the server guessing, and core does not forward them to the model.
	Environment *EnvironmentGate `json:"environment,omitempty"`
	// ReloadFiles are workspace-relative paths whose change means the language
	// server has to re-read the project (a go.mod, a lockfile). A server sees no
	// file-watch events through a bind mount, so the turn's changed-file list is
	// what tells us.
	ReloadFiles []string `json:"reload_files,omitempty"`
	// Dependency names where this server reads its resolved dependencies from, so the host can
	// supply them without being told where to look. Manifest data.
	Dependency *DependencyCache `json:"dependency_cache,omitempty"`
	// DependencyHostPath is the host directory the user granted for that cache. It lives
	// beside the declaration, not inside it, so the two origins stay distinguishable in the stored
	// record: a manifest declares a path in its own container, a human names a directory on this
	// machine. A manifest carrying it is refused (manifest_validate.go), never merged.
	//
	// It has its own endpoint because an empty value must mean "revoke", which a handler that
	// copies non-empty fields cannot express.
	DependencyHostPath string `json:"dependency_host_path,omitempty"`
}

// DependencyCache is a language server declaring where it looks for resolved dependencies inside its
// own container. It says nothing about the host: which directory fills that path is chosen by a person
// in Settings, and a manifest that carries it is refused rather than stripped.
type DependencyCache struct {
	// ContainerPath is where the cache appears inside the extension's container. Shadowing a path
	// the server keeps its own state in is checked at bind time, where the real paths are known.
	ContainerPath string `json:"container_path"`
	// Env is the variable the server reads the location from: a NAME, never an assignment or a
	// value. The value is always ContainerPath; arbitrary values here would be a second,
	// unvalidated `envs`.
	Env string `json:"env"`
}

// DependencyGrant is the host directory the user granted for this server's cache, or "" when
// there is nothing to bind — a granted path with no declaration, or a declaration nobody
// granted, binds nothing rather than half a configuration. Only a path comes back because the
// mode is not the extension's to choose: this container is the only one that binds the grant,
// and it binds it read-only.
func (sc *SandboxConfig) DependencyGrant() string {
	if sc == nil || sc.DependencyHostPath == "" || sc.Dependency == nil {
		return ""
	}
	return sc.DependencyHostPath
}

// CommandRef is a manifest-declared command. Core never runs it on the host: it
// runs in the extension's own sandbox (EnvironmentGate.Check).
type CommandRef struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

// Argv is the command line to run, command first. Returns false when no command
// is declared, so a half-written manifest is skipped instead of run empty.
func (c CommandRef) Argv() ([]string, bool) {
	if strings.TrimSpace(c.Command) == "" {
		return nil, false
	}
	return append([]string{c.Command}, c.Args...), true
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
		MemoryLimit:      DefaultExtensionMemory,
	}
	if m == nil {
		return sc
	}
	// A limit of 0 means the manifest has no opinion and takes the default, a positive limit is that
	// bound, and a negative limit asks for no bound at all. Only the 0 case is replaced here, so a
	// manifest that declares a limit never loses it to the default.
	if m.MemoryLimit != 0 {
		sc.MemoryLimit = m.MemoryLimit
	}
	sc.Mounts = m.Mounts
	sc.Envs = m.Envs
	sc.ForwardPorts = m.ForwardPorts
	sc.Capabilities = m.Capabilities
	// The gate and the reload list are manifest data, not user config: they travel
	// with the stored record so the runtime can act on them without re-reading the
	// manifest.
	sc.Environment = m.Environment
	sc.ReloadFiles = m.ReloadFiles
	sc.Dependency = m.Dependency
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

// mergeManifestSandboxAdditions brings in what a manifest declares that the stored row does not
// have yet — how an update delivers a sandbox declaration the installed commit did not ship.
//
// Everything merged was also editable through PUT /api/extensions/{id}, so the stored value wins
// on a conflict, and the memory limit and network isolation are never touched at all.
func mergeManifestSandboxAdditions(stored SandboxConfig, declared *SandboxConfig) SandboxConfig {
	if declared == nil {
		return stored
	}
	// Copy before appending: the stored slices are shared with the caller's extension record, and
	// an append with spare capacity would write through to it. `before` in the update path is
	// exactly such a copy.
	stored.Envs = append([]types.SandboxEnv(nil), stored.Envs...)
	stored.Mounts = append([]types.SandboxMount(nil), stored.Mounts...)
	stored.ForwardPorts = append([]types.PortForward(nil), stored.ForwardPorts...)
	stored.Capabilities = append([]string(nil), stored.Capabilities...)

	haveEnv := make(map[string]bool, len(stored.Envs))
	for _, e := range stored.Envs {
		haveEnv[e.Key] = true
	}
	for _, e := range declared.Envs {
		if !haveEnv[e.Key] {
			haveEnv[e.Key] = true
			stored.Envs = append(stored.Envs, e)
		}
	}
	haveMount := make(map[string]bool, len(stored.Mounts))
	for _, m := range stored.Mounts {
		haveMount[m.ContainerPath] = true
	}
	for _, m := range declared.Mounts {
		if !haveMount[m.ContainerPath] {
			haveMount[m.ContainerPath] = true
			stored.Mounts = append(stored.Mounts, m)
		}
	}
	havePort := make(map[int]bool, len(stored.ForwardPorts))
	for _, p := range stored.ForwardPorts {
		havePort[p.ContainerPort] = true
	}
	for _, p := range declared.ForwardPorts {
		if !havePort[p.ContainerPort] {
			havePort[p.ContainerPort] = true
			stored.ForwardPorts = append(stored.ForwardPorts, p)
		}
	}
	haveCap := make(map[string]bool, len(stored.Capabilities))
	for _, c := range stored.Capabilities {
		haveCap[c] = true
	}
	for _, c := range declared.Capabilities {
		if !haveCap[c] {
			haveCap[c] = true
			stored.Capabilities = append(stored.Capabilities, c)
		}
	}
	// These three are the manifest's own declarations, not user-additive lists: a new manifest
	// replaces them. Merging them additively would keep acting on the previous commit's gate and
	// reload list forever, and a manifest that deliberately dropped one would keep it.
	stored.Environment = declared.Environment
	stored.ReloadFiles = declared.ReloadFiles
	stored.Dependency = declared.Dependency
	return stored
}
