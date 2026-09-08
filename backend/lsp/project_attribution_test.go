package lsp

// Which project a workspace belongs to decides whose Sandbox Configuration the language server
// gets, so the match is by directory containment, not by string prefix.

import (
	"path/filepath"
	"testing"

	"kanban/backend/types"
)

type attributionProjects struct{ paths []string }

func (p attributionProjects) ListProjects() ([]types.Project, error) {
	out := make([]types.Project, 0, len(p.paths))
	for _, path := range p.paths {
		out = append(out, types.Project{ID: filepath.Base(path), Name: filepath.Base(path), Path: path})
	}
	return out, nil
}

func TestFindProjectForWorkspaceMatchesDirectoriesNotPrefixes(t *testing.T) {
	m := &ExtensionLSPManager{projects: attributionProjects{paths: []string{"/home/dev/proj"}}}

	cases := []struct {
		workspace string
		wantID    string // "" means no project owns it
		why       string
	}{
		{"/home/dev/proj", "proj", "the checkout itself"},
		{"/home/dev/proj/cmd/tool", "proj", "a descendant of the checkout"},
		{"/home/dev/proj-evil", "", "a sibling whose name merely starts with the project's"},
		{"/home/dev/proj_evil/deep", "", "same, deeper"},
		{"/home/dev/other", "", "unrelated"},
	}

	for _, tc := range cases {
		project, err := m.findProjectForWorkspace(tc.workspace)
		if tc.wantID == "" {
			if err == nil {
				t.Errorf("findProjectForWorkspace(%q) claimed project %q: %s", tc.workspace, project.ID, tc.why)
			}
			continue
		}
		if err != nil {
			t.Errorf("findProjectForWorkspace(%q): %v (%s)", tc.workspace, err, tc.why)
			continue
		}
		if project.ID != tc.wantID {
			t.Errorf("findProjectForWorkspace(%q) = %q, want %q: %s", tc.workspace, project.ID, tc.wantID, tc.why)
		}
	}
}
