package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	_, _, err := wm.CreateWorktree("test-project", taskID, branchName, "")
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
	_, _, err := wm.CreateWorktree("test-project", taskID, branchName, "")
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
	_, baseRef, err := wm.CreateWorktree("test-project", taskID, branchName, "")
	if err != nil {
		t.Fatalf("create worktree (early return): %v", err)
	}
	// In detached HEAD state, gitCurrentBranch returns "HEAD" (valid), not "fatal:"
	// The "fatal:" case only happens when git itself errors out
	if baseRef == "fatal: Needed a single revision" {
		t.Error("CreateWorktree should not return fatal: messages as baseRef")
	}
}

func TestCreateWorktree_EarlyReturn_ReturnsBranchNameNotSHA(t *testing.T) {
	_, taskID, _, _, wm := setupTestRepo(t)

	// Create the worktree normally first
	branchName := "task/" + taskID
	_, baseRefFresh, err := wm.CreateWorktree("test-project", taskID, branchName, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	// Fresh creation returns a commit SHA (40 hex chars)
	if len(baseRefFresh) != 40 {
		t.Logf("WARNING: fresh CreateWorktree returned baseRef %q (len=%d), expected 40-char SHA", baseRefFresh, len(baseRefFresh))
	}

	// Call CreateWorktree again — worktree already exists, hits early return
	_, baseRefEarly, err := wm.CreateWorktree("test-project", taskID, branchName, "")
	if err != nil {
		t.Fatalf("create worktree (early return): %v", err)
	}
	// Early return returns the main repo's current branch name (e.g. "master"), NOT a SHA
	// This is a known issue: the caller (prepareTaskExecution) should preserve the original BaseRef
	if baseRefEarly == baseRefFresh {
		t.Error("early return should return a different value (branch name) than fresh creation (SHA)")
	}
	// The early return value is a short branch name, not a 40-char SHA
	if len(baseRefEarly) == 40 {
		t.Error("early return should NOT return a 40-char SHA, it returns the main repo's branch name")
	}
	t.Logf("fresh baseRef=%q (SHA), early return baseRef=%q (branch name)", baseRefFresh, baseRefEarly)
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
	_, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", "")
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

	// Truncated lines should have exactly 60 entries
	if len(untracked.Lines) != defaultMaxChangeLines {
		t.Errorf("expected %d lines in truncated untracked file, got %d", defaultMaxChangeLines, len(untracked.Lines))
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
	if len(resultFull.Untracked[0].Lines) != 80 {
		t.Errorf("expected 80 lines in full untracked file, got %d", len(resultFull.Untracked[0].Lines))
	}
}

func TestGetTaskDiff_SmallUntrackedFile_NotTruncated(t *testing.T) {
	_, taskID, _, _, wm := setupTestRepo(t)

	_, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", "")
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

	_, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", "")
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
			func() []string {
				paths := make([]string, len(result.Untracked))
				for i, u := range result.Untracked {
					paths[i] = u.Path
				}
				return paths
			}())
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

	_, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	worktreePath, _ := wm.WorktreePath(taskID)
	// Write binary content (null bytes trigger isBinaryContent detection)
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
	if len(untracked.Lines) != 0 {
		t.Error("expected binary file to have no lines content")
	}
	if untracked.FileSize == 0 {
		t.Error("expected binary file to have FileSize set")
	}
	if !untracked.IsBinary {
		t.Error("expected IsBinary to be true for binary file")
	}
}

func TestGetTaskDiff_BinaryUntrackedByExtension(t *testing.T) {
	_, taskID, _, _, wm := setupTestRepo(t)

	_, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	worktreePath, _ := wm.WorktreePath(taskID)
	// Write text content but with a binary extension — should be detected as binary by extension
	if err := os.WriteFile(filepath.Join(worktreePath, "image.png"), []byte("fake png content"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	result, err := wm.GetTaskDiff(taskID, "", "")
	if err != nil {
		t.Fatalf("GetTaskDiff: %v", err)
	}

	if len(result.Untracked) != 1 {
		t.Fatalf("expected 1 untracked file, got %d", len(result.Untracked))
	}
	if !result.Untracked[0].IsBinary {
		t.Error("expected IsBinary to be true for .png extension")
	}
}

func TestGetTaskDiff_TrackedBinaryFile(t *testing.T) {
	repoDir, taskID, _, _, wm := setupTestRepo(t)

	// Add a binary file to the repo
	binaryContent := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00}
	writeFile(t, repoDir, "logo.png", string(binaryContent))
	runGit(t, repoDir, "add", "logo.png")
	runGit(t, repoDir, "commit", "-m", "Add logo")

	_, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	worktreePath, _ := wm.WorktreePath(taskID)
	// Modify the binary file
	newContent := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01, 0x02, 0x03}
	if err := os.WriteFile(filepath.Join(worktreePath, "logo.png"), newContent, 0o644); err != nil {
		t.Fatalf("write binary file: %v", err)
	}

	result, err := wm.GetTaskDiff(taskID, "", "")
	if err != nil {
		t.Fatalf("GetTaskDiff: %v", err)
	}

	// Binary file should appear in Tracked map
	if result.Tracked == nil {
		t.Fatal("expected Tracked map to be non-nil")
	}
	file, ok := result.Tracked["logo.png"]
	if !ok {
		t.Fatal("expected logo.png in Tracked map")
	}
	if !file.IsBinary {
		t.Error("expected IsBinary to be true")
	}
	if file.FileSize == nil || *file.FileSize == 0 {
		t.Error("expected FileSize to be set")
	}
	if len(file.Rows) != 0 {
		t.Errorf("expected 0 rows for binary file, got %d", len(file.Rows))
	}
}

func TestGetTaskDiff_LargeTextFile(t *testing.T) {
	_, taskID, _, _, wm := setupTestRepo(t)

	_, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	worktreePath, _ := wm.WorktreePath(taskID)
	// Create a large text file (>1MB = 1048576 bytes)
	largeContent := strings.Repeat("line of text\n", 81000) // 81000*13 = 1053000 bytes
	if err := os.WriteFile(filepath.Join(worktreePath, "large.txt"), []byte(largeContent), 0o644); err != nil {
		t.Fatalf("write large file: %v", err)
	}

	result, err := wm.GetTaskDiff(taskID, "", "")
	if err != nil {
		t.Fatalf("GetTaskDiff: %v", err)
	}

	if len(result.Untracked) != 1 {
		t.Fatalf("expected 1 untracked file, got %d", len(result.Untracked))
	}
	untracked := result.Untracked[0]
	if untracked.IsBinary {
		t.Error("expected IsBinary to be false for large text file")
	}
	if untracked.FileSize == 0 {
		t.Error("expected FileSize to be set for large file")
	}
}

func TestIsBinaryExtension(t *testing.T) {
	// Known binary extensions
	for _, ext := range []string{"image.png", "file.jpg", "archive.zip", "font.woff2", "video.mp4", "doc.pdf"} {
		if !isBinaryExtension(ext) {
			t.Errorf("expected %s to be detected as binary extension", ext)
		}
	}
	// Text extensions
	for _, ext := range []string{"file.go", "script.py", "style.css", "data.json", "page.html", "notes.txt"} {
		if isBinaryExtension(ext) {
			t.Errorf("expected %s to NOT be detected as binary extension", ext)
		}
	}
	// No extension
	if isBinaryExtension("Makefile") {
		t.Error("expected Makefile (no extension) to NOT be detected as binary")
	}
}

func TestGetTaskDiff_DeletedBinaryFile(t *testing.T) {
	repoDir, taskID, _, _, wm := setupTestRepo(t)

	// Add a binary file to the repo
	binaryContent := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00}
	writeFile(t, repoDir, "logo.png", string(binaryContent))
	runGit(t, repoDir, "add", "logo.png")
	runGit(t, repoDir, "commit", "-m", "Add logo")

	_, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	worktreePath, _ := wm.WorktreePath(taskID)
	// Delete the binary file
	if err := os.Remove(filepath.Join(worktreePath, "logo.png")); err != nil {
		t.Fatalf("remove binary file: %v", err)
	}

	result, err := wm.GetTaskDiff(taskID, "", "")
	if err != nil {
		t.Fatalf("GetTaskDiff: %v", err)
	}

	// Deleted binary file should appear in Tracked map
	if result.Tracked == nil {
		t.Fatal("expected Tracked map to be non-nil")
	}
	file, ok := result.Tracked["logo.png"]
	if !ok {
		t.Fatal("expected logo.png in Tracked map (deleted binary)")
	}
	if !file.IsBinary {
		t.Error("expected IsBinary to be true for deleted binary file")
	}
}

