package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"kanban/backend/types"
	"kanban/pkg/bridge/protocol"
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

func TestIsLSPTool(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"lsp_references", true},
		{"lsp_definition", true},
		{"lsp_hover", true},
		{"lsp_rename", true},
		{"read_file", false},
		{"write_file", false},
		{"unknown_tool", false},
		{"", false},
	}

	for _, tt := range tests {
		got := isLSPTool(tt.name)
		if got != tt.want {
			t.Errorf("isLSPTool(%q) = %v, want %v", tt.name, got, tt.want)
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

	te := newBareTestExecutor(t)

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

	te := newBareTestExecutor(t)

	// No sandboxRead — should fall back to host
	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{"path": "host.txt"}, dir, &types.Task{}, &executionState{}, nil)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}
	if result != expectedContent {
		t.Errorf("expected %q, got %q", expectedContent, result)
	}
}

func TestExecuteTool_ReadFile_RelativeParentIsAHostPath(t *testing.T) {
	// A task whose project runs without a sandbox works on the host: ".." resolves against
	// the worktree and reads whatever it names. The boundary for such a task is the
	// project's sandbox_enabled column; TestSandboxedTaskWithoutBridgeRefusesEveryHostTool
	// holds the other side.
	base := t.TempDir()
	worktree := filepath.Join(base, "worktree")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatalf("create worktree dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "notes.txt"), []byte("outside"), 0o644); err != nil {
		t.Fatalf("seed file outside the worktree: %v", err)
	}
	te := newBareTestExecutor(t)

	result, ok := te.executeTool(context.Background(), "read_file", map[string]any{"path": "../notes.txt"}, worktree, &types.Task{}, &executionState{}, nil)
	if !ok {
		t.Fatalf("expected the host path to be read, got failure: %s", result)
	}
	if result != "outside" {
		t.Errorf("read %q, want %q", result, "outside")
	}
}

func TestExecuteTool_WriteFile_Sandboxed(t *testing.T) {
	dir := t.TempDir()
	te := newBareTestExecutor(t)
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
	te := newBareTestExecutor(t)

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

func TestExecuteTool_WriteFile_WritesWhereThePathNames(t *testing.T) {
	// The host write is the mirror of the host read: a relative path resolves against the
	// worktree, ".." and absolute paths name whatever they name, and the parent chain is
	// created as needed. A task that must not touch the host is a task whose project is
	// sandboxed, and that is refused before a callback is chosen.
	base := t.TempDir()
	worktree := filepath.Join(base, "a", "b", "worktree")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("create worktree dir: %v", err)
	}
	te := newBareTestExecutor(t)

	abs := filepath.Join(base, "absolute", "file.txt")
	for _, target := range []string{"deep/nested/dir/file.txt", "../escaped/evil.txt", abs} {
		result, ok := te.executeTool(context.Background(), "write_file", map[string]any{
			"path": target, "content": "written",
		}, worktree, &types.Task{}, &executionState{}, nil)
		if !ok {
			t.Fatalf("write_file(%q) failed: %s", target, result)
		}
		full := target
		if !filepath.IsAbs(full) {
			full = filepath.Join(worktree, target)
		}
		data, err := os.ReadFile(filepath.Clean(full))
		if err != nil {
			t.Errorf("write_file(%q) left nothing at %s: %v", target, filepath.Clean(full), err)
			continue
		}
		if string(data) != "written" {
			t.Errorf("write_file(%q) wrote %q", target, data)
		}
	}
	// Inside the worktree, only the path that was named there exists.
	assertTreeEquals(t, worktree, []string{"deep"})
}

