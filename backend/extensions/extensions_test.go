package extensions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"kanban/backend/types"
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

func TestAgentConfig_ResolveSandboxMountTemplates(t *testing.T) {
	tests := []struct {
		name     string
		agent    *AgentConfig
		wantHost string
		wantCtn  string
		wantMode string
		wantEnv  string
		wantNil  bool
	}{
		{
			name: "resolve all templates from config_schema defaults",
			agent: &AgentConfig{
				ConfigSchema: &ConfigSchema{
					Properties: map[string]SchemaProp{
						"sandbox_mount_host_path":      {Default: "~/.pi/agent"},
						"sandbox_mount_container_path": {Default: "/root/.pi/agent"},
						"sandbox_mount_mode":           {Default: "rw"},
						"sandbox_mount_env_key":        {Default: "HOME"},
						"sandbox_mount_env_value":      {Default: "~/.pi/agent"},
					},
				},
				SandboxMount: &types.AgentConfigMount{
					HostPath:      "{{sandbox_mount_host_path}}",
					ContainerPath: "{{sandbox_mount_container_path}}",
					Mode:          "{{sandbox_mount_mode}}",
					Envs:          []types.SandboxEnv{{Key: "{{sandbox_mount_env_key}}", Value: "{{sandbox_mount_env_value}}"}},
				},
			},
			wantHost: "~/.pi/agent",
			wantCtn:  "/root/.pi/agent",
			wantMode: "rw",
			wantEnv:  "HOME",
		},
		{
			name: "literal values pass through unchanged",
			agent: &AgentConfig{
				SandboxMount: &types.AgentConfigMount{
					HostPath:      "/custom/path",
					ContainerPath: "/custom/container",
					Mode:          "ro",
					Envs:          []types.SandboxEnv{{Key: "MY_ENV_VAR", Value: "/custom/path"}},
				},
			},
			wantHost: "/custom/path",
			wantCtn:  "/custom/container",
			wantMode: "ro",
			wantEnv:  "MY_ENV_VAR",
		},
		{
			name: "no config_schema - templates pass through unchanged",
			agent: &AgentConfig{
				SandboxMount: &types.AgentConfigMount{
					HostPath: "{{sandbox_mount_host_path}}",
					Mode:     "{{sandbox_mount_mode}}",
				},
			},
			wantHost: "{{sandbox_mount_host_path}}",
			wantMode: "{{sandbox_mount_mode}}",
		},
		{
			name: "template for missing property passes through unchanged",
			agent: &AgentConfig{
				ConfigSchema: &ConfigSchema{
					Properties: map[string]SchemaProp{
						"sandbox_mount_host_path": {Default: "~/.pi/agent"},
					},
				},
				SandboxMount: &types.AgentConfigMount{
					HostPath:      "{{sandbox_mount_host_path}}",
					ContainerPath: "{{sandbox_mount_container_path}}",
					Mode:          "rw",
					Envs:          nil,
				},
			},
			wantHost: "~/.pi/agent",
			wantCtn:  "{{sandbox_mount_container_path}}",
			wantMode: "rw",
		},
		{
			name:    "nil agent config returns nil mount",
			agent:   nil,
			wantNil: true,
		},
		{
			name: "nil sandbox_mount returns nil",
			agent: &AgentConfig{
				SandboxMount: nil,
			},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var schema *ConfigSchema
			if tt.agent != nil {
				schema = tt.agent.ConfigSchema
			}
			mount := tt.agent.ResolveSandboxMountTemplates(schema)
			if tt.wantNil {
				if mount != nil {
					t.Errorf("expected nil mount, got %+v", mount)
				}
				return
			}
			if mount == nil {
				t.Fatal("expected non-nil mount")
			}
			if mount.HostPath != tt.wantHost {
				t.Errorf("HostPath = %q, want %q", mount.HostPath, tt.wantHost)
			}
			if mount.ContainerPath != tt.wantCtn {
				t.Errorf("ContainerPath = %q, want %q", mount.ContainerPath, tt.wantCtn)
			}
			if mount.Mode != tt.wantMode {
				t.Errorf("Mode = %q, want %q", mount.Mode, tt.wantMode)
			}
			if tt.wantEnv == "" {
				if len(mount.Envs) != 0 {
					t.Errorf("Envs = %+v, want empty", mount.Envs)
				}
			} else if len(mount.Envs) != 1 || mount.Envs[0].Key != tt.wantEnv {
				t.Errorf("Envs = %+v, want key %q", mount.Envs, tt.wantEnv)
			}
		})
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

func TestManifest_Priority_Default(t *testing.T) {
	m := ExtensionManifest{DisplayName: "Test"}
	if m.Priority != 0 {
		t.Errorf("Priority = %d, want 0 (default)", m.Priority)
	}
}

func TestManifest_Priority_Set(t *testing.T) {
	m := ExtensionManifest{DisplayName: "Test", Priority: 10}
	if m.Priority != 10 {
		t.Errorf("Priority = %d, want 10", m.Priority)
	}
}

func TestManifest_Priority_Negative(t *testing.T) {
	m := ExtensionManifest{DisplayName: "Test", Priority: -5}
	if m.Priority != -5 {
		t.Errorf("Priority = %d, want -5", m.Priority)
	}
}

func TestManifest_Priority_JSONRoundTrip(t *testing.T) {
	m := ExtensionManifest{
		DisplayName: "Test",
		Description: "Desc",
		Author:      "Author",
		Deprecated:  false,
		Priority:    42,
	}

	jsonBytes, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded ExtensionManifest
	if err := json.Unmarshal(jsonBytes, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Priority != 42 {
		t.Errorf("Priority after JSON round-trip = %d, want 42", decoded.Priority)
	}
}

func TestManifest_Priority_JSONDefault(t *testing.T) {
	// When priority is omitted in JSON, it should default to 0
	jsonStr := `{"display_name":"Test","description":"","author":"","deprecated":false}`
	var m ExtensionManifest
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if m.Priority != 0 {
		t.Errorf("Priority = %d, want 0 (default from missing field)", m.Priority)
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

// TestPiManifestTemplateResolution loads the actual Pi agent manifest and verifies
// that {{property}} template references in sandbox_mount are resolved using
// config_schema defaults end-to-end.
func TestPiManifestTemplateResolution(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "extensions", "agent", "pi", "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skipf("manifest not found at %s", manifestPath)
		}
		t.Fatalf("read manifest: %v", err)
	}

	var manifest ExtensionManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	if manifest.Agent == nil {
		t.Fatal("manifest.Agent is nil")
	}
	if manifest.Agent.SandboxMount == nil {
		t.Fatal("manifest.Agent.SandboxMount is nil")
	}

	// Verify template references are present in the raw manifest
	if manifest.Agent.SandboxMount.HostPath != "{{sandbox_mount_host_path}}" {
		t.Errorf("expected template host_path, got %q", manifest.Agent.SandboxMount.HostPath)
	}
	if manifest.Agent.SandboxMount.ContainerPath != "{{sandbox_mount_container_path}}" {
		t.Errorf("expected template container_path, got %q", manifest.Agent.SandboxMount.ContainerPath)
	}
	if manifest.Agent.SandboxMount.Mode != "{{sandbox_mount_mode}}" {
		t.Errorf("expected template mode, got %q", manifest.Agent.SandboxMount.Mode)
	}
	if len(manifest.Agent.SandboxMount.Envs) != 1 || manifest.Agent.SandboxMount.Envs[0].Key != "{{sandbox_mount_env_key}}" {
		t.Errorf("expected template env key, got %+v", manifest.Agent.SandboxMount.Envs)
	}

	// Resolve templates and verify against config_schema defaults
	mount := manifest.Agent.ResolveSandboxMountTemplates(manifest.ConfigSchema)
	if mount == nil {
		t.Fatal("resolved mount is nil")
	}
	if mount.HostPath != "~/.pi/agent" {
		t.Errorf("resolved HostPath = %q, want ~/.pi/agent", mount.HostPath)
	}
	if mount.ContainerPath != "/root/.pi/agent" {
		t.Errorf("resolved ContainerPath = %q, want /root/.pi/agent", mount.ContainerPath)
	}
	if mount.Mode != "rw" {
		t.Errorf("resolved Mode = %q, want rw", mount.Mode)
	}
	if len(mount.Envs) != 1 || mount.Envs[0].Key != "HOME" {
		t.Errorf("resolved Envs = %+v, want key HOME", mount.Envs)
	}
}
