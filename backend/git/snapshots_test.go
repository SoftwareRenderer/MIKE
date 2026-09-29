package git

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// gitIn runs git straight against repoDir, so assertions read what git recorded rather than what MIKE's
// own helpers report.
func gitIn(t *testing.T, repoDir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repoDir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

// gitInFails runs git and reports whether it failed (no fatal).
func gitInFails(repoDir string, args ...string) bool {
	return exec.Command("git", append([]string{"-C", repoDir}, args...)...).Run() != nil
}

// refPinned reports whether ref exists — asked of git, not of MIKE's own helpers.
func refPinned(repoDir, ref string) bool {
	return !gitInFails(repoDir, "show-ref", "--verify", "--quiet", ref)
}

// makeSnapshot dirties the worktree and returns a `git stash create` snapshot of it — what MIKE stores
// as a checkpoint.
func makeSnapshot(t *testing.T, repoDir, name, content string) string {
	t.Helper()
	writeFile(t, repoDir, name, content)
	gitIn(t, repoDir, "add", "-A")
	return gitIn(t, repoDir, "stash", "create")
}

func TestPinSnapshotOutlivesStashClearAndGC(t *testing.T) {
	t.Parallel()
	repoDir, _, taskID, _, _, wm := setupStashTest(t)

	pinned := makeSnapshot(t, repoDir, "pinned.txt", "keep me\n")
	unpinned := makeSnapshot(t, repoDir, "unpinned.txt", "lose me\n")
	if pinned == "" || unpinned == "" || pinned == unpinned {
		t.Fatalf("bad fixtures: pinned=%q unpinned=%q", pinned, unpinned)
	}

	ref := SnapshotRef(taskID, "asst-1")
	if err := wm.PinSnapshot(repoDir, ref, pinned); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if !refPinned(repoDir, ref) {
		t.Fatal("ref should exist after pinning")
	}
	// The whole point: a pinned snapshot never enters the user's stash list.
	if out := gitIn(t, repoDir, "stash", "list"); out != "" {
		t.Fatalf("pinning must not create a stash entry, got: %q", out)
	}

	gitIn(t, repoDir, "stash", "store", "-m", "mike-checkpoint-"+taskID, unpinned)
	gitIn(t, repoDir, "stash", "drop", "stash@{0}")

	// Normal git activity: reflogs expire, gc prunes.
	gitIn(t, repoDir, "reflog", "expire", "--expire=now", "--all")
	gitIn(t, repoDir, "gc", "--prune=now", "--quiet")

	if gitInFails(repoDir, "cat-file", "-e", pinned+"^{commit}") {
		t.Fatalf("a pinned snapshot was garbage-collected (%s)", pinned)
	}
	// The control must really be gone, or nothing about the hazard has been shown. Very fresh loose
	// objects can survive a prune in some gits, which is not a reason to fail.
	if !gitInFails(repoDir, "cat-file", "-e", unpinned+"^{commit}") {
		t.Skip("this git kept the unreachable object; the hazard is still real, just not reproducible here")
	}

	// Unpinning is what makes the snapshot collectable again.
	if err := wm.UnpinSnapshot(repoDir, ref); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	if refPinned(repoDir, ref) {
		t.Fatal("ref should be gone after unpinning")
	}
	if err := wm.UnpinSnapshot(repoDir, ref); err != nil {
		t.Fatalf("unpinning a missing ref must be a no-op, got %v", err)
	}
}

func TestSnapshotRefsRejectAnythingElse(t *testing.T) {
	t.Parallel()
	repoDir, _, taskID, _, _, wm := setupStashTest(t)
	hash := makeSnapshot(t, repoDir, "a.txt", "content\n")

	bad := []string{
		"",
		"refs/heads/main",                       // the user's refs are never ours to touch
		"refs/stash",                            // not ours either
		"refs/mike/snap",                        // the namespace itself
		"refs/mike/snap/" + taskID,              // no owner
		"refs/mike/snap/" + taskID + "/",        // empty owner
		"refs/mike/snap//owner",                 // empty task
		"refs/mike/snap/../owner",               // ".." as the task component
		"refs/mike/snap/x/../../heads/main",     // escape by path
		"refs/mike/snap/x/y/owner",              // one namespace level too deep
		"refs/mike/snap/x/owner.lock",           // git reserves .lock
		"refs/mike/snap/x/own er",               // spaces
		"refs/mike/snap/x/own~er",               // rev syntax
		"refs/mike/snap/x/own@{0}er",            // reflog syntax
		"refs/mike/snap/x/\towner",              // control character
		"refs/mike/snap/x/-owner",               // leading dash: an argv option waiting to happen
		"refs/mike/snap/" + taskID + "/owner\n", // newline
	}
	for _, ref := range bad {
		if err := wm.PinSnapshot(repoDir, ref, hash); !errors.Is(err, ErrInvalidSnapshotRef) {
			t.Errorf("PinSnapshot(%q) = %v, want ErrInvalidSnapshotRef", ref, err)
		}
		if err := wm.UnpinSnapshot(repoDir, ref); !errors.Is(err, ErrInvalidSnapshotRef) {
			t.Errorf("UnpinSnapshot(%q) = %v, want ErrInvalidSnapshotRef", ref, err)
		}
		if refPinned(repoDir, ref) {
			t.Errorf("ref %q was created despite being malformed", ref)
		}
	}
	if _, err := wm.DeleteSnapshotRefsUnder(repoDir, "refs/heads"); !errors.Is(err, ErrInvalidSnapshotRef) {
		t.Errorf("DeleteSnapshotRefsUnder(refs/heads) = %v, want ErrInvalidSnapshotRef", err)
	}
	if _, err := wm.DeleteSnapshotRefsUnder(repoDir, "refs"); !errors.Is(err, ErrInvalidSnapshotRef) {
		t.Errorf("DeleteSnapshotRefsUnder(refs) = %v, want ErrInvalidSnapshotRef", err)
	}

	// A hash git cannot resolve is not pinnable: storing such a ref would only defer the failure.
	if err := wm.PinSnapshot(repoDir, SnapshotRef(taskID, "nope"), strings.Repeat("0", 40)); err == nil {
		t.Error("pinning a missing object must fail")
	}
	if err := wm.PinSnapshot(repoDir, SnapshotRef(taskID, "nope"), ""); err == nil {
		t.Error("pinning an empty hash must fail")
	}
}

func TestDeleteSnapshotRefsUnderTakesOnlyThatTask(t *testing.T) {
	t.Parallel()
	repoDir, _, taskID, _, _, wm := setupStashTest(t)
	other := "other-task-id"
	hash := makeSnapshot(t, repoDir, "a.txt", "content\n")

	refs := []string{
		SnapshotRef(taskID, "asst-1"),
		SnapshotRef(taskID, ThreadSnapshotOwner("thread-1")),
		SnapshotRef(other, "asst-9"),
	}
	for _, ref := range refs {
		if err := wm.PinSnapshot(repoDir, ref, hash); err != nil {
			t.Fatalf("pin %s: %v", ref, err)
		}
	}

	removed, err := wm.DeleteSnapshotRefsUnder(repoDir, SnapshotRefPrefix+"/"+taskID+"/")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if removed != 2 {
		t.Fatalf("expected 2 refs removed, got %d", removed)
	}
	for _, ref := range refs[:2] {
		if refPinned(repoDir, ref) {
			t.Errorf("%s should be gone", ref)
		}
	}
	if !refPinned(repoDir, refs[2]) {
		t.Error("another task's snapshot must survive")
	}
	if !refPinned(repoDir, SnapshotRef(other, "asst-9")) {
		t.Error("another task's snapshot must survive")
	}
	// The user's own branch is untouched by all of this.
	if !refPinned(repoDir, "refs/heads/"+gitIn(t, repoDir, "rev-parse", "--abbrev-ref", "HEAD")) {
		t.Error("the current branch ref must still exist")
	}
	// Deleting the namespace root is allowed but takes everything inside it.
	if n, err := wm.DeleteSnapshotRefsUnder(repoDir, SnapshotRefPrefix); err != nil || n != 1 {
		t.Fatalf("deleting the namespace should take the last snapshot, got n=%d err=%v", n, err)
	}
}
