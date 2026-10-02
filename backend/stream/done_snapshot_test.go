package stream

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mike/backend/config"
	"mike/backend/git"
	"mike/backend/store"
	"mike/backend/testutil/testgit"
	"mike/backend/types"
)

// setupDoneSnapshotTest creates a temp project repo, store, config, project,
// done task, and worktree manager. The project repo has an initial commit;
// the task has a worktree on its own branch.
func setupDoneSnapshotTest(t *testing.T) (string, *store.Store, *config.ConfigStore, *git.WorktreeManager, *types.Task) {
	t.Helper()
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	worktreesDir := filepath.Join(tmpDir, "worktrees")
	os.MkdirAll(repoDir, 0o755)
	os.MkdirAll(worktreesDir, 0o755)

	runGitTest(t, repoDir, "init")
	runGitTest(t, repoDir, "config", "user.email", "test@test.com")
	runGitTest(t, repoDir, "config", "user.name", "Test")
	writeTestFile(t, repoDir, "README.md", "# Base\n")
	runGitTest(t, repoDir, "add", "README.md")
	runGitTest(t, repoDir, "commit", "-m", "initial")

	s, err := store.NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	cfgStore := config.NewConfigStore(s, filepath.Join(tmpDir, "cfg.db"), "test", nil, false, false)
	cfgStore.SetWorkspaceDirOverride(worktreesDir)
	cfg, err := cfgStore.GetConfig()
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if err := cfgStore.UpdateConfig(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}

	proj := &types.Project{ID: "proj-1", Name: "test", Path: repoDir}
	if err := s.CreateProject(proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := &types.Task{
		ID:        "task-1",
		ProjectID: "proj-1",
		Title:     "done task",
		Prompt:    "p",
		Status:    types.StatusDone,
		Branch:    "task/task-1",
	}
	if err := s.CreateTask(task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	wm := git.NewWorktreeManager(s, cfgStore)
	if _, _, err := wm.CreateWorktree("proj-1", "task-1", "task/task-1", ""); err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	return repoDir, s, cfgStore, wm, task
}

// commitInWorktree writes a file change and commits it on the task branch.
func commitInWorktree(t *testing.T, wm *git.WorktreeManager, taskID, file, content string) string {
	t.Helper()
	worktreePath, err := wm.WorktreePath(taskID)
	if err != nil {
		t.Fatalf("worktree path: %v", err)
	}
	writeTestFile(t, worktreePath, file, content)
	runGitTest(t, worktreePath, "add", "-A")
	runGitTest(t, worktreePath, "commit", "-m", "change "+file)
	return runGitTestOutput(t, worktreePath, "rev-parse", "HEAD")
}

func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	return runGitTestOutput(t, dir, "rev-parse", ref)
}

// runGitTest runs a git command in dir, failing the test on error.
func runGitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	testgit.Git(t, dir, args...)
}

// runGitTestOutput runs a git command in dir and returns trimmed output.
func runGitTestOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return testgit.GitOutput(t, dir, args...)
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

// markDone sets the task's pinned refs and status to done, persisting it.
func markDone(t *testing.T, s *store.Store, task *types.Task, baseCommit, branchTip string) {
	t.Helper()
	task.BaseCommit = baseCommit
	task.BranchTip = branchTip
	task.Status = types.StatusDone
	if err := s.UpdateTask(task); err != nil {
		t.Fatalf("update task: %v", err)
	}
}

func statsFromJSON(t *testing.T, statsJSON string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(statsJSON), &m); err != nil {
		t.Fatalf("unmarshal stats: %v", err)
	}
	return m
}