func TestExecuteTool_WriteFile_CreatesNestedDirs(t *testing.T) {
	// A brand-new nested subdirectory under the worktree is created on the way to the
	// file (no sandboxWrite injected), which keeps the historical 0644 mode.
	worktree := t.TempDir()
	te := newBareTestExecutor(t)

	result, ok := te.executeTool(context.Background(), "write_file", map[string]any{
		"path":    "deep/nested/dir/file.txt",
		"content": "nested content",
	}, worktree, &types.Task{}, &executionState{}, nil)
	if !ok {
		t.Fatalf("expected success writing into a new nested dir, got failure: %s", result)
	}

	full := filepath.Join(worktree, "deep", "nested", "dir", "file.txt")
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("read nested file: %v", err)
	}
	if string(data) != "nested content" {
		t.Errorf("nested file content = %q, want 'nested content'", string(data))
	}
	info, err := os.Stat(full)
	if err != nil {
		t.Fatalf("stat nested file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("nested file mode = %o, want 0644", perm)
	}
	for _, d := range []string{"deep", "deep/nested", "deep/nested/dir"} {
		di, err := os.Stat(filepath.Join(worktree, d))
		if err != nil {
			t.Fatalf("created dir %q missing: %v", d, err)
		}
		if !di.IsDir() {
			t.Errorf("%q is not a directory", d)
		}
	}
}

func TestExecuteTool_WriteFile_DirCreation(t *testing.T) {
	dir := t.TempDir()
	te := newBareTestExecutor(t)

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

// assertTreeEquals checks that dir contains exactly the given top-level entries.
// Used by the escape tests to prove a rejected write left no directory behind.
func assertTreeEquals(t *testing.T, dir string, want []string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %q: %v", dir, err)
	}
	got := make([]string, 0, len(entries))
	for _, e := range entries {
		got = append(got, e.Name())
	}
	sort.Strings(got)
	sorted := append([]string(nil), want...)
	sort.Strings(sorted)
	if len(got) != len(sorted) || strings.Join(got, "\x00") != strings.Join(sorted, "\x00") {
		t.Errorf("entries in %q = %v, want %v", dir, got, sorted)
	}
}

func TestExecuteTool_TaskComplete(t *testing.T) {
	te := newBareTestExecutor(t)
	result, ok := te.executeTool(context.Background(), "request_review", map[string]any{}, "/tmp/worktree", &types.Task{}, &executionState{}, nil)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}
	if result != "Task complete" {
		t.Errorf("expected 'Task complete', got %q", result)
	}
}

func TestExecuteTool_AskQuestion(t *testing.T) {
	te := newBareTestExecutor(t)
	result, ok := te.executeTool(context.Background(), "ask_question", map[string]any{"question": "What framework should I use?"}, "/tmp/worktree", &types.Task{}, &executionState{}, nil)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}
	if result != "What framework should I use?" {
		t.Errorf("expected question text, got %q", result)
	}
}

func TestExecuteTool_AskQuestion_Empty(t *testing.T) {
	te := newBareTestExecutor(t)
	result, ok := te.executeTool(context.Background(), "ask_question", map[string]any{"question": ""}, "/tmp/worktree", &types.Task{}, &executionState{}, nil)
	if ok {
		t.Fatalf("expected failure for empty question, got success: %s", result)
	}
	if result != "Error: question is required" {
		t.Errorf("expected 'Error: question is required', got %q", result)
	}
}

func TestExecuteTool_UnknownTool(t *testing.T) {
	te := newBareTestExecutor(t)
	result, ok := te.executeTool(context.Background(), "unknown_tool", map[string]any{}, "/tmp/worktree", &types.Task{}, &executionState{}, nil)
	if ok {
		t.Fatalf("expected failure for unknown tool, got success: %s", result)
	}
	if result != "Unknown tool: unknown_tool" {
		t.Errorf("expected 'Unknown tool: unknown_tool', got %q", result)
	}
}

