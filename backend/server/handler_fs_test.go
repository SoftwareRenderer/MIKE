package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mike/backend/git"
	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

// canaryPath is used to detect command injection. If any malicious payload
// breaks out of shell quoting in gitAuthorFlagCmd, this file is created.
const canaryPath = "/tmp/gitauthorflagcmd_pwned_canary"

func cleanCanary() {
	_ = exec.Command("rm", "-f", canaryPath).Run()
}

// canaryCreated reports whether the injection canary file exists (i.e. an
// injection succeeded).
func canaryCreated() bool {
	return exec.Command("test", "-f", canaryPath).Run() == nil
}

// shellEchoArgs runs `printf '%s\n' <args...>` via a POSIX shell so that each
// shell-quoted token in command is printed on its own line. This validates that
// shellQuote groups each token into exactly one shell argument.
func shellEchoArgs(command string) (string, error) {
	out, err := exec.Command("sh", "-c", "printf '%s\n' "+command).Output()
	return string(out), err
}

// TestGitAuthorFlagCmd covers the display form of the commit-identity flags used in status
// output. It is a pure formatter over the two globals, and the rows that matter are the
// partial ones: an identity with only a name or only an email must render just the flag it
// has (git would otherwise be told about an empty identity), and no identity at all must
// render nothing rather than a dangling "-c".
func TestGitAuthorFlagCmd(t *testing.T) {
	cases := []struct {
		name  string
		who   string
		email string
		want  string
	}{
		{name: "full identity", who: "Test Author", email: "test@example.com",
			want: "'-c' 'user.name=Test Author' '-c' 'user.email=test@example.com'"},
		{name: "name only", who: "Only Name", email: "", want: "'-c' 'user.name=Only Name'"},
		{name: "email only", who: "", email: "only@x.com", want: "'-c' 'user.email=only@x.com'"},
		{name: "identity unset", who: "", email: "", want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origName, origEmail := git.AuthorIdentity()
			t.Cleanup(func() { git.SetAuthorIdentity(origName, origEmail) })
			git.SetAuthorIdentity(tc.who, tc.email)

			if got := gitAuthorFlagCmd(); got != tc.want {
				t.Fatalf("gitAuthorFlagCmd() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGitAuthorFlagCmd_ShellSafety verifies that malicious author identity values cannot break
// out of shell quoting when the flagCmd is interpolated into handleFSInitGit's command string.
func TestGitAuthorFlagCmd_ShellSafety(t *testing.T) {
	payloads := []string{
		"'; touch " + canaryPath + "; '",
		"';touch " + canaryPath + ";'",
		"$(touch " + canaryPath + ")",
		"`touch " + canaryPath + "`",
		"'; touch " + canaryPath + "; ' &&",
		"'; touch " + canaryPath + " #",
		"; touch " + canaryPath,
		"| touch " + canaryPath,
		"'; touch " + canaryPath + "; echo '",
		"'; touch " + canaryPath + " ; ' -c user.name=evil",
	}

	origName, origEmail := git.AuthorIdentity()
	defer func() { git.SetAuthorIdentity(origName, origEmail) }()

	for _, payload := range payloads {
		t.Run(payload, func(t *testing.T) {
			cleanCanary()
			// Inject the payload into both name and email so every component
			// produced by GitAuthorFlags() / shellQuote is exercised.
			git.SetAuthorIdentity(payload, payload+"@x.com")

			flagCmd := gitAuthorFlagCmd()

			// Reproduce the exact command-string pattern from handleFSInitGit's
			// sandbox branch. `git -C <path> <flags> commit` will exit non-zero
			// (no repo / no staged changes), which is harmless — we only care
			// whether the injected `touch` executes during shell parsing.
			sandboxPath := t.TempDir()
			shellCmd := "git -C " + shellQuote(sandboxPath) + " " + flagCmd + " commit -m 'Initial commit' || true"

			cmd := exec.Command("sh", "-c", shellCmd)
			if err := cmd.Run(); err != nil {
				t.Fatalf("sh -c failed unexpectedly: %v", err)
			}

			if canaryCreated() {
				t.Errorf("command injection detected! payload %q created canary. flagCmd was: %q", payload, flagCmd)
			}
			cleanCanary()
		})
	}
}

// TestFSMkdirNoProject verifies mkdir without a "project" param (project-picker
// FileBrowser) creates the dir on the host fs instead of panicking on nil project.
func TestFSMkdirNoProject(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)

	parent := t.TempDir()
	target := filepath.Join(parent, "new-folder")

	req := testRequest(http.MethodPost, "/api/fs/mkdir", map[string]string{"path": target})
	withAuthCSRF(req, cookie, csrfToken)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("expected directory %s to exist, err=%v", target, err)
	}
}

// TestGitAuthorFlagCmd_RoundTrip verifies that each shell-quoted token, when
// parsed back by a POSIX shell, reconstructs the original author identity value
// as a single argument.
func TestGitAuthorFlagCmd_RoundTrip(t *testing.T) {
	origName, origEmail := git.AuthorIdentity()
	defer func() { git.SetAuthorIdentity(origName, origEmail) }()

	cases := []struct {
		name  string
		email string
	}{
		{"simple", "a@b.com"},
		{"name with spaces", "a b@x.com"},
		{"quote in name", "o'brien@x.com"},
		{"semicolon in name", "a;ls;@x.com"},
		{"dollar in name", "a$(whoami)@x.com"},
		{"backtick in name", "a`whoami`@x.com"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			git.SetAuthorIdentity(c.name, c.email)

			flagCmd := gitAuthorFlagCmd()

			// `printf '%s\n' <flagCmd>` prints each shell-quoted token on its
			// own line, proving each token survived as a single argument.
			out, err := shellEchoArgs(flagCmd)
			if err != nil {
				t.Fatalf("printf failed: %v", err)
			}
			lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

			// flagCmd is a series of ("-c", "user.name=<name>", "email parts")
			// pairs: ["-c", "user.name=NAME", "-c", "user.email=EMAIL"].
			wantArgs := []string{"-c", "user.name=" + c.name, "-c", "user.email=" + c.email}
			if len(lines) != len(wantArgs) {
				t.Fatalf("expected %d args, got %d: %v", len(wantArgs), len(lines), lines)
			}
			for i, want := range wantArgs {
				if lines[i] != want {
					t.Errorf("arg %d: expected %q, got %q", i, want, lines[i])
				}
			}
		})
	}
}

// fsReadRefRepo creates a git repo registered as a project, with one committed
// file, a branch and a known HEAD sha — the fixture for the `?ref=` tests.
// Returns the absolute file path, the HEAD sha and the branch name.
func fsReadRefRepo(t *testing.T, s *Server, projectID string) (string, string, string) {
	t.Helper()
	repo := t.TempDir()
	if err := s.store.CreateProject(&types.Project{
		ID:   projectID,
		Name: "FS Ref Project",
		Path: repo,
	}); err != nil {
		t.Fatal(err)
	}
	testgit.Git(t, repo, "init")
	testgit.Git(t, repo, "config", "user.email", "test@test.com")
	testgit.Git(t, repo, "config", "user.name", "Test")
	file := filepath.Join(repo, "f.txt")
	if err := os.WriteFile(file, []byte(fsReadRefContent), 0o644); err != nil {
		t.Fatal(err)
	}
	testgit.Git(t, repo, "add", "f.txt")
	testgit.Git(t, repo, "commit", "-m", "init")
	sha := testgit.GitOutput(t, repo, "rev-parse", "HEAD")
	testgit.Git(t, repo, "branch", "feature/ref-check")
	return file, sha, "feature/ref-check"
}

const fsReadRefContent = "hello from the commit\n"

// fsReadAt issues GET /api/fs/read for the fixture file at the given ref.
func fsReadAt(t *testing.T, s *Server, cookie, csrfToken, projectID, file, ref string) *httptest.ResponseRecorder {
	t.Helper()
	q := url.Values{"path": {file}, "project": {projectID}}
	if ref != "" {
		q.Set("ref", ref)
	}
	req := testRequest(http.MethodGet, "/api/fs/read?"+q.Encode(), nil)
	withAuthCSRF(req, cookie, csrfToken)
	return testResponse(t, s, req)
}

// fsRow is one entry of a GET /api/fs listing.
type fsRow struct {
	isDir     bool
	isGitRepo bool
	ignored   bool
}

// fsListing lists one directory through `GET /api/fs` and answers whether that directory is a
// repository, with its entries keyed by name. An empty projectID leaves `project` off the request, the
// way the add-project picker's request does.
func fsListing(t *testing.T, s *Server, projectID, dir string) (bool, map[string]fsRow) {
	t.Helper()
	cookie, csrf := loginTestCSRF(t, s)
	q := url.Values{"path": {dir}, "files": {"1"}}
	if projectID != "" {
		q.Set("project", projectID)
	}
	req := testRequest(http.MethodGet, "/api/fs?"+q.Encode(), nil)
	withAuthCSRF(req, cookie, csrf)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/fs %s: %d %s", dir, rr.Code, rr.Body.String())
	}
	var body struct {
		IsGitRepo bool      `json:"isGitRepo"`
		Entries   []fsEntry `json:"entries"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	rows := make(map[string]fsRow, len(body.Entries))
	for _, e := range body.Entries {
		rows[e.Name] = fsRow{isDir: e.IsDir, isGitRepo: e.IsGitRepo, ignored: e.Ignored}
	}
	return body.IsGitRepo, rows
}

// TestFSReadRefRejectsGitOptions: a `?ref=` shaped like a git option is refused with 400, and the
// rev is passed after --end-of-options. An option-shaped ref handed to `git show "--output=X:p"`
// would create or truncate a file anywhere the server user can write.
func TestFSReadRefRejectsGitOptions(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	file, _, _ := fsReadRefRepo(t, s, "fs-ref-inject")

	// Payload: ref="--output=/tmp/pwn" + ":" + <relPath> -> writes /tmp/pwn:f.txt.
	// Remove any leftover from an earlier (vulnerable) run so the assertions below
	// describe THIS request, then re-check after the request.
	targets := []string{"/tmp/pwn", "/tmp/pwn:f.txt"}
	for _, p := range targets {
		_ = os.Remove(p)
		p := p
		t.Cleanup(func() { _ = os.Remove(p) })
	}

	rr := fsReadAt(t, s, cookie, csrfToken, "fs-ref-inject", file, "--output=/tmp/pwn")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an option-shaped ref, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "ref") {
		t.Errorf("error should mention the offending parameter, got %s", rr.Body.String())
	}
	for _, p := range targets {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("injection succeeded: %s was created by the request", p)
		}
	}

	// Other option-shaped / syntax-carrying refs are refused the same way.
	for _, ref := range []string{
		"-C/tmp",
		"--exec-path=/tmp",
		"--output=/tmp/pwn",
		"HEAD~1",      // revision syntax
		"HEAD^{}",     // revision syntax
		"HEAD:f.txt",  // the colon is added by the handler, not the client
		"main..HEAD",  // range syntax
		"-",           // stdin
		"feat branch", // whitespace
		"feat$(touch /tmp/pwn)",
	} {
		rr := fsReadAt(t, s, cookie, csrfToken, "fs-ref-inject", file, ref)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("ref %q: expected 400, got %d: %s", ref, rr.Code, rr.Body.String())
		}
	}
	for _, p := range targets {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("injection succeeded: %s was created by a rejected request", p)
		}
	}
}

// TestFSReadRefValidRefsStillWork keeps the feature: reading a file at HEAD, at
// a hex SHA and at a branch name must keep returning the committed content.
func TestFSReadRefValidRefsStillWork(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)
	file, sha, branch := fsReadRefRepo(t, s, "fs-ref-valid")

	// Modify the working tree copy: a ref read must still return the COMMITTED
	// content, proving the read really goes through `git show`.
	if err := os.WriteFile(file, []byte("dirty working tree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, ref := range []string{"HEAD", sha, branch, "refs/heads/" + branch} {
		rr := fsReadAt(t, s, cookie, csrfToken, "fs-ref-valid", file, ref)
		if rr.Code != http.StatusOK {
			t.Fatalf("ref %q: expected 200, got %d: %s", ref, rr.Code, rr.Body.String())
		}
		var resp fsReadResponse
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("ref %q: decode: %v", ref, err)
		}
		if resp.Content != fsReadRefContent {
			t.Errorf("ref %q: expected committed content %q, got %q", ref, fsReadRefContent, resp.Content)
		}
		if resp.GutterMarkers != nil {
			t.Errorf("ref %q: expected no gutter markers, got %v", ref, resp.GutterMarkers)
		}
	}

	// A well-formed ref whose blob does not exist is still a 404, not a 400.
	rr := fsReadAt(t, s, cookie, csrfToken, "fs-ref-valid", filepath.Dir(file), "HEAD")
	if rr.Code == http.StatusOK {
		t.Fatal("expected the directory read at a ref to fail")
	}
	missing := filepath.Join(filepath.Dir(file), "nope.txt")
	if rr := fsReadAt(t, s, cookie, csrfToken, "fs-ref-valid", missing, "HEAD"); rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a file missing at the ref, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestValidateGitRefBlocksOptions double-checks the shared validator used by
// /api/fs/read (the table lives in backend/types/validate_test.go).
func TestFSReadRefTable(t *testing.T) {
	for _, ref := range []string{"HEAD", "main", "refs/heads/main", "0123456789abcdef0123456789abcdef01234567", "task/frost-amber-quartz"} {
		if err := types.ValidateGitRef(ref); err != nil {
			t.Errorf("ValidateGitRef(%q) = %v, want nil", ref, err)
		}
	}
	for _, ref := range []string{"", "-", "-f", "--output=/tmp/pwn", "HEAD~1", "HEAD^", "a..b", "feat branch", "feat\n"} {
		if err := types.ValidateGitRef(ref); err == nil {
			t.Errorf("ValidateGitRef(%q) = nil, want error", ref)
		}
	}
}

// TestFSReadRefInTaskWorktree covers the task-scoped branch of the handler
// (GitRunInWorktreeOutput), which resolves the worktree from the DB before the
// ref reaches git. Same rule must hold: option-shaped refs are rejected before
// git runs, valid refs still read from the worktree's history.
func TestFSReadRefInTaskWorktree(t *testing.T) {
	s := testServer(t)
	cookie, csrfToken := loginTestCSRF(t, s)

	repo := t.TempDir()
	projID := "fs-ref-task"
	if err := s.store.CreateProject(&types.Project{ID: projID, Name: "FS Ref Task Project", Path: repo}); err != nil {
		t.Fatal(err)
	}
	testgit.Git(t, repo, "init")
	testgit.Git(t, repo, "config", "user.email", "test@test.com")
	testgit.Git(t, repo, "config", "user.name", "Test")
	writeAndCommit := func(dir, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		testgit.Git(t, dir, "add", "f.txt")
		testgit.Git(t, dir, "commit", "-m", "commit")
	}
	writeAndCommit(repo, "base commit\n")

	taskID := createTestTask(t, s, projID, "worktree ref task")
	worktreePath, _, err := s.executor.WorktreeMgr().CreateWorktree(projID, taskID, "task/"+taskID, "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	writeAndCommit(worktreePath, "worktree commit\n")

	read := func(ref string) *httptest.ResponseRecorder {
		q := url.Values{
			"path":    {filepath.Join(worktreePath, "f.txt")},
			"project": {projID},
			"task":    {taskID},
		}
		if ref != "" {
			q.Set("ref", ref)
		}
		req := testRequest(http.MethodGet, "/api/fs/read?"+q.Encode(), nil)
		withAuthCSRF(req, cookie, csrfToken)
		return testResponse(t, s, req)
	}

	canary := "/tmp/pwt-pwn:f.txt"
	_ = os.Remove(canary)
	t.Cleanup(func() { _ = os.Remove(canary) })

	if rr := read("--output=/tmp/pwt-pwn"); rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an option-shaped ref in a worktree read, got %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(canary); err == nil {
		t.Error("injection succeeded in the worktree branch: /tmp/pwt-pwn:f.txt was created")
	}

	// A valid ref still resolves inside the worktree's history.
	sha := testgit.GitOutput(t, worktreePath, "rev-parse", "HEAD")
	for _, ref := range []string{"HEAD", sha, "task/" + taskID} {
		rr := read(ref)
		if rr.Code != http.StatusOK {
			t.Fatalf("ref %q: expected 200, got %d: %s", ref, rr.Code, rr.Body.String())
		}
		var resp fsReadResponse
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("ref %q: decode: %v", ref, err)
		}
		if resp.Content != "worktree commit\n" {
			t.Errorf("ref %q: got %q, want the worktree commit content", ref, resp.Content)
		}
	}
}

// --- Symlink containment (security regression) --------------------------------
//
// A sandboxed task writes through the rw bind mount, so it can plant a symlink in
// the worktree that resolves against the host. The fs handlers must resolve links,
// re-check containment, and open the resolved path.

// fsSymlinkProject creates a non-sandboxed project at <base>/repo with one
// regular file, plus <base>/outside (the escape target) holding a secret, and a
// symlink inside the repo pointing at it.
func fsSymlinkProject(t *testing.T, s *Server, id string) (repo, outside, secret, link string) {
	t.Helper()
	base := t.TempDir()
	repo = filepath.Join(base, "repo")
	outside = filepath.Join(base, "outside")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	secret = filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("host secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "ok.txt"), []byte("inside content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(repo, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := s.store.CreateProject(&types.Project{ID: id, Name: "FS Symlink Project", Path: repo}); err != nil {
		t.Fatal(err)
	}
	return
}

func TestFSReadRejectsSymlinkEscape(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)
	repo, _, secret, link := fsSymlinkProject(t, s, "fs-link-read")

	rr := fsReadAt(t, s, cookie, csrf, "fs-link-read", filepath.Join(link, "secret.txt"), "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("read through a symlink: expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "host secret") {
		t.Error("escaped the project root: outside content was returned")
	}

	// The containment check must not break ordinary in-project reads.
	rr = fsReadAt(t, s, cookie, csrf, "fs-link-read", filepath.Join(repo, "ok.txt"), "")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "inside content") {
		t.Fatalf("ordinary in-project read broken: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(secret); err != nil {
		t.Fatalf("fixture disappeared: %v", err)
	}
}

func TestFSListRejectsSymlinkEscape(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)
	_, _, _, link := fsSymlinkProject(t, s, "fs-link-list")

	q := url.Values{"path": {link}, "project": {"fs-link-list"}, "files": {"1"}}
	req := testRequest(http.MethodGet, "/api/fs?"+q.Encode(), nil)
	withAuthCSRF(req, cookie, csrf)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("listing a symlinked directory: expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "secret.txt") {
		t.Error("escaped the project root: outside directory was listed")
	}
}

func TestFSWriteRejectsSymlinkEscape(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)
	repo, outside, _, link := fsSymlinkProject(t, s, "fs-link-write")

	// A path whose existing components traverse the link.
	q := url.Values{"project": {"fs-link-write"}}
	req := testRequest(http.MethodPost, "/api/fs/write?"+q.Encode(), map[string]string{
		"path":    filepath.Join(link, "evil.txt"),
		"content": "pwned",
	})
	withAuthCSRF(req, cookie, csrf)
	if rr := testResponse(t, s, req); rr.Code != http.StatusBadRequest {
		t.Fatalf("write through a symlink: expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(outside, "evil.txt")); err == nil {
		t.Error("escaped the project root: file written outside")
	}

	// The final component being a DANGLING symlink (target does not exist yet)
	// must be rejected too — writing through it would create the target.
	dangling := filepath.Join(repo, "dangling")
	if err := os.Symlink(filepath.Join(outside, "new.txt"), dangling); err != nil {
		t.Fatal(err)
	}
	req = testRequest(http.MethodPost, "/api/fs/write?"+q.Encode(), map[string]string{
		"path":    dangling,
		"content": "pwned",
	})
	withAuthCSRF(req, cookie, csrf)
	if rr := testResponse(t, s, req); rr.Code != http.StatusBadRequest {
		t.Fatalf("write to a dangling symlink: expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); err == nil {
		t.Error("escaped the project root: dangling-symlink write created its target")
	}

	// Ordinary in-project writes (including new files in new subdirs) still work.
	req = testRequest(http.MethodPost, "/api/fs/write?"+q.Encode(), map[string]string{
		"path":    filepath.Join(repo, "sub", "new.txt"),
		"content": "fine",
	})
	withAuthCSRF(req, cookie, csrf)
	if rr := testResponse(t, s, req); rr.Code != http.StatusOK {
		t.Fatalf("ordinary in-project write broken: %d %s", rr.Code, rr.Body.String())
	}
}

func TestFSMkdirRejectsSymlinkEscape(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)
	_, outside, _, link := fsSymlinkProject(t, s, "fs-link-mkdir")

	q := url.Values{"project": {"fs-link-mkdir"}}
	req := testRequest(http.MethodPost, "/api/fs/mkdir?"+q.Encode(), map[string]string{
		"path": filepath.Join(link, "newdir"),
	})
	withAuthCSRF(req, cookie, csrf)
	if rr := testResponse(t, s, req); rr.Code != http.StatusBadRequest {
		t.Fatalf("mkdir through a symlink: expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(outside, "newdir")); err == nil {
		t.Error("escaped the project root: directory created outside")
	}
}

// TestFSSearchSkipsSymlinks: content search walks the host tree, so it must
// never open a symlinked file — its target resolves against the host and would
// leak (or match) content from outside the search root.
func TestFSSearchSkipsSymlinks(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)
	// The fixture itself is the point: a symlink inside the search root.
	fsSymlinkProject(t, s, "fs-link-search")

	search := func(q string) string {
		t.Helper()
		v := url.Values{"project": {"fs-link-search"}, "q": {q}, "content": {"1"}}
		req := testRequest(http.MethodGet, "/api/fs/search?"+v.Encode(), nil)
		withAuthCSRF(req, cookie, csrf)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("search %q: %d %s", q, rr.Code, rr.Body.String())
		}
		return rr.Body.String()
	}

	if got := search("host secret"); strings.Contains(got, "host secret") || strings.Contains(got, "link/") || strings.Contains(got, "link\"") {
		t.Errorf("content search followed a symlink out of the root: %s", got)
	}
	// In-tree search keeps working.
	if got := search("inside content"); !strings.Contains(got, "ok.txt") {
		t.Errorf("ordinary content search broken: %s", got)
	}
}

// TestFSInitGitRejectsPathOutsideProject (security): POST /api/fs/init-git stays inside the
// project/workspace like every other fs endpoint.
func TestFSInitGitRejectsPathOutsideProject(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)
	base := t.TempDir()
	projDir := filepath.Join(base, "project")
	outside := filepath.Join(base, "elsewhere")
	for _, d := range []string{projDir, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.store.CreateProject(&types.Project{ID: "fs-initgit", Name: "FS InitGit Project", Path: projDir}); err != nil {
		t.Fatal(err)
	}

	initGit := func(path string) *httptest.ResponseRecorder {
		req := testRequest(http.MethodPost, "/api/fs/init-git?project=fs-initgit", map[string]string{"path": path})
		withAuthCSRF(req, cookie, csrf)
		return testResponse(t, s, req)
	}

	if rr := initGit(outside); rr.Code != http.StatusBadRequest {
		t.Fatalf("init-git outside the project: expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if git.IsGitRepo(outside) {
		t.Error("git init ran outside the project root")
	}

	// A symlink inside the project that leads outside is rejected as well.
	link := filepath.Join(projDir, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if rr := initGit(link); rr.Code != http.StatusBadRequest {
		t.Fatalf("init-git through a symlink: expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if git.IsGitRepo(outside) {
		t.Error("git init escaped through a symlink")
	}

	// The in-project flow keeps working.
	t.Setenv("GIT_AUTHOR_NAME", "Test Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	sub := filepath.Join(projDir, "subdir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if rr := initGit(sub); rr.Code != http.StatusOK {
		t.Fatalf("in-project init-git broken: %d %s", rr.Code, rr.Body.String())
	}
	if !git.IsGitRepo(sub) {
		t.Error("in-project git init did not create a repository")
	}
}

// --- Listing badges and search matching --------------------------------------

// TestFSListGitBadge: the per-entry repository badge, now without a `git
// rev-parse` per entry. An entry qualifies when the listed directory is inside a
// repository (rev-parse walks up, so that covers every entry) or carries a .git
// of its own — a nested repository or a linked worktree.
func TestFSListGitBadge(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	s := testServer(t)
	base := t.TempDir()

	// A directory that is NOT a repository, holding a plain subdirectory and a
	// nested repository.
	plain := filepath.Join(base, "workspace")
	nested := filepath.Join(plain, "nested")
	inside := filepath.Join(plain, "plaindir")
	for _, d := range []string{nested, inside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A real repository, whose ordinary subdirectory must still read as being
	// inside one.
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{nested, repo} {
		if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
			t.Fatalf("git init %s: %v: %s", dir, err, out)
		}
	}
	for _, p := range []struct{ id, name, path string }{
		{"fs-badge-plain", "FS Badge Plain", plain},
		{"fs-badge-repo", "FS Badge Repo", repo},
	} {
		if err := s.store.CreateProject(&types.Project{ID: p.id, Name: p.name, Path: p.path}); err != nil {
			t.Fatal(err)
		}
	}

	// list returns the badge for the listed directory itself under the "" key.
	list := func(projectID, path string) map[string]bool {
		t.Helper()
		isGit, rows := fsListing(t, s, projectID, path)
		badges := map[string]bool{"": isGit}
		for name, row := range rows {
			badges[name] = row.isGitRepo
		}
		return badges
	}

	badges := list("fs-badge-plain", plain)
	if badges[""] {
		t.Error("a plain directory reported as a repository")
	}
	if !badges["nested"] {
		t.Error("nested repository not badged — carrying a .git must count")
	}
	if badges["plaindir"] {
		t.Error("a plain subdirectory of a non-repository was badged")
	}

	badges = list("fs-badge-repo", repo)
	if !badges[""] {
		t.Error("repository root not badged")
	}
	if !badges["internal"] {
		t.Error("a subdirectory inside a repository must stay badged (rev-parse walks up)")
	}
}

// TestFSSearchFilenameRegex: the filename-mode pattern, now compiled once per
// request. A valid pattern filters; an invalid one matches nothing but still
// returns success.
func TestFSSearchFilenameRegex(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)
	base := t.TempDir()
	for name, content := range map[string]string{"a.go": "package a\n", "b.txt": "hello\n"} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.store.CreateProject(&types.Project{ID: "fs-rx", Name: "FS Regex Project", Path: base}); err != nil {
		t.Fatal(err)
	}

	type searchResp struct {
		Results []struct {
			Path string `json:"path"`
		} `json:"results"`
	}

	search := func(q string) (int, []string) {
		t.Helper()
		v := url.Values{"project": {"fs-rx"}, "q": {q}, "regex": {"1"}}
		req := testRequest(http.MethodGet, "/api/fs/search?"+v.Encode(), nil)
		withAuthCSRF(req, cookie, csrf)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			return rr.Code, nil
		}
		var resp searchResp
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("decode search response: %v", err)
		}
		paths := make([]string, 0, len(resp.Results))
		for _, r := range resp.Results {
			paths = append(paths, r.Path)
		}
		return rr.Code, paths
	}

	code, paths := search(`\.go$`)
	if code != http.StatusOK {
		t.Fatalf("valid pattern: expected 200, got %d", code)
	}
	if len(paths) != 1 || paths[0] != "a.go" {
		t.Errorf("regex filename search matched %v, want exactly [a.go]", paths)
	}

	// An unbalanced paren is not a valid pattern: the request still succeeds
	// and nothing matches.
	code, paths = search(`(`)
	if code != http.StatusOK {
		t.Fatalf("invalid pattern: expected 200 with no matches, got %d", code)
	}
	if len(paths) != 0 {
		t.Errorf("invalid pattern matched %v, want no results", paths)
	}
}

