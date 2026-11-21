package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pngish is an image to git: the NUL makes it refuse to diff the file as text, so the entry has no rows.
var pngish = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x00, 0x01, 0x02, 0x03}

// writePNG writes a picture whose tail tells it from the other ones this test plants.
func writePNG(t *testing.T, dir, name string, tail ...byte) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	content := append(append([]byte{}, pngish...), tail...)
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A ref comparison answers with its two trees: HEAD's commits are a third thing and never belong in the
// result. That a `?head=` request makes that comparison is the route's decision, proved by
// backend/api/checkpoint_diff_test.go:TestTaskDiffAtCheckpointExcludesWorkCommittedLater; what is proved
// here is the shape of the answer — a comparison of two named refs has no committed half and says so.
func TestGetTaskDiff_RefComparisonExcludesWorkCommittedAfterItsHead(t *testing.T) {
	repoDir, taskID, _, _, wm := setupTestRepo(t)
	base := strings.TrimSpace(runGitOutput(t, repoDir, "rev-parse", "HEAD"))

	if _, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", ""); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	wt, err := wm.WorktreePath(taskID)
	if err != nil {
		t.Fatalf("worktree path: %v", err)
	}

	writeFile(t, wt, "early.txt", "early\n")
	runGit(t, wt, "add", "-A")
	runGit(t, wt, "commit", "-m", "early work")
	at := strings.TrimSpace(runGitOutput(t, wt, "rev-parse", "HEAD"))

	// Work committed after that ref: HEAD holds it, the comparison does not.
	writeFile(t, wt, "late.txt", "late\n")
	runGit(t, wt, "add", "-A")
	runGit(t, wt, "commit", "-m", "later work")

	res, err := wm.GetTaskDiff(taskID, base, at)
	if err != nil {
		t.Fatalf("GetTaskDiff: %v", err)
	}
	if _, ok := res.Tracked["late.txt"]; ok {
		t.Errorf("the comparison listed a file committed after its head ref: %v", keysOf(res.Tracked))
	}
	early := res.Tracked["early.txt"]
	if early == nil {
		t.Fatalf("the comparison lost the file it compares: %v", keysOf(res.Tracked))
	}
	if len(early.CommittedRows) != 0 {
		t.Errorf("a ref comparison's rows are the comparison; it has no committed half: %d rows", len(early.CommittedRows))
	}
	if res.CommittedDiff != "" {
		t.Errorf("CommittedDiff describes the working-tree comparison, not this one: %q", res.CommittedDiff)
	}
	if !res.ComparesRefs {
		t.Error("a comparison of two named refs must say so; the stats frame's committed work is derived from it")
	}
	if !res.HasCommittedWork() {
		t.Error("a ref comparison is entirely committed work, so the stats frame must report it as such")
	}

	// The control: the working-tree comparison keeps the committed half of a file it also holds dirty.
	writeFile(t, wt, "early.txt", "early\nmore\n")
	working, err := wm.GetTaskDiff(taskID, base, "")
	if err != nil {
		t.Fatalf("GetTaskDiff (working tree): %v", err)
	}
	if working.CommittedDiff == "" {
		t.Error("the working-tree comparison lost its committed diff")
	}
	if f := working.Tracked["early.txt"]; f == nil || len(f.CommittedRows) == 0 {
		t.Errorf("the working-tree comparison lost the committed half of early.txt: %+v", f)
	}
}

