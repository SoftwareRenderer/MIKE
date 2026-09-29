package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"mike/backend/startup"
)

// TestFSWriteRefusesSymlinkedParent (security): a task owns its worktree and can swap a
// directory for a link before the parent chain is created, so the write lands where the link
// points unless parent creation is contained the same way the path was.
func TestFSWriteRefusesSymlinkedParent(t *testing.T) {
	s := testServer(t)
	projectDir := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(projectDir, "swapped")); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	target := fsTarget{
		path:  filepath.Join(projectDir, "swapped", "deep", "payload.txt"),
		roots: []string{projectDir},
	}
	if s.writeContainedFile(rr, target, []byte("written through a link")) {
		t.Fatal("a write through a symlinked parent directory was accepted")
	}
	if rr.Code != 403 {
		t.Errorf("status = %d, want 403", rr.Code)
	}
	if _, err := os.Stat(filepath.Join(outside, "deep")); !os.IsNotExist(err) {
		t.Fatal("the write created a directory outside the project")
	}
	if _, err := os.Stat(filepath.Join(outside, "payload.txt")); !os.IsNotExist(err) {
		t.Fatal("the write landed outside the project")
	}
}

// TestFSWriteCreatesNewDirectoriesInsideProject is the control: the walk refuses a link, not a
// save.
func TestFSWriteCreatesNewDirectoriesInsideProject(t *testing.T) {
	s := testServer(t)
	projectDir := t.TempDir()

	rr := httptest.NewRecorder()
	target := fsTarget{
		path:  filepath.Join(projectDir, "a", "b", "new.txt"),
		roots: []string{projectDir},
	}
	if !s.writeContainedFile(rr, target, []byte("saved")) {
		t.Fatalf("an ordinary nested save was refused: %d", rr.Code)
	}
	got, err := os.ReadFile(filepath.Join(projectDir, "a", "b", "new.txt"))
	if err != nil || string(got) != "saved" {
		t.Fatalf("the file is not there: %q %v", got, err)
	}
}

// TestFSWriteIsNotWidenedByAllowRootFS: the widening that reaches the picker never reaches a
// project-scoped write — the server below was built widened, and the write is still jailed to the
// roots the request resolution named.
func TestFSWriteIsNotWidenedByAllowRootFS(t *testing.T) {
	s := testServerWith(t, func(c *startup.Config) { c.AllowRootFS = true })
	projectDir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(projectDir, "swapped")); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	target := fsTarget{
		path:  filepath.Join(projectDir, "swapped", "payload.txt"),
		roots: []string{projectDir},
	}
	if s.writeContainedFile(rr, target, []byte("x")) {
		t.Fatal("MIKE_ALLOW_ROOT_FS widened a project-scoped write")
	}
}
