// Package llmadapter adapts MIKE's OpenAI-compatible LLM request format to
// provider-specific formats. Mappings are declarative data (typically shipped
// by `llm`-type extensions in a mappings file) and selected by provider base
// URL prefix.
package llmadapter

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Template variables available in header values and set-rule values.
const (
	// varReasoningEffort is the configured reasoning effort for the call
	// (e.g. "low", "medium", "high", "none", "off", "default", or "").
	varReasoningEffort = "reasoning_effort"
	// varModel is the model name for the call.
	varModel = "model"
)

// Mapping describes how to adapt MIKE's request for one provider.
type Mapping struct {
	// Match is a provider base-URL prefix (trailing slash ignored). The next character must be
	// "/" or the end of the URL, so "https://openrouter" does not match "https://openrouter.ai".
	Match string `json:"match"`
	// Headers are added to the request once template-resolved. A header whose
	// resolved value still contains an unresolved {{placeholder}} is skipped.
	Headers map[string]string `json:"headers,omitempty"`
	// Body transforms the request body in place.
	Body BodyRules `json:"body,omitempty"`
}

// BodyRules describes request-body transformations, applied in the order:
// remove, remove_if, set.
type BodyRules struct {
	// Remove lists dot-separated JSON paths deleted unconditionally.
	Remove []string `json:"remove,omitempty"`
	// RemoveIf maps a body key to another key: the key is deleted only when the
	// referenced key is absent (e.g. drop tool_choice when no tools are sent).
	RemoveIf map[string]string `json:"remove_if,omitempty"`
	// Set maps dot-separated JSON paths to rules that create or overwrite the
	// value, creating parent objects as needed.
	Set map[string]SetRule `json:"set,omitempty"`
}

