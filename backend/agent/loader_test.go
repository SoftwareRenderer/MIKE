package agent

import (
	"os"
	"path/filepath"
	"testing"

	"mike/backend/extensions"
)

// resolveWasmPath is the only place the module's path is decided, and a path that does not exist means an
// installed agent extension cannot start a task. One question per candidate folder that holds a module of
// this name: which one does it answer? The order is the decision — the installed folder first, then the
// adapter's source tree, then the bare module name, which is also what the caller's error names.

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

func TestResolveWasmPathChoosesTheFolderThatHoldsTheModule(t *testing.T) {
	const installed = "extensions/getmike.dev/claude-code/claude-code.wasm"
	const flat = "extensions/claude-code/claude-code.wasm"
	const sourceTree = "extensions/agent/pi/pi.wasm"
	const installedPi = "extensions/getmike.dev/pi/pi.wasm"

	for _, tc := range []struct {
		name   string
		author string
		ext    string
		module string
		// holds are the module files that exist on disk, relative to the scratch root.
		holds []string
		// want is the path resolveWasmPath must answer.
		want string
	}{
		{
			name:   "an install, registry or local, materializes <author>/<name>/ and that folder answers",
			author: "getmike.dev", ext: "claude-code", module: "claude-code.wasm",
			holds: []string{installed}, want: installed,
		},
		{
			name:   "a folder with no author directory is a local extension, reached only by the flat fallback",
			author: "local", ext: "claude-code", module: "claude-code.wasm",
			holds: []string{flat}, want: flat,
		},
		{
			name:   "the source layout holds the build output before any install exists",
			author: "getmike.dev", ext: "pi", module: "pi.wasm",
			holds: []string{sourceTree}, want: sourceTree,
		},
		{
			name:   "a stale source copy loses to the installed folder",
			author: "getmike.dev", ext: "pi", module: "pi.wasm",
			holds: []string{sourceTree, installedPi}, want: installedPi,
		},
		{
			name:   "nothing on disk answers the bare name, so the error names what was wanted",
			author: "getmike.dev", ext: "pi", module: "pi.wasm",
			holds: nil, want: "pi.wasm",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			for _, module := range tc.holds {
				putModule(t, filepath.FromSlash(module))
			}
			got := resolveWasmPath(installedExt(tc.author, tc.ext, tc.module))
			if want := filepath.FromSlash(tc.want); got != want {
				t.Errorf("resolveWasmPath = %q, want %q", got, want)
			}
		})
	}
}
