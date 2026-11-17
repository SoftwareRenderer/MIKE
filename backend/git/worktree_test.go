package git

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mike/backend/config"
	"mike/backend/store"
	"mike/backend/types"
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

	cfgStore := config.NewConfigStore(s, dbPath, "test", nil, false, false)
	cfgStore.SetWorkspaceDirOverride(worktreesDir)
	cfg, err := cfgStore.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if err := cfgStore.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	projID := "test-project"
	proj := &types.Project{ID: projID, Name: "test", Path: repoDir}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	taskID := "test-task"
	task := &types.Task{
		ID:        taskID,
		ProjectID: projID,
		Title:     "Test task",
		Prompt:    "Test prompt",
		Status:    types.StatusInProgress,
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
	result := wm.resolveDiffBaseline(taskID, "fatal: Needed a single revision", "task/test-task", "")
	if result != "" {
		t.Errorf("expected empty string for fatal baseRef, got %q", result)
	}
}

func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", ref).Output()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v", ref, err)
	}
	return strings.TrimSpace(string(out))
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

// TestCreateWorktree_BothPathsReturnBranchName verifies that both the
// fresh-creation and early-return (worktree exists) paths return a branch
// name, not a commit SHA. This ensures BaseRef is always a usable merge target.
func TestCreateWorktree_BothPathsReturnBranchName(t *testing.T) {
	_, taskID, _, _, wm := setupTestRepo(t)

	branchName := "task/" + taskID
	_, baseRefFresh, err := wm.CreateWorktree("test-project", taskID, branchName, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	// Fresh creation returns a branch name (e.g. "master"), not a SHA
	if len(baseRefFresh) == 40 {
		t.Errorf("fresh CreateWorktree returned a 40-char SHA %q, expected branch name", baseRefFresh)
	}
	if baseRefFresh == "" {
		t.Error("fresh CreateWorktree returned empty baseRef")
	}

	// Call CreateWorktree again — worktree already exists, hits early return
	_, baseRefEarly, err := wm.CreateWorktree("test-project", taskID, branchName, "")
	if err != nil {
		t.Fatalf("create worktree (early return): %v", err)
	}
	// Early return also returns a branch name
	if len(baseRefEarly) == 40 {
		t.Errorf("early return returned a 40-char SHA %q, expected branch name", baseRefEarly)
	}
	if baseRefEarly == "" {
		t.Error("early return returned empty baseRef")
	}

	// Both paths should return the same branch name
	if baseRefFresh != baseRefEarly {
		t.Errorf("inconsistent baseRef: fresh=%q, early=%q", baseRefFresh, baseRefEarly)
	}
	t.Logf("fresh baseRef=%q, early return baseRef=%q (both branch names)", baseRefFresh, baseRefEarly)
}

// A branch name that is empty or 40 hex reaches one guard in CreateWorktree, which names the worktree's
// branch task/<taskID>. The hex shape is the one that hurts: git reads such a branch as the commit itself.
func TestCreateWorktree_GeneratesBranchNameForAnUnusableRef(t *testing.T) {
	for _, branchName := range []string{"", "abcdef1234567890abcdef1234567890abcdef12"} {
		_, taskID, _, _, wm := setupTestRepo(t)

		wtPath, _, err := wm.CreateWorktree("test-project", taskID, branchName, "")
		if err != nil {
			t.Fatalf("branchName %q: unexpected error: %v", branchName, err)
		}

		currentBranch, _ := Run(wtPath, "rev-parse", "--abbrev-ref", "HEAD")
		currentBranch = strings.TrimSpace(currentBranch)
		if currentBranch != "task/"+taskID {
			t.Errorf("branchName %q: HEAD is on %q, want 'task/%s'", branchName, currentBranch, taskID)
		}
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
	for _, ext := range []string{"file.go", "script.py", "style.css", "data.json", "page.html", "notes.txt", "logo.svg", "page.xhtml"} {
		if isBinaryExtension(ext) {
			t.Errorf("expected %s to NOT be detected as binary extension", ext)
		}
	}
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
	unstaged, _ := Run(worktreePath, "diff")
	staged, _ := Run(worktreePath, "diff", "--staged", "master")
	t.Logf("unstaged diff:\n%s", unstaged)
	t.Logf("staged diff:\n%s", staged)

	// Use "master" as baseRef (like resolveDiffBaseline returns)
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

func TestParseUnifiedDiffToFiles_BlankLinesHaveTextField(t *testing.T) {
	// Verify that blank lines in a diff (empty context/added/removed lines)
	// have the Text field serialized as an empty string, not omitted.
	// This is critical for the frontend's enrichRowsWithInlineSegments,
	// which pairs removed/added rows and accesses row.text.
	diff := `diff --git a/file.go b/file.go
index abc1234..def5678 100644
--- a/file.go
+++ b/file.go
@@ -1,6 +1,4 @@
 line1
-
-oldLine
-
 line2
+
+newLine
 line3`

	files := parseUnifiedDiffToFiles(diff, nil, nil, nil)

	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	file := files["file.go"]

	// Verify row count and that blank lines have empty (not nil/missing) text
	if len(file.Rows) == 0 {
		t.Fatal("expected non-zero rows")
	}

	// Serialize to JSON and verify text field is always present
	jsonBytes, err := json.Marshal(file.Rows)
	if err != nil {
		t.Fatalf("failed to marshal rows: %v", err)
	}

	// Parse back and verify each row has a "text" key
	var rows []map[string]any
	if err := json.Unmarshal(jsonBytes, &rows); err != nil {
		t.Fatalf("failed to unmarshal rows: %v", err)
	}

	for i, row := range rows {
		if _, ok := row["text"]; !ok {
			t.Errorf("row %d (variant=%q): missing 'text' field in JSON — frontend will crash on undefined.text", i, row["variant"])
		}
	}
}

// The panel's counts come from here: a staged file belongs to the tracked map (a staged new file is not
// "untracked"), and a file with both a staged and an unstaged half counts both.
func TestGetTaskDiffCountsStagedAndUnstagedWork(t *testing.T) {
	// taskWorktreeWith commits `committed`, then writes `now` in the task's worktree, staging staged.
	taskWorktreeWith := func(t *testing.T, committed map[string]string, staged map[string]string, unstaged map[string]string) (*WorktreeManager, string) {
		t.Helper()
		repoDir, taskID, _, _, wm := setupTestRepo(t)
		for name, body := range committed {
			writeFile(t, repoDir, name, body)
			runGit(t, repoDir, "add", name)
		}
		if len(committed) > 0 {
			runGit(t, repoDir, "commit", "-m", "Initial")
		}
		if _, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", ""); err != nil {
			t.Fatalf("create worktree: %v", err)
		}
		wt, err := wm.WorktreePath(taskID)
		if err != nil {
			t.Fatalf("worktree path: %v", err)
		}
		for name, body := range staged {
			writeFile(t, wt, name, body)
			runGit(t, wt, "add", name)
		}
		for name, body := range unstaged {
			writeFile(t, wt, name, body)
		}
		return wm, taskID
	}

	t.Run("a staged modification", func(t *testing.T) {
		wm, taskID := taskWorktreeWith(t,
			map[string]string{"hello.txt": "line1\nline2\n"},
			map[string]string{"hello.txt": "line1\nline2\nline3\nline4\n"}, nil)

		result, err := wm.GetTaskDiff(taskID, "", "")
		if err != nil {
			t.Fatalf("GetTaskDiff: %v", err)
		}
		file, ok := result.Tracked["hello.txt"]
		if !ok {
			t.Fatalf("expected hello.txt in the Tracked map, got %v", keysOf(result.Tracked))
		}
		if file.Added != 2 || file.Removed != 0 {
			t.Errorf("added/removed = %d/%d, want 2/0", file.Added, file.Removed)
		}
	})

	t.Run("a staged new file is tracked, not untracked", func(t *testing.T) {
		wm, taskID := taskWorktreeWith(t,
			map[string]string{"readme.md": "# README\n"},
			map[string]string{"new.txt": "new line 1\nnew line 2\nnew line 3\n"}, nil)

		result, err := wm.GetTaskDiff(taskID, "", "")
		if err != nil {
			t.Fatalf("GetTaskDiff: %v", err)
		}
		file, ok := result.Tracked["new.txt"]
		if !ok {
			for _, u := range result.Untracked {
				if u.Path == "new.txt" {
					t.Fatal("a staged new file must be in Tracked, not Untracked — the panel stages from Tracked")
				}
			}
			t.Fatalf("expected new.txt in the Tracked map, got %v", keysOf(result.Tracked))
		}
		if file.Added != 3 {
			t.Errorf("added = %d, want 3", file.Added)
		}
	})

	t.Run("a staged and an unstaged half both count", func(t *testing.T) {
		wm, taskID := taskWorktreeWith(t,
			map[string]string{"hello.txt": "line1\nline2\n"},
			map[string]string{"hello.txt": "line1\nline2\nline3\n"},
			map[string]string{"hello.txt": "line1\nline2\nline3\nline4\n"})

		result, err := wm.GetTaskDiff(taskID, "", "")
		if err != nil {
			t.Fatalf("GetTaskDiff: %v", err)
		}
		file, ok := result.Tracked["hello.txt"]
		if !ok {
			t.Fatalf("expected hello.txt in the Tracked map, got %v", keysOf(result.Tracked))
		}
		if file.Added != 2 {
			t.Errorf("added = %d, want 2 (the staged line3 and the unstaged line4)", file.Added)
		}
	})
}

// A file with nothing in it is still a file the task made: dropping it from both shapes would hide it
// from the panel entirely.
func TestGetTaskDiffKeepsABlankNewFile(t *testing.T) {
	t.Run("staged", func(t *testing.T) {
		repoDir, taskID, _, _, wm := setupTestRepo(t)
		writeFile(t, repoDir, "readme.md", "# README\n")
		runGit(t, repoDir, "add", "readme.md")
		runGit(t, repoDir, "commit", "-m", "Initial")
		if _, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", ""); err != nil {
			t.Fatalf("create worktree: %v", err)
		}
		wt, _ := wm.WorktreePath(taskID)
		writeFile(t, wt, "blank.txt", "")
		runGit(t, wt, "add", "blank.txt")

		result, err := wm.GetTaskDiff(taskID, "", "")
		if err != nil {
			t.Fatalf("GetTaskDiff: %v", err)
		}
		file, ok := result.Tracked["blank.txt"]
		if !ok {
			t.Fatalf("expected blank.txt in the Tracked map, got %v", keysOf(result.Tracked))
		}
		if !file.IsNew {
			t.Error("expected IsNew to be true for a staged blank file")
		}
		if len(file.Rows) != 0 {
			t.Errorf("expected 0 rows for a blank file, got %d", len(file.Rows))
		}
	})

	t.Run("untracked", func(t *testing.T) {
		repoDir, taskID, _, _, wm := setupTestRepo(t)
		writeFile(t, repoDir, "readme.md", "# README\n")
		runGit(t, repoDir, "add", "readme.md")
		runGit(t, repoDir, "commit", "-m", "Initial")
		if _, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", ""); err != nil {
			t.Fatalf("create worktree: %v", err)
		}
		wt, _ := wm.WorktreePath(taskID)
		writeFile(t, wt, "blank_untracked.txt", "")

		result, err := wm.GetTaskDiff(taskID, "", "")
		if err != nil {
			t.Fatalf("GetTaskDiff: %v", err)
		}
		for _, u := range result.Untracked {
			if u.Path == "blank_untracked.txt" {
				if len(u.Lines) != 0 {
					t.Errorf("expected 0 lines for a blank file, got %d", len(u.Lines))
				}
				return
			}
		}
		t.Error("expected blank_untracked.txt in the Untracked slice")
	})
}