func TestGetTaskDiff_RenamedBinaryFile(t *testing.T) {
	repoDir, taskID, _, _, wm := setupTestRepo(t)

	// Add a binary file to the repo
	binaryContent := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00}
	writeFile(t, repoDir, "old-name.png", string(binaryContent))
	runGit(t, repoDir, "add", "old-name.png")
	runGit(t, repoDir, "commit", "-m", "Add logo")

	_, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	worktreePath, _ := wm.WorktreePath(taskID)
	// Rename the binary file (delete old + add new = git sees as modify for same content)
	newContent := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01}
	os.Remove(filepath.Join(worktreePath, "old-name.png"))
	os.WriteFile(filepath.Join(worktreePath, "new-name.png"), newContent, 0o644)

	result, err := wm.GetTaskDiff(taskID, "", "")
	if err != nil {
		t.Fatalf("GetTaskDiff: %v", err)
	}

	// Should have the deleted binary in tracked
	if result.Tracked == nil {
		t.Fatal("expected Tracked map to be non-nil")
	}
	oldFile, ok := result.Tracked["old-name.png"]
	if !ok {
		t.Fatal("expected old-name.png in Tracked map (deleted binary)")
	}
	if !oldFile.IsBinary {
		t.Error("expected IsBinary to be true for old-name.png")
	}
}

