package exec

import (
	"encoding/json/jsontext"
	"errors"
	"strings"
	"testing"
)

func pos(line, ch int) lspPosition { return lspPosition{Line: line, Character: ch} }

func rng(s, e lspPosition) lspRange { return lspRange{Start: s, End: e} }

// ─── applyTextEdits ──────────────────────────────────────────────────────────

// applyTextEdits finds every position in the original content and writes the result in one pass, so
// an edit delivered later must not be measured against a string an earlier edit already changed —
// the offset-invalidation trap a language server's WorkspaceEdit lands in otherwise. A range that
// cannot be honoured is refused as a whole call: a rename that applied half its edits would leave the
// task holding a broken file.
func TestApplyTextEdits(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
		edits   []lspTextEdit
		want    string
		wantErr string
	}{
		{
			name:    "one replacement",
			content: "foo bar baz",
			edits:   []lspTextEdit{{Range: rng(pos(0, 4), pos(0, 7)), NewText: "baz"}},
			want:    "foo baz baz",
		},
		{
			name:    "edits delivered in reverse order of position",
			content: "aaa bbb ccc",
			edits: []lspTextEdit{
				{Range: rng(pos(0, 8), pos(0, 11)), NewText: "DDD"},
				{Range: rng(pos(0, 4), pos(0, 7)), NewText: "BBB"},
			},
			want: "aaa BBB DDD",
		},
		{
			name:    "a range spanning two lines",
			content: "line one\nline two\nline three",
			edits:   []lspTextEdit{{Range: rng(pos(1, 0), pos(2, 0)), NewText: ""}},
			want:    "line one\nline three",
		},
		{
			name:    "no edits",
			content: "unchanged",
			edits:   nil,
			want:    "unchanged",
		},
		{
			name:    "an empty range inserts",
			content: "ab",
			edits:   []lspTextEdit{{Range: rng(pos(0, 1), pos(0, 1)), NewText: "XYZ"}},
			want:    "aXYZb",
		},
		{
			name:    "an insertion at the end of the file",
			content: "end",
			edits:   []lspTextEdit{{Range: rng(pos(0, 3), pos(0, 3)), NewText: "!"}},
			want:    "end!",
		},
		{
			name:    "end before start",
			content: "a\nb",
			edits:   []lspTextEdit{{Range: rng(pos(1, 0), pos(0, 0)), NewText: "x"}},
			wantErr: "precedes",
		},
		{
			name:    "overlapping ranges",
			content: "0123456789",
			edits: []lspTextEdit{
				{Range: rng(pos(0, 2), pos(0, 5)), NewText: "A"},
				{Range: rng(pos(0, 4), pos(0, 7)), NewText: "B"},
			},
			wantErr: "overlap",
		},
		{
			name:    "a line past the end of the file",
			content: "one line",
			edits:   []lspTextEdit{{Range: rng(pos(5, 0), pos(5, 1)), NewText: "x"}},
			wantErr: "out of range",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := applyTextEdits(tc.content, tc.edits)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error saying %q, got result %q", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not say %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("applyTextEdits: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// ─── parseWorkspaceEdit ──────────────────────────────────────────────────────

// A language server may answer a rename with either shape of WorkspaceEdit, with nothing at all, or
// with an error envelope. Each is read here and nowhere else, and an unsupported change kind is
// refused by name rather than skipped — silently dropping a rename file is how a rename lands half
// applied.
func TestParseWorkspaceEdit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		resp jsontext.Value
		want map[string][]lspTextEdit
		// wantErr is a substring of the refusal; wantErrIs is the sentinel it must wrap.
		wantErr   string
		wantErrIs error
	}{
		{
			name: "the changes map",
			resp: jsontext.Value(`{
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
	}`),
			want: map[string][]lspTextEdit{
				"file:///w/a.ts": {{Range: rng(pos(0, 9), pos(0, 21)), NewText: "loadDraftText"}},
				"file:///w/b.ts": {
					{Range: rng(pos(0, 9), pos(0, 21)), NewText: "loadDraftText"},
					{Range: rng(pos(3, 11), pos(3, 23)), NewText: "loadDraftText"},
				},
			},
		},
		{
			name: "documentChanges",
			resp: jsontext.Value(`{
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
	}`),
			want: map[string][]lspTextEdit{
				"file:///w/a.ts": {{Range: rng(pos(0, 9), pos(0, 21)), NewText: "y"}},
			},
		},
		{
			name: "an unsupported change kind is named",
			resp: jsontext.Value(`{
		"jsonrpc": "2.0", "id": 9,
		"result": {
			"documentChanges": [
				{"kind": "rename", "oldUri": "file:///w/a.ts", "newUri": "file:///w/a2.ts"}
			]
		}
	}`),
			wantErr: "rename",
		},
		{
			name: "a null result is no edits",
			resp: jsontext.Value(`{"jsonrpc": "2.0", "id": 10, "result": null}`),
			want: nil,
		},
		{
			name:    "an error envelope carries code and message",
			resp:    jsontext.Value(`{"jsonrpc": "2.0", "id": 11, "error": {"code": -32602, "message": "Invalid newName"}}`),
			wantErr: "-32602",
		},
		{
			name:    "a result with neither shape is refused",
			resp:    jsontext.Value(`{"jsonrpc": "2.0", "id": 12, "result": {"foo": "bar"}}`),
			wantErr: "no changes or documentChanges",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			edits, err := parseWorkspaceEdit(tc.resp)
			if tc.wantErr != "" || tc.wantErrIs != nil {
				if err == nil {
					t.Fatalf("expected an error, got %v", edits)
				}
				if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not say %q", err, tc.wantErr)
				}
				if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
					t.Errorf("error %q does not wrap %v", err, tc.wantErrIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseWorkspaceEdit: %v", err)
			}
			if len(edits) != len(tc.want) {
				t.Fatalf("got %d files, want %d: %v", len(edits), len(tc.want), edits)
			}
			for uri, wantEdits := range tc.want {
				got := edits[uri]
				if len(got) != len(wantEdits) {
					t.Errorf("%s: got %d edits, want %d", uri, len(got), len(wantEdits))
					continue
				}
				for i := range got {
					if got[i] != wantEdits[i] {
						t.Errorf("%s edit %d = %+v, want %+v", uri, i, got[i], wantEdits[i])
					}
				}
			}
		})
	}
}

