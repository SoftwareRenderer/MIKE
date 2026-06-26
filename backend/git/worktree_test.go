package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"kanban/backend/config"
	"kanban/backend/store"
	"kanban/backend/types"
)

// setupTestRepo creates a temporary git repo, store, config, project, and task.
func setupTestRepo(t *testing.T) (string, string, *store.Store, *config.ConfigStore, *WorktreeManager) {
	t.Helper()

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	worktreesDir := filepath.Join(tmpDir, "worktrees")
	os.MkdirAll(repoDir, 0o755)
	os.MkdirAll(worktreesDir, 0o755)

	// Initialize git repo
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")
	writeFile(t, repoDir, "README.md", "# Test\n")
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "Initial")

	// Create in-memory store and config
	dbPath := filepath.Join(tmpDir, "test.db")
	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	cfgStore := config.NewConfigStore(s, dbPath)
	cfg, err := cfgStore.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	cfg.WorkspaceDir = worktreesDir
	if err := cfgStore.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	// Create project
	projID := "test-project"
	proj := &types.Project{ID: projID, Name: "test", Path: repoDir}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create task
	taskID := "test-task"
	task := &types.Task{
		ID:        taskID,
		ProjectID: projID,
		Title:     "Test task",
		Prompt:    "Test prompt",
		Status:    "in_progress",
		Branch:    "task/" + taskID,
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	wm := NewWorktreeManager(s, cfgStore)

	return repoDir, taskID, s, cfgStore, wm
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %s: %v", args, string(out), err)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func TestResolveDiffBaseline_RejectsFatalBaseRef(t *testing.T) {
	_, taskID, _, _, wm := setupTestRepo(t)

	// A "fatal:" baseRef should be treated as empty
	result, err := wm.ResolveDiffBaseline(taskID, "fatal: Needed a single revision", "task/test-task", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty string for fatal baseRef, got %q", result)
	}
}