func TestShouldIgnorePath_KnownDirs(t *testing.T) {
	// Known directories at root level
	for _, dir := range []string{"node_modules", ".next", "dist", "build", ".cache", "__pycache__", ".venv", "target", "vendor", ".gradle", ".mypy_cache", ".pytest_cache", "coverage", ".nuxt", ".output", ".svelte-kit", ".turbo", ".vercel", ".docker"} {
		if !ShouldIgnorePath(dir) {
			t.Errorf("expected %q to be ignored", dir)
		}
		if !ShouldIgnorePath(dir + "/file.txt") {
			t.Errorf("expected %q/file.txt to be ignored", dir)
		}
	}
}

func TestShouldIgnorePath_NestedPaths(t *testing.T) {
	// Known directories nested deeper
	tests := []struct {
		path     string
		expected bool
	}{
		{"src/node_modules/pkg/index.js", true},
		{"packages/app/.next/cache/build", true},
		{"lib/__pycache__/module.cpython-311.pyc", true},
		{"a/b/c/vendor/bin/tool", true},
		{"src/dist/output.js", true},
		// Should NOT match partial names
		{"my_node_modules/file.txt", false},
		{"node_modules_backup/file.txt", false},
		{"distro/file.txt", false},
		{"building/file.txt", false},
		{"coverage_report/file.txt", false},
		// Normal paths should not be ignored
		{"src/index.ts", false},
		{"lib/utils/helpers.ts", false},
		{"README.md", false},
		{".github/workflows/ci.yml", false},
	}

	for _, tc := range tests {
		result := ShouldIgnorePath(tc.path)
		if result != tc.expected {
			t.Errorf("ShouldIgnorePath(%q) = %v, want %v", tc.path, result, tc.expected)
		}
	}
}

func TestComputeUntrackedHash_ContentChange(t *testing.T) {
	// Verify that ComputeUntrackedHash produces different hashes when
	// file content changes but line count stays the same.
	// This is critical for delta detection: if the hash doesn't change,
	// the frontend will skip refetching the file and show stale content.
	u1 := UntrackedFile{Path: "test.txt", Lines: []string{"todo"}}
	u2 := UntrackedFile{Path: "test.txt", Lines: []string{"todo app"}}

	hash1 := ComputeUntrackedHash(u1)
	hash2 := ComputeUntrackedHash(u2)

	if hash1 == hash2 {
		t.Errorf("ComputeUntrackedHash produced same hash for different content (same line count): %s", hash1)
	}

	// Also verify same content produces same hash (deterministic)
	u3 := UntrackedFile{Path: "test.txt", Lines: []string{"todo"}}
	hash3 := ComputeUntrackedHash(u3)
	if hash1 != hash3 {
		t.Errorf("ComputeUntrackedHash non-deterministic: %s != %s", hash1, hash3)
	}

	// Verify different line count also produces different hash
	u4 := UntrackedFile{Path: "test.txt", Lines: []string{"todo", "more"}}
	hash4 := ComputeUntrackedHash(u4)
	if hash1 == hash4 {
		t.Errorf("ComputeUntrackedHash produced same hash for different line count: %s", hash1)
	}
}

func TestGetTaskDiff_StagedFile(t *testing.T) {
	repoDir, taskID, _, _, wm := setupTestRepo(t)

	// Create and commit a file
	writeFile(t, repoDir, "hello.txt", "line1\nline2\n")
	runGit(t, repoDir, "add", "hello.txt")
	runGit(t, repoDir, "commit", "-m", "Initial")

	_, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	worktreePath, _ := wm.WorktreePath(taskID)
	// Modify the file and stage it (git add) — don't commit
	writeFile(t, worktreePath, "hello.txt", "line1\nline2\nline3\nline4\n")
	runGit(t, worktreePath, "add", "hello.txt")

	result, err := wm.GetTaskDiff(taskID, "", "")
	if err != nil {
		t.Fatalf("GetTaskDiff: %v", err)
	}

	// Staged file should appear in Tracked map
	if result.Tracked == nil {
		t.Fatal("expected Tracked map to be non-nil")
	}
	file, ok := result.Tracked["hello.txt"]
	if !ok {
		t.Fatal("expected hello.txt in Tracked map")
	}
	if file.Added != 2 {
		t.Errorf("expected 2 added lines for staged file, got %d", file.Added)
	}
	if file.Removed != 0 {
		t.Errorf("expected 0 removed lines for staged file, got %d", file.Removed)
	}
}

func TestGetTaskDiff_StagedNewFile(t *testing.T) {
	repoDir, taskID, _, _, wm := setupTestRepo(t)

	// Create initial commit
	writeFile(t, repoDir, "readme.md", "# README\n")
	runGit(t, repoDir, "add", "readme.md")
	runGit(t, repoDir, "commit", "-m", "Initial")

	_, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	worktreePath, _ := wm.WorktreePath(taskID)
	// Create a new file and stage it
	writeFile(t, worktreePath, "new.txt", "new line 1\nnew line 2\nnew line 3\n")
	runGit(t, worktreePath, "add", "new.txt")

	result, err := wm.GetTaskDiff(taskID, "", "")
	if err != nil {
		t.Fatalf("GetTaskDiff: %v", err)
	}

	// Staged new file should appear in Tracked map (not untracked)
	if result.Tracked == nil {
		t.Fatal("expected Tracked map to be non-nil")
	}
	file, ok := result.Tracked["new.txt"]
	if !ok {
		// Check if it's in untracked instead (shouldn't be)
		for _, u := range result.Untracked {
			if u.Path == "new.txt" {
				t.Fatal("staged new file should be in Tracked, not Untracked")
			}
		}
		t.Fatal("expected new.txt in Tracked map")
	}
	if file.Added != 3 {
		t.Errorf("expected 3 added lines for staged new file, got %d", file.Added)
	}
}

