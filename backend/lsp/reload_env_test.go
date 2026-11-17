package lsp

// A language server over a bind mount gets no file-watch events, so the only thing that tells it
// the project changed is the turn's own changed-file list. These pin the two halves of that:
// which file means "re-read the project", and ending *that* workspace's connection — no other's.

import (
	"testing"

	"mike/backend/extensions"
)

func TestReloadNeeded(t *testing.T) {
	cases := []struct {
		name     string
		declared []string
		changed  string
		want     bool
	}{
		{"declared nothing", nil, "go.mod", false},
		{"exact match", []string{"go.mod"}, "go.mod", true},
		{"nested module of the same name", []string{"go.mod"}, "internal/foo/go.mod", true},
		{"cleaned path", []string{"go.mod"}, "./go.mod", true},
		{"lockfile declared by path", []string{"pnpm-lock.yaml"}, "pnpm-lock.yaml", true},
		{"unrelated file", []string{"go.mod"}, "main.go", false},
		{"directory itself", []string{"go.mod"}, "go", false},
		{"empty or dot", []string{"go.mod"}, "", false},
		{"blank declaration is skipped", []string{"  "}, "go.mod", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reloadNeeded(tc.declared, tc.changed); got != tc.want {
				t.Errorf("reloadNeeded(%v, %q) = %v, want %v", tc.declared, tc.changed, got, tc.want)
			}
		})
	}
}

// TestFilesChangedRecyclesOnlyTheDeclaredFileOfItsWorkspace holds the other half: an ordinary source
// file recycles nothing — that would be a cold start every turn — and when the declared file does
// change, only the workspace whose project changed loses its connection, marked dead so its holders
// re-attach rather than reusing a server with the old graph.
func TestFilesChangedRecyclesOnlyTheDeclaredFileOfItsWorkspace(t *testing.T) {
	m := NewExtensionLSPManager(nil, nil, nil)
	ext := &extensions.Extension{ID: "go-team-gopls"}
	ext.SandboxConfig.ReloadFiles = []string{"go.mod"}

	changed := newSandboxClient(nil, "/ws/a", "go", ext.ID, map[string]string{})
	changed.extension = ext
	untouched := newSandboxClient(nil, "/ws/b", "go", ext.ID, map[string]string{})
	untouched.extension = ext
	m.clients[processKey("/ws/a", "go")] = changed
	m.clients[processKey("/ws/b", "go")] = untouched

	for _, files := range [][]string{{"main.go"}, nil} {
		if m.FilesChanged("/ws/a", files) {
			t.Errorf("FilesChanged(%v) recycled the server; only the declared file changes the project", files)
		}
		if changed.dead.Load() {
			t.Fatalf("the connection ended for %v, which does not change the project", files)
		}
	}

	if !m.FilesChanged("/ws/a", []string{"main.go", "go.mod"}) {
		t.Fatal("FilesChanged reported nothing was recycled")
	}
	if !changed.dead.Load() {
		t.Error("the changed workspace's connection must be marked dead so its holders re-attach")
	}
	if _, ok := m.clients[processKey("/ws/a", "go")]; ok {
		t.Error("the recycled connection is still installed — its holders would reuse a server with the old graph")
	}
	if untouched.dead.Load() {
		t.Error("another workspace's connection was ended — it did not change its project")
	}
	if _, ok := m.clients[processKey("/ws/b", "go")]; !ok {
		t.Error("the untouched workspace lost its connection")
	}
}
