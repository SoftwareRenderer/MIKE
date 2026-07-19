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