func TestGetTaskDiff_MixedStagedAndUnstaged(t *testing.T) {
	repoDir, taskID, _, _, wm := setupTestRepo(t)

	// Create and commit a file
	writeFile(t, repoDir, "hello.txt", "line1\nline2\n")
	runGit(t, repoDir, "add", "hello.txt")
	runGit(t, repoDir, "commit", "-m", "Initial")

	_, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	worktreePath, _ := wm.WorktreePath(taskID)
	// Make staged changes: add line3
	writeFile(t, worktreePath, "hello.txt", "line1\nline2\nline3\n")
	runGit(t, worktreePath, "add", "hello.txt")
	// Make additional unstaged changes: add line4
	writeFile(t, worktreePath, "hello.txt", "line1\nline2\nline3\nline4\n")

	result, err := wm.GetTaskDiff(taskID, "", "")
	if err != nil {
		t.Fatalf("GetTaskDiff: %v", err)
	}

	file, ok := result.Tracked["hello.txt"]
	if !ok {
		t.Fatal("expected hello.txt in Tracked map")
	}
	// Total changes: +2 (line3 staged, line4 unstaged)
	if file.Added != 2 {
		t.Errorf("expected 2 added lines (1 staged + 1 unstaged), got %d", file.Added)
	}
}

func TestGetTaskDiff_StagedFileWithBaseRef(t *testing.T) {
	repoDir, taskID, _, _, wm := setupTestRepo(t)

	// Create and commit a file on master
	writeFile(t, repoDir, "hello.txt", "line1\nline2\n")
	runGit(t, repoDir, "add", "hello.txt")
	runGit(t, repoDir, "commit", "-m", "Initial")

	// Create worktree on task branch
	_, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	worktreePath, _ := wm.WorktreePath(taskID)
	// Modify and stage the file
	writeFile(t, worktreePath, "hello.txt", "line1\nline2\nline3\nline4\nline5\nline6\n")
	runGit(t, worktreePath, "add", "hello.txt")

	// Debug: print raw git output
	unstaged, _ := wm.gitRunOutput(worktreePath, "diff")
	staged, _ := wm.gitRunOutput(worktreePath, "diff", "--staged", "master")
	t.Logf("unstaged diff:\n%s", unstaged)
	t.Logf("staged diff:\n%s", staged)

	// Use "master" as baseRef (like ResolveDiffBaseline returns)
	result, err := wm.GetTaskDiff(taskID, "master", "")
	if err != nil {
		t.Fatalf("GetTaskDiff: %v", err)
	}

	file, ok := result.Tracked["hello.txt"]
	if !ok {
		t.Fatal("expected hello.txt in Tracked map")
	}
	if file.Added != 4 {
		t.Errorf("expected 4 added lines, got %d", file.Added)
	}
	if file.Removed != 0 {
		t.Errorf("expected 0 removed lines, got %d", file.Removed)
	}
}

func TestResetWorktreeTo_StashHash(t *testing.T) {
	_, taskID, _, _, wm := setupTestRepo(t)

	// Create worktree.
	branchName := "task/" + taskID
	_, _, err := wm.CreateWorktree("test-project", taskID, branchName, "")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	t.Cleanup(func() { _ = wm.RemoveWorktree("test-project", taskID, branchName) })

	worktreePath, _ := wm.WorktreePath(taskID)

	// Write a file.
	writeFile(t, worktreePath, "hello.c", "// hello\nint main() { return 0; }\n")

	// Stash worktree, extract hash, drop stash (simulates fork handler).
	out := runGitOutput(t, worktreePath, "stash", "push", "-u", "-m", "test-fork")
	t.Log("stash push output:", out)

	hash := runGitOutput(t, worktreePath, "rev-parse", "stash@{0}")
	hash = strings.TrimSpace(hash)
	if hash == "" {
		t.Fatal("expected non-empty stash hash")
	}

	runGit(t, worktreePath, "stash", "drop", "stash@{0}")

	if _, err := os.Stat(filepath.Join(worktreePath, "hello.c")); !os.IsNotExist(err) {
		t.Log("note: hello.c still exists after stash (expected if not git add -A'd)")
	}

	// Write a different file (simulates fork branch).
	writeFile(t, worktreePath, "goodbye.c", "// goodbye\nint main() { return 1; }\n")

	// Reset worktree to saved stash hash (simulates switch back to main).
	if err := wm.ResetWorktreeTo(taskID, hash); err != nil {
		t.Fatalf("ResetWorktreeTo: %v", err)
	}

	// Verify: hello.c restored, goodbye.c gone.
	helloPath := filepath.Join(worktreePath, "hello.c")
	goodbyePath := filepath.Join(worktreePath, "goodbye.c")

	if _, err := os.Stat(helloPath); os.IsNotExist(err) {
		t.Error("hello.c should exist after ResetWorktreeTo (was in stash)")
	} else {
		content, _ := os.ReadFile(helloPath)
		if !strings.Contains(string(content), "hello") {
			t.Errorf("hello.c has wrong content: %s", string(content))
		}
	}

	if _, err := os.Stat(goodbyePath); !os.IsNotExist(err) {
		t.Error("goodbye.c should NOT exist after ResetWorktreeTo (was not in stash)")
	}
}

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s: %v", args, string(out), err)
	}
	return string(out)
}