// ─── workspaceEditTargets ────────────────────────────────────────────────────

// A language server names its own files, so every URI of the answer is jailed to the workspace
// before a byte is written: a sibling directory sharing the workspace's prefix is not inside it, and
// a non-file scheme is not a path at all.
func TestWorkspaceEditTargets(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		uris      []string
		want      map[string]string
		wantErr   string
		wantErrIs error
	}{
		{
			name: "every file inside the workspace",
			uris: []string{"file:///home/u/work/src/a.ts", "file:///home/u/work/b.ts"},
			want: map[string]string{
				"file:///home/u/work/src/a.ts": "src/a.ts",
				"file:///home/u/work/b.ts":     "b.ts",
			},
		},
		{
			name:      "a sibling directory sharing the workspace's prefix",
			uris:      []string{"file:///home/u/work-evil/a.ts"},
			wantErrIs: errEditOutsideWorkspace,
		},
		{
			name:      "another tree entirely",
			uris:      []string{"file:///home/u/other/b.ts"},
			wantErrIs: errEditOutsideWorkspace,
		},
		{
			name:    "a URI that is not a file",
			uris:    []string{"http://example.com/a.ts"},
			wantErr: "unsupported edit URI scheme",
		},
	}

	const ws = "/home/u/work"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rel, err := workspaceEditTargets(ws, tc.uris)
			if tc.wantErr != "" || tc.wantErrIs != nil {
				if err == nil {
					t.Fatalf("expected an error, got %v", rel)
				}
				if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not say %q", err, tc.wantErr)
				}
				if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
					t.Errorf("error %q does not wrap %v", err, tc.wantErrIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("workspaceEditTargets: %v", err)
			}
			if len(rel) != len(tc.want) {
				t.Fatalf("got %v, want %v", rel, tc.want)
			}
			for uri, want := range tc.want {
				if rel[uri] != want {
					t.Errorf("%s = %q, want %q", uri, rel[uri], want)
				}
			}
		})
	}
}
