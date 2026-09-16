package exec

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
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

// TestGetConflictedFiles_DetectsUUConflicts verifies that standard UU (both modified) conflicts are detected.
func TestGetConflictedFiles_DetectsUUConflicts(t *testing.T) {
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

	files := GetConflictedFiles(tmpDir)
	if len(files) != 1 || files[0] != "file.txt" {
		t.Fatalf("expected [file.txt], got %v", files)
	}

	runGit(t, tmpDir, "merge", "--abort")
}

// TestGetConflictedFiles_DetectsMultipleConflicts verifies that multiple conflicted files are all detected.
func TestGetConflictedFiles_DetectsMultipleConflicts(t *testing.T) {
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

	files := GetConflictedFiles(tmpDir)
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

// TestGetConflictedFiles_ReturnsEmptyWhenNoConflicts verifies that no files are returned when there are no conflicts.
func TestGetConflictedFiles_ReturnsEmptyWhenNoConflicts(t *testing.T) {
	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test")
	os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("initial\n"), 0o644)
	runGit(t, tmpDir, "add", "file.txt")
	runGit(t, tmpDir, "commit", "-m", "Initial")

	files := GetConflictedFiles(tmpDir)
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

// TestIsMergeInProgress_InWorktree verifies that isMergeInProgress works
// correctly inside a git worktree (where .git is a file, not a directory).
func TestIsMergeInProgress_InWorktree(t *testing.T) {
	tmpDir := t.TempDir()
	mainRepo := filepath.Join(tmpDir, "main")
	os.MkdirAll(mainRepo, 0o755)
	runGit(t, mainRepo, "init")
	runGit(t, mainRepo, "config", "user.email", "test@test.com")
	runGit(t, mainRepo, "config", "user.name", "Test")
	os.WriteFile(filepath.Join(mainRepo, "file.txt"), []byte("line 1\nline 2\nline 3\n"), 0o644)
	runGit(t, mainRepo, "add", "file.txt")
	runGit(t, mainRepo, "commit", "-m", "Initial")

	// Create a worktree on a new branch
	wtDir := filepath.Join(tmpDir, "worktree")
	runGit(t, mainRepo, "worktree", "add", wtDir, "-b", "wt-branch")

	// Make conflicting change in worktree
	os.WriteFile(filepath.Join(wtDir, "file.txt"), []byte("line 1\nwt change\nline 3\n"), 0o644)
	runGitInDir(t, wtDir, "add", "file.txt")
	runGitInDir(t, wtDir, "commit", "-m", "WT change")

	// Make conflicting change in main
	runGit(t, mainRepo, "checkout", "master")
	os.WriteFile(filepath.Join(mainRepo, "file.txt"), []byte("line 1\nmain change\nline 3\n"), 0o644)
	runGit(t, mainRepo, "add", "file.txt")
	runGit(t, mainRepo, "commit", "-m", "Main change")

	// Merge in worktree (will conflict)
	cmd := exec.Command("git", "merge", "master")
	cmd.Dir = wtDir
	_ = cmd.Run()

	// isMergeInProgress should work inside the worktree
	if !isMergeInProgress(wtDir) {
		t.Fatal("expected isMergeInProgress to return true inside worktree during merge conflict")
	}

	runGitInDir(t, wtDir, "merge", "--abort")

	if isMergeInProgress(wtDir) {
		t.Fatal("expected isMergeInProgress to return false after abort in worktree")
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

	runGit(t, tmpDir, "merge", "--abort")

	if isMergeInProgress(tmpDir) {
		t.Fatal("expected no merge in progress after abort")
	}
}

// TestGetConflictedFiles_OptionsShapedAndQuotedNames checks that a conflicted file whose name git
// would quote, or read as one of its own options, still comes back as a path and gets staged.
func TestGetConflictedFiles_OptionsShapedAndQuotedNames(t *testing.T) {
	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@test.com")
	runGit(t, tmpDir, "config", "user.name", "Test")

	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("--force", "base\n")
	write("café.txt", "base\n")
	write("UU-notes.txt", "base\n")
	runGit(t, tmpDir, "add", "-A")
	runGit(t, tmpDir, "commit", "-m", "base")

	runGit(t, tmpDir, "checkout", "-b", "feature")
	write("--force", "feature\n")
	write("café.txt", "feature\n")
	runGit(t, tmpDir, "mv", "UU-notes.txt", "notes.txt")
	runGit(t, tmpDir, "commit", "-qam", "feature")

	runGit(t, tmpDir, "checkout", "master")
	write("--force", "master\n")
	write("café.txt", "master\n")
	runGit(t, tmpDir, "commit", "-qam", "master")

	// The merge conflicts on both files and applies the rename cleanly.
	merge := exec.Command("git", "merge", "feature")
	merge.Dir = tmpDir
	_ = merge.Run()

	got := append([]string(nil), GetConflictedFiles(tmpDir)...)
	sort.Strings(got)
	// The rename merged cleanly, so neither its target nor its source is conflicted.
	if want := []string{"--force", "café.txt"}; !slices.Equal(got, want) {
		t.Fatalf("conflicted files = %q, want %q", got, want)
	}

	write("--force", "resolved\n")
	write("café.txt", "resolved\n")
	if !(&mergeOrchestrator{}).stageResolvedConflicts("task-1", tmpDir) {
		t.Fatal("stageResolvedConflicts failed to stage the resolved files")
	}

	listCmd := exec.Command("git", "diff", "--cached", "--name-only", "-z")
	listCmd.Dir = tmpDir
	stagedRaw, err := listCmd.Output()
	if err != nil {
		t.Fatalf("git diff --cached --name-only: %v", err)
	}
	staged := strings.Split(string(stagedRaw), "\x00")
	for _, name := range []string{"--force", "café.txt"} {
		if !slices.Contains(staged, name) {
			t.Errorf("%q was not staged; staged: %q", name, staged)
		}
	}
	// "--force" is a real option of git add too, and taken as one it leaves the file conflicted.
	if still := GetConflictedFiles(tmpDir); len(still) != 0 {
		t.Errorf("%q is still conflicted after staging the resolution", still)
	}
}
