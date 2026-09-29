package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/config"
	"mike/backend/store"
	"mike/backend/types"
)

// uploadsPattern is the exclude pattern the task-upload path asks for. The name itself belongs to
// backend/exec (`uploadsDirName`); it is spelled out here because a test in this package cannot import the
// package that imports it, and the literal is what these tests are pinning.
const uploadsPattern = ".mike-uploads/"

// excludedUploadWorktree builds a task worktree, excludes the uploads directory the way the upload path
// does, and leaves one uploaded file in it.
func excludedUploadWorktree(t *testing.T, taskID string) (repoDir, wtPath string, wm *WorktreeManager) {
	t.Helper()

	repoDir, _, s, _, wm := setupTestRepo(t)
	if err := s.CreateTask(&types.Task{
		ID: taskID, ProjectID: "test-project", Title: "t", Status: types.StatusInProgress,
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	wtPath, _, err := wm.CreateWorktree("test-project", taskID, "task/"+taskID, "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if err := EnsureExcluded(repoDir, uploadsPattern, "# mike uploads (kept out of task status)"); err != nil {
		t.Fatalf("exclude the uploads dir: %v", err)
	}
	uploads := filepath.Join(wtPath, ".mike-uploads")
	if err := os.MkdirAll(uploads, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, uploads, "report.pdf", "%PDF-1.7\n")
	return repoDir, wtPath, wm
}

func excludeFileContents(t *testing.T, repoDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoDir, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("read info/exclude: %v", err)
	}
	return string(data)
}

func TestEnsureExcludedAppendsThePatternOnce(t *testing.T) {
	t.Parallel()
	repoDir := t.TempDir()
	runGit(t, repoDir, "init")
	for _, header := range []string{"# mike uploads (kept out of task status)", "# second call, same pattern"} {
		if err := EnsureExcluded(repoDir, uploadsPattern, header); err != nil {
			t.Fatalf("EnsureExcluded: %v", err)
		}
	}

	contents := excludeFileContents(t, repoDir)
	if got := strings.Count(contents, uploadsPattern); got != 1 {
		t.Errorf("pattern appears %d times in info/exclude, want once:\n%s", got, contents)
	}
}

// A project that is itself a worktree has a .git file, not a .git directory; the pattern has to land in the
// git dir the checkouts share.
func TestEnsureExcludedFollowsAGitdirPointerToTheSharedGitDir(t *testing.T) {
	t.Parallel()
	repoDir := t.TempDir()
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")
	writeFile(t, repoDir, "README.md", "# Test\n")
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "Initial")

	nested := filepath.Join(repoDir, "nested")
	runGit(t, repoDir, "worktree", "add", "-b", "nested-branch", nested)

	if err := EnsureExcluded(nested, uploadsPattern, "# mike uploads (kept out of task status)"); err != nil {
		t.Fatalf("EnsureExcluded on a checkout whose .git is a file: %v", err)
	}
	if !strings.Contains(excludeFileContents(t, repoDir), uploadsPattern) {
		t.Error("the pattern did not reach the git dir the checkouts share")
	}
	if _, err := os.Stat(filepath.Join(nested, ".git", "info", "exclude")); err == nil {
		t.Error("a .git directory was invented inside the nested checkout")
	}

	// The proof that matters: git reads exclude from the common dir, so the nested checkout must obey it.
	uploads := filepath.Join(nested, ".mike-uploads")
	if err := os.MkdirAll(uploads, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, uploads, "report.pdf", "%PDF-1.7\n")
	if out := strings.TrimSpace(runGitOutput(t, nested, "status", "--porcelain")); out != "" {
		t.Errorf("the nested checkout does not obey the exclusion:\n%s", out)
	}
}

// The pattern is unanchored for a reason: exclude patterns are relative to the checkout root, and a task's
// worktree lives below it at `.worktrees/<taskID>/`. This is the fixture that reproduces that shape.
func TestExcludedUploadsAreHiddenInAWorktreeInsideTheProject(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")
	writeFile(t, repoDir, "README.md", "# Test\n")
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "Initial")

	dbPath := filepath.Join(tmpDir, "test.db")
	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Worktrees dir INSIDE the project, which is the layout that puts the uploads dir below the root the
	// pattern is relative to.
	worktreesDir := filepath.Join(repoDir, ".worktrees")
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgStore := config.NewConfigStore(s, dbPath, "test", nil)
	cfgStore.SetWorkspaceDirOverride(worktreesDir)

	if err := s.CreateProject(&types.Project{ID: "proj-in-repo", Name: "in-repo", Path: repoDir}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(&types.Task{ID: "in-repo-task", ProjectID: "proj-in-repo", Title: "t", Status: types.StatusInProgress}); err != nil {
		t.Fatalf("create task: %v", err)
	}

	wm := NewWorktreeManager(s, cfgStore)
	wtPath, _, err := wm.CreateWorktree("proj-in-repo", "in-repo-task", "task/in-repo-task", "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if err := EnsureExcluded(repoDir, uploadsPattern, "# mike uploads (kept out of task status)"); err != nil {
		t.Fatalf("exclude the uploads dir: %v", err)
	}

	uploads := filepath.Join(wtPath, ".mike-uploads")
	if err := os.MkdirAll(uploads, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, uploads, "report.pdf", "%PDF-1.7\n")

	if rel, err := filepath.Rel(repoDir, uploads); err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("the uploads dir is not below the checkout root, so this fixture proves nothing: %q", rel)
	}
	if out := strings.TrimSpace(runGitOutput(t, wtPath, "status", "--porcelain")); out != "" {
		t.Errorf("the upload shows in git status below the checkout root:\n%s", out)
	}
}