// TestFSSearchFilenameWholeWordRegex: the whole-word toggle bounds where the pattern landed in the path, so
// a longer word is excluded and an anchor still works.
func TestFSSearchFilenameWholeWordRegex(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)
	base := t.TempDir()
	for _, name := range []string{"handler.go", "ahandlerb.go"} {
		if err := os.WriteFile(filepath.Join(base, name), []byte("package a\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.store.CreateProject(&types.Project{ID: "fs-word", Name: "FS Word Project", Path: base}); err != nil {
		t.Fatal(err)
	}

	search := func(q string, wholeWord bool) []string {
		t.Helper()
		v := url.Values{"project": {"fs-word"}, "q": {q}, "regex": {"1"}}
		if wholeWord {
			v.Set("word", "1")
		}
		req := testRequest(http.MethodGet, "/api/fs/search?"+v.Encode(), nil)
		withAuthCSRF(req, cookie, csrf)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("search %q: got %d", q, rr.Code)
		}
		var resp struct {
			Results []struct {
				Path string `json:"path"`
			} `json:"results"`
		}
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("decode search response: %v", err)
		}
		paths := make([]string, 0, len(resp.Results))
		for _, r := range resp.Results {
			paths = append(paths, r.Path)
		}
		return paths
	}

	if got := search(`handler`, true); len(got) != 1 || got[0] != "handler.go" {
		t.Errorf("whole-word pattern matched %v, want exactly [handler.go]", got)
	}
	// Without the toggle the same pattern is a plain substring test, so the longer name answers too.
	if got := search(`handler`, false); len(got) != 2 {
		t.Errorf("pattern without the whole-word toggle matched %v, want both files", got)
	}
	if got := search(`^handler\.go$`, true); len(got) != 1 || got[0] != "handler.go" {
		t.Errorf("anchored whole-word pattern matched %v, want exactly [handler.go]", got)
	}
}

