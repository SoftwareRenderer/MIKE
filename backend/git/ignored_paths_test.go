package git

import (
	"os"
	"path/filepath"
	"testing"
)

// ignoredPathsRepo builds a repository holding the shapes a listing hands IgnoredPaths.
func ignoredPathsRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	for _, d := range []string{"sub", "build", filepath.Join("node_modules", "pkg")} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@test.com")
	runGit(t, repo, "config", "user.name", "Test")

	writeFile(t, repo, ".gitignore", "*.log\n/build/\nnode_modules/\n!keep.log\nweird*\n")
	writeFile(t, repo, "sub/.gitignore", "secret.txt\n")
	writeFile(t, repo, "README.md", "# readme\n")
	runGit(t, repo, "add", ".gitignore", "sub/.gitignore", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	// The forced add makes tracked.log tracked even though *.log is ignored.
	writeFile(t, repo, "tracked.log", "tracked\n")
	runGit(t, repo, "add", "-f", "tracked.log")
	runGit(t, repo, "commit", "-m", "add tracked.log")

	writeFile(t, repo, "app.log", "x\n")
	writeFile(t, repo, "keep.log", "x\n")
	writeFile(t, repo, "build/out.js", "x\n")
	writeFile(t, repo, "node_modules/pkg/index.js", "x\n")
	writeFile(t, repo, "sub/secret.txt", "x\n")
	writeFile(t, repo, "sub/visible.txt", "x\n")
	writeFile(t, repo, "secret.txt", "x\n")
	writeFile(t, repo, "weird\nname", "x\n")
	return repo
}

// TestIgnoredPaths: a batch that reports a tracked file, or misses a nested .gitignore, dims the
// wrong explorer row.
func TestIgnoredPaths(t *testing.T) {
	repo := ignoredPathsRepo(t)

	got := IgnoredPaths(repo, []string{
		"app.log", "src/app.log", "keep.log", "tracked.log", ".gitignore", "README.md",
		"build", "build/out.js", "node_modules", "node_modules/pkg",
		"sub/secret.txt", "secret.txt", "sub/.gitignore", "weird\nname",
	})

	wantIgnored := []string{"app.log", "src/app.log", "build", "build/out.js", "node_modules",
		"node_modules/pkg", "sub/secret.txt", "weird\nname"}
	for _, p := range wantIgnored {
		if !got[p] {
			t.Errorf("%q: not reported as ignored", p)
		}
	}
	// A newline inside one name must not split it into two judged paths.
	if len(got) != len(wantIgnored) {
		t.Errorf("reported %d ignored paths, want %d: %v", len(got), len(wantIgnored), got)
	}
	for _, p := range []string{"keep.log", "tracked.log", ".gitignore", "README.md", "secret.txt", "sub/.gitignore"} {
		if got[p] {
			t.Errorf("%q: reported as ignored", p)
		}
	}
}

// TestIgnoredPathsFromAListedSubdirectory: judged from `sub`, its own .gitignore answers for it and
// the top-level rules still reach down.
func TestIgnoredPathsFromAListedSubdirectory(t *testing.T) {
	repo := ignoredPathsRepo(t)

	got := IgnoredPaths(filepath.Join(repo, "sub"), []string{"secret.txt", "visible.txt", "debug.log"})
	if !got["secret.txt"] {
		t.Error("sub/.gitignore's own rule did not answer for sub")
	}
	if !got["debug.log"] {
		t.Error("the top-level *.log rule did not reach into sub")
	}
	if got["visible.txt"] {
		t.Error("visible.txt reported as ignored")
	}
}

// TestIgnoredPathsWithoutARepository: a plain folder answers unmarked, not dimmed or failed.
func TestIgnoredPathsWithoutARepository(t *testing.T) {
	plain := t.TempDir()
	writeFile(t, plain, "notes.log", "x\n")

	if got := IgnoredPaths(plain, []string{"notes.log"}); len(got) != 0 {
		t.Errorf("a folder that is not a repository reported %v", got)
	}
	if got := IgnoredPaths("", []string{"notes.log"}); len(got) != 0 {
		t.Errorf("an empty root reported %v", got)
	}
	if got := IgnoredPaths(plain, nil); len(got) != 0 {
		t.Errorf("an empty batch reported %v", got)
	}
}
