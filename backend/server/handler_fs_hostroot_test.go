package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/fsutil"
	"mike/backend/git"
	"mike/backend/types"

	"mike/backend/startup"
)

// The host-root jail on the three no-project file endpoints: the picker (GET /api/fs), its mkdir,
// and git init. A request naming a project is jailed to that project by validateFSPath instead.
// HOME is set before the server is built, because NewServer decides the roots once.

// jailedHostRootServer returns a server whose home is home and whose jail is closed, plus a
// logged-in session. outside is a second directory that is outside every root. HOME is set before the
// server is built because NewServer decides the roots once, and the widening is refused in the
// configuration rather than in the environment, so a developer's own shell cannot decide the outcome.
func jailedHostRootServer(t *testing.T) (s *Server, home, outside, cookie, csrf string) {
	t.Helper()
	home = t.TempDir()
	outside = t.TempDir()
	t.Setenv("HOME", home)
	srv := testServerWith(t, func(c *startup.Config) { c.AllowRootFS = false })
	c, tok := loginTestCSRF(t, srv)
	return srv, home, outside, c, tok
}

func getFS(t *testing.T, s *Server, cookie, csrf, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := testRequest(http.MethodGet, "/api/fs?"+query, nil)
	withAuthCSRF(req, cookie, csrf)
	return testResponse(t, s, req)
}

