package extensions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// findExtensionsDir locates the published-extensions repo (../MIKE-extensions)
// relative to the repo root. Tests run from the module root, so we walk up from
// the package dir. An env override (MIKE_EXTENSIONS) takes precedence.
func findExtensionsDir(t *testing.T) string {
	t.Helper()
	// Package is at backend/extensions/; the sibling repo is ../../../MIKE-extensions
	candidates := []string{
		filepath.Join(os.Getenv("MIKE_EXTENSIONS"), ""), // env override
		"../../../MIKE-extensions",                      // sibling repo from backend/extensions/
	}
	for _, c := range candidates {
		if c != "" {
			if _, err := os.Stat(c); err == nil {
				return c
			}
		}
	}
	t.Skip("MIKE-extensions directory not found (skip if running outside repo root)")
	return ""
}

func TestExtensionPackage_TypescriptLanguageServer_Manifest(t *testing.T) {
	extDir := findExtensionsDir(t)
	manifestPath := filepath.Join(extDir, "lsp", "typescript-language-server", "manifest.json")

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("ReadFile(manifest.json): %v", err)
	}

	var manifest ExtensionManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("Unmarshal(manifest.json): %v", err)
	}

	// Required fields
	if manifest.DisplayName == "" {
		t.Error("DisplayName is empty")
	}
	if manifest.Description == "" {
		t.Error("Description is empty")
	}
	if manifest.Author == "" {
		t.Error("Author is empty")
	}

	// LSP config must be present
	if manifest.Capabilities.GetLSP() == nil {
		t.Fatal("LSP config is nil")
	}

	lsp := manifest.Capabilities.GetLSP()
	if lsp.Command == "" {
		t.Error("LSP.Command is empty")
	}
	if lsp.Language == "" {
		t.Error("LSP.Language is empty")
	}
	if len(lsp.FileTypes) == 0 {
		t.Error("LSP.FileTypes is empty")
	}

	// Verify expected file extensions, and that each declares the languageId its server needs
	expectedExts := []string{"ts", "tsx", "js", "jsx"}
	extSet := make(map[string]bool)
	for _, ft := range lsp.FileTypes {
		extSet[ft.Extension] = true
		if ft.LanguageID == "" {
			t.Errorf("LSP.FileTypes[%q] declares no language_id", ft.Extension)
		}
	}
	for _, want := range expectedExts {
		if !extSet[want] {
			t.Errorf("LSP.Extensions missing %q", want)
		}
	}

	// Config schema should have properties
	if manifest.ConfigSchema == nil {
		t.Error("ConfigSchema is nil")
	} else if len(manifest.ConfigSchema.Properties) == 0 {
		t.Error("ConfigSchema.Properties is empty")
	}
}

func TestExtensionPackage_TypescriptLanguageServer_Dockerfile(t *testing.T) {
	extDir := findExtensionsDir(t)
	dockerfilePath := filepath.Join(extDir, "lsp", "typescript-language-server", "Dockerfile")

	data, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatalf("ReadFile(Dockerfile): %v", err)
	}

	content := string(data)

	// Must reference sandbox-bridge
	if !strings.Contains(content, "sandbox-bridge") {
		t.Error("Dockerfile does not reference sandbox-bridge")
	}

	// Must have a CMD or ENTRYPOINT
	if !slices.ContainsFunc([]string{"CMD", "ENTRYPOINT"}, func(kw string) bool {
		return strings.Contains(content, kw)
	}) {
		t.Error("Dockerfile has no CMD or ENTRYPOINT")
	}

	// Must install typescript-language-server
	if !strings.Contains(content, "typescript-language-server") {
		t.Error("Dockerfile does not install typescript-language-server")
	}
}

func TestExtensionPackage_TypescriptLanguageServer_RegistryEntry(t *testing.T) {
	extDir := findExtensionsDir(t)
	registryPath := filepath.Join(extDir, "registry.json")

	data, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("ReadFile(registry.json): %v", err)
	}

	var index RegistryIndex
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatalf("Unmarshal(registry.json): %v", err)
	}

	// Find the typescript-language-server entry
	var found *RegistryEntry
	for i, entry := range index {
		if entry.Name == "typescript-language-server" {
			found = &index[i]
			break
		}
	}

	if found == nil {
		t.Fatal("registry.json does not contain typescript-language-server entry")
	}

	// Validate required fields
	if found.Type != ExtensionTypeLSP {
		t.Errorf("Type = %q, want %q", found.Type, ExtensionTypeLSP)
	}
	if found.DisplayName == "" {
		t.Error("DisplayName is empty")
	}

	// Git-pinned source fields must be present and valid.
	if found.Author == "" {
		t.Error("Author is empty")
	}
	if found.Repo == "" {
		t.Error("Repo is empty")
	}
	if !commitHashRe.MatchString(found.Commit) {
		t.Errorf("Commit %q is not a valid SHA-1/SHA-256 hex hash", found.Commit)
	}

	// Verify the subdirectory references the correct repo path.
	if found.Subdir != "lsp/typescript-language-server" {
		t.Errorf("Subdir = %q, want %q", found.Subdir, "lsp/typescript-language-server")
	}
}
