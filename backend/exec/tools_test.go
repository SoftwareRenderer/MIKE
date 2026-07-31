package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/types"
)

// --- isSandboxableTool Tests ---

func TestIsSandboxableTool(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"read_file", true},
		{"write_file", true},
		{"edit_file", true},
		{"bash", true},
		{"request_review", false},
		{"unknown_tool", false},
		{"", false},
	}

	for _, tt := range tests {
		got := isSandboxableTool(tt.name)
		if got != tt.want {
			t.Errorf("isSandboxableTool(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// --- executeTool with sandbox callbacks ---

func TestExecuteTool_ReadFile_Sandboxed(t *testing.T) {
	// Create a temp file to simulate sandbox read
	dir := t.TempDir()
	testFile := filepath.Join(dir, "hello.txt")
	expectedContent := "hello from sandbox"
	if err := os.WriteFile(testFile, []byte(expectedContent), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	te := &TaskExecutor{}

	// Simulate sandbox read callback
	sandboxRead := func(_ context.Context, path string) ([]byte, error) {
		// In real sandbox, path is relative to worktree
		return os.ReadFile(filepath.Join(dir, path))
	}

	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{"path": "hello.txt"}, dir, &types.Task{}, &executionState{sandboxRead: sandboxRead}, nil)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}
	if result != expectedContent {
		t.Errorf("expected %q, got %q", expectedContent, result)
	}
}

func TestExecuteTool_ReadFile_HostFallback(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "host.txt")
	expectedContent := "hello from host"
	if err := os.WriteFile(testFile, []byte(expectedContent), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	te := &TaskExecutor{}

	// No sandboxRead — should fall back to host
	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{"path": "host.txt"}, dir, &types.Task{}, &executionState{}, nil)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}
	if result != expectedContent {
		t.Errorf("expected %q, got %q", expectedContent, result)
	}
}

func TestExecuteTool_ReadFile_PathEscape(t *testing.T) {
	// Non-sandbox: os.Root blocks path escapes at the OS level
	te := &TaskExecutor{}

	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{"path": "../etc/passwd"}, "/tmp/worktree", &types.Task{}, &executionState{}, nil)
	if ok {
		t.Fatalf("expected failure for path escape, got success: %s", result)
	}
	if !strings.Contains(result, "Error:") {
		t.Errorf("expected error, got %q", result)
	}
}

func TestExecuteTool_WriteFile_Sandboxed(t *testing.T) {
	dir := t.TempDir()
	te := &TaskExecutor{}
	var writtenPath, writtenContent string

	// Simulate sandbox write callback
	sandboxWrite := func(_ context.Context, path, content string) error {
		writtenPath = path
		writtenContent = content
		fullPath := filepath.Join(dir, path)
		os.MkdirAll(filepath.Dir(fullPath), 0755)
		return os.WriteFile(fullPath, []byte(content), 0644)
	}

	result, ok := te.executeTool(context.Background(), "write_file", map[string]any{
		"path":    "test.txt",
		"content": "hello sandbox",
	}, dir, &types.Task{}, &executionState{sandboxWrite: sandboxWrite}, nil)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}
	if result != "Wrote 13 bytes to test.txt" {
		t.Errorf("expected 'Wrote 13 bytes to test.txt', got %q", result)
	}
	if writtenPath != "test.txt" {
		t.Errorf("expected path 'test.txt', got %q", writtenPath)
	}
	if writtenContent != "hello sandbox" {
		t.Errorf("expected content 'hello sandbox', got %q", writtenContent)
	}

	// Verify file was actually written
	data, err := os.ReadFile(filepath.Join(dir, "test.txt"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "hello sandbox" {
		t.Errorf("file content = %q, want 'hello sandbox'", string(data))
	}
}

func TestExecuteTool_WriteFile_HostFallback(t *testing.T) {
	dir := t.TempDir()
	te := &TaskExecutor{}

	// No sandboxWrite — should fall back to host
	result, ok := te.executeTool(context.Background(), "write_file", map[string]any{
		"path":    "host.txt",
		"content": "hello host",
	}, dir, &types.Task{}, &executionState{}, nil)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}
	if result != "Wrote 10 bytes to host.txt" {
		t.Errorf("expected 'Wrote 10 bytes to host.txt', got %q", result)
	}

	// Verify file was written
	data, err := os.ReadFile(filepath.Join(dir, "host.txt"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "hello host" {
		t.Errorf("file content = %q, want 'hello host'", string(data))
	}
}

func TestExecuteTool_WriteFile_PathEscape(t *testing.T) {
	// Non-sandbox: os.Root blocks path escapes at the OS level
	te := &TaskExecutor{}

	result, ok := te.executeTool(context.Background(), "write_file", map[string]any{
		"path":    "../etc/evil",
		"content": "malicious",
	}, "/tmp/worktree", &types.Task{}, &executionState{}, nil)
	if ok {
		t.Fatalf("expected failure for path escape, got success: %s", result)
	}
	if !strings.Contains(result, "Error:") {
		t.Errorf("expected error, got %q", result)
	}
}

func TestExecuteTool_WriteFile_DirCreation(t *testing.T) {
	dir := t.TempDir()
	te := &TaskExecutor{}

	sandboxWrite := func(_ context.Context, path, content string) error {
		fullPath := filepath.Join(dir, path)
		os.MkdirAll(filepath.Dir(fullPath), 0755)
		return os.WriteFile(fullPath, []byte(content), 0644)
	}

	result, ok := te.executeTool(context.Background(), "write_file", map[string]any{
		"path":    "deep/nested/dir/file.txt",
		"content": "nested content",
	}, dir, &types.Task{}, &executionState{sandboxWrite: sandboxWrite}, nil)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}

	data, err := os.ReadFile(filepath.Join(dir, "deep", "nested", "dir", "file.txt"))
	if err != nil {
		t.Fatalf("read nested file: %v", err)
	}
	if string(data) != "nested content" {
		t.Errorf("nested file content = %q, want 'nested content'", string(data))
	}
}

func TestExecuteTool_TaskComplete(t *testing.T) {
	te := &TaskExecutor{}
	result, ok := te.executeTool(context.Background(), "request_review", map[string]any{}, "/tmp/worktree", &types.Task{}, &executionState{}, nil)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}
	if result != "Task complete" {
		t.Errorf("expected 'Task complete', got %q", result)
	}
}

