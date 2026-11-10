package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestTaskWorktreeUsagePricesEveryTaskCheckout(t *testing.T) {
	_, _, _, cfgStore, wm := setupTestRepo(t)
	worktreesDir := cfgStore.WorkspaceDir()
	taskTree(t, worktreesDir, "test-project", "test-task", "a.txt", 100)
	taskTree(t, worktreesDir, "test-project", "other-task", filepath.Join("nested", "b.txt"), 40)
	// A worktree's .git is a pointer file, and the file at the top of one is content like any other.
	taskTree(t, worktreesDir, "test-project", "test-task", ".git", 64)
	// Not a task directory, and not under a project either.
	if err := os.WriteFile(filepath.Join(worktreesDir, "test-project", "loose.txt"), make([]byte, 9), 0o644); err != nil {
		t.Fatal(err)
	}

	usage, ok := wm.MeasureTaskSpace()
	if !ok {
		t.Fatal("the measurement refused a readable root")
	}
	if usage.Worktrees != 2 {
		t.Fatalf("worktrees = %d, want the two task directories", usage.Worktrees)
	}
	if usage.Bytes != 204 {
		t.Fatalf("bytes = %d, want 204 (100 + 64 + 40)", usage.Bytes)
	}
}

// MIKE's build scratch tree shares the worktrees root; a directory no project row names is not a
// task, and its bytes are not the tasks'.
func TestTaskWorktreeUsageSkipsWhatNoProjectRowNames(t *testing.T) {
	_, _, _, cfgStore, wm := setupTestRepo(t)
	worktreesDir := cfgStore.WorkspaceDir()
	taskTree(t, worktreesDir, "test-project", "test-task", "a.txt", 10)
	taskTree(t, worktreesDir, "build-scratch", "build-1", "layer.tar", 5<<20)
	taskTree(t, worktreesDir, "deleted-project", "old-task", "stale.txt", 5<<20)

	usage, ok := wm.MeasureTaskSpace()
	if !ok {
		t.Fatal("the measurement refused a readable root")
	}
	if usage.Worktrees != 1 || usage.Bytes != 10 {
		t.Fatalf("usage = %+v, want only the task under a project that exists", usage)
	}
}

// A root the server has not written to yet is a machine with no tasks, not an unreadable one.
func TestTaskWorktreeUsageAnswersZeroForARootThatDoesNotExist(t *testing.T) {
	_, _, _, cfgStore, wm := setupTestRepo(t)
	missing := filepath.Join(t.TempDir(), "worktrees-not-yet")
	cfgStore.SetWorkspaceDirOverride(missing)
	if _, err := os.ReadDir(missing); !os.IsNotExist(err) {
		t.Fatalf("setup: %s exists", missing)
	}

	usage, ok := wm.MeasureTaskSpace()
	if !ok {
		t.Fatal("a missing root is a figure of zero, not a refusal")
	}
	if usage.Bytes != 0 || usage.Worktrees != 0 {
		t.Fatalf("usage = %+v, want zero", usage)
	}
}

// Half a total is not a total: an unpriced checkout means the figure is not shown at all.
func TestTaskWorktreeUsageRefusesAnUnpricedCheckout(t *testing.T) {
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

	if usage, ok := wm.MeasureTaskSpace(); ok {
		t.Fatalf("TaskWorktreeUsage answered %+v over a tree it cannot read", usage)
	}
}

// The measurement is retried on every refresh, so the refusal reaches the log once per manager, not
// once per retry.
func TestAnUnreadableRootRefusesEveryRefreshAndWarnsOnce(t *testing.T) {
	_, _, _, cfgStore, wm := setupTestRepo(t)
	worktreesDir := cfgStore.WorkspaceDir()
	if err := os.Remove(worktreesDir); err != nil {
		t.Fatalf("remove root: %v", err)
	}
	if err := os.WriteFile(worktreesDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf, restore := logging.TestSink()
	defer restore()
	for range 3 {
		if usage, ok := wm.MeasureTaskSpace(); ok {
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
