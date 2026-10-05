package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/testutil/testgit"
)

// fakeLSPManager records LSP method calls and returns canned responses.
// Embeds the interface so any unstubbed call panics (unexpected call).
type fakeLSPManager struct {
	lspManager
	methods            []string
	responses          map[string]json.RawMessage
	enabled            bool
	lastParamsByMethod map[string]any
}

func (f *fakeLSPManager) LSPRequest(_ context.Context, workspacePath, filePath, method string, params any) (json.RawMessage, error) {
	f.methods = append(f.methods, method)
	if f.lastParamsByMethod == nil {
		f.lastParamsByMethod = map[string]any{}
	}
	f.lastParamsByMethod[method] = params
	if resp, ok := f.responses[method]; ok {
		return resp, nil
	}
	return json.RawMessage(`{"jsonrpc":"2.0","result":null}`), nil
}

func (f *fakeLSPManager) IsToolCallsEnabledForFile(string) bool { return f.enabled }

// osReadWrite makes read/write callbacks over a real temp directory.
func osReadWrite(ws string) (readFunc, writeFunc) {
	read := func(_ context.Context, p string) ([]byte, error) {
		return os.ReadFile(filepath.Join(ws, p))
	}
	write := func(_ context.Context, p, content string) error {
		return os.WriteFile(filepath.Join(ws, p), []byte(content), 0o644)
	}
	return read, write
}

func renameArgs() map[string]any {
	return map[string]any{"path": "a.txt", "symbol": "foo", "new_name": "bar"}
}

func TestHandleLSPRename_AppliesAllFiles(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	testgit.WriteFile(t, ws, "a.txt", "foo := 1\nx := foo\n")
	testgit.WriteFile(t, ws, "b.txt", "y := foo\n")

	uriA := "file://" + filepath.Join(ws, "a.txt")
	uriB := "file://" + filepath.Join(ws, "b.txt")
	fake := &fakeLSPManager{
		enabled: true,
		responses: map[string]json.RawMessage{
			"textDocument/prepareRename": json.RawMessage(
				`{"jsonrpc":"2.0","id":1,"result":{"start":{"line":0,"character":0},"end":{"line":0,"character":3}}}`),
			"textDocument/rename": json.RawMessage(fmt.Sprintf(`{
				"jsonrpc":"2.0","id":2,
				"result":{"changes":{
					%q:[
						{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":3}},"newText":"bar"},
						{"range":{"start":{"line":1,"character":5},"end":{"line":1,"character":8}},"newText":"bar"}
					],
					%q:[
						{"range":{"start":{"line":0,"character":5},"end":{"line":0,"character":8}},"newText":"bar"}
					]
				}}
			}`, uriA, uriB)),
		},
	}
	te := newBareTestExecutor(t)
	te.lspMgr = fake
	read, write := osReadWrite(ws)

	result, err := te.tools.handleLSPRename(context.Background(), renameArgs(), ws, read, write)
	if err != nil {
		t.Fatalf("handleLSPRename: %v", err)
	}

	// prepareRename must precede rename
	if len(fake.methods) != 2 || fake.methods[0] != "textDocument/prepareRename" || fake.methods[1] != "textDocument/rename" {
		t.Errorf("method order = %v, want [textDocument/prepareRename textDocument/rename]", fake.methods)
	}

	// Both files renamed on disk
	checkContent := func(p, want string) {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(ws, p))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
	}
	checkContent("a.txt", "bar := 1\nx := bar\n")
	checkContent("b.txt", "y := bar\n")

	// Result summarizes the files
	if !strings.Contains(result, "a.txt") || !strings.Contains(result, "b.txt") {
		t.Errorf("result should name both files: %q", result)
	}
	if !strings.Contains(result, "foo") || !strings.Contains(result, "bar") {
		t.Errorf("result should name old and new symbol: %q", result)
	}
}

func TestHandleLSPRename_NotRenamable(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	testgit.WriteFile(t, ws, "a.txt", "foo := 1\n")
	before, _ := os.ReadFile(filepath.Join(ws, "a.txt"))

	fake := &fakeLSPManager{
		enabled: true,
		responses: map[string]json.RawMessage{
			"textDocument/prepareRename": json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":null}`),
		},
	}
	te := newBareTestExecutor(t)
	te.lspMgr = fake
	read, write := osReadWrite(ws)

	result, err := te.tools.handleLSPRename(context.Background(), renameArgs(), ws, read, write)
	if err != nil {
		t.Fatalf("not-renamable should be an informative result, not an error: %v", err)
	}
	if !strings.Contains(strings.ToLower(result), "not renamable") {
		t.Errorf("result should say not renamable: %q", result)
	}
	if len(fake.methods) != 1 {
		t.Errorf("rename must not be called after null prepareRename; methods = %v", fake.methods)
	}
	after, _ := os.ReadFile(filepath.Join(ws, "a.txt"))
	if string(before) != string(after) {
		t.Error("file must be untouched when not renamable")
	}
}

