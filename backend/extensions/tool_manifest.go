package extensions

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"mike/backend/types"
)

// A tool declaration is checked where the manifest is stored, so nothing downstream re-checks it. The
// request's origin must name a configured `url` property, never a host written here.

const (
	// Every definition is sent with every request to the provider.
	MaxToolsPerExtension = 8

	maxToolNameLen        = 40
	maxToolDisplayNameLen = 40
	maxToolDescriptionLen = 2000
	maxURLTemplateLen     = 512
	maxTemplateValueLen   = 256
	maxQueryEntries       = 16
	maxResponseFields     = 8
	maxPathLen            = 128

	// Always capped, whatever the manifest asks: the answer costs the model's context.
	defaultToolResults      = 10
	MaxToolResults          = 25
	defaultToolSnippetChars = 500
	MaxToolSnippetChars     = 1200
	defaultToolTimeoutSecs  = 15
	MaxToolTimeoutSecs      = 60
)

var (
	toolNamePattern    = regexp.MustCompile(`^[a-z][a-z0-9_]{2,39}$`)
	queryKeyPattern    = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,64}$`)
	pathSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	placeholderPattern = regexp.MustCompile(`\{\{([^{}]*)\}\}`)
)

// validateTools runs wherever a manifest is stored — install, update and PUT.
func validateTools(caps *Capabilities, schema *ConfigSchema) error {
	tools := caps.GetTools()
	if len(tools) == 0 {
		return nil
	}
	// The executor finds these rows by type, so a second executing capability would be stored under one
	// type and missed by the other's lookup.
	if peers := caps.toolPeerNames(); len(peers) > 0 {
		return fmt.Errorf("capabilities.tools: a tool extension declares %s beside tools — with two executing capabilities the row is stored under one type and the other's lookup misses it. Install them as separate extensions",
			strings.Join(peers, ", "))
	}
	if caps.GetNetwork() == nil {
		return fmt.Errorf("capabilities.tools: a contributed tool reaches its upstream through the extension egress path, which requires capabilities.network (allowed_domains may stay empty — the target comes from the configuration)")
	}
	if len(tools) > MaxToolsPerExtension {
		return fmt.Errorf("capabilities.tools: %d tools declared, at most %d — every definition rides on every request to the provider", len(tools), MaxToolsPerExtension)
	}
	if err := schema.validateURLProperties(); err != nil {
		return err
	}
	seen := make(map[string]bool, len(tools))
	for i := range tools {
		if err := tools[i].Validate(schema); err != nil {
			return fmt.Errorf("capabilities.tools[%d] (%s): %w", i, tools[i].Name, err)
		}
		if seen[tools[i].Name] {
			return fmt.Errorf("capabilities.tools[%d]: %s is declared twice", i, tools[i].Name)
		}
		seen[tools[i].Name] = true
	}
	return nil
}

// toolPeerNames excludes network, which is how a tool reaches its upstream.
func (c *Capabilities) toolPeerNames() []string {
	if c == nil {
		return nil
	}
	var peers []string
	if c.LSP != nil {
		peers = append(peers, "lsp")
	}
	if c.UI != nil {
		peers = append(peers, "ui")
	}
	if c.CSS != nil {
		peers = append(peers, "css")
	}
	if c.Agent != nil {
		peers = append(peers, "agent")
	}
	if c.LLM != nil {
		peers = append(peers, "llm")
	}
	if c.I18N != nil {
		peers = append(peers, "i18n")
	}
	return peers
}

// Validate needs the manifest's own schema: a request template can only name a property that exists.
func (t *ToolConfig) Validate(schema *ConfigSchema) error {
	if t == nil {
		return nil
	}
	if !toolNamePattern.MatchString(t.Name) {
		return fmt.Errorf("name %q is not usable — lowercase letters, digits and underscore, starting with a letter, 3 to %d characters", t.Name, maxToolNameLen)
	}
	if types.IsCoreToolName(t.Name) {
		return fmt.Errorf("name %q belongs to a core tool — a contributed tool with a core name shadows the one the model would expect to reach", t.Name)
	}
	// The mcp_ prefix belongs to registered servers and dispatch routes on it, so a contributed tool
	// taking it would never be reached.
	if strings.HasPrefix(t.Name, types.MCPToolNamePrefix) {
		return fmt.Errorf("name %q starts with %q, which is reserved for tools from a registered MCP server", t.Name, types.MCPToolNamePrefix)
	}
	if len(t.DisplayName) > maxToolDisplayNameLen {
		return fmt.Errorf("display_name is longer than %d characters", maxToolDisplayNameLen)
	}
	if err := checkProse("description", t.Description, maxToolDescriptionLen); err != nil {
		return err
	}
	params, err := t.declaredParameters()
	if err != nil {
		return err
	}
	switch strings.ToUpper(t.Request.Method) {
	case "", "GET", "POST":
	default:
		return fmt.Errorf("request.method %q is not supported — GET or POST", t.Request.Method)
	}
	if err := validateURLTemplate(t.Request.URL, schema, params); err != nil {
		return err
	}
	if len(t.Request.Query) > maxQueryEntries {
		return fmt.Errorf("request.query declares %d entries, at most %d", len(t.Request.Query), maxQueryEntries)
	}
	for _, key := range sortedToolKeys(t.Request.Query) {
		if !queryKeyPattern.MatchString(key) {
			return fmt.Errorf("request.query key %q is not a usable parameter name", key)
		}
		value := t.Request.Query[key]
		if len(value) > maxTemplateValueLen {
			return fmt.Errorf("request.query[%q] is longer than %d characters", key, maxTemplateValueLen)
		}
		if err := validatePlaceholders(value, schema, params, fmt.Sprintf("request.query[%q]", key)); err != nil {
			return err
		}
	}
	if t.Request.TimeoutSeconds < 0 || t.Request.TimeoutSeconds > MaxToolTimeoutSecs {
		return fmt.Errorf("request.timeout_seconds %d is out of range (0 = %d, at most %d)", t.Request.TimeoutSeconds, defaultToolTimeoutSecs, MaxToolTimeoutSecs)
	}
	return t.Response.validate()
}

// An empty set is legal; a template may not read from it.
func (t *ToolConfig) declaredParameters() (map[string]bool, error) {
	params := map[string]bool{}
	if len(t.Parameters) == 0 {
		return params, nil
	}
	if typ, _ := t.Parameters["type"].(string); typ != "object" {
		return nil, fmt.Errorf("parameters.type must be \"object\"")
	}
	raw, ok := t.Parameters["properties"]
	if !ok || raw == nil {
		return params, nil
	}
	props, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("parameters.properties must be an object of parameter definitions")
	}
	for name, def := range props {
		if _, ok := def.(map[string]any); !ok {
			return nil, fmt.Errorf("parameters.properties.%s is not a parameter definition", name)
		}
		params[name] = true
	}
	if req, ok := t.Parameters["required"]; ok {
		list, ok := req.([]any)
		if !ok {
			return nil, fmt.Errorf("parameters.required must be a list of names")
		}
		for _, entry := range list {
			name, ok := entry.(string)
			if !ok || !params[name] {
				return nil, fmt.Errorf("parameters.required names %v, which parameters.properties does not declare", entry)
			}
		}
	}
	return params, nil
}

func (r ToolResponse) validate() error {
	if err := validateDottedPath("response.results", r.Results); err != nil {
		return err
	}
	if len(r.Fields) == 0 {
		return fmt.Errorf("response.fields declares nothing to show — name at least one field of a result item")
	}
	if len(r.Fields) > maxResponseFields {
		return fmt.Errorf("response.fields declares %d fields, at most %d", len(r.Fields), maxResponseFields)
	}
	for i, f := range r.Fields {
		if strings.TrimSpace(f.Label) == "" || len(f.Label) > maxToolDisplayNameLen {
			return fmt.Errorf("response.fields[%d].label is empty or longer than %d characters", i, maxToolDisplayNameLen)
		}
		if err := validateDottedPath(fmt.Sprintf("response.fields[%d].key", i), f.Key); err != nil {
			return err
		}
	}
	if r.Total != "" {
		if err := validateDottedPath("response.total", r.Total); err != nil {
			return err
		}
	}
	if r.MaxResults < 0 || r.MaxResults > MaxToolResults {
		return fmt.Errorf("response.max_results %d is out of range (0 = %d, at most %d)", r.MaxResults, defaultToolResults, MaxToolResults)
	}
	if r.MaxSnippetChars < 0 || r.MaxSnippetChars > MaxToolSnippetChars {
		return fmt.Errorf("response.max_snippet_chars %d is out of range (0 = %d, at most %d)", r.MaxSnippetChars, defaultToolSnippetChars, MaxToolSnippetChars)
	}
	return nil
}

// The manifest may write only what follows the configured origin. A manifest that could write its own
// host would be choosing the peer its traffic reaches.
func validateURLTemplate(raw string, schema *ConfigSchema, params map[string]bool) error {
	const where = "request.url"
	if raw == "" {
		return fmt.Errorf("%s is empty", where)
	}
	if len(raw) > maxURLTemplateLen {
		return fmt.Errorf("%s is longer than %d characters", where, maxURLTemplateLen)
	}
	if err := checkNoControl(raw); err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	if !strings.HasPrefix(raw, "{{") {
		return fmt.Errorf("%s %q must start with {{config.<key>}} — the origin a tool reaches is the address a person typed under Settings → Extensions, not one this manifest writes", where, raw)
	}
	end := strings.Index(raw, "}}")
	if end < 0 {
		return fmt.Errorf("%s %q has an unterminated placeholder", where, raw)
	}
	body := raw[2:end]
	name := originPlaceholder(raw)
	if name == "" {
		return fmt.Errorf("%s: the origin must be a bare {{config.<key>}} placeholder, not {{%s}}", where, body)
	}
	prop := schema.GetProperty(name)
	if prop == nil {
		return fmt.Errorf("%s: config.%s is not a property of this extension's config_schema", where, name)
	}
	if prop.Type != SchemaTypeURL {
		return fmt.Errorf("%s: config.%s is a %q property — an origin may only come from one declared type %q, which is the one a person types rather than one a default ships",
			where, name, prop.Type, SchemaTypeURL)
	}
	suffix := raw[end+2:]
	if strings.ContainsAny(suffix, "?#") {
		return fmt.Errorf("%s: what follows the configured origin is a path; its parameters belong in request.query", where)
	}
	if strings.ContainsAny(suffix, " \t\n") {
		return fmt.Errorf("%s: what follows the configured origin must not contain whitespace", where)
	}
	if strings.Contains(suffix, "//") {
		return fmt.Errorf("%s: what follows the configured origin must not contain \"//\" — it is a path, not a new origin", where)
	}
	if strings.Contains(suffix, "@") {
		return fmt.Errorf("%s: what follows the configured origin must not contain \"@\" — an upstream's secret rides in the extension's headers, not in the URL", where)
	}
	if err := validatePlaceholders(suffix, schema, params, where); err != nil {
		return err
	}
	return nil
}

func (t *ToolConfig) RequestSuffix() string {
	if t == nil {
		return ""
	}
	raw := t.Request.URL
	end := strings.Index(raw, "}}")
	if end < 0 {
		return ""
	}
	return raw[end+2:]
}

// OriginProperty names the property the tool sends to, or "" when its URL template carries no origin.
func (t *ToolConfig) OriginProperty() string {
	if t == nil {
		return ""
	}
	return originPlaceholder(t.Request.URL)
}

func originPlaceholder(raw string) string {
	if !strings.HasPrefix(raw, "{{") {
		return ""
	}
	end := strings.Index(raw, "}}")
	if end < 0 {
		return ""
	}
	name, hasDefault := splitDefault(raw[2:end])
	name = strings.TrimSpace(name)
	if hasDefault || !strings.HasPrefix(name, "config.") {
		return ""
	}
	return strings.TrimPrefix(name, "config.")
}

// An unresolved placeholder would be sent upstream verbatim.
func validatePlaceholders(tmpl string, schema *ConfigSchema, params map[string]bool, where string) error {
	if strings.Count(tmpl, "{{") != strings.Count(tmpl, "}}") {
		return fmt.Errorf("%s %q has an unbalanced placeholder", where, tmpl)
	}
	for _, match := range placeholderPattern.FindAllStringSubmatch(tmpl, -1) {
		body := match[1]
		key, hasDefault := splitDefault(body)
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, " \t\n") {
			return fmt.Errorf("%s: placeholder {{%s}} must name config.<key> or args.<parameter> with no whitespace", where, body)
		}
		if def, _ := splitDefault(body); hasDefault && len(def) > maxTemplateValueLen {
			return fmt.Errorf("%s: the default for %q is longer than %d characters", where, key, maxTemplateValueLen)
		}
		switch {
		case strings.HasPrefix(key, "config."):
			name := strings.TrimPrefix(key, "config.")
			if name == "" || schema == nil || schema.GetProperty(name) == nil {
				return fmt.Errorf("%s: config.%s is not a property of this extension's config_schema", where, name)
			}
		case strings.HasPrefix(key, "args."):
			name := strings.TrimPrefix(key, "args.")
			if !params[name] {
				return fmt.Errorf("%s: args.%s is not a parameter this tool declares", where, name)
			}
		default:
			return fmt.Errorf("%s: placeholder {{%s}} must be config.<key> or args.<parameter>", where, body)
		}
	}
	return nil
}

// A blank default is no default.
func splitDefault(body string) (name string, hasDefault bool) {
	name, def, found := strings.Cut(body, "|")
	if !found || strings.TrimSpace(def) == "" {
		return body, false
	}
	return name, true
}

// A numeric segment addresses an array element, so a nested answer needs no second grammar.
func validateDottedPath(where, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%s is empty", where)
	}
	if len(path) > maxPathLen {
		return fmt.Errorf("%s is longer than %d characters", where, maxPathLen)
	}
	for _, seg := range strings.Split(path, ".") {
		if !pathSegmentPattern.MatchString(seg) {
			return fmt.Errorf("%s %q has a segment %q that is not a field name or an array index", where, path, seg)
		}
	}
	return nil
}

// A `url` property may not declare a default: a prefilled value that a Save writes back would make
// registry text the grant.
func (s *ConfigSchema) validateURLProperties() error {
	if s == nil {
		return nil
	}
	for i := range s.Properties {
		p := &s.Properties[i]
		if p.Type != SchemaTypeURL {
			continue
		}
		if def, ok := p.Default.(string); ok && strings.TrimSpace(def) != "" {
			return fmt.Errorf("config_schema.%s: a %q property declares the default %q — the address is the grant, so it has to be typed rather than pre-filled and saved", p.Key, SchemaTypeURL, def)
		}
	}
	return nil
}

// ValidateConfigValues checks only a property type with a shape.
func ValidateConfigValues(schema *ConfigSchema, config map[string]any) error {
	if schema == nil {
		return nil
	}
	for i := range schema.Properties {
		p := &schema.Properties[i]
		if p.Type != SchemaTypeURL {
			continue
		}
		if _, err := ConfiguredURLOf(p.Key, config); err != nil {
			return fmt.Errorf("config %s: %w", p.Key, err)
		}
	}
	return nil
}

// ConfiguredURLOf reads one configured target. A stored value that cannot be used is an error, not a skip:
// "not configured yet" and "configured wrong" need different answers.
func ConfiguredURLOf(key string, config map[string]any) (string, error) {
	raw, ok := config[key]
	if !ok || raw == nil {
		return "", nil
	}
	text, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", nil
	}
	if _, _, err := ParseTargetURL(text); err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	return text, nil
}

// ParseTargetURL reads an address a person typed. The port is explicit, so a stored origin and a typed one
// compare equal.
func ParseTargetURL(raw string) (host, origin string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("is empty")
	}
	if strings.ContainsAny(raw, " \t\n\r") {
		return "", "", fmt.Errorf("%q contains whitespace", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("%q is not a URL: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", fmt.Errorf("%q must start with http:// or https://", raw)
	}
	if u.User != nil {
		return "", "", fmt.Errorf("%q carries credentials — an upstream's secret rides in the extension's headers, not in the address", raw)
	}
	if u.Hostname() == "" {
		return "", "", fmt.Errorf("%q names no host", raw)
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", "", fmt.Errorf("%q must be an address only — no query or fragment", raw)
	}
	if strings.HasSuffix(u.Path, "/") {
		return "", "", fmt.Errorf("%q has a trailing slash — give the address without one so the tool's path composes onto it", raw)
	}
	host = strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		port = defaultSchemePort(u.Scheme)
	}
	return host, u.Scheme + "://" + host + ":" + port, nil
}

func defaultSchemePort(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return "80"
}

// GrantedURLHosts counts a stored value because a person wrote it; an empty property grants nothing.
func (s *ConfigSchema) GrantedURLHosts(config map[string]any) []string {
	if s == nil {
		return nil
	}
	var hosts []string
	seen := map[string]bool{}
	for i := range s.Properties {
		p := &s.Properties[i]
		if p.Type != SchemaTypeURL {
			continue
		}
		text, err := ConfiguredURLOf(p.Key, config)
		if err != nil || text == "" {
			continue
		}
		host, _, err := ParseTargetURL(text)
		if err != nil || seen[host] {
			continue
		}
		seen[host] = true
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

// UsableContributedTools re-checks a stored row, which can predate a rule. One
// broken declaration is left out and named; its siblings keep working.
func (e *Extension) UsableContributedTools() ([]ToolConfig, error) {
	if e == nil {
		return nil, nil
	}
	tools := e.Manifest.Capabilities.GetTools()
	if len(tools) == 0 {
		return nil, nil
	}
	if e.Manifest.Capabilities.GetNetwork() == nil {
		return nil, fmt.Errorf("the extension declares %d tool(s) but no network capability", len(tools))
	}
	var usable []ToolConfig
	var firstErr error
	for i := range tools {
		if !tools[i].IsToolCallsEnabled() {
			continue
		}
		if err := tools[i].Validate(e.Manifest.ConfigSchema); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", tools[i].Name, err)
			}
			continue
		}
		usable = append(usable, tools[i])
	}
	return usable, firstErr
}

// GrantedHosts are the hosts of the stored `url` properties. A contributed request may be sent only to
// one of them.
func (e *Extension) GrantedHosts() []string {
	if e == nil {
		return nil
	}
	return e.Manifest.ConfigSchema.GrantedURLHosts(e.Config)
}

func sortedToolKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func checkProse(where, value string, maxLen int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is empty", where)
	}
	if len(value) > maxLen {
		return fmt.Errorf("%s is longer than %d characters", where, maxLen)
	}
	return checkNoControl(value)
}

func checkNoControl(value string) error {
	if strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return fmt.Errorf("contains a control character")
	}
	return nil
}

func (r ToolResponse) ResultLimit() int {
	if r.MaxResults <= 0 {
		return defaultToolResults
	}
	if r.MaxResults > MaxToolResults {
		return MaxToolResults
	}
	return r.MaxResults
}

func (r ToolResponse) SnippetLimit() int {
	if r.MaxSnippetChars <= 0 {
		return defaultToolSnippetChars
	}
	if r.MaxSnippetChars > MaxToolSnippetChars {
		return MaxToolSnippetChars
	}
	return r.MaxSnippetChars
}

func (q ToolRequest) TimeoutSecondsOrDefault() int {
	if q.TimeoutSeconds <= 0 {
		return defaultToolTimeoutSecs
	}
	if q.TimeoutSeconds > MaxToolTimeoutSecs {
		return MaxToolTimeoutSecs
	}
	return q.TimeoutSeconds
}
