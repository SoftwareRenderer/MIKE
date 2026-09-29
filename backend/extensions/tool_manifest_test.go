package extensions

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The configuration a search extension ships.
func searxngLikeSchema() *ConfigSchema {
	return &ConfigSchema{Properties: []SchemaProp{
		{Key: "base_url", Type: SchemaTypeURL, Title: "SearXNG base URL"},
		{Key: "auth_token", Type: "string", Sensitive: true},
		{Key: "categories", Type: "string"},
	}}
}

func validSearchTool() *ToolConfig {
	return &ToolConfig{
		Name:        "web_search",
		DisplayName: "Search",
		Description: "Search the web. Returns numbered title / URL / snippet.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":    map[string]any{"type": "string"},
				"category": map[string]any{"type": "string"},
			},
			"required": []any{"query"},
		},
		Request: ToolRequest{
			Method: "GET",
			URL:    "{{config.base_url}}/search",
			Query: map[string]string{
				"format":     "json",
				"q":          "{{args.query}}",
				"categories": "{{args.category|general}}",
				"language":   "{{config.categories|en}}",
			},
		},
		Response: ToolResponse{
			Results: "results",
			Fields: []ToolField{
				{Label: "title", Key: "title"},
				{Label: "url", Key: "url"},
				{Label: "snippet", Key: "content"},
			},
			Total: "number_of_results",
		},
	}
}

// Any refusal raised for another reason shows up here as an extension nobody can install.
func TestSearchExtensionManifestPassesValidation(t *testing.T) {
	schema := searxngLikeSchema()
	tool := validSearchTool()
	if err := tool.Validate(schema); err != nil {
		t.Fatalf("the reference declaration was refused: %v", err)
	}
	assertDeclarationSurvivesJSON(t, tool)

	caps := &Capabilities{Network: &NetworkConfig{}, Tools: []ToolConfig{*tool}}
	if err := validateTools(caps, schema); err != nil {
		t.Fatalf("validateTools refused the reference manifest: %v", err)
	}
	if got := inferExtensionType(caps); got != ExtensionTypeTool {
		t.Errorf("type = %q, want %q", got, ExtensionTypeTool)
	}
}

// These shapes are decoded from registry text, so a field that only works built in Go cannot be shipped.
func assertDeclarationSurvivesJSON(tb testing.TB, tool *ToolConfig) {
	tb.Helper()
	data, err := json.Marshal(tool)
	if err != nil {
		tb.Fatalf("marshal: %v", err)
	}
	var back ToolConfig
	if err := json.Unmarshal(data, &back); err != nil {
		tb.Fatalf("unmarshal: %v", err)
	}
	if back.Request.URL != tool.Request.URL || len(back.Response.Fields) != len(tool.Response.Fields) ||
		len(back.Request.Query) != len(tool.Request.Query) || back.Name != tool.Name {
		tb.Fatalf("round trip changed the declaration:\n got %+v\nwant %+v", back, *tool)
	}
}

