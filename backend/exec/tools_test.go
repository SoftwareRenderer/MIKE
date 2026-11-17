package exec

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"mike/backend/types"
	"mike/pkg/bridge/protocol"
)

// --- tool families ---

// The two families decide which tool calls a sandbox container runs, and they are decided by name
// alone: the sandbox badge on a card, and the read-only streaming path, both read these tables, so a
// name in the wrong family is a card that lies rather than a build failure.
func TestToolSandboxFamilies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		sandboxable bool
		lsp         bool
	}{
		{name: "read_file", sandboxable: true},
		{name: "write_file", sandboxable: true},
		{name: "edit_file", sandboxable: true},
		{name: "bash", sandboxable: true},
		{name: "lsp_references", lsp: true},
		{name: "lsp_definition", lsp: true},
		{name: "lsp_hover", lsp: true},
		{name: "lsp_rename", lsp: true},
		{name: "request_review"},
		{name: "unknown_tool"},
		{name: ""},
	}

	for _, tt := range tests {
		if got := isSandboxableTool(tt.name); got != tt.sandboxable {
			t.Errorf("isSandboxableTool(%q) = %v, want %v", tt.name, got, tt.sandboxable)
		}
		if got := isLSPTool(tt.name); got != tt.lsp {
			t.Errorf("isLSPTool(%q) = %v, want %v", tt.name, got, tt.lsp)
		}
	}
}

// --- executeTool with sandbox callbacks ---

func TestExecuteTool_ReadFile_Sandboxed(t *testing.T) {
	t.Parallel()
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

func TestExecuteTool_WriteFile_Sandboxed(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// The tools that answer the model directly, without touching a file or a container: their answer is
// the conversation, so the sentence and the success flag are the whole contract.
func TestExecuteTool_ConversationToolsAnswerTheModel(t *testing.T) {
	t.Parallel()
	te := newBareTestExecutor(t)
	cases := []struct {
		name   string
		tool   string
		args   map[string]any
		want   string
		wantOK bool
	}{
		{name: "request_review outside plan mode", tool: "request_review", args: map[string]any{}, want: "Task complete", wantOK: true},
		{name: "ask_question echoes the question", tool: "ask_question",
			args: map[string]any{"question": "What framework should I use?"},
			want: "What framework should I use?", wantOK: true},
		{name: "ask_question with no question", tool: "ask_question", args: map[string]any{"question": ""},
			want: "Error: question is required", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, ok := te.executeTool(context.Background(), tc.tool, tc.args, "/tmp/worktree", &types.Task{}, &executionState{}, nil)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (result %q)", ok, tc.wantOK, result)
			}
			if result != tc.want {
				t.Errorf("result = %q, want %q", result, tc.want)
			}
		})
	}
}

// --- edit_file Tests ---

