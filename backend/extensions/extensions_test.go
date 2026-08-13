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
	ext.Manifest.Capabilities = &Capabilities{LSP: &LSPConfig{Command: "gopls", Args: []string{"serve"}}}
	if !ext.HasLSPConfig() {
		t.Error("HasLSPConfig() = false, want true")
	}
	// UI type with LSP config (should still be false)
	uiExt := makeTestExtension(t, ExtensionTypeUI, "registry")
	uiExt.Manifest.Capabilities = &Capabilities{LSP: &LSPConfig{Command: "test"}}
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
	ext.Manifest.Capabilities = &Capabilities{UI: &UIConfig{InjectionPoints: []string{"createTask"}}}
	if !ext.HasUIConfig() {
		t.Error("HasUIConfig() = false, want true")
	}
	// LSP type with UI config (should still be false)
	lspExt := makeTestExtension(t, ExtensionTypeLSP, "registry")
	lspExt.Manifest.Capabilities = &Capabilities{UI: &UIConfig{InjectionPoints: []string{"createTask"}}}
	if lspExt.HasUIConfig() {
		t.Error("HasUIConfig() = true for LSP extension, want false")
	}
}

func TestExtension_InstallSourceChecks(t *testing.T) {
	tests := []struct {
		source       string
		wantRegistry bool
		wantLocal    bool
	}{
		{"registry", true, false},
		{"local", false, true},
		{"unknown", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			ext := makeTestExtension(t, ExtensionTypeLSP, tt.source)
			if got := ext.IsRegistryInstalled(); got != tt.wantRegistry {
				t.Errorf("IsRegistryInstalled() = %v, want %v", got, tt.wantRegistry)
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

func TestLSPConfig_EditorEnabled(t *testing.T) {
	// Default (nil) should return true
	cfg := &LSPConfig{Command: "gopls", Args: []string{"serve"}}
	if !cfg.IsEditorEnabled() {
		t.Error("IsEditorEnabled() = false, want true (nil defaults to true)")
	}

	// Explicitly enabled
	trueVal := true
	cfg.EditorEnabled = &trueVal
	if !cfg.IsEditorEnabled() {
		t.Error("IsEditorEnabled() = false, want true (explicitly enabled)")
	}

	// Explicitly disabled
	falseVal := false
	cfg.EditorEnabled = &falseVal
	if cfg.IsEditorEnabled() {
		t.Error("IsEditorEnabled() = true, want false (explicitly disabled)")
	}

	// Nil config
	var nilCfg *LSPConfig
	if !nilCfg.IsEditorEnabled() {
		t.Error("nil.IsEditorEnabled() = false, want true")
	}
}

func TestLSPConfig_ToolCallsEnabled(t *testing.T) {
	// Default (nil) should return true
	cfg := &LSPConfig{Command: "gopls", Args: []string{"serve"}}
	if !cfg.IsToolCallsEnabled() {
		t.Error("IsToolCallsEnabled() = false, want true (nil defaults to true)")
	}

	// Explicitly enabled
	trueVal := true
	cfg.ToolCallsEnabled = &trueVal
	if !cfg.IsToolCallsEnabled() {
		t.Error("IsToolCallsEnabled() = false, want true (explicitly enabled)")
	}

	// Explicitly disabled
	falseVal := false
	cfg.ToolCallsEnabled = &falseVal
	if cfg.IsToolCallsEnabled() {
		t.Error("IsToolCallsEnabled() = true, want false (explicitly disabled)")
	}

	// Nil config
	var nilCfg *LSPConfig
	if !nilCfg.IsToolCallsEnabled() {
		t.Error("nil.IsToolCallsEnabled() = false, want true")
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
		Properties: []SchemaProp{
			{
				Key:         "memoryLimit",
				Type:        "number",
				Title:       "Memory Limit",
				Description: "Memory limit in MB",
				Default:     256,
				Minimum:     &minVal,
				Maximum:     &maxVal,
			},
			{
				Key:         "networkIsolation",
				Type:        "boolean",
				Title:       "Network Isolation",
				Description: "Enable network isolation",
				Default:     true,
			},
		},
	}

	if len(schema.Properties) != 2 {
		t.Errorf("Properties count = %v, want 2", len(schema.Properties))
	}
	memProp := schema.GetProperty("memoryLimit")
	if memProp == nil {
		t.Fatal("GetProperty(memoryLimit) returned nil")
	}
	if memProp.Type != "number" {
		t.Errorf("memoryLimit.Type = %v, want number", memProp.Type)
	}
	if *memProp.Minimum != 64.0 {
		t.Errorf("memoryLimit.Minimum = %v, want 64", *memProp.Minimum)
	}
	if *memProp.Maximum != 2048.0 {
		t.Errorf("memoryLimit.Maximum = %v, want 2048", *memProp.Maximum)
	}
	netProp := schema.GetProperty("networkIsolation")
	if netProp == nil {
		t.Fatal("GetProperty(networkIsolation) returned nil")
	}
	if netProp.Type != "boolean" {
		t.Errorf("networkIsolation.Type = %v, want boolean", netProp.Type)
	}
}

func TestConfigSchema_JSON_PreservesPropertyOrder(t *testing.T) {
	jsonData := `{
		"properties": [
			{"key": "api_key", "type": "string", "title": "API Key", "sensitive": true},
			{"key": "memory_limit", "type": "number", "title": "Memory Limit", "default": 256},
			{"key": "sandbox_mount_mode", "type": "string", "title": "Mount Mode", "default": "rw"}
		]
	}`

	var schema ConfigSchema
	if err := json.Unmarshal([]byte(jsonData), &schema); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if len(schema.Properties) != 3 {
		t.Fatalf("Properties count = %d, want 3", len(schema.Properties))
	}

	expectedOrder := []string{"api_key", "memory_limit", "sandbox_mount_mode"}
	for i, want := range expectedOrder {
		if schema.Properties[i].Key != want {
			t.Errorf("Properties[%d].Key = %q, want %q", i, schema.Properties[i].Key, want)
		}
	}

	// Verify GetProperty works
	prop := schema.GetProperty("memory_limit")
	if prop == nil {
		t.Fatal("GetProperty(memory_limit) returned nil")
	}
	if prop.Type != "number" {
		t.Errorf("memory_limit.Type = %v, want number", prop.Type)
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

func TestAgentConfig_ResolveSandboxTemplates(t *testing.T) {
	tests := []struct {
		name       string
		agent      *AgentConfig
		wantMounts []types.SandboxMount
		wantEnvs   []types.SandboxEnv
		wantNil    bool
	}{
		{
			name: "resolve all templates from config_schema array defaults",
			agent: &AgentConfig{
				ConfigSchema: &ConfigSchema{
					Properties: []SchemaProp{
						{Key: "sandbox_mount", Default: []any{map[string]any{"host_path": "~/.pi/agent", "container_path": "/root/.pi/agent", "mode": "rw"}}},
						{Key: "sandbox_env", Default: []any{map[string]any{"key": "HOME", "value": "~/.pi/agent"}}},
					},
				},
				Sandbox: &types.AgentSandbox{
					Mounts: []types.SandboxMount{
						{HostPath: "{{sandbox_mount_host_path[]}}", ContainerPath: "{{sandbox_mount_container_path[]}}", Mode: "{{sandbox_mount_mode[]}}"},
					},
					Envs: []types.SandboxEnv{{Key: "{{sandbox_env_key[]}}", Value: "{{sandbox_env_value[]}}"}},
				},
			},
			wantMounts: []types.SandboxMount{{HostPath: "~/.pi/agent", ContainerPath: "/root/.pi/agent", Mode: "rw"}},
			wantEnvs:   []types.SandboxEnv{{Key: "HOME", Value: "~/.pi/agent"}},
		},
		{
			name: "literal values pass through unchanged",
			agent: &AgentConfig{
				Sandbox: &types.AgentSandbox{
					Mounts: []types.SandboxMount{{HostPath: "/custom/path", ContainerPath: "/custom/container", Mode: "ro"}},
					Envs:   []types.SandboxEnv{{Key: "MY_ENV_VAR", Value: "/custom/path"}},
				},
			},
			wantMounts: []types.SandboxMount{{HostPath: "/custom/path", ContainerPath: "/custom/container", Mode: "ro"}},
			wantEnvs:   []types.SandboxEnv{{Key: "MY_ENV_VAR", Value: "/custom/path"}},
		},
		{
			name: "no config_schema - templates pass through unchanged",
			agent: &AgentConfig{
				Sandbox: &types.AgentSandbox{
					Mounts: []types.SandboxMount{{HostPath: "{{sandbox_mount_host_path[]}}", ContainerPath: "/x", Mode: "{{sandbox_mount_mode[]}}"}},
				},
			},
			wantMounts: []types.SandboxMount{{HostPath: "{{sandbox_mount_host_path[]}}", ContainerPath: "/x", Mode: "{{sandbox_mount_mode[]}}"}},
		},
		{
			name: "template for missing field passes through unchanged",
			agent: &AgentConfig{
				ConfigSchema: &ConfigSchema{
					Properties: []SchemaProp{
						{Key: "sandbox_mount", Default: []any{map[string]any{"host_path": "~/.pi/agent"}}},
					},
				},
				Sandbox: &types.AgentSandbox{
					Mounts: []types.SandboxMount{{HostPath: "{{sandbox_mount_host_path[]}}", ContainerPath: "{{sandbox_mount_container_path[]}}", Mode: "rw"}},
				},
			},
			wantMounts: []types.SandboxMount{{HostPath: "~/.pi/agent", ContainerPath: "{{sandbox_mount_container_path[]}}", Mode: "rw"}},
		},
		{
			name:    "nil agent config returns nil",
			agent:   nil,
			wantNil: true,
		},
		{
			name: "nil sandbox returns nil",
			agent: &AgentConfig{
				Sandbox: nil,
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
			sb := tt.agent.ResolveSandboxTemplates(schema)
			if tt.wantNil {
				if sb != nil {
					t.Errorf("expected nil sandbox, got %+v", sb)
				}
				return
			}
			if sb == nil {
				t.Fatal("expected non-nil sandbox")
			}
			if len(sb.Mounts) != len(tt.wantMounts) {
				t.Fatalf("Mounts count = %d, want %d", len(sb.Mounts), len(tt.wantMounts))
			}
			for i, want := range tt.wantMounts {
				if sb.Mounts[i].HostPath != want.HostPath {
					t.Errorf("Mounts[%d].HostPath = %q, want %q", i, sb.Mounts[i].HostPath, want.HostPath)
				}
				if sb.Mounts[i].ContainerPath != want.ContainerPath {
					t.Errorf("Mounts[%d].ContainerPath = %q, want %q", i, sb.Mounts[i].ContainerPath, want.ContainerPath)
				}
				if sb.Mounts[i].Mode != want.Mode {
					t.Errorf("Mounts[%d].Mode = %q, want %q", i, sb.Mounts[i].Mode, want.Mode)
				}
			}
			if len(sb.Envs) != len(tt.wantEnvs) {
				t.Fatalf("Envs count = %d, want %d", len(sb.Envs), len(tt.wantEnvs))
			}
			for i, want := range tt.wantEnvs {
				if sb.Envs[i].Key != want.Key {
					t.Errorf("Envs[%d].Key = %q, want %q", i, sb.Envs[i].Key, want.Key)
				}
				if sb.Envs[i].Value != want.Value {
					t.Errorf("Envs[%d].Value = %q, want %q", i, sb.Envs[i].Value, want.Value)
				}
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
		Version:     "1.0.0",
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
	jsonStr := `{"display_name":"Test","description":"","author":"","version":"1.0.0"}`
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
	cssExt.Manifest.Capabilities = &Capabilities{CSS: &CSSConfig{CSSURL: "/theme.css"}}
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
	uiExt.Manifest.Capabilities = &Capabilities{CSS: &CSSConfig{CSSURL: "/theme.css"}}
	if uiExt.HasCSSConfig() {
		t.Error("HasCSSConfig() = true for UI extension, want false")
	}
}

// TestPiManifestTemplateResolution loads the actual Pi agent manifest and verifies
// that {{property}} template references in sandbox are resolved using
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

	agent := manifest.Capabilities.GetAgent()
	if agent == nil {
		t.Fatal("manifest.Capabilities.Agent is nil")
	}
	if agent.Sandbox == nil {
		t.Fatal("manifest.Capabilities.Agent.Sandbox is nil")
	}

	// Verify template references are present in the raw manifest
	if len(agent.Sandbox.Mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(agent.Sandbox.Mounts))
	}
	if agent.Sandbox.Mounts[0].HostPath != "{{sandbox_mount_host_path[]}}" {
		t.Errorf("expected template host_path, got %q", agent.Sandbox.Mounts[0].HostPath)
	}
	if agent.Sandbox.Mounts[0].ContainerPath != "{{sandbox_mount_container_path[]}}" {
		t.Errorf("expected template container_path, got %q", agent.Sandbox.Mounts[0].ContainerPath)
	}
	if agent.Sandbox.Mounts[0].Mode != "{{sandbox_mount_mode[]}}" {
		t.Errorf("expected template mode, got %q", agent.Sandbox.Mounts[0].Mode)
	}
	if len(agent.Sandbox.Envs) != 1 || agent.Sandbox.Envs[0].Key != "{{sandbox_env_key[]}}" {
		t.Errorf("expected template env key, got %+v", agent.Sandbox.Envs)
	}

	// Resolve templates and verify against config_schema defaults
	sb := agent.ResolveSandboxTemplates(manifest.ConfigSchema)
	if sb == nil {
		t.Fatal("resolved sandbox is nil")
	}
	if len(sb.Mounts) != 1 {
		t.Fatalf("resolved mount count = %d, want 1", len(sb.Mounts))
	}
	if sb.Mounts[0].HostPath != "~/.pi/agent" {
		t.Errorf("resolved HostPath = %q, want ~/.pi/agent", sb.Mounts[0].HostPath)
	}
	if sb.Mounts[0].ContainerPath != "/root/.pi/agent" {
		t.Errorf("resolved ContainerPath = %q, want /root/.pi/agent", sb.Mounts[0].ContainerPath)
	}
	if sb.Mounts[0].Mode != "rw" {
		t.Errorf("resolved Mode = %q, want rw", sb.Mounts[0].Mode)
	}
	if len(sb.Envs) != 1 || sb.Envs[0].Key != "HOME" {
		t.Errorf("resolved Envs = %+v, want key HOME", sb.Envs)
	}
}

func TestManifest_SandboxConfigUnmarshal(t *testing.T) {
	raw := `{
		"display_name": "Go Language Server",
		"version": "1.0.0",
		"capabilities": {
			"lsp": {
				"command": "gopls", "args": ["serve"], "language": "go",
				"sandbox": {
					"envs": [
						{"key": "XDG_CACHE_HOME", "value": "/var/cache/gopls"},
						{"key": "GOCACHE", "value": "/var/cache/gopls/go-build"}
					],
					"mounts": [
						{"host_path": "/tmp/cache", "container_path": "/var/cache/gopls", "mode": "rw"}
					],
					"memory_limit": 512
				}
			}
		}
	}`
	var m ExtensionManifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	sb := m.Capabilities.LSP.Sandbox
	if sb == nil {
		t.Fatal("capabilities.lsp.sandbox was not parsed")
	}
	if len(sb.Envs) != 2 || sb.Envs[0].Key != "XDG_CACHE_HOME" {
		t.Errorf("unexpected envs: %+v", sb.Envs)
	}
	if len(sb.Mounts) != 1 || sb.Mounts[0].Mode != "rw" {
		t.Errorf("unexpected mounts: %+v", sb.Mounts)
	}
	if sb.MemoryLimit != 512 {
		t.Errorf("memory_limit = %d, want 512", sb.MemoryLimit)
	}
	if got := lspSandboxFromManifest(&m); got == nil {
		t.Error("lspSandboxFromManifest returned nil")
	}
	if got := lspSandboxFromManifest(nil); got != nil {
		t.Error("lspSandboxFromManifest(nil) should return nil")
	}
}

func TestSandboxConfigFromManifest(t *testing.T) {
	// Nil manifest config -> defaults.
	def := sandboxConfigFromManifest(nil)
	if !def.NetworkIsolation {
		t.Error("default NetworkIsolation should be true")
	}
	if def.MemoryLimit != DefaultExtensionMemory {
		t.Errorf("default MemoryLimit = %d, want %d", def.MemoryLimit, DefaultExtensionMemory)
	}

	// Manifest envs/mounts are carried over; defaults preserved.
	sc := sandboxConfigFromManifest(&SandboxConfig{
		MemoryLimit: 512,
		Envs:        []types.SandboxEnv{{Key: "XDG_CACHE_HOME", Value: "/var/cache/gopls"}},
		Mounts:      []types.SandboxMount{{HostPath: "/tmp/cache", ContainerPath: "/var/cache/gopls", Mode: "rw"}},
	})
	if !sc.NetworkIsolation {
		t.Error("NetworkIsolation should remain true")
	}
	if sc.MemoryLimit != 512 {
		t.Errorf("MemoryLimit = %d, want 512", sc.MemoryLimit)
	}
	if len(sc.Envs) != 1 || sc.Envs[0].Key != "XDG_CACHE_HOME" {
		t.Errorf("envs not carried over: %+v", sc.Envs)
	}
	if len(sc.Mounts) != 1 || sc.Mounts[0].Mode != "rw" {
		t.Errorf("mounts not carried over: %+v", sc.Mounts)
	}
}
