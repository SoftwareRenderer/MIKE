package lsp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"kanban/backend/extensions"
	"kanban/backend/fsutil"
)

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

// The wire languageId is the extension's declaration, not the routing language: tsserver needs the
// ts/tsx/js/jsx difference and keeps the first didOpen's verdict for the URI's whole life.
func TestServerConfigLanguageIDFor(t *testing.T) {
	ts := &ServerConfig{
		Language: "typescript",
		FileTypes: []extensions.LSPFileType{
			{Extension: "ts", LanguageID: "typescript"},
			{Extension: "tsx", LanguageID: "typescriptreact"},
			{Extension: "js", LanguageID: "javascript"},
			{Extension: "jsx", LanguageID: "javascriptreact"},
		},
	}
	goCfg := &ServerConfig{Language: "go", FileTypes: []extensions.LSPFileType{{Extension: "go", LanguageID: "go"}}}

	tests := []struct {
		name string
		cfg  *ServerConfig
		path string
		want string
	}{
		{"tsx is typescriptreact, not the routing language", ts, "src/App.tsx", "typescriptreact"},
		{"jsx is javascriptreact", ts, "src/App.jsx", "javascriptreact"},
		{"plain ts", ts, "src/lib/util.ts", "typescript"},
		{"js is javascript, so tsserver stops type-checking it", ts, "legacy/app.js", "javascript"},
		{"upper-case path", ts, "src/APP.TSX", "typescriptreact"},
		{"a suffix the manifest declares nothing for falls back to the language", ts, "notes.txt", "typescript"},
		{"other extensions are untouched", goCfg, "main.go", "go"},
		{"nil config answers nothing", nil, "main.go", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.LanguageIDFor(tt.path); got != tt.want {
				t.Errorf("LanguageIDFor(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// The document sync's read is the server's own, so a path walking out of the workspace is stopped
// here or it happens.
func TestDocSyncPathStaysInsideTheWorkspace(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "worktrees")
	// The worktree layout a tool's relative path is joined onto: <workspace>/<project>/<task>.
	worktree := filepath.Join(workspace, "frost", "amber")
	if err := os.MkdirAll(filepath.Join(worktree, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(home, "secrets")
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(sibling, "id_rsa")
	if err := os.WriteFile(secret, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := docSyncPath(workspace, "frost/amber/src/main.go")
	if err != nil {
		t.Fatalf("a file inside the workspace was refused: %v", err)
	}
	if got != filepath.Join(worktree, "src", "main.go") {
		t.Errorf("docSyncPath = %q, want the joined workspace path", got)
	}

	for _, escape := range []string{
		"../../secrets/id_rsa",
		"frost/amber/../../../secrets/id_rsa",
	} {
		if got, err := docSyncPath(workspace, escape); err == nil {
			t.Errorf("docSyncPath(%q) = %q, want a refusal", escape, got)
		} else if !errors.Is(err, fsutil.ErrOutside) {
			t.Errorf("docSyncPath(%q) failed with %v, want ErrOutside", escape, err)
		}
	}

	// An absolute path is not an escape: filepath.Join drops what precedes it.
	if got, err := docSyncPath(workspace, "/etc/passwd"); err != nil || got != filepath.Join(workspace, "etc", "passwd") {
		t.Errorf(`docSyncPath("/etc/passwd") = %q, %v; want it resolved inside the workspace`, got, err)
	}

	// A link in the worktree escapes only after the join, so the lexical check would pass it.
	link := filepath.Join(worktree, "src", "link.go")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if got, err := docSyncPath(workspace, "frost/amber/src/link.go"); err == nil {
		t.Errorf("a symlink out of the workspace was read anyway: %q", got)
	}
}
