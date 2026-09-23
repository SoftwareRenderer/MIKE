package exec

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"mike/backend/fsutil"
	"mike/backend/types"
)

// hostWriter is the writer a host tool call dispatches with: the state carries no sandbox callbacks.
func hostWriter(t *testing.T, worktree string) writeFunc {
	t.Helper()
	env := newToolEnv(&executionState{projectName: "proj"}, worktree, &types.Task{}, nil)
	return env.writer()
}

func assertNoAtomicTemps(t *testing.T, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read dir %s: %v", dir, err)
		}
		for _, e := range entries {
			if fsutil.IsAtomicTemp(e.Name()) {
				t.Errorf("%s left a temporary %s behind; it shows up in the task's git status", dir, e.Name())
			}
		}
	}
}

// A tool call can be interrupted while its write is in flight, so the write is visible in one step:
// a refused or half-finished write must leave the bytes that were there, never a truncated file.
func TestHostWrite_RefusedWriteKeepsTheFileBytes(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	w := hostWriter(t, dir)
	if err := w(context.Background(), "link.txt", "replacement"); err == nil {
		t.Fatal("a write through a symlink was accepted; the containment check judged the link, not what it names")
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Errorf("the refused write changed what its target holds: got %q, want %q", got, "original")
	}
	assertNoAtomicTemps(t, dir)
}

func TestHostWrite_LeavesNoTempAndMakesParents(t *testing.T) {
	dir := t.TempDir()
	w := hostWriter(t, dir)

	if err := w(context.Background(), filepath.Join("nested", "deep", "note.txt"), "hello"); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "nested", "deep", "note.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("wrote %q, want %q", got, "hello")
	}
	assertNoAtomicTemps(t, dir, filepath.Join(dir, "nested"), filepath.Join(dir, "nested", "deep"))
}

// edit_file builds its whole result before writing, so an interrupted edit leaves the file as the
// read found it; only the write itself needs the atomic step.
func TestHostEdit_RefusedReplacementKeepsTheFileBytes(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "code.txt")
	if err := os.WriteFile(target, []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "code-link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	r := newBareTestExecutor(t).tools
	env := newToolEnv(&executionState{projectName: "proj"}, dir, &types.Task{}, nil)
	args := map[string]any{
		"path":  "code-link.txt",
		"edits": []any{map[string]any{"oldText": "alpha", "newText": "beta"}},
	}
	if _, ok := r.runEditFile(context.Background(), args, env); ok {
		t.Fatal("an edit through a symlink was accepted")
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "alpha\n" {
		t.Errorf("the refused edit changed what its target holds: got %q, want %q", got, "alpha\n")
	}
	assertNoAtomicTemps(t, dir)
}
