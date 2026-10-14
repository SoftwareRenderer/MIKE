package agent

import (
	"os"
	"path/filepath"
	"testing"

	"mike/backend/extensions"
)

// resolveWasmPath is the only place the module's path is decided. A path that does not exist means an
// installed agent extension cannot start a task, so each candidate is covered here.

func installedExt(author, name, module string) *extensions.Extension {
	return &extensions.Extension{
		Name:   name,
		Author: author,
		Type:   extensions.ExtensionTypeAgent,
		Manifest: extensions.ExtensionManifest{
			Capabilities: &extensions.Capabilities{
				Agent: &extensions.AgentConfig{WasmModule: module},
			},
		},
	}
}

// putModule creates a file at path; resolveWasmPath only checks that it exists.
func putModule(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte("wasm"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestResolveWasmPath_FindsTheInstalledFolder(t *testing.T) {
	t.Chdir(t.TempDir())
	// An install, registry or local, materializes <author>/<name>/.
	putModule(t, filepath.Join("extensions", "getmike.dev", "claude-code", "claude-code.wasm"))

	got := resolveWasmPath(installedExt("getmike.dev", "claude-code", "claude-code.wasm"))
	want := filepath.Join("extensions", "getmike.dev", "claude-code", "claude-code.wasm")
	if got != want {
		t.Errorf("resolveWasmPath = %q, want the installed folder %q", got, want)
	}
}

func TestResolveWasmPath_FindsAFlatLocalFolder(t *testing.T) {
	t.Chdir(t.TempDir())
	// A folder with no author directory is a local extension, reached only by extensionDir's flat
	// fallback.
	putModule(t, filepath.Join("extensions", "claude-code", "claude-code.wasm"))

	got := resolveWasmPath(installedExt("local", "claude-code", "claude-code.wasm"))
	want := filepath.Join("extensions", "claude-code", "claude-code.wasm")
	if got != want {
		t.Errorf("resolveWasmPath = %q, want the local folder %q", got, want)
	}
}

func TestResolveWasmPath_StillFindsTheAdapterSourceTree(t *testing.T) {
	t.Chdir(t.TempDir())
	// The source layout holds the build output before any install exists.
	putModule(t, filepath.Join("extensions", "agent", "pi", "pi.wasm"))

	got := resolveWasmPath(installedExt("getmike.dev", "pi", "pi.wasm"))
	want := filepath.Join("extensions", "agent", "pi", "pi.wasm")
	if got != want {
		t.Errorf("resolveWasmPath = %q, want the source-tree copy %q", got, want)
	}
}

// TestResolveWasmPath_InstalledFolderWins: when a stale source copy and the installed folder both hold
// a module, the installed one is used.
func TestResolveWasmPath_InstalledFolderWins(t *testing.T) {
	t.Chdir(t.TempDir())
	putModule(t, filepath.Join("extensions", "agent", "pi", "pi.wasm"))
	installed := filepath.Join("extensions", "getmike.dev", "pi", "pi.wasm")
	putModule(t, installed)

	got := resolveWasmPath(installedExt("getmike.dev", "pi", "pi.wasm"))
	if got != installed {
		t.Errorf("resolveWasmPath = %q, want the installed %q", got, installed)
	}
}

func TestResolveWasmPath_NothingOnDiskFallsThrough(t *testing.T) {
	t.Chdir(t.TempDir())

	got := resolveWasmPath(installedExt("getmike.dev", "pi", "pi.wasm"))
	if got != "pi.wasm" {
		t.Errorf("resolveWasmPath = %q, want the bare module name so the error names what was wanted", got)
	}
}
