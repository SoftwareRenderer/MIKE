package exec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsMergeInProgress_ReturnsTrueWhenMergeHeadExists verifies that
// isMergeInProgress returns true when .git/MERGE_HEAD is present.
func TestIsMergeInProgress_ReturnsTrueWhenMergeHeadExists(t *testing.T) {
	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test")
	os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("initial\n"), 0o644)
	runGit(t, tmpDir, "add", "file.txt")
	runGit(t, tmpDir, "commit", "-m", "Initial")

	// Create a branch with a conflicting change
	runGit(t, tmpDir, "checkout", "-b", "feature")
	os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("feature change\n"), 0o644)
	runGit(t, tmpDir, "add", "file.txt")
	runGit(t, tmpDir, "commit", "-m", "Feature change")

	// Switch back to main and make a conflicting change
	runGit(t, tmpDir, "checkout", "master")
	os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("master change\n"), 0o644)
	runGit(t, tmpDir, "add", "file.txt")
	runGit(t, tmpDir, "commit", "-m", "Master change")

	// Start a merge (will conflict)
	cmd := exec.Command("git", "merge", "feature")
	cmd.Dir = tmpDir
	_ = cmd.Run() // expected to fail with conflict

	// MERGE_HEAD should now exist
	if !isMergeInProgress(tmpDir) {
		t.Fatal("expected isMergeInProgress to return true during merge conflict")
	}

	// Clean up
	runGit(t, tmpDir, "merge", "--abort")
}

// TestIsMergeInProgress_ReturnsFalseWhenNoMerge verifies that
// isMergeInProgress returns false when no merge is in progress.
func TestIsMergeInProgress_ReturnsFalseWhenNoMerge(t *testing.T) {
	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test")
	os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("initial\n"), 0o644)
	runGit(t, tmpDir, "add", "file.txt")
	runGit(t, tmpDir, "commit", "-m", "Initial")

	if isMergeInProgress(tmpDir) {
		t.Fatal("expected isMergeInProgress to return false when no merge is in progress")
	}
}

// TestGetConflictedFilesInDir_DetectsUUConflicts verifies that the primary
// porcelain scan detects standard UU (both modified) conflicts.
func TestGetConflictedFilesInDir_DetectsUUConflicts(t *testing.T) {
	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test")

	// Create initial file
	os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("line 1\nline 2\nline 3\n"), 0o644)
	runGit(t, tmpDir, "add", "file.txt")
	runGit(t, tmpDir, "commit", "-m", "Initial")

	// Create conflicting branch
	runGit(t, tmpDir, "checkout", "-b", "feature")
	os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("line 1\nfeature change\nline 3\n"), 0o644)
	runGit(t, tmpDir, "add", "file.txt")
	runGit(t, tmpDir, "commit", "-m", "Feature")

	// Conflicting change on master
	runGit(t, tmpDir, "checkout", "master")
	os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("line 1\nmaster change\nline 3\n"), 0o644)
	runGit(t, tmpDir, "add", "file.txt")
	runGit(t, tmpDir, "commit", "-m", "Master")

	// Start merge (will conflict)
	cmd := exec.Command("git", "merge", "feature")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	files := getConflictedFilesInDir(tmpDir)
	if len(files) != 1 || files[0] != "file.txt" {
		t.Fatalf("expected [file.txt], got %v", files)
	}

	runGit(t, tmpDir, "merge", "--abort")
}

// TestGetConflictedFilesInDir_DetectsMultipleConflicts verifies that
// multiple conflicted files are all detected.
func TestGetConflictedFilesInDir_DetectsMultipleConflicts(t *testing.T) {
	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test")

	// Create two initial files
	os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("a original\n"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "b.txt"), []byte("b original\n"), 0o644)
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "Initial")

	// Create conflicting branch
	runGit(t, tmpDir, "checkout", "-b", "feature")
	os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("a feature\n"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "b.txt"), []byte("b feature\n"), 0o644)
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "Feature")

	// Conflicting changes on master
	runGit(t, tmpDir, "checkout", "master")
	os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("a master\n"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "b.txt"), []byte("b master\n"), 0o644)
	runGit(t, tmpDir, "add", ".")
	runGit(t, tmpDir, "commit", "-m", "Master")

	// Start merge
	cmd := exec.Command("git", "merge", "feature")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	files := getConflictedFilesInDir(tmpDir)
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(files), files)
	}
	// Files may be in any order
	fileSet := make(map[string]bool)
	for _, f := range files {
		fileSet[f] = true
	}
	if !fileSet["a.txt"] || !fileSet["b.txt"] {
		t.Fatalf("expected a.txt and b.txt, got %v", files)
	}

	runGit(t, tmpDir, "merge", "--abort")
}

// TestGetConflictedFilesInDir_ReturnsEmptyWhenNoConflicts verifies that
// no files are returned when there are no conflicts.
func TestGetConflictedFilesInDir_ReturnsEmptyWhenNoConflicts(t *testing.T) {
	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test")
	os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("initial\n"), 0o644)
	runGit(t, tmpDir, "add", "file.txt")
	runGit(t, tmpDir, "commit", "-m", "Initial")

	files := getConflictedFilesInDir(tmpDir)
	if len(files) != 0 {
		t.Fatalf("expected no files, got %v", files)
	}
}

