package exec

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"kanban/backend/types"
)

// --- shellQuote Tests ---

func TestShellQuote_Basic(t *testing.T) {
	got := shellQuote("hello.txt")
	want := "'hello.txt'"
	if got != want {
		t.Errorf("shellQuote(%q) = %q, want %q", "hello.txt", got, want)
	}
}

func TestShellQuote_WithSingleQuote(t *testing.T) {
	got := shellQuote("it's.txt")
	want := "'it'\\''s.txt'"
	if got != want {
		t.Errorf("shellQuote(%q) = %q, want %q", "it's.txt", got, want)
	}
}

func TestShellQuote_WithSpaces(t *testing.T) {
	got := shellQuote("my file.txt")
	want := "'my file.txt'"
	if got != want {
		t.Errorf("shellQuote(%q) = %q, want %q", "my file.txt", got, want)
	}
}

func TestShellQuote_WithPath(t *testing.T) {
	got := shellQuote("src/main/file.go")
	want := "'src/main/file.go'"
	if got != want {
		t.Errorf("shellQuote(%q) = %q, want %q", "src/main/file.go", got, want)
	}
}

func TestShellQuote_Empty(t *testing.T) {
	got := shellQuote("")
	want := "''"
	if got != want {
		t.Errorf("shellQuote(%q) = %q, want %q", "", got, want)
	}
}

// --- isSandboxableTool Tests ---

func TestIsSandboxableTool(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"read_file", true},
		{"write_file", true},
		{"execute_command", true},
		{"task_complete", false},
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
	readFn := func(path string) ([]byte, error) {
		// In real sandbox, path is relative to worktree
		return os.ReadFile(filepath.Join(dir, path))
	}

	result, ok := te.executeTool("read_file", map[string]interface{}{"path": "hello.txt"}, dir, &types.Task{}, nil, readFn, nil)
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

	// No readFn — should fall back to host
	result, ok := te.executeTool("read_file", map[string]interface{}{"path": "host.txt"}, dir, &types.Task{}, nil, nil, nil)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}
	if result != expectedContent {
		t.Errorf("expected %q, got %q", expectedContent, result)
	}
}

func TestExecuteTool_ReadFile_PathEscape(t *testing.T) {
	te := &TaskExecutor{}

	// Path escape should be rejected even with sandbox callback
	readFn := func(path string) ([]byte, error) {
		return []byte("should not reach here"), nil
	}

	result, ok := te.executeTool("read_file", map[string]interface{}{"path": "../etc/passwd"}, "/tmp/worktree", &types.Task{}, nil, readFn, nil)
	if ok {
		t.Fatalf("expected failure for path escape, got success: %s", result)
	}
	if result != "Error: path escape" {
		t.Errorf("expected 'Error: path escape', got %q", result)
	}
}

func TestExecuteTool_WriteFile_Sandboxed(t *testing.T) {
	dir := t.TempDir()
	te := &TaskExecutor{}
	var writtenPath, writtenContent string

	// Simulate sandbox write callback
	writeFn := func(path, content string) error {
		writtenPath = path
		writtenContent = content
		fullPath := filepath.Join(dir, path)
		os.MkdirAll(filepath.Dir(fullPath), 0755)
		return os.WriteFile(fullPath, []byte(content), 0644)
	}

	result, ok := te.executeTool("write_file", map[string]interface{}{
		"path":    "test.txt",
		"content": "hello sandbox",
	}, dir, &types.Task{}, nil, nil, writeFn)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}
	if result != "Wrote 13 bytes" {
		t.Errorf("expected 'Wrote 13 bytes', got %q", result)
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

	// No writeFn — should fall back to host
	result, ok := te.executeTool("write_file", map[string]interface{}{
		"path":    "host.txt",
		"content": "hello host",
	}, dir, &types.Task{}, nil, nil, nil)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}
	if result != "Wrote 10 bytes" {
		t.Errorf("expected 'Wrote 10 bytes', got %q", result)
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
	te := &TaskExecutor{}

	writeFn := func(path, content string) error {
		return nil // should not reach here
	}

	result, ok := te.executeTool("write_file", map[string]interface{}{
		"path":    "../etc/evil",
		"content": "malicious",
	}, "/tmp/worktree", &types.Task{}, nil, nil, writeFn)
	if ok {
		t.Fatalf("expected failure for path escape, got success: %s", result)
	}
	if result != "Error: path escape" {
		t.Errorf("expected 'Error: path escape', got %q", result)
	}
}

