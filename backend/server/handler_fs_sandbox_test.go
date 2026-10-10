package server

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/sandbox"
	"mike/backend/types"
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
			_, err := s.validateFSPath(fsProject(t, s, tt.projectID), "", tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateFSPath(%q, project=%q) error = %v, wantErr %v", tt.path, tt.projectID, err, tt.wantErr)
			}
		})
	}
}

// TestValidateFSPathWorktreeRoot is the other half of the jail: the worktree of the task the request
// named is allowed, and the workspace directory that holds it is not — a project-scoped request must
// not reach another project's worktrees.
func TestValidateFSPathWorktreeRoot(t *testing.T) {
	s := testServer(t)
	cfg, err := s.configStore.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkspaceDir == "" {
		t.Skip("workspace dir not set")
	}
	dir := t.TempDir()
	plain := sandboxFSProject(t, s, "plain", "plainproj", filepath.Join(dir, "repo-plain"), false)

	own := filepath.Join(cfg.WorkspaceDir, "plain", "task-a")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	// The named task's own worktree is the point of the extra root.
	if _, err := s.validateFSPath(plain, own, filepath.Join(own, "f.go")); err != nil {
		t.Errorf("the request's own worktree was rejected: %v", err)
	}
	// Another task of the same project, and another project entirely, are not.
	if _, err := s.validateFSPath(plain, own, filepath.Join(cfg.WorkspaceDir, "plain", "task-b", "f.go")); err == nil {
		t.Error("a sibling task worktree was accepted")
	}
	if _, err := s.validateFSPath(plain, own, filepath.Join(cfg.WorkspaceDir, "other", "task-c", "f.go")); err == nil {
		t.Error("another project's task worktree was accepted")
	}
	// Without a task there is no worktree root at all: the project checkout only.
	if _, err := s.validateFSPath(plain, "", filepath.Join(cfg.WorkspaceDir, "plain", "task-a", "f.go")); err == nil {
		t.Error("the workspace dir is still a root when no task was named")
	}
	// The project checkout stays allowed.
	if _, err := s.validateFSPath(plain, own, filepath.Join(dir, "repo-plain", "a.go")); err != nil {
		t.Errorf("the project root was rejected: %v", err)
	}
}

