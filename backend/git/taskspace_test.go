package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mike/backend/store"
	"mike/backend/types"

	"mike/pkg/logging"
)

// A worktree is files on disk here; building a real one needs a repository, and the figure does not
// care how the bytes arrived.

// taskTree puts a file of n bytes at <root>/<projectID>/<taskID>/<name>.
func taskTree(t *testing.T, root, projectID, taskID, name string, n int) string {
	t.Helper()
	dir := filepath.Join(root, projectID, taskID)
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return dir
}

// addProject gives a test a second project row, so a directory on disk can be told apart from one the
// server owns.
func addProject(t *testing.T, s *store.Store, id string) {
	t.Helper()
	if err := s.CreateProject(&types.Project{ID: id, Name: id, Path: t.TempDir()}); err != nil {
		t.Fatalf("create project %s: %v", id, err)
	}
}

func TestProjectTaskSpacePricesOnlyThatProjectsCheckouts(t *testing.T) {
	_, _, s, cfgStore, wm := setupTestRepo(t)
	addProject(t, s, "other-project")
	worktreesDir := cfgStore.WorkspaceDir()
	taskTree(t, worktreesDir, "test-project", "test-task", "a.txt", 100)
	taskTree(t, worktreesDir, "test-project", "other-task", filepath.Join("nested", "b.txt"), 40)
	// A worktree's .git is a pointer file, and the file at the top of one is content like any other.
	taskTree(t, worktreesDir, "test-project", "test-task", ".git", 64)
	// A checkout of another project, and a file that is not a task directory, are both out of this figure.
	taskTree(t, worktreesDir, "other-project", "their-task", "theirs.txt", 5<<20)
	if err := os.WriteFile(filepath.Join(worktreesDir, "test-project", "loose.txt"), make([]byte, 9), 0o644); err != nil {
		t.Fatal(err)
	}

	usage, ok := wm.MeasureProjectTaskSpace("test-project")
	if !ok {
		t.Fatal("the measurement refused a readable root")
	}
	if usage.Worktrees != 2 {
		t.Fatalf("worktrees = %d, want the two task directories of this project", usage.Worktrees)
	}
	if usage.Bytes != 204 {
		t.Fatalf("bytes = %d, want 204 (100 + 64 + 40) and no byte of the other project's tree", usage.Bytes)
	}
}

// MIKE's build scratch tree shares the worktrees root; a directory no project row names is not a
// task, and its bytes are nobody's figure.
func TestProjectTaskSpacePricesNothingUnderADirectoryNoProjectRowNames(t *testing.T) {
	_, _, _, cfgStore, wm := setupTestRepo(t)
	worktreesDir := cfgStore.WorkspaceDir()
	taskTree(t, worktreesDir, "build-scratch", "build-1", "layer.tar", 5<<20)
	taskTree(t, worktreesDir, "deleted-project", "old-task", "stale.txt", 5<<20)

	for _, id := range []string{"build-scratch", "deleted-project"} {
		usage, ok := wm.MeasureProjectTaskSpace(id)
		if !ok {
			t.Fatalf("a directory with no project row is a figure of zero, not a refusal (%s)", id)
		}
		if usage.Bytes != 0 || usage.Worktrees != 0 {
			t.Fatalf("usage for %s = %+v, want zero", id, usage)
		}
	}
}

// A project directory the server has not written to yet is a project with no tasks, not an unreadable
// one.
func TestProjectTaskSpaceAnswersZeroForADirectoryThatDoesNotExist(t *testing.T) {
	_, _, s, _, wm := setupTestRepo(t)
	addProject(t, s, "untouched-project")

	usage, ok := wm.MeasureProjectTaskSpace("untouched-project")
	if !ok {
		t.Fatal("a missing project directory is a figure of zero, not a refusal")
	}
	if usage.Bytes != 0 || usage.Worktrees != 0 {
		t.Fatalf("usage = %+v, want zero", usage)
	}
}

// A total missing one checkout would understate the project, so the walk answers no figure at all.
func TestProjectTaskSpaceRefusesAnUnpricedCheckoutOfItsOwnProject(t *testing.T) {
	_, _, _, cfgStore, wm := setupTestRepo(t)
	worktreesDir := cfgStore.WorkspaceDir()
	taskTree(t, worktreesDir, "test-project", "test-task", "a.txt", 10)
	// A task directory the walk cannot read is one whose bytes are unknown.
	unreadable := taskTree(t, worktreesDir, "test-project", "locked-task", "secret.txt", 10)
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o755) })
	if _, err := os.ReadDir(unreadable); err == nil {
		t.Skip("this host reads a 0o000 directory anyway (running as root)")
	}

	if usage, ok := wm.MeasureProjectTaskSpace("test-project"); ok {
		t.Fatalf("TaskWorktreeUsage answered %+v over a tree it cannot read", usage)
	}
}

// One project's unpriceable checkout is that project's own problem. A board asking about another project
// is owed a figure, not that refusal.
func TestProjectTaskSpaceIgnoresAnUnpricedCheckoutOfAnotherProject(t *testing.T) {
	_, _, s, cfgStore, wm := setupTestRepo(t)
	addProject(t, s, "other-project")
	worktreesDir := cfgStore.WorkspaceDir()
	taskTree(t, worktreesDir, "test-project", "test-task", "a.txt", 10)
	unreadable := taskTree(t, worktreesDir, "other-project", "locked-task", "secret.txt", 10)
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o755) })
	if _, err := os.ReadDir(unreadable); err == nil {
		t.Skip("this host reads a 0o000 directory anyway (running as root)")
	}

	usage, ok := wm.MeasureProjectTaskSpace("test-project")
	if !ok {
		t.Fatal("another project's unreadable checkout refused this project's figure")
	}
	if usage.Bytes != 10 || usage.Worktrees != 1 {
		t.Fatalf("usage = %+v, want this project's 10 bytes in 1 worktree", usage)
	}
}

// The measurement is retried on every refresh, so the refusal reaches the log once per manager, not
// once per retry.
func TestAnUnreadableRootRefusesEveryRefreshAndWarnsOnce(t *testing.T) {
	_, _, _, cfgStore, wm := setupTestRepo(t)
	worktreesDir := cfgStore.WorkspaceDir()
	if err := os.RemoveAll(worktreesDir); err != nil {
		t.Fatalf("remove root: %v", err)
	}
	if err := os.WriteFile(worktreesDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf, restore := logging.TestSink()
	defer restore()
	for range 3 {
		if usage, ok := wm.MeasureProjectTaskSpace("test-project"); ok {
			t.Fatalf("TaskWorktreeUsage measured a root that is a plain file: %+v", usage)
		}
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	reported := 0
	for _, line := range lines {
		if strings.Contains(line, "measuring the task worktrees") {
			reported++
		}
	}
	if reported != 1 {
		t.Fatalf("the refusal was logged %d times over three refusals, want once:\n%s", reported, buf.String())
	}
	if !strings.Contains(buf.String(), worktreesDir) {
		t.Fatalf("the refusal does not name the root:\n%s", buf.String())
	}
}
