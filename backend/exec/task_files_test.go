package exec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mike/backend/config"
	"mike/backend/errs"
	"mike/backend/git"
	"mike/backend/sandbox"
	"mike/backend/store"
	"mike/backend/types"
)

// uploadFixture seeds a real git repository, a project (sandboxed or not), a task and its worktree. The
// repository has to be real: where an upload lands and what git makes of it is the behaviour under test.
func uploadFixture(t *testing.T, sandboxed bool) (*TaskExecutor, string, string) {
	t.Helper()

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	worktreesDir := filepath.Join(tmpDir, "worktrees")
	os.MkdirAll(repoDir, 0o755)
	os.MkdirAll(worktreesDir, 0o755)

	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")
	os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# Test\n"), 0o644)
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "Initial")

	dbPath := filepath.Join(tmpDir, "uploads.db")
	s, err := store.NewStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	cfgStore := config.NewConfigStore(s, dbPath, "test", nil, false)
	cfgStore.SetWorkspaceDirOverride(worktreesDir)

	now := time.Now().UTC()
	if err := s.CreateProject(&types.Project{
		ID: "p1", Name: "p1", Path: repoDir, SandboxEnabled: sandboxed, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(&types.Task{
		ID: "t1", ProjectID: "p1", Title: "t1", Prompt: "prompt", Status: types.StatusInProgress,
		ExecutionState: types.ExecStatePaused, CurrentThreadID: "main", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}

	wm := git.NewWorktreeManager(s, cfgStore)
	wtPath, _, err := wm.CreateWorktree("p1", "t1", "task/t1", "")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	// The fixture builds no sandbox manager at all: the write is host-side, so no container is involved.
	return newTestExecutorWith(t, Deps{Store: s, WorktreeMgr: wm}), repoDir, wtPath
}

func hostUpload(t *testing.T, wtPath, name string) string {
	t.Helper()
	return filepath.Join(wtPath, uploadsDirName, name)
}

func gitStatus(t *testing.T, dir string) string {
	t.Helper()
	cmd := exCmd(t, dir, "status", "--porcelain")
	return strings.TrimSpace(cmd)
}

// exCmd runs git and hands back what it printed. The package's runGit fails on a non-zero exit, which is not
// what a status check wants.
func exCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s: %v", args, string(out), err)
	}
	return string(out)
}

func TestWriteTaskFilePutsTheBytesWhereTheTasksActionsRun(t *testing.T) {
	t.Parallel()
	te, _, wtPath := uploadFixture(t, false)

	path, err := te.WriteTaskFile("p1", "t1", "report.pdf", []byte("%PDF-1.7\x00\xff"))
	if err != nil {
		t.Fatalf("WriteTaskFile: %v", err)
	}
	if path != hostUpload(t, wtPath, "report.pdf") {
		t.Errorf("path = %q, want the file under the name the user dropped it under, in the task's worktree", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the agent cannot open the path it was given: %v", err)
	}
	if string(got) != "%PDF-1.7\x00\xff" {
		t.Errorf("bytes = %q, want the bytes that were sent", got)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("uploaded file mode = %v, want no group or other access", info.Mode().Perm())
	}
}

// The task's environment for a sandboxed project is the bind mount, not the host path: the message has to
// name the path the tools open. The fixture builds no sandbox manager, so this also proves no container is
// needed to write through — the bytes go to the host path that mount is backed by.
func TestWriteTaskFileAnswersASandboxedTaskWithItsOwnMountPath(t *testing.T) {
	t.Parallel()
	te, _, wtPath := uploadFixture(t, true)

	path, err := te.WriteTaskFile("p1", "t1", "archive.zip", []byte("PK\x03\x04"))
	if err != nil {
		t.Fatalf("WriteTaskFile: %v", err)
	}
	want := filepath.Join(sandbox.MountPath("p1"), uploadsDirName, "archive.zip")
	if path != want {
		t.Errorf("path = %q, want the guest path %q", path, want)
	}
	if _, err := os.Stat(hostUpload(t, wtPath, "archive.zip")); err != nil {
		t.Errorf("the mount path was answered but nothing is on the host for the guest to read: %v", err)
	}
}

func TestWriteTaskFileGivesTwoUploadsOfOneNameTheirOwnPaths(t *testing.T) {
	t.Parallel()
	te, _, wtPath := uploadFixture(t, false)

	first, err := te.WriteTaskFile("p1", "t1", "data.csv", []byte("a,b\n1,2\n"))
	if err != nil {
		t.Fatalf("first upload: %v", err)
	}
	second, err := te.WriteTaskFile("p1", "t1", "data.csv", []byte("c,d\n3,4\n"))
	if err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if first == second {
		t.Fatalf("both uploads answered %q: the second would overwrite the bytes the first message names", first)
	}
	if second != hostUpload(t, wtPath, "data-1.csv") {
		t.Errorf("second = %q, want the suffixed name in the same directory", second)
	}
	for path, want := range map[string]string{first: "a,b\n1,2\n", second: "c,d\n3,4\n"} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(got) != want {
			t.Errorf("%s holds %q, want %q", path, got, want)
		}
	}
}