func TestExecuteTool_ReadFile_CallbackError(t *testing.T) {
	te := newBareTestExecutor(t)

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

	te := newBareTestExecutor(t)

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
			name:    "offset past end returns error to inform the LLM",
			args:    map[string]any{"path": "test.txt", "offset": float64(100)},
			want:    "Error: offset 100 is past the end of the file (file has 10 line(s)). Use a valid offset.",
			wantErr: true,
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

	te := newBareTestExecutor(t)
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

	te := newBareTestExecutor(t)
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

// --- edit_file sandbox delegation (MethodEdit) tests ---

// sandboxEditRecorder is a test edit callback that records the edits/path it
// receives and optionally fails the first call with a bridge error. When it
// runs without error it applies the edits to the file on disk, mirroring the
// sandbox agent's behavior.
type sandboxEditRecorder struct {
	mu     sync.Mutex
	path   string
	calls  []protocol.Edit
	failN  int // fail the Nth call (1-based); 0 = never fail
	callsN int
}

func (r *sandboxEditRecorder) callback(dir string) editFunc {
	return func(_ context.Context, path string, edits []protocol.Edit) error {
		r.mu.Lock()
		r.callsN++
		r.path = path
		r.calls = append(r.calls, edits...)
		fail := r.failN > 0 && r.callsN == r.failN
		r.mu.Unlock()
		if fail {
			return fmt.Errorf("broken pipe")
		}
		data, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil {
			return err
		}
		content := string(data)
		for i, e := range edits {
			idx := strings.Index(content, e.OldText)
			if idx == -1 {
				return fmt.Errorf("edits[%d].oldText not found in file", i)
			}
			content = content[:idx] + e.NewText + content[idx+len(e.OldText):]
		}
		return os.WriteFile(filepath.Join(dir, path), []byte(content), 0644)
	}
}

func TestExecuteTool_EditFile_SandboxDelegation(t *testing.T) {
	dir := t.TempDir()
	content := "alpha\nbeta\ngamma\ndelta"
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	rec := &sandboxEditRecorder{}
	state := &executionState{
		projectName:  "test",
		sandboxRead:  nil, // must NOT be used when sandboxEdit is present
		sandboxWrite: nil, // must NOT be used when sandboxEdit is present
		sandboxEdit:  rec.callback(dir),
	}

	te := newBareTestExecutor(t)
	editsJSON, _ := json.Marshal([]map[string]any{
		{"oldText": "alpha", "newText": "ALPHA"},
		{"oldText": "gamma", "newText": "GAMMA"},
	})
	result, ok := te.executeTool(context.Background(), "edit_file", map[string]any{
		"path":  "test.txt",
		"edits": json.RawMessage(editsJSON),
	}, dir, &types.Task{}, state, nil)
	if !ok {
		t.Fatalf("expected success, got failure: %s", result)
	}

	// The edits must be forwarded to the sandbox agent as protocol.Edit.
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.path != "test.txt" {
		t.Errorf("sandboxEdit got path %q, want %q", rec.path, "test.txt")
	}
	if len(rec.calls) != 2 {
		t.Fatalf("sandboxEdit got %d edits, want 2", len(rec.calls))
	}
	if rec.calls[0] != (protocol.Edit{OldText: "alpha", NewText: "ALPHA"}) ||
		rec.calls[1] != (protocol.Edit{OldText: "gamma", NewText: "GAMMA"}) {
		t.Errorf("sandboxEdit got edits %+v", rec.calls)
	}

	// Success message reflects the number of edits.
	if !strings.Contains(result, "Edited test.txt (2 edit(s) applied)") {
		t.Errorf("unexpected result: %q", result)
	}

	// The file must reflect the applied edits.
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if want := "ALPHA\nbeta\nGAMMA\ndelta"; string(data) != want {
		t.Errorf("got %q, want %q", string(data), want)
	}
}

func TestExecuteTool_EditFile_SandboxDelegation_Error(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello world"), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	editErr := func(_ context.Context, path string, edits []protocol.Edit) error {
		return fmt.Errorf("edits[0].oldText not found in file. Ensure it matches exactly (whitespace, indentation). Got 1 lines in file.")
	}
	state := &executionState{
		projectName: "test",
		sandboxEdit: editErr,
	}

	te := newBareTestExecutor(t)
	editsJSON, _ := json.Marshal([]map[string]any{{"oldText": "nope", "newText": "x"}})
	result, ok := te.executeTool(context.Background(), "edit_file", map[string]any{
		"path":  "test.txt",
		"edits": json.RawMessage(editsJSON),
	}, dir, &types.Task{}, state, nil)
	if ok {
		t.Fatalf("expected failure, got success: %s", result)
	}
	if !strings.Contains(result, "not found in file") {
		t.Errorf("expected sandbox edit error, got %q", result)
	}
}