// A new file the size of a build artifact must not ship whole: the diff is capped, the panel is told
// the file was cut and what it really held, and `skipTruncate` answers the rest on demand.
func TestGetTaskDiffTruncatesLargeUntrackedFiles(t *testing.T) {
	worktreeWith := func(t *testing.T, lines int) (*WorktreeManager, string) {
		t.Helper()
		_, taskID, _, _, wm := setupTestRepo(t)
		if _, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", ""); err != nil {
			t.Fatalf("create worktree: %v", err)
		}
		wt, _ := wm.WorktreePath(taskID)
		if lines == 3 {
			writeFile(t, wt, "small-new-file.txt", "line1\nline2\nline3\n")
			return wm, taskID
		}
		var b strings.Builder
		for i := 1; i <= lines; i++ {
			fmt.Fprintf(&b, "line %d\n", i)
		}
		writeFile(t, wt, "large-new-file.txt", b.String())
		return wm, taskID
	}

	t.Run("over the limit", func(t *testing.T) {
		wm, taskID := worktreeWith(t, 80)

		result, err := wm.GetTaskDiff(taskID, "", "")
		if err != nil {
			t.Fatalf("GetTaskDiff: %v", err)
		}
		if len(result.Untracked) != 1 {
			t.Fatalf("expected 1 untracked file, got %d", len(result.Untracked))
		}
		untracked := result.Untracked[0]
		if untracked.Path != "large-new-file.txt" {
			t.Errorf("path = %s, want large-new-file.txt", untracked.Path)
		}
		if !untracked.Truncated {
			t.Error("expected the untracked file to be truncated")
		}
		if untracked.OrigAdded != 80 {
			t.Errorf("origAdded = %d, want 80", untracked.OrigAdded)
		}
		if len(untracked.Lines) != defaultMaxChangeLines {
			t.Errorf("expected %d lines, got %d", defaultMaxChangeLines, len(untracked.Lines))
		}

		full, err := wm.GetTaskDiff(taskID, "", "", true)
		if err != nil {
			t.Fatalf("GetTaskDiff(full): %v", err)
		}
		if len(full.Untracked) != 1 || full.Untracked[0].Truncated || len(full.Untracked[0].Lines) != 80 {
			t.Errorf("expected the full diff to hold all 80 untruncated lines, got %+v", full.Untracked)
		}
	})

	t.Run("under the limit", func(t *testing.T) {
		wm, taskID := worktreeWith(t, 3)

		result, err := wm.GetTaskDiff(taskID, "", "")
		if err != nil {
			t.Fatalf("GetTaskDiff: %v", err)
		}
		if len(result.Untracked) != 1 {
			t.Fatalf("expected 1 untracked file, got %d", len(result.Untracked))
		}
		if result.Untracked[0].Truncated {
			t.Error("a small untracked file must not be marked truncated")
		}
	})
}

