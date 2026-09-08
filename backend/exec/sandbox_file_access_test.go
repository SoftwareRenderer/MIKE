package exec

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/extensions"
	"kanban/backend/types"
)

// A tool call of a sandboxed task runs in its container, so a missing sandbox callback is a
// visible refusal rather than a silent escape onto the host.
func TestSandboxedTaskCannotReadHostFileWithoutBridge(t *testing.T) {
	worktree := t.TempDir()
	te := newReadTestExecutor(t, worktree)
	task := makeTestTask(t, te.rawStore(), "proj-hostguard-1", "task-hostguard-1")

	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("SECRET ON THE HOST"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{"path": secret},
		worktree, task, &executionState{sandboxEnabled: true}, nil)

	if ok {
		t.Errorf("sandboxed task read a host file with no bridge: %q", result)
	}
	if strings.Contains(result, "SECRET ON THE HOST") {
		t.Errorf("host content was returned to the task: %q", result)
	}
	if !strings.Contains(result, "sandbox") {
		t.Errorf("refusal should name the sandbox, got: %q", result)
	}
}

// A project with sandboxing disabled is the operator's choice of the host, so its reads are not
// jailed to the worktree.
func TestUnsandboxedTaskReadsOutsideWorktree(t *testing.T) {
	worktree := t.TempDir()
	te := newReadTestExecutor(t, worktree)
	task := makeTestTask(t, te.rawStore(), "proj-hostguard-2", "task-hostguard-2")

	outside := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(outside, []byte("outside content"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{"path": outside},
		worktree, task, &executionState{}, nil)

	if !ok || result != "outside content" {
		t.Errorf("unsandboxed task should read any host path, got ok=%v result=%q", ok, result)
	}
}

// The same refusal on the write side, where the host fallback is the worktree os.Root.
func TestSandboxedTaskCannotWriteHostFileWithoutBridge(t *testing.T) {
	worktree := t.TempDir()
	te := newReadTestExecutor(t, worktree)
	task := makeTestTask(t, te.rawStore(), "proj-hostguard-3", "task-hostguard-3")

	target := filepath.Join(t.TempDir(), "dropped.txt")
	result, ok := te.executeTool(context.Background(), "write_file", map[string]any{
		"path": target, "content": "written",
	}, worktree, task, &executionState{sandboxEnabled: true}, nil)

	if ok {
		t.Errorf("sandboxed task wrote to the host with no bridge: %q", result)
	}
	if _, err := os.Stat(target); err == nil {
		t.Errorf("the refused write still created a host file at %s", target)
	}
}

// DANGER_ZONE=1 is set for this whole package (TestMain), so a refusal here is the sandbox rule
// holding rather than the danger-zone flag doing the work.
func TestSandboxedTaskCannotRunHostCommandWithoutBridge(t *testing.T) {
	worktree := t.TempDir()
	te := newReadTestExecutor(t, worktree)
	task := makeTestTask(t, te.rawStore(), "proj-hostguard-4", "task-hostguard-4")

	result, ok := te.executeTool(context.Background(), "bash", map[string]any{
		"command": "printf 'RAN ON THE HOST\\n' > ran.txt",
		"timeout": float64(10),
	}, worktree, task, &executionState{sandboxEnabled: true}, nil)

	if ok {
		t.Errorf("sandboxed task ran a command on the host with no bridge: %q", result)
	}
	if _, err := os.Stat(filepath.Join(worktree, "ran.txt")); err == nil {
		t.Error("the refused command still ran — the guard sits after the spawn")
	}
	if !strings.Contains(result, "sandbox") {
		t.Errorf("refusal should name the sandbox, got: %q", result)
	}
}

// goplsExtension is the LSP extension the sandboxed read test installs, with tool calls
// enabled so the read happens at all.
func goplsExtension() *extensions.Extension {
	enabled := true
	return &extensions.Extension{
		ID:      "gopls",
		Name:    "gopls",
		Version: "1.0.0",
		Type:    extensions.ExtensionTypeLSP,
		Manifest: extensions.ExtensionManifest{
			DisplayName: "Go Language Server",
			Version:     "1.0.0",
			Capabilities: &extensions.Capabilities{
				LSP: &extensions.LSPConfig{
					Command:          "gopls",
					Args:             []string{"serve"},
					FileTypes:        []extensions.LSPFileType{{Extension: "go", LanguageID: "go"}},
					Language:         "go",
					ToolCallsEnabled: &enabled,
				},
			},
		},
		Enabled: true,
	}
}

// The LSP tools read the file to find the symbol, so that read carries the same boundary as
// read_file: a sandboxed task looks it up in the container, not at filepath.Join(worktree, "..").
func TestLSPToolReadsThroughSandboxCallback(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "wt")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	// The host copy carries the symbol; the container's answer does not. If the tool reads
	// the host, it gets past the not-found branch and this test cannot pass.
	writeFile(t, filepath.Join(root, "outside.go"), "package other\n\nfunc hostSideSymbol() {}\n")

	te := newLSPTestExecutorWithMgr(t, worktree, goplsExtension())
	task := makeTestTaskLSP(t, te.rawStore(), "proj-lsp-guard", "task-lsp-guard")

	var asked []string
	state := &executionState{sandboxEnabled: true}
	state.sandboxRead = func(_ context.Context, p string) ([]byte, error) {
		asked = append(asked, p)
		return []byte("package other\n\nfunc containerSideOnly() {}\n"), nil
	}

	result, _ := te.executeTool(context.Background(), "lsp_references", map[string]any{
		"path":   "../outside.go",
		"symbol": "hostSideSymbol",
	}, worktree, task, state, nil)

	if len(asked) != 1 || asked[0] != "../outside.go" {
		t.Errorf("expected the container reader to be asked for ../outside.go, got %v", asked)
	}
	if !strings.Contains(result, "not found in ../outside.go") {
		t.Errorf("expected the symbol to be searched in the container's content, got: %q", result)
	}
}

// A task with sandboxing disabled keeps the host read, so the position the tool asks about is the
// one in the worktree copy.
func TestLSPToolReadsWorktreeFileWhenUnsandboxed(t *testing.T) {
	worktree := t.TempDir()
	writeFile(t, filepath.Join(worktree, "main.go"), "package main\n\nfunc hostSymbol() {}\n")

	fake := &fakeLSPManager{
		enabled:   true,
		responses: map[string]json.RawMessage{"textDocument/references": json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":[]}`)},
	}
	te := &TaskExecutor{lspMgr: fake}

	result, ok := te.executeTool(context.Background(), "lsp_references", map[string]any{
		"path":   "main.go",
		"symbol": "hostSymbol",
	}, worktree, &types.Task{}, &executionState{}, nil)

	if !ok {
		t.Fatalf("an unsandboxed task reads the worktree on the host, got: %q", result)
	}
	if len(fake.methods) != 1 || fake.methods[0] != "textDocument/references" {
		t.Fatalf("expected one references request, got %v", fake.methods)
	}
	params, _ := fake.lastParamsByMethod["textDocument/references"].(map[string]any)
	position, _ := params["position"].(map[string]any)
	if position["line"] != 2 || position["character"] != 5 {
		t.Errorf("position came from content the task cannot see: %v", position)
	}
}
