package git

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An untracked file's content ships to the browser in the diff, and git status lists a symlink the
// way it lists a file — so a `key -> ~/.ssh/id_rsa` planted in a task worktree must answer "no",
// not the key.

const secretCanary = "CANARY-NEVER-LEAVES-THE-HOME-DIRECTORY"

// worktreeWithSecretLink returns a task worktree containing an ordinary untracked file, a link
// to a file outside the worktree, and a link to a file inside it.
func worktreeWithSecretLink(t *testing.T) (wm *WorktreeManager, taskID, worktreePath, secretPath string) {
	t.Helper()
	_, taskID, _, _, wm = setupTestRepo(t)
	if _, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", ""); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		t.Fatal(err)
	}

	// The secret lives outside the worktree — outside the repository entirely.
	outside := t.TempDir()
	secretPath = filepath.Join(outside, "id_rsa")
	if err := os.WriteFile(secretPath, []byte(secretCanary+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	writeFile(t, worktreePath, "notes.txt", "a real untracked file\n")
	if err := os.Symlink(secretPath, filepath.Join(worktreePath, "key")); err != nil {
		t.Fatal(err)
	}
	// A link that stays inside must keep working: repos ship links, and the jail is about
	// leaving, not about linking.
	if err := os.Symlink("notes.txt", filepath.Join(worktreePath, "inside-link.txt")); err != nil {
		t.Fatal(err)
	}
	return wm, taskID, worktreePath, secretPath
}

func TestUntrackedSymlinkOutsideIsNeverReadIntoTheDiff(t *testing.T) {
	wm, taskID, _, secretPath := worktreeWithSecretLink(t)

	diff, err := wm.GetTaskDiff(taskID, "", "")
	if err != nil {
		t.Fatalf("GetTaskDiff: %v", err)
	}

	names := make([]string, 0, len(diff.Untracked))
	for _, u := range diff.Untracked {
		names = append(names, u.Path)
	}
	if len(diff.Untracked) == 0 || !strings.Contains(strings.Join(names, " "), "notes.txt") {
		t.Fatalf("the ordinary untracked file disappeared from the diff: %v", names)
	}
	for _, u := range diff.Untracked {
		if u.Path == "key" {
			t.Errorf("the escaping symlink was listed as file content: %q", strings.Join(u.Lines, "\n"))
		}
	}

	// The real assertion: nothing the browser receives carries the secret. The whole struct
	// is marshalled because a leak could ride any field.
	encoded, err := json.Marshal(diff)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secretCanary) {
		t.Error("the outside file's bytes reached the task diff")
	}
	if _, err := os.Stat(secretPath); err != nil {
		t.Errorf("the test's own fixture broke: %v", err)
	}
}

func TestUntrackedReadTargetRefusesWhatItMustAndAllowsWhatItMust(t *testing.T) {
	wm, taskID, worktreePath, secretPath := worktreeWithSecretLink(t)
	_ = wm
	_ = taskID

	// An ordinary file: allowed, and the size came back with it.
	target, size, err := UntrackedReadTarget(worktreePath, "notes.txt")
	if err != nil || target != filepath.Join(worktreePath, "notes.txt") || size == 0 {
		t.Fatalf("ordinary untracked file: %q, %d, %v", target, size, err)
	}

	// A link that resolves outside the worktree: refused with the containment error, not a
	// read that happened to fail.
	if _, _, err := UntrackedReadTarget(worktreePath, "key"); err == nil {
		t.Error("a symlink out of the worktree was accepted")
	} else if !strings.Contains(err.Error(), "outside") {
		t.Errorf("refusal should name containment, got: %v", err)
	}

	// A link that stays inside: readable, resolved to the target it names.
	target, _, err = UntrackedReadTarget(worktreePath, "inside-link.txt")
	if err != nil {
		t.Errorf("a link inside the worktree must stay readable: %v", err)
	} else if target != filepath.Join(worktreePath, "notes.txt") {
		t.Errorf("resolved target = %q, want the link's target", target)
	}

	// A directory is not file content, and neither is an absolute or escaping path.
	if _, _, err := UntrackedReadTarget(worktreePath, "."); err == nil {
		t.Error("a directory was accepted as untracked content")
	}
	if _, _, err := UntrackedReadTarget(worktreePath, secretPath); err == nil {
		t.Error("an absolute path was accepted as untracked content")
	}
	if _, _, err := UntrackedReadTarget(worktreePath, "../outside.txt"); err == nil {
		t.Error("a path escaping by .. was accepted")
	}
}

func TestWalkUntrackedEntriesDoesNotDescendLinks(t *testing.T) {
	wm, taskID, worktreePath, _ := worktreeWithSecretLink(t)
	_ = wm
	_ = taskID

	if got := walkUntrackedEntries(worktreePath, "key"); got != nil {
		t.Errorf("a symlink to a file was expanded into %v", got)
	}
	if got := walkUntrackedEntries(worktreePath, "notes.txt"); len(got) != 1 || got[0] != "notes.txt" {
		t.Errorf("ordinary file expanded to %v, want [notes.txt]", got)
	}

	// A directory reachable only through a link is not walked either: that would enumerate a
	// tree the worktree does not own.
	outside := filepath.Join(filepath.Dir(worktreePath), "outside-dir")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "listed.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(worktreePath, "link-dir")); err != nil {
		t.Fatal(err)
	}
	if got := walkUntrackedEntries(worktreePath, "link-dir"); got != nil {
		t.Errorf("a symlink to a directory was walked into: %v", got)
	}
}
