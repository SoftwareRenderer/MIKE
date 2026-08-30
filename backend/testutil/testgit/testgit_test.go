package testgit

import (
	"os/exec"
	"strings"
	"testing"
)

// TestRepoIsCommitted checks the fixture every git suite depends on: exactly one commit, a
// resolvable HEAD, and the local identity written into config (some production paths read
// `git config user.*` instead of taking -c flags).
//
// The branch name is deliberately NOT asserted: Init leaves it on the machine's
// init.defaultBranch, and suites that later run `git worktree add -B main …` rely on main
// NOT already being checked out.
func TestRepoIsCommitted(t *testing.T) {
	dir := Repo(t)

	if count := GitOutput(t, dir, "rev-list", "--count", "HEAD"); count != "1" {
		t.Errorf("repo has %s commits, want 1", count)
	}
	if hash := RevParse(t, dir, "HEAD"); len(hash) != 40 {
		t.Errorf("RevParse(HEAD) = %q, want a commit hash", hash)
	}
	if got := GitOutput(t, dir, "config", "user.email"); got != AuthorEmail {
		t.Errorf("user.email = %q, want %q", got, AuthorEmail)
	}
	if got := GitOutput(t, dir, "status", "--porcelain"); got != "" {
		t.Errorf("a fresh repo has a dirty work tree: %q", got)
	}
}

// TestCommitWorksWithoutAGitIdentity is the reason this package passes -c user.* on every
// command. With the user's and the system's config files hidden, a commit only succeeds
// because the identity came from -c — on a machine (or CI runner) with no global git config,
// the same command otherwise dies with "Committer identity unknown", which reads like a
// product bug and is really a fixture bug.
func TestCommitWorksWithoutAGitIdentity(t *testing.T) {
	dir := Repo(t)
	WriteFile(t, dir, "second.txt", "content\n")
	Git(t, dir, "add", "second.txt")

	cmd := exec.Command("git", "-c", "user.name="+AuthorName, "-c", "user.email="+AuthorEmail,
		"commit", "-m", "identity-free")
	cmd.Dir = dir
	// Point git at an empty pair of config files instead of the developer's.
	cmd.Env = append(cmd.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit without any git config failed: %v: %s", err, out)
	}

	if count := GitOutput(t, dir, "rev-list", "--count", "HEAD"); count != "2" {
		t.Errorf("repo has %s commits after the identity-free commit, want 2", count)
	}
}

// TestEmptyRepoHasNoCommits pins the bare fixture used by staging tests: a working tree git
// recognises, with HEAD still unborn.
func TestEmptyRepoHasNoCommits(t *testing.T) {
	dir := EmptyRepo(t)

	if _, err := exec.Command("git", "-C", dir, "rev-parse", "--git-dir").Output(); err != nil {
		t.Fatalf("%s is not a git repository: %v", dir, err)
	}
	if branch, err := exec.Command("git", "-C", dir, "symbolic-ref", "--short", "HEAD").Output(); err != nil {
		t.Fatalf("empty repo has no HEAD branch: %v", err)
	} else if got := strings.TrimSpace(string(branch)); got != "main" {
		t.Errorf("HEAD is on %q, want main", got)
	}
	// No commits yet: rev-parse cannot resolve HEAD and exits non-zero.
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "--verify", "HEAD").CombinedOutput(); err == nil {
		t.Fatalf("empty repo already has a commit: %s", strings.TrimSpace(string(out)))
	}
}