func TestExecuteTool_EditFile_SandboxDelegation_Recovery(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("line one\nline two\nline three"), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	working := &sandboxEditRecorder{}
	failing := &sandboxEditRecorder{failN: 1}
	recoveryCalled := false
	state := &executionState{
		projectName: "test",
		sandboxEdit: failing.callback(dir),
	}
	state.recoverBridge = func() bool {
		recoveryCalled = true
		// Refresh to a working callback, like a reconnected sandbox client.
		state.sandboxEdit = working.callback(dir)
		return true
	}

	edits, _ := json.Marshal([]map[string]any{{"oldText": "line two", "newText": "modified two"}})
	te := newBareTestExecutor(t)
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
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	if !strings.Contains(string(data), "modified two") {
		t.Errorf("file content = %q, want to contain %q", string(data), "modified two")
	}
}

// BenchmarkExecuteTool_EditFile measures the cost of applying many edits to a
// large file. The apply path is O(file size) regardless of edit count; a
// regression to the old quadratic (per-edit full-string rebuild) shows up as a
// dramatic per-op time increase here.
func BenchmarkExecuteTool_EditFile(b *testing.B) {
	const nEdits = 2000
	var content strings.Builder
	for i := 0; i < nEdits; i++ {
		content.WriteString(fmt.Sprintf("some code line %d with content to pad the file\n", i))
		content.WriteString(fmt.Sprintf("MARKER_%04d\n", i))
	}
	fileContent := content.String()
	// Fixed-width markers ensure no marker is a substring of another, so each
	// oldText is unique and the edit succeeds.
	edits := make([]map[string]any, nEdits)
	for j := 0; j < nEdits; j++ {
		edits[j] = map[string]any{
			"oldText": fmt.Sprintf("MARKER_%04d", j),
			"newText": fmt.Sprintf("REPLACED_%04d", j),
		}
	}
	editsJSON, _ := json.Marshal(edits)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dir := b.TempDir()
		path := filepath.Join(dir, "test.txt")
		if err := os.WriteFile(path, []byte(fileContent), 0644); err != nil {
			b.Fatal(err)
		}
		te := newBareTestExecutor(b)
		result, ok := te.executeTool(context.Background(), "edit_file", map[string]any{
			"path":  "test.txt",
			"edits": json.RawMessage(editsJSON),
		}, dir, &types.Task{}, &executionState{}, nil)
		if !ok {
			b.Fatalf("edit_file failed: %s", result)
		}
	}
}

func TestExecuteTool_EditFile_OldTextNotFound(t *testing.T) {
	dir := t.TempDir()
	content := "hello world"
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	te := newBareTestExecutor(t)
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

	te := newBareTestExecutor(t)
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

	te := newBareTestExecutor(t)
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

func TestExecuteTool_EditFile_OutsideWorktreeIsAHostEdit(t *testing.T) {
	// edit_file reads and writes the same path, so on a host task it edits whatever
	// ".." names — one rule for both halves, never a middle ground where the read
	// escapes and the write does not.
	base := t.TempDir()
	worktree := filepath.Join(base, "worktree")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatalf("create worktree dir: %v", err)
	}
	target := filepath.Join(base, "victim.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed file outside the worktree: %v", err)
	}

	te := newBareTestExecutor(t)
	editsJSON, _ := json.Marshal([]map[string]any{
		{"oldText": "x", "newText": "y"},
	})

	result, ok := te.executeTool(context.Background(), "edit_file", map[string]any{
		"path":  "../victim.txt",
		"edits": json.RawMessage(editsJSON),
	}, worktree, &types.Task{}, &executionState{}, nil)
	if !ok {
		t.Fatalf("expected the host edit to succeed, got failure: %s", result)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	if string(data) != "y" {
		t.Errorf("host edit did not reach %s: got %q", target, string(data))
	}
	assertTreeEquals(t, base, []string{"victim.txt", "worktree"})
}

