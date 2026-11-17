package git

// branches.go owns the branch listing, the name-usability rule, the divergence point, the merged-into
// answer and Main-branch recovery. The rule behind the refusals is the one written at usableBranchName:
// a value git would read as more than one argument is never returned to a caller that hands it back.

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mike/backend/types"
)

func checkoutNamed(t *testing.T, dir, name string) {
	t.Helper()
	runGit(t, dir, "update-ref", "refs/heads/"+name, "HEAD")
	runGit(t, dir, "symbolic-ref", "HEAD", "refs/heads/"+name)
}

// The listing feeds the orphan sweep, so `git branch --list`'s decorations — "+" on a branch a linked
// worktree holds, "*" on the current one — must not reach a comparison.
func TestListBranchNamesStripsGitsDecoration(t *testing.T) {
	t.Run("a branch another worktree holds", func(t *testing.T) {
		repoDir, _, _, _, wm := setupTestRepo(t)
		runGit(t, repoDir, "branch", "task/plain")
		runGit(t, repoDir, "worktree", "add", "-b", "task/held-by-worktree", filepath.Join(t.TempDir(), "wt"))

		if listed := strings.TrimSpace(runGitOutput(t, repoDir, "branch", "--list", "task/*")); !strings.Contains(listed, "+ task/held-by-worktree") {
			t.Fatalf("git stopped decorating a worktree checkout, so this test is measuring nothing:\n%s", listed)
		}

		got, err := wm.listBranchNames(repoDir, types.TaskBranchPrefix)
		if err != nil {
			t.Fatalf("listBranchNames: %v", err)
		}
		want := []string{"task/held-by-worktree", "task/plain"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("listBranchNames = %q, want %q", got, want)
		}
	})

	t.Run("the current branch", func(t *testing.T) {
		repoDir, _, _, _, wm := setupTestRepo(t)
		runGit(t, repoDir, "checkout", "-b", types.TaskBranchPrefix+"is-checked-out")

		if listed := strings.TrimSpace(runGitOutput(t, repoDir, "branch", "--list", "task/*")); !strings.HasPrefix(listed, "* task/is-checked-out") {
			t.Fatalf("git stopped marking the current branch, so this test is measuring nothing:\n%s", listed)
		}

		got, err := wm.listBranchNames(repoDir, types.TaskBranchPrefix)
		if err != nil {
			t.Fatalf("listBranchNames: %v", err)
		}
		if len(got) != 1 || got[0] != "task/is-checked-out" {
			t.Errorf("listBranchNames = %q, want the bare branch name", got)
		}
	})
}

// The glob is one path segment wide and matches the prefix literally: the shape this repo writes is
// task/<id>, task/<slug>-<random6> and thread/<id>, and a nested branch a person made under the same
// namespace is never read as an orphan.
func TestListBranchNamesGlobsOneSegmentOfItsOwnPrefix(t *testing.T) {
	t.Run("a slug prefix matches only its own branch", func(t *testing.T) {
		repoDir, _, _, _, wm := setupTestRepo(t)
		runGit(t, repoDir, "branch", "task/hey-zimxpq")
		runGit(t, repoDir, "branch", "task/other-abc123")

		got, err := wm.ListProjectBranches("test-project", types.TaskBranchPrefix+"hey-")
		if err != nil {
			t.Fatalf("ListProjectBranches: %v", err)
		}
		if len(got) != 1 || got[0] != "task/hey-zimxpq" {
			t.Errorf("ListProjectBranches with a slug prefix = %q, want only task/hey-zimxpq", got)
		}
	})

	t.Run("a nested branch under the namespace is not swept", func(t *testing.T) {
		repoDir, _, _, _, wm := setupTestRepo(t)
		runGit(t, repoDir, "branch", "task/human-made-this")
		runGit(t, repoDir, "branch", "task/human/made-this")

		got, err := wm.listBranchNames(repoDir, types.TaskBranchPrefix)
		if err != nil {
			t.Fatalf("listBranchNames: %v", err)
		}
		for _, b := range got {
			if b == "task/human/made-this" {
				t.Errorf("listBranchNames returned a nested branch %q; the sweep would treat it as an orphan", b)
			}
		}
		if !slices.Contains(got, "task/human-made-this") {
			t.Errorf("listBranchNames = %q, want the one-segment task/human-made-this in it", got)
		}
	})
}

