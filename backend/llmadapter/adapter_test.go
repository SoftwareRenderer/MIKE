package llmadapter

import (
	"reflect"
	"testing"
)

var openRouterMapping = Mapping{
	Match:   "https://openrouter.ai",
	Headers: map[string]string{"HTTP-Referer": "https://getmike.dev"},
	Body: BodyRules{
		Remove:   []string{"return_progress", "thinking_budget_tokens", "reasoning_control", "reasoning_effort", "chat_template_kwargs", "nested.junk"},
		RemoveIf: map[string]string{"tool_choice": "tools"},
		Set: map[string]SetRule{
			"reasoning.effort":  {Value: "{{reasoning_effort}}", When: []string{"low", "medium", "high"}},
			"reasoning.enabled": {From: "reasoning_effort", Value: true, When: []string{"", "default"}},
		},
	},
}

func baseBody() map[string]any {
	return map[string]any{
		"model":                  "some-model",
		"messages":               []any{map[string]any{"role": "user", "content": "hi"}},
		"stream":                 true,
		"return_progress":        true,
		"tool_choice":            "none",
		"thinking_budget_tokens": 8192,
		"nested":                 map[string]any{"junk": "x", "keep": "y"},
	}
}

func TestMatchPrefix(t *testing.T) {
	cases := []struct {
		base, prefix string
		want         bool
	}{
		{"https://openrouter.ai", "https://openrouter.ai", true},
		{"https://openrouter.ai/api/v1", "https://openrouter.ai", true},
		{"https://openrouter.ai/", "https://openrouter.ai", true},
		{"https://openrouter.ai", "https://openrouter.ai/", true},
		{"https://openrouter.ai/api/v1", "https://openrouter.ai/", true},
		{"https://openrouter.ai", "https://openrouter", false}, // host-boundary: "." is not "/"
		{"https://openrouterx.ai", "https://openrouter.ai", false},
		{"https://other.ai/api/v1", "https://openrouter.ai", false},
		{"", "https://openrouter.ai", false},
		{"https://openrouter.ai", "", false},
	}
	for _, c := range cases {
		if got := matchPrefix(c.base, c.prefix); got != c.want {
			t.Errorf("matchPrefix(%q, %q) = %v, want %v", c.base, c.prefix, got, c.want)
		}
	}
}

func TestAdaptOpenRouterShapes(t *testing.T) {
	a := NewAdapter()
	a.SetMappings([]Mapping{openRouterMapping})

	// Effort high → reasoning.effort set, no enabled.
	body := baseBody()
	headers, match, ok := a.Adapt("https://openrouter.ai/api/v1", "some-model", "high", body)
	if !ok || match != "https://openrouter.ai" {
		t.Fatalf("ok=%v match=%q, want matched", ok, match)
	}
	reasoning, _ := body["reasoning"].(map[string]any)
	if reasoning == nil || reasoning["effort"] != "high" {
		t.Errorf("reasoning = %v, want {effort: high}", body["reasoning"])
	}
	if _, exists := reasoning["enabled"]; exists {
		t.Errorf("reasoning.enabled should not be set for effort=high: %v", reasoning)
	}
	for _, gone := range []string{"return_progress", "tool_choice", "thinking_budget_tokens"} {
		if _, exists := body[gone]; exists {
			t.Errorf("body still has %q", gone)
		}
	}
	if nested, _ := body["nested"].(map[string]any); nested == nil || nested["junk"] != nil || nested["keep"] != "y" {
		t.Errorf("nested = %v, want junk removed, keep intact", body["nested"])
	}
	if headers["HTTP-Referer"] != "https://getmike.dev" {
		t.Errorf("headers = %v", headers)
	}

	// Effort default → reasoning.enabled true, no effort.
	body = baseBody()
	if _, _, ok := a.Adapt("https://openrouter.ai", "m", "default", body); !ok {
		t.Fatal("expected match")
	}
	reasoning, _ = body["reasoning"].(map[string]any)
	if reasoning == nil || reasoning["enabled"] != true {
		t.Errorf("reasoning = %v, want {enabled: true}", body["reasoning"])
	}
	if _, exists := reasoning["effort"]; exists {
		t.Errorf("reasoning.effort should not be set for effort=default: %v", reasoning)
	}

	// Effort none → no reasoning field at all.
	body = baseBody()
	if _, _, ok := a.Adapt("https://openrouter.ai", "m", "none", body); !ok {
		t.Fatal("expected match")
	}
	if _, exists := body["reasoning"]; exists {
		t.Errorf("reasoning should be absent for effort=none, got %v", body["reasoning"])
	}
}