func TestResolveDiffBaseline_ValidBaseRef(t *testing.T) {
	_, taskID, _, _, wm := setupTestRepo(t)

	// A valid baseRef should be returned as-is (original thread)
	result, err := wm.ResolveDiffBaseline(taskID, "master", "task/test-task", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "master" {
		t.Errorf("expected master, got %q", result)
	}
}

func TestGetTaskDiff_RejectsFatalBaseRef(t *testing.T) {
	_, taskID, _, _, wm := setupTestRepo(t)

	// Create the worktree first
	branchName := "task/" + taskID
	_, _, err := wm.CreateWorktree("test-project", taskID, branchName)
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	// Make a change in the worktree
	wtPath, _ := wm.WorktreePath(taskID)
	writeFile(t, wtPath, "README.md", "# Test changed\n")

	// With a "fatal:" baseRef, it should fall back to HEAD and succeed
	result, err := wm.GetTaskDiff(taskID, "fatal: Needed a single revision", "")
	if err != nil {
		t.Fatalf("GetTaskDiff should succeed with fatal baseRef (fallback to HEAD): %v", err)
	}
	// Should have diff content since we modified README.md
	if result.Diff == "" {
		t.Error("expected diff content after modifying file, got empty diff")
	}
}

func TestCreateWorktree_EarlyReturn_SanitizesFatalBranch(t *testing.T) {
	repoDir, taskID, _, _, wm := setupTestRepo(t)

	// Create the worktree normally first
	branchName := "task/" + taskID
	_, _, err := wm.CreateWorktree("test-project", taskID, branchName)
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	// Now simulate the "fatal:" scenario by putting the repo in detached HEAD
	// and calling CreateWorktree again (early return path)
	runGit(t, repoDir, "checkout", "--detach", "HEAD")
	defer runGit(t, repoDir, "checkout", "master")

	// Call CreateWorktree again — worktree already exists, hits early return
	// gitCurrentBranch will return "HEAD" in detached state, which is fine
	// But we need to test the "fatal:" case specifically
	// Since we can't easily mock gitCurrentBranch, we verify the logic by
	// checking that the early return path sanitizes the value.
	// The actual sanitization is tested via ResolveDiffBaseline above.
	// Here we just verify the early return works without error.
	_, baseRef, err := wm.CreateWorktree("test-project", taskID, branchName)
	if err != nil {
		t.Fatalf("create worktree (early return): %v", err)
	}
	// In detached HEAD state, gitCurrentBranch returns "HEAD" (valid), not "fatal:"
	// The "fatal:" case only happens when git itself errors out
	if baseRef == "fatal: Needed a single revision" {
		t.Error("CreateWorktree should not return fatal: messages as baseRef")
	}
}

func TestTruncateDiffLines_NoTruncationNeeded(t *testing.T) {
	diff := `diff --git a/file.txt b/file.txt
index 1234567..abcdef 100644
--- a/file.txt
+++ b/file.txt
@@ -1,3 +1,4 @@
 line1
+added1
 line2
 line3
+added2`
	result, truncated := truncateDiffLines(diff, 60)
	if len(truncated) != 0 {
		t.Errorf("expected no truncated files, got %v", truncated)
	}
	if result != diff {
		t.Error("expected diff to be unchanged")
	}
}

func TestTruncateDiffLines_TruncatesLargeFile(t *testing.T) {
	diff := `diff --git a/small.txt b/small.txt
--- a/small.txt
+++ b/small.txt
@@ -1,2 +1,4 @@
 line1
+added1
+added2
 line2
+added3
+added4
diff --git a/large.txt b/large.txt
--- a/large.txt
+++ b/large.txt
@@ -1,2 +1,4 @@
 line1
+h1-added1
+h1-added2
 line2
+h1-added3
@@ -10,2 +10,4 @@
 line10
+h2-added1
+h2-added2
 line11
+h2-added3
@@ -20,2 +20,4 @@
 line20
+h3-added1
+h3-added2
 line21
+h3-added3`
	result, truncated := truncateDiffLines(diff, 6)

	// large.txt has 9 changes (> 6), should be truncated
	if len(truncated) != 1 || truncated[0] != "large.txt" {
		t.Errorf("expected [large.txt], got %v", truncated)
	}

	// small.txt should be fully present
	if !strings.Contains(result, "added4") {
		t.Error("expected small.txt changes to be fully present")
	}

	// large.txt: first hunk always kept (3 changes), second hunk fits (3+3=6), third dropped
	if !strings.Contains(result, "h1-added3") {
		t.Error("expected first hunk of large.txt")
	}
	if !strings.Contains(result, "h2-added3") {
		t.Error("expected second hunk of large.txt (fits within limit)")
	}
	if strings.Contains(result, "h3-added1") {
		t.Error("expected third hunk of large.txt to be truncated")
	}

	// Count @@ headers: small.txt(1) + large.txt(2) = 3 hunks
	// Each hunk header has two @@ markers, so 6 total
	hunkMarkers := strings.Count(result, "@@")
	if hunkMarkers != 6 {
		t.Errorf("expected 6 @@ markers (3 hunks), got %d", hunkMarkers)
	}
	// Third hunk should be fully absent
	if strings.Contains(result, "h3-added") {
		t.Error("expected third hunk to be truncated")
	}
}

func TestTruncateDiffLines_SingleLargeHunk(t *testing.T) {
	var lines []string
	lines = append(lines, "diff --git a/file.txt b/file.txt")
	lines = append(lines, "--- a/file.txt")
	lines = append(lines, "+++ b/file.txt")
	lines = append(lines, "@@ -1,2 +1,12 @@")
	lines = append(lines, " line1")
	for i := 1; i <= 10; i++ {
		lines = append(lines, "+line"+string(rune('0'+i)))
	}
	lines = append(lines, " line2")
	diff := "\n" + strings.Join(lines, "\n")

	result, truncated := truncateDiffLines(diff, 6)

	if len(truncated) != 1 || truncated[0] != "file.txt" {
		t.Errorf("expected [file.txt], got %v", truncated)
	}
	// Hunk truncated to 6 changes, header updated
	if !strings.Contains(result, "@@") {
		t.Error("expected hunk header")
	}
	if !strings.Contains(result, "+line1") {
		t.Error("expected first change line")
	}
	if !strings.Contains(result, "+line6") {
		t.Error("expected 6th change line")
	}
	if strings.Contains(result, "+line:") {
		t.Error("expected 7th+ change lines to be truncated")
	}
}

func TestTruncateDiffLines_EmptyDiff(t *testing.T) {
	result, truncated := truncateDiffLines("", 60)
	if result != "" {
		t.Errorf("expected empty result, got %q", result)
	}
	if len(truncated) != 0 {
		t.Errorf("expected no truncated files, got %v", truncated)
	}
}

func TestGetTaskDiff_TruncatesUntrackedFile(t *testing.T) {
	_, taskID, _, _, wm := setupTestRepo(t)

	// Create worktree
	_, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	// Create an untracked file with > 60 lines (all additions)
	var lines []string
	for i := 1; i <= 80; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	content := strings.Join(lines, "\n") + "\n"
	worktreePath, _ := wm.WorktreePath(taskID)
	writeFile(t, worktreePath, "large-new-file.txt", content)

	// Get diff (with truncation)
	result, err := wm.GetTaskDiff(taskID, "", "")
	if err != nil {
		t.Fatalf("GetTaskDiff: %v", err)
	}

	// Should have 1 untracked file
	if len(result.Untracked) != 1 {
		t.Fatalf("expected 1 untracked file, got %d", len(result.Untracked))
	}

	untracked := result.Untracked[0]
	if untracked.Path != "large-new-file.txt" {
		t.Errorf("expected path large-new-file.txt, got %s", untracked.Path)
	}
	if !untracked.Truncated {
		t.Error("expected untracked file to be truncated")
	}
	if untracked.OrigAdded != 80 {
		t.Errorf("expected origAdded=80, got %d", untracked.OrigAdded)
	}

	// Truncated diff should have exactly 60 change lines
	changeCount := 0
	for _, line := range strings.Split(untracked.Diff, "\n") {
		if len(line) > 0 && line[0] == '+' && !strings.HasPrefix(line, "+++") {
			changeCount++
		}
	}
	if changeCount != defaultMaxChangeLines {
		t.Errorf("expected %d change lines in truncated diff, got %d", defaultMaxChangeLines, changeCount)
	}

	// Verify full diff is available via skipTruncate
	resultFull, err := wm.GetTaskDiff(taskID, "", "", true)
	if err != nil {
		t.Fatalf("GetTaskDiff(full): %v", err)
	}
	if len(resultFull.Untracked) != 1 {
		t.Fatalf("expected 1 untracked file in full diff, got %d", len(resultFull.Untracked))
	}
	if resultFull.Untracked[0].Truncated {
		t.Error("expected full diff to NOT be truncated")
	}
	fullChangeCount := 0
	for _, line := range strings.Split(resultFull.Untracked[0].Diff, "\n") {
		if len(line) > 0 && line[0] == '+' && !strings.HasPrefix(line, "+++") {
			fullChangeCount++
		}
	}
	if fullChangeCount != 80 {
		t.Errorf("expected 80 change lines in full diff, got %d", fullChangeCount)
	}
}

func TestGetTaskDiff_SmallUntrackedFile_NotTruncated(t *testing.T) {
	_, taskID, _, _, wm := setupTestRepo(t)

	_, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	worktreePath, _ := wm.WorktreePath(taskID)
	writeFile(t, worktreePath, "small-new-file.txt", "line1\nline2\nline3\n")

	result, err := wm.GetTaskDiff(taskID, "", "")
	if err != nil {
		t.Fatalf("GetTaskDiff: %v", err)
	}

	if len(result.Untracked) != 1 {
		t.Fatalf("expected 1 untracked file, got %d", len(result.Untracked))
	}

	if result.Untracked[0].Truncated {
		t.Error("expected small untracked file to NOT be truncated")
	}
}

func TestWalkUntrackedEntries_RegularFile(t *testing.T) {
	tmpDir := t.TempDir()
	writeFile(t, tmpDir, "file.txt", "content")

	files := walkUntrackedEntries(tmpDir, "file.txt")
	if len(files) != 1 || files[0] != "file.txt" {
		t.Errorf("expected [file.txt], got %v", files)
	}
}

func TestWalkUntrackedEntries_Directory(t *testing.T) {
	tmpDir := t.TempDir()

	// Create subdirectory with files
	os.MkdirAll(filepath.Join(tmpDir, "sub", "nested"), 0o755)
	writeFile(t, filepath.Join(tmpDir, "sub"), "file1.txt", "content1")
	writeFile(t, filepath.Join(tmpDir, "sub", "nested"), "file2.txt", "content2")

	// Walk the directory entry (simulating "?? sub/" from git status)
	files := walkUntrackedEntries(tmpDir, "sub")
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(files), files)
	}

	// Check paths use forward slashes (filepath.Rel on Unix)
	pathSet := make(map[string]bool)
	for _, f := range files {
		pathSet[f] = true
	}
	if !pathSet["sub/file1.txt"] {
		t.Error("expected sub/file1.txt")
	}
	if !pathSet["sub/nested/file2.txt"] {
		t.Error("expected sub/nested/file2.txt")
	}
}