// applyEditReplacements answers from positions found in the original content, so every shape it
// refuses is one the model can correct. The refusal is the whole answer: nothing is written.
func TestExecuteTool_EditFile(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
		absent  bool // the file does not exist at all
		edits   string
		want    string // the file's content after a successful edit
		wantErr string // the substring the refusal must carry
	}{
		{
			name: "single edit", content: "hello world\nfoo bar\nbaz qux",
			edits: `[{"oldText":"hello world","newText":"goodbye world"}]`,
			want:  "goodbye world\nfoo bar\nbaz qux",
		},
		{
			name: "multiple edits in one pass", content: "alpha\nbeta\ngamma\ndelta",
			edits: `[{"oldText":"alpha","newText":"ALPHA"},{"oldText":"gamma","newText":"GAMMA"}]`,
			want:  "ALPHA\nbeta\nGAMMA\ndelta",
		},
		{
			name: "oldText not found", content: "hello world",
			edits: `[{"oldText":"not here","newText":"replacement"}]`, wantErr: "not found",
		},
		{
			name: "oldText appears more than once", content: "foo\nbar\nfoo\nbaz",
			edits: `[{"oldText":"foo","newText":"BAR"}]`, wantErr: "multiple times",
		},
		{
			name: "overlapping edits", content: "abcdefgh",
			edits: `[{"oldText":"abcd","newText":"ABCD"},{"oldText":"cdef","newText":"CDEF"}]`, wantErr: "overlap",
		},
		{
			name: "empty oldText", content: "content",
			edits: `[{"oldText":"","newText":"replacement"}]`, wantErr: "empty",
		},
		{
			name: "no edits", content: "content",
			edits: `[]`, wantErr: "no edits",
		},
		{
			name: "missing file", absent: true,
			edits: `[{"oldText":"x","newText":"y"}]`, wantErr: "Error:",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if !tc.absent {
				if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte(tc.content), 0o644); err != nil {
					t.Fatalf("seed file: %v", err)
				}
			}
			te := newBareTestExecutor(t)

			result, ok := te.executeTool(context.Background(), "edit_file", map[string]any{
				"path":  "test.txt",
				"edits": jsontext.Value(tc.edits),
			}, dir, &types.Task{}, &executionState{}, nil)

			if tc.wantErr != "" {
				if ok {
					t.Fatalf("expected failure, got success: %s", result)
				}
				if !strings.Contains(result, tc.wantErr) {
					t.Errorf("refusal %q does not say %q", result, tc.wantErr)
				}
				got, err := os.ReadFile(filepath.Join(dir, "test.txt"))
				if tc.absent {
					if err == nil {
						t.Errorf("a refused edit created the file: %q", got)
					}
					return
				}
				if err != nil {
					t.Fatalf("read file after refusal: %v", err)
				}
				if string(got) != tc.content {
					t.Errorf("the refused edit changed the file to %q", got)
				}
				return
			}

			if !ok {
				t.Fatalf("expected success, got failure: %s", result)
			}
			data, err := os.ReadFile(filepath.Join(dir, "test.txt"))
			if err != nil {
				t.Fatalf("read file: %v", err)
			}
			if string(data) != tc.want {
				t.Errorf("got %q, want %q", string(data), tc.want)
			}
		})
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
	t.Parallel()
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
		"edits": jsontext.Value(editsJSON),
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

// A callback's own sentence is the answer the model gets, so the container's reason for refusing
// (or the host's) is never replaced by a generic failure.
func TestExecuteTool_CallbackFailureIsTheAnswer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		tool    string
		args    func(dir string) map[string]any
		state   func(t *testing.T, dir string) *executionState
		wantSub string
	}{
		{
			name: "sandbox write", tool: "write_file",
			args: func(string) map[string]any {
				return map[string]any{"path": "readonly.txt", "content": "data"}
			},
			state: func(*testing.T, string) *executionState {
				return &executionState{sandboxWrite: func(_ context.Context, path, content string) error {
					return fmt.Errorf("permission denied")
				}}
			},
			wantSub: "Error: permission denied",
		},
		{
			name: "sandbox edit", tool: "edit_file",
			args: func(string) map[string]any {
				return map[string]any{"path": "test.txt", "edits": jsontext.Value(`[{"oldText":"nope","newText":"x"}]`)}
			},
			state: func(t *testing.T, dir string) *executionState {
				if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello world"), 0o644); err != nil {
					t.Fatalf("seed file: %v", err)
				}
				return &executionState{
					projectName: "test",
					sandboxEdit: func(_ context.Context, path string, edits []protocol.Edit) error {
						return fmt.Errorf("edits[0].oldText not found in file. Ensure it matches exactly (whitespace, indentation). Got 1 lines in file.")
					},
				}
			},
			wantSub: "not found in file",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			te := newBareTestExecutor(t)
			result, ok := te.executeTool(context.Background(), tc.tool, tc.args(dir), dir, &types.Task{}, tc.state(t, dir), nil)
			if ok {
				t.Fatalf("expected failure, got success: %s", result)
			}
			if !strings.Contains(result, tc.wantSub) {
				t.Errorf("result %q does not carry the cause %q", result, tc.wantSub)
			}
		})
	}
}