func TestFSBrowseIsJailedToTheHostRoots(t *testing.T) {
	s, home, outside, cookie, csrf := jailedHostRootServer(t)
	if err := os.WriteFile(filepath.Join(home, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Inside home: allowed, and the file is there.
	rr := getFS(t, s, cookie, csrf, "path="+home+"&files=1")
	if rr.Code != http.StatusOK {
		t.Fatalf("listing home = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "notes.txt") {
		t.Errorf("listing home missing notes.txt: %s", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), `"name":".."`) {
		t.Error("listing a jail root must not offer ..")
	}

	// Outside every root: 403, and the message names the variable that widens it.
	for _, q := range []string{"path=/etc", "path=" + outside, "path=" + home + string(filepath.Separator) + ".."} {
		rr := getFS(t, s, cookie, csrf, q)
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s = %d, want 403: %s", q, rr.Code, rr.Body.String())
			continue
		}
		if !strings.Contains(rr.Body.String(), fsutil.AllowRootFSEnv) {
			t.Errorf("%s: refusal must name %s, got %s", q, fsutil.AllowRootFSEnv, rr.Body.String())
		}
	}

	// A declared directory (the workspace dir NewServer passed in) is a root.
	ws := s.configStore.WorkspaceDir()
	if rr := getFS(t, s, cookie, csrf, "path="+ws); rr.Code != http.StatusOK {
		t.Errorf("listing the declared workspace dir = %d, want 200: %s", rr.Code, rr.Body.String())
	}

	// A jail boundary offers no parent: ".." would name the outside just refused.
	if rr := getFS(t, s, cookie, csrf, "path="+home); strings.Contains(rr.Body.String(), `"name":".."`) {
		t.Error("listing a jail root must not offer ..")
	}
	if rr := getFS(t, s, cookie, csrf, "path="+filepath.Join(home, "..")); rr.Code == http.StatusOK {
		t.Errorf("listing home's parent = 200, want refusal: %s", rr.Body.String())
	}

	// A symlink inside home is still refused when it points out of every root.
	link := filepath.Join(home, "escape")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatal(err)
	}
	if rr := getFS(t, s, cookie, csrf, "path="+link); rr.Code != http.StatusForbidden {
		t.Errorf("listing a symlink out of the roots = %d, want 403: %s", rr.Code, rr.Body.String())
	}
	// ...but the listing that merely HOLDS the link is fine and shows the entry
	// (files=1, because without it the endpoint lists directories only).
	if rr := getFS(t, s, cookie, csrf, "path="+home+"&files=1"); rr.Code != http.StatusOK ||
		!strings.Contains(rr.Body.String(), "escape") {
		t.Errorf("listing the directory holding the link = %d: %s", rr.Code, rr.Body.String())
	}

	// Widened: the same requests succeed, which is how the test suites run.
	widenHostRoot(s)
	if rr := getFS(t, s, cookie, csrf, "path=/etc"); rr.Code != http.StatusOK {
		t.Errorf("widened listing /etc = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if rr := getFS(t, s, cookie, csrf, "path="+outside); rr.Code != http.StatusOK {
		t.Errorf("widened listing outside = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	// And with no jail there is no boundary to hide, so .. comes back (files=1 keeps
	// the listing non-empty, which is what makes the check mean something).
	if rr := getFS(t, s, cookie, csrf, "path="+home+"&files=1"); !strings.Contains(rr.Body.String(), `"name":".."`) {
		t.Error("widened listing must offer .. again")
	}
}

// A listing of a directory that merely does not exist stays the 404 it always was
// — the jail must not turn every miss into a 403.
func TestFSBrowseStillReportsAMissingDirectoryAsNotFound(t *testing.T) {
	s, home, _, cookie, csrf := jailedHostRootServer(t)
	rr := getFS(t, s, cookie, csrf, "path="+filepath.Join(home, "nope"))
	if rr.Code != http.StatusNotFound {
		t.Errorf("listing a missing in-home directory = %d, want 404: %s", rr.Code, rr.Body.String())
	}
}

func TestFSMkdirIsJailedToTheHostRoots(t *testing.T) {
	s, home, outside, cookie, csrf := jailedHostRootServer(t)

	mkdir := func(path string) *httptest.ResponseRecorder {
		req := testRequest(http.MethodPost, "/api/fs/mkdir", map[string]string{"path": path})
		withAuthCSRF(req, cookie, csrf)
		return testResponse(t, s, req)
	}

	// Inside home, whose parent exists: created.
	if rr := mkdir(filepath.Join(home, "folder")); rr.Code != http.StatusOK {
		t.Fatalf("mkdir in home = %d: %s", rr.Code, rr.Body.String())
	}
	if fi, err := os.Stat(filepath.Join(home, "folder")); err != nil || !fi.IsDir() {
		t.Fatalf("directory not created: %v", err)
	}

	// Outside every root: 403 naming the variable, and nothing created.
	target := filepath.Join(outside, "created")
	rr := mkdir(target)
	if rr.Code != http.StatusForbidden {
		t.Errorf("mkdir outside = %d, want 403: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), fsutil.AllowRootFSEnv) {
		t.Errorf("refusal must name %s, got %s", fsutil.AllowRootFSEnv, rr.Body.String())
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("a refused mkdir created %s", target)
	}

	// A refusal on the PARENT is also a 403, not the misleading "parent directory
	// does not exist": here the parent is a symlink in home pointing out.
	link := filepath.Join(home, "link-out")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	rr = mkdir(filepath.Join(link, "sub"))
	if rr.Code != http.StatusForbidden {
		t.Errorf("mkdir through a symlink out of the roots = %d, want 403: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(outside, "sub")); !os.IsNotExist(err) {
		t.Error("mkdir followed a symlink out of the jail")
	}

	widenHostRoot(s)
	if rr := mkdir(filepath.Join(outside, "widened")); rr.Code != http.StatusOK {
		t.Errorf("widened mkdir outside = %d, want 200: %s", rr.Code, rr.Body.String())
	}
}

func TestFSInitGitIsJailedToTheHostRoots(t *testing.T) {
	s, home, outside, cookie, csrf := jailedHostRootServer(t)
	t.Setenv("GIT_AUTHOR_NAME", "Test Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")

	initGit := func(path string) *httptest.ResponseRecorder {
		req := testRequest(http.MethodPost, "/api/fs/init-git", map[string]string{"path": path})
		withAuthCSRF(req, cookie, csrf)
		return testResponse(t, s, req)
	}

	// Outside every root: refused before git is spawned, and no repository appears.
	rr := initGit(outside)
	if rr.Code != http.StatusForbidden {
		t.Errorf("init-git outside = %d, want 403: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(outside, ".git")); !os.IsNotExist(err) {
		t.Error("a refused init-git still created a repository")
	}

	// Inside home: works.
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if rr := initGit(repo); rr.Code != http.StatusOK {
		t.Fatalf("init-git in home = %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		t.Errorf("expected a repository in home: %v", err)
	}

	// Widened: the outside directory is reachable again (200, or a 400 from git
	// itself — either way the jail let it through).
	widenHostRoot(s)
	other := t.TempDir()
	if rr := initGit(other); rr.Code == http.StatusForbidden {
		t.Errorf("widened init-git outside = 403, want the jail out of the way: %s", rr.Body.String())
	}
}

// The two jails are chosen by whether a project is in the request. A registered project
// outside home must stay browsable while jailed — the jail gates adding a project, not using
// one that is already there.
func TestProjectScopedListingIgnoresTheHostJail(t *testing.T) {
	s, _, outside, cookie, csrf := jailedHostRootServer(t)
	projPath := filepath.Join(outside, "registered")
	if err := os.MkdirAll(projPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projPath, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.store.CreateProject(&types.Project{
		ID: "hostroot-project", Name: "Hostroot", Path: projPath,
	}); err != nil {
		t.Fatal(err)
	}

	// Project-scoped: the project's own jail applies, and the path is inside it.
	if rr := getFS(t, s, cookie, csrf, "project=hostroot-project&path="+projPath); rr.Code != http.StatusOK {
		t.Errorf("project-scoped listing of an out-of-home project = %d, want 200: %s",
			rr.Code, rr.Body.String())
	}
	// Same directory, no project: now it is the picker's path, and the host jail owns it.
	if rr := getFS(t, s, cookie, csrf, "path="+projPath); rr.Code != http.StatusForbidden {
		t.Errorf("no-project listing of the same path = %d, want 403: %s", rr.Code, rr.Body.String())
	}
}

func TestHomeEndpointReportsTheHome(t *testing.T) {
	s, home, _, cookie, csrf := jailedHostRootServer(t)

	req := testRequest(http.MethodGet, "/api/home", nil)
	withAuthCSRF(req, cookie, csrf)
	rr := testResponse(t, s, req)
	body := rr.Body.String()
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/home = %d: %s", rr.Code, body)
	}
	if !strings.Contains(body, `"home"`) || !strings.Contains(body, home) {
		t.Errorf("home must still be reported: %s", body)
	}
}

// TestFSInitGitRefusesANamePlantedAsALink (security): a link standing at README.md is written
// wherever it points, which can be outside every host root and is not reported anywhere else.
func TestFSInitGitRefusesANamePlantedAsALink(t *testing.T) {
	s, home, outside, cookie, csrf := jailedHostRootServer(t)
	t.Setenv("GIT_AUTHOR_NAME", "Test Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")

	const stored = "ssh-rsa AAAA key\n"
	victim := filepath.Join(outside, "authorized_keys")
	if err := os.WriteFile(victim, []byte(stored), 0o600); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(home, "unpacked")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, "README.md")); err != nil {
		t.Fatal(err)
	}

	req := testRequest(http.MethodPost, "/api/fs/init-git", map[string]string{"path": dir})
	withAuthCSRF(req, cookie, csrf)
	if rr := testResponse(t, s, req); rr.Code == http.StatusOK {
		t.Errorf("init-git at a linked README.md = 200, want a refusal: %s", rr.Body.String())
	}

	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("the link's target is gone: %v", err)
	}
	if string(got) != stored {
		t.Errorf("init-git wrote through the symlink: %q", got)
	}
}

// TestFSInitGitLeavesACommittedReadme (feature): the picker's "initialize git repository" leaves a
// repository with one commit holding README.md, not a bare `git init` and not an untracked file.
func TestFSInitGitLeavesACommittedReadme(t *testing.T) {
	s, home, _, cookie, csrf := jailedHostRootServer(t)
	t.Setenv("GIT_AUTHOR_NAME", "Test Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")

	dir := filepath.Join(home, "new-project")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	req := testRequest(http.MethodPost, "/api/fs/init-git", map[string]string{"path": dir})
	withAuthCSRF(req, cookie, csrf)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("init-git = %d: %s", rr.Code, rr.Body.String())
	}

	body, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("no README.md: %v", err)
	}
	if string(body) != "# new-project\n" {
		t.Errorf("README.md = %q, want %q", body, "# new-project\n")
	}
	// A clean working tree says the README was staged and committed.
	out, err := git.Command(dir, "-C", dir, "status", "--porcelain").Output()
	if err != nil || len(out) != 0 {
		t.Errorf("working tree after init-git is not clean: out=%q err=%v", out, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if fsutil.IsAtomicTemp(e.Name()) {
			t.Errorf("a temporary write survived in %s: %s", dir, e.Name())
		}
	}
}
