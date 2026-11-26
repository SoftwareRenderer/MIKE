package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWrite holds what both halves of the bridge rely on: the bytes land complete, the count says so,
// and the temporary file survives no path — a leftover is what a task's `git status` shows and a
// `git add -A` commits.
func TestWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	content := "atomic write test\n"

	n, err := Write(path, []byte(content), 0o644)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(content) {
		t.Errorf("wrote %d bytes, reported %d", len(content), n)
	}
	if _, err := Write(path, []byte("shorter\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "shorter\n" {
		t.Errorf("after a shorter rewrite: %q", data)
	}
	if leftovers := temps(t, dir); len(leftovers) != 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}

	missing := filepath.Join(dir, "no-such-dir", "file.txt")
	if _, err := Write(missing, []byte("x"), 0o644); err == nil {
		t.Error("Write into a missing directory succeeded")
	}
	if fi, err := os.Stat(filepath.Join(dir, "no-such-dir")); err == nil && fi.IsDir() {
		t.Error("a failed write created a directory the caller never asked for")
	}
}

// TestWriteDoesNotTakeModeFromALink: the rename replaces a link at the name rather than writing
// through it, and the link's own bits (0777 on Linux) do not become the mode of the new file.
func TestWriteDoesNotTakeModeFromALink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "victim")
	if err := os.WriteFile(target, []byte("do not touch\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	path := filepath.Join(dir, "out")
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, err := Write(path, []byte("content\n"), 0o644); err != nil {
		t.Fatalf("Write: %v", err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if !fi.Mode().IsRegular() {
		t.Errorf("mode = %v, want a regular file replacing the link", fi.Mode())
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want the 0644 the caller asked for", fi.Mode().Perm())
	}
	if kept, err := os.ReadFile(target); err != nil || string(kept) != "do not touch\n" {
		t.Errorf("the link's target changed: %q (%v)", kept, err)
	}
}

// TestIsTemp is the prefix contract: the guest names a mid-write file this and the host's filters drop
// it by the same constant. backend/stream/atomic_write_watch_test.go reads it from the other side.
func TestIsTemp(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want bool
	}{
		{TempPrefix + "foo.go-1234", true},
		{"/work/proj/" + TempPrefix + "foo.go-1234", true},
		{TempPrefix + "notes.md", true},
		// Names a person could plausibly have are not ours.
		{"foo.go", false},
		{".mike-tmp", false},
		{"mike-tmp-foo.go-1", false},
		{"foo.mike-tmp.go", false},
	}
	for _, tc := range cases {
		if got := IsTemp(tc.path); got != tc.want {
			t.Errorf("IsTemp(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func temps(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), TempPrefix) {
			out = append(out, e.Name())
		}
	}
	return out
}
