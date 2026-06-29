package exec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		{"edit_file", true},
		{"bash", true},
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

	result, ok := te.executeTool("read_file", map[string]any{"path": "hello.txt"}, dir, &types.Task{}, nil, nil, readFn, nil)
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
	result, ok := te.executeTool("read_file", map[string]any{"path": "host.txt"}, dir, &types.Task{}, nil, nil, nil, nil)
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

	result, ok := te.executeTool("read_file", map[string]any{"path": "../etc/passwd"}, "/tmp/worktree", &types.Task{}, nil, nil, readFn, nil)
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

	result, ok := te.executeTool("write_file", map[string]any{
		"path":    "test.txt",
		"content": "hello sandbox",
	}, dir, &types.Task{}, nil, nil, nil, writeFn)
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
	result, ok := te.executeTool("write_file", map[string]any{
		"path":    "host.txt",
		"content": "hello host",
	}, dir, &types.Task{}, nil, nil, nil, nil)
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

	result, ok := te.executeTool("write_file", map[string]any{
		"path":    "../etc/evil",
		"content": "malicious",
	}, "/tmp/worktree", &types.Task{}, nil, nil, nil, writeFn)
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

	result, ok := te.executeTool("write_file", map[string]any{
		"path":    "deep/nested/dir/file.txt",
		"content": "nested content",
	}, dir, &types.Task{}, nil, nil, nil, writeFn)
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
	result, ok := te.executeTool("task_complete", map[string]any{}, "/tmp/worktree", &types.Task{}, nil, nil, nil, nil)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}
	if result != "Task complete" {
		t.Errorf("expected 'Task complete', got %q", result)
	}
}

func TestExecuteTool_UnknownTool(t *testing.T) {
	te := &TaskExecutor{}
	result, ok := te.executeTool("unknown_tool", map[string]any{}, "/tmp/worktree", &types.Task{}, nil, nil, nil, nil)
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

	result, ok := te.executeTool("read_file", map[string]any{"path": "missing.txt"}, "/tmp/worktree", &types.Task{}, nil, nil, readFn, nil)
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
			result, ok := te.executeTool("read_file", tt.args, dir, &types.Task{}, nil, nil, nil, nil)
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

	result, ok := te.executeTool("edit_file", map[string]any{
		"path":  "test.txt",
		"edits": json.RawMessage(editsJSON),
	}, dir, &types.Task{}, nil, nil, nil, nil)
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

	result, ok := te.executeTool("edit_file", map[string]any{
		"path":  "test.txt",
		"edits": json.RawMessage(editsJSON),
	}, dir, &types.Task{}, nil, nil, nil, nil)
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

	result, ok := te.executeTool("edit_file", map[string]any{
		"path":  "test.txt",
		"edits": json.RawMessage(editsJSON),
	}, dir, &types.Task{}, nil, nil, nil, nil)
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

	result, ok := te.executeTool("edit_file", map[string]any{
		"path":  "test.txt",
		"edits": json.RawMessage(editsJSON),
	}, dir, &types.Task{}, nil, nil, nil, nil)
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

	result, ok := te.executeTool("edit_file", map[string]any{
		"path":  "test.txt",
		"edits": json.RawMessage(editsJSON),
	}, dir, &types.Task{}, nil, nil, nil, nil)
	if ok {
		t.Fatalf("expected failure for overlapping edits, got success: %s", result)
	}
	if !strings.Contains(result, "overlap") {
		t.Errorf("expected 'overlap' error, got %q", result)
	}
}