func TestExecuteTool_AskQuestion(t *testing.T) {
	te := &TaskExecutor{}
	result, ok := te.executeTool(context.Background(), "ask_question", map[string]any{"question": "What framework should I use?"}, "/tmp/worktree", &types.Task{}, &executionState{}, nil)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}
	if result != "What framework should I use?" {
		t.Errorf("expected question text, got %q", result)
	}
}

func TestExecuteTool_AskQuestion_Empty(t *testing.T) {
	te := &TaskExecutor{}
	result, ok := te.executeTool(context.Background(), "ask_question", map[string]any{"question": ""}, "/tmp/worktree", &types.Task{}, &executionState{}, nil)
	if ok {
		t.Fatalf("expected failure for empty question, got success: %s", result)
	}
	if result != "Error: question is required" {
		t.Errorf("expected 'Error: question is required', got %q", result)
	}
}

func TestExecuteTool_UnknownTool(t *testing.T) {
	te := &TaskExecutor{}
	result, ok := te.executeTool(context.Background(), "unknown_tool", map[string]any{}, "/tmp/worktree", &types.Task{}, &executionState{}, nil)
	if ok {
		t.Fatalf("expected failure for unknown tool, got success: %s", result)
	}
	if result != "Unknown tool: unknown_tool" {
		t.Errorf("expected 'Unknown tool: unknown_tool', got %q", result)
	}
}

func TestExecuteTool_ReadFile_CallbackError(t *testing.T) {
	te := &TaskExecutor{}

	sandboxRead := func(_ context.Context, path string) ([]byte, error) {
		return nil, fmt.Errorf("file not found in sandbox")
	}

	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{"path": "missing.txt"}, "/tmp/worktree", &types.Task{}, &executionState{sandboxRead: sandboxRead}, nil)
	if ok {
		t.Fatalf("expected failure, got success: %s", result)
	}
	if result != "Error: file not found in sandbox" {
		t.Errorf("unexpected error: %q", result)
	}
}

// --- read_file with offset/limit Tests ---

func TestExecuteTool_ReadFile_OffsetLimit(t *testing.T) {
	dir := t.TempDir()
	content := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10"
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	te := &TaskExecutor{}

	tests := []struct {
		name    string
		args    map[string]any
		want    string
		wantErr bool
	}{
		{
			name:    "no offset/limit returns full file",
			args:    map[string]any{"path": "test.txt"},
			want:    content,
			wantErr: false,
		},
		{
			name:    "offset 1 returns full file",
			args:    map[string]any{"path": "test.txt", "offset": float64(1)},
			want:    content,
			wantErr: false,
		},
		{
			name:    "offset 4 returns lines 4-10",
			args:    map[string]any{"path": "test.txt", "offset": float64(4)},
			want:    "line4\nline5\nline6\nline7\nline8\nline9\nline10",
			wantErr: false,
		},
		{
			name:    "offset 4 limit 3 returns lines 4-6",
			args:    map[string]any{"path": "test.txt", "offset": float64(4), "limit": float64(3)},
			want:    "line4\nline5\nline6",
			wantErr: false,
		},
		{
			name:    "limit 3 returns first 3 lines",
			args:    map[string]any{"path": "test.txt", "limit": float64(3)},
			want:    "line1\nline2\nline3",
			wantErr: false,
		},
		{
			name:    "offset past end returns empty message",
			args:    map[string]any{"path": "test.txt", "offset": float64(100)},
			want:    "(empty — offset past end of file)",
			wantErr: false,
		},
		{
			name:    "limit larger than file returns full file",
			args:    map[string]any{"path": "test.txt", "limit": float64(100)},
			want:    content,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := te.executeTool(context.Background(), "read_file", tt.args, dir, &types.Task{}, &executionState{}, nil)
			if tt.wantErr {
				if ok {
					t.Fatalf("expected failure, got success: %s", result)
				}
			} else {
				if !ok {
					t.Fatalf("expected success, got failure: %s", result)
				}
				if result != tt.want {
					t.Errorf("got %q, want %q", result, tt.want)
				}
			}
		})
	}
}

// --- edit_file Tests ---

func TestExecuteTool_EditFile_SingleEdit(t *testing.T) {
	dir := t.TempDir()
	content := "hello world\nfoo bar\nbaz qux"
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	te := &TaskExecutor{}
	editsJSON, _ := json.Marshal([]map[string]any{
		{"oldText": "hello world", "newText": "goodbye world"},
	})

	result, ok := te.executeTool(context.Background(), "edit_file", map[string]any{
		"path":  "test.txt",
		"edits": json.RawMessage(editsJSON),
	}, dir, &types.Task{}, &executionState{}, nil)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}

	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	want := "goodbye world\nfoo bar\nbaz qux"
	if string(data) != want {
		t.Errorf("got %q, want %q", string(data), want)
	}
}

