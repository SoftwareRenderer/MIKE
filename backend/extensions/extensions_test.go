package extensions

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mike/backend/types"
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

// TestInferExtensionType pins the whole priority chain, including where the translation pack
// sits: last, because a pack is the only capability that is pure data. A manifest that declares a
// pack beside anything else never reaches an installed row (validateI18N refuses it), so the mixed
// cases here are what a rejected manifest is called in the log — and they still have an answer.
func TestInferExtensionType(t *testing.T) {
	pack := func() *I18NConfig { return &I18NConfig{Locale: "ja", MessagesURL: "messages.json"} }

	tests := []struct {
		name string
		caps *Capabilities
		want string
	}{
		{"no capabilities at all", nil, ""},
		{"capabilities that declare nothing", &Capabilities{}, ""},
		{"lsp", &Capabilities{LSP: &LSPConfig{Language: "go"}}, ExtensionTypeLSP},
		{"ui", &Capabilities{UI: &UIConfig{InjectionPoints: []string{"taskCard"}}}, ExtensionTypeUI},
		{"css", &Capabilities{CSS: &CSSConfig{CSSURL: "theme.css"}}, ExtensionTypeCSS},
		{"agent", &Capabilities{Agent: &AgentConfig{WasmModule: "adapter.wasm"}}, ExtensionTypeAgent},
		{"llm", &Capabilities{LLM: &LLMConfig{}}, ExtensionTypeLLM},
		{"a translation pack on its own", &Capabilities{I18N: pack()}, ExtensionTypeI18N},
		// A pack plus a sibling: refused on install, but the chain still resolves to the sibling.
		{"a pack beside ui yields the capability that runs", &Capabilities{I18N: pack(), UI: &UIConfig{}}, ExtensionTypeUI},
		{"a pack beside llm yields the capability that runs", &Capabilities{I18N: pack(), LLM: &LLMConfig{}}, ExtensionTypeLLM},
		{"lsp outranks every other declaration", &Capabilities{LSP: &LSPConfig{}, UI: &UIConfig{}, CSS: &CSSConfig{}, Agent: &AgentConfig{}, LLM: &LLMConfig{}, I18N: pack()}, ExtensionTypeLSP},
		{"network is a modifier, not a type", &Capabilities{Network: &NetworkConfig{AllowedDomains: []string{"api.example.com"}}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inferExtensionType(tt.caps); got != tt.want {
				t.Errorf("inferExtensionType() = %q, want %q", got, tt.want)
			}
		})
	}

	// The constant and the directory name in the registry source are the same string: a published
	// pack lives at <type>/<name>, and gen-registry derives the entry's type from that folder.
	if ExtensionTypeI18N != "i18n" {
		t.Errorf("ExtensionTypeI18N = %q, want \"i18n\" — the registry's folder name", ExtensionTypeI18N)
	}

	// A refused manifest is still a declaration: the install is refused, but the pack half of it
	// has to stay readable, because refusing an install is not the same as losing the text.
	mixed := &Capabilities{LSP: &LSPConfig{}, I18N: &I18NConfig{Locale: "fr", MessagesURL: "fr.json"}}
	if mixed.GetI18N() == nil {
		t.Error("an i18n declaration beside another capability must still be readable as a pack")
	}
}

func TestExtension_InstallSourceChecks(t *testing.T) {
	tests := []struct {
		source    string
		wantLocal bool
	}{
		{"registry", false},
		{"local", true},
		{"unknown", false},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			ext := makeTestExtension(t, ExtensionTypeLSP, tt.source)
			if got := ext.IsLocalInstalled(); got != tt.wantLocal {
				t.Errorf("IsLocalInstalled() = %v, want %v", got, tt.wantLocal)
			}
		})
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

func TestLSPConfig_ChatDiagnosticsEnabled(t *testing.T) {
	// The post-edit pass is opt-in, so the absence of the field is the off state — the
	// opposite of the gate above, and the reason a fresh install diagnoses nothing.
	cfg := &LSPConfig{Command: "gopls", Args: []string{"serve"}}
	if cfg.IsChatDiagnosticsEnabled() {
		t.Error("IsChatDiagnosticsEnabled() = true, want false (nil defaults to disabled)")
	}

	trueVal := true
	cfg.ChatDiagnosticsEnabled = &trueVal
	if !cfg.IsChatDiagnosticsEnabled() {
		t.Error("IsChatDiagnosticsEnabled() = false, want true (explicitly enabled)")
	}

	falseVal := false
	cfg.ChatDiagnosticsEnabled = &falseVal
	if cfg.IsChatDiagnosticsEnabled() {
		t.Error("IsChatDiagnosticsEnabled() = true, want false (explicitly disabled)")
	}

	var nilCfg *LSPConfig
	if nilCfg.IsChatDiagnosticsEnabled() {
		t.Error("nil.IsChatDiagnosticsEnabled() = true, want false")
	}
}

// `css_url` is the manifest key the browser reads to load a theme's stylesheet, so the spelling is
// the contract. A decode of the manifest text, not a round-trip through the same tags.
func TestCSSConfig_DecodesTheManifestKey(t *testing.T) {
	var m ExtensionManifest
	if err := json.Unmarshal([]byte(`{"display_name":"light","capabilities":{"css":{"css_url":"/extensions/mike-light/styles.css"}}}`), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := m.Capabilities.GetCSS(); got == nil || got.CSSURL != "/extensions/mike-light/styles.css" {
		t.Errorf("capabilities.css.css_url did not decode: %+v", m.Capabilities.GetCSS())
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

// `priority` is the manifest key that orders the installed list and the i18n pack choice, so the
// spelling is the contract: a pack the author ranked decodes to that rank or it loads beside them.
func TestManifest_Priority_DecodesFromTheManifestKey(t *testing.T) {
	var m ExtensionManifest
	if err := json.Unmarshal([]byte(`{"display_name":"Ranked","priority":42}`), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Priority != 42 {
		t.Errorf("Priority = %d, want 42 — the extension list and the pack choice sort on this field", m.Priority)
	}
}

func TestPiManifestTemplateResolution(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "..", "MIKE-extensions", "agent", "pi", "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read the shipped agent manifest: %v", err)
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
	// An extension sandbox is bounded by default, so the memory budget can see its growth before it
	// happens.
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
	// A negative limit is the manifest asking for no bound, and the default must not swallow it.
	negative := sandboxConfigFromManifest(&SandboxConfig{MemoryLimit: -1})
	if negative.MemoryLimit >= 0 {
		t.Errorf("MemoryLimit = %d, want the manifest's unlimited request preserved", negative.MemoryLimit)
	}
	if got := extensionMemoryLimit(&Extension{SandboxConfig: negative}); got != 0 {
		t.Errorf("extensionMemoryLimit = %d, want 0 (no write, no bound)", got)
	}
	if len(sc.Envs) != 1 || sc.Envs[0].Key != "XDG_CACHE_HOME" {
		t.Errorf("envs not carried over: %+v", sc.Envs)
	}
	if len(sc.Mounts) != 1 || sc.Mounts[0].Mode != "rw" {
		t.Errorf("mounts not carried over: %+v", sc.Mounts)
	}
}