func TestMaterializeDoneTaskDiff_CacheHitSkipsGit(t *testing.T) {
	repoDir, s, _, wm, task := setupDoneSnapshotTest(t)
	base := revParse(t, repoDir, "HEAD")
	tip := commitInWorktree(t, wm, "task-1", "a.txt", "alpha\nbeta\n")
	markDone(t, s, task, base, tip)

	first, err := MaterializeDoneTaskDiff(s, wm, "proj-1", task)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if !first.HasChanges {
		t.Fatal("expected changes")
	}
	stats := statsFromJSON(t, first.StatsJSON)
	if stats["files_n"] != float64(1) {
		t.Fatalf("expected files_n=1, got %v", stats["files_n"])
	}
	if stats["tracked_add"] != float64(2) {
		t.Fatalf("expected tracked_add=2, got %v", stats["tracked_add"])
	}
	var diffBody map[string]any
	if err := json.Unmarshal([]byte(first.DiffJSON), &diffBody); err != nil {
		t.Fatalf("unmarshal diff json: %v", err)
	}
	if diffBody["diff"] == "" || diffBody["diff"] == nil {
		t.Fatal("expected non-empty diff in response body")
	}

	// Break git entirely — a cache hit must not need it.
	if err := os.Rename(filepath.Join(repoDir, ".git"), filepath.Join(repoDir, ".git.bak")); err != nil {
		t.Fatalf("rename .git: %v", err)
	}
	t.Cleanup(func() { _ = os.Rename(filepath.Join(repoDir, ".git.bak"), filepath.Join(repoDir, ".git")) })

	second, err := MaterializeDoneTaskDiff(s, wm, "proj-1", task)
	if err != nil {
		t.Fatalf("second materialize (must be a cache hit): %v", err)
	}
	if second.StatsJSON != first.StatsJSON || second.DiffJSON != first.DiffJSON {
		t.Fatal("cache hit returned different data")
	}
}

func TestMaterializeDoneTaskDiff_StaleRefsReMaterialize(t *testing.T) {
	repoDir, s, _, wm, task := setupDoneSnapshotTest(t)
	base := revParse(t, repoDir, "HEAD")
	tip1 := commitInWorktree(t, wm, "task-1", "a.txt", "alpha\n")
	markDone(t, s, task, base, tip1)

	first, err := MaterializeDoneTaskDiff(s, wm, "proj-1", task)
	if err != nil {
		t.Fatalf("materialize 1: %v", err)
	}

	// Re-run the task: new tip, redone.
	tip2 := commitInWorktree(t, wm, "task-1", "b.txt", "beta\n")
	markDone(t, s, task, base, tip2)

	second, err := MaterializeDoneTaskDiff(s, wm, "proj-1", task)
	if err != nil {
		t.Fatalf("materialize 2: %v", err)
	}
	firstHash, _ := statsFromJSON(t, first.StatsJSON)["hash"].(string)
	secondHash, _ := statsFromJSON(t, second.StatsJSON)["hash"].(string)
	if firstHash == secondHash {
		t.Fatal("expected a different diff hash after the task was redone")
	}
	stats := statsFromJSON(t, second.StatsJSON)
	if stats["files_n"] != float64(2) {
		t.Fatalf("expected files_n=2 after re-run, got %v", stats["files_n"])
	}
}

func TestMaterializeDoneTaskDiff_WorktreeDeletedFallback(t *testing.T) {
	repoDir, s, _, wm, task := setupDoneSnapshotTest(t)
	base := revParse(t, repoDir, "HEAD")
	tip := commitInWorktree(t, wm, "task-1", "a.txt", "alpha\n")
	markDone(t, s, task, base, tip)

	// Remove the worktree directory but keep the branch (the fallback reads
	// commits from the project repo).
	wtPath, err := wm.WorktreePath("task-1")
	if err != nil {
		t.Fatalf("worktree path: %v", err)
	}
	if err := os.RemoveAll(wtPath); err != nil {
		t.Fatalf("remove worktree dir: %v", err)
	}
	if err := wm.GitRunInProject("proj-1", "worktree", "prune"); err != nil {
		t.Fatalf("prune worktree: %v", err)
	}

	got, err := MaterializeDoneTaskDiff(s, wm, "proj-1", task)
	if err != nil {
		t.Fatalf("materialize without worktree: %v", err)
	}
	if !got.HasChanges {
		t.Fatal("expected changes via repo fallback")
	}
	stats := statsFromJSON(t, got.StatsJSON)
	if stats["files_n"] != float64(1) {
		t.Fatalf("expected files_n=1, got %v", stats["files_n"])
	}
	// And it persisted — verify via a git-less cache hit.
	if err := os.Rename(filepath.Join(repoDir, ".git"), filepath.Join(repoDir, ".git.bak")); err != nil {
		t.Fatalf("rename .git: %v", err)
	}
	t.Cleanup(func() { _ = os.Rename(filepath.Join(repoDir, ".git.bak"), filepath.Join(repoDir, ".git")) })
	second, err := MaterializeDoneTaskDiff(s, wm, "proj-1", task)
	if err != nil {
		t.Fatalf("cache hit after fallback: %v", err)
	}
	if second.StatsJSON != got.StatsJSON {
		t.Fatal("fallback materialization was not persisted")
	}
}

