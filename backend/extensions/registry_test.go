package extensions

import (
	"encoding/json"
	"net/http"
	"os"
	"net/http/httptest"
	"testing"
)

func TestFetchIndex(t *testing.T) {
	index := RegistryIndex{
		{
			Name:        "gopls",
			Version:     "1.0.0",
			Type:        ExtensionTypeLSP,
			DisplayName: "Go Language Server",
			Description: "Official Go language server",
			Author:      "Go Team",
			Image:       "ghcr.io/kanban/gopls:1.0.0",
			ManifestURL: "https://registry.kanban.dev/extensions/gopls/1.0.0/manifest.json",
		},
		{
			Name:        "typescript",
			Version:     "2.0.0",
			Type:        ExtensionTypeLSP,
			DisplayName: "TypeScript Language Server",
			Description: "TypeScript language server",
			Author:      "Microsoft",
			Image:       "ghcr.io/kanban/typescript:2.0.0",
			ManifestURL: "https://registry.kanban.dev/extensions/typescript/2.0.0/manifest.json",
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(index)
	}))
	defer server.Close()

	rc := NewRegistryClient()
	got, err := rc.FetchIndex(server.URL)
	if err != nil {
		t.Fatalf("FetchIndex: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Index length = %v, want 2", len(got))
	}
	if got[0].Name != "gopls" {
		t.Errorf("First entry name = %v, want gopls", got[0].Name)
	}
	if got[1].Name != "typescript" {
		t.Errorf("Second entry name = %v, want typescript", got[1].Name)
	}
}

func TestFetchIndex_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	rc := NewRegistryClient()
	_, err := rc.FetchIndex(server.URL)
	if err == nil {
		t.Error("FetchIndex(HTTP 404) = nil, want error")
	}
}

func TestFetchIndex_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json"))
	}))
	defer server.Close()

	rc := NewRegistryClient()
	_, err := rc.FetchIndex(server.URL)
	if err == nil {
		t.Error("FetchIndex(invalid json) = nil, want error")
	}
}

func TestFetchManifest(t *testing.T) {
	manifest := ExtensionManifest{
		DisplayName: "Test Extension",
		Description: "A test extension",
		Author:      "Test Author",
		Homepage:    "https://example.com",
		LSP: &LSPConfig{
			Command:    "test-lsp",
			Args:       []string{"serve"},
			Extensions: []string{"test"},
			Language:   "test",
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(manifest)
	}))
	defer server.Close()

	rc := NewRegistryClient()
	got, err := rc.FetchManifest(server.URL)
	if err != nil {
		t.Fatalf("FetchManifest: %v", err)
	}
	if got.DisplayName != "Test Extension" {
		t.Errorf("DisplayName = %v, want Test Extension", got.DisplayName)
	}
	if got.LSP == nil {
		t.Fatal("LSP config is nil")
	}
	if got.LSP.Command != "test-lsp" {
		t.Errorf("LSP.Command = %v, want test-lsp", got.LSP.Command)
	}
}

func TestFetchDockerfile(t *testing.T) {
	expected := `FROM ubuntu:latest
RUN apt-get update && apt-get install -y gopls
CMD ["/usr/bin/gopls", "serve"]
`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(expected))
	}))
	defer server.Close()

	rc := NewRegistryClient()
	got, err := rc.FetchDockerfile(server.URL)
	if err != nil {
		t.Fatalf("FetchDockerfile: %v", err)
	}
	if got != expected {
		t.Errorf("Dockerfile = %q, want %q", got, expected)
	}
}