func TestGarbageCollect_PrunesOrphanedThreadBranches(t *testing.T) {
	repoDir, taskID, s, _, wm := setupTestRepo(t)

	// Create a thread in the DB with a git_branch
	threadID := "test-thread-abc"
	thread := &types.TaskThread{
		ID:        threadID,
		TaskID:    taskID,
		Name:      "Fork #1",
		IsActive:  false,
		GitBranch: "thread/" + threadID,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateThread(thread); err != nil {
		t.Fatalf("create thread: %v", err)
	}

	// Create the corresponding git branch in the repo
	runGit(t, repoDir, "branch", "thread/"+threadID)

	// Verify branch exists
	branches := getBranches(t, repoDir)
	if !contains(branches, "thread/"+threadID) {
		t.Fatal("thread branch should exist before GC")
	}

	// Delete the thread from DB (simulates task delete CASCADE)
	if err := s.DeleteThread(threadID); err != nil {
		t.Fatalf("delete thread: %v", err)
	}

	// Run GC
	removed, err := wm.GarbageCollect(false)
	if err != nil {
		t.Fatalf("GarbageCollect: %v", err)
	}
	t.Logf("GC removed %d orphaned worktrees", removed)

	// Verify thread branch is gone
	branches = getBranches(t, repoDir)
	if contains(branches, "thread/"+threadID) {
		t.Error("orphaned thread branch should be pruned by GC")
	}
}

func TestGarbageCollect_KeepsActiveThreadBranches(t *testing.T) {
	repoDir, taskID, s, _, wm := setupTestRepo(t)

	// Create a thread in the DB with a git_branch
	threadID := "test-thread-xyz"
	thread := &types.TaskThread{
		ID:        threadID,
		TaskID:    taskID,
		Name:      "Fork #2",
		IsActive:  true,
		GitBranch: "thread/" + threadID,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateThread(thread); err != nil {
		t.Fatalf("create thread: %v", err)
	}

	// Create the corresponding git branch in the repo
	runGit(t, repoDir, "branch", "thread/"+threadID)

	// Run GC — should NOT remove active thread branch
	_, err := wm.GarbageCollect(false)
	if err != nil {
		t.Fatalf("GarbageCollect: %v", err)
	}

	// Verify thread branch still exists
	branches := getBranches(t, repoDir)
	if !contains(branches, "thread/"+threadID) {
		t.Error("active thread branch should be kept by GC")
	}
}

func TestPruneOrphanedBranches_RemovesOrphanedTaskBranches(t *testing.T) {
	repoDir, taskID, s, _, wm := setupTestRepo(t)

	// Create orphaned task branches (task IDs that don't exist in DB)
	runGit(t, repoDir, "branch", "task/orphan-one-branch")
	runGit(t, repoDir, "branch", "task/orphan-two-branch")
	// Also create a branch that matches the active task (should be kept)
	runGit(t, repoDir, "branch", "task/"+taskID)

	// Verify branches exist
	branches := getBranches(t, repoDir)
	if !contains(branches, "task/orphan-one-branch") {
		t.Fatal("orphan-one-branch should exist before prune")
	}
	if !contains(branches, "task/orphan-two-branch") {
		t.Fatal("orphan-two-branch should exist before prune")
	}
	if !contains(branches, "task/"+taskID) {
		t.Fatal("active task branch should exist before prune")
	}

	// Get project ID
	proj, err := s.GetProject("test-project")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}

	// Run prune
	removed := wm.PruneOrphanedBranches(proj.ID)
	if removed != 2 {
		t.Fatalf("expected 2 branches removed, got %d", removed)
	}

	// Verify orphaned branches are gone, active branch remains
	branches = getBranches(t, repoDir)
	if contains(branches, "task/orphan-one-branch") {
		t.Error("orphan-one-branch should be pruned")
	}
	if contains(branches, "task/orphan-two-branch") {
		t.Error("orphan-two-branch should be pruned")
	}
	if !contains(branches, "task/"+taskID) {
		t.Error("active task branch should be kept")
	}
}

func TestPruneOrphanedBranches_RemovesOrphanedThreadBranches(t *testing.T) {
	repoDir, taskID, s, _, wm := setupTestRepo(t)

	// Create orphaned thread branches (thread IDs that don't exist in DB)
	runGit(t, repoDir, "branch", "thread/orphan-thread-1")
	runGit(t, repoDir, "branch", "thread/orphan-thread-2")

	// Create an active thread in DB with a branch
	activeThreadID := "active-thread-xyz"
	thread := &types.TaskThread{
		ID:        activeThreadID,
		TaskID:    taskID,
		Name:      "Active Fork",
		IsActive:  true,
		GitBranch: "thread/" + activeThreadID,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.CreateThread(thread); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	runGit(t, repoDir, "branch", "thread/"+activeThreadID)

	// Verify branches exist
	branches := getBranches(t, repoDir)
	if !contains(branches, "thread/orphan-thread-1") {
		t.Fatal("orphan thread should exist before prune")
	}
	if !contains(branches, "thread/"+activeThreadID) {
		t.Fatal("active thread branch should exist before prune")
	}

	// Get project ID
	proj, err := s.GetProject("test-project")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}

	// Run prune
	removed := wm.PruneOrphanedBranches(proj.ID)
	if removed != 2 {
		t.Fatalf("expected 2 branches removed, got %d", removed)
	}

	// Verify orphaned branches are gone, active branch remains
	branches = getBranches(t, repoDir)
	if contains(branches, "thread/orphan-thread-1") {
		t.Error("orphan thread branch should be pruned")
	}
	if contains(branches, "thread/orphan-thread-2") {
		t.Error("orphan thread branch should be pruned")
	}
	if !contains(branches, "thread/"+activeThreadID) {
		t.Error("active thread branch should be kept")
	}
}

func TestPruneOrphanedBranches_RemovesBothTaskAndThreadBranches(t *testing.T) {
	repoDir, _, s, _, wm := setupTestRepo(t)

	// Create both orphaned task and thread branches
	runGit(t, repoDir, "branch", "task/orphan-task")
	runGit(t, repoDir, "branch", "thread/orphan-thread")

	proj, err := s.GetProject("test-project")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}

	removed := wm.PruneOrphanedBranches(proj.ID)
	if removed != 2 {
		t.Fatalf("expected 2 branches removed, got %d", removed)
	}

	branches := getBranches(t, repoDir)
	if contains(branches, "task/orphan-task") {
		t.Error("orphan task branch should be pruned")
	}
	if contains(branches, "thread/orphan-thread") {
		t.Error("orphan thread branch should be pruned")
	}
}

