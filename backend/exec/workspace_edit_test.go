package exec

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func pos(line, ch int) lspPosition { return lspPosition{Line: line, Character: ch} }

func rng(s, e lspPosition) lspRange { return lspRange{Start: s, End: e} }

// ─── applyTextEdits ──────────────────────────────────────────────────────────

func TestApplyTextEdits_Single(t *testing.T) {
	content := "foo bar baz"
	edits := []lspTextEdit{{Range: rng(pos(0, 4), pos(0, 7)), NewText: "baz"}}
	got, err := applyTextEdits(content, edits)
	if err != nil {
		t.Fatal(err)
	}
	if got != "foo baz baz" {
		t.Errorf("got %q, want %q", got, "foo baz baz")
	}
}

func TestApplyTextEdits_MultipleOutOfOrder(t *testing.T) {
	// Edits delivered in reverse order of position — the classic offset
	// invalidation trap. Both must land correctly.
	content := "aaa bbb ccc"
	edits := []lspTextEdit{
		{Range: rng(pos(0, 8), pos(0, 11)), NewText: "DDD"}, // ccc -> DDD (later first)
		{Range: rng(pos(0, 4), pos(0, 7)), NewText: "BBB"},  // bbb -> BBB
	}
	got, err := applyTextEdits(content, edits)
	if err != nil {
		t.Fatal(err)
	}
	if got != "aaa BBB DDD" {
		t.Errorf("got %q, want %q", got, "aaa BBB DDD")
	}
}

func TestApplyTextEdits_MultiLine(t *testing.T) {
	content := "line one\nline two\nline three"
	// Delete "line two\n" entirely (range spans two lines)
	edits := []lspTextEdit{{Range: rng(pos(1, 0), pos(2, 0)), NewText: ""}}
	got, err := applyTextEdits(content, edits)
	if err != nil {
		t.Fatal(err)
	}
	if got != "line one\nline three" {
		t.Errorf("got %q", got)
	}
}

