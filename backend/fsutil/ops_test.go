package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveTreeDeletesDirectory(t *testing.T) {
	dir := t.TempDir()
	tree := filepath.Join(dir, "pkg")
	writeTestFile(t, filepath.Join(tree, "a.go"), "package a")
	writeTestFile(t, filepath.Join(tree, "deep", "b.go"), "package b")

	if err := RemoveTree(tree, []string{dir}); err != nil {
		t.Fatalf("RemoveTree: %v", err)
	}
	if _, err := os.Lstat(tree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tree still there: %v", err)
	}
}

func TestRemoveTreeRefusesTreeRoots(t *testing.T) {
	dir := t.TempDir()
	worktree := filepath.Join(dir, "wt", "task-1")
	writeTestFile(t, filepath.Join(worktree, "main.go"), "package main")

	protected := []string{filepath.Join(dir, "wt")}
	for _, path := range []string{filepath.Join(dir, "wt"), filepath.Join(dir, "wt", ".."), string(filepath.Separator)} {
		if err := RemoveTree(path, protected); !errors.Is(err, ErrProtectedPath) {
			t.Errorf("RemoveTree(%q) = %v, want ErrProtectedPath", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(worktree, "main.go")); err != nil {
		t.Fatalf("a refused delete must touch nothing: %v", err)
	}
}

func TestRemoveTreeRefusesGitPaths(t *testing.T) {
	dir := t.TempDir()
	// A worktree's .git is a file rather than a folder; both are refused.
	writeTestFile(t, filepath.Join(dir, "wt", ".git"), "gitdir: /somewhere/worktrees/wt\n")
	writeTestFile(t, filepath.Join(dir, "repo", ".git", "config"), "[core]\n")

	for _, path := range []string{
		filepath.Join(dir, "wt", ".git"),
		filepath.Join(dir, "repo", ".git"),
		filepath.Join(dir, "repo", ".git", "config"),
	} {
		if err := RemoveTree(path, nil); !errors.Is(err, ErrGitPath) {
			t.Errorf("RemoveTree(%q) = %v, want ErrGitPath", path, err)
		}
	}
}

func TestRemoveTreeUnlinksSymlinkWithoutTouchingTarget(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "keepme")
	writeTestFile(t, outside, "stays")
	link := filepath.Join(dir, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	if err := RemoveTree(link, []string{dir}); err != nil {
		t.Fatalf("RemoveTree: %v", err)
	}
	if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the link should be gone")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("the target of a link must survive its removal: %v", err)
	}
}

func TestUniqueName(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "report.txt"), "one")
	writeTestFile(t, filepath.Join(dir, ".gitignore"), "x")

	t.Run("free name is taken as asked", func(t *testing.T) {
		name, renamed, err := UniqueName(dir, "fresh.md")
		if err != nil || name != "fresh.md" || renamed {
			t.Fatalf("UniqueName = %q, %v, %v", name, renamed, err)
		}
	})

	t.Run("collision keeps the extension", func(t *testing.T) {
		name, renamed, err := UniqueName(dir, "report.txt")
		if err != nil || name != "report copy.txt" || !renamed {
			t.Fatalf("UniqueName = %q, %v, %v", name, renamed, err)
		}
	})

	t.Run("second collision counts", func(t *testing.T) {
		writeTestFile(t, filepath.Join(dir, "report copy.txt"), "two")
		name, renamed, err := UniqueName(dir, "report.txt")
		if err != nil || name != "report copy2.txt" || !renamed {
			t.Fatalf("UniqueName = %q, %v, %v", name, renamed, err)
		}
	})

	t.Run("a leading dot is a name not an extension", func(t *testing.T) {
		name, renamed, err := UniqueName(dir, ".gitignore")
		if err != nil || name != ".gitignore copy" || !renamed {
			t.Fatalf("UniqueName = %q, %v, %v", name, renamed, err)
		}
	})

	t.Run("a dangling symlink still occupies the name", func(t *testing.T) {
		if err := os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "ghost")); err != nil {
			t.Fatal(err)
		}
		name, renamed, err := UniqueName(dir, "ghost")
		if err != nil || name != "ghost copy" || !renamed {
			t.Fatalf("UniqueName = %q, %v, %v", name, renamed, err)
		}
	})

	t.Run("a name with a separator in it is refused", func(t *testing.T) {
		for _, bad := range []string{"", ".", "..", "a/b"} {
			if _, _, err := UniqueName(dir, bad); err == nil {
				t.Errorf("UniqueName(%q) should refuse", bad)
			}
		}
	})
}