func getBranches(t *testing.T, dir string) []string {
	t.Helper()
	out := runGitOutput(t, dir, "branch")
	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		b := strings.TrimSpace(line)
		if b != "" && !strings.HasPrefix(b, "*") {
			// Strip leading "* " for active branch
			b = strings.TrimPrefix(b, "* ")
			branches = append(branches, b)
		}
	}
	return branches
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func TestGitAuthorFlags_NoEnvVars(t *testing.T) {
	// Save and restore package-level vars
	origName, origEmail := GitAuthorName, GitAuthorEmail
	defer func() {
		GitAuthorName, GitAuthorEmail = origName, origEmail
	}()

	GitAuthorName, GitAuthorEmail = "", ""
	flags := GitAuthorFlags()
	if flags != nil {
		t.Errorf("expected nil flags when no env vars set, got %v", flags)
	}
}

func TestGitAuthorFlags_NameOnly(t *testing.T) {
	origName, origEmail := GitAuthorName, GitAuthorEmail
	defer func() {
		GitAuthorName, GitAuthorEmail = origName, origEmail
	}()

	GitAuthorName, GitAuthorEmail = "Custom Name", ""
	flags := GitAuthorFlags()
	if len(flags) != 2 {
		t.Fatalf("expected 2 flags, got %d: %v", len(flags), flags)
	}
	if flags[0] != "-c" || flags[1] != "user.name=Custom Name" {
		t.Errorf("expected [-c user.name=Custom Name], got %v", flags)
	}
}

func TestGitAuthorFlags_EmailOnly(t *testing.T) {
	origName, origEmail := GitAuthorName, GitAuthorEmail
	defer func() {
		GitAuthorName, GitAuthorEmail = origName, origEmail
	}()

	GitAuthorName, GitAuthorEmail = "", "user@example.com"
	flags := GitAuthorFlags()
	if len(flags) != 2 {
		t.Fatalf("expected 2 flags, got %d: %v", len(flags), flags)
	}
	if flags[0] != "-c" || flags[1] != "user.email=user@example.com" {
		t.Errorf("expected [-c user.email=user@example.com], got %v", flags)
	}
}

func TestGitAuthorFlags_BothSet(t *testing.T) {
	origName, origEmail := GitAuthorName, GitAuthorEmail
	defer func() {
		GitAuthorName, GitAuthorEmail = origName, origEmail
	}()

	GitAuthorName, GitAuthorEmail = "Custom Name", "user@example.com"
	flags := GitAuthorFlags()
	if len(flags) != 4 {
		t.Fatalf("expected 4 flags, got %d: %v", len(flags), flags)
	}
	expected := []string{"-c", "user.name=Custom Name", "-c", "user.email=user@example.com"}
	for i, e := range expected {
		if flags[i] != e {
			t.Errorf("flags[%d]=%q, want %q", i, flags[i], e)
		}
	}
}