func TestMaterializeDoneTaskDiff_EmptyDiffNotPersisted(t *testing.T) {
	repoDir, s, _, wm, task := setupDoneSnapshotTest(t)
	base := revParse(t, repoDir, "HEAD")
	// BranchTip == BaseCommit: the branch was created but never advanced.
	markDone(t, s, task, base, base)

	got, err := MaterializeDoneTaskDiff(s, wm, "proj-1", task)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if got == nil || got.HasChanges {
		t.Fatalf("expected no changes, got %+v", got)
	}
	if snap, _ := s.GetTaskDiffSnapshot("task-1"); snap != nil {
		t.Fatalf("expected no persisted snapshot for an empty diff, got %+v", snap)
	}
}

func TestMaterializeDoneTaskDiff_NoBranch(t *testing.T) {
	_, s, _, wm, task := setupDoneSnapshotTest(t)
	task.Branch = ""
	if err := s.UpdateTask(task); err != nil {
		t.Fatalf("update task: %v", err)
	}
	got, err := MaterializeDoneTaskDiff(s, wm, "proj-1", task)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for a task without a branch, got %+v", got)
	}
}

// doneDiffBody reads a served snapshot body the way the browser does: it renders parsed_files.
type doneDiffBody struct {
	Diff        string                `json:"diff"`
	ParsedFiles []git.TrackedDiffFile `json:"parsed_files"`
}

func parseDoneDiffBody(t *testing.T, body string) doneDiffBody {
	t.Helper()
	var b doneDiffBody
	if err := json.Unmarshal([]byte(body), &b); err != nil {
		t.Fatalf("unmarshal done-diff body: %v", err)
	}
	return b
}

func TestMaterializeDoneTaskDiff_BodyCarriesParsedFiles(t *testing.T) {
	repoDir, s, _, wm, task := setupDoneSnapshotTest(t)
	base := revParse(t, repoDir, "HEAD")
	tip := commitInWorktree(t, wm, "task-1", "a.txt", "alpha\nbeta\n")
	markDone(t, s, task, base, tip)

	first, err := MaterializeDoneTaskDiff(s, wm, "proj-1", task)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	body := parseDoneDiffBody(t, first.DiffJSON)
	if len(body.ParsedFiles) != 1 {
		t.Fatalf("expected 1 parsed file, got %d (%s)", len(body.ParsedFiles), first.DiffJSON)
	}
	file := body.ParsedFiles[0]
	if file.Path != "a.txt" || file.Added != 2 || len(file.Rows) != 2 {
		t.Fatalf("parsed entry does not describe the commit: %+v", file)
	}

	snap, err := s.GetTaskDiffSnapshot("task-1")
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if snap == nil {
		t.Fatal("expected a persisted snapshot")
	}
	if stored := parseDoneDiffBody(t, snap.DiffJSON); len(stored.ParsedFiles) != 1 {
		t.Fatalf("stored snapshot carries no parsed view: %s", snap.DiffJSON)
	}
}

// A binary file has no hunk: only the parsed structure carries isBinary, fileSize and the rename's
// origPath, which patch text alone would flatten into git's "Binary files … differ" marker as a row.
func TestMaterializeDoneTaskDiff_ParsedFilesKeepsBinaryEntry(t *testing.T) {
	repoDir, s, _, wm, task := setupDoneSnapshotTest(t)
	base := revParse(t, repoDir, "HEAD")
	tip := commitInWorktree(t, wm, "task-1", "logo.png", "\x89PNG\r\n\x1a\n\x00\x01\x02\x03binary\n")
	markDone(t, s, task, base, tip)

	got, err := MaterializeDoneTaskDiff(s, wm, "proj-1", task)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	body := parseDoneDiffBody(t, got.DiffJSON)
	if len(body.ParsedFiles) != 1 || body.ParsedFiles[0].Path != "logo.png" {
		t.Fatalf("binary file lost from the parsed view: %+v", body.ParsedFiles)
	}
	file := body.ParsedFiles[0]
	if !file.IsBinary {
		t.Fatalf("binary entry is not marked binary: %+v", file)
	}
	if file.FileSize == nil || *file.FileSize != 19 {
		t.Fatalf("binary entry does not carry its size: %+v", file)
	}
	if len(file.Rows) != 0 {
		t.Fatalf("binary entry renders git's marker line as content: %+v", file.Rows)
	}
}