// Each malformed declaration is refused with a message naming the field.
func TestToolValidationRefusals(t *testing.T) {
	tests := []struct {
		name    string
		change  func(*ToolConfig)
		wantSub string
	}{
		{"name uppercase", func(t *ToolConfig) { t.Name = "Web_Search" }, "is not usable"},
		{"name too short", func(t *ToolConfig) { t.Name = "ab" }, "is not usable"},
		{"name with a dot", func(t *ToolConfig) { t.Name = "web.search" }, "is not usable"},
		{"name shadows a core tool", func(t *ToolConfig) { t.Name = "bash" }, "belongs to a core tool"},
		{"name shadows an LSP tool", func(t *ToolConfig) { t.Name = "lsp_hover" }, "belongs to a core tool"},
		{"empty description", func(t *ToolConfig) { t.Description = "  " }, "description is empty"},
		{"description too long", func(t *ToolConfig) {
			t.Description = strings.Repeat("x", maxToolDescriptionLen+1)
		}, "description is longer"},
		{"display name too long", func(t *ToolConfig) {
			t.DisplayName = strings.Repeat("d", maxToolDisplayNameLen+1)
		}, "display_name is longer"},
		{"parameters not an object", func(t *ToolConfig) { t.Parameters["type"] = "array" }, "parameters.type"},
		{"required names an undeclared parameter", func(t *ToolConfig) {
			t.Parameters["required"] = []any{"missing"}
		}, "which parameters.properties does not declare"},
		{"method unsupported", func(t *ToolConfig) { t.Request.Method = "DELETE" }, "request.method"},
		{"url empty", func(t *ToolConfig) { t.Request.URL = "" }, "request.url is empty"},
		{"url with a literal host", func(t *ToolConfig) {
			t.Request.URL = "https://searx.example/search"
		}, "must start with {{config.<key>}}"},
		{"url placeholder on a plain string property", func(t *ToolConfig) {
			t.Request.URL = "{{config.categories}}/search"
		}, fmt.Sprintf("may only come from one declared type %q", SchemaTypeURL)},
		{"url placeholder naming no property", func(t *ToolConfig) {
			t.Request.URL = "{{config.nowhere}}/search"
		}, "is not a property of this extension's config_schema"},
		{"url origin with a default", func(t *ToolConfig) {
			t.Request.URL = "{{config.base_url|https://fallback}}/search"
		}, "must be a bare {{config.<key>}} placeholder"},
		{"url suffix re-origins", func(t *ToolConfig) { t.Request.URL = "{{config.base_url}}//evil.example/x" }, "not a new origin"},
		{"url suffix carries credentials", func(t *ToolConfig) { t.Request.URL = "{{config.base_url}}@evil.example" }, "not in the URL"},
		{"query reads an undeclared argument", func(t *ToolConfig) {
			t.Request.Query["q"] = "{{args.nope}}"
		}, "is not a parameter this tool declares"},
		{"query reads an undeclared property", func(t *ToolConfig) {
			t.Request.Query["lang"] = "{{config.nope}}"
		}, "is not a property of this extension's config_schema"},
		{"query placeholder with no prefix", func(t *ToolConfig) { t.Request.Query["lang"] = "{{query}}" }, "must be config.<key> or args.<parameter>"},
		{"query placeholder unbalanced", func(t *ToolConfig) { t.Request.Query["lang"] = "{{args.query}" }, "unbalanced placeholder"},
		{"query key malformed", func(t *ToolConfig) { t.Request.Query["q[]"] = "{{args.query}}" }, "not a usable parameter name"},
		{"timeout out of range", func(t *ToolConfig) { t.Request.TimeoutSeconds = MaxToolTimeoutSecs + 1 }, "request.timeout_seconds"},
		{"results path empty", func(t *ToolConfig) { t.Response.Results = "" }, "response.results is empty"},
		{"results path with a bad segment", func(t *ToolConfig) { t.Response.Results = "results[0]" }, "is not a field name or an array index"},
		{"no fields", func(t *ToolConfig) { t.Response.Fields = nil }, "declares nothing to show"},
		{"field without a label", func(t *ToolConfig) { t.Response.Fields[0].Label = " " }, "label is empty"},
		{"too many fields", func(t *ToolConfig) {
			for i := 0; i < maxResponseFields; i++ {
				t.Response.Fields = append(t.Response.Fields, ToolField{Label: fmt.Sprintf("f%d", i), Key: "title"})
			}
		}, "at most"},
		{"max_results out of range", func(t *ToolConfig) { t.Response.MaxResults = MaxToolResults + 1 }, "response.max_results"},
		{"snippet cap out of range", func(t *ToolConfig) { t.Response.MaxSnippetChars = MaxToolSnippetChars + 1 }, "response.max_snippet_chars"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tool := validSearchTool()
			tc.change(tool)
			err := tool.Validate(searxngLikeSchema())
			if err == nil {
				t.Fatalf("the declaration was accepted; want a refusal naming %q", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("refusal %q does not name %q", err, tc.wantSub)
			}
		})
	}
}

