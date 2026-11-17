package exec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/config"
	"mike/backend/extensions"
	"mike/backend/lsp"
)

// newToolTestExecutorWithMgr creates a TaskExecutor with an LSP manager and extensions.
func newToolTestExecutorWithMgr(t *testing.T, worktreePath string, exts ...*extensions.Extension) *TaskExecutor {
	t.Helper()
	s := testStore(t)
	es := extensions.NewExtensionStore(s, nil)
	if err := es.InitSchema(); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	for _, ext := range exts {
		if err := es.Create(ext); err != nil {
			t.Fatalf("Create extension %s: %v", ext.Name, err)
		}
	}
	mgr := lsp.NewExtensionLSPManager(es, nil, s)
	appStreamMgr := newTestStreamMgr(t, s, nil)
	return newTestExecutorWith(t, Deps{
		Store:        s,
		AppStreamMgr: appStreamMgr,
		ConfigStore:  &config.ConfigStore{},
		LSPMgr:       mgr,
	})
}

// Every LSP tool asks the same questions before it asks a language server anything, so every one of
// them must refuse alone: no manager, a missing argument, or a file type whose tool calls are off is
// an answer, never a request and never a panic over a nil manager.
func TestLSPTools_RefusedBeforeTheLanguageServer(t *testing.T) {
	t.Parallel()
	const notAvailable = "LSP manager not available"

	readerCall := func() map[string]any { return map[string]any{"path": "main.go", "symbol": "main"} }
	renameCall := func() map[string]any {
		return map[string]any{"path": "main.go", "symbol": "main", "new_name": "other"}
	}

	t.Run("no manager", func(t *testing.T) {
		dir := t.TempDir()
		te := newToolTestExecutor(t, dir) // no LSP manager at all
		task := makeTestTask(t, te.store, "proj-lsp-1", "task-lsp-1")
		for _, tc := range []struct {
			tool string
			args map[string]any
		}{
			{"lsp_references", readerCall()},
			{"lsp_definition", readerCall()},
			{"lsp_hover", readerCall()},
			{"lsp_rename", renameCall()},
		} {
			t.Run(tc.tool, func(t *testing.T) {
				result, ok := te.executeTool(context.Background(), tc.tool, tc.args, dir, task, &executionState{}, nil)
				if ok {
					t.Fatalf("expected error, got success: %s", result)
				}
				if !strings.Contains(result, notAvailable) {
					t.Errorf("result %q does not name the missing manager", result)
				}
			})
		}
	})

	t.Run("missing arguments", func(t *testing.T) {
		dir := t.TempDir()
		te := newToolTestExecutor(t, dir)
		te.lspMgr = &fakeLSPManager{enabled: true}
		task := makeTestTask(t, te.store, "proj-lsp-2", "task-lsp-2")
		for _, tc := range []struct {
			tool string
			args map[string]any
			want string // the argument the refusal must name
		}{
			{"lsp_references", map[string]any{"symbol": "main"}, "path"},
			{"lsp_references", map[string]any{"path": "main.go"}, "symbol"},
			{"lsp_rename", map[string]any{"path": "main.go", "symbol": "main"}, "new_name"},
			{"lsp_rename", map[string]any{"symbol": "main", "new_name": "other"}, "path"},
		} {
			t.Run(tc.tool+" without "+tc.want, func(t *testing.T) {
				result, ok := te.executeTool(context.Background(), tc.tool, tc.args, dir, task, &executionState{}, nil)
				if ok {
					t.Fatalf("expected error, got success: %s", result)
				}
				if !strings.Contains(result, tc.want) || !strings.Contains(result, "required") {
					t.Errorf("result %q does not name the missing %q", result, tc.want)
				}
			})
		}
	})

	t.Run("tool calls disabled for the file type", func(t *testing.T) {
		// The manifest's tool_calls_enabled is what decides, read off the file's own extension.
		falseVal := false
		goExt := &extensions.Extension{
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
						ToolCallsEnabled: &falseVal,
					},
				},
			},
			Enabled: true,
		}

		dir := t.TempDir()
		te := newToolTestExecutorWithMgr(t, dir, goExt)
		task := makeTestTask(t, te.store, "proj-lsp-3", "task-lsp-3")
		writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}")

		for _, tc := range []struct {
			tool string
			args map[string]any
		}{
			{"lsp_references", readerCall()},
			{"lsp_definition", readerCall()},
			{"lsp_hover", readerCall()},
			{"lsp_rename", renameCall()},
		} {
			t.Run(tc.tool, func(t *testing.T) {
				result, ok := te.executeTool(context.Background(), tc.tool, tc.args, dir, task, &executionState{}, nil)
				if ok {
					t.Fatalf("expected error for a Go file, got success: %s", result)
				}
				if !containsString(result, "tool calls are disabled") {
					t.Errorf("expected 'tool calls are disabled' error, got: %s", result)
				}
			})
		}
	})
}

