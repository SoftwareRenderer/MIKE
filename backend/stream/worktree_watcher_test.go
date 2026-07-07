package stream

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveGitIndexDir_Worktree(t *testing.T) {
	dir := t.TempDir()

	// Simulate a worktree .git file
	gitFile := filepath.Join(dir, ".git")
	gitdirPath := "/home/user/projects/myrepo/.git/worktrees/abc-123-def"
	err := os.WriteFile(gitFile, []byte("gitdir: "+gitdirPath+"\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	result := resolveGitIndexDir(dir)
	if result != gitdirPath {
		t.Errorf("resolveGitIndexDir() = %q, want %q", result, gitdirPath)
	}
}

func TestResolveGitIndexDir_RegularRepo(t *testing.T) {
	dir := t.TempDir()

	// Simulate a regular repo where .git is a directory
	gitDir := filepath.Join(dir, ".git")
	err := os.Mkdir(gitDir, 0755)
	if err != nil {
		t.Fatal(err)
	}

	result := resolveGitIndexDir(dir)
	if result != "" {
		t.Errorf("resolveGitIndexDir() = %q, want empty string (regular repo)", result)
	}
}

func TestResolveGitIndexDir_NoGit(t *testing.T) {
	dir := t.TempDir()

	// No .git at all
	result := resolveGitIndexDir(dir)
	if result != "" {
		t.Errorf("resolveGitIndexDir() = %q, want empty string (no .git)", result)
	}
}

func TestResolveGitIndexDir_InvalidContent(t *testing.T) {
	dir := t.TempDir()

	// .git file with no gitdir: line
	gitFile := filepath.Join(dir, ".git")
	err := os.WriteFile(gitFile, []byte("some random content\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	result := resolveGitIndexDir(dir)
	if result != "" {
		t.Errorf("resolveGitIndexDir() = %q, want empty string (no gitdir line)", result)
	}
}

func TestResolveGitIndexDir_MultiLine(t *testing.T) {
	dir := t.TempDir()

	// .git file with extra lines before/after gitdir
	gitFile := filepath.Join(dir, ".git")
	gitdirPath := "/some/path/.git/worktrees/task-xyz"
	content := "# comment\ngitdir: " + gitdirPath + "\n# another comment\n"
	err := os.WriteFile(gitFile, []byte(content), 0644)
	if err != nil {
		t.Fatal(err)
	}

	result := resolveGitIndexDir(dir)
	if result != gitdirPath {
		t.Errorf("resolveGitIndexDir() = %q, want %q", result, gitdirPath)
	}
}