// The rule is "cannot be one argument", not a character set: an option-shaped name reaches git as a
// flag, and a name outside ASCII is a name somebody named.
func TestGitCurrentBranchRefusesWhatGitWouldReadAsMoreThanOneArgument(t *testing.T) {
	t.Run("an option-shaped name", func(t *testing.T) {
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
	})

	t.Run("a name outside ASCII", func(t *testing.T) {
		repoDir, _, _, _, wm := setupTestRepo(t)
		checkoutNamed(t, repoDir, "主干")

		name, err := wm.gitCurrentBranch(repoDir)
		if err != nil {
			t.Fatalf("gitCurrentBranch: %v", err)
		}
		if name != "主干" {
			t.Errorf("gitCurrentBranch = %q, want 主干", name)
		}
	})
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

// DivergenceBase answers the commit a task branch was cut at, or nothing at all — never a name a caller
// would hand back to git.
func TestDivergenceBaseAnswersTheForkPointOrNothing(t *testing.T) {
	t.Run("the fork point, named or inferred", func(t *testing.T) {
		repoDir, _, _, _, wm := setupTestRepo(t)

		mainBranch, err := wm.gitCurrentBranch(repoDir)
		if err != nil || mainBranch == "" || mainBranch == "HEAD" {
			t.Fatalf("the fixture repo holds a commit, so rev-parse must name its branch: got %q (err %v)", mainBranch, err)
		}
		forkPoint := revParse(t, repoDir, "HEAD")

		// Fork a task branch from the initial commit, then commit on both sides.
		runGit(t, repoDir, "branch", "task/divergent")
		runGit(t, repoDir, "checkout", "task/divergent")
		writeFile(t, repoDir, "branch.txt", "branch\n")
		runGit(t, repoDir, "add", "branch.txt")
		runGit(t, repoDir, "commit", "-m", "branch commit")
		runGit(t, repoDir, "checkout", mainBranch)
		writeFile(t, repoDir, "main.txt", "main\n")
		runGit(t, repoDir, "add", "main.txt")
		runGit(t, repoDir, "commit", "-m", "main commit")

		if got := wm.DivergenceBase(repoDir, mainBranch, "task/divergent"); got != forkPoint {
			t.Errorf("DivergenceBase(explicit main) = %q, want fork point %q", got, forkPoint)
		}
		// Empty mainBranch falls back to the project's current branch.
		if got := wm.DivergenceBase(repoDir, "", "task/divergent"); got != forkPoint {
			t.Errorf("DivergenceBase(empty main) = %q, want fork point %q", got, forkPoint)
		}
	})

	t.Run("a ref that resolves to nothing", func(t *testing.T) {
		repoDir, _, _, _, wm := setupTestRepo(t)

		if got := wm.DivergenceBase(repoDir, "main", ""); got != "" {
			t.Errorf("empty taskBranch should return empty, got %q", got)
		}
		if got := wm.DivergenceBase("", "main", "task/x"); got != "" {
			t.Errorf("empty projectPath should return empty, got %q", got)
		}
		if got := wm.DivergenceBase(repoDir, "main", "task/does-not-exist"); got != "" {
			t.Errorf("nonexistent branch should return empty, got %q", got)
		}
	})

	t.Run("a detached HEAD has no main to measure from", func(t *testing.T) {
		repoDir, _, _, _, wm := setupTestRepo(t)

		mainBranch, err := wm.gitCurrentBranch(repoDir)
		if err != nil || mainBranch == "" || mainBranch == "HEAD" {
			t.Fatalf("the fixture repo holds a commit, so rev-parse must name its branch: got %q (err %v)", mainBranch, err)
		}
		runGit(t, repoDir, "checkout", "--detach", "HEAD")

		if got := wm.DivergenceBase(repoDir, "", "main"); got != "" {
			t.Errorf("detached HEAD with empty main should return empty, got %q", got)
		}
		// An explicit main still resolves.
		if got := wm.DivergenceBase(repoDir, mainBranch, mainBranch); got == "" {
			t.Error("explicit main should resolve, got empty")
		}
	})

	t.Run("an unusable main branch name", func(t *testing.T) {
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
	})
}

// Nothing is marked Done that did not reach the target branch, and this is the answer that decides it —
// a merge that only moved a ref by fast-forward still counts.
func TestIsMergedInto(t *testing.T) {
	// mergedFixture is one repo holding master plus a task-branch with a commit of its own.
	mergedFixture := func(t *testing.T) string {
		t.Helper()
		repoDir := t.TempDir()
		runGit(t, repoDir, "init")
		runGit(t, repoDir, "config", "user.email", "test@test.com")
		runGit(t, repoDir, "config", "user.name", "Test")
		writeFile(t, repoDir, "README.md", "# Test\n")
		runGit(t, repoDir, "add", "README.md")
		runGit(t, repoDir, "commit", "-m", "Initial")
		runGit(t, repoDir, "checkout", "-b", "task-branch")
		writeFile(t, repoDir, "feature.txt", "feature content\n")
		runGit(t, repoDir, "add", "feature.txt")
		runGit(t, repoDir, "commit", "-m", "Add feature")
		runGit(t, repoDir, "checkout", "master")
		return repoDir
	}

	wm := &WorktreeManager{}

	for _, style := range []struct {
		name  string
		merge []string
	}{
		{"a merge commit", []string{"merge", "task-branch", "--no-ff", "-m", "Merge task-branch"}},
		{"a fast-forward", []string{"merge", "task-branch"}},
	} {
		t.Run("ancestor after "+style.name, func(t *testing.T) {
			repoDir := mergedFixture(t)

			if wm.IsMergedInto(repoDir, "task-branch", "master") {
				t.Fatal("the task branch is not merged yet")
			}
			runGit(t, repoDir, style.merge...)
			if !wm.IsMergedInto(repoDir, "task-branch", "master") {
				t.Errorf("IsMergedInto = false after %s, so a landed task would be refused Review", style.name)
			}
		})
	}

	t.Run("an empty or unknown name is never merged", func(t *testing.T) {
		repoDir := mergedFixture(t)

		if wm.IsMergedInto(repoDir, "", "master") {
			t.Error("IsMergedInto should return false for empty task branch")
		}
		if wm.IsMergedInto(repoDir, "task-branch", "") {
			t.Error("IsMergedInto should return false for empty target branch")
		}
		if wm.IsMergedInto(repoDir, "nonexistent", "master") {
			t.Error("IsMergedInto should return false for non-existent task branch")
		}
	})
}

// The task row names task/<taskID>, and only a model-written title names the slug branch. A GC or a
// recovery that parses a different shape than the executor writes deletes, or loses, a live branch.
func TestResolveMainBranch(t *testing.T) {
	t.Run("recovers the slug branch when the canonical one is gone", func(t *testing.T) {
		repoDir, taskID, s, _, wm := setupTestRepo(t)

		task, err := s.GetTask("test-project", taskID)
		if err != nil {
			t.Fatalf("get task: %v", err)
		}
		task.Title = "Hey" // slug "hey"
		if err := s.UpdateTask(task); err != nil {
			t.Fatalf("update task: %v", err)
		}

		// The real Main branch is slug-named; task/<taskID> does not exist.
		runGit(t, repoDir, "branch", "task/hey-zimxpq")

		got := wm.ResolveMainBranch("test-project", taskID, task.Title, "task/"+taskID)
		if got != "task/hey-zimxpq" {
			t.Fatalf("expected recovered slug branch task/hey-zimxpq, got %q", got)
		}

		// If the canonical branch exists, it takes precedence.
		runGit(t, repoDir, "branch", "task/"+taskID)
		got = wm.ResolveMainBranch("test-project", taskID, task.Title, "task/"+taskID)
		if got != "task/"+taskID {
			t.Fatalf("expected canonical task/%s when it exists, got %q", taskID, got)
		}
	})

	t.Run("falls back to the canonical name when no slug matches", func(t *testing.T) {
		repoDir, taskID, _, _, wm := setupTestRepo(t)
		// No task/<taskID> branch and a title that produces no matching branch.
		runGit(t, repoDir, "branch", "task/other-zzz")
		got := wm.ResolveMainBranch("test-project", taskID, "No Match", "task/"+taskID)
		if got != "task/"+taskID {
			t.Fatalf("expected fallback task/%s, got %q", taskID, got)
		}
	})
}