// A file git will not diff as text has no rows, so its names are all the panel has to place it with.
func TestGetTaskDiff_CommittedHalfKeepsWhatOnlyTheCommittedSideKnows(t *testing.T) {
	repoDir, taskID, _, _, wm := setupTestRepo(t)
	writePNG(t, repoDir, "gone.png", 'g', 'g', 'g')
	// Most of one picture is kept across the move, or git reports an add and a delete instead of a rename.
	icon := []byte(strings.Repeat("o", 200))
	writePNG(t, repoDir, "old-icon.png", icon...)
	writeFile(t, repoDir, "notes.txt", "one\ntwo\nthree\nfour\nfive\n")
	runGit(t, repoDir, "add", "-A")
	runGit(t, repoDir, "commit", "-m", "images in the base")
	base := strings.TrimSpace(runGitOutput(t, repoDir, "rev-parse", "HEAD"))

	if _, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", ""); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	wt, err := wm.WorktreePath(taskID)
	if err != nil {
		t.Fatalf("worktree path: %v", err)
	}

	writePNG(t, wt, "docs/new.png", 'n', 'n', 'n')
	writePNG(t, wt, "dirty.png", 'd', 'd', 'd')
	if err := os.Remove(filepath.Join(wt, "gone.png")); err != nil {
		t.Fatal(err)
	}
	runGit(t, wt, "mv", "notes.txt", "notes-v2.txt")
	writeFile(t, wt, "notes-v2.txt", "one\ntwo\nTHREE\nfour\nfive\n")
	runGit(t, wt, "mv", "old-icon.png", "icon.png")
	writePNG(t, wt, "icon.png", append(append([]byte{}, icon...), 'i')...)
	runGit(t, wt, "add", "-A")
	runGit(t, wt, "commit", "-m", "the agent's work")
	// dirty.png is changed again after the commit, so its change lives in both halves.
	writePNG(t, wt, "dirty.png", 'W', 'W', 'W')

	res, err := wm.GetTaskDiff(taskID, base, "")
	if err != nil {
		t.Fatalf("GetTaskDiff: %v", err)
	}

	type want struct {
		path       string
		binary     bool
		isNew      bool
		isDeleted  bool
		origPath   string
		hasWorking bool
	}
	cases := []want{
		{path: "docs/new.png", binary: true, isNew: true},
		{path: "gone.png", binary: true, isDeleted: true},
		{path: "dirty.png", binary: true, hasWorking: true},
		{path: "icon.png", binary: true, origPath: "old-icon.png"},
		// A text file's rows already say this; the names still have to survive the merge.
		{path: "notes-v2.txt", origPath: "notes.txt"},
	}
	for _, c := range cases {
		f := res.Tracked[c.path]
		if f == nil {
			t.Fatalf("%s: missing from the diff: %v", c.path, keysOf(res.Tracked))
		}
		if f.IsBinary != c.binary {
			t.Errorf("%s: IsBinary = %v, want %v", c.path, f.IsBinary, c.binary)
		}
		if f.IsNew != c.isNew {
			t.Errorf("%s: IsNew = %v, want %v — one pane or two is read from it", c.path, f.IsNew, c.isNew)
		}
		if f.IsDeleted != c.isDeleted {
			t.Errorf("%s: IsDeleted = %v, want %v — no picture is offered for a file that is gone", c.path, f.IsDeleted, c.isDeleted)
		}
		if f.OrigPath != c.origPath {
			t.Errorf("%s: OrigPath = %q, want %q — the old side of a rename is read from it", c.path, f.OrigPath, c.origPath)
		}
		if !f.HasCommitted {
			t.Errorf("%s: HasCommitted = false, so the committed section cannot list it", c.path)
		}
		if f.HasWorking != c.hasWorking {
			t.Errorf("%s: HasWorking = %v, want %v — that is what keeps committed work out of Unstaged", c.path, f.HasWorking, c.hasWorking)
		}
	}
}

// A hash of rows cannot tell two pictures of the same size apart, so nobody is asked for the new bytes.
func TestComputeFileHash_SameSizeImageReplacementIsAChange(t *testing.T) {
	size := int64(len(pngish))
	first := &TrackedDiffFile{Path: "logo.png", IsBinary: true, FileSize: &size, BlobHash: "aaaaaaaa"}
	replacement := &TrackedDiffFile{Path: "logo.png", IsBinary: true, FileSize: &size, BlobHash: "bbbbbbbb"}

	if ComputeFileHash(first) == ComputeFileHash(replacement) {
		t.Error("two blobs of one size hash the same, so nobody is told to look at the new bytes")
	}
	// Unchanged bytes must hash the same, or every refresh re-reads the file.
	same := &TrackedDiffFile{Path: "logo.png", IsBinary: true, FileSize: &size, BlobHash: "aaaaaaaa"}
	if ComputeFileHash(first) != ComputeFileHash(same) {
		t.Error("an unchanged binary file changed hash, so every refresh re-reads it")
	}
}
