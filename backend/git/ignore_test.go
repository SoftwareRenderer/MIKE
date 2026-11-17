package git

// What git excludes is decided by git, in two shapes: IsGitIgnored judges one path for an fs_event
// broadcast (backend/stream), IgnoredPaths judges a whole directory listing in one process
// (backend/server/handler_fs). They must answer the same verdict for the same path, and a tracked file
// matching an ignore rule is never ignored — its events and its explorer row must survive.

import (
	"os"
	"path/filepath"
	"testing"
)

// IgnoredDirs hands out what the watcher treats as the ignorable set. The invariant is not the
// contents, it is that a caller cannot change them for everybody: a returned map must be a copy, or
// one caller's delete would have every other caller — the worktree watcher included — walk what the
// product agrees to skip.
func TestIgnoredDirsReturnsACopy(t *testing.T) {
	first := IgnoredDirs()
	if len(first) == 0 {
		t.Fatal("the ignore set is empty; this test needs it populated")
	}

	// Mutate the returned map in both directions.
	delete(first, "node_modules")
	first["a-test-only-addition"] = struct{}{}

	second := IgnoredDirs()
	if _, ok := second["node_modules"]; !ok {
		t.Error("deleting from the returned map removed node_modules from the package's set")
	}
	if _, ok := second["a-test-only-addition"]; ok {
		t.Error("adding to the returned map added an entry to the package's set for every caller")
	}
}

// The match is a whole path component, and the set the predicate consults is the set handed out.
func TestShouldIgnorePathSkipsWholeKnownComponents(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"src/node_modules/pkg/index.js", true},
		{"packages/app/.next/cache/build", true},
		{"lib/__pycache__/module.cpython-311.pyc", true},
		{"a/b/c/vendor/bin/tool", true},
		{"src/dist/output.js", true},
		// Partial component names are not the known set.
		{"my_node_modules/file.txt", false},
		{"node_modules_backup/file.txt", false},
		{"distro/file.txt", false},
		{"building/file.txt", false},
		{"coverage_report/file.txt", false},
		{"a/my_node_modules/b.js", false},
		// Ordinary paths.
		{"src/index.ts", false},
		{"lib/utils/helpers.ts", false},
		{"README.md", false},
		{".github/workflows/ci.yml", false},
	} {
		if got := ShouldIgnorePath(tc.path); got != tc.want {
			t.Errorf("ShouldIgnorePath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}

	// Whatever IgnoredDirs hands a caller, the predicate still skips it.
	for name := range IgnoredDirs() {
		if !ShouldIgnorePath("a/" + name + "/b.js") {
			t.Errorf("IgnoredDirs lists %q but ShouldIgnorePath does not skip it", name)
		}
	}
}

// ignoredPathsRepo builds a repository holding the shapes a listing hands IgnoredPaths and an fs_event
// hands IsGitIgnored: ignore rules by extension and by directory, a negation, a nested .gitignore, a
// tracked file matching an ignore rule, and a name holding a newline.
func ignoredPathsRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	for _, d := range []string{"sub", "build", filepath.Join("node_modules", "pkg"), filepath.Join("a", "b", "node_modules", "pkg"), "src"} {
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
	writeFile(t, repo, "src/app.log", "x\n")
	writeFile(t, repo, "keep.log", "x\n")
	writeFile(t, repo, "main.go", "package main\n")
	writeFile(t, repo, "src/main.go", "package src\n")
	writeFile(t, repo, "build/out.js", "x\n")
	writeFile(t, repo, "node_modules/pkg/index.js", "x\n")
	writeFile(t, repo, filepath.Join("a", "b", "node_modules", "pkg", "index.js"), "x\n")
	writeFile(t, repo, "sub/secret.txt", "x\n")
	writeFile(t, repo, "sub/visible.txt", "x\n")
	writeFile(t, repo, "secret.txt", "x\n")
	writeFile(t, repo, "weird\nname", "x\n")
	return repo
}

// The two entry points judge the same paths, so they must not disagree: one dims a row, the other
// drops an event, and a tracked file or a negated rule that reads as ignored dims the wrong thing.
func TestGitIgnoredAnswersTheSameVerdictInBothShapes(t *testing.T) {
	repo := ignoredPathsRepo(t)
	paths := []string{
		"app.log", "src/app.log", "build", "build/out.js", "node_modules", "node_modules/pkg",
		"node_modules/pkg/index.js", filepath.Join("a", "b", "node_modules", "pkg", "index.js"),
		"sub/secret.txt", "weird\nname",
		"keep.log", "tracked.log", ".gitignore", "README.md", "main.go", "src/main.go",
		"secret.txt", "sub/.gitignore", "sub/visible.txt",
	}
	wantIgnored := map[string]bool{
		"app.log": true, "src/app.log": true, "build": true, "build/out.js": true,
		"node_modules": true, "node_modules/pkg": true, "node_modules/pkg/index.js": true,
		filepath.Join("a", "b", "node_modules", "pkg", "index.js"): true,
		"sub/secret.txt": true, "weird\nname": true,
	}

	batch := IgnoredPaths(repo, paths)
	for _, p := range paths {
		want := wantIgnored[p]
		if got := IsGitIgnored(repo, p); got != want {
			t.Errorf("IsGitIgnored(%q) = %v, want %v", p, got, want)
		}
		if got := batch[p]; got != want {
			t.Errorf("IgnoredPaths reported %q = %v, want %v", p, got, want)
		}
	}
	// A newline inside one name must not split it into two judged paths.
	if len(batch) != len(wantIgnored) {
		t.Errorf("reported %d ignored paths, want %d: %v", len(batch), len(wantIgnored), batch)
	}
}

// IsGitIgnored is a judgement about one path inside one repository: anything that is not that answers
// "not ignored", so the event is broadcast rather than silently dropped.
func TestIsGitIgnoredAnswersFalseForAnythingButAPathInsideARepository(t *testing.T) {
	dir := t.TempDir()
	repo := ignoredPathsRepo(t)

	if !IsGitIgnored(repo, filepath.Join(repo, "app.log")) {
		t.Error("an absolute path inside the repo was not judged relative to it")
	}
	if IsGitIgnored("", "main.go") {
		t.Error("expected false for empty repo root")
	}
	if IsGitIgnored(repo, "") {
		t.Error("expected false for empty path")
	}
	if IsGitIgnored(filepath.Join(dir, "missing"), "main.go") {
		t.Error("expected false for a missing repo root")
	}
	if IsGitIgnored(repo, filepath.Join(dir, "outside.log")) {
		t.Error("expected false for an absolute path outside the repo")
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