// TestFSSearchSkipsDotNames: neither half of a search reaches a dot-prefixed file or directory, and the
// non-dot caches stay out as well. One token sits in most files of the fixture, so a single content query
// answers both halves.
func TestFSSearchSkipsDotNames(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)
	base := t.TempDir()
	for rel, content := range map[string]string{
		".env":                      "token = dot-search-token\n",
		".gitignore":                "build/\n",
		".github/workflows/ci.yml":  "name: dot-search-token\n",
		".vscode/settings.json":     "dot-search-token in the editor settings\n",
		"main.go":                   "package main // dot-search-token\n",
		".git/config":               "dot-search-token from the git directory\n",
		"node_modules/pkg/leak.txt": "dot-search-token from node_modules\n",
	} {
		full := filepath.Join(base, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.store.CreateProject(&types.Project{ID: "fs-dots", Name: "FS Dots Project", Path: base}); err != nil {
		t.Fatal(err)
	}

	type searchResp struct {
		Results []struct {
			Path string `json:"path"`
		} `json:"results"`
	}

	search := func(q string, content bool) map[string]bool {
		t.Helper()
		v := url.Values{"project": {"fs-dots"}, "q": {q}}
		if content {
			v.Set("content", "1")
		}
		req := testRequest(http.MethodGet, "/api/fs/search?"+v.Encode(), nil)
		withAuthCSRF(req, cookie, csrf)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("search %q: %d %s", q, rr.Code, rr.Body.String())
		}
		var resp searchResp
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("decode search response: %v", err)
		}
		got := map[string]bool{}
		for _, r := range resp.Results {
			got[r.Path] = true
		}
		return got
	}

	for _, q := range []string{"ci.yml", "env", "gitignore", "settings"} {
		if names := search(q, false); len(names) != 0 {
			t.Errorf("filename search for %q reached a dot-prefixed name: %v", q, names)
		}
	}
	if names := search("main", false); !names["main.go"] {
		t.Errorf("filename search lost the ordinary file: %v", names)
	}

	contents := search("dot-search-token", true)
	if !contents["main.go"] {
		t.Errorf("content search missed the ordinary file: %v", contents)
	}
	for _, skip := range []string{".env",
		filepath.Join(".github", "workflows", "ci.yml"),
		filepath.Join(".vscode", "settings.json"),
		filepath.Join(".git", "config"),
		filepath.Join("node_modules", "pkg", "leak.txt")} {
		if contents[skip] {
			t.Errorf("content search read into %s, which no search walks", skip)
		}
	}
}

