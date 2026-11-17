package exec

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"mike/backend/testutil/testgit"
)

// conflictedRepo is a repository with master and feature changing the same line and no merge
// started. Merge it to reach the state isMergeInProgress and GetConflictedFiles are judged on.
func conflictedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	testgit.RepoAt(t, dir)
	testgit.Git(t, dir, "checkout", "-b", "feature")
	testgit.WriteFile(t, dir, "file.txt", "line 1\nfeature\nline 3\n")
	testgit.Git(t, dir, "add", "file.txt")
	testgit.Git(t, dir, "commit", "-m", "Feature change")
	testgit.Git(t, dir, "checkout", "master")
	testgit.WriteFile(t, dir, "file.txt", "line 1\nmaster\nline 3\n")
	testgit.Git(t, dir, "add", "file.txt")
	testgit.Git(t, dir, "commit", "-m", "Master change")
	return dir
}

// TestIsMergeInProgress asks the one question the conflict resolver branches on — is there a merge
// to resolve here — in the repository and in a worktree (where .git is a file, not a directory),
// and says no both before a merge and after it was aborted.
func TestIsMergeInProgress(t *testing.T) {
	t.Parallel()

	t.Run("a clean repository has no merge", func(t *testing.T) {
		dir := testgit.Repo(t)
		if isMergeInProgress(dir) {
			t.Fatal("a repository nobody merged into reports a merge in progress")
		}
		if files := GetConflictedFiles(dir); len(files) != 0 {
			t.Fatalf("a clean repository reports conflicted files %v", files)
		}
	})

	t.Run("a conflicted merge in a repository", func(t *testing.T) {
		dir := conflictedRepo(t)
		merge := exec.Command("git", "merge", "feature")
		merge.Dir = dir
		_ = merge.Run() // expected to fail with a conflict

		if !isMergeInProgress(dir) {
			t.Fatal("expected isMergeInProgress to return true during merge conflict")
		}

		testgit.Git(t, dir, "merge", "--abort")
		if isMergeInProgress(dir) {
			t.Fatal("expected isMergeInProgress to return false after abort")
		}
	})

	t.Run("a conflicted merge in a worktree", func(t *testing.T) {
		tmpDir := t.TempDir()
		mainRepo := filepath.Join(tmpDir, "main")
		testgit.RepoAt(t, mainRepo)
		wtDir := filepath.Join(tmpDir, "worktree")
		testgit.Git(t, mainRepo, "worktree", "add", wtDir, "-b", "wt-branch")

		testgit.WriteFile(t, wtDir, "file.txt", "line 1\nwt change\nline 3\n")
		testgit.Git(t, wtDir, "add", "file.txt")
		testgit.Git(t, wtDir, "commit", "-m", "WT change")

		testgit.Git(t, mainRepo, "checkout", "master")
		testgit.WriteFile(t, mainRepo, "file.txt", "line 1\nmain change\nline 3\n")
		testgit.Git(t, mainRepo, "add", "file.txt")
		testgit.Git(t, mainRepo, "commit", "-m", "Main change")

		merge := exec.Command("git", "merge", "master")
		merge.Dir = wtDir
		_ = merge.Run()

		if !isMergeInProgress(wtDir) {
			t.Fatal("expected isMergeInProgress to return true inside worktree during merge conflict")
		}

		testgit.Git(t, wtDir, "merge", "--abort")
		if isMergeInProgress(wtDir) {
			t.Fatal("expected isMergeInProgress to return false after abort in worktree")
		}
	})
}

// TestGetMergeStatusOutput keeps the diagnostic a refused merge reports non-empty: the clean tree, the
// dirty one and a directory that is no repository at all all answer with something a person can read.
func TestGetMergeStatusOutput(t *testing.T) {
	t.Parallel()

	t.Run("clean, dirty and not-a-repository", func(t *testing.T) {
		dir := testgit.Repo(t)
		if output := getMergeStatusOutput(dir); output != "(working tree clean)" {
			t.Fatalf("expected '(working tree clean)', got %q", output)
		}

		// Dirty working tree
		testgit.WriteFile(t, dir, "file.txt", "modified\n")
		output := getMergeStatusOutput(dir)
		if output == "(working tree clean)" {
			t.Fatal("expected dirty status, got clean")
		}
		if !strings.Contains(output, "file.txt") {
			t.Fatalf("expected status to mention file.txt, got %q", output)
		}
	})

	t.Run("a directory that is no repository", func(t *testing.T) {
		if output := getMergeStatusOutput(t.TempDir()); !strings.Contains(output, "git status failed") {
			t.Fatalf("expected error message for non-git dir, got %q", output)
		}
	})
}

// TestGetConflictedFiles_OptionsShapedAndQuotedNames checks that a conflicted file whose name git
// would quote, or read as one of its own options, still comes back as a path and gets staged.
func TestGetConflictedFiles_OptionsShapedAndQuotedNames(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	testgit.Git(t, tmpDir, "init")
	testgit.Git(t, tmpDir, "config", "user.email", "test@test.com")
	testgit.Git(t, tmpDir, "config", "user.name", "Test")

	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("--force", "base\n")
	write("café.txt", "base\n")
	write("UU-notes.txt", "base\n")
	testgit.Git(t, tmpDir, "add", "-A")
	testgit.Git(t, tmpDir, "commit", "-m", "base")

	testgit.Git(t, tmpDir, "checkout", "-b", "feature")
	write("--force", "feature\n")
	write("café.txt", "feature\n")
	testgit.Git(t, tmpDir, "mv", "UU-notes.txt", "notes.txt")
	testgit.Git(t, tmpDir, "commit", "-qam", "feature")

	testgit.Git(t, tmpDir, "checkout", "master")
	write("--force", "master\n")
	write("café.txt", "master\n")
	testgit.Git(t, tmpDir, "commit", "-qam", "master")

	// The merge conflicts on both files and applies the rename cleanly.
	merge := exec.Command("git", "merge", "feature")
	merge.Dir = tmpDir
	_ = merge.Run()

	got := append([]string(nil), GetConflictedFiles(tmpDir)...)
	sort.Strings(got)
	// The rename merged cleanly, so neither its target nor its source is conflicted.
	if want := []string{"--force", "café.txt"}; !slices.Equal(got, want) {
		t.Fatalf("conflicted files = %q, want %q", got, want)
	}

	write("--force", "resolved\n")
	write("café.txt", "resolved\n")
	if !(&mergeOrchestrator{}).stageResolvedConflicts("task-1", tmpDir) {
		t.Fatal("stageResolvedConflicts failed to stage the resolved files")
	}

	listCmd := exec.Command("git", "diff", "--cached", "--name-only", "-z")
	listCmd.Dir = tmpDir
	stagedRaw, err := listCmd.Output()
	if err != nil {
		t.Fatalf("git diff --cached --name-only: %v", err)
	}
	staged := strings.Split(string(stagedRaw), "\x00")
	for _, name := range []string{"--force", "café.txt"} {
		if !slices.Contains(staged, name) {
			t.Errorf("%q was not staged; staged: %q", name, staged)
		}
	}
	// "--force" is a real option of git add too, and taken as one it leaves the file conflicted.
	if still := GetConflictedFiles(tmpDir); len(still) != 0 {
		t.Errorf("%q is still conflicted after staging the resolution", still)
	}
}
