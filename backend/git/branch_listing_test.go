package git

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"kanban/backend/types"
)

func TestListBranchNamesStripsGitDecoration(t *testing.T) {
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
}

func TestListBranchNamesStripsCurrentBranchMarker(t *testing.T) {
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
}

func TestListBranchNamesSlugPrefixMatchesOnlyItsOwn(t *testing.T) {
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
}

func TestListBranchNamesSweepsOneSegmentOnly(t *testing.T) {
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
}