func TestExecuteTool_EditFile_MultipleEdits(t *testing.T) {
	dir := t.TempDir()
	content := "alpha\nbeta\ngamma\ndelta"
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	te := &TaskExecutor{}
	editsJSON, _ := json.Marshal([]map[string]any{
		{"oldText": "alpha", "newText": "ALPHA"},
		{"oldText": "gamma", "newText": "GAMMA"},
	})

	result, ok := te.executeTool(context.Background(), "edit_file", map[string]any{
		"path":  "test.txt",
		"edits": json.RawMessage(editsJSON),
	}, dir, &types.Task{}, &executionState{}, nil)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}

	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	want := "ALPHA\nbeta\nGAMMA\ndelta"
	if string(data) != want {
		t.Errorf("got %q, want %q", string(data), want)
	}
}

func TestExecuteTool_EditFile_OldTextNotFound(t *testing.T) {
	dir := t.TempDir()
	content := "hello world"
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	te := &TaskExecutor{}
	editsJSON, _ := json.Marshal([]map[string]any{
		{"oldText": "not here", "newText": "replacement"},
	})

	result, ok := te.executeTool(context.Background(), "edit_file", map[string]any{
		"path":  "test.txt",
		"edits": json.RawMessage(editsJSON),
	}, dir, &types.Task{}, &executionState{}, nil)
	if ok {
		t.Fatalf("expected failure, got success: %s", result)
	}
	if !strings.Contains(result, "not found") {
		t.Errorf("expected 'not found' error, got %q", result)
	}
}

func TestExecuteTool_EditFile_DuplicateOldText(t *testing.T) {
	dir := t.TempDir()
	content := "foo\nbar\nfoo\nbaz"
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	te := &TaskExecutor{}
	editsJSON, _ := json.Marshal([]map[string]any{
		{"oldText": "foo", "newText": "BAR"},
	})

	result, ok := te.executeTool(context.Background(), "edit_file", map[string]any{
		"path":  "test.txt",
		"edits": json.RawMessage(editsJSON),
	}, dir, &types.Task{}, &executionState{}, nil)
	if ok {
		t.Fatalf("expected failure for duplicate oldText, got success: %s", result)
	}
	if !strings.Contains(result, "multiple times") {
		t.Errorf("expected 'multiple times' error, got %q", result)
	}
}

func TestExecuteTool_EditFile_OverlappingEdits(t *testing.T) {
	dir := t.TempDir()
	content := "abcdefgh"
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	te := &TaskExecutor{}
	editsJSON, _ := json.Marshal([]map[string]any{
		{"oldText": "abcd", "newText": "ABCD"},
		{"oldText": "cdef", "newText": "CDEF"},
	})

	result, ok := te.executeTool(context.Background(), "edit_file", map[string]any{
		"path":  "test.txt",
		"edits": json.RawMessage(editsJSON),
	}, dir, &types.Task{}, &executionState{}, nil)
	if ok {
		t.Fatalf("expected failure for overlapping edits, got success: %s", result)
	}
	if !strings.Contains(result, "overlap") {
		t.Errorf("expected 'overlap' error, got %q", result)
	}
}

func TestExecuteTool_EditFile_PathEscape(t *testing.T) {
	// Non-sandbox: os.Root blocks path escapes at the OS level
	te := &TaskExecutor{}
	editsJSON, _ := json.Marshal([]map[string]any{
		{"oldText": "x", "newText": "y"},
	})

	result, ok := te.executeTool(context.Background(), "edit_file", map[string]any{
		"path":  "../etc/passwd",
		"edits": json.RawMessage(editsJSON),
	}, "/tmp/worktree", &types.Task{}, &executionState{}, nil)
	if ok {
		t.Fatalf("expected failure for path escape, got success: %s", result)
	}
	if !strings.Contains(result, "Error:") {
		t.Errorf("expected error, got %q", result)
	}
}

func TestExecuteTool_EditFile_EmptyOldText(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("content"), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	te := &TaskExecutor{}
	editsJSON, _ := json.Marshal([]map[string]any{
		{"oldText": "", "newText": "replacement"},
	})

	result, ok := te.executeTool(context.Background(), "edit_file", map[string]any{
		"path":  "test.txt",
		"edits": json.RawMessage(editsJSON),
	}, dir, &types.Task{}, &executionState{}, nil)
	if ok {
		t.Fatalf("expected failure for empty oldText, got success: %s", result)
	}
	if !strings.Contains(result, "empty") {
		t.Errorf("expected 'empty' error, got %q", result)
	}
}

func TestExecuteTool_EditFile_NoEdits(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("content"), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	te := &TaskExecutor{}
	editsJSON, _ := json.Marshal([]map[string]any{})

	result, ok := te.executeTool(context.Background(), "edit_file", map[string]any{
		"path":  "test.txt",
		"edits": json.RawMessage(editsJSON),
	}, dir, &types.Task{}, &executionState{}, nil)
	if ok {
		t.Fatalf("expected failure for empty edits, got success: %s", result)
	}
	if !strings.Contains(result, "no edits") {
		t.Errorf("expected 'no edits' error, got %q", result)
	}
}