func TestCompareVersion(t *testing.T) {
	tests := []struct {
		a    string
		b    string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
		{"1.0.1", "1.0.0", 1},
		{"1.0.0", "2.0.0", -1},
		{"2.0.0", "1.0.0", 1},
		{"1.1.0", "1.0.9", 1},
		{"1.0.0", "1.1.0", -1},
		{"v1.0.0", "v1.0.0", 0},
		{"v1.0.0", "1.0.0", 0},
		{"1.0.0-alpha", "1.0.0", 1}, // non-semver falls back to lexicographic ("1.0.0-alpha" > "1.0.0")
		{"latest", "latest", 0},
		{"beta", "alpha", 1},
	}

	for _, tt := range tests {
		t.Run(tt.a+"_vs_"+tt.b, func(t *testing.T) {
			got := CompareVersion(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("CompareVersion(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestUpdateAvailable(t *testing.T) {
	tests := []struct {
		installed string
		remote    string
		want      bool
	}{
		{"1.0.0", "1.0.0", false},
		{"1.0.0", "1.0.1", true},
		{"1.0.1", "1.0.0", false},
		{"1.0.0", "2.0.0", true},
		{"2.0.0", "1.0.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.installed+"_vs_"+tt.remote, func(t *testing.T) {
			got := UpdateAvailable(tt.installed, tt.remote)
			if got != tt.want {
				t.Errorf("UpdateAvailable(%q, %q) = %v, want %v", tt.installed, tt.remote, got, tt.want)
			}
		})
	}
}

func TestRegistryClient_New(t *testing.T) {
	rc := NewRegistryClient()
	if rc == nil {
		t.Fatal("NewRegistryClient() = nil")
	}
	if rc.httpClient == nil {
		t.Fatal("NewRegistryClient().httpClient = nil")
	}
}

func TestFetchIndex_NetworkError(t *testing.T) {
	rc := NewRegistryClient()
	_, err := rc.FetchIndex("http://localhost:1/not-a-real-server")
	if err == nil {
		t.Error("FetchIndex(invalid host) = nil, want error")
	}
}

func TestFetchIndex_FileURL(t *testing.T) {
	rc := NewRegistryClient()

	// Create a temp file with valid registry JSON
	tmp := t.TempDir()
	indexPath := tmp + "/registry.json"
	data := `[{"name":"test-ext","version":"1.0.0","type":"lsp","display_name":"Test"}]`
	if err := os.WriteFile(indexPath, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	index, err := rc.FetchIndex("file://" + indexPath)
	if err != nil {
		t.Fatalf("FetchIndex(file://) = error: %v", err)
	}
	if len(index) != 1 {
		t.Fatalf("len(index) = %d, want 1", len(index))
	}
	if index[0].Name != "test-ext" {
		t.Fatalf("index[0].Name = %q, want %q", index[0].Name, "test-ext")
	}
}

func TestFetchIndex_FileNotFound(t *testing.T) {
	rc := NewRegistryClient()
	_, err := rc.FetchIndex("file:///nonexistent/path/registry.json")
	if err == nil {
		t.Error("FetchIndex(nonexistent file) = nil, want error")
	}
}

func TestFetchManifest_FileURL(t *testing.T) {
	rc := NewRegistryClient()

	// Create a temp file with valid manifest JSON
	tmp := t.TempDir()
	manifestPath := tmp + "/manifest.json"
	data := `{"display_name":"Test","description":"Desc","author":"Auth","lsp":{"command":"test","extensions":["go"],"language":"go"}}`
	if err := os.WriteFile(manifestPath, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	manifest, err := rc.FetchManifest("file://" + manifestPath)
	if err != nil {
		t.Fatalf("FetchManifest(file://) = error: %v", err)
	}
	if manifest.DisplayName != "Test" {
		t.Fatalf("DisplayName = %q, want %q", manifest.DisplayName, "Test")
	}
}

func TestFetchDockerfile_FileURL(t *testing.T) {
	rc := NewRegistryClient()

	// Create a temp Dockerfile
	tmp := t.TempDir()
	dfPath := tmp + "/Dockerfile"
	content := "FROM ubuntu:22.04\nRUN apt-get update\n"
	if err := os.WriteFile(dfPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := rc.FetchDockerfile("file://" + dfPath)
	if err != nil {
		t.Fatalf("FetchDockerfile(file://) = error: %v", err)
	}
	if got != content {
		t.Fatalf("got = %q, want %q", got, content)
	}
}

func TestResolveURL(t *testing.T) {
	tests := []struct {
		base   string
		target string
		want   string
	}{
		// file:// base with relative path (registry in extensions/ dir, target is gopls/manifest.json)
		{"file:///home/user/extensions/registry.json", "gopls/manifest.json", "file:///home/user/extensions/gopls/manifest.json"},
		{"file:///home/user/extensions/registry.json", "./gopls/manifest.json", "file:///home/user/extensions/gopls/manifest.json"},
		// Absolute file:// target returns as-is
		{"file:///base/registry.json", "file:///absolute/path.json", "file:///absolute/path.json"},
		// HTTP base with relative path
		{"https://registry.example.com/index.json", "gopls/manifest.json", "https://registry.example.com/gopls/manifest.json"},
		{"https://registry.example.com/v1/index.json", "../gopls/manifest.json", "https://registry.example.com/gopls/manifest.json"},
		// Absolute HTTP target returns as-is
		{"https://base.com/index.json", "https://other.com/manifest.json", "https://other.com/manifest.json"},
		// Target with scheme returns as-is
		{"file:///base/registry.json", "https://remote.com/manifest.json", "https://remote.com/manifest.json"},
	}

	for _, tt := range tests {
		got := ResolveURL(tt.base, tt.target)
		if got != tt.want {
			t.Errorf("ResolveURL(%q, %q) = %q, want %q", tt.base, tt.target, got, tt.want)
		}
	}
}