func TestExecuteTool_EditFile_PathEscape(t *testing.T) {
	te := &TaskExecutor{}
	editsJSON, _ := json.Marshal([]map[string]any{
		{"oldText": "x", "newText": "y"},
	})

	result, ok := te.executeTool("edit_file", map[string]any{
		"path":  "../etc/passwd",
		"edits": json.RawMessage(editsJSON),
	}, "/tmp/worktree", &types.Task{}, nil, nil, nil, nil)
	if ok {
		t.Fatalf("expected failure for path escape, got success: %s", result)
	}
	if result != "Error: path escape" {
		t.Errorf("expected 'Error: path escape', got %q", result)
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

	result, ok := te.executeTool("edit_file", map[string]any{
		"path":  "test.txt",
		"edits": json.RawMessage(editsJSON),
	}, dir, &types.Task{}, nil, nil, nil, nil)
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

	result, ok := te.executeTool("edit_file", map[string]any{
		"path":  "test.txt",
		"edits": json.RawMessage(editsJSON),
	}, dir, &types.Task{}, nil, nil, nil, nil)
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

	readFn := func(path string) ([]byte, error) {
		return os.ReadFile(filepath.Join(dir, path))
	}
	writeFn := func(path, c string) error {
		writtenPath = path
		writtenContent = c
		fullPath := filepath.Join(dir, path)
		os.MkdirAll(filepath.Dir(fullPath), 0755)
		return os.WriteFile(fullPath, []byte(c), 0644)
	}

	editsJSON, _ := json.Marshal([]map[string]any{
		{"oldText": "original", "newText": "modified"},
	})

	result, ok := te.executeTool("edit_file", map[string]any{
		"path":  "test.txt",
		"edits": json.RawMessage(editsJSON),
	}, dir, &types.Task{}, nil, nil, readFn, writeFn)
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

	result, ok := te.executeTool("edit_file", map[string]any{
		"path":  "nonexistent.txt",
		"edits": json.RawMessage(editsJSON),
	}, "/tmp/worktree", &types.Task{}, nil, nil, nil, nil)
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

	result, ok := te.executeTool("bash", map[string]any{
		"command": "echo hello",
		"timeout": float64(5),
	}, dir, &types.Task{}, nil, nil, nil, nil)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}
	if !strings.Contains(result, "hello") {
		t.Errorf("expected 'hello' in output, got %q", result)
	}
}

func TestExecuteTool_Bash_Dangerous(t *testing.T) {
	te := &TaskExecutor{}

	result, ok := te.executeTool("bash", map[string]any{
		"command": "sudo rm -rf /",
		"timeout": float64(5),
	}, "/tmp/worktree", &types.Task{}, nil, nil, nil, nil)
	if ok {
		t.Fatalf("expected failure for dangerous command, got success: %s", result)
	}
	if result != "Error: command blocked" {
		t.Errorf("expected 'Error: command blocked', got %q", result)
	}
}

// --- isDangerousCommand Tests ---

func TestIsDangerousCommand(t *testing.T) {
	dangerous := []string{
		"sudo rm -rf /",
		"mkfs.ext4 /dev/sda",
		"dd if=/dev/zero of=/dev/sda",
		":(){ :|: };:",
		"chmod -R 777 /",
		"rm -rf /",
		"rm -rf /*",
	}
	for _, cmd := range dangerous {
		if !isDangerousCommand(cmd) {
			t.Errorf("expected %q to be dangerous", cmd)
		}
	}

	safe := []string{
		"ls -la",
		"cat file.txt",
		"git status",
		"echo hello",
		"rm -rf ./mydir",
	}
	for _, cmd := range safe {
		if isDangerousCommand(cmd) {
			t.Errorf("expected %q to be safe", cmd)
		}
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
		"/etc/passwd",        // absolute path
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

	result, ok := te.executeTool("write_file", map[string]any{
		"path":    "readonly.txt",
		"content": "data",
	}, "/tmp/worktree", &types.Task{}, nil, nil, nil, writeFn)
	if ok {
		t.Fatalf("expected failure, got success: %s", result)
	}
	if result != "Error: permission denied" {
		t.Errorf("unexpected error: %q", result)
	}
}

func TestValidateWorktreePath_SandboxMountPath(t *testing.T) {
	// Sandbox mode: LLM uses /sandbox/<basename>/... paths
	tests := []struct {
		name   string
		path   string
		wantOK bool
	}{
		{"sandbox valid", "/sandbox/worktree/hello.c", true},
		{"sandbox nested", "/sandbox/worktree/src/main.go", true},
		{"sandbox root", "/sandbox/worktree", true},
		{"sandbox escape", "/sandbox/worktree/../etc/passwd", false},
		{"sandbox absolute escape", "/etc/passwd", false},
		{"sandbox sibling", "/sandbox-other/file.txt", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validateWorktreePath("/tmp/worktree", tt.path)
			if tt.wantOK && result == "" {
				t.Errorf("expected ok for %q, got rejection", tt.path)
			}
			if !tt.wantOK && result != "" {
				t.Errorf("expected rejection for %q, got %q", tt.path, result)
			}
		})
	}
}
