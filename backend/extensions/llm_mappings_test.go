package extensions

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func makeLLMExt(id, author, name string, enabled bool, mappingsFile string) *Extension {
	now := time.Now().UTC().Format(time.RFC3339)
	manifest := ExtensionManifest{DisplayName: name, Version: "1.0.0"}
	manifest.Capabilities = &Capabilities{LLM: &LLMConfig{MappingsFile: mappingsFile}}
	return &Extension{
		ID:            id,
		Name:          name,
		Author:        author,
		Version:       "1.0.0",
		Type:          ExtensionTypeLLM,
		Manifest:      manifest,
		Config:        map[string]any{},
		InstallSource: "local",
		Enabled:       enabled,
		InstalledAt:   now,
	}
}

func writeMappings(t *testing.T, extDir, author, name, file, content string) {
	t.Helper()
	dir := filepath.Join(extDir, author, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatalf("write mappings: %v", err)
	}
}

func TestInferExtensionTypeLLM(t *testing.T) {
	if got := inferExtensionType(&Capabilities{LLM: &LLMConfig{}}); got != ExtensionTypeLLM {
		t.Errorf("inferExtensionType(llm) = %q, want %q", got, ExtensionTypeLLM)
	}
	// Priority: lsp wins over llm when both are present.
	if got := inferExtensionType(&Capabilities{LSP: &LSPConfig{}, LLM: &LLMConfig{}}); got != ExtensionTypeLSP {
		t.Errorf("inferExtensionType(lsp+llm) = %q, want %q", got, ExtensionTypeLSP)
	}
}

func TestIsLLMHelpers(t *testing.T) {
	ext := makeLLMExt("id", "local", "name", true, "")
	if !ext.IsLLM() || !ext.HasLLMConfig() {
		t.Error("IsLLM/HasLLMConfig should be true")
	}
	ext.Type = ExtensionTypeUI
	if ext.IsLLM() || ext.HasLLMConfig() {
		t.Error("non-llm type must not report IsLLM/HasLLMConfig")
	}
}

func TestCollectLLMMappings(t *testing.T) {
	es := testStore(t)
	extDir := t.TempDir()

	writeMappings(t, extDir, "local", "alpha", "mappings.json", `[{"match":"https://a.example"},{"match":"https://b.example"}]`)
	writeMappings(t, extDir, "local", "beta", "custom.json", `[{"match":"https://c.example"}]`)
	writeMappings(t, extDir, "local", "gamma", "mappings.json", `{"not":"an array"}`)
	writeMappings(t, extDir, "local", "epsilon", "mappings.json", `[{"match":"https://e.example"}]`)
	// flata: flat layout (extensions/<name>) for author "local".
	writeMappings(t, extDir, "flata", "", "mappings.json", `[{"match":"https://f.example"}]`)

	if err := es.Create(makeLLMExt("id-alpha", "local", "alpha", true, "")); err != nil {
		t.Fatal(err)
	}
	if err := es.Create(makeLLMExt("id-beta", "local", "beta", true, "custom.json")); err != nil {
		t.Fatal(err)
	}
	if err := es.Create(makeLLMExt("id-gamma", "local", "gamma", true, "")); err != nil {
		t.Fatal(err)
	}
	// delta: enabled but its mappings file is missing.
	if err := es.Create(makeLLMExt("id-delta", "local", "delta", true, "")); err != nil {
		t.Fatal(err)
	}
	if err := es.Create(makeLLMExt("id-flata", "local", "flata", true, "")); err != nil {
		t.Fatal(err)
	}
	// epsilon: disabled — must be excluded.
	if err := es.Create(makeLLMExt("id-epsilon", "local", "epsilon", false, "")); err != nil {
		t.Fatal(err)
	}

	ms, err := CollectLLMMappings(es, extDir)
	if err != nil {
		t.Fatalf("CollectLLMMappings: %v", err)
	}
	want := []string{"https://a.example", "https://b.example", "https://c.example", "https://f.example"}
	if len(ms) != len(want) {
		t.Fatalf("got %d mappings, want %d: %+v", len(ms), len(want), ms)
	}
	for i, w := range want {
		if ms[i].Match != w {
			t.Errorf("mapping[%d].Match = %q, want %q", i, ms[i].Match, w)
		}
	}
}

func TestCollectLLMMappingsNoneInstalled(t *testing.T) {
	es := testStore(t)
	ms, err := CollectLLMMappings(es, t.TempDir())
	if err != nil {
		t.Fatalf("CollectLLMMappings: %v", err)
	}
	if len(ms) != 0 {
		t.Errorf("got %d mappings, want 0", len(ms))
	}
}

func TestMappingsFilePath(t *testing.T) {
	dir := "/tmp/exts"
	cases := []struct {
		author, name, file string
		want               string
		ok                 bool
	}{
		{"local", "provider-adapters", "mappings.json", filepath.Join(dir, "local", "provider-adapters", "mappings.json"), true},
		{"local", "provider-adapters", "custom.json", filepath.Join(dir, "local", "provider-adapters", "custom.json"), true},
		{"local", "provider-adapters", "/etc/passwd", "", false},
		{"local", "provider-adapters", "../evil.json", "", false},
		{"local", "provider-adapters", "a/../../evil.json", "", false},
		{"local", "provider-adapters", "", "", false},
	}
	for _, c := range cases {
		got, ok := mappingsFilePath(dir, c.author, c.name, c.file)
		if ok != c.ok || got != c.want {
			t.Errorf("mappingsFilePath(%q,%q,%q) = (%q,%v), want (%q,%v)", c.author, c.name, c.file, got, ok, c.want, c.ok)
		}
	}
}

// TestMappingsFilePathFlatLayout verifies author "local" resolves to the flat
// layout (extensions/<name>) when only that directory exists, and to the
// namespaced layout when that one exists.
func TestMappingsFilePathFlatLayout(t *testing.T) {
	root := t.TempDir()
	// Only flat exists.
	if err := os.MkdirAll(filepath.Join(root, "myext"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := mappingsFilePath(root, "local", "myext", "mappings.json")
	if !ok || got != filepath.Join(root, "myext", "mappings.json") {
		t.Errorf("flat: got (%q,%v)", got, ok)
	}
	// Both exist → namespaced wins.
	if err := os.MkdirAll(filepath.Join(root, "local", "myext"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok = mappingsFilePath(root, "local", "myext", "mappings.json")
	if !ok || got != filepath.Join(root, "local", "myext", "mappings.json") {
		t.Errorf("namespaced: got (%q,%v)", got, ok)
	}
	// Neither exists → standard (namespaced) path reported.
	got, ok = mappingsFilePath(root, "local", "ghost", "mappings.json")
	if !ok || got != filepath.Join(root, "local", "ghost", "mappings.json") {
		t.Errorf("missing: got (%q,%v)", got, ok)
	}
}