// An untracked file's content ships to the browser, so what counts as opaque decides whether a person
// can read what the agent wrote. An .svg is XML source, not a blob; a .png is a blob whatever it holds.
func TestGetTaskDiffClassifiesUntrackedContent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		file     string
		body     string
		wantBin  bool
		wantRows bool
	}{
		{"a png is binary even when it holds text", "image.png", "fake png content", true, false},
		{"an svg is source", "logo.svg", `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16"><rect width="16" height="16"/></svg>`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, taskID, _, _, wm := setupTestRepo(t)
			if _, _, err := wm.CreateWorktree("test-project", taskID, "task/test-task", ""); err != nil {
				t.Fatalf("create worktree: %v", err)
			}
			wt, _ := wm.WorktreePath(taskID)
			if err := os.WriteFile(filepath.Join(wt, tc.file), []byte(tc.body), 0o644); err != nil {
				t.Fatalf("write file: %v", err)
			}

			result, err := wm.GetTaskDiff(taskID, "", "")
			if err != nil {
				t.Fatalf("GetTaskDiff: %v", err)
			}
			if len(result.Untracked) != 1 {
				t.Fatalf("expected 1 untracked file, got %d", len(result.Untracked))
			}
			file := result.Untracked[0]
			if file.IsBinary != tc.wantBin {
				t.Errorf("IsBinary = %v, want %v", file.IsBinary, tc.wantBin)
			}
			if got := len(file.Lines) > 0; got != tc.wantRows {
				t.Errorf("lines present = %v, want %v", got, tc.wantRows)
			}
		})
	}
}

