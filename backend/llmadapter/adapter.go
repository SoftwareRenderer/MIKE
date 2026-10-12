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
	varReasoningEffort = "reasoning_effort"
	varModel = "model"
)

// Mapping describes how to adapt MIKE's request for one provider.
type Mapping struct {
	// Provider base-URL prefix. Matches if base equals prefix or is followed by '/'
	// (e.g. 'https://openrouter' does not match 'https://openrouter.ai').
	Match string `json:"match"`
	// Template-resolved headers. Headers with unresolved {{placeholders}} are skipped.
	Headers map[string]string `json:"headers,omitempty"`
	// Body transforms the request body in place.
	Body BodyRules `json:"body,omitempty"`
}

// BodyRules describes request-body transformations, applied in the order:
// remove, remove_if, set.
type BodyRules struct {
	Remove   []string          `json:"remove,omitempty"`
	// Deletes key only when referenced key is absent (e.g. drop tool_choice when no tools are sent).
	RemoveIf map[string]string `json:"remove_if,omitempty"`
	Set      map[string]SetRule `json:"set,omitempty"`
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
	// Variable used for gating and table lookups. If empty, uses the first placeholder in the string value.
	From string `json:"from,omitempty"`
	// Value to write: a string (template-resolved), a scalar (verbatim), or a map (lookup table with optional '*' fallback).
	Value any `json:"value,omitempty"`
	// Rule applies only if resolved 'from' value is in this list. Gated rules with no resolvable variable never apply.
	When []string `json:"when,omitempty"`
}

type Adapter struct {
	mu       sync.RWMutex
	mappings []Mapping
}

func NewAdapter() *Adapter { return &Adapter{} }

// SetMappings replaces the active mapping set. First match wins.
func (a *Adapter) SetMappings(mappings []Mapping) {
	a.mu.Lock()
	a.mappings = mappings
	a.mu.Unlock()
}

// Adapt applies the first matching mapping to body and returns headers to add.
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

// resolveTemplate replaces {{var}} using vars. Unresolved placeholders are left intact.
func resolveTemplate(tpl string, vars map[string]string) string {
	for k, v := range vars {
		tpl = strings.ReplaceAll(tpl, "{{"+k+"}}", v)
	}
	return tpl
}
