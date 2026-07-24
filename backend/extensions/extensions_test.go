package extensions

import (
	"encoding/json"
	"testing"
	"time"
)

func makeTestExtension(t *testing.T, extType, installSource string) *Extension {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	return &Extension{
		ID:            "test-ext-1",
		Name:          "test-extension",
		Version:       "1.0.0",
		Type:          extType,
		Manifest:      ExtensionManifest{DisplayName: "Test Extension"},
		Config:        map[string]any{},
		InstallSource: installSource,
		Enabled:       true,
		InstalledAt:   now,
		BuiltAt:       now,
	}
}

func TestExtension_IsLSP(t *testing.T) {
	tests := []struct {
		name string
		ext  *Extension
		want bool
	}{
		{"lsp type", makeTestExtension(t, ExtensionTypeLSP, "registry"), true},
		{"ui type", makeTestExtension(t, ExtensionTypeUI, "registry"), false},
		{"unknown type", makeTestExtension(t, "unknown", "registry"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ext.IsLSP(); got != tt.want {
				t.Errorf("IsLSP() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtension_IsUI(t *testing.T) {
	tests := []struct {
		name string
		ext  *Extension
		want bool
	}{
		{"lsp type", makeTestExtension(t, ExtensionTypeLSP, "registry"), false},
		{"ui type", makeTestExtension(t, ExtensionTypeUI, "registry"), true},
		{"unknown type", makeTestExtension(t, "unknown", "registry"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ext.IsUI(); got != tt.want {
				t.Errorf("IsUI() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtension_HasLSPConfig(t *testing.T) {
	ext := makeTestExtension(t, ExtensionTypeLSP, "registry")
	// No LSP config set
	if ext.HasLSPConfig() {
		t.Error("HasLSPConfig() = true, want false (no LSP config)")
	}
	// Set LSP config
	ext.Manifest.LSP = &LSPConfig{Command: "gopls", Args: []string{"serve"}}
	if !ext.HasLSPConfig() {
		t.Error("HasLSPConfig() = false, want true")
	}
	// UI type with LSP config (should still be false)
	uiExt := makeTestExtension(t, ExtensionTypeUI, "registry")
	uiExt.Manifest.LSP = &LSPConfig{Command: "test"}
	if uiExt.HasLSPConfig() {
		t.Error("HasLSPConfig() = true for UI extension, want false")
	}
}

func TestExtension_HasUIConfig(t *testing.T) {
	ext := makeTestExtension(t, ExtensionTypeUI, "registry")
	// No UI config set
	if ext.HasUIConfig() {
		t.Error("HasUIConfig() = true, want false (no UI config)")
	}
	// Set UI config
	ext.Manifest.UI = &UIConfig{ModuleURL: "/ext/test.js"}
	if !ext.HasUIConfig() {
		t.Error("HasUIConfig() = false, want true")
	}
	// LSP type with UI config (should still be false)
	lspExt := makeTestExtension(t, ExtensionTypeLSP, "registry")
	lspExt.Manifest.UI = &UIConfig{ModuleURL: "/ext/test.js"}
	if lspExt.HasUIConfig() {
		t.Error("HasUIConfig() = true for LSP extension, want false")
	}
}

func TestExtension_InstallSourceChecks(t *testing.T) {
	tests := []struct {
		source       string
		wantRegistry bool
		wantManual   bool
		wantLocal    bool
	}{
		{"registry", true, false, false},
		{"manual", false, true, false},
		{"local", false, false, true},
		{"unknown", false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			ext := makeTestExtension(t, ExtensionTypeLSP, tt.source)
			if got := ext.IsRegistryInstalled(); got != tt.wantRegistry {
				t.Errorf("IsRegistryInstalled() = %v, want %v", got, tt.wantRegistry)
			}
			if got := ext.IsManualInstalled(); got != tt.wantManual {
				t.Errorf("IsManualInstalled() = %v, want %v", got, tt.wantManual)
			}
			if got := ext.IsLocalInstalled(); got != tt.wantLocal {
				t.Errorf("IsLocalInstalled() = %v, want %v", got, tt.wantLocal)
			}
		})
	}
}

func TestLSPConfig_FullConfig(t *testing.T) {
	cfg := &LSPConfig{
		Command:    "gopls",
		Args:       []string{"serve"},
		Extensions: []string{"go"},
		Language:   "go",
		InitParamsOverride: map[string]any{
			"hover": map[string]any{
				"actions": map[string]any{
					"enabled": true,
				},
			},
			"usePlaceholders": true,
			"staticcheck":     true,
		},
	}

	if cfg.Command != "gopls" {
		t.Errorf("Command = %v, want gopls", cfg.Command)
	}
	if len(cfg.Args) != 1 || cfg.Args[0] != "serve" {
		t.Errorf("Args = %v, want [serve]", cfg.Args)
	}
	if len(cfg.Extensions) != 1 || cfg.Extensions[0] != "go" {
		t.Errorf("Extensions = %v, want [go]", cfg.Extensions)
	}
	if cfg.Language != "go" {
		t.Errorf("Language = %v, want go", cfg.Language)
	}
	if cfg.InitParamsOverride["staticcheck"] != true {
		t.Error("InitParamsOverride.staticcheck = false, want true")
	}
}

func TestRegistryEntry_Fields(t *testing.T) {
	entry := RegistryEntry{
		Name:          "gopls",
		Version:       "1.0.0",
		Type:          ExtensionTypeLSP,
		DisplayName:   "Go Language Server",
		Description:   "Go language server",
		Author:        "Go Team",
		Image:         "ghcr.io/kanban/gopls:1.0.0",
		DockerfileURL: "https://registry.kanban.dev/extensions/gopls/1.0.0/Dockerfile",
		ManifestURL:   "https://registry.kanban.dev/extensions/gopls/1.0.0/manifest.json",
	}

	if entry.Name != "gopls" {
		t.Errorf("Name = %v, want gopls", entry.Name)
	}
	if entry.Type != ExtensionTypeLSP {
		t.Errorf("Type = %v, want lsp", entry.Type)
	}
	if entry.Image == "" {
		t.Error("Image is empty, want non-empty")
	}
	if entry.DockerfileURL == "" {
		t.Error("DockerfileURL is empty, want non-empty")
	}
}

func TestConfigSchema_Properties(t *testing.T) {
	minVal := 64.0
	maxVal := 2048.0
	schema := &ConfigSchema{
		Type: "object",
		Properties: map[string]SchemaProp{
			"memoryLimit": {
				Type:        "number",
				Title:       "Memory Limit",
				Description: "Memory limit in MB",
				Default:     256,
				Minimum:     &minVal,
				Maximum:     &maxVal,
			},
			"networkIsolation": {
				Type:        "boolean",
				Title:       "Network Isolation",
				Description: "Enable network isolation",
				Default:     true,
			},
		},
		Required: []string{"memoryLimit"},
	}

	if schema.Type != "object" {
		t.Errorf("Type = %v, want object", schema.Type)
	}
	if len(schema.Properties) != 2 {
		t.Errorf("Properties count = %v, want 2", len(schema.Properties))
	}
	memProp := schema.Properties["memoryLimit"]
	if memProp.Type != "number" {
		t.Errorf("memoryLimit.Type = %v, want number", memProp.Type)
	}
	if *memProp.Minimum != 64.0 {
		t.Errorf("memoryLimit.Minimum = %v, want 64", *memProp.Minimum)
	}
	if *memProp.Maximum != 2048.0 {
		t.Errorf("memoryLimit.Maximum = %v, want 2048", *memProp.Maximum)
	}
	netProp := schema.Properties["networkIsolation"]
	if netProp.Type != "boolean" {
		t.Errorf("networkIsolation.Type = %v, want boolean", netProp.Type)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "memoryLimit" {
		t.Errorf("Required = %v, want [memoryLimit]", schema.Required)
	}
}

func TestSandboxConfig_Defaults(t *testing.T) {
	cfg := SandboxConfig{
		NetworkIsolation: true,
		MemoryLimit:      256,
	}

	if !cfg.NetworkIsolation {
		t.Error("NetworkIsolation = false, want true")
	}
	if cfg.MemoryLimit != 256 {
		t.Errorf("MemoryLimit = %v, want 256", cfg.MemoryLimit)
	}
	if len(cfg.ForwardPorts) != 0 {
		t.Errorf("ForwardPorts = %v, want empty", cfg.ForwardPorts)
	}
	if len(cfg.Capabilities) != 0 {
		t.Errorf("Capabilities = %v, want empty", cfg.Capabilities)
	}
}

func TestCSSConfig_CSSURL(t *testing.T) {
	cfg := &CSSConfig{
		CSSURL: "/extensions/mike-light/styles.css",
	}

	if cfg.CSSURL != "/extensions/mike-light/styles.css" {
		t.Errorf("CSSURL = %v, want /extensions/mike-light/styles.css", cfg.CSSURL)
	}
}

func TestCSSConfig_JSONRoundTrip(t *testing.T) {
	// Verify that css_url is correctly serialized/deserialized via JSON
	original := &CSSConfig{
		CSSURL: "/extensions/mike-light/styles.css",
	}

	jsonBytes, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded CSSConfig
	if err := json.Unmarshal(jsonBytes, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.CSSURL != original.CSSURL {
		t.Errorf("CSSURL after JSON round-trip = %v, want %v", decoded.CSSURL, original.CSSURL)
	}
}

func TestExtension_IsCSS(t *testing.T) {
	tests := []struct {
		name string
		ext  *Extension
		want bool
	}{
		{"css type", makeTestExtension(t, ExtensionTypeCSS, "registry"), true},
		{"lsp type", makeTestExtension(t, ExtensionTypeLSP, "registry"), false},
		{"ui type", makeTestExtension(t, ExtensionTypeUI, "registry"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ext.IsCSS(); got != tt.want {
				t.Errorf("IsCSS() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtension_HasCSSConfig(t *testing.T) {
	// CSS type with CSS config
	cssExt := makeTestExtension(t, ExtensionTypeCSS, "manual")
	cssExt.Manifest.CSS = &CSSConfig{CSSURL: "/theme.css"}
	if !cssExt.HasCSSConfig() {
		t.Error("HasCSSConfig() = false, want true")
	}

	// CSS type without CSS config
	emptyCSS := makeTestExtension(t, ExtensionTypeCSS, "manual")
	if emptyCSS.HasCSSConfig() {
		t.Error("HasCSSConfig() = true, want false (no CSS config)")
	}

	// UI type with CSS config (should still be false)
	uiExt := makeTestExtension(t, ExtensionTypeUI, "manual")
	uiExt.Manifest.CSS = &CSSConfig{CSSURL: "/theme.css"}
	if uiExt.HasCSSConfig() {
		t.Error("HasCSSConfig() = true for UI extension, want false")
	}
}