func TestExecuteTool_EditFile_Sandboxed(t *testing.T) {
	dir := t.TempDir()
	content := "original content"
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	te := &TaskExecutor{}
	var writtenPath, writtenContent string

	sandboxRead := func(_ context.Context, path string) ([]byte, error) {
		return os.ReadFile(filepath.Join(dir, path))
	}
	sandboxWrite := func(_ context.Context, path, c string) error {
		writtenPath = path
		writtenContent = c
		fullPath := filepath.Join(dir, path)
		os.MkdirAll(filepath.Dir(fullPath), 0755)
		return os.WriteFile(fullPath, []byte(c), 0644)
	}

	editsJSON, _ := json.Marshal([]map[string]any{
		{"oldText": "original", "newText": "modified"},
	})

	result, ok := te.executeTool(context.Background(), "edit_file", map[string]any{
		"path":  "test.txt",
		"edits": json.RawMessage(editsJSON),
	}, dir, &types.Task{}, &executionState{sandboxRead: sandboxRead, sandboxWrite: sandboxWrite}, nil)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}
	if writtenPath != "test.txt" {
		t.Errorf("expected path 'test.txt', got %q", writtenPath)
	}
	if writtenContent != "modified content" {
		t.Errorf("expected content 'modified content', got %q", writtenContent)
	}

	// Verify file was actually written
	data, err := os.ReadFile(filepath.Join(dir, "test.txt"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "modified content" {
		t.Errorf("file content = %q, want 'modified content'", string(data))
	}
}

func TestExecuteTool_EditFile_MissingFile(t *testing.T) {
	te := &TaskExecutor{}
	editsJSON, _ := json.Marshal([]map[string]any{
		{"oldText": "x", "newText": "y"},
	})

	result, ok := te.executeTool(context.Background(), "edit_file", map[string]any{
		"path":  "nonexistent.txt",
		"edits": json.RawMessage(editsJSON),
	}, "/tmp/worktree", &types.Task{}, &executionState{}, nil)
	if ok {
		t.Fatalf("expected failure for missing file, got success: %s", result)
	}
	if !strings.Contains(result, "Error:") {
		t.Errorf("expected error, got %q", result)
	}
}

// --- bash Tests ---

func TestExecuteTool_Bash_Direct(t *testing.T) {
	dir := t.TempDir()
	te := &TaskExecutor{}

	result, ok := te.executeTool(context.Background(), "bash", map[string]any{
		"command": "echo hello",
		"timeout": float64(5),
	}, dir, &types.Task{}, &executionState{}, nil)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}
	if !strings.Contains(result, "hello") {
		t.Errorf("expected 'hello' in output, got %q", result)
	}
}

func TestExecuteTool_Bash_ANSIStripping(t *testing.T) {
	dir := t.TempDir()
	te := &TaskExecutor{}

	// printf with ANSI color codes (bold, foreground, background, reset)
	result, ok := te.executeTool(context.Background(), "bash", map[string]any{
		"command": "printf '\x1b[1m\x1b[30m\x1b[46m RUN \x1b[49m\x1b[39m\x1b[22m \x1b[36mv4.1.10\n'",
		"timeout": float64(5),
	}, dir, &types.Task{}, &executionState{}, nil)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}
	// ANSI codes should be stripped
	if strings.Contains(result, "\x1b") {
		t.Errorf("expected no ANSI escape codes in output, got %q", result)
	}
	if !strings.Contains(result, "RUN") {
		t.Errorf("expected 'RUN' in output, got %q", result)
	}
	if !strings.Contains(result, "v4.1.10") {
		t.Errorf("expected 'v4.1.10' in output, got %q", result)
	}
}

func TestExecuteTool_Bash_FailedWithANSI(t *testing.T) {
	dir := t.TempDir()
	te := &TaskExecutor{}

	// Command that fails and outputs ANSI codes (use \\x1b so bash printf interprets them)
	result, ok := te.executeTool(context.Background(), "bash", map[string]any{
		"command": "printf '\\x1b[31mERROR\\x1b[0m something failed\\n'; exit 1",
		"timeout": float64(5),
	}, dir, &types.Task{}, &executionState{}, nil)
	if ok {
		t.Fatalf("expected failure, got success: %s", result)
	}
	// Even in error output, ANSI should be stripped
	if strings.Contains(result, "\x1b") {
		t.Errorf("expected no ANSI escape codes in error output, got %q", result)
	}
	if !strings.Contains(result, "ERROR") {
		t.Errorf("expected 'ERROR' in output, got %q", result)
	}
}

// --- stripANSI Tests ---

func TestStripANSI_Empty(t *testing.T) {
	got := stripANSI("")
	if got != "" {
		t.Errorf("stripANSI(\"\") = %q, want \"\"", got)
	}
}

func TestStripANSI_NoANSI(t *testing.T) {
	got := stripANSI("hello world\nline two")
	if got != "hello world\nline two" {
		t.Errorf("stripANSI() = %q, want unchanged", got)
	}
}

func TestStripANSI_BoldAndColor(t *testing.T) {
	// \x1b[1m = bold, \x1b[31m = red, \x1b[0m = reset
	got := stripANSI("\x1b[1m\x1b[31mERROR\x1b[0m")
	want := "ERROR"
	if got != want {
		t.Errorf("stripANSI() = %q, want %q", got, want)
	}
}