// TestFSForeignTaskIsRejected is the ownership rule: ?project and ?task are separate query
// parameters and must be correlated, or a task of another project aims the whole request — the
// worktree, and for a sandboxed project the container — at someone else's task.
func TestFSForeignTaskIsRejected(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)

	a := sandboxFSProject(t, s, "own", "ownproj", t.TempDir(), false)
	b := sandboxFSProject(t, s, "other", "otherproj", t.TempDir(), false)
	foreign := createTestTask(t, s, b.ID, "a task of the other project")

	// The foreign task id alone reaches nothing, whichever fs endpoint asks.
	for _, target := range []string{
		"/api/fs?" + url.Values{"path": {b.Path}, "project": {a.ID}, "task": {foreign}}.Encode(),
		"/api/fs/read?" + url.Values{"path": {filepath.Join(b.Path, "x.go")}, "project": {a.ID}, "task": {foreign}}.Encode(),
	} {
		req := testRequest(http.MethodGet, target, nil)
		withAuthCSRF(req, cookie, csrf)
		if rr := testResponse(t, s, req); rr.Code == http.StatusOK {
			t.Errorf("%s: a task of another project was honoured (%s)", target, rr.Body.String())
		}
	}

	// mkdir is the one with a side effect: it must not run in the foreign task's tree.
	marker := filepath.Join(s.configStore.WorkspaceDir(), "other", foreign, "pwned")
	req := testRequest(http.MethodPost, "/api/fs/mkdir?"+url.Values{"project": {a.ID}, "task": {foreign}}.Encode(),
		map[string]string{"path": marker})
	withAuthCSRF(req, cookie, csrf)
	if rr := testResponse(t, s, req); rr.Code == http.StatusOK {
		t.Errorf("mkdir accepted a foreign task: %s", rr.Body.String())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("mkdir created %s under a task of another project", marker)
	}

	// A task parameter with no project to own it is a client error, not a bare browse.
	req = testRequest(http.MethodGet, "/api/fs?"+url.Values{"task": {foreign}}.Encode(), nil)
	withAuthCSRF(req, cookie, csrf)
	if rr := testResponse(t, s, req); rr.Code != http.StatusBadRequest {
		t.Errorf("task without project: got %d, want 400: %s", rr.Code, rr.Body.String())
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
	if _, err := s.validateFSPath(evil, "", "/home/sandbox/b/x"); err == nil {
		t.Error("crafted project name widened the allow-set: /home/sandbox/b/x was accepted")
	}
	if _, err := s.validateFSPath(evil, "", "/home/sandbox/a/../../b/x"); err == nil {
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

// TestValidateFSPathContainerSpellingNamesTheHostFile: the container spelling is a name inside the
// project's container, so what validateFSPath() hands back has to be the host file it stands for, jailed
// like every other path.
func TestValidateFSPathContainerSpellingNamesTheHostFile(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo-host-form")
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	proj := sandboxFSProject(t, s, "hostform", "hostformproj", repo, true)

	wantBase, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.validateFSPath(proj, "", "/home/sandbox/hostformproj/sub/f.go")
	if err != nil {
		t.Fatalf("the project's own container path was refused: %v", err)
	}
	if strings.HasPrefix(got, sandbox.MountRoot+string(filepath.Separator)) {
		t.Errorf("validateFSPath returned the container spelling %q; a caller that opens it resolves no symlink", got)
	}
	if want := filepath.Join(wantBase, "sub", "f.go"); got != want {
		t.Errorf("returned %q, want the host file %q", got, want)
	}

	// A request naming a task is about that task's tree, so the spelling lands in its worktree.
	wt := filepath.Join(dir, "task-wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	wtBase, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.validateFSPath(proj, wt, "/home/sandbox/hostformproj/sub/f.go")
	if err != nil {
		t.Fatalf("the task's container path was refused: %v", err)
	}
	if want := filepath.Join(wtBase, "sub", "f.go"); got != want {
		t.Errorf("with a task worktree returned %q, want %q", got, want)
	}
}

// TestValidateFSPathMountShapedHostPathStillResolvesLinks is the escape: a project whose host path is
// shaped exactly like its own mount (/home/sandbox/<name>) made the container spelling and the host path
// one string, so the accepted answer was unresolved and the caller read through a committed link.
func TestValidateFSPathMountShapedHostPathStillResolvesLinks(t *testing.T) {
	s := testServer(t)
	name := "mikeshapelink"
	root := sandbox.MountPath(name)
	// os.Mkdir, not MkdirAll: a test must not create /home/sandbox on a machine that lacks it.
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Skipf("%s does not exist and is not creatable here, so a host path shaped like the mount cannot be built: %v", root, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("host secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "real.txt"), []byte("inside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	proj := sandboxFSProject(t, s, "shaped", name, root, true)

	// The control: an ordinary file in the project still answers.
	if _, err := s.validateFSPath(proj, "", filepath.Join(root, "real.txt")); err != nil {
		t.Fatalf("an ordinary in-project read was refused: %v", err)
	}

	if got, err := s.validateFSPath(proj, "", filepath.Join(root, "link", "secret.txt")); err == nil {
		t.Errorf("read through a committed symlink was accepted as %q; its bytes are outside the project", got)
	}
	// The same path spelled the container way is the request the browser actually sends.
	if got, err := s.validateFSPath(proj, "", filepath.Join(sandbox.MountPath(name), "link", "secret.txt")); err == nil {
		t.Errorf("the container spelling read through a committed symlink and was accepted as %q", got)
	}
}

// TestFSSandboxPathRoundTripsThroughSymlinkedProjectPath: a project registered through a symlink puts
// the jail's resolved answer and the caller's root in different trees, and the container path built from
// them still has to name the directory that was asked for.
func TestFSSandboxPathRoundTripsThroughSymlinkedProjectPath(t *testing.T) {
	s := testServer(t)
	realDir := t.TempDir()
	linkDir := t.TempDir()
	linked := filepath.Join(linkDir, "repo")
	if err := os.Symlink(realDir, linked); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(realDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	proj := sandboxFSProject(t, s, "rt", "rtproj", linked, true)
	spelling := filepath.Join(sandbox.MountPath("rtproj"), "sub", "f.go")

	host, err := s.validateFSPath(proj, "", spelling)
	if err != nil {
		t.Fatalf("the project's own container path was refused: %v", err)
	}
	back, ok := fsSandboxPath(proj.Path, proj, host)
	if !ok || back != spelling {
		t.Errorf("round trip of the jail's answer %q = (%q, %v), want the directory asked for (%q, true)",
			host, back, ok, spelling)
	}

	// A task worktree under the linked tree gives the same answer, rooted on the worktree.
	wt := filepath.Join(linked, "wt")
	if err := os.MkdirAll(filepath.Join(realDir, "wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	host, err = s.validateFSPath(proj, wt, spelling)
	if err != nil {
		t.Fatalf("the task's container path was refused: %v", err)
	}
	back, ok = fsSandboxPath(wt, proj, host)
	if !ok || back != spelling {
		t.Errorf("task worktree round trip of %q = (%q, %v), want (%q, true)", host, back, ok, spelling)
	}

	// A file of the checkout named by a request that also named a task has no place in that container,
	// which mounts only the worktree: no container path, not a ".."-stepping one beside the mount.
	if got, ok := fsSandboxPath(filepath.Join(realDir, "wt"), proj, filepath.Join(realDir, "sub", "f.go")); ok {
		t.Errorf("a checkout file was given the container path %q inside a container that cannot see it", got)
	}
}

// TestFSRoutesOfASandboxedProjectActOnTheHostBytes: a sandboxed project's worktree is a host directory
// the container bind-mounts, so the paste and the folder download read and write it with the server's own
// privileges, from a tree the task controls.
func TestFSRoutesOfASandboxedProjectActOnTheHostBytes(t *testing.T) {
	s := testServer(t)
	if !s.sandboxMgr.IsAvailable() {
		t.Skip("gVisor is not installed here, so a sandboxed project's file request is refused before it reaches any filesystem")
	}
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "notes.txt"), []byte("notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sandboxFSProject(t, s, "sb-hostpath", "sbhost", repo, true)
	mount := sandbox.MountPath("sbhost")

	// The browser sends the container spelling; the bytes it moves are the host's.
	status, res := fsOpsPost(t, s, "copy", "sb-hostpath", "", map[string]any{
		"paths": []string{mount + "/src/notes.txt"},
		"dest":  mount + "/src",
	})
	if status != http.StatusOK || len(res.Done) != 1 {
		t.Fatalf("paste of a sandboxed project's own file: status %d, done %+v", status, res.Done)
	}
	if copied, err := os.ReadFile(filepath.Join(repo, "src", "notes copy.txt")); err != nil || string(copied) != "notes\n" {
		t.Errorf("the paste did not move the host bytes the container spelling names: %q, %v", copied, err)
	}

	rr := downloadAsk(t, s, "sb-hostpath", "", mount+"/src")
	if rr.Code != http.StatusOK {
		t.Fatalf("download of a sandboxed project's folder: %d %s", rr.Code, rr.Body.String())
	}
	if got := unpackArchive(t, rr.Body)["src/notes.txt"]; got != "notes\n" {
		t.Errorf("the archive's src/notes.txt = %q, want the host file's bytes", got)
	}
}