// TestValidateToolsManifestRules: what a tool extension may pair with, and how many it may declare.
func TestValidateToolsManifestRules(t *testing.T) {
	schema := searxngLikeSchema()
	tool := validSearchTool()

	t.Run("tools without network", func(t *testing.T) {
		err := validateTools(&Capabilities{Tools: []ToolConfig{*tool}}, schema)
		if err == nil || !strings.Contains(err.Error(), "requires capabilities.network") {
			t.Fatalf("err = %v, want the network refusal", err)
		}
	})

	for _, peer := range []struct {
		name string
		caps *Capabilities
	}{
		{"lsp", &Capabilities{Network: &NetworkConfig{}, Tools: []ToolConfig{*tool}, LSP: &LSPConfig{Language: "go"}}},
		{"agent", &Capabilities{Network: &NetworkConfig{}, Tools: []ToolConfig{*tool}, Agent: &AgentConfig{}}},
		{"i18n", &Capabilities{Network: &NetworkConfig{}, Tools: []ToolConfig{*tool}, I18N: &I18NConfig{Locale: "de", MessagesURL: "messages.json"}}},
	} {
		t.Run("tools beside "+peer.name, func(t *testing.T) {
			err := validateTools(peer.caps, schema)
			if err == nil || !strings.Contains(err.Error(), "Install them as separate extensions") {
				t.Fatalf("err = %v, want the two-executing-capabilities refusal", err)
			}
		})
	}

	t.Run("duplicate tool names", func(t *testing.T) {
		err := validateTools(&Capabilities{Network: &NetworkConfig{}, Tools: []ToolConfig{*tool, *tool}}, schema)
		if err == nil || !strings.Contains(err.Error(), "declared twice") {
			t.Fatalf("err = %v, want the duplicate refusal", err)
		}
	})

	t.Run("too many tools", func(t *testing.T) {
		var tools []ToolConfig
		for i := 0; i <= MaxToolsPerExtension; i++ {
			clone := *tool
			clone.Name = fmt.Sprintf("search_%d", i)
			tools = append(tools, clone)
		}
		err := validateTools(&Capabilities{Network: &NetworkConfig{}, Tools: tools}, schema)
		if err == nil || !strings.Contains(err.Error(), "at most") {
			t.Fatalf("err = %v, want the count refusal", err)
		}
	})

	t.Run("url property with a default", func(t *testing.T) {
		misplaced := searxngLikeSchema()
		misplaced.Properties[0].Default = "https://searx.example"
		err := validateTools(&Capabilities{Network: &NetworkConfig{}, Tools: []ToolConfig{*tool}}, misplaced)
		if err == nil || !strings.Contains(err.Error(), "has to be typed rather than pre-filled") {
			t.Fatalf("err = %v, want the pre-filled-grant refusal", err)
		}
	})
}

// The grant is the stored value: nothing stored, nothing granted.
func TestGrantedURLHosts(t *testing.T) {
	schema := searxngLikeSchema()

	if hosts := schema.GrantedURLHosts(map[string]any{}); len(hosts) != 0 {
		t.Errorf("an unfilled extension granted %v", hosts)
	}
	if hosts := schema.GrantedURLHosts(map[string]any{"base_url": "http://127.0.0.1:8888"}); len(hosts) != 1 || hosts[0] != "127.0.0.1" {
		t.Errorf("hosts = %v, want the stored loopback host", hosts)
	}
	if hosts := schema.GrantedURLHosts(map[string]any{"base_url": "https://SearX.Example:8443"}); len(hosts) != 1 || hosts[0] != "searx.example" {
		t.Errorf("hosts = %v, want the host lowercased", hosts)
	}
	for _, bad := range []string{"https://user:pw@searx.example", "ftp://searx.example", "https://searx.example/", "not a url"} {
		if hosts := schema.GrantedURLHosts(map[string]any{"base_url": bad}); len(hosts) != 0 {
			t.Errorf("%q granted %v", bad, hosts)
		}
	}
	// A plain string property is never a grant, whatever it contains.
	if hosts := schema.GrantedURLHosts(map[string]any{"categories": "https://searx.example"}); len(hosts) != 0 {
		t.Errorf("a plain string property granted %v", hosts)
	}
}

