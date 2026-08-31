package server

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"kanban/backend/types"
)

// sandboxFSProject creates a project directly in the store and returns it.
func sandboxFSProject(t *testing.T, s *Server, id, name, path string, sandboxEnabled bool) *types.Project {
	t.Helper()
	p := &types.Project{ID: id, Name: name, Path: path, SandboxEnabled: sandboxEnabled}
	if err := s.store.CreateProject(p); err != nil {
		t.Fatalf("create project %s: %v", id, err)
	}
	return p
}

// fsProject loads a project created by sandboxFSProject, the way the fs
// handlers see it — validateFSPath takes the project the handler already
// loaded, not the request.
func fsProject(t *testing.T, s *Server, projectID string) *types.Project {
	t.Helper()
	if projectID == "" {
		return nil // directory-picker flow: no project constraint
	}
	p, err := s.store.GetProject(projectID)
	if err != nil {
		t.Fatalf("get project %s: %v", projectID, err)
	}
	return p
}

// TestValidateFSPathSandboxMountScoping verifies that /home/sandbox/* client
// paths are accepted only as the requesting project's own sandbox mount (and
// only for sandbox-enabled projects), and rejected everywhere else — including
// other mounts and host paths that merely live under /home/sandbox.
func TestValidateFSPathSandboxMountScoping(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()

	sandboxFSProject(t, s, "sbx", "sbxproj", filepath.Join(dir, "repo-sbx"), true)
	sandboxFSProject(t, s, "plain", "plainproj", filepath.Join(dir, "repo-plain"), false)

	tests := []struct {
		name      string
		projectID string
		path      string
		wantErr   bool
	}{
		// Sandbox-enabled project: own mount is allowed.
		{name: "sandbox own mount file", projectID: "sbx", path: "/home/sandbox/sbxproj/file.txt", wantErr: false},
		{name: "sandbox own mount root", projectID: "sbx", path: "/home/sandbox/sbxproj", wantErr: false},

		// Sandbox-enabled project: any other /home/sandbox path is rejected.
		{name: "sandbox other project mount", projectID: "sbx", path: "/home/sandbox/plainproj/file.txt", wantErr: true},
		{name: "sandbox prefix-lookalike mount", projectID: "sbx", path: "/home/sandbox/sbxproj2/file.txt", wantErr: true},
		{name: "sandbox host tree escape", projectID: "sbx", path: "/home/sandbox/MIKE/go.mod", wantErr: true},

		// Non-sandbox project: /home/sandbox paths are always rejected.
		{name: "non-sandbox own mount name", projectID: "plain", path: "/home/sandbox/plainproj/file.txt", wantErr: true},
		{name: "non-sandbox other path", projectID: "plain", path: "/home/sandbox/anything", wantErr: true},

		// Host paths are unaffected by the sandbox rule.
		{name: "project root file", projectID: "plain", path: filepath.Join(dir, "repo-plain", "a.go"), wantErr: false},
		{name: "relative path not under workspace", projectID: "plain", path: ".worktrees/x", wantErr: true},
		{name: "outside everything", projectID: "plain", path: "/etc/passwd", wantErr: true},

		// Traversal via .. is cleaned away and then rejected.
		{name: "dotdot escape via sandbox prefix", projectID: "sbx", path: "/home/sandbox/../../etc/passwd", wantErr: true},

		// No project: directory-picker flow has no constraint.
		{name: "no project picker path", projectID: "", path: "/etc", wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.validateFSPath(fsProject(t, s, tt.projectID), tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateFSPath(%q, project=%q) error = %v, wantErr %v", tt.path, tt.projectID, err, tt.wantErr)
			}
		})
	}
}

// TestValidateFSPathWorkspaceDir verifies that files under the configured
// (absolute) workspace dir remain allowed for project-scoped requests.
func TestValidateFSPathWorkspaceDir(t *testing.T) {
	s := testServer(t)
	cfg, err := s.configStore.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if cfg.WorkspaceDir == "" {
		t.Skip("workspace dir not set")
	}
	dir := t.TempDir()
	plain := sandboxFSProject(t, s, "plain", "plainproj", filepath.Join(dir, "repo-plain"), false)

	if _, err := s.validateFSPath(plain, filepath.Join(cfg.WorkspaceDir, "projid", "taskid", "f.go")); err != nil {
		t.Errorf("workspace worktree path rejected: %v", err)
	}
}

// TestValidateFSPathCraftedProjectName verifies a project name containing
// path separators cannot widen the sandbox-mount allow-set.
func TestValidateFSPathCraftedProjectName(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	evil := sandboxFSProject(t, s, "evil", "a/../../b", filepath.Join(dir, "repo-evil"), true)

	// MountPath cleans, so the mount is /home/b — neither the cleaned path it
	// implies nor a lookalike under the mount root may be accepted.
	if _, err := s.validateFSPath(evil, "/home/sandbox/b/x"); err == nil {
		t.Error("crafted project name widened the allow-set: /home/sandbox/b/x was accepted")
	}
	if _, err := s.validateFSPath(evil, "/home/sandbox/a/../../b/x"); err == nil {
		// Cleaned to /home/b/x — not under /home/sandbox, so it must fail
		// the project-root/workspace checks.
		t.Error("crafted project name widened the allow-set: dotdot mount path was accepted")
	}
}

// TestFSMkdirProjectScoped verifies that project-scoped mkdir is validated
// while the no-project directory-picker flow is not.
func TestFSMkdirProjectScoped(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo-plain")
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	_ = sandboxFSProject(t, s, "plain", "plainproj", repo, false)

	cookie, csrf := loginTestCSRF(t, s)

	doMkdir := func(projectID, path string) int {
		body := map[string]string{"path": path}
		q := url.Values{}
		if projectID != "" {
			q.Set("project", projectID)
		}
		req := testRequest(http.MethodPost, "/api/fs/mkdir?"+q.Encode(), body)
		withAuthCSRF(req, cookie, csrf)
		rr := testResponse(t, s, req)
		return rr.Code
	}

	// Inside the project (existing parent) → ok.
	if code := doMkdir("plain", filepath.Join(repo, "sub", "newdir")); code != http.StatusOK {
		t.Errorf("in-project mkdir: got %d, want 200", code)
	}
	// Outside the project → rejected (validation runs before the parent check).
	outside := filepath.Join(dir, "outside-mkdir")
	if code := doMkdir("plain", outside); code != http.StatusBadRequest {
		t.Errorf("outside-project mkdir: got %d, want 400", code)
	}
	// No project (picker flow) → still allowed anywhere.
	picker := filepath.Join(dir, "picker-mkdir")
	if code := doMkdir("", picker); code != http.StatusOK {
		t.Errorf("no-project picker mkdir: got %d, want 200", code)
	}
}