func TestApplyTextEdits_EmptySet(t *testing.T) {
	got, err := applyTextEdits("unchanged", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "unchanged" {
		t.Errorf("got %q", got)
	}
}

func TestApplyTextEdits_Insertion(t *testing.T) {
	content := "ab"
	edits := []lspTextEdit{{Range: rng(pos(0, 1), pos(0, 1)), NewText: "XYZ"}}
	got, err := applyTextEdits(content, edits)
	if err != nil {
		t.Fatal(err)
	}
	if got != "aXYZb" {
		t.Errorf("got %q", got)
	}
}

func TestApplyTextEdits_AtEOF(t *testing.T) {
	content := "end"
	edits := []lspTextEdit{{Range: rng(pos(0, 3), pos(0, 3)), NewText: "!"}}
	got, err := applyTextEdits(content, edits)
	if err != nil {
		t.Fatal(err)
	}
	if got != "end!" {
		t.Errorf("got %q", got)
	}
}

func TestApplyTextEdits_InvalidRange(t *testing.T) {
	edits := []lspTextEdit{{Range: rng(pos(1, 0), pos(0, 0)), NewText: "x"}}
	if _, err := applyTextEdits("a\nb", edits); err == nil {
		t.Error("expected error for end-before-start range")
	}
}

func TestApplyTextEdits_Overlapping(t *testing.T) {
	content := "0123456789"
	edits := []lspTextEdit{
		{Range: rng(pos(0, 2), pos(0, 5)), NewText: "A"},
		{Range: rng(pos(0, 4), pos(0, 7)), NewText: "B"},
	}
	if _, err := applyTextEdits(content, edits); err == nil {
		t.Error("expected error for overlapping edits")
	}
}

func TestApplyTextEdits_OutOfBoundsLine(t *testing.T) {
	edits := []lspTextEdit{{Range: rng(pos(5, 0), pos(5, 1)), NewText: "x"}}
	if _, err := applyTextEdits("one line", edits); err == nil {
		t.Error("expected error for line past end of file")
	}
}

// ─── parseWorkspaceEdit ──────────────────────────────────────────────────────

func TestParseWorkspaceEdit_ChangesMap(t *testing.T) {
	resp := json.RawMessage(`{
		"jsonrpc": "2.0", "id": 7,
		"result": {
			"changes": {
				"file:///w/a.ts": [
					{"range": {"start": {"line": 0, "character": 9}, "end": {"line": 0, "character": 21}}, "newText": "loadDraftText"}
				],
				"file:///w/b.ts": [
					{"range": {"start": {"line": 0, "character": 9}, "end": {"line": 0, "character": 21}}, "newText": "loadDraftText"},
					{"range": {"start": {"line": 3, "character": 11}, "end": {"line": 3, "character": 23}}, "newText": "loadDraftText"}
				]
			}
		}
	}`)
	edits, err := parseWorkspaceEdit(resp)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 2 {
		t.Fatalf("want 2 files, got %d", len(edits))
	}
	if n := len(edits["file:///w/b.ts"]); n != 2 {
		t.Errorf("want 2 edits in b.ts, got %d", n)
	}
	if got := edits["file:///w/a.ts"][0].NewText; got != "loadDraftText" {
		t.Errorf("newText = %q", got)
	}
}

func TestParseWorkspaceEdit_DocumentChanges(t *testing.T) {
	resp := json.RawMessage(`{
		"jsonrpc": "2.0", "id": 8,
		"result": {
			"documentChanges": [
				{"kind": "text",
				 "textDocument": {"uri": "file:///w/a.ts", "version": 3},
				 "edits": [
					 {"range": {"start": {"line": 0, "character": 9}, "end": {"line": 0, "character": 21}}, "newText": "y"}
				 ]}
			]
		}
	}`)
	edits, err := parseWorkspaceEdit(resp)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(edits["file:///w/a.ts"]); n != 1 {
		t.Errorf("want 1 edit, got %d", n)
	}
}

func TestParseWorkspaceEdit_UnknownChangeKind(t *testing.T) {
	resp := json.RawMessage(`{
		"jsonrpc": "2.0", "id": 9,
		"result": {
			"documentChanges": [
				{"kind": "rename", "oldUri": "file:///w/a.ts", "newUri": "file:///w/a2.ts"}
			]
		}
	}`)
	_, err := parseWorkspaceEdit(resp)
	if err == nil {
		t.Fatal("expected error for unsupported documentChanges kind")
	}
	if !strings.Contains(err.Error(), "rename") {
		t.Errorf("error should name the unsupported kind, got: %v", err)
	}
}

func TestParseWorkspaceEdit_NullResult(t *testing.T) {
	resp := json.RawMessage(`{"jsonrpc": "2.0", "id": 10, "result": null}`)
	edits, err := parseWorkspaceEdit(resp)
	if err != nil {
		t.Fatal(err)
	}
	if edits != nil {
		t.Errorf("want nil for null result, got %v", edits)
	}
}

func TestParseWorkspaceEdit_LSPError(t *testing.T) {
	resp := json.RawMessage(`{"jsonrpc": "2.0", "id": 11, "error": {"code": -32602, "message": "Invalid newName"}}`)
	_, err := parseWorkspaceEdit(resp)
	if err == nil {
		t.Fatal("expected error for LSP error envelope")
	}
	if !strings.Contains(err.Error(), "-32602") || !strings.Contains(err.Error(), "Invalid newName") {
		t.Errorf("error should carry code+message, got: %v", err)
	}
}

func TestParseWorkspaceEdit_UnexpectedShape(t *testing.T) {
	resp := json.RawMessage(`{"jsonrpc": "2.0", "id": 12, "result": {"foo": "bar"}}`)
	if _, err := parseWorkspaceEdit(resp); err == nil {
		t.Error("expected error for result without changes/documentChanges")
	}
}

// ─── workspaceEditTargets ────────────────────────────────────────────────────

func TestWorkspaceEditTargets_AllInside(t *testing.T) {
	ws := "/home/u/work"
	uris := []string{"file:///home/u/work/src/a.ts", "file:///home/u/work/b.ts"}
	rel, err := workspaceEditTargets(ws, uris)
	if err != nil {
		t.Fatal(err)
	}
	if rel["file:///home/u/work/src/a.ts"] != "src/a.ts" || rel["file:///home/u/work/b.ts"] != "b.ts" {
		t.Errorf("got %v", rel)
	}
}

func TestWorkspaceEditTargets_OutsideWorkspace(t *testing.T) {
	ws := "/home/u/work"
	uris := []string{"file:///home/u/work/a.ts", "file:///home/u/other/b.ts"}
	if _, err := workspaceEditTargets(ws, uris); err == nil {
		t.Fatal("expected error for target outside workspace")
	}
}

func TestWorkspaceEditTargets_PrefixTrick(t *testing.T) {
	// /home/u/work-evil must NOT pass as inside /home/u/work
	ws := "/home/u/work"
	uris := []string{"file:///home/u/work-evil/a.ts"}
	if _, err := workspaceEditTargets(ws, uris); err == nil {
		t.Fatal("expected error for sibling path sharing a prefix")
	}
}

func TestWorkspaceEditTargets_NonFileURI(t *testing.T) {
	ws := "/home/u/work"
	if _, err := workspaceEditTargets(ws, []string{"http://example.com/a.ts"}); err == nil {
		t.Fatal("expected error for non-file URI")
	}
}

func TestWorkspaceEditTargets_ErrorsWrap(t *testing.T) {
	ws := "/home/u/work"
	_, err := workspaceEditTargets(ws, []string{"file:///home/u/other/b.ts"})
	if !errors.Is(err, errEditOutsideWorkspace) {
		t.Errorf("expected errEditOutsideWorkspace, got: %v", err)
	}
}