func TestAdaptToolChoiceRemovedOnlyWhenNoTools(t *testing.T) {
	a := NewAdapter()
	a.SetMappings([]Mapping{openRouterMapping})

	body := baseBody()
	body["tools"] = []any{map[string]any{"type": "function"}}
	body["tool_choice"] = "auto" // what the client sends when tools are present
	if _, _, ok := a.Adapt("https://openrouter.ai", "m", "high", body); !ok {
		t.Fatal("expected match")
	}
	if body["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v, want kept when tools present", body["tool_choice"])
	}
}

func TestAdaptNoMatchPassthrough(t *testing.T) {
	a := NewAdapter()
	a.SetMappings([]Mapping{openRouterMapping})

	body := baseBody()
	headers, _, ok := a.Adapt("http://localhost:8080/v1", "m", "high", body)
	if ok {
		t.Fatal("expected no match")
	}
	if headers != nil {
		t.Errorf("headers = %v, want nil", headers)
	}
	if _, exists := body["return_progress"]; !exists {
		t.Error("body was mutated despite no match")
	}
}

func TestAdaptFirstMatchWins(t *testing.T) {
	other := Mapping{Match: "https://openrouter.ai", Headers: map[string]string{"X-Other": "1"}}
	a := NewAdapter()
	a.SetMappings([]Mapping{other, openRouterMapping})

	headers, match, ok := a.Adapt("https://openrouter.ai", "m", "high", baseBody())
	if !ok || match != "https://openrouter.ai" {
		t.Fatalf("ok=%v match=%q", ok, match)
	}
	// The first mapping (other) wins: its header, not openrouter's.
	if headers["X-Other"] != "1" || headers["HTTP-Referer"] != "" {
		t.Errorf("headers = %v, want first mapping only", headers)
	}
}

func TestAdaptHeaderTemplateResolution(t *testing.T) {
	a := NewAdapter()
	a.SetMappings([]Mapping{{
		Match: "https://example.com",
		Headers: map[string]string{
			"X-Model":  "{{model}}",
			"X-Effort": "{{reasoning_effort}}",
			"X-Broken": "{{unknown_var}}",
			"X-Static": "static",
		},
	}})

	headers, _, ok := a.Adapt("https://example.com/v1", "gpt-x", "medium", baseBody())
	if !ok {
		t.Fatal("expected match")
	}
	if headers["X-Model"] != "gpt-x" || headers["X-Effort"] != "medium" || headers["X-Static"] != "static" {
		t.Errorf("headers = %v", headers)
	}
	if _, exists := headers["X-Broken"]; exists {
		t.Errorf("unresolved header should be skipped: %v", headers)
	}

	// A header that resolves to the empty string is skipped.
	headers, _, _ = a.Adapt("https://example.com/v1", "gpt-x", "", baseBody())
	if _, exists := headers["X-Effort"]; exists {
		t.Errorf("empty-resolved header should be skipped: %v", headers)
	}
}