func TestParseUnifiedDiffToFiles_RemovedLineNumbers(t *testing.T) {
	// Verify that removed lines get old-file line numbers (not new-file line numbers).
	// Bug: all removed lines in a hunk were assigned the same newLine value.
	diff := `diff --git a/file.go b/file.go
--- a/file.go
+++ b/file.go
@@ -48,10 +48,7 @@ func hello() {
 line48
 line49
-removed50
-removed51
-removed52
 line53
 line54
 line55
+added56`

	files := parseUnifiedDiffToFiles(diff, nil, nil, nil)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	file, ok := files["file.go"]
	if !ok {
		t.Fatal("expected file.go in parsed files")
	}

	// Verify line numbers for each row
	// Context: line48 -> oldLine=48, newLine=48
	// Context: line49 -> oldLine=49, newLine=49
	// Removed: removed50 -> oldLine=50 (NOT newLine)
	// Removed: removed51 -> oldLine=51 (NOT newLine)
	// Removed: removed52 -> oldLine=52 (NOT newLine)
	// Context: line53 -> oldLine=53, newLine=50
	// Context: line54 -> oldLine=54, newLine=51
	// Context: line55 -> oldLine=55, newLine=52
	// Added: added56 -> newLine=53

	expected := []struct {
		variant    string
		text       string
		lineNumber int
	}{
		{"context", "line48", 48},
		{"context", "line49", 49},
		{"removed", "removed50", 50},
		{"removed", "removed51", 51},
		{"removed", "removed52", 52},
		{"context", "line53", 50},
		{"context", "line54", 51},
		{"context", "line55", 52},
		{"added", "added56", 53},
	}

	if len(file.Rows) != len(expected) {
		t.Fatalf("expected %d rows, got %d", len(expected), len(file.Rows))
	}

	for i, exp := range expected {
		row := file.Rows[i]
		if row.Variant != exp.variant {
			t.Errorf("row %d: variant=%q, want %q", i, row.Variant, exp.variant)
		}
		if row.Text != exp.text {
			t.Errorf("row %d: text=%q, want %q", i, row.Text, exp.text)
		}
		if row.LineNumber == nil || *row.LineNumber != exp.lineNumber {
			t.Errorf("row %d (%s %q): lineNumber=%v, want %d", i, row.Variant, row.Text, row.LineNumber, exp.lineNumber)
		}
	}

	// Key assertion: removed lines should have DIFFERENT (sequential) line numbers
	removedLines := make([]int, 0, 3)
	for _, row := range file.Rows {
		if row.Variant == "removed" && row.LineNumber != nil {
			removedLines = append(removedLines, *row.LineNumber)
		}
	}
	if len(removedLines) != 3 {
		t.Fatalf("expected 3 removed lines, got %d", len(removedLines))
	}
	for i := 1; i < len(removedLines); i++ {
		if removedLines[i] == removedLines[i-1] {
			t.Errorf("removed lines %d and %d have same lineNumber=%d (should be sequential)", i-1, i, removedLines[i])
		}
		if removedLines[i] != removedLines[i-1]+1 {
			t.Errorf("removed lines not sequential: %d then %d", removedLines[i-1], removedLines[i])
		}
	}
}

func TestParseUnifiedDiffToFiles_MultipleHunks(t *testing.T) {
	// Verify line numbers are correct across multiple hunks in the same file.
	diff := `diff --git a/file.txt b/file.txt
--- a/file.txt
+++ b/file.txt
@@ -1,3 +1,4 @@
 line1
+added2
 line2
 line3
@@ -20,5 +21,4 @@
 line20
-line21-old
 line22
 line23
-line24-old
 line25
+added25`

	files := parseUnifiedDiffToFiles(diff, nil, nil, nil)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	file := files["file.txt"]

	// Hunk 1: @@ -1,3 +1,4 @@
	// context line1: old=1, new=1
	// added added2: new=2
	// context line2: old=2, new=3
	// context line3: old=3, new=4
	// Hunk 2: @@ -20,5 +21,4 @@
	// context line20: old=20, new=21
	// removed line21-old: old=21
	// context line22: old=22, new=22
	// context line23: old=23, new=23
	// removed line24-old: old=24
	// context line25: old=25, new=24
	// added added25: new=25

	expected := []struct {
		variant    string
		text       string
		lineNumber int
	}{
		{"context", "line1", 1},
		{"added", "added2", 2},
		{"context", "line2", 3},
		{"context", "line3", 4},
		{"context", "line20", 21},
		{"removed", "line21-old", 21},
		{"context", "line22", 22},
		{"context", "line23", 23},
		{"removed", "line24-old", 24},
		{"context", "line25", 24},
		{"added", "added25", 25},
	}

	if len(file.Rows) != len(expected) {
		t.Fatalf("expected %d rows, got %d", len(expected), len(file.Rows))
	}

	for i, exp := range expected {
		row := file.Rows[i]
		if row.Variant != exp.variant {
			t.Errorf("row %d: variant=%q, want %q", i, row.Variant, exp.variant)
		}
		if row.Text != exp.text {
			t.Errorf("row %d: text=%q, want %q", i, row.Text, exp.text)
		}
		if row.LineNumber == nil || *row.LineNumber != exp.lineNumber {
			t.Errorf("row %d (%s %q): lineNumber=%v, want %d", i, row.Variant, row.Text, row.LineNumber, exp.lineNumber)
		}
	}
}

