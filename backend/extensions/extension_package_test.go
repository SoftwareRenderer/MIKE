package extensions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// findExtensionsDir locates the extensions/ directory relative to the repo root.
// Tests run from the module root, so we walk up from the package dir.
func findExtensionsDir(t *testing.T) string {
	t.Helper()
	// Package is at backend/extensions/, repo root is ../../..  from here
	candidates := []string{
		"../../extensions",                         // from backend/extensions/
		"../../../../extensions",                   // deeper nesting
		filepath.Join(os.Getenv("MIKE_EXTENSIONS"), ""), // env override
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	t.Skip("extensions/ directory not found (skip if running outside repo root)")
	return ""
}

func TestExtensionPackage_TypescriptLanguageServer_Manifest(t *testing.T) {
	extDir := findExtensionsDir(t)
	manifestPath := filepath.Join(extDir, "typescript-language-server", "manifest.json")

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
	if manifest.LSP == nil {
		t.Fatal("LSP config is nil")
	}

	lsp := manifest.LSP
	if lsp.Command == "" {
		t.Error("LSP.Command is empty")
	}
	if lsp.Language == "" {
		t.Error("LSP.Language is empty")
	}
	if len(lsp.Extensions) == 0 {
		t.Error("LSP.Extensions is empty")
	}

	// Verify expected file extensions
	expectedExts := []string{"ts", "tsx", "js", "jsx"}
	extSet := make(map[string]bool)
	for _, e := range lsp.Extensions {
		extSet[e] = true
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
	dockerfilePath := filepath.Join(extDir, "typescript-language-server", "Dockerfile")

	data, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatalf("ReadFile(Dockerfile): %v", err)
	}

	content := string(data)

	// Must reference sandbox-bridge
	if !containsAll(content, "sandbox-bridge") {
		t.Error("Dockerfile does not reference sandbox-bridge")
	}

	// Must have a CMD or ENTRYPOINT
	if !containsAny(content, "CMD", "ENTRYPOINT") {
		t.Error("Dockerfile has no CMD or ENTRYPOINT")
	}

	// Must install typescript-language-server
	if !containsAll(content, "typescript-language-server") {
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
	if found.DockerfileURL == "" {
		t.Error("DockerfileURL is empty")
	}
	if found.ManifestURL == "" {
		t.Error("ManifestURL is empty")
	}

	// Verify paths reference the correct subdirectory
	if found.DockerfileURL != "typescript-language-server/Dockerfile" {
		t.Errorf("DockerfileURL = %q, want %q", found.DockerfileURL, "typescript-language-server/Dockerfile")
	}
	if found.ManifestURL != "typescript-language-server/manifest.json" {
		t.Errorf("ManifestURL = %q, want %q", found.ManifestURL, "typescript-language-server/manifest.json")
	}
}

func TestExtensionPackage_TypescriptLanguageServer_ResolveURLs(t *testing.T) {
	extDir := findExtensionsDir(t)
	registryPath := filepath.Join(extDir, "registry.json")

	// Resolve relative URLs against the registry file
	baseURL := "file://" + registryPath

	manifestURL := ResolveURL(baseURL, "typescript-language-server/manifest.json")
	dockerfileURL := ResolveURL(baseURL, "typescript-language-server/Dockerfile")

	// Verify resolved paths exist
	manifestPath := manifestURL[len("file://"):]
	if _, err := os.Stat(manifestPath); err != nil {
		t.Errorf("Resolved manifest path %q does not exist: %v", manifestPath, err)
	}

	dockerfilePath := dockerfileURL[len("file://"):]
	if _, err := os.Stat(dockerfilePath); err != nil {
		t.Errorf("Resolved Dockerfile path %q does not exist: %v", dockerfilePath, err)
	}
}

func containsAll(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if !containsAny(s, sub) {
			return false
		}
	}
	return true
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if stringsContains(s, sub) {
			return true
		}
	}
	return false
}

func stringsContains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
