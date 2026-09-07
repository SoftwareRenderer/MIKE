// Package testgit builds throwaway git repositories for tests, so the packages that touch git
// stop each carrying their own copy of "init a repo, commit one file, fail the test on error".
//
//   - Every command carries an explicit identity (-c user.name / -c user.email): a real git
//     commit fails where no global identity exists, and a test must not depend on the
//     developer's config.
//   - Init does NOT rename the initial branch: suites that follow with git worktree add -B main
//     create main rather than move it, so pinning it here would break them.
//
// Test-only: nothing outside _test files may import it.
package testgit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/git"
)

// Identity written into every repository created here.
const (
	AuthorName  = "Test"
	AuthorEmail = "test@test.com"
)

// identityArgs are prepended to every command so a commit works with no global git config.
var identityArgs = []string{"-c", "user.name=" + AuthorName, "-c", "user.email=" + AuthorEmail}

// Git runs `git <args>` with dir as the working directory and fails the test on error.
func Git(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := gitCmd(t, dir, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// GitOutput runs `git <args>` in dir and returns trimmed stdout, failing the test on error.
// It captures stdout only, so a command that writes diagnostics to stderr still yields a
// clean value (git rev-parse and friends).
func GitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitCmd(t, dir, args...).Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("git %v: %v: stdout=%s stderr=%s", args, err, out, stderr)
	}
	return strings.TrimSpace(string(out))
}

func gitCmd(t *testing.T, dir string, args ...string) *exec.Cmd {
	t.Helper()
	all := append(append([]string{}, identityArgs...), args...)
	cmd := git.Command("", all...)
	cmd.Dir = dir
	return cmd
}

// WriteFile writes content to name inside dir, creating parent directories.
func WriteFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// Init turns dir into a git repository with no commits.
func Init(t *testing.T, dir string) {
	t.Helper()
	Git(t, dir, "init")
}

// EmptyRepo returns a fresh temp directory holding a repository with no commits, on a branch
// named "main" (staging tests address files through that name).
func EmptyRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	Init(t, dir)
	Git(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")
	return dir
}

// Repo returns a fresh temp directory holding a repository with one commit, and the local
// identity configured (some code paths read `git config user.*` rather than taking -c flags).
func Repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	RepoAt(t, dir)
	return dir
}

// RepoAt turns dir into a repository with one committed file. The initial commit stages
// README.md ONLY: several fixtures write extra files into the directory before calling this
// and then commit them themselves, so staging the whole tree would silently swallow their
// second commit.
func RepoAt(t *testing.T, dir string) {
	t.Helper()
	RepoAtMessage(t, dir, "Initial commit")
}

// RepoAtMessage is RepoAt with an explicit subject for the root commit. Commit subjects are
// observable (git log output, diff baselines resolved by subject), so a suite that asserts
// one needs the fixture to write exactly that text.
func RepoAtMessage(t *testing.T, dir, subject string) {
	t.Helper()
	WriteFile(t, dir, "README.md", "# Test\n")
	Init(t, dir)
	Git(t, dir, "config", "user.email", AuthorEmail)
	Git(t, dir, "config", "user.name", AuthorName)
	Git(t, dir, "add", "README.md")
	Git(t, dir, "commit", "-m", subject)
}

// RevParse resolves a ref (HEAD, a branch, a tag) to its commit hash.
func RevParse(t *testing.T, dir, ref string) string {
	t.Helper()
	return GitOutput(t, dir, "rev-parse", ref)
}
