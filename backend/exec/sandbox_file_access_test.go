package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/extensions"
	"kanban/backend/types"
)

// A tool call of a sandboxed task runs in its container, so a missing sandbox callback is a
// visible refusal rather than a silent escape onto the host. One table, because every tool that
// could reach the host shares the one guard and must answer the same way; the LSP tools carry the
// same boundary and are pinned by TestLSPToolReadsThroughSandboxCallback below.
func TestSandboxedTaskWithoutBridgeRefusesEveryHostTool(t *testing.T) {
	cases := []struct {
		name      string
		tool      string
		args      func(target string) map[string]any
		target    func(worktree, outside string) string
		planted   bool                  // target exists before the call, so only its content matters
		forbidden func(res string) bool // host content that must never come back
	}{
		{
			name: "read_file",
			tool: "read_file",
			args: func(target string) map[string]any { return map[string]any{"path": target} },
			target: func(_, outside string) string {
				p := filepath.Join(outside, "secret.txt")
				writeFile(t, p, "SECRET ON THE HOST")
				return p
			},
			planted:   true,
			forbidden: func(res string) bool { return strings.Contains(res, "SECRET ON THE HOST") },
		},
		{
			name: "write_file",
			tool: "write_file",
			args: func(target string) map[string]any {
				return map[string]any{"path": target, "content": "written"}
			},
			target: func(_, outside string) string { return filepath.Join(outside, "dropped.txt") },
		},
		{
			name: "edit_file",
			tool: "edit_file",
			args: func(target string) map[string]any {
				return map[string]any{"path": target, "edits": json.RawMessage(`[{"oldText":"a","newText":"b"}]`)}
			},
			target: func(_, outside string) string { return filepath.Join(outside, "edited.txt") },
		},
		{
			name: "bash",
			tool: "bash",
			args: func(string) map[string]any {
				return map[string]any{"command": "printf 'RAN ON THE HOST\\n' > ran.txt", "timeout": float64(10)}
			},
			// The command works in the worktree, so this is the case that catches a guard
			// sitting after the spawn.
			target: func(worktree, _ string) string { return filepath.Join(worktree, "ran.txt") },
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			worktree := t.TempDir()
			outside := t.TempDir()
			te := newReadTestExecutor(t, worktree)
			task := makeTestTask(t, te.rawStore(), fmt.Sprintf("proj-hostguard-%d", i), fmt.Sprintf("task-hostguard-%d", i))

			target := tc.target(worktree, outside)
			result, ok := te.executeTool(context.Background(), tc.tool, tc.args(target),
				worktree, task, &executionState{sandboxEnabled: true}, nil)

			if ok {
				t.Errorf("sandboxed task completed a host %s with no bridge: %q", tc.tool, result)
			}
			if !tc.planted {
				if _, err := os.Stat(target); err == nil {
					t.Errorf("the refused %s still created a host file at %s", tc.tool, target)
				}
			}
			if tc.forbidden != nil && tc.forbidden(result) {
				t.Errorf("host content was returned to the task: %q", result)
			}
			if !strings.Contains(result, "sandbox") {
				t.Errorf("refusal should name the sandbox, got: %q", result)
			}
		})
	}
}

// A project with sandboxing disabled is the operator's choice of the host, so its reads and writes
// are not jailed to the worktree.
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