// BenchmarkExecuteTool_EditFile measures the cost of applying many edits to a large file. The apply
// path is O(file size) regardless of edit count; a per-edit full-string rebuild would show up here as a
// dramatic per-op time increase.
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
			"edits": jsontext.Value(editsJSON),
		}, dir, &types.Task{}, &executionState{}, nil)
		if !ok {
			b.Fatalf("edit_file failed: %s", result)
		}
	}
}

func TestExecuteTool_EditFile_OutsideWorktreeIsAHostEdit(t *testing.T) {
	t.Parallel()
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
		"edits": jsontext.Value(editsJSON),
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

func TestExecuteTool_EditFile_Sandboxed(t *testing.T) {
	t.Parallel()
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
		"edits": jsontext.Value(editsJSON),
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

// --- bash Tests ---

// A tool's output is what the model reads and the card shows, so the escape sequences a build or a
// test runner prints are stripped on the way to both — on the failing path as well as the succeeding
// one, which is the half that would otherwise keep them.
func TestExecuteTool_BashStripsANSI(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		command string
		wantOK  bool
		wants   []string
	}{
		{
			name:    "succeeding command",
			command: "printf '\x1b[1m\x1b[30m\x1b[46m RUN \x1b[49m\x1b[39m\x1b[22m \x1b[36mv4.1.10\n'",
			wantOK:  true,
			wants:   []string{"RUN", "v4.1.10"},
		},
		{
			name:    "failing command",
			command: "printf '\\x1b[31mERROR\\x1b[0m something failed\\n'; exit 1",
			wantOK:  false,
			wants:   []string{"ERROR"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			te := newBareTestExecutor(t)
			result, ok := te.executeTool(context.Background(), "bash", map[string]any{
				"command": tc.command,
				"timeout": float64(5),
			}, t.TempDir(), &types.Task{}, &executionState{}, nil)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (result %q)", ok, tc.wantOK, result)
			}
			if strings.Contains(result, "\x1b") {
				t.Errorf("ANSI escape codes survived: %q", result)
			}
			for _, want := range tc.wants {
				if !strings.Contains(result, want) {
					t.Errorf("output lost %q: %q", want, result)
				}
			}
		})
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
	t.Parallel()
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

// --- Bridge recovery tests ---

// bridgeAttempts carries what one recovery case exercised: whether the latch fired and how many
// times the dying callback was asked.
type bridgeAttempts struct {
	recovered bool
	failed    int
}

// bridgeCases run the same retry over every callback a tool call can hold. Each installs a callback
// that answers with a bridge error and a recoverBridge that swaps in a working one. The read, exec
// and stream callbacks are re-read from live state, so those cases use a callback that fails every
// time: a retry that reused the snapshot the dispatch took would call it again and never answer. The
// write and the delegated edit are the opposite — the retry runs through the callback the call was
// dispatched with — so those refuse once and then do the work.
func TestExecuteTool_BridgeRecoveryRetriesEachToolCall(t *testing.T) {
	t.Parallel()
	type bridgeCase struct {
		name    string
		tool    string
		args    func(dir string) map[string]any
		prepare func(t *testing.T, dir string)
		// withOutput gives the call an output callback, which is what makes bash take its
		// streaming callback rather than the plain one; wantStreamed is what the UI must have seen.
		withOutput   bool
		wantStreamed string
		install      func(t *testing.T, st *executionState, dir string, a *bridgeAttempts)
		// want checks the answer and, where the call touched a file, the file.
		want func(t *testing.T, dir, result string, a *bridgeAttempts)
	}

	workingRead := func(dir string) readFunc {
		return func(_ context.Context, path string) ([]byte, error) {
			return os.ReadFile(filepath.Join(dir, path))
		}
	}
	workingWrite := func(dir string) writeFunc {
		return func(_ context.Context, path, content string) error {
			return os.WriteFile(filepath.Join(dir, path), []byte(content), 0644)
		}
	}
	// dying is a callback on the connection that died: it fails every time, so a retry that reused
	// the snapshot the dispatch took keeps failing and the case goes red.
	dyingRead := func(marker string, a *bridgeAttempts) readFunc {
		return func(_ context.Context, _ string) ([]byte, error) {
			a.failed++
			return nil, fmt.Errorf("read: %s", marker)
		}
	}
	dyingWrite := func(marker string, a *bridgeAttempts, dir string) writeFunc {
		// Unlike the reader, the writer is the callback the call was dispatched with, so the retry
		// runs through this one: it refuses once, as a connection that died does, then writes.
		return func(_ context.Context, path, content string) error {
			a.failed++
			if a.failed == 1 {
				return fmt.Errorf("%s", marker)
			}
			return os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644)
		}
	}

	cases := []bridgeCase{
		{
			name: "read_file",
			tool: "read_file",
			args: func(string) map[string]any { return map[string]any{"path": "hello.txt"} },
			prepare: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello from sandbox"), 0o644); err != nil {
					t.Fatalf("seed file: %v", err)
				}
			},
			install: func(_ *testing.T, st *executionState, dir string, a *bridgeAttempts) {
				st.sandboxRead = dyingRead("broken pipe", a)
				st.recoverBridge = func() bool { a.recovered = true; st.sandboxRead = workingRead(dir); return true }
			},
			want: func(t *testing.T, _, result string, a *bridgeAttempts) {
				if want := "hello from sandbox"; result != want {
					t.Errorf("result = %q, want %q", result, want)
				}
				if a.failed != 1 {
					t.Errorf("the dead reader was called %d times, want once", a.failed)
				}
			},
		},
		{
			name: "read_file client closed",
			tool: "read_file",
			args: func(string) map[string]any { return map[string]any{"path": "hello.txt"} },
			prepare: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello from sandbox"), 0o644); err != nil {
					t.Fatalf("seed file: %v", err)
				}
			},
			install: func(_ *testing.T, st *executionState, dir string, a *bridgeAttempts) {
				// "client closed" is what Client.Close() answers with, when a sandbox was
				// recreated (an operating-mode change) and a call still holds the old client.
				st.sandboxRead = dyingRead("client closed", a)
				st.recoverBridge = func() bool { a.recovered = true; st.sandboxRead = workingRead(dir); return true }
			},
			want: func(t *testing.T, _, result string, _ *bridgeAttempts) {
				if want := "hello from sandbox"; result != want {
					t.Errorf("result = %q, want %q", result, want)
				}
			},
		},
		{
			name: "read_file connection closed",
			tool: "read_file",
			args: func(string) map[string]any { return map[string]any{"path": "hello.txt"} },
			prepare: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello from sandbox"), 0o644); err != nil {
					t.Fatalf("seed file: %v", err)
				}
			},
			install: func(_ *testing.T, st *executionState, dir string, a *bridgeAttempts) {
				// "connection closed" is what the readLoop hands pending requests when it exits.
				st.sandboxRead = dyingRead("connection closed", a)
				st.recoverBridge = func() bool { a.recovered = true; st.sandboxRead = workingRead(dir); return true }
			},
			want: func(t *testing.T, _, result string, _ *bridgeAttempts) {
				if want := "hello from sandbox"; result != want {
					t.Errorf("result = %q, want %q", result, want)
				}
			},
		},
		{
			name: "write_file",
			tool: "write_file",
			args: func(string) map[string]any {
				return map[string]any{"path": "test.txt", "content": "hello"}
			},
			install: func(_ *testing.T, st *executionState, dir string, a *bridgeAttempts) {
				st.sandboxWrite = dyingWrite("broken pipe", a, dir)
				st.recoverBridge = func() bool { a.recovered = true; st.sandboxWrite = workingWrite(dir); return true }
			},
			want: func(t *testing.T, dir, result string, a *bridgeAttempts) {
				if want := "Wrote 5 bytes to test.txt"; result != want {
					t.Errorf("result = %q, want %q", result, want)
				}
				if a.failed != 2 {
					t.Errorf("the writer was called %d times, want the refusal and the retry", a.failed)
				}
				data, err := os.ReadFile(filepath.Join(dir, "test.txt"))
				if err != nil {
					t.Fatalf("read written file: %v", err)
				}
				if string(data) != "hello" {
					t.Errorf("file content = %q, want %q", data, "hello")
				}
			},
		},
		{
			name: "bash",
			tool: "bash",
			args: func(string) map[string]any { return map[string]any{"command": "echo hello"} },
			install: func(_ *testing.T, st *executionState, _ string, a *bridgeAttempts) {
				st.sandboxExec = func(_ context.Context, _ string, _ int64) (string, error) {
					a.failed++
					return "", fmt.Errorf("bash: send bash request: write header: write |1: broken pipe")
				}
				st.recoverBridge = func() bool {
					a.recovered = true
					st.sandboxExec = func(_ context.Context, _ string, _ int64) (string, error) {
						return "echo output", nil
					}
					return true
				}
			},
			want: func(t *testing.T, _, result string, a *bridgeAttempts) {
				if result != "echo output" {
					t.Errorf("unexpected result: %q", result)
				}
				if a.failed != 1 {
					t.Errorf("the dead exec was called %d times, want once", a.failed)
				}
			},
		},
		{
			name:         "bash streaming",
			tool:         "bash",
			args:         func(string) map[string]any { return map[string]any{"command": "echo hello"} },
			withOutput:   true,
			wantStreamed: "streamed line\n",
			install: func(_ *testing.T, st *executionState, _ string, a *bridgeAttempts) {
				st.sandboxExecStream = func(_ context.Context, _ string, _ int64, _ func(string)) (string, error) {
					a.failed++
					return "", fmt.Errorf("bash: send bash request: write header: write |1: broken pipe")
				}
				st.recoverBridge = func() bool {
					a.recovered = true
					st.sandboxExecStream = func(_ context.Context, _ string, _ int64, cb func(string)) (string, error) {
						if cb != nil {
							cb("streamed line\n")
						}
						return "streamed line\n", nil
					}
					return true
				}
			},
			want: func(t *testing.T, _, result string, a *bridgeAttempts) {
				if result != "streamed line\n" {
					t.Errorf("unexpected result: %q", result)
				}
				if a.failed != 1 {
					t.Errorf("the dead streaming exec was called %d times, want once", a.failed)
				}
			},
		},
		{
			name: "edit_file delegated",
			tool: "edit_file",
			args: func(string) map[string]any {
				return map[string]any{"path": "test.txt", "edits": jsontext.Value(`[{"oldText":"line two","newText":"modified two"}]`)}
			},
			prepare: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("line one\nline two\nline three"), 0o644); err != nil {
					t.Fatalf("seed file: %v", err)
				}
			},
			install: func(t *testing.T, st *executionState, dir string, a *bridgeAttempts) {
				failing := &sandboxEditRecorder{failN: 1}
				working := &sandboxEditRecorder{}
				st.sandboxEdit = failing.callback(dir)
				st.recoverBridge = func() bool {
					a.recovered = true
					st.sandboxEdit = working.callback(dir)
					return true
				}
			},
			want: func(t *testing.T, dir, result string, _ *bridgeAttempts) {
				if !strings.Contains(result, "Edited") {
					t.Errorf("unexpected result: %q", result)
				}
				data, err := os.ReadFile(filepath.Join(dir, "test.txt"))
				if err != nil {
					t.Fatalf("read edited file: %v", err)
				}
				if !strings.Contains(string(data), "modified two") {
					t.Errorf("file content = %q, want it to contain %q", data, "modified two")
				}
			},
		},
		{
			name: "edit_file read half",
			tool: "edit_file",
			args: func(string) map[string]any {
				return map[string]any{"path": "test.txt", "edits": jsontext.Value(`[{"oldText":"line two","newText":"modified two"}]`)}
			},
			prepare: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("line one\nline two\nline three"), 0o644); err != nil {
					t.Fatalf("seed file: %v", err)
				}
			},
			install: func(_ *testing.T, st *executionState, dir string, a *bridgeAttempts) {
				st.sandboxRead = dyingRead("broken pipe", a)
				st.sandboxWrite = workingWrite(dir)
				st.recoverBridge = func() bool {
					a.recovered = true
					st.sandboxRead = workingRead(dir)
					st.sandboxWrite = workingWrite(dir)
					return true
				}
			},
			want: func(t *testing.T, dir, result string, _ *bridgeAttempts) {
				if !strings.Contains(result, "Edited") {
					t.Errorf("unexpected result: %q", result)
				}
				data, err := os.ReadFile(filepath.Join(dir, "test.txt"))
				if err != nil {
					t.Fatalf("read edited file: %v", err)
				}
				if !strings.Contains(string(data), "modified two") {
					t.Errorf("file content = %q, want it to contain %q", data, "modified two")
				}
			},
		},
		{
			name: "edit_file write half",
			tool: "edit_file",
			args: func(string) map[string]any {
				return map[string]any{"path": "test.txt", "edits": jsontext.Value(`[{"oldText":"line two","newText":"modified two"}]`)}
			},
			prepare: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("line one\nline two\nline three"), 0o644); err != nil {
					t.Fatalf("seed file: %v", err)
				}
			},
			install: func(_ *testing.T, st *executionState, dir string, a *bridgeAttempts) {
				st.sandboxRead = workingRead(dir)
				st.sandboxWrite = dyingWrite("broken pipe", a, dir)
				st.recoverBridge = func() bool {
					a.recovered = true
					st.sandboxRead = workingRead(dir)
					st.sandboxWrite = workingWrite(dir)
					return true
				}
			},
			want: func(t *testing.T, dir, result string, a *bridgeAttempts) {
				if !strings.Contains(result, "Edited") {
					t.Errorf("unexpected result: %q", result)
				}
				if a.failed != 2 {
					t.Errorf("the writer was called %d times, want the refusal and the retry", a.failed)
				}
				data, err := os.ReadFile(filepath.Join(dir, "test.txt"))
				if err != nil {
					t.Fatalf("read edited file: %v", err)
				}
				if !strings.Contains(string(data), "modified two") {
					t.Errorf("file content = %q, want it to contain %q", data, "modified two")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.prepare != nil {
				tc.prepare(t, dir)
			}
			te := newBareTestExecutor(t)
			state := &executionState{projectName: "test"}
			attempts := &bridgeAttempts{}
			tc.install(t, state, dir, attempts)

			var streamed []string
			var output func(toolID, text string)
			if tc.withOutput {
				output = func(_, text string) { streamed = append(streamed, text) }
			}
			result, ok := te.executeTool(context.Background(), tc.tool, tc.args(dir), dir, &types.Task{}, state, output)
			if !ok {
				t.Fatalf("expected success after recovery, got failure: %s", result)
			}
			if !attempts.recovered {
				t.Fatal("expected recoverBridge to be called")
			}
			if tc.wantStreamed != "" {
				if got := strings.Join(streamed, ""); got != tc.wantStreamed {
					t.Errorf("the live stream carried %q, want %q", got, tc.wantStreamed)
				}
			}
			tc.want(t, dir, result, attempts)
		})
	}
}

// A call whose callback died for a reason that is not the bridge must not be retried: the retry
// would repeat a command that already ran, and the model would get the error either way.
func TestExecuteTool_ReadFile_NoRecoveryOnNonBridgeError(t *testing.T) {
	t.Parallel()
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

func TestExecuteTool_Bash_NoRecoveryOnNonBridgeError(t *testing.T) {
	t.Parallel()
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

func TestExecuteTool_RecoveryReturnsFalse(t *testing.T) {
	t.Parallel()
	// When recoverBridge reports that recovery failed, the error propagates.
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
		t.Fatalf("expected failure when recovery returns false, got success: %s", result)
	}
	if result != "Error: broken pipe" {
		t.Errorf("unexpected error: %q", result)
	}
}
