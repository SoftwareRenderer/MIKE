package lsp

import (
	"path/filepath"
	"strings"
)

// ServerConfig holds the configuration for a single language server.
type ServerConfig struct {
	Command            string         // binary to execute (e.g., "gopls")
	Args               []string       // args template, {workspace} replaced with workspace path
	Extensions         []string       // file extensions (without dot), e.g., []string{"go"}
	Language           string         // language identifier, e.g., "go", "typescript"
	InitParamsOverride map[string]any // merged into initialize params sent to server
}

// Registry maps file extensions to language server configurations.
type Registry struct {
	servers map[string]*ServerConfig // extension → config
}

// NewRegistry creates a Registry pre-populated with default language server configs.
func NewRegistry() *Registry {
	r := &Registry{
		servers: make(map[string]*ServerConfig),
	}

	defaults := []*ServerConfig{
		{
			Command:    "gopls",
			Args:       []string{"serve"},
			Extensions: []string{"go"},
			Language:   "go",
			InitParamsOverride: map[string]any{
				"hover": map[string]any{
					"actions": map[string]any{"enabled": true},
				},
				"codeLens": map[string]any{
					"disable": []string{"generated_test"},
				},
				"hints": map[string]any{
					"assignVariableType":     true,
					"compositeLiteralFields": true,
					"constantValues":         true,
					"functionTypeParameters": true,
					"parameterNames":         true,
				},
				"usePlaceholders": true,
				"staticcheck":     true,
			},
		},
		{
			Command:    "npx",
			Args:       []string{"-y", "typescript-language-server", "--stdio"},
			Extensions: []string{"ts", "tsx", "js", "jsx", "mjs", "cjs"},
			Language:   "typescript",
		},
		{
			Command:    "pyright-langserver",
			Args:       []string{"--stdio"},
			Extensions: []string{"py", "pyi"},
			Language:   "python",
		},
		{
			Command:    "rust-analyzer",
			Args:       []string{},
			Extensions: []string{"rs"},
			Language:   "rust",
		},
	}

	for _, cfg := range defaults {
		r.Register(cfg)
	}

	return r
}

// Lookup returns the ServerConfig for the given file path, or nil if no match.
func (r *Registry) Lookup(path string) *ServerConfig {
	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	if ext == "" {
		return nil
	}
	return r.servers[ext]
}

// Register adds a server config, mapping each of its extensions to the config.
func (r *Registry) Register(cfg *ServerConfig) {
	for _, ext := range cfg.Extensions {
		r.servers[ext] = cfg
	}
}
