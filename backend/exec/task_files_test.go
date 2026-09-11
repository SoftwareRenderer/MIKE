package exec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kanban/backend/errs"
	"kanban/backend/store"
	"kanban/backend/types"
	"kanban/pkg/bridge/protocol"
)

// uploadStore seeds one project and one task, with TMPDIR redirected so a host upload lands in the
// test's own directory instead of the machine's /tmp.
func uploadStore(t *testing.T, sandboxed bool) *store.Store {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())

	s, err := store.NewStore(filepath.Join(t.TempDir(), "uploads.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	now := time.Now().UTC()
	if err := s.CreateProject(&types.Project{
		ID: "p1", Name: "p1", Path: "/tmp/p1", SandboxEnabled: sandboxed, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := s.CreateTask(&types.Task{
		ID: "t1", ProjectID: "p1", Title: "t1", Prompt: "prompt", Status: types.StatusBacklog,
		ExecutionState: types.ExecStateIdle, CurrentThreadID: "main", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := s.CreateThread(&types.TaskThread{ID: "main", TaskID: "t1", Name: "Main", CreatedAt: now}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	return s
}

func uploadHostRoot(t *testing.T) string {
	t.Helper()
	root := os.TempDir()
	if root == "/tmp" {
		t.Fatal("TMPDIR was not redirected: this test would write to the machine's /tmp")
	}
	return root
}

func TestWriteTaskFilePutsTheBytesWhereTheTasksActionsRun(t *testing.T) {
	te := &TaskExecutor{store: uploadStore(t, false)}

	path, err := te.WriteTaskFile(t.Context(), "p1", "t1", "report.pdf", []byte("%PDF-1.7\x00\xff"))
	if err != nil {
		t.Fatalf("WriteTaskFile: %v", err)
	}
	if filepath.Base(path) != "report.pdf" {
		t.Errorf("path = %q, want the file under the name the user dropped it under", path)
	}
	if !strings.HasPrefix(path, uploadHostRoot(t)) {
		t.Errorf("path %q is outside the environment this task's actions run in", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the agent cannot open the path it was given: %v", err)
	}
	if string(got) != "%PDF-1.7\x00\xff" {
		t.Errorf("bytes changed: %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode(); mode&0o077 != 0 {
		t.Errorf("mode = %v, want owner-only: one person's clipboard in a shared /tmp", mode)
	}
}

func TestWriteTaskFileGivesTwoUploadsOfOneNameTheirOwnPaths(t *testing.T) {
	te := &TaskExecutor{store: uploadStore(t, false)}

	first, err := te.WriteTaskFile(t.Context(), "p1", "t1", "data.csv", []byte("a,b\n1,2\n"))
	if err != nil {
		t.Fatalf("first upload: %v", err)
	}
	second, err := te.WriteTaskFile(t.Context(), "p1", "t1", "data.csv", []byte("c,d\n3,4\n"))
	if err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if first == second {
		t.Fatalf("both uploads answered %q; the first message would name the second file's bytes", first)
	}
	for _, want := range []struct {
		path, body string
	}{{first, "a,b\n1,2\n"}, {second, "c,d\n3,4\n"}} {
		got, err := os.ReadFile(want.path)
		if err != nil {
			t.Fatalf("read %s: %v", want.path, err)
		}
		if string(got) != want.body {
			t.Errorf("%s holds %q, want %q", want.path, got, want.body)
		}
	}
}

func TestWriteTaskFileRefusesANameThatIsNotASingleFileName(t *testing.T) {
	te := &TaskExecutor{store: uploadStore(t, false)}

	for _, name := range []string{
		"../escape", "a/b.txt", "/etc/passwd", `back\slash`, "new\nline", "", ".", "..",
		strings.Repeat("x", 300) + ".txt",
	} {
		if path, err := te.WriteTaskFile(t.Context(), "p1", "t1", name, []byte("x")); err == nil {
			t.Errorf("WriteTaskFile(%q) answered %q with no error", name, path)
		}
	}

	entries, err := os.ReadDir(uploadHostRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a refused upload still wrote: %v", entries)
	}
}

func TestWriteTaskFileRefusesAnEmptyFileAndOneOverTheBridgesLimit(t *testing.T) {
	te := &TaskExecutor{store: uploadStore(t, false)}

	if _, err := te.WriteTaskFile(t.Context(), "p1", "t1", "empty.bin", nil); err == nil {
		t.Error("an empty upload was accepted")
	}

	oversize := make([]byte, protocol.MaxWriteBytes+1)
	if _, err := te.WriteTaskFile(t.Context(), "p1", "t1", "big.bin", oversize); err == nil {
		t.Error("an upload over the bridge's frame limit was accepted")
	}
	entries, err := os.ReadDir(uploadHostRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a refused upload still wrote: %v", entries)
	}
}

func TestWriteTaskFileNamesAMissingTaskAsNotFound(t *testing.T) {
	te := &TaskExecutor{store: uploadStore(t, false)}

	if _, err := te.WriteTaskFile(t.Context(), "p1", "no-such-task", "note.txt", []byte("x")); !errs.IsNotFound(err) {
		t.Errorf("error = %v, want it to still answer not-found", err)
	}
}

func TestSandboxedUploadWithoutARunningContainerWritesNothingOnTheHost(t *testing.T) {
	te := &TaskExecutor{store: uploadStore(t, true)}

	_, err := te.WriteTaskFile(t.Context(), "p1", "t1", "archive.zip", []byte("PK\x03\x04"))
	if err == nil {
		t.Fatal("a sandboxed task with no sandbox answered a path")
	}
	if !errs.IsConflict(err) {
		t.Errorf("error = %v, want a conflict the browser can act on", err)
	}
	if strings.Contains(err.Error(), "/tmp/") {
		t.Errorf("the refusal leaks a path: %v", err)
	}
	entries, readErr := os.ReadDir(uploadHostRoot(t))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Errorf("a sandboxed upload wrote to the host: %v", entries)
	}
}

func TestRemoveTaskUploadsTakesOnlyTheDeletedTasks(t *testing.T) {
	te := &TaskExecutor{store: uploadStore(t, false)}
	root := uploadHostRoot(t)

	if _, err := te.writeHostUpload("t1", "one.txt", []byte("1")); err != nil {
		t.Fatal(err)
	}
	if _, err := te.writeHostUpload("t12", "two.txt", []byte("2")); err != nil {
		t.Fatal(err)
	}

	te.removeTaskUploads("t1")

	for _, want := range []struct{ task, missing string }{{"t1", ""}, {"t12", "two.txt"}} {
		dirs, err := filepath.Glob(filepath.Join(root, "kanban-uploads-"+want.task+"-*"))
		if err != nil {
			t.Fatal(err)
		}
		if want.missing == "" {
			if len(dirs) != 0 {
				t.Errorf("task %s still has %v after its deletion", want.task, dirs)
			}
			continue
		}
		if len(dirs) != 1 {
			t.Fatalf("task %s should have kept its upload directory, found %v", want.task, dirs)
		}
		if _, err := os.Stat(filepath.Join(dirs[0], want.missing)); err != nil {
			t.Errorf("the other task's upload was taken too: %v", err)
		}
	}

	te.removeTaskUploads("*")
	remaining, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 {
		t.Errorf("an ID that is a pattern reached every task's uploads: %v", remaining)
	}
}