func TestExecuteTool_EditFile_EmptyOldText(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("content"), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	te := newBareTestExecutor(t)
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

	te := newBareTestExecutor(t)
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

	te := newBareTestExecutor(t)
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
	te := newBareTestExecutor(t)
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
	te := newBareTestExecutor(t)

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
	te := newBareTestExecutor(t)

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
	te := newBareTestExecutor(t)

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

// TestStripANSI is one table over every escape shape a real terminal emits, because the
// function is a single pure scanner and each shape is one more branch of it: SGR colors
// (including 256-color and 24-bit), cursor movement, erase-line, OSC window titles with
// both terminators, single-byte escapes, truncated sequences at end of string, and the
// things that must SURVIVE — tabs, CRs, newlines and non-ASCII text. Progress bars and
// test-runner output are the real inputs, so both the noise and the payload come from there.
func TestStripANSI(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "no ANSI", in: "hello world\nline two", want: "hello world\nline two"},
		{
			name: "bold and color", // \x1b[1m bold, \x1b[31m red, \x1b[0m reset
			in:   "\x1b[1m\x1b[31mERROR\x1b[0m", want: "ERROR",
		},
		{
			name: "vitest banner", // real-world: \x1b[1m\x1b[30m\x1b[46m RUN \x1b[49m\x1b[39m\x1b[22m \x1b[36mv4.1.10
			in:   "\x1b[1m\x1b[30m\x1b[46m RUN \x1b[49m\x1b[39m\x1b[22m \x1b[36mv4.1.10", want: " RUN  v4.1.10",
		},
		{
			name: "multiple lines",
			in:   "\x1b[32mPASS\x1b[0m test1\n\x1b[31mFAIL\x1b[0m test2\nplain line",
			want: "PASS test1\nFAIL test2\nplain line",
		},
		{name: "OSC title terminated by BEL", in: "\x1b]0;My Title\x07hello", want: "hello"},
		{name: "OSC title terminated by ST", in: "\x1b]0;Title\x1b\\hello", want: "hello"},
		{
			name: "single-byte escapes", // \x1bH home, \x1bM reverse index
			in:   "\x1bH\x1bMstart", want: "start",
		},
		{name: "truncated sequence at end", in: "hello\x1b[31", want: "hello"},
		{name: "bare escape at end", in: "hello\x1b", want: "hello"},
		{
			name: "cursor movement", // \x1b[2A up 2, \x1b[10C forward 10
			in:   "\x1b[2Aline1\x1b[10Cline2", want: "line1line2",
		},
		{
			name: "mixed npm output",
			in:   "\x1b[1;34mnpm\x1b[0m run \x1b[1mtest\x1b[0m\n\x1b[32m✓\x1b[0m passed\n\x1b[33m⚠\x1b[0m warning",
			want: "npm run test\n✓ passed\n⚠ warning",
		},
		{
			name: "non-ANSI specials are preserved",
			in:   "tab\there\nnewline\r\r\nunicode: \u00e9\u00e8\u00ea",
			want: "tab\there\nnewline\r\r\nunicode: \u00e9\u00e8\u00ea",
		},
		{name: "256-color CSI", in: "\x1b[38;5;196mred\x1b[0m", want: "red"},
		{name: "24-bit color CSI", in: "\x1b[38;2;255;128;0morange\x1b[0m", want: "orange"},
		{name: "256-color background", in: "\x1b[48;5;27mhighlighted\x1b[0m", want: "highlighted"},
		{name: "erase line", in: "\x1b[2Kprogress: 50%", want: "progress: 50%"},
		{
			name: "progress bar keeps its carriage return",
			in:   "\x1b[2K\r\x1b[32m✓\x1b[0m done", want: "\r✓ done",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripANSI(tc.in); got != tc.want {
				t.Errorf("stripANSI(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestExecuteTool_WriteFile_CallbackError(t *testing.T) {
	te := newBareTestExecutor(t)

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
	te := newBareTestExecutor(t)
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
	te := newBareTestExecutor(t)

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
	te := newBareTestExecutor(t)
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
	te := newBareTestExecutor(t)

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
	te := newBareTestExecutor(t)

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
	te := newBareTestExecutor(t)

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
	te := newBareTestExecutor(t)
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
	te := newBareTestExecutor(t)
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
	te := newBareTestExecutor(t)

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
	te := newBareTestExecutor(t)
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
	te := newBareTestExecutor(t)
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
	te := newBareTestExecutor(t)

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