// TestGetConflictedFilesFallback_DetectsConflicts verifies that the fallback
// git diff --diff-filter=U method detects conflicts.
func TestGetConflictedFilesFallback_DetectsConflicts(t *testing.T) {
	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test")

	// Create initial file
	os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("line 1\nline 2\nline 3\n"), 0o644)
	runGit(t, tmpDir, "add", "file.txt")
	runGit(t, tmpDir, "commit", "-m", "Initial")

	// Create conflicting branch
	runGit(t, tmpDir, "checkout", "-b", "feature")
	os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("line 1\nfeature change\nline 3\n"), 0o644)
	runGit(t, tmpDir, "add", "file.txt")
	runGit(t, tmpDir, "commit", "-m", "Feature")

	// Conflicting change on master
	runGit(t, tmpDir, "checkout", "master")
	os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("line 1\nmaster change\nline 3\n"), 0o644)
	runGit(t, tmpDir, "add", "file.txt")
	runGit(t, tmpDir, "commit", "-m", "Master")

	// Start merge (will conflict)
	cmd := exec.Command("git", "merge", "feature")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	files := getConflictedFilesFallback(tmpDir)
	if len(files) != 1 || files[0] != "file.txt" {
		t.Fatalf("expected [file.txt], got %v", files)
	}

	runGit(t, tmpDir, "merge", "--abort")
}

// TestGetConflictedFilesFallback_ReturnsEmptyWhenNoConflicts verifies that
// the fallback returns empty when there are no conflicts.
func TestGetConflictedFilesFallback_ReturnsEmptyWhenNoConflicts(t *testing.T) {
	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test")
	os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("initial\n"), 0o644)
	runGit(t, tmpDir, "add", "file.txt")
	runGit(t, tmpDir, "commit", "-m", "Initial")

	files := getConflictedFilesFallback(tmpDir)
	if len(files) != 0 {
		t.Fatalf("expected no files, got %v", files)
	}
}

// TestGetMergeStatusOutput_ReturnsDiagnostics verifies that the diagnostic
// function returns meaningful output in various states.
func TestGetMergeStatusOutput_ReturnsDiagnostics(t *testing.T) {
	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test")
	os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("initial\n"), 0o644)
	runGit(t, tmpDir, "add", "file.txt")
	runGit(t, tmpDir, "commit", "-m", "Initial")

	// Clean working tree
	output := getMergeStatusOutput(tmpDir)
	if output != "(working tree clean)" {
		t.Fatalf("expected '(working tree clean)', got %q", output)
	}

	// Dirty working tree
	os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("modified\n"), 0o644)
	output = getMergeStatusOutput(tmpDir)
	if output == "(working tree clean)" {
		t.Fatal("expected dirty status, got clean")
	}
	if !strings.Contains(output, "file.txt") {
		t.Fatalf("expected status to mention file.txt, got %q", output)
	}
}

// TestGetMergeStatusOutput_NonGitDir verifies behavior in a non-git directory.
func TestGetMergeStatusOutput_NonGitDir(t *testing.T) {
	tmpDir := t.TempDir()
	output := getMergeStatusOutput(tmpDir)
	if !strings.Contains(output, "git status failed") {
		t.Fatalf("expected error message for non-git dir, got %q", output)
	}
}

// TestMergeConflictFallback_ChainWithAutoResolve simulates the full fallback
// chain: merge conflict where git auto-resolves (both deleted same file).
func TestMergeConflictFallback_ChainWithAutoResolve(t *testing.T) {
	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test")

	// Create a file
	os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("original\n"), 0o644)
	runGit(t, tmpDir, "add", "file.txt")
	runGit(t, tmpDir, "commit", "-m", "Initial")

	// Both branches delete the same file — git auto-resolves this
	runGit(t, tmpDir, "checkout", "-b", "feature")
	os.Remove(filepath.Join(tmpDir, "file.txt"))
	runGit(t, tmpDir, "add", "-A")
	runGit(t, tmpDir, "commit", "-m", "Delete file")

	runGit(t, tmpDir, "checkout", "master")
	os.Remove(filepath.Join(tmpDir, "file.txt"))
	runGit(t, tmpDir, "add", "-A")
	runGit(t, tmpDir, "commit", "-m", "Delete file")

	// Merge should succeed (git auto-resolves DD)
	cmd := exec.Command("git", "merge", "feature")
	cmd.Dir = tmpDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("merge should have succeeded (both deleted same file): %v: %s", err, string(out))
	}

	// Verify no merge in progress
	if isMergeInProgress(tmpDir) {
		t.Fatal("expected no merge in progress after successful auto-resolve")
	}
}

// TestMergeConflictFallback_NoMergeHead verifies that isMergeInProgress
// returns false after merge --abort.
func TestMergeConflictFallback_NoMergeHead(t *testing.T) {
	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test")

	// Create initial file
	os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("line 1\nline 2\nline 3\n"), 0o644)
	runGit(t, tmpDir, "add", "file.txt")
	runGit(t, tmpDir, "commit", "-m", "Initial")

	// Create conflicting branch
	runGit(t, tmpDir, "checkout", "-b", "feature")
	os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("line 1\nfeature\nline 3\n"), 0o644)
	runGit(t, tmpDir, "add", "file.txt")
	runGit(t, tmpDir, "commit", "-m", "Feature")

	// Conflicting change on master
	runGit(t, tmpDir, "checkout", "master")
	os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("line 1\nmaster\nline 3\n"), 0o644)
	runGit(t, tmpDir, "add", "file.txt")
	runGit(t, tmpDir, "commit", "-m", "Master")

	// Start merge (will conflict)
	cmd := exec.Command("git", "merge", "feature")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	if !isMergeInProgress(tmpDir) {
		t.Fatal("expected merge in progress before abort")
	}

	// Abort merge
	runGit(t, tmpDir, "merge", "--abort")

	if isMergeInProgress(tmpDir) {
		t.Fatal("expected no merge in progress after abort")
	}
}