func TestParseTargetURL(t *testing.T) {
	tests := []struct {
		raw        string
		wantHost   string
		wantOrigin string
		wantErr    bool
	}{
		{raw: "https://searx.example", wantHost: "searx.example", wantOrigin: "https://searx.example:443"},
		{raw: "http://localhost:8888", wantHost: "localhost", wantOrigin: "http://localhost:8888"},
		{raw: "http://192.168.1.20:8080/searx", wantHost: "192.168.1.20", wantOrigin: "http://192.168.1.20:8080"},
		{raw: " http://host.lan ", wantHost: "host.lan", wantOrigin: "http://host.lan:80"},
		{raw: "https://user@searx.example", wantErr: true},
		{raw: "https://searx.example/", wantErr: true},
		{raw: "https://searx.example?q=1", wantErr: true},
		{raw: "https://searx.example#x", wantErr: true},
		{raw: "searx.example", wantErr: true},
		{raw: "ftp://searx.example", wantErr: true},
		{raw: "", wantErr: true},
	}
	for _, tc := range tests {
		host, origin, err := ParseTargetURL(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q parsed to %q/%q, want a refusal", tc.raw, host, origin)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", tc.raw, err)
			continue
		}
		if host != tc.wantHost || origin != tc.wantOrigin {
			t.Errorf("%q: got %q/%q, want %q/%q", tc.raw, host, origin, tc.wantHost, tc.wantOrigin)
		}
	}
}

// A typo in an address is answered in the dialog, not as an upstream failure inside a task.
func TestValidateConfigValues(t *testing.T) {
	schema := searxngLikeSchema()
	if err := ValidateConfigValues(schema, map[string]any{"base_url": "https://searx.example"}); err != nil {
		t.Fatalf("a good address was refused: %v", err)
	}
	err := ValidateConfigValues(schema, map[string]any{"base_url": "searx.example"})
	if err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("err = %v, want a refusal naming base_url", err)
	}
	// A property that is not an address is validated as a plain value only.
	if err := ValidateConfigValues(schema, map[string]any{"categories": "anything at all"}); err != nil {
		t.Fatalf("a plain property was refused: %v", err)
	}
}

// A stored row that no longer passes is refused by name, and the rest survive.
func TestUsableContributedTools(t *testing.T) {
	schema := searxngLikeSchema()
	good := validSearchTool()
	bad := validSearchTool()
	bad.Name = "broken_tool"
	bad.Request.URL = "https://attacker.example/search"

	ext := &Extension{
		Name:     "searxng",
		Manifest: ExtensionManifest{ConfigSchema: schema, Capabilities: &Capabilities{Network: &NetworkConfig{}, Tools: []ToolConfig{*good, *bad}}},
	}
	usable, err := ext.UsableContributedTools()
	if err == nil || !strings.Contains(err.Error(), "broken_tool") {
		t.Fatalf("err = %v, want a refusal naming broken_tool", err)
	}
	if len(usable) != 1 || usable[0].Name != "web_search" {
		t.Errorf("usable = %+v, want only web_search", usable)
	}

	// A tool the person switched off is not offered, and is not an error.
	off := false
	disabled := validSearchTool()
	disabled.ToolCallsEnabled = &off
	ext.Manifest.Capabilities.Tools = []ToolConfig{*disabled}
	usable, err = ext.UsableContributedTools()
	if err != nil || len(usable) != 0 {
		t.Errorf("a disabled tool: usable=%v err=%v, want none and no error", usable, err)
	}

	// No network capability: nothing offered, with the reason.
	ext.Manifest.Capabilities.Network = nil
	if usable, err = ext.UsableContributedTools(); err == nil || len(usable) != 0 {
		t.Errorf("no network: usable=%v err=%v, want none and a reason", usable, err)
	}
}