func TestStripANSI_VitestStyle(t *testing.T) {
	// Real-world example: \x1b[1m\x1b[30m\x1b[46m RUN \x1b[49m\x1b[39m\x1b[22m \x1b[36mv4.1.10
	got := stripANSI("\x1b[1m\x1b[30m\x1b[46m RUN \x1b[49m\x1b[39m\x1b[22m \x1b[36mv4.1.10")
	want := " RUN  v4.1.10"
	if got != want {
		t.Errorf("stripANSI() = %q, want %q", got, want)
	}
}

func TestStripANSI_MultipleLines(t *testing.T) {
	got := stripANSI("\x1b[32mPASS\x1b[0m test1\n\x1b[31mFAIL\x1b[0m test2\nplain line")
	want := "PASS test1\nFAIL test2\nplain line"
	if got != want {
		t.Errorf("stripANSI() = %q, want %q", got, want)
	}
}

func TestStripANSI_OSCSequence(t *testing.T) {
	// OSC sequence: \x1b]0;Terminal Title\x07
	got := stripANSI("\x1b]0;My Title\x07hello")
	want := "hello"
	if got != want {
		t.Errorf("stripANSI() = %q, want %q", got, want)
	}
}

func TestStripANSI_OSCWithST(t *testing.T) {
	// OSC with \x1b\\ string terminator
	got := stripANSI("\x1b]0;Title\x1b\\hello")
	want := "hello"
	if got != want {
		t.Errorf("stripANSI() = %q, want %q", got, want)
	}
}

func TestStripANSI_SingleByteEscape(t *testing.T) {
	// \x1bH = home cursor, \x1bM = reverse index
	got := stripANSI("\x1bH\x1bMstart")
	want := "start"
	if got != want {
		t.Errorf("stripANSI() = %q, want %q", got, want)
	}
}

func TestStripANSI_TrailingEscape(t *testing.T) {
	// Escape sequence at end of string with no final byte
	got := stripANSI("hello\x1b[31")
	want := "hello"
	if got != want {
		t.Errorf("stripANSI() = %q, want %q", got, want)
	}
}

func TestStripANSI_BareEscape(t *testing.T) {
	// Just an ESC character at end
	got := stripANSI("hello\x1b")
	want := "hello"
	if got != want {
		t.Errorf("stripANSI() = %q, want %q", got, want)
	}
}

func TestStripANSI_CursorMovement(t *testing.T) {
	// \x1b[2A = cursor up 2, \x1b[10C = cursor forward 10
	got := stripANSI("\x1b[2Aline1\x1b[10Cline2")
	want := "line1line2"
	if got != want {
		t.Errorf("stripANSI() = %q, want %q", got, want)
	}
}

func TestStripANSI_MixedContent(t *testing.T) {
	got := stripANSI("\x1b[1;34mnpm\x1b[0m run \x1b[1mtest\x1b[0m\n\x1b[32m✓\x1b[0m passed\n\x1b[33m⚠\x1b[0m warning")
	want := "npm run test\n✓ passed\n⚠ warning"
	if got != want {
		t.Errorf("stripANSI() = %q, want %q", got, want)
	}
}

func TestStripANSI_PreserveSpecialChars(t *testing.T) {
	// Non-ANSI special characters should be preserved
	got := stripANSI("tab\there\nnewline\r\r\nunicode: \u00e9\u00e8\u00ea")
	want := "tab\there\nnewline\r\r\nunicode: \u00e9\u00e8\u00ea"
	if got != want {
		t.Errorf("stripANSI() = %q, want %q", got, want)
	}
}

func TestStripANSI_ComplexCSI(t *testing.T) {
	// CSI with intermediate bytes: \x1b[38;5;196m (256-color red)
	got := stripANSI("\x1b[38;5;196mred\x1b[0m")
	want := "red"
	if got != want {
		t.Errorf("stripANSI() = %q, want %q", got, want)
	}
}

func TestStripANSI_24BitColor(t *testing.T) {
	// \x1b[38;2;255;128;0m (24-bit RGB)
	got := stripANSI("\x1b[38;2;255;128;0morange\x1b[0m")
	want := "orange"
	if got != want {
		t.Errorf("stripANSI() = %q, want %q", got, want)
	}
}

func TestStripANSI_BackgroundColor(t *testing.T) {
	// \x1b[48;5;27m (256-color background)
	got := stripANSI("\x1b[48;5;27mhighlighted\x1b[0m")
	want := "highlighted"
	if got != want {
		t.Errorf("stripANSI() = %q, want %q", got, want)
	}
}

func TestStripANSI_EraseLine(t *testing.T) {
	// \x1b[2K = erase entire line
	got := stripANSI("\x1b[2Kprogress: 50%")
	want := "progress: 50%"
	if got != want {
		t.Errorf("stripANSI() = %q, want %q", got, want)
	}
}

func TestStripANSI_CarriageReturn(t *testing.T) {
	// ANSI codes mixed with carriage returns (common in progress bars)
	got := stripANSI("\x1b[2K\r\x1b[32m✓\x1b[0m done")
	want := "\r✓ done"
	if got != want {
		t.Errorf("stripANSI() = %q, want %q", got, want)
	}
}

