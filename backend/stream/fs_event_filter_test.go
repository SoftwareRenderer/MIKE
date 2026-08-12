package stream

import (
	"os"
	"path/filepath"
	"testing"
)

// TestShouldSkipFsEvent verifies fs_event filtering: internal files (.git,
// flight recorder) and paths ignored by .gitignore are skipped, everything
// else is broadcast.
func TestShouldSkipFsEvent(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(repo, 0o755)
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@test.com")
	runGit(t, repo, "config", "user.name", "Test")

	writeFile(t, repo, ".gitignore", "*.log\nnode_modules/\n")
	writeFile(t, repo, "README.md", "# readme\n")
	runGit(t, repo, "add", ".gitignore", "README.md")
	runGit(t, repo, "commit", "-m", "init")

	tests := []struct {
		name     string
		repoRoot string
		path     string
		expected bool // true = skip broadcast
	}{
		{"normal file", repo, "main.go", false},
		{"nested normal file", repo, "src/main.go", false},
		{"gitignored file", repo, "app.log", true},
		{"gitignored dir file", repo, "node_modules/pkg/index.js", true},
		{"internal .git file", repo, ".git/index", true},
		{"internal kanban.trace", repo, "kanban.trace", true},
		{"internal nested trace", repo, "data/kanban.trace", true},
		{"non-repo root", dir, "main.go", false}, // not a git repo → not skipped
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldSkipFsEvent(tc.repoRoot, tc.path)
			if got != tc.expected {
				t.Errorf("shouldSkipFsEvent(%q, %q) = %v, want %v", tc.repoRoot, tc.path, got, tc.expected)
			}
		})
	}
}