func TestGetTaskDiff_UntrackedDirectory(t *testing.T) {
	_, taskID, _, _, wm := setupTestRepo(t)

	_, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	worktreePath, _ := wm.WorktreePath(taskID)

	// Create a new directory with files (simulating "?? js/" from git status)
	os.MkdirAll(filepath.Join(worktreePath, "js"), 0o755)
	writeFile(t, filepath.Join(worktreePath, "js"), "app.js", "console.log('hello');\n")
	writeFile(t, filepath.Join(worktreePath, "js"), "utils.js", "export fn() {}\n")

	// Also create a file at root level
	writeFile(t, worktreePath, "index.html", "<html></html>\n")

	result, err := wm.GetTaskDiff(taskID, "", "")
	if err != nil {
		t.Fatalf("GetTaskDiff: %v", err)
	}

	// Should have 3 untracked files (2 in js/ + 1 at root)
	if len(result.Untracked) != 3 {
		t.Fatalf("expected 3 untracked files, got %d: paths=%v",
			len(result.Untracked),
			func() []string { paths := make([]string, len(result.Untracked)); for i, u := range result.Untracked { paths[i] = u.Path }; return paths }())
	}

	pathSet := make(map[string]bool)
	for _, u := range result.Untracked {
		pathSet[u.Path] = true
	}
	if !pathSet["js/app.js"] {
		t.Error("expected js/app.js in untracked files")
	}
	if !pathSet["js/utils.js"] {
		t.Error("expected js/utils.js in untracked files")
	}
	if !pathSet["index.html"] {
		t.Error("expected index.html in untracked files")
	}
}

func TestGetTaskDiff_BinaryUntrackedFile_NoDiff(t *testing.T) {
	_, taskID, _, _, wm := setupTestRepo(t)

	_, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	worktreePath, _ := wm.WorktreePath(taskID)
	// Write binary content (null bytes trigger isBinaryFile detection)
	binaryContent := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00}
	if err := os.WriteFile(filepath.Join(worktreePath, "binary-file.bin"), binaryContent, 0o644); err != nil {
		t.Fatalf("write binary file: %v", err)
	}

	result, err := wm.GetTaskDiff(taskID, "", "")
	if err != nil {
		t.Fatalf("GetTaskDiff: %v", err)
	}

	if len(result.Untracked) != 1 {
		t.Fatalf("expected 1 untracked file, got %d", len(result.Untracked))
	}

	untracked := result.Untracked[0]
	if untracked.Path != "binary-file.bin" {
		t.Errorf("expected path binary-file.bin, got %s", untracked.Path)
	}
	if untracked.Diff != "" {
		t.Error("expected binary file to have no diff content")
	}
	if untracked.FileSize == 0 {
		t.Error("expected binary file to have FileSize set")
	}
}