// UnmarshalJSON accepts set entries either as a plain-string shorthand
// (SetRule{Value: s}) or as a full SetRule object.
func (b *BodyRules) UnmarshalJSON(data []byte) error {
	var raw struct {
		Remove   []string          `json:"remove"`
		RemoveIf map[string]string `json:"remove_if"`
		Set      map[string]any    `json:"set"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	b.Remove = raw.Remove
	b.RemoveIf = raw.RemoveIf
	set := make(map[string]SetRule, len(raw.Set))
	for path, v := range raw.Set {
		switch tv := v.(type) {
		case string:
			set[path] = SetRule{Value: tv}
		case map[string]any:
			enc, err := json.Marshal(tv)
			if err != nil {
				return err
			}
			var rule SetRule
			if err := json.Unmarshal(enc, &rule); err != nil {
				return fmt.Errorf("set rule %q: %w", path, err)
			}
			set[path] = rule
		default:
			return fmt.Errorf("set rule %q: must be a string or object", path)
		}
	}
	b.Set = set
	return nil
}

// SetRule describes how to set one JSON body path.
type SetRule struct {
	// From is the variable the "when" gate and table lookups key on
	// (e.g. "reasoning_effort"). Empty = the first placeholder in a string
	// value; a table value requires an explicit "from".
	From string `json:"from,omitempty"`
	// Value is what gets written to the path:
	//   - a string: template-resolved at request time;
	//   - any other scalar (number, bool, ...): set verbatim;
	//   - a map: a lookup table keyed by the resolved "from" value, with an
	//     optional "*" fallback entry. The rule is skipped when the variable
	//     cannot be resolved, or when neither the key nor "*" matches.
	Value any `json:"value,omitempty"`
	// When, if non-empty, applies the rule only when the resolved "from" value
	// is one of these entries ("" is a legitimate entry). A when-gated rule
	// with no resolvable variable never applies.
	When []string `json:"when,omitempty"`
}

// Adapter applies a set of mappings to outbound LLM requests.
type Adapter struct {
	mu       sync.RWMutex
	mappings []Mapping
}

// NewAdapter returns an adapter with no mappings (every call passes through).
func NewAdapter() *Adapter { return &Adapter{} }

// SetMappings replaces the active mapping set. Mappings are applied in list
// order; the first match wins.
func (a *Adapter) SetMappings(mappings []Mapping) {
	a.mu.Lock()
	a.mappings = mappings
	a.mu.Unlock()
}

// Adapt applies the first mapping whose Match prefixes baseURL to body (mutated
// in place; body must be non-nil) and returns the headers to add plus the
// matched mapping's Match string. ok is false when no mapping applies, in which
// case body is left untouched and headers is nil.
func (a *Adapter) Adapt(baseURL, model, effort string, body map[string]any) (headers map[string]string, match string, ok bool) {
	a.mu.RLock()
	m := a.match(baseURL)
	a.mu.RUnlock()
	if m == nil {
		return nil, "", false
	}
	vars := map[string]string{varReasoningEffort: effort, varModel: model}
	applyBody(m.Body, body, vars)
	headers = make(map[string]string, len(m.Headers))
	for k, tpl := range m.Headers {
		v := resolveTemplate(tpl, vars)
		if v == "" || strings.Contains(v, "{{") {
			// Empty or unresolved — skip rather than send a blank/garbage header.
			continue
		}
		headers[k] = v
	}
	return headers, m.Match, true
}

func (a *Adapter) match(baseURL string) *Mapping {
	base := strings.TrimRight(baseURL, "/")
	for i := range a.mappings {
		m := &a.mappings[i]
		if matchPrefix(base, m.Match) {
			return m
		}
	}
	return nil
}

// matchPrefix reports whether base equals prefix or has prefix followed by "/"
// as a prefix. Both sides are trailing-slash-insensitive; an empty prefix
// never matches.
func matchPrefix(base, prefix string) bool {
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" || base == "" {
		return false
	}
	if base == prefix {
		return true
	}
	return strings.HasPrefix(base, prefix+"/")
}

// MappingsFromJSON parses a top-level JSON array of mappings (the format of an
// extension's mappings file).
func MappingsFromJSON(data []byte) ([]Mapping, error) {
	var ms []Mapping
	if err := json.Unmarshal(data, &ms); err != nil {
		return nil, err
	}
	return ms, nil
}

func applyBody(rules BodyRules, body map[string]any, vars map[string]string) {
	for _, p := range rules.Remove {
		deletePath(body, splitPath(p))
	}
	for key, absentKey := range rules.RemoveIf {
		if _, ok := body[absentKey]; !ok {
			delete(body, key)
		}
	}
	// Deterministic order, shallow paths before deeper ones: a set on a parent
	// path is applied before (and thus cannot clobber) a set on a child path.
	paths := make([]string, 0, len(rules.Set))
	for p := range rules.Set {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		val, ok := rules.Set[p].resolve(vars)
		if !ok {
			continue
		}
		setPath(body, splitPath(p), val)
	}
}

func (r SetRule) resolve(vars map[string]string) (any, bool) {
	table, isTable := r.Value.(map[string]any)
	if len(r.When) == 0 && !isTable {
		return resolvedValue(r.Value, vars)
	}
	from := r.From
	if from == "" {
		from = firstPlaceholder(r.placeholderString())
	}
	if isTable && from == "" {
		// A lookup table needs a variable to look up by: skip the rule.
		return nil, false
	}
	if len(r.When) > 0 {
		if from == "" {
			// When-gated but no variable to gate on: never apply.
			return nil, false
		}
		v := vars[from]
		allowed := false
		for _, w := range r.When {
			if v == w {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, false
		}
	}
	if isTable {
		v := vars[from]
		val, ok := table[v]
		if !ok {
			val, ok = table["*"]
		}
		if !ok {
			return nil, false
		}
		return resolvedValue(val, vars)
	}
	return resolvedValue(r.Value, vars)
}

// resolvedValue resolves templates in strings; other types are used as-is.
func resolvedValue(val any, vars map[string]string) (any, bool) {
	switch tv := val.(type) {
	case string:
		res := resolveTemplate(tv, vars)
		if strings.Contains(res, "{{") {
			return nil, false
		}
		return res, true
	default:
		return tv, true
	}
}

// placeholderString returns the rule's value if it is a string ("" otherwise,
// e.g. for table values).
func (r SetRule) placeholderString() string {
	s, _ := r.Value.(string)
	return s
}

func splitPath(p string) []string {
	p = strings.TrimSpace(p)
	if p == "" {
		return nil
	}
	return strings.Split(p, ".")
}

func setPath(body map[string]any, parts []string, val any) {
	if len(parts) == 0 {
		return
	}
	m := body
	for _, p := range parts[:len(parts)-1] {
		next, ok := m[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
	m[parts[len(parts)-1]] = val
}

func deletePath(body map[string]any, parts []string) {
	if len(parts) == 0 {
		return
	}
	m := body
	for _, p := range parts[:len(parts)-1] {
		next, ok := m[p].(map[string]any)
		if !ok {
			return
		}
		m = next
	}
	delete(m, parts[len(parts)-1])
}

func firstPlaceholder(s string) string {
	i := strings.Index(s, "{{")
	if i < 0 {
		return ""
	}
	j := strings.Index(s[i:], "}}")
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(s[i+2 : i+j])
}

// resolveTemplate replaces {{var}} occurrences using vars. Unknown
// placeholders are left intact so callers can detect them.
func resolveTemplate(tpl string, vars map[string]string) string {
	for k, v := range vars {
		tpl = strings.ReplaceAll(tpl, "{{"+k+"}}", v)
	}
	return tpl
}