// The identity is accessor-only and stamped at boot; a commit path appends these flags, so an unnamed
// half must not send an empty `-c user.name=` for git to resolve somewhere else.
func TestGitAuthorFlagsCarryOnlyWhatIsNamed(t *testing.T) {
	origName, origEmail := AuthorIdentity()
	t.Cleanup(func() { SetAuthorIdentity(origName, origEmail) })

	for _, tc := range []struct {
		name, email string
		want        []string
	}{
		{"", "", nil},
		{"Custom Name", "", []string{"-c", "user.name=Custom Name"}},
		{"", "user@example.com", []string{"-c", "user.email=user@example.com"}},
		{"Custom Name", "user@example.com", []string{"-c", "user.name=Custom Name", "-c", "user.email=user@example.com"}},
	} {
		SetAuthorIdentity(tc.name, tc.email)
		if got := GitAuthorFlags(); !slices.Equal(got, tc.want) {
			t.Errorf("GitAuthorFlags() for %q <%q> = %v, want %v", tc.name, tc.email, got, tc.want)
		}
	}
}

// The identity is stamped at boot from git's own GIT_COMMITTER_* / GIT_AUTHOR_* variables: the committer
// half wins where it exists, the author half fills the gap, and an environment that names nothing leaves
// the repository to answer.
func TestAuthorIdentityFromEnvPrefersTheCommitterPerHalf(t *testing.T) {
	for _, tc := range []struct {
		name              string
		env               map[string]string
		wantName, wantSet string
	}{
		{
			"the committer half wins, the author half fills the gap",
			map[string]string{
				"GIT_COMMITTER_NAME": "Committer",
				"GIT_AUTHOR_NAME":    "Author",
				"GIT_AUTHOR_EMAIL":   "  author@example.com  ",
			},
			"Committer", "author@example.com",
		},
		{
			"an environment that names nothing",
			map[string]string{},
			"", "",
		},
	} {
		name, email := AuthorIdentityFromEnv(func(key string) string { return tc.env[key] })
		if name != tc.wantName || email != tc.wantSet {
			t.Errorf("%s: got %q <%q>, want %q <%q>", tc.name, name, email, tc.wantName, tc.wantSet)
		}
	}
}