func TestHasGitSegment(t *testing.T) {
	for path, want := range map[string]bool{
		"/p/.git":                true,
		"/p/.git/config":         true,
		"/p/wt/sub/.git/objects": true,
		"/p/git/git.txt":         false,
		"/p/mygit":               false,
	} {
		if got := HasGitSegment(path); got != want {
			t.Errorf("HasGitSegment(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestGuardCopyTarget(t *testing.T) {
	if err := guardCopyTarget("/p/src", "/p/src/deep/x"); !errors.Is(err, ErrCopyIntoItself) {
		t.Errorf("destination inside the source: got %v", err)
	}
	if err := guardCopyTarget("/p/src", "/p/other"); err != nil {
		t.Errorf("sibling destination: %v", err)
	}
	if err := guardCopyTarget("/p/src", "/p/src"); err != nil {
		t.Errorf("paste in place is not this refusal: %v", err)
	}
}

func TestCopyTreeCopiesFilesDirsAndLinks(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeTestFile(t, filepath.Join(src, "a.txt"), "alpha")
	writeTestFile(t, filepath.Join(src, "sub", "b.txt"), "beta")
	if err := os.Symlink("a.txt", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "dst")
	if err := CopyTree(src, dst); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dst, "sub", "b.txt")); err != nil || string(got) != "beta" {
		t.Fatalf("nested file: %q, %v", got, err)
	}
	fi, err := os.Lstat(filepath.Join(dst, "link"))
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link copied as %v (%v), want a symlink", fi, err)
	}
	copied, err := os.Stat(filepath.Join(dst, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if copied.Mode().Perm()&0o111 != 0 {
		t.Errorf("copy added the executable bit: %v", copied.Mode().Perm())
	}
}

func TestCopyTreeSkipsNestedGitDir(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeTestFile(t, filepath.Join(src, "main.go"), "package main")
	writeTestFile(t, filepath.Join(src, "vendored", ".git", "config"), "[core]\n")

	dst := filepath.Join(dir, "dst")
	if err := CopyTree(src, dst); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "main.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "vendored", ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a nested .git was copied: %v", err)
	}
}

func TestCopyTreeRefusesOversizedFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "big.bin")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(CopyMaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	dst := filepath.Join(dir, "dst.bin")
	if err := CopyTree(src, dst); !errors.Is(err, ErrCopyTooLarge) {
		t.Fatalf("CopyTree = %v, want ErrCopyTooLarge", err)
	}
	// The size check runs before anything is written.
	if _, err := os.Lstat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused copy must leave no destination")
	}
}

func TestCopyTreeRefusesTooDeepTree(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "src")
	tail := deep
	for i := 0; i <= CopyMaxDepth; i++ {
		tail = filepath.Join(tail, "level")
	}
	writeTestFile(t, filepath.Join(tail, "leaf.txt"), "leaf")

	if err := CopyTree(deep, filepath.Join(dir, "dst")); !errors.Is(err, ErrCopyTooLarge) {
		t.Fatalf("CopyTree = %v, want ErrCopyTooLarge", err)
	}
}

func TestCopyTreeRefusesTooManyEntries(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= CopyMaxEntries; i++ {
		f, err := os.Create(filepath.Join(src, "f"+strconv.Itoa(i)))
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}

	if err := CopyTree(src, filepath.Join(dir, "dst")); !errors.Is(err, ErrCopyTooLarge) {
		t.Fatalf("CopyTree = %v, want ErrCopyTooLarge", err)
	}
}

func TestCopyTreeRefusesDestinationInsideSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeTestFile(t, filepath.Join(src, "a.txt"), "a")

	if err := CopyTree(src, filepath.Join(src, "inner", "dst")); !errors.Is(err, ErrCopyIntoItself) {
		t.Fatalf("CopyTree = %v, want ErrCopyIntoItself", err)
	}
}

func TestMovePathAcrossOneFilesystem(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	writeTestFile(t, src, "moved")
	dst := filepath.Join(dir, "dst.txt")

	if err := MovePath(src, dst); err != nil {
		t.Fatalf("MovePath: %v", err)
	}
	if _, err := os.Lstat(src); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the source is still there")
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != "moved" {
		t.Fatalf("moved content: %q, %v", got, err)
	}
}

func TestMovePathRefusesDirectoryIntoItself(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	writeTestFile(t, filepath.Join(src, "a.txt"), "a")

	if err := MovePath(src, filepath.Join(src, "under")); !errors.Is(err, ErrCopyIntoItself) {
		t.Fatalf("MovePath = %v, want ErrCopyIntoItself", err)
	}
	if _, err := os.Stat(filepath.Join(src, "a.txt")); err != nil {
		t.Fatalf("a refused move must touch nothing: %v", err)
	}
}

// TestCopyEntryRefusesANameTheWalkNoLongerRecognises: the paste copies from the paths its walk measured,
// and the tree belongs to the task, so a name made to stand for a file outside the project puts that
// file's bytes inside it.
func TestCopyEntryRefusesANameTheWalkNoLongerRecognises(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "wt", "notes.txt")
	writeTestFile(t, name, "notes")
	secret := filepath.Join(dir, "host-secret.txt")
	writeTestFile(t, secret, "host secret\n")
	dstDir := filepath.Join(dir, "paste-here")
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatal(err)
	}

	checked, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, name); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dstDir, "notes.txt")
	if err := copyEntry(name, dst, checked); !errors.Is(err, ErrSwapped) {
		t.Fatalf("copying a swapped name = %v, want %v", err, ErrSwapped)
	}
	if got, err := os.ReadFile(dst); err == nil {
		t.Errorf("the paste wrote %q into the destination", got)
	}
}