func TestSetRuleVariants(t *testing.T) {
	body := map[string]any{}

	// Plain-string shorthand: always applied, template-resolved.
	a := NewAdapter()
	a.SetMappings([]Mapping{{Match: "https://a", Body: BodyRules{Set: map[string]SetRule{
		"tag": {Value: "{{model}}"},
	}}}})
	if _, _, ok := a.Adapt("https://a", "m1", "low", body); !ok {
		t.Fatal("expected match")
	}
	if body["tag"] != "m1" {
		t.Errorf("tag = %v, want m1", body["tag"])
	}

	// Constant value, when-gated on explicit from, "" is a legitimate entry.
	body = map[string]any{}
	a.SetMappings([]Mapping{{Match: "https://a", Body: BodyRules{Set: map[string]SetRule{
		"on": {From: "reasoning_effort", Value: true, When: []string{""}},
	}}}})
	if _, _, ok := a.Adapt("https://a", "m", "", body); !ok {
		t.Fatal("expected match")
	}
	if body["on"] != true {
		t.Errorf("on = %v, want true for empty effort", body["on"])
	}

	// When-gated constant with no from and no placeholder → never applies.
	body = map[string]any{}
	a.SetMappings([]Mapping{{Match: "https://a", Body: BodyRules{Set: map[string]SetRule{
		"never": {Value: true, When: []string{"low"}},
	}}}})
	if _, _, ok := a.Adapt("https://a", "m", "low", body); !ok {
		t.Fatal("expected match")
	}
	if _, exists := body["never"]; exists {
		t.Errorf("when-gated rule without variable must never apply, got %v", body["never"])
	}

	// Default from: first placeholder in value.
	body = map[string]any{}
	a.SetMappings([]Mapping{{Match: "https://a", Body: BodyRules{Set: map[string]SetRule{
		"r.e": {Value: "{{reasoning_effort}}", When: []string{"high"}},
	}}}})
	if _, _, ok := a.Adapt("https://a", "m", "low", body); !ok {
		t.Fatal("expected match")
	}
	if _, exists := body["r"]; exists {
		t.Errorf("r should not be created when gate fails: %v", body)
	}
}

func TestSetRulesMergeIntoOneParent(t *testing.T) {
	a := NewAdapter()
	a.SetMappings([]Mapping{{Match: "https://a", Body: BodyRules{Set: map[string]SetRule{
		"reasoning.effort": {Value: "{{reasoning_effort}}", When: []string{"low", "medium", "high"}},
		"reasoning.extra":  {Value: "x"},
	}}}})
	body := map[string]any{}
	if _, _, ok := a.Adapt("https://a", "m", "low", body); !ok {
		t.Fatal("expected match")
	}
	want := map[string]any{"effort": "low", "extra": "x"}
	if !reflect.DeepEqual(body["reasoning"], want) {
		t.Errorf("reasoning = %v, want %v", body["reasoning"], want)
	}
}

func TestMappingsFromJSON(t *testing.T) {
	const doc = `[
	  {
	    "match": "https://openrouter.ai",
	    "headers": {"HTTP-Referer": "https://getmike.dev"},
	    "body": {
	      "remove": ["return_progress"],
	      "remove_if": {"tool_choice": "tools"},
	      "set": {
	        "reasoning.effort": {"value": "{{reasoning_effort}}", "when": ["low", "medium", "high"]},
	        "reasoning.enabled": {"from": "reasoning_effort", "value": true, "when": ["", "default"]},
	        "static.path": "plain-string-shorthand"
	      }
	    }
	  }
	]`
	ms, err := MappingsFromJSON([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ms) != 1 {
		t.Fatalf("len = %d", len(ms))
	}
	m := ms[0]
	if m.Match != "https://openrouter.ai" {
		t.Errorf("match = %q", m.Match)
	}
	if len(m.Body.Set) != 3 {
		t.Fatalf("set rules = %d, want 3", len(m.Body.Set))
	}
	if v, ok := m.Body.Set["static.path"].Value.(string); !ok || v != "plain-string-shorthand" {
		t.Errorf("shorthand rule = %#v", m.Body.Set["static.path"])
	}
	if v, ok := m.Body.Set["reasoning.enabled"].Value.(bool); !ok || !v {
		t.Errorf("boolean value rule = %#v", m.Body.Set["reasoning.enabled"])
	}
	if len(m.Body.Set["reasoning.effort"].When) != 3 {
		t.Errorf("when = %v", m.Body.Set["reasoning.effort"].When)
	}
	if _, err := MappingsFromJSON([]byte(`{"not":"an array"}`)); err == nil {
		t.Error("expected error for non-array doc")
	}
}

func TestRemoveNestedPathMissingParentNoop(t *testing.T) {
	a := NewAdapter()
	a.SetMappings([]Mapping{{Match: "https://a", Body: BodyRules{Remove: []string{"nope.deep.leaf", "top"}}}})
	body := map[string]any{"top": 1}
	if _, _, ok := a.Adapt("https://a", "m", "low", body); !ok {
		t.Fatal("expected match")
	}
	if _, exists := body["top"]; exists {
		t.Error("top should be removed")
	}
}