// The card asks this hash whether an untracked file changed. A file whose content moved while its line
// count stayed the same must answer differently, or the browser keeps showing the stale file.
func TestComputeUntrackedHashPricesTheContentNotTheShape(t *testing.T) {
	sameShape := UntrackedFile{Path: "test.txt", Lines: []string{"todo"}}
	changed := UntrackedFile{Path: "test.txt", Lines: []string{"todo app"}}
	if ComputeUntrackedHash(sameShape) == ComputeUntrackedHash(changed) {
		t.Errorf("one hash %s for different content of one line — delta detection would skip the refetch",
			ComputeUntrackedHash(sameShape))
	}
}

// A project names the person its commits carry, and it does so one half at a time: the half it leaves
// blank stays this server's answer, and a project that names nothing sends no flags at all, which is
// how a repository's own user.name/user.email keeps deciding.
func TestProjectIdentityLayersEachHalfOnItsOwn(t *testing.T) {
	origName, origEmail := AuthorIdentity()
	t.Cleanup(func() { SetAuthorIdentity(origName, origEmail) })

	for _, tc := range []struct {
		name         string
		serverName   string
		serverEmail  string
		projectName  string
		projectEmail string
		want         []string
	}{
		{
			"the project's name over the server's email",
			"Server", "server@example.com", "Ada Lovelace", "",
			[]string{"-c", "user.name=Ada Lovelace", "-c", "user.email=server@example.com"},
		},
		{
			"the project's email over the server's name",
			"Server", "server@example.com", "", "ada@example.com",
			[]string{"-c", "user.name=Server", "-c", "user.email=ada@example.com"},
		},
		{
			"a project that names both replaces both",
			"Server", "server@example.com", "Ada Lovelace", "ada@example.com",
			[]string{"-c", "user.name=Ada Lovelace", "-c", "user.email=ada@example.com"},
		},
		{
			"a project that names nothing leaves the server's pair",
			"Server", "server@example.com", "", "",
			[]string{"-c", "user.name=Server", "-c", "user.email=server@example.com"},
		},
		{
			"nobody names anything, so git answers",
			"", "", "", "",
			nil,
		},
	} {
		SetAuthorIdentity(tc.serverName, tc.serverEmail)
		if got := ProjectIdentity(tc.projectName, tc.projectEmail).flags(); !slices.Equal(got, tc.want) {
			t.Errorf("%s: ProjectIdentity(%q, %q).flags() = %v, want %v", tc.name, tc.projectName, tc.projectEmail, got, tc.want)
		}
	}
}

// The commit preview and the git status panel name a person; the flags a commit carries are the same
// layering, with the repository's own config answering only where neither row nor server did.
func TestProjectAuthorIdentityAnswersTheProjectFirst(t *testing.T) {
	origName, origEmail := AuthorIdentity()
	t.Cleanup(func() { SetAuthorIdentity(origName, origEmail) })
	fill := func(key string) string {
		if key == "user.name" {
			return "Repo Person"
		}
		return "repo@example.com"
	}

	SetAuthorIdentity("Server", "")
	if name, email := ProjectAuthorIdentity("", "ada@example.com", fill); name != "Server" || email != "ada@example.com" {
		t.Errorf("project email over the server name: got %q <%q>", name, email)
	}
	if name, email := ProjectAuthorIdentity("Ada Lovelace", "", fill); name != "Ada Lovelace" || email != "repo@example.com" {
		t.Errorf("an unnamed email falls to the repository: got %q <%q>", name, email)
	}
	if name, email := ProjectAuthorIdentity("", "", fill); name != "Server" || email != "repo@example.com" {
		t.Errorf("nobody naming the email asked the repository: got %q <%q>", name, email)
	}

	SetAuthorIdentity("", "")
	if name, email := ProjectAuthorIdentity("", "", fill); name != "Repo Person" || email != "repo@example.com" {
		t.Errorf("an unstamped server asked the repository: got %q <%q>", name, email)
	}
}