func TestExecuteTool_WriteFile_DirCreation(t *testing.T) {
	dir := t.TempDir()
	te := &TaskExecutor{}

	writeFn := func(path, content string) error {
		fullPath := filepath.Join(dir, path)
		os.MkdirAll(filepath.Dir(fullPath), 0755)
		return os.WriteFile(fullPath, []byte(content), 0644)
	}

	result, ok := te.executeTool("write_file", map[string]interface{}{
		"path":    "deep/nested/dir/file.txt",
		"content": "nested content",
	}, dir, &types.Task{}, nil, nil, writeFn)
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
	result, ok := te.executeTool("task_complete", map[string]interface{}{}, "/tmp/worktree", &types.Task{}, nil, nil, nil)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}
	if result != "Task complete" {
		t.Errorf("expected 'Task complete', got %q", result)
	}
}

func TestExecuteTool_UnknownTool(t *testing.T) {
	te := &TaskExecutor{}
	result, ok := te.executeTool("unknown_tool", map[string]interface{}{}, "/tmp/worktree", &types.Task{}, nil, nil, nil)
	if ok {
		t.Fatalf("expected failure for unknown tool, got success: %s", result)
	}
	if result != "Unknown tool: unknown_tool" {
		t.Errorf("expected 'Unknown tool: unknown_tool', got %q", result)
	}
}

func TestExecuteTool_ReadFile_CallbackError(t *testing.T) {
	te := &TaskExecutor{}

	readFn := func(path string) ([]byte, error) {
		return nil, fmt.Errorf("file not found in sandbox")
	}

	result, ok := te.executeTool("read_file", map[string]interface{}{"path": "missing.txt"}, "/tmp/worktree", &types.Task{}, nil, readFn, nil)
	if ok {
		t.Fatalf("expected failure, got success: %s", result)
	}
	if result != "Error: file not found in sandbox" {
		t.Errorf("unexpected error: %q", result)
	}
}

// --- validateWorktreePath Tests ---

func TestValidateWorktreePath_Valid(t *testing.T) {
	tests := []struct {
		worktree string
		path     string
		want     string
	}{
		{"/tmp/worktree", "src/main.go", "/tmp/worktree/src/main.go"},
		{"/tmp/worktree", "file.txt", "/tmp/worktree/file.txt"},
		{"/tmp/worktree", "deep/nested/file.go", "/tmp/worktree/deep/nested/file.go"},
		{"/tmp/worktree", ".", "/tmp/worktree"},
	}
	for _, tt := range tests {
		got := validateWorktreePath(tt.worktree, tt.path)
		if got != tt.want {
			t.Errorf("validateWorktreePath(%q, %q) = %q, want %q", tt.worktree, tt.path, got, tt.want)
		}
	}
}

func TestValidateWorktreePath_Escapes(t *testing.T) {
	escapes := []string{
		"../etc/passwd",
		"../../etc/passwd",
		"foo/../../etc/passwd",
		"..",
		"/etc/passwd",       // absolute path
		"/tmp/other/file.go", // absolute path outside worktree
	}
	for _, path := range escapes {
		got := validateWorktreePath("/tmp/worktree", path)
		if got != "" {
			t.Errorf("validateWorktreePath(%q, %q) = %q, want empty (escape)", "/tmp/worktree", path, got)
		}
	}
}

func TestExecuteTool_WriteFile_CallbackError(t *testing.T) {
	te := &TaskExecutor{}

	writeFn := func(path, content string) error {
		return fmt.Errorf("permission denied")
	}

	result, ok := te.executeTool("write_file", map[string]interface{}{
		"path":    "readonly.txt",
		"content": "data",
	}, "/tmp/worktree", &types.Task{}, nil, nil, writeFn)
	if ok {
		t.Fatalf("expected failure, got success: %s", result)
	}
	if result != "Error: permission denied" {
		t.Errorf("unexpected error: %q", result)
	}
}
