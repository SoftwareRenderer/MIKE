package lsp

import (
	"testing"
)

func TestLookupGo(t *testing.T) {
	r := NewRegistry()
	cfg := r.Lookup("main.go")
	if cfg == nil {
		t.Fatal("expected gopls config for .go file")
	}
	if cfg.Command != "gopls" {
		t.Errorf("expected command gopls, got %s", cfg.Command)
	}
}

func TestLookupTypeScript(t *testing.T) {
	r := NewRegistry()
	cfg := r.Lookup("app.ts")
	if cfg == nil {
		t.Fatal("expected typescript-language-server config for .ts file")
	}
	if cfg.Command != "npx" {
		t.Errorf("expected command npx, got %s", cfg.Command)
	}
}

func TestLookupUnknown(t *testing.T) {
	r := NewRegistry()
	cfg := r.Lookup("file.unknown")
	if cfg != nil {
		t.Errorf("expected nil for unknown extension, got %+v", cfg)
	}
}

func TestAllPreRegisteredExtensions(t *testing.T) {
	r := NewRegistry()

	tests := []struct {
		path     string
		command  string
		language string
	}{
		// Go
		{"file.go", "gopls", "go"},

		// TypeScript family
		{"file.ts", "npx", "typescript"},
		{"file.tsx", "npx", "typescript"},
		{"file.js", "npx", "typescript"},
		{"file.jsx", "npx", "typescript"},
		{"file.mjs", "npx", "typescript"},
		{"file.cjs", "npx", "typescript"},

		// Python
		{"file.py", "pyright-langserver", "python"},
		{"file.pyi", "pyright-langserver", "python"},

		// Rust
		{"file.rs", "rust-analyzer", "rust"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			cfg := r.Lookup(tt.path)
			if cfg == nil {
				t.Fatalf("expected config for %s, got nil", tt.path)
			}
			if cfg.Command != tt.command {
				t.Errorf("path %s: expected command %s, got %s", tt.path, tt.command, cfg.Command)
			}
			if cfg.Language != tt.language {
				t.Errorf("path %s: expected language %s, got %s", tt.path, tt.language, cfg.Language)
			}
		})
	}
}

func TestRegisterRuntime(t *testing.T) {
	r := NewRegistry()
	r.Register(&ServerConfig{
		Command:    "custom-ls",
		Args:       []string{"--mode", "stdio"},
		Extensions: []string{"xyz"},
	})

	cfg := r.Lookup("file.xyz")
	if cfg == nil {
		t.Fatal("expected custom config for .xyz file")
	}
	if cfg.Command != "custom-ls" {
		t.Errorf("expected command custom-ls, got %s", cfg.Command)
	}
}

func TestLookupNoExtension(t *testing.T) {
	r := NewRegistry()
	cfg := r.Lookup("Makefile")
	if cfg != nil {
		t.Errorf("expected nil for file with no extension, got %+v", cfg)
	}
}

func TestLookupDeepPath(t *testing.T) {
	r := NewRegistry()
	cfg := r.Lookup("/some/deep/path/to/module/src/component.tsx")
	if cfg == nil {
		t.Fatal("expected config for deep .tsx path")
	}
	if cfg.Command != "npx" {
		t.Errorf("expected command npx, got %s", cfg.Command)
	}
}

func TestGoConfigHasInitParamsOverride(t *testing.T) {
	r := NewRegistry()
	cfg := r.Lookup("main.go")
	if cfg == nil {
		t.Fatal("expected gopls config for .go file")
	}
	if cfg.Command != "gopls" {
		t.Fatalf("expected command gopls, got %s", cfg.Command)
	}
	if cfg.InitParamsOverride == nil {
		t.Fatal("expected InitParamsOverride to be set for gopls")
	}

	// Check hover.actions.enabled
	hover, ok := cfg.InitParamsOverride["hover"].(map[string]any)
	if !ok {
		t.Fatal("expected hover to be a map")
	}
	actions, ok := hover["actions"].(map[string]any)
	if !ok {
		t.Fatal("expected hover.actions to be a map")
	}
	if actions["enabled"] != true {
		t.Errorf("expected hover.actions.enabled = true, got %v", actions["enabled"])
	}

	// Check codeLens.disable contains "generated_test"
	codeLens, ok := cfg.InitParamsOverride["codeLens"].(map[string]any)
	if !ok {
		t.Fatal("expected codeLens to be a map")
	}
	disable, ok := codeLens["disable"].([]string)
	if !ok {
		t.Fatalf("expected codeLens.disable to be []string, got %T", codeLens["disable"])
	}
	if len(disable) != 1 || disable[0] != "generated_test" {
		t.Errorf("expected codeLens.disable = [\"generated_test\"], got %v", disable)
	}

	// Check hints fields
	hints, ok := cfg.InitParamsOverride["hints"].(map[string]any)
	if !ok {
		t.Fatal("expected hints to be a map")
	}
	boolHints := []string{"assignVariableType", "compositeLiteralFields", "constantValues", "functionTypeParameters", "parameterNames"}
	for _, key := range boolHints {
		if hints[key] != true {
			t.Errorf("expected hints.%s = true, got %v", key, hints[key])
		}
	}

	// Check usePlaceholders
	if cfg.InitParamsOverride["usePlaceholders"] != true {
		t.Errorf("expected usePlaceholders = true, got %v", cfg.InitParamsOverride["usePlaceholders"])
	}

	// Check staticcheck
	if cfg.InitParamsOverride["staticcheck"] != true {
		t.Errorf("expected staticcheck = true, got %v", cfg.InitParamsOverride["staticcheck"])
	}
}

func TestNonGoConfigHasNoInitParamsOverride(t *testing.T) {
	r := NewRegistry()
	cfg := r.Lookup("app.ts")
	if cfg == nil {
		t.Fatal("expected typescript config for .ts file")
	}
	if cfg.InitParamsOverride != nil {
		t.Errorf("expected no InitParamsOverride for typescript, got %+v", cfg.InitParamsOverride)
	}
}