func TestExecuteTool_WriteFile_CallbackError(t *testing.T) {
	te := &TaskExecutor{}

	sandboxWrite := func(_ context.Context, path, content string) error {
		return fmt.Errorf("permission denied")
	}

	result, ok := te.executeTool(context.Background(), "write_file", map[string]any{
		"path":    "readonly.txt",
		"content": "data",
	}, "/tmp/worktree", &types.Task{}, &executionState{sandboxWrite: sandboxWrite}, nil)
	if ok {
		t.Fatalf("expected failure, got success: %s", result)
	}
	if result != "Error: permission denied" {
		t.Errorf("unexpected error: %q", result)
	}
}

// --- Bridge recovery tests ---
// These test the recoverBridge callback and retry logic in executeTool.
// Callbacks return bridge errors, recoverBridge refreshes them, and the retry succeeds.

func TestExecuteTool_ReadFile_BridgeRecovery(t *testing.T) {
	te := &TaskExecutor{}
	dir := t.TempDir()
	testFile := filepath.Join(dir, "hello.txt")
	expectedContent := "hello from sandbox"
	if err := os.WriteFile(testFile, []byte(expectedContent), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	// First read fails with bridge error, recovery refreshes the callback
	readAttempts := 0
	sandboxRead := func(_ context.Context, path string) ([]byte, error) {
		readAttempts++
		if readAttempts == 1 {
			return nil, fmt.Errorf("broken pipe")
		}
		return os.ReadFile(filepath.Join(dir, path))
	}

	recoveryCalled := false
	state := &executionState{sandboxRead: sandboxRead}
	state.recoverBridge = func() bool {
		recoveryCalled = true
		state.sandboxRead = func(_ context.Context, path string) ([]byte, error) {
			return os.ReadFile(filepath.Join(dir, path))
		}
		return true
	}

	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{"path": "hello.txt"}, dir, &types.Task{}, state, nil)
	if !ok {
		t.Fatalf("expected success after recovery, got failure: %s", result)
	}
	if !recoveryCalled {
		t.Fatal("expected recoverBridge to be called")
	}
	if result != expectedContent {
		t.Errorf("unexpected content: %q, want %q", result, expectedContent)
	}
}

func TestExecuteTool_ReadFile_NoRecoveryOnNonBridgeError(t *testing.T) {
	te := &TaskExecutor{}

	recoveryCalled := false
	state := &executionState{
		sandboxRead: func(_ context.Context, path string) ([]byte, error) {
		return nil, fmt.Errorf("file not found")
	},
		recoverBridge: func() bool {
			recoveryCalled = true
			return true
		},
	}

	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{"path": "missing.txt"}, "/tmp/worktree", &types.Task{}, state, nil)
	if ok {
		t.Fatalf("expected failure, got success: %s", result)
	}
	if recoveryCalled {
		t.Fatal("recoverBridge should not be called for non-bridge errors")
	}
	if result != "Error: file not found" {
		t.Errorf("unexpected error: %q", result)
	}
}