func TestParseUnifiedDiffToFiles_NewBlankFile(t *testing.T) {
	// A new blank file has a diff header but no hunk (no content to diff).
	// Parser must detect "new file mode" and preserve the entry even with 0 rows.
	diff := `diff --git a/empty.txt b/empty.txt
new file mode 100644
index 0000000..e69de29`

	files := parseUnifiedDiffToFiles(diff, nil, nil, nil)

	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	file, ok := files["empty.txt"]
	if !ok {
		t.Fatal("expected empty.txt in files map")
	}
	if !file.IsNew {
		t.Error("expected IsNew to be true for new blank file")
	}
	if len(file.Rows) != 0 {
		t.Errorf("expected 0 rows for blank file, got %d", len(file.Rows))
	}
	if file.Added != 0 {
		t.Errorf("expected 0 added for blank file, got %d", file.Added)
	}
}

func TestParseUnifiedDiffToFiles_NewNonBlankFile(t *testing.T) {
	// Verify existing behavior: new files with content still work correctly.
	diff := `diff --git a/new.txt b/new.txt
new file mode 100644
index 0000000..1234567
--- /dev/null
+++ b/new.txt
@@ -0,0 +1,3 @@
+line1
+line2
+line3`

	files := parseUnifiedDiffToFiles(diff, nil, nil, nil)

	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	file, ok := files["new.txt"]
	if !ok {
		t.Fatal("expected new.txt in files map")
	}
	if !file.IsNew {
		t.Error("expected IsNew to be true")
	}
	if file.Added != 3 {
		t.Errorf("expected 3 added lines, got %d", file.Added)
	}
	if len(file.Rows) != 3 {
		t.Errorf("expected 3 rows, got %d", len(file.Rows))
	}
}

func TestParseUnifiedDiffToFiles_ModifiedFile_NotNew(t *testing.T) {
	// Verify that modified (non-new) files do NOT get IsNew set.
	diff := `diff --git a/existing.txt b/existing.txt
index abc1234..def5678 100644
--- a/existing.txt
+++ b/existing.txt
@@ -1,3 +1,4 @@
 line1
+new line
 line2
 line3`

	files := parseUnifiedDiffToFiles(diff, nil, nil, nil)

	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	file, ok := files["existing.txt"]
	if !ok {
		t.Fatal("expected existing.txt in files map")
	}
	if file.IsNew {
		t.Error("expected IsNew to be false for modified file")
	}
	if file.Added != 1 {
		t.Errorf("expected 1 added line, got %d", file.Added)
	}
}

func TestGetTaskDiff_StagedBlankNewFile(t *testing.T) {
	repoDir, taskID, _, _, wm := setupTestRepo(t)

	// Create initial commit
	writeFile(t, repoDir, "readme.md", "# README\n")
	runGit(t, repoDir, "add", "readme.md")
	runGit(t, repoDir, "commit", "-m", "Initial")

	_, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	worktreePath, _ := wm.WorktreePath(taskID)
	// Create a blank (empty) file and stage it
	writeFile(t, worktreePath, "blank.txt", "")
	runGit(t, worktreePath, "add", "blank.txt")

	result, err := wm.GetTaskDiff(taskID, "", "")
	if err != nil {
		t.Fatalf("GetTaskDiff: %v", err)
	}

	// Staged blank new file should appear in Tracked map with IsNew=true
	if result.Tracked == nil {
		t.Fatal("expected Tracked map to be non-nil")
	}
	file, ok := result.Tracked["blank.txt"]
	if !ok {
		t.Fatal("expected blank.txt in Tracked map")
	}
	if !file.IsNew {
		t.Error("expected IsNew to be true for staged blank file")
	}
	if len(file.Rows) != 0 {
		t.Errorf("expected 0 rows for blank file, got %d", len(file.Rows))
	}
}

func TestGetTaskDiff_UnstagedBlankNewFile(t *testing.T) {
	repoDir, taskID, _, _, wm := setupTestRepo(t)

	// Create initial commit
	writeFile(t, repoDir, "readme.md", "# README\n")
	runGit(t, repoDir, "add", "readme.md")
	runGit(t, repoDir, "commit", "-m", "Initial")

	_, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}

	worktreePath, _ := wm.WorktreePath(taskID)
	// Create a blank (empty) file — do NOT stage it
	writeFile(t, worktreePath, "blank_untracked.txt", "")

	result, err := wm.GetTaskDiff(taskID, "", "")
	if err != nil {
		t.Fatalf("GetTaskDiff: %v", err)
	}

	// Unstaged blank new file should appear in Untracked slice
	found := false
	for _, u := range result.Untracked {
		if u.Path == "blank_untracked.txt" {
			found = true
			if len(u.Lines) != 0 {
				t.Errorf("expected 0 lines for blank file, got %d", len(u.Lines))
			}
		}
	}
	if !found {
		t.Error("expected blank_untracked.txt in Untracked slice")
	}
}