// One write declares the exclusion, which is what keeps the upload out of the task's status. What git then
// cannot do to an excluded directory — `add -A`, `stash -u`, `clean -fd`, a restore — is owned by
// backend/git's exclude tests, which assert it against the pattern directly.
func TestWriteTaskFileKeepsTheUploadOutOfGit(t *testing.T) {
	t.Parallel()
	te, _, wtPath := uploadFixture(t, false)

	if _, err := te.WriteTaskFile("p1", "t1", "payload.bin", []byte("x")); err != nil {
		t.Fatalf("WriteTaskFile: %v", err)
	}
	if out := gitStatus(t, wtPath); out != "" {
		t.Errorf("the upload shows in the task's git status:\n%s", out)
	}
}

func TestWriteTaskFileRefusesANameThatIsNotASingleFileName(t *testing.T) {
	t.Parallel()
	te, _, wtPath := uploadFixture(t, false)

	for _, name := range []string{"../../etc/passwd", "../escape.txt", "a/b.txt", "/abs/path.txt", "."} {
		if path, err := te.WriteTaskFile("p1", "t1", name, []byte("x")); err == nil {
			t.Errorf("WriteTaskFile(%q) answered %q with no error", name, path)
		}
	}
	if _, err := os.Stat(uploadsDir(wtPath)); err == nil {
		t.Error("a refused name still created the uploads directory")
	}
}

func TestWriteTaskFileRefusesAnEmptyFileAndOneOverTheLimit(t *testing.T) {
	t.Parallel()
	te, _, _ := uploadFixture(t, false)

	if _, err := te.WriteTaskFile("p1", "t1", "empty.bin", nil); err == nil {
		t.Error("an empty upload was accepted")
	}
	oversize := make([]byte, MaxUploadBytes+1)
	if _, err := te.WriteTaskFile("p1", "t1", "big.bin", oversize); err == nil {
		t.Error("an upload over the limit was accepted")
	}
}

func TestWriteTaskFileNamesAMissingTaskAsNotFound(t *testing.T) {
	t.Parallel()
	te, _, _ := uploadFixture(t, false)

	if _, err := te.WriteTaskFile("p1", "no-such-task", "note.txt", []byte("x")); !errs.IsNotFound(err) {
		t.Errorf("error = %v, want not-found for a task that is not there", err)
	}
}

// A task without a worktree is refused rather than served by preparing one, which would create a branch and
// rewrite the task row as a side effect of a paste.
func TestWriteTaskFileRefusesATaskWithoutAWorktree(t *testing.T) {
	t.Parallel()
	te, _, wtPath := uploadFixture(t, true)

	if err := os.RemoveAll(wtPath); err != nil {
		t.Fatal(err)
	}
	_, err := te.WriteTaskFile("p1", "t1", "report.pdf", []byte("x"))
	if !errs.IsConflict(err) {
		t.Fatalf("error = %v, want a conflict naming the task's environment", err)
	}
	if !strings.Contains(errs.Message(err), "start the task") {
		t.Errorf("message = %q, want the sentence that says what to do", errs.Message(err))
	}
}

// An executor built as a struct literal has no worktree manager; the answer is the same refusal, not a panic.
func TestWriteTaskFileRefusesWhenThereIsNoWorktreeManager(t *testing.T) {
	t.Parallel()
	te, _, _ := uploadFixture(t, false)
	te.worktreeMgr = nil

	if _, err := te.WriteTaskFile("p1", "t1", "report.pdf", []byte("x")); !errs.IsConflict(err) {
		t.Errorf("error = %v, want a conflict, not a panic over a nil manager", err)
	}
}

// A symlink planted where the uploads directory belongs must not be followed out of the worktree — the
// task's own actions can create it.
func TestWriteTaskFileDoesNotWriteThroughASymlinkedUploadsDir(t *testing.T) {
	t.Parallel()
	te, _, wtPath := uploadFixture(t, false)

	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, uploadsDir(wtPath)); err != nil {
		t.Fatal(err)
	}

	path, err := te.WriteTaskFile("p1", "t1", "report.pdf", []byte("x"))
	if err == nil {
		t.Fatalf("the write followed the symlink and answered %q", path)
	}
	if _, err := os.Stat(filepath.Join(outside, "report.pdf")); err == nil {
		t.Error("bytes landed outside the task's worktree")
	}
}
