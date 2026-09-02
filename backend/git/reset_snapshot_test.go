package git

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gitTrim runs git for an assertion and trims the trailing newline runGitOutput leaves in place.
func gitTrim(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(runGitOutput(t, dir, args...))
}

// A file restore must not rewrite history. The old path ended in `git reset --hard <snapshot>` whenever
// the stash patch failed to apply — and a patch is exactly what git cannot render for a binary file.
func TestResetWorktreeTo_BinarySnapshotKeepsBranchAndBytes(t *testing.T) {
	repoDir, taskID, _, _, wm := setupTestRepo(t)
	branch := "task/" + taskID
	if _, _, err := wm.CreateWorktree("test-project", taskID, branch, ""); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	wt, err := wm.WorktreePath(taskID)
	if err != nil {
		t.Fatalf("WorktreePath: %v", err)
	}

	// A modified tracked binary: MIKE only unstages *newly added* binaries, so this one is in the
	// snapshot.
	original := []byte("\x89PNG\r\n\x1a\n\x00\x00binary-payload\x00\x01\x02")
	if err := os.WriteFile(filepath.Join(wt, "logo.png"), original, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, wt, "add", "-A")
	runGit(t, wt, "commit", "-m", "add logo")
	branchTip := gitTrim(t, wt, "rev-parse", "HEAD")

	replacement := bytes.Repeat([]byte("\xff\x00new-binary"), 400)
	if err := os.WriteFile(filepath.Join(wt, "logo.png"), replacement, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt, "note.txt"), []byte("turn output\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, wt, "add", "-A")
	snapshot := gitTrim(t, wt, "stash", "create")

	// The turn moves on: different content, different new file.
	if err := os.WriteFile(filepath.Join(wt, "logo.png"), bytes.Repeat([]byte("other"), 900), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt, "later.txt"), []byte("must go\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := wm.ResetWorktreeTo(taskID, snapshot); err != nil {
		t.Fatalf("ResetWorktreeTo: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(wt, "logo.png"))
	if err != nil {
		t.Fatalf("read restored binary: %v", err)
	}
	if !bytes.Equal(got, replacement) {
		t.Errorf("restored binary differs: %d bytes, want %d", len(got), len(replacement))
	}
	if _, err := os.Stat(filepath.Join(wt, "note.txt")); err != nil {
		t.Errorf("the snapshot's text file must be restored too: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "later.txt")); !os.IsNotExist(err) {
		t.Error("files the snapshot does not contain must be gone")
	}

	// The assertion this test exists for: a restore is not a history edit.
	if head := gitTrim(t, wt, "rev-parse", "HEAD"); head != branchTip {
		t.Errorf("HEAD moved from %s to %s", branchTip, head)
	}
	if tip := gitTrim(t, repoDir, "rev-parse", branch); tip != branchTip {
		t.Errorf("branch %s was moved onto the snapshot commit (%s)", branch, tip)
	}
	// The restored work reads as the task's own changes, not a commit someone has to undo.
	if staged := runGitOutput(t, wt, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("a restore must not stage anything, staged: %q", staged)
	}
}

// An unresolvable snapshot must be refused before anything is discarded: the old `reset --hard` fallback
// failed on a bad hash after the worktree had already been cleaned.
func TestResetWorktreeTo_UnknownSnapshotRestoresNothing(t *testing.T) {
	_, taskID, _, _, wm := setupTestRepo(t)
	branch := "task/" + taskID
	if _, _, err := wm.CreateWorktree("test-project", taskID, branch, ""); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	wt, err := wm.WorktreePath(taskID)
	if err != nil {
		t.Fatalf("WorktreePath: %v", err)
	}
	writeFile(t, wt, "keepme.txt", "uncommitted work\n")
	headBefore := gitTrim(t, wt, "rev-parse", "HEAD")

	err = wm.ResetWorktreeTo(taskID, "0000000000000000000000000000000000000000")
	if !errors.Is(err, ErrSnapshotMissing) {
		t.Fatalf("expected ErrSnapshotMissing, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "keepme.txt")); err != nil {
		t.Error("a refused restore must leave the working files alone")
	}
	if gitTrim(t, wt, "rev-parse", "HEAD") != headBefore {
		t.Error("HEAD must not move")
	}

	// An empty hash (a thread with no saved state) must not wipe anything either.
	if err := wm.ResetWorktreeTo(taskID, "  "); err == nil {
		t.Error("an empty snapshot must be refused")
	}
	if _, err := os.Stat(filepath.Join(wt, "keepme.txt")); err != nil {
		t.Error("an empty snapshot must leave the working files alone")
	}
}

// Old rows store the hash of a `git stash create` commit. That is still a commit whose tree is the working
// state, so the tree restore reads them with no migration — and HEAD still has no reason to move.
func TestResetWorktreeTo_LegacyStashCommitRestoresFromItsTree(t *testing.T) {
	_, taskID, _, _, wm := setupTestRepo(t)
	branch := "task/" + taskID
	if _, _, err := wm.CreateWorktree("test-project", taskID, branch, ""); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	wt, err := wm.WorktreePath(taskID)
	if err != nil {
		t.Fatalf("WorktreePath: %v", err)
	}
	writeFile(t, wt, "count.bas", "count to 10\n")
	writeFile(t, wt, "scratch.txt", "untracked at checkpoint\n")
	headBefore := gitTrim(t, wt, "rev-parse", "HEAD")

	runGit(t, wt, "add", "-A")
	runGit(t, wt, "stash", "push", "-u", "-m", "WIP on task checkpoint")
	stashHash := gitTrim(t, wt, "rev-parse", "stash@{0}")
	if stashHash == headBefore {
		t.Fatal("expected a real stash commit")
	}

	// The conversation moved on.
	writeFile(t, wt, "count.bas", "count to 3\n")
	writeFile(t, wt, "scratch.txt", "rewritten\n")

	if err := wm.ResetWorktreeTo(taskID, stashHash); err != nil {
		t.Fatalf("ResetWorktreeTo: %v", err)
	}
	for name, want := range map[string]string{"count.bas": "count to 10\n", "scratch.txt": "untracked at checkpoint\n"} {
		data, err := os.ReadFile(filepath.Join(wt, name))
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		if string(data) != want {
			t.Errorf("%s restored to %q, want %q", name, data, want)
		}
	}
	if got := gitTrim(t, wt, "rev-parse", "HEAD"); got != headBefore {
		t.Errorf("HEAD moved from %s to %s — a file restore must not rewrite history", headBefore, got)
	}
	if got := gitTrim(t, wt, "branch", "--show-current"); got != branch {
		t.Errorf("branch is %q, want %q", got, branch)
	}
}