// TestFSSearchLongLine: one long line must not end the scan of its file, so a match after a minified line
// is still found.
func TestFSSearchLongLine(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)
	base := t.TempDir()
	long := strings.Repeat("x", 300_000) // a minified line is this long
	if err := os.WriteFile(filepath.Join(base, "min.css"), []byte("needle before\n"+long+"\nneedle after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.store.CreateProject(&types.Project{ID: "fs-longline", Name: "FS Long Line", Path: base}); err != nil {
		t.Fatal(err)
	}

	v := url.Values{"project": {"fs-longline"}, "q": {"needle"}, "content": {"1"}}
	req := testRequest(http.MethodGet, "/api/fs/search?"+v.Encode(), nil)
	withAuthCSRF(req, cookie, csrf)
	rr := testResponse(t, s, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("search: %d %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Results []struct {
			Line int    `json:"line"`
			Text string `json:"text"`
		} `json:"results"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("a long line ended the file's scan: got %d results, want the line before and after it", len(resp.Results))
	}
	if resp.Results[0].Line != 1 || resp.Results[1].Line != 3 {
		t.Errorf("line numbers after a long line: got %d and %d, want 1 and 3", resp.Results[0].Line, resp.Results[1].Line)
	}
}

// TestFSSearchFoldsNonASCII: a line with a non-ASCII byte takes the same fold the rest of the product uses,
// or case-insensitive search is quietly ASCII-only.
func TestFSSearchFoldsNonASCII(t *testing.T) {
	s := testServer(t)
	cookie, csrf := loginTestCSRF(t, s)
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "notes.txt"), []byte("café ÉCOLE\nplain ASCII LINE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.store.CreateProject(&types.Project{ID: "fs-fold", Name: "FS Fold", Path: base}); err != nil {
		t.Fatal(err)
	}

	search := func(q string, params ...string) map[int]bool {
		t.Helper()
		v := url.Values{"project": {"fs-fold"}, "q": {q}, "content": {"1"}}
		for i := 0; i+1 < len(params); i += 2 {
			v.Set(params[i], params[i+1])
		}
		req := testRequest(http.MethodGet, "/api/fs/search?"+v.Encode(), nil)
		withAuthCSRF(req, cookie, csrf)
		rr := testResponse(t, s, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("search %q: %d %s", q, rr.Code, rr.Body.String())
		}
		var resp struct {
			Results []struct {
				Line int `json:"line"`
			} `json:"results"`
		}
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatal(err)
		}
		got := map[int]bool{}
		for _, r := range resp.Results {
			got[r.Line] = true
		}
		return got
	}

	if lines := search("école"); !lines[1] {
		t.Errorf("a non-ASCII line was not folded for a case-insensitive search: %v", lines)
	}
	if lines := search("line"); !lines[2] {
		t.Errorf("an ASCII line was not folded: %v", lines)
	}
	if lines := search("école", "case", "1"); lines[1] {
		t.Errorf("case-sensitive search matched a differently-cased non-ASCII line: %v", lines)
	}
}

// TestSearchSlotsBound: a search that cannot get a walk slot waits for one, and that wait ends when its
// request ends.
func TestSearchSlotsBound(t *testing.T) {
	slots := make(searchSlots, 2)

	first, err := slots.acquire(context.Background())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	second, err := slots.acquire(context.Background())
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	defer second()

	type outcome struct {
		release func()
		err     error
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	admitted := make(chan outcome, 1)
	go func() {
		release, err := slots.acquire(ctx)
		admitted <- outcome{release: release, err: err}
	}()

	select {
	case got := <-admitted:
		t.Fatalf("a third walk was admitted while both slots were held: %v", got.err)
	case <-time.After(50 * time.Millisecond):
	}

	// The freed slot goes to the waiter, so both are held again for what follows.
	first()
	var third func()
	select {
	case got := <-admitted:
		if got.err != nil {
			t.Fatalf("waiter after a release: %v", got.err)
		}
		third = got.release
		defer third()
	case <-time.After(2 * time.Second):
		t.Fatal("the released slot was never taken")
	}

	// A spent release must neither block nor free a slot the search no longer holds.
	first()

	// Both slots are held again, so this waiter's only way out is its own context.
	blocked, cancelBlocked := context.WithCancel(context.Background())
	blockedErr := make(chan error, 1)
	go func() {
		_, err := slots.acquire(blocked)
		blockedErr <- err
	}()
	cancelBlocked()
	select {
	case err := <-blockedErr:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("a wait with no reader ended with %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the cancelled wait never ended")
	}
}
