package server

// The LSP workspace string comes from the browser and ends up as the bind-mount source of the
// language server's container, so what it may name is a jail, not a prefix comparison.

import (
	"os"
	"path/filepath"
	"testing"

	"kanban/backend/types"
)

func TestLSPWorkspaceAllowed(t *testing.T) {
	s := testServer(t)
	projectDir := t.TempDir()
	if err := s.store.CreateProject(&types.Project{ID: "lsp-ws-proj", Name: "WS", Path: projectDir}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	workspaceDir := s.configStore.WorkspaceDir()
	secret := t.TempDir()
	link := filepath.Join(projectDir, "link")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatalf("plant symlink inside the project: %v", err)
	}
	// A sibling directory whose name starts with the project's: not inside it.
	nearMiss := projectDir + "-evil"
	if err := os.MkdirAll(nearMiss, 0o700); err != nil {
		t.Fatalf("create near-miss dir: %v", err)
	}

	cases := []struct {
		name string
		path string
		want bool
		why  string
	}{
		{"project root", projectDir, true, "a project's own checkout is a workspace"},
		{"project subdir", filepath.Join(projectDir, "cmd", "tool"), true, "descendants of a project are workspaces"},
		{"workspace dir", workspaceDir, true, "task worktrees live under it"},
		{"outside any root", secret, false, "an arbitrary host directory is not a workspace"},
		{"prefix with no separator", nearMiss, false, "a directory merely named like a project is not inside it"},
		{"symlink out of the project", link, false, "the resolved path is what gets mounted, so the link must not pass"},
		{"relative path", "relative/workspace", false, "a relative workspace names nothing the server can jail"},
	}

	for _, tc := range cases {
		if got := s.lspWorkspaceAllowed(tc.path); got != tc.want {
			t.Errorf("lspWorkspaceAllowed(%q) = %v, want %v: %s", tc.path, got, tc.want, tc.why)
		}
	}
}
