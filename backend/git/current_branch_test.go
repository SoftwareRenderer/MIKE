package git

// The rule these tests pin lives at usableBranchName in branches.go.

import (
	"strings"
	"testing"
)

func checkoutNamed(t *testing.T, dir, name string) {
	t.Helper()
	runGit(t, dir, "update-ref", "refs/heads/"+name, "HEAD")
	runGit(t, dir, "symbolic-ref", "HEAD", "refs/heads/"+name)
}

func TestCurrentBranchRefusesANameGitWouldReadAsAnOption(t *testing.T) {
	repoDir, _, _, _, wm := setupTestRepo(t)
	checkoutNamed(t, repoDir, "--delete-me")

	// The fixture has to be real, or the refusal below measures nothing.
	if raw := strings.TrimSpace(runGitOutput(t, repoDir, "rev-parse", "--abbrev-ref", "HEAD")); raw != "--delete-me" {
		t.Fatalf("fixture left HEAD on %q, want --delete-me", raw)
	}

	name, err := wm.gitCurrentBranch(repoDir)
	if err == nil {
		t.Fatalf("gitCurrentBranch returned %q with no error; a name starting with '-' reaches git as a flag", name)
	}
	if name != "" {
		t.Errorf("gitCurrentBranch returned %q alongside the error; the value is what a caller stores, so it must be empty", name)
	}
}

// A failed rev-parse prints git's reason to the combined output, so prose must never become the
// value stored as project.origin_branch or task.base_ref.
func TestCurrentBranchAnswersNothingWhenGitNamesNoBranch(t *testing.T) {
	_, _, _, _, wm := setupTestRepo(t)
	unborn := t.TempDir()
	runGit(t, unborn, "init", "-q")

	name, err := wm.gitCurrentBranch(unborn)
	if err == nil {
		t.Fatalf("gitCurrentBranch returned %q with no error on a repository with no commit", name)
	}
	if name != "" {
		t.Errorf("gitCurrentBranch = %q, want the empty answer", name)
	}
}

// The rule is "cannot be one argument", not a character set.
func TestCurrentBranchKeepsANameOutsideASCII(t *testing.T) {
	repoDir, _, _, _, wm := setupTestRepo(t)
	checkoutNamed(t, repoDir, "主干")

	name, err := wm.gitCurrentBranch(repoDir)
	if err != nil {
		t.Fatalf("gitCurrentBranch: %v", err)
	}
	if name != "主干" {
		t.Errorf("gitCurrentBranch = %q, want 主干", name)
	}
}

// A rejected main branch leaves the divergence point unknown rather than answered with the name a
// caller would hand back to git.
func TestDivergenceBaseAnswersNothingForAnUnusableMainBranch(t *testing.T) {
	repoDir, _, _, _, wm := setupTestRepo(t)
	runGit(t, repoDir, "branch", "task/one")
	// Which branch a fresh repo starts on is git's own default, so it is read rather than assumed.
	initial := strings.TrimSpace(runGitOutput(t, repoDir, "rev-parse", "--abbrev-ref", "HEAD"))
	checkoutNamed(t, repoDir, "--delete-me")

	if got := wm.DivergenceBase(repoDir, "", "task/one"); got != "" {
		t.Errorf("DivergenceBase = %q, want the empty answer that refuses the unusable name", got)
	}

	// The same call with a usable main branch answers a commit, so the empty above is the refusal.
	runGit(t, repoDir, "symbolic-ref", "HEAD", "refs/heads/"+initial)
	if got := wm.DivergenceBase(repoDir, "", "task/one"); len(got) != 40 {
		t.Errorf("DivergenceBase with a usable main branch = %q, want a commit hash", got)
	}
}

// Every refusal below would reach git as a flag, or as two words.
func TestUsableBranchNameRefusesWhatGitWouldReadAsMoreThanOneArgument(t *testing.T) {
	for _, name := range []string{"", "-", "--delete", "-c", "x y", "x\ty", "x\ny", "x\x7fy"} {
		if usableBranchName(name) {
			t.Errorf("usableBranchName(%q) = true, want false", name)
		}
	}
	for _, name := range []string{"main", "release/1.2", "task/my-task_1", "feature+flag", "主干"} {
		if !usableBranchName(name) {
			t.Errorf("usableBranchName(%q) = false, want true", name)
		}
	}
}
