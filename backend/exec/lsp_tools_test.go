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
	"mike/backend/store"
	"mike/backend/types"
	"time"
)

// newLSPTestExecutor creates a TaskExecutor with no LSP manager for testing LSP tool error paths.
func newLSPTestExecutor(t *testing.T, worktreePath string) *TaskExecutor {
	t.Helper()
	s := testStore(t)
	appStreamMgr := newTestStreamMgr(t, s, nil)
	// No LSPMgr: no LSP extension is installed.
	return newTestExecutorWith(t, Deps{
		Store:        s,
		AppStreamMgr: appStreamMgr,
		ConfigStore:  &config.ConfigStore{},
	})
}

// newLSPTestExecutorWithMgr creates a TaskExecutor with an LSP manager and extensions.
func newLSPTestExecutorWithMgr(t *testing.T, worktreePath string, exts ...*extensions.Extension) *TaskExecutor {
	t.Helper()
	s := testStore(t)
	es := extensions.NewExtensionStore(s)
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

func TestLSPTools_NoLSPManager(t *testing.T) {
	dir := t.TempDir()
	te := newLSPTestExecutor(t, dir)
	task := makeTestTaskLSP(t, te.store, "proj-lsp-1", "task-lsp-1")

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"lsp_references", map[string]any{"path": "main.go", "symbol": "main"}},
		{"lsp_definition", map[string]any{"path": "main.go", "symbol": "main"}},
		{"lsp_hover", map[string]any{"path": "main.go", "symbol": "main"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, ok := te.executeTool(context.Background(), tc.name, tc.args, dir, task, &executionState{}, nil)

			if ok {
				t.Fatalf("expected error, got success: %s", result)
			}
			if result == "" {
				t.Fatal("expected error message, got empty string")
			}
		})
	}
}

func TestLSPTools_MissingPath(t *testing.T) {
	dir := t.TempDir()
	te := newLSPTestExecutor(t, dir)
	task := makeTestTaskLSP(t, te.store, "proj-lsp-2", "task-lsp-2")

	// lsp_references with no path
	result, ok := te.executeTool(context.Background(), "lsp_references", map[string]any{
		"symbol": "main",
	}, dir, task, &executionState{}, nil)

	if ok {
		t.Fatalf("expected error, got success: %s", result)
	}
	if result == "" {
		t.Fatal("expected error message, got empty string")
	}
}

func TestLSPTools_ToolCallsDisabledForFile(t *testing.T) {
	// Create Go extension with tool calls disabled
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
	te := newLSPTestExecutorWithMgr(t, dir, goExt)
	task := makeTestTaskLSP(t, te.store, "proj-lsp-3", "task-lsp-3")

	// Create a Go file
	goFile := filepath.Join(dir, "main.go")
	writeFile(t, goFile, "package main\n\nfunc main() {}")

	// Go file — tool calls should be rejected
	result, ok := te.executeTool(context.Background(), "lsp_references", map[string]any{
		"path":   "main.go",
		"symbol": "main",
	}, dir, task, &executionState{}, nil)

	if ok {
		t.Fatalf("expected error for Go file, got success: %s", result)
	}
	if result == "" {
		t.Fatal("expected error message for Go file, got empty string")
	}
	if !containsString(result, "tool calls are disabled") {
		t.Errorf("expected 'tool calls are disabled' error, got: %s", result)
	}
}

func TestFormatLSPResponse_ErrorEnvelope(t *testing.T) {
	resp := formatLSPResponse([]byte(`{"error":{"code":-32602,"message":"Invalid params"}}`), "textDocument/definition", "/tmp")
	if resp != "LSP error -32602: Invalid params" {
		t.Errorf("got %q", resp)
	}
}

func TestFormatLSPResponse_EmptyResult(t *testing.T) {
	resp := formatLSPResponse([]byte(`{"result":null}`), "textDocument/hover", "/tmp")
	if resp != "(no result)" {
		t.Errorf("got %q", resp)
	}
}

func TestFormatLSPResponse_References(t *testing.T) {
	resp := formatLSPResponse([]byte(`{
		"result": [
			{"uri": "file:///tmp/proj/main.go", "range": {"start": {"line": 5, "character": 10}}},
			{"uri": "file:///tmp/proj/main.go", "range": {"start": {"line": 20, "character": 0}}}
		]
	}`), "textDocument/references", "/tmp/proj")

	if resp != "Found 2 references:\n  1. main.go:6:11\n  2. main.go:21:1\n" {
		t.Errorf("got %q", resp)
	}
}

func TestFormatLSPResponse_ReferencesEmpty(t *testing.T) {
	resp := formatLSPResponse([]byte(`{"result":[]}`), "textDocument/references", "/tmp/proj")
	if resp != "No references found" {
		t.Errorf("got %q", resp)
	}
}

func TestFormatLSPResponse_DefinitionSingle(t *testing.T) {
	resp := formatLSPResponse([]byte(`{
		"result": {
			"uri": "file:///tmp/proj/pkg/utils.go",
			"range": {"start": {"line": 42, "character": 6}}
		}
	}`), "textDocument/definition", "/tmp/proj")

	if resp != "Definition found at pkg/utils.go:43:7" {
		t.Errorf("got %q", resp)
	}
}

func TestFormatLSPResponse_Hover(t *testing.T) {
	resp := formatLSPResponse([]byte(`{"result":{"contents":{"kind":"markdown","value":"func Foo() string\nReturns a foo."}}}`), "textDocument/hover", "/tmp/proj")

	expected := "[markdown]\nfunc Foo() string\nReturns a foo."
	if resp != expected {
		t.Errorf("got %q", resp)
	}
}

func TestIsReadOnlyCall_LSPTools(t *testing.T) {
	for _, toolName := range []string{"lsp_references", "lsp_definition", "lsp_hover"} {
		if !isReadOnlyCall(toolName, `{"path":"main.go"}`) {
			t.Errorf("isReadOnlyCall(%q) = false, want true", toolName)
		}
	}
}

// makeTestTaskLSP creates a task for LSP tool tests.
func makeTestTaskLSP(t *testing.T, s *store.Store, projectID, taskID string) *types.Task {
	t.Helper()
	createProject(t, s, projectID, projectID)
	now := time.Now().UTC()
	task := &types.Task{
		ID:             taskID,
		ProjectID:      projectID,
		Title:          "Test Task",
		Prompt:         "Test prompt",
		Status:         types.StatusInProgress,
		ExecutionState: types.ExecStateRunning,
		LlamaServerURL: "http://test:8080",
		ModelName:      "test-model",
		OperatingMode:  "act",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return task
}

func TestFindSymbolInText(t *testing.T) {
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
