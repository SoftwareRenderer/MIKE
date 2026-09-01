package lsp

import "testing"

func TestDecideDocSync(t *testing.T) {
	tests := []struct {
		name        string
		opened      bool
		lastSent    string
		onDisk      string
		lastVersion int
		want        docSyncAction
	}{
		{
			name:   "never opened -> didOpen version 1",
			opened: false,
			onDisk: "a",
			want:   docSyncAction{kind: "open", version: 1},
		},
		{
			name:        "opened, unchanged -> no sync",
			opened:      true,
			lastSent:    "a",
			onDisk:      "a",
			lastVersion: 1,
			want:        docSyncAction{},
		},
		{
			name:        "opened, content changed -> didChange with bumped version",
			opened:      true,
			lastSent:    "a",
			onDisk:      "b",
			lastVersion: 1,
			want:        docSyncAction{kind: "change", version: 2},
		},
		{
			name:        "multiple changes keep bumping version",
			opened:      true,
			lastSent:    "b",
			onDisk:      "c",
			lastVersion: 3,
			want:        docSyncAction{kind: "change", version: 4},
		},
		{
			name:        "empty content counts as a change",
			opened:      true,
			lastSent:    "a",
			onDisk:      "",
			lastVersion: 1,
			want:        docSyncAction{kind: "change", version: 2},
		},
		{
			name:        "never opened wins even when content matches a stale record",
			opened:      false,
			lastSent:    "a",
			onDisk:      "a",
			lastVersion: 7,
			want:        docSyncAction{kind: "open", version: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideDocSync(tt.opened, tt.lastSent, tt.onDisk, tt.lastVersion)
			if got != tt.want {
				t.Errorf("decideDocSync(%v, %q, %q, %d) = %+v, want %+v", tt.opened, tt.lastSent, tt.onDisk, tt.lastVersion, got, tt.want)
			}
		})
	}
}

func TestWireLanguageId(t *testing.T) {
	tests := []struct {
		name     string
		language string
		filePath string
		want     string
	}{
		{"tsx is typescriptreact, not typescript", "typescript", "src/App.tsx", "typescriptreact"},
		{"jsx is javascriptreact", "typescript", "src/App.jsx", "javascriptreact"},
		{"plain ts", "typescript", "src/lib/util.ts", "typescript"},
		{"mts and cts stay typescript", "typescript", "src/decls.d.mts", "typescript"},
		{"js is javascript so tsserver stops type-checking it", "typescript", "legacy/app.js", "javascript"},
		{"jsx upper case", "typescript", "src/APP.JSX", "javascriptreact"},
		{"manifest language survives for an unknown extension", "typescript", "notes.txt", "typescript"},
		{"other languages are untouched", "go", "main.go", "go"},
		{"svelte is untouched", "svelte", "App.svelte", "svelte"},
		{"no language at all is passed through", "", "main.go", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wireLanguageId(tt.language, tt.filePath); got != tt.want {
				t.Errorf("wireLanguageId(%q, %q) = %q, want %q", tt.language, tt.filePath, got, tt.want)
			}
		})
	}
}