func TestExecuteTool_WriteFile_BridgeRecovery(t *testing.T) {
	te := &TaskExecutor{}
	dir := t.TempDir()

	writeAttempts := 0
	sandboxWrite := func(_ context.Context, path, content string) error {
		writeAttempts++
		if writeAttempts == 1 {
			return fmt.Errorf("broken pipe")
		}
		return os.WriteFile(filepath.Join(dir, path), []byte(content), 0644)
	}

	recoveryCalled := false
	state := &executionState{sandboxWrite: sandboxWrite}
	state.recoverBridge = func() bool {
		recoveryCalled = true
		state.sandboxWrite = func(_ context.Context, path, content string) error {
			return os.WriteFile(filepath.Join(dir, path), []byte(content), 0644)
		}
		return true
	}

	result, ok := te.executeTool(context.Background(), "write_file", map[string]any{
		"path":    "test.txt",
		"content": "hello",
	}, dir, &types.Task{}, state, nil)
	if !ok {
		t.Fatalf("expected success after recovery, got failure: %s", result)
	}
	if !recoveryCalled {
		t.Fatal("expected recoverBridge to be called")
	}
	if !strings.Contains(result, "Wrote") {
		t.Errorf("unexpected result: %q", result)
	}
	// Verify file was actually written
	data, err := os.ReadFile(filepath.Join(dir, "test.txt"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("file content = %q, want %q", string(data), "hello")
	}
}

func TestExecuteTool_Bash_BridgeRecovery(t *testing.T) {
	te := &TaskExecutor{}

	execAttempts := 0
	sandboxExec := func(_ context.Context, command string, timeout int64) (string, error) {
		execAttempts++
		if execAttempts == 1 {
			return "", fmt.Errorf("bash: send bash request: write header: write |1: broken pipe")
		}
		return "echo output", nil
	}

	recoveryCalled := false
	state := &executionState{sandboxExec: sandboxExec}
	state.recoverBridge = func() bool {
		recoveryCalled = true
		state.sandboxExec = func(_ context.Context, command string, timeout int64) (string, error) {
			return "echo output", nil
		}
		return true
	}

	result, ok := te.executeTool(context.Background(), "bash", map[string]any{
		"command": "echo hello",
	}, "/tmp/worktree", &types.Task{}, state, nil)
	if !ok {
		t.Fatalf("expected success after recovery, got failure: %s", result)
	}
	if !recoveryCalled {
		t.Fatal("expected recoverBridge to be called")
	}
	if result != "echo output" {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestExecuteTool_Bash_BridgeRecoveryStreaming(t *testing.T) {
	te := &TaskExecutor{}

	execAttempts := 0
	sandboxExecStream := func(_ context.Context, command string, timeout int64, cb func(string)) (string, error) {
		execAttempts++
		if execAttempts == 1 {
			return "", fmt.Errorf("bash: send bash request: write header: write |1: broken pipe")
		}
		if cb != nil {
			cb("streamed line\n")
		}
		return "streamed line\n", nil
	}

	recoveryCalled := false
	state := &executionState{sandboxExecStream: sandboxExecStream}
	state.recoverBridge = func() bool {
		recoveryCalled = true
		state.sandboxExecStream = func(_ context.Context, command string, timeout int64, cb func(string)) (string, error) {
			if cb != nil {
				cb("streamed line\n")
			}
			return "streamed line\n", nil
		}
		return true
	}

	var streamedOutput []string
	result, ok := te.executeTool(context.Background(), "bash", map[string]any{
		"command": "echo hello",
	}, "/tmp/worktree", &types.Task{}, state, func(id, text string) {
		streamedOutput = append(streamedOutput, text)
	})
	if !ok {
		t.Fatalf("expected success after recovery, got failure: %s", result)
	}
	if !recoveryCalled {
		t.Fatal("expected recoverBridge to be called")
	}
	if result != "streamed line\n" {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestExecuteTool_Bash_NoRecoveryOnNonBridgeError(t *testing.T) {
	te := &TaskExecutor{}

	execAttempts := 0
	sandboxExec := func(_ context.Context, command string, timeout int64) (string, error) {
		execAttempts++
		return "", fmt.Errorf("exit code 1")
	}

	recoveryCalled := false
	state := &executionState{
		sandboxExec: sandboxExec,
		recoverBridge: func() bool {
			recoveryCalled = true
			return true
		},
	}

	result, ok := te.executeTool(context.Background(), "bash", map[string]any{
		"command": "false",
	}, "/tmp/worktree", &types.Task{}, state, nil)
	if ok {
		t.Fatalf("expected failure, got success: %s", result)
	}
	if recoveryCalled {
		t.Fatal("recoverBridge should not be called for non-bridge errors")
	}
	if execAttempts != 1 {
		t.Errorf("expected 1 exec attempt, got %d", execAttempts)
	}
	if !strings.Contains(result, "exit code 1") {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestExecuteTool_EditFile_BridgeRecovery(t *testing.T) {
	te := &TaskExecutor{}
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	originalContent := "line one\nline two\nline three"
	if err := os.WriteFile(testFile, []byte(originalContent), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	readAttempts := 0
	sandboxRead := func(_ context.Context, path string) ([]byte, error) {
		readAttempts++
		if readAttempts == 1 {
			return nil, fmt.Errorf("broken pipe")
		}
		return os.ReadFile(filepath.Join(dir, path))
	}
	sandboxWrite := func(_ context.Context, path, content string) error {
		return os.WriteFile(filepath.Join(dir, path), []byte(content), 0644)
	}

	recoveryCalled := false
	state := &executionState{
		sandboxRead:  sandboxRead,
		sandboxWrite: sandboxWrite,
		projectName:  "test",
	}
	state.recoverBridge = func() bool {
		recoveryCalled = true
		state.sandboxRead = func(_ context.Context, path string) ([]byte, error) {
			return os.ReadFile(filepath.Join(dir, path))
		}
		state.sandboxWrite = func(_ context.Context, path, content string) error {
			return os.WriteFile(filepath.Join(dir, path), []byte(content), 0644)
		}
		return true
	}

	edits, _ := json.Marshal([]map[string]any{{"oldText": "line two", "newText": "modified two"}})
	result, ok := te.executeTool(context.Background(), "edit_file", map[string]any{
		"path":  "test.txt",
		"edits": json.RawMessage(edits),
	}, dir, &types.Task{}, state, nil)
	if !ok {
		t.Fatalf("expected success after recovery, got failure: %s", result)
	}
	if !recoveryCalled {
		t.Fatal("expected recoverBridge to be called")
	}
	if !strings.Contains(result, "Edited") {
		t.Errorf("unexpected result: %q", result)
	}
	// Verify file was actually edited
	data, err := os.ReadFile(filepath.Join(dir, "test.txt"))
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	if !strings.Contains(string(data), "modified two") {
		t.Errorf("file content = %q, want to contain %q", string(data), "modified two")
	}
}

func TestExecuteTool_EditFile_WriteBridgeRecovery(t *testing.T) {
	te := &TaskExecutor{}
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	originalContent := "line one\nline two\nline three"
	if err := os.WriteFile(testFile, []byte(originalContent), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	sandboxRead := func(_ context.Context, path string) ([]byte, error) {
		return os.ReadFile(filepath.Join(dir, path))
	}
	writeAttempts := 0
	sandboxWrite := func(_ context.Context, path, content string) error {
		writeAttempts++
		if writeAttempts == 1 {
			return fmt.Errorf("broken pipe")
		}
		return os.WriteFile(filepath.Join(dir, path), []byte(content), 0644)
	}

	recoveryCalled := false
	state := &executionState{
		sandboxRead:  sandboxRead,
		sandboxWrite: sandboxWrite,
		projectName:  "test",
	}
	state.recoverBridge = func() bool {
		recoveryCalled = true
		// Reuse the same callback — it will succeed on 2nd call (writeAttempts == 2)
		state.sandboxWrite = sandboxWrite
		return true
	}

	edits, _ := json.Marshal([]map[string]any{{"oldText": "line two", "newText": "modified two"}})
	result, ok := te.executeTool(context.Background(), "edit_file", map[string]any{
		"path":  "test.txt",
		"edits": json.RawMessage(edits),
	}, dir, &types.Task{}, state, nil)
	if !ok {
		t.Fatalf("expected success after recovery, got failure: %s", result)
	}
	if !recoveryCalled {
		t.Fatal("expected recoverBridge to be called for write failure")
	}
	if writeAttempts != 2 {
		t.Errorf("expected 2 write attempts, got %d", writeAttempts)
	}
	if !strings.Contains(result, "Edited") {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestExecuteTool_RecoveryReturnsNil(t *testing.T) {
	// When recoverBridge returns nil (recovery failed), the error propagates
	te := &TaskExecutor{}

	state := &executionState{
		sandboxRead: func(_ context.Context, path string) ([]byte, error) {
		return nil, fmt.Errorf("broken pipe")
	},
		recoverBridge: func() bool {
			return false // recovery failed
		},
	}

	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{"path": "test.txt"}, "/tmp/worktree", &types.Task{}, state, nil)
	if ok {
		t.Fatalf("expected failure when recovery returns nil, got success: %s", result)
	}
	if result != "Error: broken pipe" {
		t.Errorf("unexpected error: %q", result)
	}
}

func TestExecuteTool_ReadFile_ClientClosedRecovery(t *testing.T) {
	// "client closed" error (from Client.Close()) should trigger bridge recovery.
	// This happens when the sandbox is recreated (e.g., operating mode change)
	// and a tool call hits the old, closed client.
	te := &TaskExecutor{}
	dir := t.TempDir()
	testFile := filepath.Join(dir, "hello.txt")
	expectedContent := "hello from sandbox"
	if err := os.WriteFile(testFile, []byte(expectedContent), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	readAttempts := 0
	sandboxRead := func(_ context.Context, path string) ([]byte, error) {
		readAttempts++
		if readAttempts == 1 {
			return nil, fmt.Errorf("read: client closed")
		}
		return os.ReadFile(filepath.Join(dir, path))
	}

	recoveryCalled := false
	state := &executionState{sandboxRead: sandboxRead}
	state.recoverBridge = func() bool {
		recoveryCalled = true
		state.sandboxRead = func(_ context.Context, path string) ([]byte, error) {
			return os.ReadFile(filepath.Join(dir, path))
		}
		return true
	}

	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{"path": "hello.txt"}, dir, &types.Task{}, state, nil)
	if !ok {
		t.Fatalf("expected success after recovery, got failure: %s", result)
	}
	if !recoveryCalled {
		t.Fatal("expected recoverBridge to be called for 'client closed' error")
	}
	if result != expectedContent {
		t.Errorf("unexpected content: %q, want %q", result, expectedContent)
	}
}

func TestExecuteTool_ReadFile_ConnectionClosedRecovery(t *testing.T) {
	// "connection closed" error (from readLoop cleanup) should trigger bridge recovery.
	// This happens when the bridge readLoop exits and delivers error frames to pending requests.
	te := &TaskExecutor{}
	dir := t.TempDir()
	testFile := filepath.Join(dir, "hello.txt")
	expectedContent := "hello from sandbox"
	if err := os.WriteFile(testFile, []byte(expectedContent), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	readAttempts := 0
	sandboxRead := func(_ context.Context, path string) ([]byte, error) {
		readAttempts++
		if readAttempts == 1 {
			return nil, fmt.Errorf("read: connection closed")
		}
		return os.ReadFile(filepath.Join(dir, path))
	}

	recoveryCalled := false
	state := &executionState{sandboxRead: sandboxRead}
	state.recoverBridge = func() bool {
		recoveryCalled = true
		state.sandboxRead = func(_ context.Context, path string) ([]byte, error) {
			return os.ReadFile(filepath.Join(dir, path))
		}
		return true
	}

	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{"path": "hello.txt"}, dir, &types.Task{}, state, nil)
	if !ok {
		t.Fatalf("expected success after recovery, got failure: %s", result)
	}
	if !recoveryCalled {
		t.Fatal("expected recoverBridge to be called for 'connection closed' error")
	}
	if result != expectedContent {
		t.Errorf("unexpected content: %q, want %q", result, expectedContent)
	}
}

func TestExecuteTool_Bash_ClientClosedRecovery(t *testing.T) {
	// "client closed" error from bash tool should also trigger recovery.
	te := &TaskExecutor{}

	execAttempts := 0
	sandboxExec := func(_ context.Context, command string, timeout int64) (string, error) {
		execAttempts++
		if execAttempts == 1 {
			return "", fmt.Errorf("bash: client closed")
		}
		return "echo output", nil
	}

	recoveryCalled := false
	state := &executionState{sandboxExec: sandboxExec}
	state.recoverBridge = func() bool {
		recoveryCalled = true
		state.sandboxExec = func(_ context.Context, command string, timeout int64) (string, error) {
			return "echo output", nil
		}
		return true
	}

	result, ok := te.executeTool(context.Background(), "bash", map[string]any{
		"command": "echo hello",
	}, "/tmp/worktree", &types.Task{}, state, nil)
	if !ok {
		t.Fatalf("expected success after recovery, got failure: %s", result)
	}
	if !recoveryCalled {
		t.Fatal("expected recoverBridge to be called for 'client closed' error in bash")
	}
	if result != "echo output" {
		t.Errorf("unexpected result: %q", result)
	}
}
