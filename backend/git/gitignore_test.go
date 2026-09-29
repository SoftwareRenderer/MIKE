package git

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIsGitIgnored verifies .gitignore-based path filtering used for fs_event
// broadcasts. Tracked files matching an ignore rule are NOT ignored (matching
// git check-ignore default behavior), so their events still get broadcast.
func TestIsGitIgnored(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(repo, 0o755)
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@test.com")
	runGit(t, repo, "config", "user.name", "Test")

	writeFile(t, repo, ".gitignore", "*.log\n/build/\nnode_modules/\n!keep.log\n")
	writeFile(t, repo, "README.md", "# readme\n")
	runGit(t, repo, "add", ".gitignore", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	// A file matching an ignore rule that is tracked must NOT be ignored.
	writeFile(t, repo, "tracked.log", "tracked\n")
	runGit(t, repo, "add", "-f", "tracked.log")
	runGit(t, repo, "commit", "-m", "add tracked.log")

	tests := []struct {
		name     string
		relPath  string
		expected bool
	}{
		{"ignored extension", "app.log", true},
		{"nested ignored extension", "src/app.log", true},
		{"ignored build dir", "build/out.js", true},
		{"ignored dir nested", "a/b/node_modules/pkg/index.js", true},
		{"normal file", "main.go", false},
		{"nested normal file", "src/main.go", false},
		{"tracked file matching ignore rule", "tracked.log", false},
		{"negation re-includes", "keep.log", false},
		{"gitignore itself", ".gitignore", false},
		{"empty path", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsGitIgnored(repo, tc.relPath)
			if got != tc.expected {
				t.Errorf("IsGitIgnored(%q) = %v, want %v", tc.relPath, got, tc.expected)
			}
		})
	}

	// Absolute path inside the repo is checked relative to the repo root.
	absIgnored := filepath.Join(repo, "app.log")
	if !IsGitIgnored(repo, absIgnored) {
		t.Error("expected absolute ignored path to be ignored")
	}

	// Edge cases must not panic and must return false (broadcast anyway).
	if IsGitIgnored("", "main.go") {
		t.Error("expected false for empty repo root")
	}
	if IsGitIgnored(repo, "") {
		t.Error("expected false for empty path")
	}
	if IsGitIgnored(filepath.Join(dir, "missing"), "main.go") {
		t.Error("expected false for missing repo root")
	}
	outside := filepath.Join(dir, "outside.log")
	if IsGitIgnored(repo, outside) {
		t.Error("expected false for absolute path outside repo")
	}
}