// The answer the model gets for each wire shape a language server sends back: the file is named
// relative to the workspace it was asked in, positions are 1-indexed for the reader, and an error
// envelope keeps its code and its sentence.
func TestFormatLSPResponse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		resp   string
		method string
		root   string
		want   string
	}{
		{
			name: "error envelope", method: "textDocument/definition", root: "/tmp",
			resp: `{"error":{"code":-32602,"message":"Invalid params"}}`, want: "LSP error -32602: Invalid params",
		},
		{
			name: "null result", method: "textDocument/hover", root: "/tmp",
			resp: `{"result":null}`, want: "(no result)",
		},
		{
			name: "references", method: "textDocument/references", root: "/tmp/proj",
			resp: `{"result":[
				{"uri": "file:///tmp/proj/main.go", "range": {"start": {"line": 5, "character": 10}}},
				{"uri": "file:///tmp/proj/main.go", "range": {"start": {"line": 20, "character": 0}}}]}`,
			want: "Found 2 references:\n  1. main.go:6:11\n  2. main.go:21:1\n",
		},
		{
			name: "no references", method: "textDocument/references", root: "/tmp/proj",
			resp: `{"result":[]}`, want: "No references found",
		},
		{
			name: "single definition", method: "textDocument/definition", root: "/tmp/proj",
			resp: `{"result":{"uri": "file:///tmp/proj/pkg/utils.go",
				"range": {"start": {"line": 42, "character": 6}}}}`,
			want: "Definition found at pkg/utils.go:43:7",
		},
		{
			name: "hover", method: "textDocument/hover", root: "/tmp/proj",
			resp: `{"result":{"contents":{"kind":"markdown","value":"func Foo() string\nReturns a foo."}}}`,
			want: "[markdown]\nfunc Foo() string\nReturns a foo.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatLSPResponse([]byte(tc.resp), tc.method, tc.root); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsReadOnlyCall_LSPTools(t *testing.T) {
	t.Parallel()
	for _, toolName := range []string{"lsp_references", "lsp_definition", "lsp_hover"} {
		if !isReadOnlyCall(toolName, `{"path":"main.go"}`) {
			t.Errorf("isReadOnlyCall(%q) = false, want true", toolName)
		}
	}
}

func TestFindSymbolInText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		content string
		symbol  string
		want    map[string]any
	}{
		// Simple case: "fmt" on line 1 (after import "")
		{
			content: "package main\nimport \"fmt\"\n",
			symbol:  "fmt",
			want:    map[string]any{"line": 1, "character": 8},
		},
		// "main" in package declaration (first match wins)
		{
			content: "package main\n\nfunc main() {}\n",
			symbol:  "main",
			want:    map[string]any{"line": 0, "character": 8},
		},
		// "fmt" in package declaration (first match wins)
		{
			content: "package fmt\n\nfunc Foo() {}\n",
			symbol:  "fmt",
			want:    map[string]any{"line": 0, "character": 8},
		},
		// No match for partial word
		{
			content: "package main\n\nfunc format() {}\n",
			symbol:  "fmt",
			want:    nil,
		},
		// Not found
		{
			content: "package main\n\nfunc main() {}\n",
			symbol:  "os",
			want:    nil,
		},
		// Match with underscore prefix
		{
			content: "_fmt := 1\n",
			symbol:  "_fmt",
			want:    map[string]any{"line": 0, "character": 0},
		},
	}

	for _, tt := range tests {
		got := findSymbolInText(tt.content, tt.symbol)
		if tt.want == nil {
			if got != nil {
				t.Errorf("findSymbolInText(%q, %q) = %v, want nil", tt.content, tt.symbol, got)
			}
		} else {
			if got == nil {
				t.Errorf("findSymbolInText(%q, %q) = nil, want %v", tt.content, tt.symbol, tt.want)
			} else if got["line"] != tt.want["line"] || got["character"] != tt.want["character"] {
				t.Errorf("findSymbolInText(%q, %q) = %v, want %v", tt.content, tt.symbol, got, tt.want)
			}
		}
	}
}

func TestFindWordIndex(t *testing.T) {
	t.Parallel()
	tests := []struct {
		line string
		word string
		want int
	}{
		{"import \"fmt\"", "fmt", 8},
		{"fmt.Println()", "fmt", 0},
		{"x := fmt.Sprintf", "fmt", 5},
		{"format()", "fmt", -1},
		{"fmt2()", "fmt", -1},
		{"_fmt", "fmt", -1},
		{"fmt", "fmt", 0},
		{"fmt fmt", "fmt", 0}, // first match
	}

	for _, tt := range tests {
		got := findWordIndex(tt.line, tt.word)
		if got != tt.want {
			t.Errorf("findWordIndex(%q, %q) = %d, want %d", tt.line, tt.word, got, tt.want)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
}

func containsString(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