// The four git operations that would otherwise take an uploaded file away, and the restore that runs the
// one that deletes untracked files.
func TestExcludedUploadsDoNotShowInTheTasksStatus(t *testing.T) {
	t.Parallel()
	_, wtPath, _ := excludedUploadWorktree(t, "wt-status")

	if out := strings.TrimSpace(runGitOutput(t, wtPath, "status", "--porcelain")); out != "" {
		t.Errorf("the upload shows in git status, want it invisible:\n%s", out)
	}
}

func TestExcludedUploadsAreNotStagedByAddAll(t *testing.T) {
	t.Parallel()
	_, wtPath, _ := excludedUploadWorktree(t, "wt-add")

	writeFile(t, wtPath, "work.txt", "the agent's own file\n")
	runGitOutput(t, wtPath, "add", "-A")

	staged := strings.TrimSpace(runGitOutput(t, wtPath, "diff", "--cached", "--name-only"))
	if strings.Contains(staged, ".mike-uploads") {
		t.Errorf("git add -A staged the upload, want it skipped:\n%s", staged)
	}
	if !strings.Contains(staged, "work.txt") {
		t.Errorf("git add -A did not stage the file that should have been staged:\n%s", staged)
	}
}

func TestExcludedUploadsAreNotSweptIntoAStash(t *testing.T) {
	t.Parallel()
	_, wtPath, _ := excludedUploadWorktree(t, "wt-stash")

	writeFile(t, wtPath, "README.md", "# changed\n")
	writeFile(t, wtPath, "scratch.txt", "untracked\n")
	runGitOutput(t, wtPath, "stash", "push", "-u", "-m", "wip")

	if _, err := os.Stat(filepath.Join(wtPath, ".mike-uploads", "report.pdf")); err != nil {
		t.Errorf("git stash -u took the upload: %v", err)
	}
}

func TestExcludedUploadsSurviveACleanAndARestore(t *testing.T) {
	t.Parallel()
	_, wtPath, wm := excludedUploadWorktree(t, "wt-clean")

	upload := filepath.Join(wtPath, ".mike-uploads", "report.pdf")
	runGitOutput(t, wtPath, "clean", "-fd")
	if _, err := os.Stat(upload); err != nil {
		t.Errorf("git clean -fd deleted the upload: %v", err)
	}

	// A restore resets index and working tree to a snapshot; the snapshot has no upload in it.
	writeFile(t, wtPath, "work.txt", "committed work\n")
	runGitOutput(t, wtPath, "add", "work.txt")
	runGitOutput(t, wtPath, "commit", "-m", "work")
	snapshot := strings.TrimSpace(runGitOutput(t, wtPath, "rev-parse", "HEAD"))

	writeFile(t, wtPath, "work.txt", "dirty again\n")
	writeFile(t, wtPath, "scratch.txt", "untracked\n")
	if err := wm.ResetWorktreeTo("wt-clean", snapshot); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(upload); err != nil {
		t.Errorf("the checkpoint restore deleted the upload: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "scratch.txt")); err == nil {
		t.Error("the restore kept the untracked file it is supposed to discard — the test proves nothing")
	}
}

// The pattern is unanchored so it reaches worktrees below the checkout root, which means it must still be
// narrow enough to leave a project's own directories alone.
func TestExcludingUploadsLeavesTheProjectsOwnDirectoriesAlone(t *testing.T) {
	t.Parallel()
	_, wtPath, _ := excludedUploadWorktree(t, "wt-naming")

	for _, dir := range []string{"uploads", ".uploads", "mike-uploads"} {
		if err := os.MkdirAll(filepath.Join(wtPath, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(wtPath, dir), "notes.txt", "the project's own file\n")
	}
	out := runGitOutput(t, wtPath, "status", "--porcelain")
	for _, dir := range []string{"uploads", ".uploads", "mike-uploads"} {
		if !strings.Contains(out, "?? "+dir+"/") {
			t.Errorf("%s/ is missing from git status, want only .mike-uploads/ excluded:\n%s", dir, out)
		}
	}
	if strings.Contains(out, ".mike-uploads") {
		t.Errorf("the uploads directory shows in git status, so the exclusion is not holding:\n%s", out)
	}
}