func TestHandleLSPRename_NullResult(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	testgit.WriteFile(t, ws, "a.txt", "foo := 1\n")

	fake := &fakeLSPManager{
		enabled: true,
		responses: map[string]json.RawMessage{
			"textDocument/prepareRename": json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"start":{"line":0,"character":0},"end":{"line":0,"character":3}}}`),
			"textDocument/rename":        json.RawMessage(`{"jsonrpc":"2.0","id":2,"result":null}`),
		},
	}
	te := newBareTestExecutor(t)
	te.lspMgr = fake
	read, write := osReadWrite(ws)

	result, err := te.tools.handleLSPRename(context.Background(), renameArgs(), ws, read, write)
	if err != nil {
		t.Fatalf("null rename result should be informative, not an error: %v", err)
	}
	if !strings.Contains(strings.ToLower(result), "no edits") {
		t.Errorf("result should report no edits: %q", result)
	}
	got, _ := os.ReadFile(filepath.Join(ws, "a.txt"))
	if string(got) != "foo := 1\n" {
		t.Error("file must be untouched on null rename result")
	}
}

func TestHandleLSPRename_LSPError(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	testgit.WriteFile(t, ws, "a.txt", "foo := 1\n")

	fake := &fakeLSPManager{
		enabled: true,
		responses: map[string]json.RawMessage{
			"textDocument/prepareRename": json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"start":{"line":0,"character":0},"end":{"line":0,"character":3}}}`),
			"textDocument/rename":        json.RawMessage(`{"jsonrpc":"2.0","id":2,"error":{"code":-32602,"message":"name taken"}}`),
		},
	}
	te := newBareTestExecutor(t)
	te.lspMgr = fake
	read, write := osReadWrite(ws)

	_, err := te.tools.handleLSPRename(context.Background(), renameArgs(), ws, read, write)
	if err == nil {
		t.Fatal("expected error for LSP error envelope")
	}
	if !strings.Contains(err.Error(), "name taken") {
		t.Errorf("error should carry the server message: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(ws, "a.txt"))
	if string(got) != "foo := 1\n" {
		t.Error("file must be untouched on LSP error")
	}
}

func TestHandleLSPRename_OutsideWorkspace(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	testgit.WriteFile(t, ws, "a.txt", "foo := 1\n")
	outside := filepath.Join(t.TempDir(), "outside.txt")

	fake := &fakeLSPManager{
		enabled: true,
		responses: map[string]json.RawMessage{
			"textDocument/prepareRename": json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"start":{"line":0,"character":0},"end":{"line":0,"character":3}}}`),
			"textDocument/rename": json.RawMessage(fmt.Sprintf(`{
				"jsonrpc":"2.0","id":2,
				"result":{"changes":{%q:[
					{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":3}},"newText":"bar"}
				]}}
			}`, "file://"+outside)),
		},
	}
	te := newBareTestExecutor(t)
	te.lspMgr = fake
	read, write := osReadWrite(ws)

	_, err := te.tools.handleLSPRename(context.Background(), renameArgs(), ws, read, write)
	if err == nil {
		t.Fatal("expected error for edit target outside workspace")
	}
	if !strings.Contains(err.Error(), "outside workspace") {
		t.Errorf("error should name the cause: %v", err)
	}
}

func TestHandleLSPRename_RequiredArgs(t *testing.T) {
	t.Parallel()
	te := newBareTestExecutor(t)
	te.lspMgr = &fakeLSPManager{enabled: true}
	read, write := osReadWrite(t.TempDir())
	for _, args := range []map[string]any{
		{"symbol": "foo", "new_name": "bar"},
		{"path": "a.txt", "new_name": "bar"},
		{"path": "a.txt", "symbol": "foo"},
	} {
		if _, err := te.tools.handleLSPRename(context.Background(), args, t.TempDir(), read, write); err == nil {
			t.Errorf("expected error for args %v", args)
		}
	}
}

func TestHandleLSPRename_NoLSPManager(t *testing.T) {
	t.Parallel()
	te := newBareTestExecutor(t) // lspMgr nil
	read, write := osReadWrite(t.TempDir())
	if _, err := te.tools.handleLSPRename(context.Background(), renameArgs(), t.TempDir(), read, write); err == nil {
		t.Error("expected error when LSP manager is nil")
	}
}

func TestHandleLSPRename_ToolCallsDisabled(t *testing.T) {
	t.Parallel()
	te := newBareTestExecutor(t)
	te.lspMgr = &fakeLSPManager{enabled: false}
	read, write := osReadWrite(t.TempDir())
	if _, err := te.tools.handleLSPRename(context.Background(), renameArgs(), t.TempDir(), read, write); err == nil {
		t.Error("expected error when LSP tool calls are disabled")
	}
}

func TestHandleLSPRename_SymbolNotFound(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	testgit.WriteFile(t, ws, "a.txt", "baz := 1\n")
	te := newBareTestExecutor(t)
	te.lspMgr = &fakeLSPManager{enabled: true}
	read, write := osReadWrite(ws)
	result, err := te.tools.handleLSPRename(context.Background(), renameArgs(), ws, read, write)
	if err != nil {
		t.Fatalf("missing symbol should be informative, not an error: %v", err)
	}
	if !strings.Contains(result, "not found") {
		t.Errorf("result should say not found: %q", result)
	}
}
