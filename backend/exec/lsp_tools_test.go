package exec

import (
	"context"
	"testing"

	"kanban/backend/config"
	"kanban/backend/store"
	"kanban/backend/stream"
	"kanban/backend/types"
	"time"
)

// newLSPTestExecutor creates a TaskExecutor with no LSP manager for testing LSP tool error paths.
func newLSPTestExecutor(t *testing.T, worktreePath string) *TaskExecutor {
	t.Helper()
	s := testStore(t)
	appStreamMgr := stream.NewAppStreamManager(s, nil)
	return &TaskExecutor{
		store:        s,
		appStreamMgr: appStreamMgr,
		configStore:  &config.ConfigStore{},
		tasks:        make(map[string]*executionState),
		// lspMgr is nil — no LSP extension installed
	}
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
		{"lsp_code_actions", map[string]any{"path": "main.go", "symbol": "main"}},
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

func TestFormatLSPResponse_CodeActions(t *testing.T) {
	resp := formatLSPResponse([]byte(`{
		"result": [
			{"title": "Add missing import", "kind": "quickfix"},
			{"title": "Organize imports", "kind": "source.organizeImports"}
		]
	}`), "textDocument/codeAction", "/tmp/proj")

	if resp != "Found 2 code action(s):\n  1. Add missing import [quickfix]\n  2. Organize imports [source.organizeImports]\n" {
		t.Errorf("got %q", resp)
	}
}

func TestFormatLSPResponse_CodeActionsEmpty(t *testing.T) {
	resp := formatLSPResponse([]byte(`{"result":[]}`), "textDocument/codeAction", "/tmp/proj")
	if resp != "No code actions available at this position" {
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
	for _, toolName := range []string{"lsp_references", "lsp_definition", "lsp_code_actions", "lsp_hover"} {
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
		Status:         "in_progress",
		ExecutionState: "running",
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
